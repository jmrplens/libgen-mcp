//go:build e2e

package e2e

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/tools"
)

// mcpDownloadEnv bundles a size-capped live client, the config it was built from
// and a connected in-memory MCP session whose `download` tool is wired to that
// client. It is the live analog of the eval's download harness.
type mcpDownloadEnv struct {
	client  *libgen.Client
	cfg     *config.Config
	session *mcp.ClientSession
}

// newMCPDownloadEnv builds a live client (capped at maxE2EDownloadBytes so a
// size-parsing mistake can never pull a large file) and connects an in-memory MCP
// server exposing it. When remote is true the download tool is registered in
// remote mode (tools.WithRemoteDownloads), so it always returns a link instead of
// saving a file. The session is closed on test cleanup.
func newMCPDownloadEnv(t *testing.T, ctx context.Context, remote bool) *mcpDownloadEnv {
	t.Helper()
	cfg := loadLiveConfig(t)
	cfg.MaxDownloadBytes = maxE2EDownloadBytes
	client := buildClient(t, cfg)

	server := mcp.NewServer(&mcp.Implementation{Name: "libgen-mcp-e2e", Version: "test"}, nil)
	if remote {
		tools.Register(server, client, cfg, tools.WithRemoteDownloads())
	} else {
		tools.Register(server, client, cfg)
	}

	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "e2e-client", Version: "test"}, nil)
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &mcpDownloadEnv{client: client, cfg: cfg, session: session}
}

// smallBookTarget searches nonfiction ordered by ascending size and returns the
// smallest downloadable result (canonical md5, parseable non-zero size within the
// polite cap). It skips the calling test when no such target is available, to stay
// a polite citizen of the public mirrors.
func smallBookTarget(t *testing.T, ctx context.Context, client *libgen.Client, query string) libgen.Result {
	t.Helper()
	page, _, err := client.Search(ctx, libgen.SearchParams{
		Query: query, Topics: []string{"nonfiction"}, Order: "size", OrderMode: "asc",
	})
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	target := smallestDownloadable(page.Results)
	if target.MD5 == "" {
		t.Skip("no small downloadable target found; skipping to stay polite")
	}
	return target
}

// callDownload invokes the `download` tool and decodes its structured output. It
// skips the calling test (never fails) when the live chain cannot serve — a
// transport error, or a tool error result from an expired key / blocked CDN /
// unavailable source — since a live hiccup is not a suite failure.
func callDownload(t *testing.T, ctx context.Context, session *mcp.ClientSession, args map[string]any) (*mcp.CallToolResult, tools.DownloadOutput) {
	t.Helper()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "download", Arguments: args})
	if err != nil {
		t.Skipf("download tool call failed live: %v", err)
	}
	if res.IsError {
		t.Skipf("download tool returned an error result (live chain unavailable): %+v", res.Content)
	}
	var out tools.DownloadOutput
	decodeStructured(t, res, &out)
	return res, out
}

// resourceLinkOf returns the first *mcp.ResourceLink content block in res, or nil
// when the result carries none.
func resourceLinkOf(res *mcp.CallToolResult) *mcp.ResourceLink {
	for _, c := range res.Content {
		if rl, ok := c.(*mcp.ResourceLink); ok {
			return rl
		}
	}
	return nil
}

// fetchResolvedLink acts as the agent's own fetch tool: it HTTP GETs the resolved
// link (applying any resolver-supplied headers and a User-Agent), caps the body at
// maxE2EDownloadBytes with an io.LimitReader, and writes it into dir. It returns
// the path to the fetched file, and skips the calling test on any transport or
// non-2xx status, since a live fetch hiccup is not a suite failure.
func fetchResolvedLink(t *testing.T, ctx context.Context, link *tools.ResolvedLink, dir string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, http.NoBody)
	if err != nil {
		t.Skipf("building fetch request: %v", err)
	}
	for k, v := range link.Headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "libgen-mcp-e2e")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("fetching resolved link: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Skipf("resolved link returned status %d", resp.StatusCode)
	}

	name := strings.TrimSpace(link.Filename)
	if name == "" {
		name = "fetched.bin"
	}
	path := filepath.Join(dir, filepath.Base(name))
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fetched file: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, cerr := io.Copy(f, io.LimitReader(resp.Body, maxE2EDownloadBytes)); cerr != nil {
		t.Skipf("copying fetched body: %v", cerr)
	}
	return path
}

// assertFetchedFile asserts the file at path exists, is non-empty, and is not an
// HTML error page (its first bytes are not an HTML document marker) — proving the
// fetch landed a real payload rather than a block/error page.
func assertFetchedFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("fetched file missing: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("fetched file is empty: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	if looksLikeHTML(head[:n]) {
		t.Errorf("fetched file looks like an HTML error page, not a real file: %s", path)
	}
}

// TestE2EMCPDownloadLocalToDisk is the local block (analog of the eval's
// S5/S12/S13): a server registered WITHOUT the remote option saves the file to
// disk by default. It finds a small nonfiction book (size asc + smallestDownloadable)
// and calls the `download` tool with no resolve_only, then asserts the structured
// output reports a saved file on disk that exists and is non-empty (and carries no
// resolved link). It skips gracefully when the live download cannot complete.
func TestE2EMCPDownloadLocalToDisk(t *testing.T) {
	requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	env := newMCPDownloadEnv(t, ctx, false)
	target := smallBookTarget(t, ctx, env.client, "python")
	t.Logf("local download target md5=%s size=%q title=%q", target.MD5, target.Size, target.Title)
	pace()

	res, out := callDownload(t, ctx, env.session, map[string]any{"md5": target.MD5, "path": t.TempDir()})
	if out.Resolved != nil {
		t.Errorf("local mode must save a file, not resolve a link: %+v", out.Resolved)
	}
	if out.Path == "" {
		t.Fatalf("local download reported no saved path: %+v", out)
	}
	info, err := os.Stat(out.Path)
	if err != nil {
		t.Fatalf("saved file missing on disk: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("saved file is empty: %s", out.Path)
	}
	t.Logf("saved md5=%s bytes=%d source=%s path=%s markdown=%d bytes",
		target.MD5, out.SizeBytes, out.Source, out.Path, len(textOf(res)))
}

// TestE2EMCPDownloadRemoteReturnsLinkAndFetches is the remote block (the important
// one; analog of the eval's S17/S18). A server registered WITH
// tools.WithRemoteDownloads always returns a link. It asserts the result did NOT
// save a file (structured `resolved` present, `Path` empty, a resource_link content
// block present), then — acting as the agent's own fetch tool — HTTP GETs the
// resolved URL, lands it into a temp dir, and asserts the fetched file is non-empty
// and NOT an HTML error page. This proves the remote block delivers the real file
// locally. It skips gracefully when resolve or the live fetch fails.
func TestE2EMCPDownloadRemoteReturnsLinkAndFetches(t *testing.T) {
	requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	env := newMCPDownloadEnv(t, ctx, true)
	target := smallBookTarget(t, ctx, env.client, "python")
	t.Logf("remote download target md5=%s size=%q title=%q", target.MD5, target.Size, target.Title)
	pace()

	res, out := callDownload(t, ctx, env.session, map[string]any{"md5": target.MD5})

	// Remote mode returns a link, never a saved file.
	if out.Path != "" {
		t.Errorf("remote mode must not save a file, but Path=%q", out.Path)
	}
	if out.Resolved == nil {
		t.Fatalf("remote mode should return a resolved link; got %+v", out)
	}
	link := resourceLinkOf(res)
	if link == nil {
		t.Fatal("remote result carried no resource_link content block")
	}
	if link.URI != out.Resolved.URL {
		t.Errorf("resource_link URI %q != resolved.url %q", link.URI, out.Resolved.URL)
	}
	t.Logf("resolved via %s url=%s verify_md5=%v headers=%d",
		out.Resolved.Source, out.Resolved.URL, out.Resolved.VerifyMD5, len(out.Resolved.Headers))

	// Act as the agent's own fetch tool: GET the resolved URL and land the file
	// locally, then prove it is a real (non-HTML) payload.
	path := fetchResolvedLink(t, ctx, out.Resolved, t.TempDir())
	assertFetchedFile(t, path)
	t.Logf("remote block landed a real file locally: %s", path)
}

// TestE2EMCPResolveOnly drives a LOCAL server but calls `download` with
// resolve_only=true and asserts a resolved link comes back (structured `resolved`
// plus a matching resource_link block) and no file is written. It is cheap: it does
// not fetch the payload.
func TestE2EMCPResolveOnly(t *testing.T) {
	requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	env := newMCPDownloadEnv(t, ctx, false)
	target := smallBookTarget(t, ctx, env.client, "python")
	t.Logf("resolve_only target md5=%s size=%q", target.MD5, target.Size)
	pace()

	res, out := callDownload(t, ctx, env.session, map[string]any{"md5": target.MD5, "resolve_only": true})
	if out.Resolved == nil {
		t.Fatalf("resolve_only should return a resolved link; got %+v", out)
	}
	if out.Path != "" {
		t.Errorf("resolve_only must not save a file, but Path=%q", out.Path)
	}
	if link := resourceLinkOf(res); link == nil || link.URI != out.Resolved.URL {
		t.Errorf("resolve_only should carry a resource_link matching resolved.url; got %v", link)
	}
	t.Logf("resolve_only via %s url=%s (no file fetched)", out.Resolved.Source, out.Resolved.URL)
}

// TestE2EMCPRemoteArticleByDOI is the remote block for an open-access article: a
// remote server resolves the DOI to a link, then the agent's own fetch tool GETs it
// and lands a PDF locally. It skips gracefully since OA availability varies.
func TestE2EMCPRemoteArticleByDOI(t *testing.T) {
	requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	env := newMCPDownloadEnv(t, ctx, true)
	// PLOS Medicine, reliably open access (Ioannidis 2005), so Unpaywall exposes a
	// PDF link for it.
	const oaDOI = "10.1371/journal.pmed.0020124"
	res, out := callDownload(t, ctx, env.session, map[string]any{"doi": oaDOI})
	if out.Resolved == nil {
		t.Fatalf("remote article should resolve a link; got %+v", out)
	}
	if resourceLinkOf(res) == nil {
		t.Fatal("remote article result carried no resource_link content block")
	}
	t.Logf("resolved OA article via %s url=%s", out.Resolved.Source, out.Resolved.URL)

	path := fetchResolvedLink(t, ctx, out.Resolved, t.TempDir())
	assertFetchedFile(t, path)
	assertPDF(t, path)
	t.Logf("remote OA article doi=%s landed a PDF locally: %s", oaDOI, path)
}
