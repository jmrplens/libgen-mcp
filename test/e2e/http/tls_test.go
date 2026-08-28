//go:build httpe2e

package httpe2e

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
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
