// listen_test.go covers the HTTP-mode listener: which --http values name a
// filesystem socket rather than a port, the permission mode that socket is bound
// with, the TLS pair this process may terminate itself, and what the startup
// line is able to say about the result.
//
// The certificates are generated per run rather than committed. A PEM pair in
// the repository is a private key in the repository, and one with an expiry is a
// test that starts failing on a date nobody chose.

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io/fs"
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

// headerHSTS is the header this server states only when it terminates TLS
// itself.
const headerHSTS = "Strict-Transport-Security"

// testCert is a self-signed certificate written to disk as a PEM pair, plus the
// pool that trusts it, so a test can drive a real handshake against it.
type testCert struct {
	certFile string
	keyFile  string
	roots    *x509.CertPool
}

// newTestCert generates a short-lived self-signed ECDSA certificate valid for
// 127.0.0.1 and localhost, writes the PEM pair into a per-test directory and
// returns both paths with a pool that trusts the certificate.
//
// The pool is what keeps the handshake tests honest: trusting this one
// certificate proves the listener served it, where InsecureSkipVerify would
// prove only that something answered.
func newTestCert(t *testing.T) testCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "libgen-mcp listener test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	dir := t.TempDir()
	got := testCert{
		certFile: filepath.Join(dir, "cert.pem"),
		keyFile:  filepath.Join(dir, "key.pem"),
		roots:    x509.NewCertPool(),
	}
	writePEM(t, got.certFile, "CERTIFICATE", der)
	writePEM(t, got.keyFile, "PRIVATE KEY", keyDER)
	got.roots.AddCert(leaf)
	return got
}

// writePEM encodes one DER block into a PEM file readable only by its owner.
func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// tempSocketPath returns a unix socket path inside a per-test directory.
//
// The basename is one character on purpose: a unix address is capped at about
// 108 bytes by the kernel, and going over it fails as an opaque bind error
// rather than as anything this package would name.
func tempSocketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "s.sock")
}

// stubLoadTLSKeyPair replaces the certificate loader for one test and restores
// it afterward, so the load-failure branches can be driven without arranging a
// filesystem that fails.
func stubLoadTLSKeyPair(t *testing.T, fn func(certFile, keyFile string) (tls.Certificate, error)) {
	t.Helper()
	old := loadTLSKeyPair
	loadTLSKeyPair = fn
	t.Cleanup(func() { loadTLSKeyPair = old })
}

// listenerFor binds a listener of the given network for a describeListener case:
// a real socket, since the point of that function is what a real listener's Addr
// does and does not say.
func listenerFor(t *testing.T, network string) net.Listener {
	t.Helper()
	addr := "127.0.0.1:0"
	if network == "unix" {
		addr = tempSocketPath(t)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), network, addr)
	if err != nil {
		t.Fatalf("listen %s: %v", network, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// TestIsUnixSocketAddr pins which --http values are read as a filesystem path.
//
// The last case is the deliberate one: a bare "mcp.sock" is indistinguishable
// from a hostname, so it stays a TCP address. Guessing there would bind
// something other than what the operator wrote.
func TestIsUnixSocketAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"/run/mcp.sock", true},
		{"./mcp.sock", true},
		{"/tmp/x/mcp.sock", true},
		{":8080", false},
		{"127.0.0.1:8080", false},
		{"localhost:8080", false},
		{"[::1]:8080", false},
		{"mcp.sock", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			if got := isUnixSocketAddr(tc.addr); got != tc.want {
				t.Errorf("isUnixSocketAddr(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}

// TestParseSocketMode covers the octal reading of --http-socket-mode.
//
// "660" and "0660" must mean the same thing, because that is what they mean to
// chmod; reading the first as decimal would silently produce 0o1224, which is a
// mode nobody asked for and everybody would believe.
func TestParseSocketMode(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		cases := []struct {
			value string
			want  os.FileMode
		}{
			{"", defaultSocketMode},
			{"  ", defaultSocketMode},
			{"0660", 0o660},
			{"660", 0o660},
			{"0o660", 0o660},
			{"600", 0o600},
			{"777", 0o777},
			{"1", 0o1},
		}
		for _, tc := range cases {
			t.Run(tc.value, func(t *testing.T) {
				got, err := parseSocketMode(tc.value)
				if err != nil {
					t.Fatalf("parseSocketMode(%q) = %v, want a mode", tc.value, err)
				}
				if got != tc.want {
					t.Errorf("parseSocketMode(%q) = %#o, want %#o", tc.value, got, tc.want)
				}
			})
		}
	})

	t.Run("refused", func(t *testing.T) {
		// "999" and "1000" are the two halves of the guard: the first is not
		// octal at all, the second is octal and out of range at 0o1000.
		for _, value := range []string{"999", "0", "1000", "abc", "-1", "0o999"} {
			t.Run(value, func(t *testing.T) {
				got, err := parseSocketMode(value)
				if err == nil {
					t.Fatalf("parseSocketMode(%q) = %#o, want an error", value, got)
				}
				// The operator has to be able to see which value was refused,
				// since the flag is often set from a template.
				if !strings.Contains(err.Error(), value) {
					t.Errorf("parseSocketMode(%q) error = %q, want it to name the value", value, err)
				}
			})
		}
	})
}

// TestResolveSocketMode covers the flag against the address actually given: a
// mode is meaningless for a TCP address and unenforceable where chmod carries no
// permission bits, so an explicit value is refused in both cases rather than
// accepted and quietly ignored.
func TestResolveSocketMode(t *testing.T) {
	cases := []struct {
		name     string
		httpAddr string
		value    string
		want     os.FileMode
		wantErr  bool
	}{
		{name: "default with a TCP address", httpAddr: "127.0.0.1:8080", value: "0660", want: defaultSocketMode},
		{name: "default with no address", httpAddr: "", value: "0660", want: defaultSocketMode},
		{name: "default with a socket path", httpAddr: "/run/mcp.sock", value: "0660", want: defaultSocketMode},
		{name: "explicit with a TCP address", httpAddr: "127.0.0.1:8080", value: "0600", wantErr: true},
		{name: "explicit with a bare port", httpAddr: ":8080", value: "0600", wantErr: true},
		{name: "unparseable", httpAddr: "/run/mcp.sock", value: "999", wantErr: true},
		// An explicit mode on a socket path is honored only where it can be:
		// the non-unix builds refuse it rather than promise a mode their chmod
		// does not carry.
		{name: "explicit with a socket path", httpAddr: "/run/mcp.sock", value: "0600", want: 0o600, wantErr: !socketModesEnforced},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveSocketMode(tc.httpAddr, tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveSocketMode(%q, %q) = %#o, want an error", tc.httpAddr, tc.value, got)
				}
				if !strings.Contains(err.Error(), tc.value) {
					t.Errorf("error = %q, want it to name the mode %q", err, tc.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSocketMode(%q, %q) = %v, want %#o", tc.httpAddr, tc.value, err, tc.want)
			}
			if got != tc.want {
				t.Errorf("resolveSocketMode(%q, %q) = %#o, want %#o", tc.httpAddr, tc.value, got, tc.want)
			}
		})
	}
}

// TestValidateTLSFiles covers the both-or-neither rule and the eager load.
//
// The two half-configured cases must not share a message: an operator who wrote
// one flag needs to be told which one is missing, not that a pair is incomplete.
func TestValidateTLSFiles(t *testing.T) {
	cert := newTestCert(t)
	missing := filepath.Join(t.TempDir(), "absent.pem")

	t.Run("neither is fine", func(t *testing.T) {
		if err := validateTLSFiles("", ""); err != nil {
			t.Fatalf("validateTLSFiles(\"\", \"\") = %v, want nil", err)
		}
	})

	t.Run("a real pair loads", func(t *testing.T) {
		if err := validateTLSFiles(cert.certFile, cert.keyFile); err != nil {
			t.Fatalf("validateTLSFiles(pair) = %v, want nil", err)
		}
	})

	t.Run("half a pair is refused, naming the missing flag", func(t *testing.T) {
		certOnly := validateTLSFiles(cert.certFile, "")
		keyOnly := validateTLSFiles("", cert.keyFile)
		if certOnly == nil || keyOnly == nil {
			t.Fatalf("validateTLSFiles(cert only) = %v, validateTLSFiles(key only) = %v, want errors", certOnly, keyOnly)
		}
		if !strings.Contains(certOnly.Error(), "--tls-key") {
			t.Errorf("cert-only error = %q, want it to name the missing --tls-key", certOnly)
		}
		if !strings.Contains(keyOnly.Error(), "--tls-cert") {
			t.Errorf("key-only error = %q, want it to name the missing --tls-cert", keyOnly)
		}
		if certOnly.Error() == keyOnly.Error() {
			t.Errorf("both halves reported %q, want a distinct message for each", certOnly)
		}
	})

	t.Run("a file that is not there is refused at startup", func(t *testing.T) {
		err := validateTLSFiles(missing, cert.keyFile)
		if err == nil {
			t.Fatal("validateTLSFiles(missing cert) = nil, want a startup error")
		}
		if !strings.Contains(err.Error(), "loading the TLS certificate and key") {
			t.Errorf("error = %q, want it to say the pair could not be loaded", err)
		}
	})

	t.Run("a loader failure is wrapped, not swallowed", func(t *testing.T) {
		sentinel := errors.New("tls: private key does not match public key")
		stubLoadTLSKeyPair(t, func(string, string) (tls.Certificate, error) { return tls.Certificate{}, sentinel })
		err := validateTLSFiles("cert.pem", "key.pem")
		if !errors.Is(err, sentinel) {
			t.Fatalf("validateTLSFiles() = %v, want the loader's error wrapped", err)
		}
	})
}

// TestTLSConfigFor pins the two settings the config exists to state.
//
// h2 in NextProtos is the load-bearing one. http.Server negotiates HTTP/2 for a
// TLS listener only when the config advertises it — ServeTLS adds it for you and
// tls.NewListener does not — so dropping it here would silently put every client
// back on HTTP/1.1, with nothing failing to say so.
func TestTLSConfigFor(t *testing.T) {
	cert := newTestCert(t)

	t.Run("advertises h2 and floors the version", func(t *testing.T) {
		cfg, err := tlsConfigFor(cert.certFile, cert.keyFile)
		if err != nil {
			t.Fatalf("tlsConfigFor() = %v, want a config", err)
		}
		if cfg.MinVersion != tls.VersionTLS12 {
			t.Errorf("MinVersion = %#x, want TLS 1.2 (%#x)", cfg.MinVersion, tls.VersionTLS12)
		}
		if !slices.Contains(cfg.NextProtos, "h2") {
			t.Errorf("NextProtos = %q, want it to advertise h2", cfg.NextProtos)
		}
		if !slices.Contains(cfg.NextProtos, "http/1.1") {
			t.Errorf("NextProtos = %q, want http/1.1 kept as the fallback", cfg.NextProtos)
		}
		if len(cfg.Certificates) != 1 {
			t.Errorf("Certificates = %d, want the loaded pair", len(cfg.Certificates))
		}
	})

	t.Run("a pair that will not load is an error, not an empty config", func(t *testing.T) {
		cfg, err := tlsConfigFor(filepath.Join(t.TempDir(), "absent.pem"), cert.keyFile)
		if err == nil {
			t.Fatalf("tlsConfigFor(missing cert) = %+v, want an error", cfg)
		}
		if cfg != nil {
			t.Errorf("tlsConfigFor(missing cert) returned %+v alongside %v, want nil", cfg, err)
		}
	})
}

// assertIsSocket fails the test unless a socket exists at path, returning its
// mode for the caller to look at further.
func assertIsSocket(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s) = %v, want a socket", path, err)
	}
	if info.Mode()&fs.ModeSocket == 0 {
		t.Fatalf("mode of %s = %v, want a socket", path, info.Mode())
	}
	return info.Mode()
}

// assertSocketMode fails the test unless path is a socket carrying the
// permission mode the caller asked for.
//
// The mode is checked only where it can be enforced: the non-unix builds bind
// the socket and say so through socketModesEnforced rather than promise a mode
// their chmod does not carry.
func assertSocketMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if mode := assertIsSocket(t, path); socketModesEnforced && mode.Perm() != want {
		t.Errorf("permissions of %s = %#o, want %#o", path, mode.Perm(), want)
	}
}

// assertPathAbsent fails the test unless nothing exists at path, reporting why
// the caller expected it gone.
func assertPathAbsent(t *testing.T, path, why string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lstat(%s) = %v, want nothing there: %s", path, err, why)
	}
}

// leaveStaleSocket creates a socket file and abandons it with the file still on
// disk, which is exactly what a process that crashed before its listener closed
// leaves behind.
func leaveStaleSocket(t *testing.T, path string) {
	t.Helper()
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	// Without this the listener unlinks the path on Close and there is no stale
	// socket left to find.
	ln.SetUnlinkOnClose(false)
	if closeErr := ln.Close(); closeErr != nil {
		t.Fatalf("close listener: %v", closeErr)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&fs.ModeSocket == 0 {
		t.Fatalf("Lstat(%s) = %v (%v), want a socket file left behind", path, info, err)
	}
}

// TestClearStaleSocketAcceptsAnAbsentPath covers the ordinary first start:
// there is nothing to clear, and nothing is created either.
//
// The tests that follow cover the other states the path can be in. The
// distinction carrying the weight is dead versus live: bind fails on an existing
// path either way, so an unconditional remove would let a second instance
// silently steal a socket the first is still serving.
func TestClearStaleSocketAcceptsAnAbsentPath(t *testing.T) {
	path := tempSocketPath(t)
	if err := clearStaleSocket(t.Context(), path); err != nil {
		t.Fatalf("clearStaleSocket() = %v, want nil for an absent path", err)
	}
	assertPathAbsent(t, path, "an absent path is nothing to clear, not something to create")
}

// TestClearStaleSocketRefusesANonSocket pins that a path pointing at something
// else is refused and left alone. The operator pointed at that file; refusing
// and removing it anyway would be the worst of both.
func TestClearStaleSocketRefusesANonSocket(t *testing.T) {
	const body = "do not delete me"
	path := filepath.Join(t.TempDir(), "notasocket")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	err := clearStaleSocket(t.Context(), path)
	if err == nil {
		t.Fatal("clearStaleSocket(regular file) = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "not a socket") {
		t.Errorf("error = %q, want it to say the path is not a socket", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("the refused file was removed: %v", readErr)
	}
	if string(got) != body {
		t.Errorf("file contents = %q, want %q untouched", got, body)
	}
}

// TestClearStaleSocketRemovesADeadSocket covers the crashed-process case: the
// socket file outlived its owner, so it is removed and the path is free to bind.
func TestClearStaleSocketRemovesADeadSocket(t *testing.T) {
	path := tempSocketPath(t)
	leaveStaleSocket(t, path)
	if err := clearStaleSocket(t.Context(), path); err != nil {
		t.Fatalf("clearStaleSocket(stale) = %v, want nil", err)
	}
	assertPathAbsent(t, path, "a socket nobody answers on is stale and should be removed")
}

// TestClearStaleSocketRefusesALiveSocket covers the case the dial probe exists
// for: a successful connect proves an owner is still serving, so the socket is
// left exactly where it is.
func TestClearStaleSocketRefusesALiveSocket(t *testing.T) {
	path := tempSocketPath(t)
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer func() { _ = ln.Close() }()

	clearErr := clearStaleSocket(t.Context(), path)
	if clearErr == nil {
		t.Fatal("clearStaleSocket(live socket) = nil, want a refusal: the owner is still serving")
	}
	if !strings.Contains(clearErr.Error(), "already served by another process") {
		t.Errorf("error = %q, want it to say another process is serving", clearErr)
	}
	// The live owner's socket is still there. Its mode is whatever the owner
	// bound it with — this listener is a plain one, not listenUnix — so what
	// matters here is only that nothing removed it.
	assertIsSocket(t, path)
}

// TestClearStaleSocketReportsAnUnreadablePath covers the branch where the path
// can be neither confirmed absent nor identified: a regular file used as a
// directory makes Lstat fail with ENOTDIR, which goes back untouched.
func TestClearStaleSocketReportsAnUnreadablePath(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := clearStaleSocket(t.Context(), filepath.Join(parent, "s.sock")); err == nil {
		t.Fatal("clearStaleSocket(unreadable path) = nil, want the stat error")
	}
}

// TestListenUnixBindsWithTheRequestedMode binds real sockets and checks the mode
// they end up with, since the mode is the whole reason the bind is wrapped: the
// kernel creates the inode at 0777 &^ umask — world-connectable in practice —
// and a chmod alone cannot close the window between bind and chmod.
//
// The 0600 case is the one that proves the chmod runs: the umask can only clear
// bits, so a mode the umask already produces would pass either way.
func TestListenUnixBindsWithTheRequestedMode(t *testing.T) {
	cases := []struct {
		name string
		mode os.FileMode
		want os.FileMode
	}{
		{name: "default", mode: defaultSocketMode, want: defaultSocketMode},
		{name: "owner only", mode: 0o600, want: 0o600},
		{name: "unset falls back", mode: 0, want: defaultSocketMode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tempSocketPath(t)
			ln, err := listenUnix(t.Context(), path, tc.mode)
			if err != nil {
				t.Fatalf("listenUnix(%s, %#o) = %v, want a listener", path, tc.mode, err)
			}
			if got := ln.Addr().Network(); got != "unix" {
				t.Errorf("Addr().Network() = %q, want unix", got)
			}
			assertSocketMode(t, path, tc.want)
			if closeErr := ln.Close(); closeErr != nil {
				t.Fatalf("close listener: %v", closeErr)
			}
			// A socket left on disk is what makes the next start refuse to
			// bind, so Close unlinking it is part of the contract.
			assertPathAbsent(t, path, "Close should have unlinked the socket")
		})
	}
}

// TestListenUnixRebindsAStaleSocket covers the whole path a restart after a
// crash takes: the leftover socket is cleared and the address is bound again.
func TestListenUnixRebindsAStaleSocket(t *testing.T) {
	path := tempSocketPath(t)
	leaveStaleSocket(t, path)
	ln, err := listenUnix(t.Context(), path, defaultSocketMode)
	if err != nil {
		t.Fatalf("listenUnix(stale path) = %v, want the stale socket cleared and rebound", err)
	}
	assertSocketMode(t, path, defaultSocketMode)
	if closeErr := ln.Close(); closeErr != nil {
		t.Fatalf("close listener: %v", closeErr)
	}
}

// TestListenUnixRefusesANonSocket pins that the refusal from clearStaleSocket
// stops the bind rather than being logged and passed over.
func TestListenUnixRefusesANonSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notasocket")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	ln, err := listenUnix(t.Context(), path, defaultSocketMode)
	if err == nil {
		_ = ln.Close()
		t.Fatal("listenUnix(regular file) = nil, want a refusal")
	}
}

// TestListenUnixNeedsItsDirectory covers the check that turns a missing parent
// directory into a message naming it, rather than the bare bind error a
// deployment would otherwise have to interpret.
func TestListenUnixNeedsItsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodir", "s.sock")
	ln, err := listenUnix(t.Context(), path, defaultSocketMode)
	if err == nil {
		_ = ln.Close()
		t.Fatal("listenUnix(missing directory) = nil, want an error")
	}
	if !strings.Contains(err.Error(), "directory is not usable") {
		t.Errorf("error = %q, want it to name the directory as the problem", err)
	}
}

// TestListenUnixReportsABindFailure covers the bind error itself: a unix address
// is capped at about 108 bytes, so an over-long name in a directory that does
// exist fails past both of the checks above it.
func TestListenUnixReportsABindFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), strings.Repeat("s", 200)+".sock")
	ln, err := listenUnix(t.Context(), path, defaultSocketMode)
	if err == nil {
		_ = ln.Close()
		t.Fatal("listenUnix(over-long path) = nil, want the bind error")
	}
}

// TestListenHTTP covers the routing between the two kinds of address, and the
// error it reports when neither can be bound.
func TestListenHTTP(t *testing.T) {
	t.Run("a host:port is a TCP listener", func(t *testing.T) {
		ln, err := listenHTTP(t.Context(), listenSpec{addr: "127.0.0.1:0"})
		if err != nil {
			t.Fatalf("listenHTTP(tcp) = %v, want a listener", err)
		}
		defer func() { _ = ln.Close() }()
		if got := ln.Addr().Network(); got != "tcp" {
			t.Errorf("Addr().Network() = %q, want tcp", got)
		}
	})

	t.Run("a path is a unix socket, bound with the requested mode", func(t *testing.T) {
		path := tempSocketPath(t)
		ln, err := listenHTTP(t.Context(), listenSpec{addr: path, socketMode: 0o600})
		if err != nil {
			t.Fatalf("listenHTTP(unix) = %v, want a listener", err)
		}
		defer func() { _ = ln.Close() }()
		if got := ln.Addr().Network(); got != "unix" {
			t.Errorf("Addr().Network() = %q, want unix", got)
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("Stat(%s) = %v, want the bound socket", path, statErr)
		}
		if socketModesEnforced && info.Mode().Perm() != 0o600 {
			t.Errorf("permissions = %#o, want %#o", info.Mode().Perm(), 0o600)
		}
	})

	t.Run("a port that cannot be bound is named", func(t *testing.T) {
		ln, err := listenHTTP(t.Context(), listenSpec{addr: "127.0.0.1:99999"})
		if err == nil {
			_ = ln.Close()
			t.Fatal("listenHTTP(invalid port) = nil, want an error")
		}
		if !strings.Contains(err.Error(), "127.0.0.1:99999") {
			t.Errorf("error = %q, want it to name the address that failed", err)
		}
	})
}

// TestListenHTTPServesTLS drives a real handshake against the wrapped listener.
//
// It asserts h2 explicitly because that is the setting with no other symptom:
// without it in NextProtos the handshake still succeeds, the certificate still
// verifies, and every client silently drops to HTTP/1.1.
//
// It also pins that Addr still reports the TCP address through the wrapper —
// tls.NewListener delegates it — which is the reason the startup line is
// rendered by describeListener rather than read off the listener.
func TestListenHTTPServesTLS(t *testing.T) {
	cert := newTestCert(t)
	ln, err := listenHTTP(t.Context(), listenSpec{addr: "127.0.0.1:0", tlsCert: cert.certFile, tlsKey: cert.keyFile})
	if err != nil {
		t.Fatalf("listenHTTP(tls) = %v, want a listener", err)
	}
	defer func() { _ = ln.Close() }()

	if got := ln.Addr().Network(); got != "tcp" {
		t.Errorf("Addr().Network() = %q, want the wrapped listener's tcp", got)
	}
	if host, _, splitErr := net.SplitHostPort(ln.Addr().String()); splitErr != nil || host != "127.0.0.1" {
		t.Errorf("Addr() = %q (%v), want the bound 127.0.0.1 address", ln.Addr(), splitErr)
	}

	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Accept returns before the handshake; ALPN is settled by running it.
		if tlsConn, ok := conn.(*tls.Conn); ok {
			_ = tlsConn.HandshakeContext(t.Context())
		}
	}()

	dialer := net.Dialer{Timeout: 5 * time.Second}
	raw, dialErr := dialer.DialContext(t.Context(), "tcp", ln.Addr().String())
	if dialErr != nil {
		t.Fatalf("dial: %v", dialErr)
	}
	defer func() { _ = raw.Close() }()

	// The generated certificate is trusted through a pool rather than waved
	// through with InsecureSkipVerify, so this also proves the listener served
	// the pair it was given.
	client := tls.Client(raw, &tls.Config{
		RootCAs:    cert.roots,
		ServerName: "127.0.0.1",
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
	})
	if handshakeErr := client.HandshakeContext(t.Context()); handshakeErr != nil {
		t.Fatalf("handshake: %v", handshakeErr)
	}
	if got := client.ConnectionState().NegotiatedProtocol; got != "h2" {
		t.Errorf("negotiated protocol = %q, want h2: HTTP/2 is lost silently without it", got)
	}
	<-accepted
}

// TestListenHTTPTLSFailureReleasesTheListener covers the error path after a
// successful bind: the socket file must not be left on disk for a failure that
// happened afterward, since the next start would then refuse the path or, worse,
// clear it.
func TestListenHTTPTLSFailureReleasesTheListener(t *testing.T) {
	path := tempSocketPath(t)
	absent := filepath.Join(t.TempDir(), "absent.pem")
	ln, err := listenHTTP(t.Context(), listenSpec{addr: path, socketMode: defaultSocketMode, tlsCert: absent, tlsKey: absent})
	if err == nil {
		_ = ln.Close()
		t.Fatal("listenHTTP(bad TLS pair) = nil, want an error")
	}
	if _, statErr := os.Lstat(path); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("Lstat(%s) = %v, want the socket unlinked after the failed TLS setup", path, statErr)
	}
}

// TestListenSpecServesTLS pins that the certificate alone decides it — the key
// is validated elsewhere, and a spec carrying one without the other never
// reaches this far.
func TestListenSpecServesTLS(t *testing.T) {
	cases := []struct {
		name string
		spec listenSpec
		want bool
	}{
		{name: "no pair", spec: listenSpec{addr: ":8080"}, want: false},
		{name: "a pair", spec: listenSpec{addr: ":8080", tlsCert: "cert.pem", tlsKey: "key.pem"}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.spec.servesTLS(); got != tc.want {
				t.Errorf("servesTLS() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDescribeListener covers both listener kinds crossed with both schemes.
//
// Each TLS case wraps a real tls.NewListener rather than passing the flag on its
// own, because the wrapper delegating Addr is the exact behavior this function
// works around: read straight off the listener, a TLS endpoint prints an
// unchanged host:port and a socket prints a bare path.
func TestDescribeListener(t *testing.T) {
	cert := newTestCert(t)
	cfg, err := tlsConfigFor(cert.certFile, cert.keyFile)
	if err != nil {
		t.Fatalf("tlsConfigFor() = %v, want a config", err)
	}

	t.Run("tcp, plain", func(t *testing.T) {
		ln := listenerFor(t, "tcp")
		want := "http://" + ln.Addr().String()
		if got := describeListener(ln, false); got != want {
			t.Errorf("describeListener() = %q, want %q", got, want)
		}
	})

	t.Run("tcp, tls", func(t *testing.T) {
		ln := listenerFor(t, "tcp")
		want := "https://" + ln.Addr().String()
		if got := describeListener(tls.NewListener(ln, cfg), true); got != want {
			t.Errorf("describeListener() = %q, want %q", got, want)
		}
	})

	t.Run("unix, plain", func(t *testing.T) {
		ln := listenerFor(t, "unix")
		want := "unix socket " + ln.Addr().String() + " (http)"
		if got := describeListener(ln, false); got != want {
			t.Errorf("describeListener() = %q, want %q", got, want)
		}
	})

	t.Run("unix, tls", func(t *testing.T) {
		ln := listenerFor(t, "unix")
		want := "unix socket " + ln.Addr().String() + " (https)"
		if got := describeListener(tls.NewListener(ln, cfg), true); got != want {
			t.Errorf("describeListener() = %q, want %q", got, want)
		}
	})
}

// TestMainWithExitRefusesAnUnservableTLSOrSocketMode covers the guards from the
// flag side, since that is the only place they act: both are checked before
// anything is served, because a deployment that believes it is serving TLS, or
// that its socket is group-only, and is wrong about it has nothing to look at
// afterward.
//
// Every case must fail before a listener exists, so none of them starts a
// server.
func TestMainWithExitRefusesAnUnservableTLSOrSocketMode(t *testing.T) {
	cert := newTestCert(t)
	absent := filepath.Join(t.TempDir(), "absent.pem")
	cases := []struct {
		name string
		args []string
	}{
		{name: "cert without key", args: []string{"--tls-cert", cert.certFile}},
		{name: "key without cert", args: []string{"--tls-key", cert.keyFile}},
		{name: "a cert file that is not there", args: []string{"--tls-cert", absent, "--tls-key", cert.keyFile}},
		{name: "a socket mode for a TCP address", args: []string{"--http", "127.0.0.1:0", "--http-socket-mode", "0600"}},
		{name: "a socket mode that is not a mode", args: []string{"--http-socket-mode", "999"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			awaitReturn(t, func() {
				code = callMainWithExit(t, append([]string{"libgen-mcp"}, tc.args...)...)
			})
			if code != 1 {
				t.Fatalf("mainWithExit(%q) = %d, want 1", tc.args, code)
			}
		})
	}
}

// TestSecurityHeadersHSTSFollowsTLS pins the one header that depends on who
// terminates TLS. It lives beside the listener tests because that is what
// decides it: over plain HTTP the claim is one this process cannot make, and on
// localhost it poisons the browser's HSTS cache for that host and port for a
// year.
func TestSecurityHeadersHSTSFollowsTLS(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	t.Run("plain http states none", func(t *testing.T) {
		rec := serveThrough(t, securityHeaders(false, ok))
		if got := sentHeader(rec).Get(headerHSTS); got != "" {
			t.Errorf("%s = %q, want none when a proxy in front terminates TLS", headerHSTS, got)
		}
	})

	t.Run("this process terminating TLS states one year", func(t *testing.T) {
		const want = "max-age=31536000; includeSubDomains"
		rec := serveThrough(t, securityHeaders(true, ok))
		sent := sentHeader(rec)
		if got := sent.Get(headerHSTS); got != want {
			t.Errorf("%s = %q, want %q", headerHSTS, got, want)
		}
		// No preload directive: preloading is a decision about a whole domain,
		// which one server behind it cannot make for the operator.
		if strings.Contains(sent.Get(headerHSTS), "preload") {
			t.Errorf("%s = %q, want no preload directive", headerHSTS, sent.Get(headerHSTS))
		}
		if values := sent.Values(headerHSTS); len(values) > 1 {
			t.Errorf("%s appears %d times (%q), want exactly one", headerHSTS, len(values), values)
		}
		// The rest of the set is unchanged by the TLS flag.
		for name, wantValue := range wantSecurityHeaders {
			if got := sent.Get(name); got != wantValue {
				t.Errorf("%s = %q, want %q", name, got, wantValue)
			}
		}
	})
}
