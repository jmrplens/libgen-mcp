//go:build httpe2e

package httpe2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The harness above starts the binary on a TCP port and reaches it with the
// default client. --http now also takes a filesystem path, and --tls-cert makes
// the process terminate TLS itself, so both the address a request is written to
// and the transport that carries it became variable. Everything that varies
// lives here; the case bodies stay the same as the ones aimed at a TCP port,
// which is the point — a handler chain that behaved differently per transport
// would be a defect, so the tests are written so that noticing one is possible.

// socketPathLimit is the longest socket path these tests will build.
//
// A unix address is a fixed-size field in the kernel (sun_path: 108 bytes on
// Linux, 104 on darwin), and a path over it does not fail with a clear message
// — it fails at bind, in a server the harness would then report as "never
// became healthy". t.TempDir names the directory after the test, so a long test
// name is all it takes; failing here says so directly.
const socketPathLimit = 100

// socketPath returns a socket path inside the test's own temporary directory.
//
// t.TempDir is not a convenience here. The harness stops a server by canceling
// its exec.CommandContext, which SIGKILLs: a killed process never unlinks its
// socket, so the file outlives the test and a fixed path would fail the next
// run's bind with "address already in use". A per-test directory that is
// removed afterwards makes that impossible rather than unlikely.
func socketPath(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "mcp.sock")
	if len(path) > socketPathLimit {
		t.Fatalf("socket path is %d bytes (%q); shorten the test name so it fits in sun_path", len(path), path)
	}
	return path
}

// unixClient reaches a server over a unix socket.
//
// The URL still needs a host, and it is never resolved: DialContext ignores the
// address it is handed and connects to the socket instead, which is how a
// request written like any other ends up on a file. The idle connections are
// closed with the test because the server they point at is about to be killed.
func unixClient(t *testing.T, path string) *http.Client {
	t.Helper()

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

// startUnixServer starts the binary on a socket of its own choosing.
func startUnixServer(t *testing.T, env map[string]string, flags ...string) *server {
	t.Helper()
	return startUnixServerAt(t, socketPath(t), env, flags...)
}

// startUnixServerAt is startUnixServer with the socket path chosen by the
// caller, for cases about the file itself — its mode, or what the server does
// when something is already there.
func startUnixServerAt(t *testing.T, path string, env map[string]string, flags ...string) *server {
	t.Helper()

	// "unix" is a placeholder host, present because a URL needs one; the client
	// above never resolves it. It reads clearly in a failure message, which is
	// the only job it has.
	return launchServer(t, "http://unix", unixClient(t, path), env, append([]string{"--http", path}, flags...))
}

// tlsPair is a generated certificate and key on disk, with the pool a client
// needs to trust them.
type tlsPair struct {
	certFile string
	keyFile  string
	pool     *x509.CertPool
}

// generateTLSPair writes a self-signed certificate and key for 127.0.0.1 into
// the test's temporary directory.
//
// In process rather than shelling out to openssl: this suite is a release gate
// that runs on every PR, so it must depend on nothing that is not in the Go
// standard library. The certificate is its own issuer, which is why the same
// DER goes into the client's root pool — there is no CA to keep.
func generateTLSPair(t *testing.T) tlsPair {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating the key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generating the serial number: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "libgen-mcp httpe2e"},
		// Backdated by an hour so a clock that disagrees with the runner's by a
		// few seconds cannot make a freshly issued certificate not-yet-valid.
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		// The server is reached as 127.0.0.1, and a name in the URL is verified
		// against the SANs and nothing else: a certificate without the address
		// in it would fail the handshake for a reason unrelated to what is
		// being tested.
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:    []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating the certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshaling the key: %v", err)
	}

	dir := t.TempDir()
	pair := tlsPair{
		certFile: filepath.Join(dir, "cert.pem"),
		keyFile:  filepath.Join(dir, "key.pem"),
		pool:     x509.NewCertPool(),
	}
	writePEM(t, pair.certFile, "CERTIFICATE", der)
	writePEM(t, pair.keyFile, "PRIVATE KEY", keyDER)

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing the certificate back: %v", err)
	}
	pair.pool.AddCert(cert)
	return pair
}

// writePEM writes one PEM block, owner-only: the key is a private key, and the
// certificate keeps the same mode so neither is the odd one out.
func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()

	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// tlsClient reaches a server whose certificate is the generated one.
//
// ForceAttemptHTTP2 is required, not optional: a Transport carrying a
// TLSClientConfig of its own does not enable HTTP/2 unless it is set, so
// without it every reply would come back HTTP/1.1 and the case asserting h2
// would fail on the client's configuration rather than the server's.
func tlsClient(t *testing.T, pair tlsPair) *http.Client {
	t.Helper()

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:    pair.pool,
			MinVersion: tls.VersionTLS12,
		},
		ForceAttemptHTTP2: true,
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

// startTLSServer starts the binary terminating TLS on a loopback port with a
// generated certificate, and returns a harness that trusts it.
func startTLSServer(t *testing.T, env map[string]string, flags ...string) *server {
	t.Helper()

	pair := generateTLSPair(t)
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	args := append([]string{"--http", addr, "--tls-cert", pair.certFile, "--tls-key", pair.keyFile}, flags...)
	return launchServer(t, "https://"+addr, tlsClient(t, pair), env, args)
}

// assertToolsListed checks that a reply to toolsListBody is a real MCP result
// and not merely a 200.
//
// Both new listeners are exercised with a tool call rather than a health probe
// because the probe is a two-line handler that would answer over anything: the
// question is whether the MCP handler behind the whole chain is reachable on
// this transport, and only a JSON-RPC result answers it.
//
// It names one tool rather than the whole surface. Which tools exist is pinned
// where that is the subject; restating it here would make an unrelated surface
// change fail a transport test.
func assertToolsListed(t *testing.T, what string, reply response) {
	t.Helper()

	if reply.status != http.StatusOK {
		t.Fatalf("%s: tools/list = %d, want %d (body: %s)", what, reply.status, http.StatusOK, truncate(reply.body))
	}
	var payload struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	frame := jsonFrame(reply.body)
	if err := json.Unmarshal([]byte(frame), &payload); err != nil {
		t.Fatalf("%s: reply is not JSON-RPC: %v (%s)", what, err, truncate(reply.body))
	}
	names := make([]string, 0, len(payload.Result.Tools))
	for _, tool := range payload.Result.Tools {
		names = append(names, tool.Name)
	}
	if !slices.Contains(names, "search") {
		t.Errorf("%s: tools/list returned %v, want the registered tools", what, names)
	}
}

// jsonFrame pulls the JSON-RPC payload out of a reply in either framing: the
// data line of an SSE event, or the whole body under --json-response.
func jsonFrame(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		if payload, ok := strings.CutPrefix(line, "data:"); ok {
			return strings.TrimSpace(payload)
		}
	}
	return strings.TrimSpace(body)
}
