package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// hostRequest builds a request as net/http delivers it: a Host header, the peer
// it came from, and the local address the connection was accepted on, which is
// the one the rebinding rule turns on and which nothing else in this package
// has to set.
func hostRequest(t *testing.T, host, peer, localAddr string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
	r.Host = host
	r.RemoteAddr = peer
	if localAddr != "" {
		ctx := context.WithValue(r.Context(), http.LocalAddrContextKey, mustTCPAddr(t, localAddr))
		r = r.WithContext(ctx)
	}
	return r
}

// mustTCPAddr resolves a literal address without a lookup.
func mustTCPAddr(t *testing.T, addr string) net.Addr {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("the test's own local address %q is not host:port: %v", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		t.Fatalf("the test's own local address %q is not an IP literal", addr)
	}
	return &net.TCPAddr{IP: ip, Port: len(port)}
}

// The two addresses every case below is written against: the loopback the
// documented proxies connect over, and a routable one standing in for a
// listener something other than this machine reached.
const (
	loopbackLocal = "127.0.0.1:8080"
	routableLocal = "10.1.2.3:8080"
	proxyPeer     = "127.0.0.1:41100"
	strangerPeer  = "198.51.100.23:41100"
	publicName    = "mcp.example.org"
	publicOrigin  = "https://" + publicName + "/libgen"
)

// TestHostGuardPermits is the whole policy in one table.
//
// The first case is the live defect this exists for: a reverse proxy forwarding
// the client's Host over loopback, which is every recipe in the getting-started
// guide and the shape the hosted deployment runs in. It was refused, and the two
// rows after it are the two ways an operator says otherwise.
func TestHostGuardPermits(t *testing.T) {
	cases := []struct {
		name      string
		addr      string
		publicURL string
		proxies   string
		host      string
		peer      string
		local     string
		want      bool
	}{
		{
			name:  "a proxied public name over loopback, undeclared",
			addr:  "0.0.0.0:8080",
			host:  publicName,
			peer:  proxyPeer,
			local: loopbackLocal,
			want:  false,
		},
		{
			name:      "the same, declared with --public-url",
			addr:      "0.0.0.0:8080",
			publicURL: publicOrigin,
			host:      publicName,
			peer:      proxyPeer,
			local:     loopbackLocal,
			want:      true,
		},
		{
			name:    "the same, vouched for by --trusted-proxies",
			addr:    "0.0.0.0:8080",
			proxies: "127.0.0.1/32",
			host:    publicName,
			peer:    proxyPeer,
			local:   loopbackLocal,
			want:    true,
		},
		{
			name:    "a trusted-proxy list that does not cover the peer",
			addr:    "0.0.0.0:8080",
			proxies: "127.0.0.1/32",
			host:    publicName,
			peer:    strangerPeer,
			local:   loopbackLocal,
			want:    false,
		},
		// A reader will assume a loopback bind is the exempt one. It is the
		// stricter one: the operator named an interface, so an undeclared Host
		// is refused whatever address the request arrived on.
		{
			name:  "a loopback bind is not exempt",
			addr:  "127.0.0.1:8080",
			host:  publicName,
			peer:  proxyPeer,
			local: loopbackLocal,
			want:  false,
		},
		{
			name:      "a loopback bind with --public-url",
			addr:      "127.0.0.1:8080",
			publicURL: publicOrigin,
			host:      publicName,
			peer:      proxyPeer,
			local:     loopbackLocal,
			want:      true,
		},
		{
			name:    "a loopback bind with --trusted-proxies",
			addr:    "127.0.0.1:8080",
			proxies: "127.0.0.1/32",
			host:    publicName,
			peer:    proxyPeer,
			local:   loopbackLocal,
			want:    true,
		},
		// A named bind refuses an undeclared Host even on a routable address,
		// which is what tells `bound` apart from the rebinding fallback below.
		{
			name:  "a named bind refuses another name on a routable address",
			addr:  publicName + ":8080",
			host:  "evil.example",
			peer:  strangerPeer,
			local: routableLocal,
			want:  false,
		},
		{
			name:  "a named bind answers its own name",
			addr:  publicName + ":8080",
			host:  publicName,
			peer:  strangerPeer,
			local: routableLocal,
			want:  true,
		},
		// The SDK's rule, reproduced: a wildcard bind reached on something other
		// than loopback is not a local server, so whatever host a proxy in front
		// forwards keeps working.
		{
			name:  "a wildcard bind on a routable address answers any name",
			addr:  "0.0.0.0:8080",
			host:  "anything.example",
			peer:  strangerPeer,
			local: routableLocal,
			want:  true,
		},
		{
			name:  "a request with no Host at all",
			addr:  "127.0.0.1:8080",
			host:  "",
			peer:  proxyPeer,
			local: loopbackLocal,
			want:  true,
		},
		{
			name:  "a unix socket answers any name",
			addr:  "/run/libgen-mcp.sock",
			host:  publicName,
			peer:  "@",
			local: "",
			want:  true,
		},
		{
			name:  "loopback names are always declared",
			addr:  "0.0.0.0:8080",
			host:  "localhost:8080",
			peer:  proxyPeer,
			local: loopbackLocal,
			want:  true,
		},
		{
			name:  "an IPv6 loopback literal with a port",
			addr:  "0.0.0.0:8080",
			host:  "[::1]:8080",
			peer:  proxyPeer,
			local: loopbackLocal,
			want:  true,
		},
		// DNS is case-insensitive and Go is not. A browser lowercases the
		// authority before sending it, so an operator who wrote the origin in
		// mixed case would otherwise be refused at the name they published.
		{
			name:      "--public-url in mixed case still answers the lowercase name",
			addr:      "0.0.0.0:8080",
			publicURL: "https://MCP.Example.ORG/libgen",
			host:      publicName,
			peer:      proxyPeer,
			local:     loopbackLocal,
			want:      true,
		},
		{
			name:      "the declared name carrying the port clients dial",
			addr:      "0.0.0.0:8080",
			publicURL: publicOrigin,
			host:      publicName + ":443",
			peer:      proxyPeer,
			local:     loopbackLocal,
			want:      true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			guard := newHostGuard(tc.addr, tc.publicURL, mustParseProxies(t, tc.proxies))
			if got := guard.permits(hostRequest(t, tc.host, tc.peer, tc.local)); got != tc.want {
				t.Errorf("permits(Host %q from %s on %s) = %v, want %v", tc.host, tc.peer, tc.local, got, tc.want)
			}
		})
	}
}

// TestHostGuardTrustsAUnixPeerOnlyWithTheLiteral pins that the Host side reads
// the same trust the charged-address side does.
//
// A socket listener declares no host, so without the peer trust the guard falls
// through to the rebinding rule — which passes here only because a socket
// carries no local TCP address. The assertion that matters is the flag being
// read at all, so it is made on a request that arrived on loopback.
func TestHostGuardTrustsAUnixPeerOnlyWithTheLiteral(t *testing.T) {
	r := hostRequest(t, publicName, "@", loopbackLocal)

	if newHostGuard("/run/libgen-mcp.sock", "", mustParseProxies(t, "127.0.0.1/32")).permits(r) {
		t.Error("a socket peer was trusted although --trusted-proxies names only an address")
	}
	if !newHostGuard("/run/libgen-mcp.sock", "", mustParseProxies(t, unixPeerEntry)).permits(r) {
		t.Error("the unix literal did not make the socket's peer a trusted hop for the Host header")
	}
}

// TestAllowedHosts covers what a listen address declares on its own.
func TestAllowedHosts(t *testing.T) {
	cases := []struct {
		addr string
		want []string // nil means "declares no host"
	}{
		{addr: "0.0.0.0:8080"},
		{addr: "[::]:8080"},
		{addr: ":8080"},
		{addr: "/run/libgen-mcp.sock"},
		{addr: "./mcp.sock"},
		{addr: "127.0.0.1:8080", want: []string{"127.0.0.1", "localhost", "::1"}},
		{addr: "MCP.Example.ORG:8080", want: []string{publicName, "localhost", "127.0.0.1", "::1"}},
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			got := allowedHosts(tc.addr)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("allowedHosts(%q) = %v, want nil: the address names no host", tc.addr, got)
				}
				return
			}
			for _, host := range tc.want {
				if !got[host] {
					t.Errorf("allowedHosts(%q) does not declare %q (got %v)", tc.addr, host, got)
				}
			}
			if got["evil.example"] {
				t.Errorf("allowedHosts(%q) declares a host nobody named", tc.addr)
			}
		})
	}
}

// TestAllowedHostsDeclaresNothingForASocketAddress is the invariant behind the
// explicit socket case in allowedHosts, stated as a property so the two
// functions cannot drift apart: whatever this build calls a socket address, it
// declares no host.
//
// The Windows path is the reason the check exists and the reason it is written
// this way. On Linux it is not a socket address at all — there is no separator
// in it — so the loop skips it; on Windows it is, and without the early return
// SplitHostPort would find the drive letter's colon and a server on
// C:\...\mcp.sock would serve nothing but a client naming host "C". The unit
// suite runs on all three platforms, which is what makes that row real.
func TestAllowedHostsDeclaresNothingForASocketAddress(t *testing.T) {
	var checked int
	for _, addr := range []string{"/run/libgen-mcp.sock", "./mcp.sock", `C:\run\libgen-mcp.sock`} {
		if !isUnixSocketAddr(addr) {
			continue
		}
		checked++
		if got := allowedHosts(addr); got != nil {
			t.Errorf("allowedHosts(%q) = %v, want nil: no name resolves to a file on disk", addr, got)
		}
	}
	if checked == 0 {
		t.Fatal("no address in the list reads as a socket on this build, so nothing was checked")
	}
}

// TestHostGuardedRefusesWithSomethingActionable covers the middleware rather
// than the policy: the status, that the handler behind it never runs, and that
// the body names the two flags that fix it.
func TestHostGuardedRefusesWithSomethingActionable(t *testing.T) {
	var reached bool
	handler := hostGuarded(
		newHostGuard("0.0.0.0:8080", "", trustedProxies{}),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusTeapot)
		}),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, hostRequest(t, publicName, proxyPeer, loopbackLocal))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if reached {
		t.Error("the refused request reached the handler behind the guard")
	}
	for _, want := range []string{"--public-url", "--trusted-proxies"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("the refusal does not name %s, so it does not say how to fix it: %q", want, rec.Body.String())
		}
	}
}

// TestHostGuardedPassesAPermittedRequestThrough is the negative half: the guard
// must be invisible to every deployment that is configured correctly.
func TestHostGuardedPassesAPermittedRequestThrough(t *testing.T) {
	handler := hostGuarded(
		newHostGuard("0.0.0.0:8080", publicOrigin, trustedProxies{}),
		teapotHandler(),
	)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, hostRequest(t, publicName, proxyPeer, loopbackLocal))

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want the handler behind the guard to have answered %d", rec.Code, http.StatusTeapot)
	}
}

// TestLoggedHostPrefixIsBounded keeps a caller's header out of the log stream
// past the point it stops being evidence.
func TestLoggedHostPrefixIsBounded(t *testing.T) {
	long := strings.Repeat("a", loggedHostPrefixBytes*3)
	got := loggedHostPrefix(long)
	if len(got) > loggedHostPrefixBytes+3 {
		t.Errorf("loggedHostPrefix returned %d bytes for a %d-byte header", len(got), len(long))
	}
	if short := loggedHostPrefix(publicName); short != publicName {
		t.Errorf("loggedHostPrefix(%q) = %q, want it unchanged", publicName, short)
	}
}

// TestValidatePublicURL refuses at startup what the guard could never act on.
func TestValidatePublicURL(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		for _, value := range []string{"", "  ", publicOrigin, "http://localhost:8080", "https://mcp.example.org"} {
			if err := validatePublicURL(value); err != nil {
				t.Errorf("validatePublicURL(%q) = %v, want accepted", value, err)
			}
		}
	})

	t.Run("refused", func(t *testing.T) {
		// A bare host is the mistake worth naming: it parses as a URL with an
		// empty scheme and an empty host, so accepting it would declare nothing
		// while looking exactly like a configured deployment.
		for _, value := range []string{publicName, "mcp.example.org/libgen", "ftp://mcp.example.org", "://nope", "https://"} {
			err := validatePublicURL(value)
			if err == nil {
				t.Errorf("validatePublicURL(%q) was accepted", value)
				continue
			}
			if !strings.Contains(err.Error(), "--public-url") {
				t.Errorf("error %q does not name the flag to change", err)
			}
		}
	})
}

// TestMainWithExitRefusesAnUnusablePublicURL drives the refusal through the flag,
// which is where an operator meets it. The unit test above would keep passing if
// nothing ever called the check.
func TestMainWithExitRefusesAnUnusablePublicURL(t *testing.T) {
	var code int
	awaitReturn(t, func() {
		code = callMainWithExit(t, "libgen-mcp", "--http", "127.0.0.1:0", "--public-url", publicName)
	})
	if code != 1 {
		t.Fatalf("mainWithExit(--public-url=%s) = %d, want 1", publicName, code)
	}
}
