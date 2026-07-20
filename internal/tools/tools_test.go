package tools

import (
	"context"
	"crypto/md5" //nolint:gosec // tests compute the LibGen file digest for integrity assertions.
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
)

type staticMirrors []string

func (s staticMirrors) Mirrors(context.Context) []string { return s }

// newSession spins up an MCP server plus an in-memory client with an httptest
// mirror that serves the libgen package fixtures.
func newSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	searchHTML, err := os.ReadFile("../libgen/testdata/search_books.html")
	if err != nil {
		t.Fatal(err)
	}
	fileJSON, _ := os.ReadFile("../libgen/testdata/file_by_md5.json")
	editionJSON, _ := os.ReadFile("../libgen/testdata/edition.json")
	mux := http.NewServeMux()
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, r *http.Request) { w.Write(searchHTML) })
	mux.HandleFunc("/json.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("object") == "f" {
			w.Write(fileJSON)
		} else {
			w.Write(editionJSON)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	client := libgen.New(staticMirrors{srv.URL}, cfg)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	Register(server, client, cfg)

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, cerr := server.Connect(ctx, st, nil); cerr != nil {
		t.Fatal(cerr)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// TestHandlerRecoversPanic verifies HandlerRecoversPanic.
func TestHandlerRecoversPanic(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	type panicIn struct{}
	type panicOut struct{}
	handler := mcp.ToolHandlerFor[panicIn, panicOut](
		func(context.Context, *mcp.CallToolRequest, panicIn) (*mcp.CallToolResult, panicOut, error) {
			panic("boom")
		})
	mcp.AddTool(server, &mcp.Tool{Name: "boom", Description: "panics on purpose for testing"},
		withRecovery("boom", handler))

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "boom", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("panic escaped as a protocol error instead of being recovered: %v", err)
	}
	if !res.IsError {
		t.Fatal("recovered panic should produce an IsError tool result")
	}
	if len(res.Content) == 0 {
		t.Fatal("recovered panic should include a helpful error message")
	}
}

// TestToolsRegistered verifies ToolsRegistered.
func TestToolsRegistered(t *testing.T) {
	session := newSession(t)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"search", "get_details", "download"} {
		if !names[want] {
			t.Errorf("missing tool %q; registered: %v", want, names)
		}
	}
	if len(res.Tools) != 3 {
		t.Errorf("got %d tools, want 3", len(res.Tools))
	}
}

// TestSearchTool verifies SearchTool.
func TestSearchTool(t *testing.T) {
	session := newSession(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "golang", "topics": []string{"nonfiction"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Results []struct {
			MD5   string `json:"md5"`
			Title string `json:"title"`
		} `json:"results"`
		HasMore bool `json:"has_more"`
	}
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatal(uerr)
	}
	if len(out.Results) == 0 || out.Results[0].MD5 == "" {
		t.Errorf("resultados inesperados: %+v", out)
	}
}

// TestSearchToolTruncated verifies that the search tool surfaces the pagination
// cap: reachable, truncated and a refine hint when the advertised total exceeds
// the reachable results.
func TestSearchToolTruncated(t *testing.T) {
	truncHTML, err := os.ReadFile("../libgen/testdata/search_truncated.html")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, _ *http.Request) { w.Write(truncHTML) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	client := libgen.New(staticMirrors{srv.URL}, cfg)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	Register(server, client, cfg)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, cerr := server.Connect(ctx, st, nil); cerr != nil {
		t.Fatal(cerr)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "physics", "results_per_page": 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		TotalFiles string `json:"total_files"`
		Reachable  int    `json:"reachable"`
		Truncated  bool   `json:"truncated"`
		Hint       string `json:"hint"`
	}
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatal(uerr)
	}
	if out.Reachable != 2000 {
		t.Errorf("reachable = %d, want 2000", out.Reachable)
	}
	if !out.Truncated {
		t.Errorf("truncated = false, want true")
	}
	if !strings.Contains(out.Hint, "2000") || !strings.Contains(out.Hint, out.TotalFiles) || !strings.Contains(out.Hint, "refine") {
		t.Errorf("hint = %q, want it to mention 2000, %s and refine", out.Hint, out.TotalFiles)
	}
}

// TestSearchToolNotTruncated verifies that a non-truncated search omits the
// hint and reports truncated=false.
func TestSearchToolNotTruncated(t *testing.T) {
	session := newSession(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "golang", "topics": []string{"nonfiction"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Reachable int    `json:"reachable"`
		Truncated bool   `json:"truncated"`
		Hint      string `json:"hint"`
	}
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatal(uerr)
	}
	if out.Reachable != 150 {
		t.Errorf("reachable = %d, want 150", out.Reachable)
	}
	if out.Truncated {
		t.Errorf("truncated = true, want false")
	}
	if out.Hint != "" {
		t.Errorf("hint = %q, want empty", out.Hint)
	}
}

// TestSearchToolBadTopic verifies SearchToolBadTopic.
func TestSearchToolBadTopic(t *testing.T) {
	session := newSession(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "x", "topics": []string{"cooking"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("invalid topic should return a tool error")
	}
}

// TestGetDetailsTool verifies GetDetailsTool.
func TestGetDetailsTool(t *testing.T) {
	session := newSession(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_details",
		Arguments: map[string]any{"md5": "87a4ebdaf21fa6cc70009a3dd63194ee"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "87a4ebdaf21fa6cc70009a3dd63194ee") {
		t.Errorf("output without md5: %s", data)
	}
}

// TestGetDetailsToolValidation verifies GetDetailsToolValidation.
func TestGetDetailsToolValidation(t *testing.T) {
	session := newSession(t)
	for _, args := range []map[string]any{
		{},
		{"md5": "87a4ebdaf21fa6cc70009a3dd63194ee", "id": "1"},
	} {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_details", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Errorf("args %v should return a tool error", args)
		}
	}
}

// TestGetDetailsToolByID exercises the id lookup branch of the details handler:
// an edition id (default object), a file id (object=file), and a rejected object.
func TestGetDetailsToolByID(t *testing.T) {
	session := newSession(t)
	ctx := context.Background()

	edRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_details",
		Arguments: map[string]any{"id": "138281637"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if edRes.IsError {
		t.Fatalf("get_details by edition id error: %v", edRes.Content)
	}
	edData, err := json.Marshal(edRes.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(edData), "edition") {
		t.Errorf("edition lookup output missing edition: %s", edData)
	}

	fileRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_details",
		Arguments: map[string]any{"id": "93485370", "object": "file"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fileRes.IsError {
		t.Fatalf("get_details by file id error: %v", fileRes.Content)
	}

	badRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_details",
		Arguments: map[string]any{"id": "1", "object": "chapter"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !badRes.IsError {
		t.Error("get_details with an invalid object should be a tool error")
	}
}

// TestGetDetailsToolBadMD5 verifies the tool rejects a syntactically invalid md5
// (not 32 hex chars) before any lookup.
func TestGetDetailsToolBadMD5(t *testing.T) {
	session := newSession(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_details",
		Arguments: map[string]any{"md5": "not-a-valid-md5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("get_details with a malformed md5 should be a tool error")
	}
}

// TestDownloadToolBadMD5 verifies the tool rejects a syntactically invalid md5.
func TestDownloadToolBadMD5(t *testing.T) {
	session := newSession(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": "xyz"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("download with a malformed md5 should be a tool error")
	}
}

// TestDownloadToolBadSource verifies the tool rejects an unknown source name.
func TestDownloadToolBadSource(t *testing.T) {
	session := newSession(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": "87a4ebdaf21fa6cc70009a3dd63194ee", "source": "nosuchsource"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("download with an unknown source should be a tool error")
	}
}

// md5ErrSource is a DownloadSource that supports md5 items but always fails to
// resolve, so the download handler's error path can be exercised without a network.
type md5ErrSource struct{}

func (md5ErrSource) Name() string                 { return "boom" }
func (md5ErrSource) Supports(it libgen.Item) bool { return it.MD5 != "" }
func (md5ErrSource) Resolve(context.Context, libgen.Item) (libgen.Resolved, error) {
	return libgen.Resolved{}, errors.New("resolve failed")
}

// TestDownloadToolResolveError verifies that a source-resolution failure surfaces
// as a tool error from the download handler.
func TestDownloadToolResolveError(t *testing.T) {
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	session := newDownloadSession(t, cfg, staticMirrors{}, libgen.WithSources(md5ErrSource{}))
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": "87a4ebdaf21fa6cc70009a3dd63194ee"},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if !res.IsError {
		t.Fatal("a download whose only source fails to resolve should be a tool error")
	}
}

// TestSearchToolEmptyResults verifies the handler normalizes a zero-result page to
// an empty (non-nil) results slice.
func TestSearchToolEmptyResults(t *testing.T) {
	emptyHTML, err := os.ReadFile("../libgen/testdata/search_empty.html")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, _ *http.Request) { w.Write(emptyHTML) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	session := newDownloadSession(t, cfg, staticMirrors{srv.URL})

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "nothingmatches"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("empty search returned a tool error: %v", res.Content)
	}
	var out struct {
		Results []any `json:"results"`
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatal(uerr)
	}
	if out.Results == nil {
		t.Error("results should be a non-nil empty array, got null")
	}
	if len(out.Results) != 0 {
		t.Errorf("results = %d, want 0", len(out.Results))
	}
}

// TestStringField verifies the record-field reader handles a nil map, a missing
// key, a non-string value, and a trimmed string value.
func TestStringField(t *testing.T) {
	if got := stringField(nil, "title"); got != "" {
		t.Errorf("stringField(nil) = %q, want empty", got)
	}
	m := map[string]any{"title": "  Go  ", "pages": 300}
	if got := stringField(m, "title"); got != "Go" {
		t.Errorf("stringField(title) = %q, want %q", got, "Go")
	}
	if got := stringField(m, "pages"); got != "" {
		t.Errorf("stringField(non-string) = %q, want empty", got)
	}
	if got := stringField(m, "absent"); got != "" {
		t.Errorf("stringField(absent) = %q, want empty", got)
	}
}

// TestBookMetaEmptyReturnsNil verifies bookMeta returns nil when the record lookup
// yields no usable bibliographic fields, so naming falls back to the md5.
func TestBookMetaEmptyReturnsNil(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/json.php", func(w http.ResponseWriter, _ *http.Request) {
		// A file record with no title/author/year/extension and no related edition.
		_, _ = w.Write([]byte(`{"93485370":{"md5":"87a4ebdaf21fa6cc70009a3dd63194ee"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	client := libgen.New(staticMirrors{srv.URL}, cfg)
	if meta := bookMeta(context.Background(), client, "87a4ebdaf21fa6cc70009a3dd63194ee"); meta != nil {
		t.Errorf("bookMeta() = %+v, want nil (no usable fields)", meta)
	}
}

// downloadMirror serves the ads.php -> get.php -> CDN chain for a payload whose
// md5 it advertises, so the download tool can run end to end against httptest.
func downloadMirror(t *testing.T, payload []byte) *httptest.Server {
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
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="book.pdf"`)
		w.Write(payload)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestDownloadToolWithProgressToken exercises the download tool wiring: when the
// client supplies a progress token, the handler must forward download progress
// as MCP notifications/progress and the final notification must report the full
// payload size.
func TestDownloadToolWithProgressToken(t *testing.T) {
	payload := []byte("%PDF-1.4 progress notification payload for the download tool")
	srv := downloadMirror(t, payload)
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])

	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	client := libgen.New(staticMirrors{srv.URL}, cfg)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	Register(server, client, cfg)

	var mu sync.Mutex
	var progresses []float64
	var totals []float64
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			mu.Lock()
			progresses = append(progresses, r.Params.Progress)
			totals = append(totals, r.Params.Total)
			mu.Unlock()
		},
	})

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })

	params := &mcp.CallToolParams{Name: "download", Arguments: map[string]any{"md5": wantMD5}}
	params.SetProgressToken("tok-1")
	res, err := session.CallTool(ctx, params)
	if err != nil {
		t.Fatalf("CallTool(download) error = %v", err)
	}
	if res.IsError {
		t.Fatalf("download returned tool error: %+v", res.Content)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(progresses) == 0 {
		t.Fatal("no progress notifications received, want at least one")
	}
	if last := progresses[len(progresses)-1]; last != float64(len(payload)) {
		t.Errorf("last progress = %v, want %d", last, len(payload))
	}
	if last := totals[len(totals)-1]; last != float64(len(payload)) {
		t.Errorf("last total = %v, want %d", last, len(payload))
	}
}

// TestDownloadToolWithoutProgressToken confirms the download tool still works
// when the client sends no progress token (the handler passes a nil callback).
func TestDownloadToolWithoutProgressToken(t *testing.T) {
	payload := []byte("%PDF-1.4 no progress token payload")
	srv := downloadMirror(t, payload)
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])

	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	client := libgen.New(staticMirrors{srv.URL}, cfg)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	Register(server, client, cfg)

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "download", Arguments: map[string]any{"md5": wantMD5}})
	if err != nil {
		t.Fatalf("CallTool(download) error = %v", err)
	}
	if res.IsError {
		t.Fatalf("download returned tool error: %+v", res.Content)
	}
}

// doiStubSource is a test DownloadSource that resolves any DOI-keyed item straight
// to a fixed URL (a local file CDN), standing in for unpaywall/sci-hub so the
// download-by-DOI path can run end to end without touching the live providers.
type doiStubSource struct {
	name    string
	fileURL string
}

func (s doiStubSource) Name() string                 { return s.name }
func (s doiStubSource) Supports(it libgen.Item) bool { return it.DOI != "" }
func (s doiStubSource) Resolve(context.Context, libgen.Item) (libgen.Resolved, error) {
	return libgen.Resolved{FileURL: s.fileURL, VerifyMD5: false, Ext: "pdf"}, nil
}

// fileCDNServer serves payload as an octet-stream at /file, with an optional
// Content-Disposition (empty to omit it so a metadata-built name can win).
func fileCDNServer(t *testing.T, payload []byte, disposition string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/file", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		if disposition != "" {
			w.Header().Set("Content-Disposition", disposition)
		}
		_, _ = w.Write(payload)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newDownloadSession registers the tools on a client built with the given options
// and returns an in-memory MCP session, so download tests can inject a custom
// source chain (e.g. a DOI stub) without reaching the network.
func newDownloadSession(t *testing.T, cfg *config.Config, mirrors libgen.MirrorLister, opts ...libgen.Option) *mcp.ClientSession {
	t.Helper()
	client := libgen.New(mirrors, cfg, opts...)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	Register(server, client, cfg)

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// decodeDownloadResult unmarshals a download tool result's structured content.
func decodeDownloadResult(t *testing.T, res *mcp.CallToolResult) libgen.DownloadResult {
	t.Helper()
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out libgen.DownloadResult
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatal(uerr)
	}
	return out
}

// TestDownloadToolByDOI verifies the download tool resolves an article by DOI
// through the (injected) DOI source and surfaces the serving source in the result.
func TestDownloadToolByDOI(t *testing.T) {
	payload := []byte("%PDF-1.4 article fetched by DOI")
	cdn := fileCDNServer(t, payload, "") // no disposition: DOI items get a name from Ext
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	session := newDownloadSession(t, cfg, staticMirrors{},
		libgen.WithSources(doiStubSource{name: "scihub", fileURL: cdn.URL + "/file"}))

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"doi": "10.1371/journal.pone.0000217"},
	})
	if err != nil {
		t.Fatalf("CallTool(download) error = %v", err)
	}
	if res.IsError {
		t.Fatalf("download returned tool error: %+v", res.Content)
	}
	out := decodeDownloadResult(t, res)
	if out.Source != "scihub" {
		t.Errorf("Source = %q, want %q", out.Source, "scihub")
	}
	if out.SizeBytes != int64(len(payload)) {
		t.Errorf("SizeBytes = %d, want %d", out.SizeBytes, len(payload))
	}
	if !strings.HasSuffix(out.Path, ".pdf") {
		t.Errorf("Path = %q, want a .pdf name", out.Path)
	}
}

// TestDownloadToolRequiresMD5OrDOI verifies the tool rejects a call carrying
// neither md5 nor doi with a tool error (no download attempted).
func TestDownloadToolRequiresMD5OrDOI(t *testing.T) {
	session := newSession(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if !res.IsError {
		t.Fatal("download with neither md5 nor doi should be a tool error")
	}
}

// bookMirror serves the full book download chain (ads.php → get.php → CDN) plus
// json.php for get_details, echoing the requested md5 into the get.php link and
// omitting a Content-Disposition so a metadata-built filename wins.
func bookMirror(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])
	fileJSON, _ := os.ReadFile("../libgen/testdata/file_by_md5.json")
	editionJSON, _ := os.ReadFile("../libgen/testdata/edition.json")
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<html><a href="get.php?md5=%s&key=TESTKEY123">GET</a></html>`, wantMD5)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/cdn/file", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/cdn/file", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream") // no Content-Disposition
		_, _ = w.Write(payload)
	})
	mux.HandleFunc("/json.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("object") == "f" {
			_, _ = w.Write(fileJSON)
		} else {
			_, _ = w.Write(editionJSON)
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestDownloadToolMD5Book verifies the md5 (book) path still works and that, with
// no explicit filename and no mirror-announced name, the file lands under a clean
// metadata-built name ("Author - Title (Year).ext") from get_details, tagged with
// the libgen source.
func TestDownloadToolMD5Book(t *testing.T) {
	payload := []byte("%PDF-1.4 book fetched by md5 for the metadata name test")
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])
	srv := bookMirror(t, payload)

	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	session := newDownloadSession(t, cfg, staticMirrors{srv.URL})

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": wantMD5},
	})
	if err != nil {
		t.Fatalf("CallTool(download) error = %v", err)
	}
	if res.IsError {
		t.Fatalf("download returned tool error: %+v", res.Content)
	}
	out := decodeDownloadResult(t, res)
	if out.Source != "libgen" {
		t.Errorf("Source = %q, want %q", out.Source, "libgen")
	}
	if !out.Verified {
		t.Error("Verified = false, want true (md5-keyed book)")
	}
	base := filepath.Base(out.Path)
	if !strings.HasPrefix(base, "Jyotiswarup Raiturkar - Hands-On Software Architecture with Golang") {
		t.Errorf("filename = %q, want a clean metadata-built name", base)
	}
	if !strings.HasSuffix(base, ".pdf") {
		t.Errorf("filename = %q, want a .pdf suffix", base)
	}
}
