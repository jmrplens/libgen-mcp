package tools

import (
	"context"
	"crypto/md5" //nolint:gosec // integrity digest, not a security primitive.
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
)

// bodyCountingMirror builds a libgen-style mirror (search → ads.php → get.php →
// CDN) that counts every request for a file BODY separately from the light
// requests a resolve makes, so a test can assert the exact property this change
// is about: the server may talk to the mirror, but must not pull the file.
func bodyCountingMirror(t *testing.T, payload []byte, bodyHits *atomic.Int32) *httptest.Server {
	t.Helper()
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<html><a href="get.php?md5=%s&key=TESTKEY123">GET</a></html>`, wantMD5)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/cdn/file", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/cdn/file", func(w http.ResponseWriter, _ *http.Request) {
		bodyHits.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="book.pdf"`)
		w.Write(payload)
	})
	// index.php answers the catalog search, so a no-fetch server can still be
	// asked for one and the test can prove that call fetched no file either.
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<html><body><table><tr><th>Title</th></tr></table></body></html>`)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// noFetchSession registers the four tools with the given options against a mirror
// that counts body fetches, and returns the connected client session, the mirror,
// the md5 of the file it serves and the download directory.
func noFetchSession(t *testing.T, bodyHits *atomic.Int32, opts ...RegisterOption) (session *mcp.ClientSession, srv *httptest.Server, fileMD5, dir string) {
	t.Helper()
	payload := []byte("%PDF-1.4 the server must not fetch this")
	srv = bodyCountingMirror(t, payload, bodyHits)
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	fileMD5 = hex.EncodeToString(sum[:])

	dir = t.TempDir()
	cfg := &config.Config{
		DownloadDir: dir, Timeout: 5 * time.Second,
		RateRPS: 1000, RateBurst: 100, RetryAttempts: 1,
		ReadMaxChars: 6000, ReadDefaultPages: 5,
		ReadCacheBytes: 1 << 20, ReadCacheTTL: time.Minute,
		// The stub catalog answers every search with no results, which under the
		// default policy would escalate to the real open-access providers. This
		// test is about what the server fetches, so it stays on the fixture.
		ExtraSources: config.ExtraSourcesNever,
	}
	client := libgen.New(staticMirrors{srv.URL}, cfg)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	Register(server, client, cfg, opts...)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session, srv, fileMD5, dir
}

// toolNames lists the tools a session advertises.
func toolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tl := range listed.Tools {
		names = append(names, tl.Name)
	}
	return names
}

// TestNoServerFetchPullsNoFileBody is the regression guard for the whole change:
// on a remote deployment that has not enabled server-side fetching, no call to
// the advertised surface may pull a file's body. It drives every tool the server
// offers and asserts the mirror's CDN was never asked for the file, because the
// happy path — download returning a link — keeps working either way, so nothing
// else would notice the day this regresses.
func TestNoServerFetchPullsNoFileBody(t *testing.T) {
	var bodyHits atomic.Int32
	session, srv, fileMD5, dir := noFetchSession(t, &bodyHits, WithRemoteDownloads(), WithoutServerFetch())
	ctx := context.Background()

	calls := []struct {
		tool string
		args map[string]any
	}{
		{"search", map[string]any{"query": "chemistry"}},
		{"get_details", map[string]any{"md5": fileMD5}},
		{"download", map[string]any{"md5": fileMD5}},
		// resolve_only=false is the argument that would ask for a saved file;
		// a no-fetch server must still answer with a link, not with bytes.
		{"download", map[string]any{"md5": fileMD5, "resolve_only": false}},
	}
	for _, call := range calls {
		// Errors are fine here (a stub mirror answers little); fetching is not.
		if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: call.tool, Arguments: call.args}); err != nil {
			t.Logf("%s returned %v (acceptable; the assertion is about fetching)", call.tool, err)
		}
	}

	if got := bodyHits.Load(); got != 0 {
		t.Errorf("the server fetched a file body %d time(s) from %s, want 0", got, srv.URL)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the server wrote %d file(s) to disk, want 0", len(entries))
	}
}

// TestNoServerFetchHidesRead asserts read is absent from tools/list rather than
// present and failing. A tool a model can see is a tool it will call, so leaving
// it listed would spend a turn per attempt to return the same refusal.
func TestNoServerFetchHidesRead(t *testing.T) {
	var bodyHits atomic.Int32
	session, _, fileMD5, _ := noFetchSession(t, &bodyHits, WithRemoteDownloads(), WithoutServerFetch())
	ctx := context.Background()

	names := toolNames(t, session)
	for _, name := range names {
		if name == "read" {
			t.Fatalf("read is advertised on a no-fetch server; tools = %v", names)
		}
	}
	// The three that do not pull a body stay, so the surface is trimmed, not gutted.
	for _, want := range []string{"search", "get_details", "download"} {
		if !contains(names, want) {
			t.Errorf("%s is missing; tools = %v", want, names)
		}
	}

	// And the unadvertised tool is genuinely unreachable, not merely unlisted.
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read", Arguments: map[string]any{"md5": fileMD5}})
	if err == nil && (res == nil || !res.IsError) {
		t.Errorf("calling the unlisted read tool succeeded: %+v", res)
	}
	if got := bodyHits.Load(); got != 0 {
		t.Errorf("a call to the unlisted read tool fetched %d body/bodies, want 0", got)
	}
}

// TestNoServerFetchForcesDownloadLinks asserts the option's second effect: a
// server that may not fetch bodies returns a link even when it is local, since
// saving to disk is a fetch like any other.
func TestNoServerFetchForcesDownloadLinks(t *testing.T) {
	var bodyHits atomic.Int32
	// No WithRemoteDownloads: this is a LOCAL server whose operator turned
	// fetching off.
	session, _, fileMD5, dir := noFetchSession(t, &bodyHits, WithoutServerFetch())
	ctx := context.Background()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "download", Arguments: map[string]any{"md5": fileMD5}})
	if err != nil || res.IsError {
		t.Fatalf("CallTool: err=%v result=%+v", err, res)
	}
	var out DownloadOutput
	data, merr := json.Marshal(res.StructuredContent)
	if merr != nil {
		t.Fatal(merr)
	}
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatal(uerr)
	}
	if out.Resolved == nil {
		t.Fatalf("download saved a file instead of resolving a link; got %s", data)
	}
	if out.Path != "" {
		t.Errorf("download reported a saved path %q on a no-fetch server", out.Path)
	}
	if bodyHits.Load() != 0 {
		t.Errorf("download fetched the body %d time(s), want 0", bodyHits.Load())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("download wrote %d file(s) to disk, want 0", len(entries))
	}
}

// TestRemoteServerFetchOptInKeepsRead asserts the opt-in direction: an operator
// who allows fetching on a remote deployment gets read back, still without the
// path argument, which a remote server could never resolve on the client's disk.
func TestRemoteServerFetchOptInKeepsRead(t *testing.T) {
	var bodyHits atomic.Int32
	session, _, _, _ := noFetchSession(t, &bodyHits, WithRemoteDownloads())

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var read *mcp.Tool
	for _, tl := range listed.Tools {
		if tl.Name == "read" {
			read = tl
		}
	}
	if read == nil {
		t.Fatal("read is missing from a remote server that allows fetching")
	}
	// The listed schema arrives as wire JSON, so it is read the way a client
	// reads it rather than through the Go type that produced it.
	schema, ok := read.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("read InputSchema = %T, want a JSON object", read.InputSchema)
	}
	props, _ := schema["properties"].(map[string]any)
	if _, has := props["path"]; has {
		t.Error("read still offers path on a remote server")
	}
}

// TestNoServerFetchDownloadDescriptionExplainsItself asserts the download tool
// still tells a model what it will get, since on a no-fetch local server the
// link-only behavior would otherwise contradict the description.
func TestNoServerFetchDownloadDescriptionExplainsItself(t *testing.T) {
	var bodyHits atomic.Int32
	session, _, _, _ := noFetchSession(t, &bodyHits, WithoutServerFetch())

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range listed.Tools {
		if tl.Name != "download" {
			continue
		}
		if !strings.Contains(tl.Description, "link") {
			t.Errorf("download description does not mention returning a link:\n%s", tl.Description)
		}
		if tl.Annotations == nil || tl.Annotations.DestructiveHint == nil || *tl.Annotations.DestructiveHint {
			t.Error("download should not be destructive on a server that writes no files")
		}
		return
	}
	t.Fatal("download is missing")
}

// contains reports whether names holds want.
func contains(names []string, want string) bool { return slices.Contains(names, want) }

// TestLinkOnlyResolveOnlyDescriptionIsHonest asserts the one argument a model
// sets to choose between the two behaviors does not describe the behavior this
// deployment cannot honor. On a link-only server `resolve_only: false` does not
// save to disk, so a schema that says it does is telling the model to expect a
// file it will never get — the same defect the tool's own description was fixed
// for, one field lower down.
func TestLinkOnlyResolveOnlyDescriptionIsHonest(t *testing.T) {
	var bodyHits atomic.Int32
	session, _, _, _ := noFetchSession(t, &bodyHits, WithoutServerFetch())

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range listed.Tools {
		if tl.Name != "download" {
			continue
		}
		schema, ok := tl.InputSchema.(map[string]any)
		if !ok {
			t.Fatalf("download InputSchema = %T, want a JSON object", tl.InputSchema)
		}
		props, _ := schema["properties"].(map[string]any)
		field, _ := props["resolve_only"].(map[string]any)
		desc, _ := field["description"].(string)
		if desc == "" {
			t.Fatal("resolve_only has no description")
		}
		if strings.Contains(desc, "saves to the server's disk") {
			t.Errorf("a link-only server must not say resolve_only=false saves to disk; got:\n%s", desc)
		}
		if !strings.Contains(desc, "always") {
			t.Errorf("resolve_only should say a link is always returned here; got:\n%s", desc)
		}
		return
	}
	t.Fatal("download is missing")
}

// TestSavingServerKeepsTheResolveOnlyContrast is the other half: where the
// server does save files, the argument must still say so, since that is the
// choice it exists to offer.
func TestSavingServerKeepsTheResolveOnlyContrast(t *testing.T) {
	var bodyHits atomic.Int32
	session, _, _, _ := noFetchSession(t, &bodyHits)

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range listed.Tools {
		if tl.Name != "download" {
			continue
		}
		schema, _ := tl.InputSchema.(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		field, _ := props["resolve_only"].(map[string]any)
		desc, _ := field["description"].(string)
		if !strings.Contains(desc, "saves to the server's disk") {
			t.Errorf("a saving server should keep the default-saves wording; got:\n%s", desc)
		}
		return
	}
	t.Fatal("download is missing")
}
