//go:build httpe2e

package httpe2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// publicHost is the name a reverse proxy in front of this server forwards. It is
// never resolved: every request below is addressed to the listener's own
// address and only carries this in the Host header, which is exactly what
// nginx's `proxy_set_header Host $host` produces.
const publicHost = "mcp.example.org"

// hostPOST is a tools/list call arriving with a forwarded Host.
func hostPOST(host string) request {
	r := mcpPOST(nil)
	r.host = host
	return r
}

// TestHostGuard_AProxiedPublicNameNeedsOneOfTheTwoFlags is the live defect, and
// both of its fixes, over the real binary.
//
// The SDK refuses any non-loopback Host on a connection accepted on a loopback
// address. That is not a description of a listener bound to loopback: it is the
// address the connection was accepted on, so a wildcard bind reached over
// 127.0.0.1 — which is every reverse-proxy recipe this project documents, and
// the shape the hosted deployment runs in — hits it too. Both bind shapes are
// asserted for that reason: a reader will assume the wildcard one is exempt.
//
// The refusal is a 403 whose body is not JSON, which a Streamable HTTP client
// reads as a server that does not speak MCP rather than as a misconfiguration.
func TestHostGuard_AProxiedPublicNameNeedsOneOfTheTwoFlags(t *testing.T) {
	binds := []struct {
		name string
		addr func(port int) string
	}{
		{name: "wildcard bind", addr: func(port int) string { return fmt.Sprintf(":%d", port) }},
		{name: "loopback bind", addr: func(port int) string { return fmt.Sprintf("127.0.0.1:%d", port) }},
	}
	cases := []struct {
		name  string
		flags []string
		want  int
	}{
		{
			name: "undeclared",
			want: http.StatusForbidden,
		},
		{
			name:  "declared with --public-url",
			flags: []string{"--public-url", "https://" + publicHost + "/libgen"},
			want:  http.StatusOK,
		},
		{
			// The address this fixture's proxy would connect from. The hosted
			// endpoint names its own, which is a property of its Docker bridge.
			name:  "vouched for by --trusted-proxies",
			flags: []string{"--trusted-proxy-header", "X-Real-IP", "--trusted-proxies", "127.0.0.1/32"},
			want:  http.StatusOK,
		},
	}
	for _, bind := range binds {
		t.Run(bind.name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					port := freePort(t)
					s := launchServer(t, fmt.Sprintf("http://127.0.0.1:%d", port), nil, nil,
						append([]string{"--http", bind.addr(port)}, tc.flags...))

					reply := s.do(t, hostPOST(publicHost))
					if reply.status != tc.want {
						t.Fatalf("tools/list with Host %q = %d, want %d (body: %s)", publicHost, reply.status, tc.want, truncate(reply.body))
					}
					if tc.want == http.StatusOK {
						assertToolsListed(t, "a forwarded public Host", reply)
					}
				})
			}
		})
	}
}

// TestHostGuard_TheRefusalSaysWhichFlagToPass is what an operator meets when
// they hit the case above, and the reason the SDK's own copy of the check had to
// go: its message names the Host and stops there.
func TestHostGuard_TheRefusalSaysWhichFlagToPass(t *testing.T) {
	s := startServer(t, nil)

	reply := s.do(t, hostPOST(publicHost))
	if reply.status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", reply.status, http.StatusForbidden)
	}
	for _, want := range []string{"--public-url", "--trusted-proxies"} {
		if !strings.Contains(reply.body, want) {
			t.Errorf("the refusal does not name %s: %q", want, truncate(reply.body))
		}
	}
}

// TestHostGuard_ARequestWithNoHostIsServed covers the health check that made
// this an outage rather than a nuisance.
//
// HAProxy's `option httpchk` sends no Host unless one is configured, so a
// listener that refused it was marked permanently DOWN by a balancer that was
// working correctly. Refusing it buys nothing either: anything that can send a
// header-less request reaches the listener directly and needs no browser to do
// it for it.
//
// Go's client always sends one, and net/http's server answers 400 to an
// HTTP/1.1 request without it before any handler runs — so the request is
// written by hand as HTTP/1.0, the version that does not require a Host and the
// one such a probe speaks.
func TestHostGuard_ARequestWithNoHostIsServed(t *testing.T) {
	s := startServer(t, nil)

	wire := "POST / HTTP/1.0\r\n" +
		"Content-Type: application/json\r\n" +
		"Accept: " + acceptHeader + "\r\n" +
		"MCP-Protocol-Version: " + protocolVersion + "\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n", len(toolsListBody)) +
		"\r\n" + toolsListBody

	reply := s.raw(t, wire)
	if !strings.Contains(reply, " 200 ") {
		t.Fatalf("a request carrying no Host header was not served:\n%s", truncate(reply))
	}
}

// TestHostGuard_ASocketServesAnyHost pins the half that must never fire there.
//
// No name resolves to a file on disk, so the rebinding attack the guard exists
// against cannot reach a socket listener at all, and the Host a socket client
// sends is whatever its HTTP library needed to build a request.
func TestHostGuard_ASocketServesAnyHost(t *testing.T) {
	s := startUnixServer(t, nil)

	for _, host := range []string{publicHost, "anything.example", "unix"} {
		assertToolsListed(t, "over a unix socket with Host "+host, s.do(t, hostPOST(host)))
	}
}

// TestHostGuard_TheMCPAliasAnswersTheEndpoint covers the second spelling, both
// of its forms, and the path beneath it that must not reach the endpoint.
//
// The last one is the point. Mounted as a plain "/mcp/" subtree, ServeMux would
// hand the endpoint every path below it — and would also answer "/mcp" with a
// 301 to "/mcp/", which a POST does not follow with its body.
func TestHostGuard_TheMCPAliasAnswersTheEndpoint(t *testing.T) {
	s := startServer(t, nil)

	for _, path := range []string{"/mcp", "/mcp/"} {
		r := mcpPOST(nil)
		r.path = path
		assertToolsListed(t, "POST "+path, s.do(t, r))
	}

	assertNotFound(t, s, request{method: http.MethodGet, path: "/mcp/x"}, "/")
}

// TestHostGuard_TheAliasTravelsWithTheMount asserts the alias is relative to
// --http-path rather than pinned at the root, so a proxy that forwards its
// prefix reaches it the same way it reaches the canonical path.
func TestHostGuard_TheAliasTravelsWithTheMount(t *testing.T) {
	s := startServer(t, nil, "--http-path", "/libgen")

	r := mcpPOST(nil)
	r.path = "/libgen/mcp"
	assertToolsListed(t, "POST /libgen/mcp", s.do(t, r))

	// And not at the root, where nothing is mounted under this flag.
	assertNotFound(t, s, request{method: http.MethodPost, path: "/mcp", body: toolsListBody}, "/libgen")
}
