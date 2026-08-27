//go:build httpe2e

package httpe2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// proxyConfig is the nginx configuration under test. Two locations share one
// upstream: /proxied carries the hosted deployment's CORS block, /plain carries
// none, so the same server is observable with and without a proxy answering
// CORS on its behalf.
//
// The /proxied block is the deployment's, not an invention: mcp.jmrp.io answers
// a POST to the MCP location with Access-Control-Allow-Origin "*" and an
// Expose-Headers list, and answers OPTIONS itself with 204.
const proxyConfig = `events { worker_connections 64; }
http {
  access_log off;
  server {
    listen %d;
    location ^~ /proxied {
      add_header Access-Control-Allow-Origin "*" always;
      add_header Access-Control-Expose-Headers "Mcp-Session-Id, Mcp-Protocol-Version" always;
      if ($request_method = OPTIONS) {
        add_header Access-Control-Allow-Origin "*" always;
        add_header Access-Control-Allow-Methods "GET, POST, DELETE, OPTIONS" always;
        add_header Access-Control-Max-Age 86400 always;
        return 204;
      }
      proxy_http_version 1.1;
      proxy_set_header Connection "";
      proxy_set_header Host $host;
      proxy_set_header Origin $http_origin;
      rewrite ^/proxied/?(.*)$ /$1 break;
      proxy_pass http://127.0.0.1:%d;
    }
    location ^~ /plain {
      proxy_http_version 1.1;
      proxy_set_header Connection "";
      proxy_set_header Host $host;
      proxy_set_header Origin $http_origin;
      rewrite ^/plain/?(.*)$ /$1 break;
      proxy_pass http://127.0.0.1:%d;
    }
  }
}
`

// startProxy runs nginx in front of the given upstream port and returns its
// base URL, skipping the test when Docker is unavailable.
//
// Skipping rather than modeling the proxy is the whole point of the file: a
// hand-written stand-in would reproduce whatever this test already believes,
// and the bug it exists to catch is one that only appears when a real proxy
// adds real headers to a real response.
func startProxy(t *testing.T, upstreamPort int) string {
	t.Helper()

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available; the proxy layer is where the header-collision class lives, so it is skipped rather than modeled")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		t.Skip("docker is installed but not usable; skipping the proxy layer")
	}

	proxyPort := freePort(t)
	dir := t.TempDir()
	confPath := filepath.Join(dir, "nginx.conf")
	conf := fmt.Sprintf(proxyConfig, proxyPort, upstreamPort, upstreamPort)
	if err := os.WriteFile(confPath, []byte(conf), 0o600); err != nil {
		t.Fatalf("writing the nginx config: %v", err)
	}
	// The container reads the file as nginx's own user, so it has to be
	// world-readable; 0o600 above is what the linter wants for a file this
	// process creates, and the chmod is the narrower statement of intent.
	if err := os.Chmod(confPath, 0o644); err != nil { //nolint:gosec // a config with no secret in it, read by another uid
		t.Fatalf("relaxing the config permissions for the container: %v", err)
	}

	name := fmt.Sprintf("libgen-mcp-httpe2e-nginx-%d", proxyPort)
	runCtx, runCancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer runCancel()
	out, err := exec.CommandContext(runCtx, "docker", "run", "-d",
		"--name", name, "--network", "host",
		"-v", confPath+":/etc/nginx/nginx.conf:ro",
		"nginx:alpine",
	).CombinedOutput()
	if err != nil {
		t.Skipf("could not start nginx (%v):\n%s", err, out)
	}
	t.Cleanup(func() {
		rmCtx, rmCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer rmCancel()
		_ = exec.CommandContext(rmCtx, "docker", "rm", "-f", name).Run()
	})

	base := fmt.Sprintf("http://127.0.0.1:%d", proxyPort)
	waitProxy(t, base)
	return base
}

// waitProxy polls the proxy until it forwards a health check.
func waitProxy(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/plain/health", http.NoBody)
		if err != nil {
			t.Fatalf("building the proxy health request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Skip("nginx never forwarded a request; skipping the proxy layer rather than failing on the environment")
}

// proxyDo issues a request through the proxy and returns the status and every
// header value — not only the first, which is the entire point here, since the
// bug this file exists for is a duplicated header and Header.Get would hide it.
func proxyDo(t *testing.T, base, path, origin string) (int, http.Header) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+path, strings.NewReader(toolsListBody))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	if origin != "" {
		req.Header.Set("Origin", origin)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	// Drained before closing so the connection can be reused rather than torn
	// down under the proxy, which would muddy the next case in the same test.
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header
}

// TestProxy_ServerAndProxyCORSCollide reproduces the failure a browser sees and
// curl does not.
//
// Once the server answers CORS too, the response leaves with two
// Access-Control-Allow-Origin headers. Fetch does not merge them, it rejects
// the response outright — so adding server-side CORS while the proxy still
// advertises its own makes browser access worse than either alone, since the
// proxy's lone "*" at least worked for uncredentialed requests. The status is
// 200 throughout, which is why every curl check passed while a browser refused.
//
// This test is why the deployment note exists: the proxy's CORS block has to
// come out in the same change as --trusted-origins.
func TestProxy_ServerAndProxyCORSCollide(t *testing.T) {
	port := freePort(t)
	startServerOnPort(t, port, nil, "--trusted-origins="+trustedOrigin)
	base := startProxy(t, port)

	status, header := proxyDo(t, base, "/proxied/", trustedOrigin)
	origins := header.Values("Access-Control-Allow-Origin")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d — the collision is invisible at the status line", status, http.StatusOK)
	}
	if len(origins) < 2 {
		t.Fatalf("Access-Control-Allow-Origin = %v, want two values: this test exists to hold the collision in place so the deployment note keeps its evidence", origins)
	}
	t.Logf("the browser-visible failure, reproduced: Access-Control-Allow-Origin = %v", origins)

	// The same server, same flags, reached through a location that adds no
	// CORS of its own: exactly one header, and the one a browser accepts.
	plainStatus, plainHeader := proxyDo(t, base, "/plain/", trustedOrigin)
	plainOrigins := plainHeader.Values("Access-Control-Allow-Origin")
	if plainStatus != http.StatusOK {
		t.Fatalf("status through /plain = %d, want %d", plainStatus, http.StatusOK)
	}
	if len(plainOrigins) != 1 || plainOrigins[0] != trustedOrigin {
		t.Errorf("Access-Control-Allow-Origin through /plain = %v, want exactly [%s]", plainOrigins, trustedOrigin)
	}
}

// TestProxy_PreflightAnsweredByTheProxyHidesTheServer is the trap that let the
// broken preflight ship.
//
// With the proxy answering OPTIONS itself, the preflight is 204 with the CORS
// headers no matter what the server behind it would have said — so a browser is
// told the request is allowed and sends it, and only then is it refused. Every
// check against the proxy looks green while the endpoint is unusable.
func TestProxy_PreflightAnsweredByTheProxyHidesTheServer(t *testing.T) {
	port := freePort(t)
	// No allowlist: the server refuses every cross-origin browser POST, which
	// is the state the deployment was actually in.
	startServerOnPort(t, port, nil)
	base := startProxy(t, port)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodOptions, base+"/proxied/", http.NoBody)
	if err != nil {
		t.Fatalf("building the preflight: %v", err)
	}
	req.Header.Set("Origin", trustedOrigin)
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	pre, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	defer pre.Body.Close()

	if pre.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight through the proxy = %d, want %d — the proxy is meant to answer it", pre.StatusCode, http.StatusNoContent)
	}

	status, _ := proxyDo(t, base, "/proxied/", trustedOrigin)
	if status != http.StatusForbidden {
		t.Fatalf("POST after a 204 preflight = %d, want %d: the proxy promised what the server refuses", status, http.StatusForbidden)
	}
	t.Log("preflight 204 from the proxy, 403 from the server: the mismatch a curl matrix cannot see")
}

// TestProxy_NoBufferingHeaderIsConsumedByTheProxy pins how the header this
// server sends for nginx's benefit actually behaves, which is not what its name
// suggests and is worth writing down.
//
// X-Accel-Buffering is an instruction to nginx, not a header for the client:
// nginx acts on it and strips it, exactly as it does with the rest of the
// X-Accel-* family. So the correct expectation is the opposite of the obvious
// one — present when a client talks to the server directly, absent once a proxy
// has read it. Asserting both halves in one test is the point: the first
// proves the server still sends it, and the second is the evidence it arrived
// somewhere that understood it.
//
// Written after asserting the obvious thing and watching it fail.
func TestProxy_NoBufferingHeaderIsConsumedByTheProxy(t *testing.T) {
	port := freePort(t)
	s := startServerOnPort(t, port, nil)
	base := startProxy(t, port)

	direct := s.do(t, mcpPOST(nil))
	if got := direct.header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering direct from the server = %q, want %q", got, "no")
	}

	_, header := proxyDo(t, base, "/plain/", "")
	if got := header.Get("X-Accel-Buffering"); got != "" {
		t.Errorf("X-Accel-Buffering through nginx = %q, want it consumed and stripped", got)
	}
}
