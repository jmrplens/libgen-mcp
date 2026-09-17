//go:build httpe2e

package httpe2e

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wantHSTS is what a process terminating TLS states about itself: one year, and
// no preload directive — preloading is a decision about a whole domain, which a
// single server behind it has no standing to make.
const wantHSTS = "max-age=31536000; includeSubDomains"

// assertTLSSecurityHeaders is assertConstantSecurityHeaders for the one
// listener that must carry HSTS.
//
// The header is the only part of the chain that depends on how the server was
// started, which is exactly why it needs its own assertion rather than a
// relaxed shared one: a chain that emitted it on every listener would poison a
// developer's browser for localhost, and a chain that emitted it on none would
// quietly drop the promise this listener exists to make.
func assertTLSSecurityHeaders(t *testing.T, what string, h http.Header) {
	t.Helper()

	assertSecurityHeaderSet(t, what, h)
	got := h.Values("Strict-Transport-Security")
	switch {
	case len(got) == 0:
		t.Errorf("%s: no Strict-Transport-Security, want %q: this process terminates TLS", what, wantHSTS)
	case len(got) > 1:
		t.Errorf("%s: Strict-Transport-Security appears %d times (%q), want exactly one", what, len(got), got)
	case got[0] != wantHSTS:
		t.Errorf("%s: Strict-Transport-Security = %q, want %q", what, got[0], wantHSTS)
	}
}

// TestTLS_ServesHTTPSAndNegotiatesHTTP2 is the case --tls-cert exists for, plus
// the one regression it is easy to ship without noticing.
//
// http.Server adds h2 to the ALPN list itself when it is the one calling
// ServeTLS; a listener wrapped with tls.NewListener gets whatever the config
// advertises and nothing more. Omitting NextProtos there breaks no test that
// asks for a status code — every client simply negotiates HTTP/1.1 and
// everything keeps working, slower — so the protocol is asserted directly.
func TestTLS_ServesHTTPSAndNegotiatesHTTP2(t *testing.T) {
	s := startTLSServer(t, nil)

	health := s.do(t, request{method: http.MethodGet, path: "/health"})
	if health.status != http.StatusOK {
		t.Fatalf("GET /health = %d, want %d. Output:\n%s", health.status, http.StatusOK, s.logs())
	}
	// The certificate is verified rather than skipped: it is in the client's
	// root pool and names 127.0.0.1, so a handshake this test accepts is one a
	// real client would accept too.
	if health.proto != "HTTP/2.0" {
		t.Errorf("GET /health came back on %s, want HTTP/2.0: the TLS config is not advertising h2", health.proto)
	}
	assertTLSSecurityHeaders(t, "GET /health over TLS", health.header)

	reply := s.do(t, mcpPOST(nil))
	assertToolsListed(t, "over TLS", reply)
	if reply.proto != "HTTP/2.0" {
		t.Errorf("the MCP POST came back on %s, want HTTP/2.0", reply.proto)
	}

	// The startup line says https, which ln.Addr() alone never would: a TLS
	// listener delegates Addr to the listener it wraps, so the endpoint would
	// otherwise be logged as a plain address.
	if logs := s.logs(); !strings.Contains(logs, "listening on https://") {
		t.Errorf("the startup log does not name the endpoint as https:\n%s", logs)
	}
}

// TestTLS_ARenewalIsServedWithoutARestart is the whole of what the reloader
// buys, against the real binary.
//
// A certificate expires, and with the pair frozen into the config at startup the
// replacement is a restart — which here cuts every download in flight and takes
// the temp cache that would have served the retry with it. The rotation is two
// file writes, and the process that was already serving presents the new
// certificate on the next handshake.
//
// The assertion is the certificate the listener actually presents, not a status
// code: a server that never reloaded would keep answering 200 to a client that
// still trusts the old certificate, which is exactly the state this is here to
// tell apart.
func TestTLS_ARenewalIsServedWithoutARestart(t *testing.T) {
	dir := t.TempDir()
	first := generateTLSPairAt(t, dir)
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	s := launchServer(t, "https://"+addr, tlsClient(t, first), nil,
		[]string{"--http", addr, "--tls-cert", first.certFile, "--tls-key", first.keyFile})

	if got := presentedSerial(t, addr, first.pool); got.Cmp(first.leaf.SerialNumber) != 0 {
		t.Fatalf("the listener presented serial %s, want the configured %s", got, first.leaf.SerialNumber)
	}

	// The second generation lands on the same two paths. The timestamps are
	// moved explicitly: two certificates of this shape can have the same byte
	// count, and a filesystem with coarse timestamps could stamp two writes
	// milliseconds apart identically — which would make the case depend on where
	// it runs rather than on the code.
	second := generateTLSPairAt(t, dir)
	stampForward(t, second.certFile, second.keyFile)

	got := presentedSerial(t, addr, second.pool)
	if got.Cmp(second.leaf.SerialNumber) != 0 {
		t.Fatalf("after the rotation the listener presented serial %s, want the renewed %s. Output:\n%s",
			got, second.leaf.SerialNumber, s.logs())
	}

	// A client that trusts only the old certificate is now refused, which is the
	// other half of the statement: the listener moved rather than presenting
	// both.
	if _, err := dialTLS(t, addr, first.pool); err == nil {
		t.Error("the old certificate still verifies after the rotation; the listener is serving both")
	}

	// And the config the rotation is served through is still the config the
	// listener needs. Dropping NextProtos while moving the pair behind a
	// callback would put every client back on HTTP/1.1, with nothing failing to
	// say so.
	renewed := &server{baseURL: "https://" + addr, client: tlsClient(t, second)}
	health := renewed.do(t, request{method: http.MethodGet, path: "/health"})
	if health.status != http.StatusOK {
		t.Errorf("GET /health after the rotation = %d, want %d", health.status, http.StatusOK)
	}
	if health.proto != "HTTP/2.0" {
		t.Errorf("GET /health after the rotation came back on %s, want HTTP/2.0", health.proto)
	}
	if logs := s.logs(); !strings.Contains(logs, "reloaded the TLS certificate") {
		t.Errorf("the reload is not in the log, so an operator has no record of it:\n%s", logs)
	}
}

// presentedSerial completes a handshake against addr and returns the serial
// number of the leaf the listener presented, failing the test if it cannot.
func presentedSerial(t *testing.T, addr string, pool *x509.CertPool) *big.Int {
	t.Helper()

	state, err := dialTLS(t, addr, pool)
	if err != nil {
		t.Fatalf("handshake with %s: %v", addr, err)
	}
	if len(state.PeerCertificates) == 0 {
		t.Fatalf("the handshake with %s produced no peer certificate", addr)
	}
	return state.PeerCertificates[0].SerialNumber
}

// dialTLS completes a handshake against addr verifying the certificate against
// pool, and returns the connection state or the reason it failed.
//
// The verification is the point: a handshake that accepted anything would say
// only that something answered, where this says which certificate it presented
// and that a real client would have accepted it.
func dialTLS(t *testing.T, addr string, pool *x509.CertPool) (tls.ConnectionState, error) {
	t.Helper()

	dialer := &tls.Dialer{Config: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	conn, err := dialer.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		return tls.ConnectionState{}, err
	}
	defer conn.Close()
	return conn.(*tls.Conn).ConnectionState(), nil //nolint:forcetypeassert // tls.Dialer always returns a *tls.Conn
}

// stampForward moves each file's modification time a second into the future, so
// a rotation is visible to a stat whatever the filesystem's timestamp
// resolution is.
func stampForward(t *testing.T, paths ...string) {
	t.Helper()

	when := time.Now().Add(time.Second)
	for _, path := range paths {
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatalf("stamping %s: %v", path, err)
		}
	}
}

// TestTLS_HandlerChainIsNotTransportDependent asks the TLS listener the
// questions the plain one is already asked.
//
// Only one thing about the chain is meant to change when this process
// terminates TLS — HSTS appears — and everything else must be the answer it
// always was. A 404 or a security header that turned out to depend on the
// listener would stay green in every existing case, all of which run over a
// plain TCP port.
func TestTLS_HandlerChainIsNotTransportDependent(t *testing.T) {
	s := startTLSServer(t, nil)

	reply := s.do(t, mcpPOST(nil))
	if reply.status != http.StatusOK {
		t.Fatalf("tools/list = %d, want %d (body: %s)", reply.status, http.StatusOK, truncate(reply.body))
	}
	assertTLSSecurityHeaders(t, "an MCP POST over TLS", reply.header)
	assertNotCacheable(t, "an MCP POST over TLS", reply.header)

	assertNotFound(t, s, request{method: http.MethodGet, path: "/nope"}, "/")

	// The 404 is written by the chain itself rather than by a handler, so it is
	// where a header set on the way out instead of the way in would go missing.
	assertTLSSecurityHeaders(t, "the 404 over TLS", s.do(t, request{method: http.MethodGet, path: "/nope"}).header)
}

// TestTLS_PlainHTTPToTheTLSPortIsRefused covers the client that got the scheme
// wrong.
//
// It is the mistake every operator makes once, and the answer must be a status
// rather than a hang or a reset — a plaintext request is a malformed TLS record
// to the listener, and the only thing that turns that into something readable
// is http.Server recognizing it and writing a 400 back in the clear.
func TestTLS_PlainHTTPToTheTLSPortIsRefused(t *testing.T) {
	s := startTLSServer(t, nil)

	plain := strings.Replace(s.baseURL, "https://", "http://", 1)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, plain+"/health", http.NoBody)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	// The default client, deliberately: this is the request of someone who does
	// not know the endpoint is TLS, so it carries no certificate pool.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("plain HTTP to the TLS port: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("plain HTTP to the TLS port = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestTLS_StartupRefusals covers the certificate mistakes that must stop the
// process instead of surfacing later.
//
// The pair is loaded eagerly for exactly this reason: left to http.Server, an
// unreadable file or a half-configured pair becomes a handshake that fails on
// the first real request, long after whoever started the server stopped
// watching — and a deployment that believes it is serving TLS and is not is the
// failure the flag exists to prevent.
func TestTLS_StartupRefusals(t *testing.T) {
	pair := generateTLSPair(t)
	missing := filepath.Join(t.TempDir(), "no-such-cert.pem")

	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "a certificate with no key",
			args: []string{"--tls-cert", pair.certFile},
			want: "--tls-cert was given without --tls-key",
		},
		{
			name: "a key with no certificate",
			args: []string{"--tls-key", pair.keyFile},
			want: "--tls-key was given without --tls-cert",
		},
		{
			name: "a certificate file that is not there",
			args: []string{"--tls-cert", missing, "--tls-key", pair.keyFile},
			want: "loading the TLS certificate and key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--http", "127.0.0.1:0"}, tc.args...)
			out, err := runServerExpectingExit(t, args...)
			if err == nil {
				t.Fatalf("the server started anyway. Output:\n%s", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("the refusal does not mention %q:\n%s", tc.want, out)
			}
		})
	}
}
