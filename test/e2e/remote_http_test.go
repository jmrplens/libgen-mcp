//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/prompts"
	"github.com/jmrplens/libgen-mcp/internal/tools"
)

// This file mirrors the local capability coverage (capabilities_test.go /
// remote_test.go) over the REAL streamable-HTTP transport, exactly as the public
// --http deployment runs: the server is registered in remote mode
// (tools.WithRemoteDownloads), wrapped in mcp.NewStreamableHTTPHandler, served by
// an httptest.Server, and driven by an MCP client connected over
// StreamableClientTransport. It proves the remote-specific guarantees — download
// always returns a link (never a server-side file), a local `path` read is
// rejected, resolve_only is implied — hold over HTTP, and that prompts, read, and
// open-access search behave the same as on stdio. Network-dependent cases gate on
// requireLive and SKIP (never fail) when the live site is unreachable; the prompt
// and local-path-rejection cases are DETERMINISTIC and run without LIBGEN_E2E.

// serveRemoteHTTPSession registers the given client in REMOTE mode (download
// always returns a link) plus the prompts on a fresh MCP server, serves it over a
// real streamable-HTTP transport (an httptest.Server, closed on cleanup), and
// returns an MCP client session connected over HTTP. The elicit options are passed
// through to the client so an elicitation case can supply a handler; the session
// is closed on cleanup.
func serveRemoteHTTPSession(t *testing.T, ctx context.Context, client *libgen.Client, cfg *config.Config, elicit mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "libgen-mcp-e2e", Version: "test"}, nil)
	tools.Register(server, client, cfg, tools.WithRemoteDownloads())
	prompts.Register(server, client, cfg)

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "e2e-http-client", Version: "test"}, &elicit)
	session, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatalf("client connect over HTTP: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// newRemoteHTTPSession stands up the MCP server in remote mode over a real
// streamable-HTTP transport (an httptest.Server), mirroring the public --http
// deployment, and returns a live client (size-capped so a parsing mistake can
// never pull a large file), the config it was built from, and a client session
// connected over HTTP. The elicit options are forwarded so an elicitation test can
// supply a handler.
func newRemoteHTTPSession(t *testing.T, ctx context.Context, elicit mcp.ClientOptions) (*libgen.Client, *config.Config, *mcp.ClientSession) {
	t.Helper()
	cfg := loadLiveConfig(t)
	cfg.MaxDownloadBytes = maxE2EDownloadBytes
	client := buildClient(t, cfg)
	return client, cfg, serveRemoteHTTPSession(t, ctx, client, cfg, elicit)
}

// TestE2EHTTPRemoteDownloadReturnsLink is the core remote guarantee over the real
// HTTP transport: a download by md5 (found via a search over HTTP) ALWAYS resolves
// a link and NEVER writes a server-side file. It asserts the structured output
// carries a resolved link (with a matching resource_link content block) and an
// empty Path. It gates on requireLive and SKIPS when the live chain cannot serve.
func TestE2EHTTPRemoteDownloadReturnsLink(t *testing.T) {
	requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, _, session := newRemoteHTTPSession(t, ctx, mcp.ClientOptions{})
	target := smallBookTarget(t, ctx, client, "python")
	t.Logf("http remote download target md5=%s size=%q title=%q", target.MD5, target.Size, target.Title)
	pace()

	res, out := callDownload(t, ctx, session, map[string]any{"md5": target.MD5})
	if out.Path != "" {
		t.Errorf("remote mode over HTTP must not save a file, but Path=%q", out.Path)
	}
	if out.Resolved == nil {
		t.Fatalf("remote mode over HTTP should return a resolved link; got %+v", out)
	}
	link := resourceLinkOf(res)
	if link == nil {
		t.Fatal("remote HTTP result carried no resource_link content block")
	}
	if link.URI != out.Resolved.URL {
		t.Errorf("resource_link URI %q != resolved.url %q", link.URI, out.Resolved.URL)
	}
	t.Logf("resolved over HTTP via %s url=%s", out.Resolved.Source, out.Resolved.URL)
}

// TestE2EHTTPRemoteRejectsLocalPath proves the KEY remote-specific behavior over
// HTTP: a `read` with a local `path` argument is REJECTED (a remote server cannot
// see the client's disk). It asserts the call yields a tool-error result (or a
// transport error) and that no file content is returned. It is DETERMINISTIC (the
// path is rejected before any I/O), so it runs and passes without LIBGEN_E2E.
func TestE2EHTTPRemoteRejectsLocalPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cfg := loadLiveConfig(t)
	client := buildClient(t, cfg)
	session := serveRemoteHTTPSession(t, ctx, client, cfg, mcp.ClientOptions{})

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read",
		Arguments: map[string]any{"path": "/etc/hostname"},
	})
	if err != nil {
		// A transport/protocol error also satisfies the rejection guarantee.
		t.Logf("remote local-path read rejected with a transport error: %v", err)
		return
	}
	if !res.IsError {
		t.Fatalf("remote server must reject a local-path read, but the result was not an error: %+v", res.Content)
	}
	var out tools.ReadOutput
	if res.StructuredContent != nil {
		decodeStructured(t, res, &out)
	}
	if strings.TrimSpace(out.Text) != "" {
		t.Errorf("a rejected local-path read must not return file text, got %d bytes", len(out.Text))
	}
	t.Logf("remote local-path read rejected as a tool error: %s", textOf(res))
}

// TestE2EHTTPRemoteReadByMD5 proves read-by-md5 still works remotely over HTTP:
// the server fetches the file server-side and returns extractable text that leads
// with the UNTRUSTED warning (only a local `path` is rejected remotely). It gates
// on requireLive and SKIPS when the target is not extractable or the live fetch
// hiccups.
func TestE2EHTTPRemoteReadByMD5(t *testing.T) {
	requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	client, _, session := newRemoteHTTPSession(t, ctx, mcp.ClientOptions{})
	target := smallestTargetIn(t, ctx, client, "nonfiction", "python")
	t.Logf("http remote read target md5=%s size=%q ext=%q", target.MD5, target.Size, target.Extension)
	pace()

	out := callRead(t, ctx, session, map[string]any{"md5": target.MD5})
	assertUntrustedFirst(t, out)
	if !out.Extractable {
		t.Skipf("target %s is not extractable (%s); read-by-md5 needs extractable text", target.MD5, out.Reason)
	}
	if strings.TrimSpace(out.Text) == "" {
		t.Error("remote read-by-md5 returned no text for an extractable file")
	}
	t.Logf("remote read-by-md5 over HTTP returned %d bytes of text", len(out.Text))
}

// TestE2EHTTPRemoteSearchOpenAccess proves open-access discovery works over HTTP:
// a search with extra_sources=always returns OA hits labeled by a known
// origin. It gates on requireLive and SKIPS when the open-access providers return
// nothing.
func TestE2EHTTPRemoteSearchOpenAccess(t *testing.T) {
	requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, _, session := newRemoteHTTPSession(t, ctx, mcp.ClientOptions{})
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "graphene", "topics": []string{"articles"}, "extra_sources": "always"},
	})
	if err != nil {
		t.Fatalf("CallTool(search) over HTTP error: %v", err)
	}
	if res.IsError {
		t.Fatalf("search tool returned an error result over HTTP: %+v", res.Content)
	}
	var out tools.SearchOutput
	decodeStructured(t, res, &out)
	if len(out.OpenAccess) == 0 {
		t.Skip("no open-access hits over HTTP (providers unreachable or no matches); skipping")
	}
	for i := range out.OpenAccess {
		if !validOrigin(out.OpenAccess[i].Origin) {
			t.Errorf("open-access hit %d has an unexpected origin %q", i, out.OpenAccess[i].Origin)
		}
	}
	t.Logf("open_access hits over HTTP=%d", len(out.OpenAccess))
}

// TestE2EHTTPRemotePrompts proves the prompt surface works over the HTTP
// transport: ListPrompts advertises the four prompts and GetPrompt(get_paper) for
// a bare DOI returns download guidance in a user-role message. It is
// DETERMINISTIC (prompt discovery and the DOI path perform no search), so it runs
// and passes without LIBGEN_E2E.
func TestE2EHTTPRemotePrompts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cfg := offlineConfig(t)
	session := serveRemoteHTTPSession(t, ctx, offlineClient(t, cfg), cfg, mcp.ClientOptions{})

	list, err := session.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("ListPrompts over HTTP error: %v", err)
	}
	got := make(map[string]bool, len(list.Prompts))
	for _, p := range list.Prompts {
		got[p.Name] = true
	}
	for _, name := range wantPromptNames {
		if !got[name] {
			t.Errorf("prompt %q not advertised over HTTP; got %v", name, got)
		}
	}

	const doi = "10.1371/journal.pmed.0020124"
	res, err := session.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "get_paper",
		Arguments: map[string]string{"doi": doi},
	})
	if err != nil {
		t.Fatalf("GetPrompt(get_paper) over HTTP error: %v", err)
	}
	txt := promptUserText(t, res)
	if !strings.Contains(txt, "download") || !strings.Contains(txt, doi) {
		t.Errorf("get_paper DOI guidance over HTTP should mention download and echo the DOI:\n%s", txt)
	}
}

// TestE2EHTTPRemoteResolveOnlyImplied proves resolve_only is IMPLIED in remote
// mode over HTTP: a plain download (no resolve_only argument) already returns a
// resolved link and writes no server-side file. It gates on requireLive and SKIPS
// when the live chain cannot serve.
func TestE2EHTTPRemoteResolveOnlyImplied(t *testing.T) {
	requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, _, session := newRemoteHTTPSession(t, ctx, mcp.ClientOptions{})
	target := smallBookTarget(t, ctx, client, "python")
	t.Logf("http resolve_only-implied target md5=%s size=%q", target.MD5, target.Size)
	pace()

	// No resolve_only argument is passed: remote mode must imply it.
	_, out := callDownload(t, ctx, session, map[string]any{"md5": target.MD5})
	if out.Resolved == nil {
		t.Fatalf("remote mode should imply resolve_only and return a link without it; got %+v", out)
	}
	if out.Path != "" {
		t.Errorf("remote mode must not save a file even without resolve_only, but Path=%q", out.Path)
	}
	t.Logf("resolve_only implied over HTTP: resolved via %s url=%s", out.Resolved.Source, out.Resolved.URL)
}

// TestE2EHTTPRemoteElicitationOverHTTP proves elicitation works over the real HTTP
// transport the same as over stdio. It is DETERMINISTIC (httptest Unpaywall stub,
// no live network): a client that advertises an ElicitationHandler answering the
// email prompt drives a resolve_only DOI download whose server has NO configured
// email, so Unpaywall can only be reached through the elicited per-call email. It
// asserts the server-initiated elicitation reached the client OVER HTTP (one
// lookup) and the elicited email threaded through to Unpaywall, resolving the link.
func TestE2EHTTPRemoteElicitationOverHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	base, lookups, lastEmail := unpaywallStub(t)
	cfg := elicitEmailConfig(t)
	client := libgen.New(staticMirrors{}, cfg, libgen.WithUnpaywallBaseURL(base))

	var elicits atomic.Int32
	handler := func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		elicits.Add(1)
		field := elicitFieldName(req)
		if field == "" {
			field = "email"
		}
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{field: "http-asked@example.com"}}, nil
	}
	session := serveRemoteHTTPSession(t, ctx, client, cfg, mcp.ClientOptions{ElicitationHandler: handler})

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"doi": "10.1/x", "resolve_only": true},
	})
	if err != nil {
		t.Fatalf("CallTool(download) over HTTP transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("an accepted-email download should not be a tool error over HTTP: %+v", res.Content)
	}
	if elicits.Load() == 0 {
		t.Fatal("the elicitation handler was never invoked over the HTTP transport")
	}
	var out tools.DownloadOutput
	decodeStructured(t, res, &out)
	if out.Resolved == nil || out.Resolved.Source != "unpaywall" {
		t.Fatalf("expected a resolved link from unpaywall via the elicited email, got %+v", out.Resolved)
	}
	if lookups.Load() != 1 {
		t.Errorf("Unpaywall lookups = %d, want 1 (elicited over HTTP)", lookups.Load())
	}
	if *lastEmail != "http-asked@example.com" {
		t.Errorf("Unpaywall received email = %q, want the HTTP-elicited %q", *lastEmail, "http-asked@example.com")
	}
}
