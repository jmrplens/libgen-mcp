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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/discovery"
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

// downloadToolSchema registers the tools for cfg and returns the download tool
// as the client sees it over a real tools/list round-trip.
func downloadToolSchema(t *testing.T, cfg *config.Config) *mcp.Tool {
	t.Helper()
	client := libgen.New(staticMirrors{"http://127.0.0.1:0"}, cfg)
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
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if tl.Name == "download" {
			return tl
		}
	}
	t.Fatal("download tool not registered")
	return nil
}

// sourceEnum extracts properties.source.enum from a tool input schema, robustly
// across whatever concrete type the client decodes the schema into.
func sourceEnum(t *testing.T, schema any) []string {
	t.Helper()
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Properties struct {
			Source struct {
				Enum []string `json:"enum"`
			} `json:"source"`
		} `json:"properties"`
	}
	if uerr := json.Unmarshal(data, &s); uerr != nil {
		t.Fatal(uerr)
	}
	return s.Properties.Source.Enum
}

// TestDownloadSchemaReflectsEnabledSources verifies the download tool advertises
// only the enabled sources — both in the source enum and in the prose — so the
// model never selects a disabled provider (including unpaywall, which is gated on
// a contact email).
func TestDownloadSchemaReflectsEnabledSources(t *testing.T) {
	base := func() *config.Config {
		return &config.Config{DownloadDir: t.TempDir(), Timeout: time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	}
	cases := []struct {
		name       string
		mutate     func(*config.Config)
		wantEnum   []string
		wantAbsent []string
	}{
		{
			name:       "book sources only",
			mutate:     func(c *config.Config) { c.Sources = []string{"libgen", "randombook"} },
			wantEnum:   []string{"libgen", "randombook"},
			wantAbsent: []string{"unpaywall", "scihub"},
		},
		{
			name:       "default without email disables unpaywall",
			mutate:     func(*config.Config) {},
			wantEnum:   []string{"scihub", "scidb", "libgen", "randombook", "annas"},
			wantAbsent: []string{"unpaywall"},
		},
		{
			name:       "unpaywall enabled once an email is set",
			mutate:     func(c *config.Config) { c.UnpaywallEmail = "me@example.com" },
			wantEnum:   []string{"unpaywall", "scihub", "scidb", "libgen", "randombook", "annas"},
			wantAbsent: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(cfg)
			dl := downloadToolSchema(t, cfg)
			if got := sourceEnum(t, dl.InputSchema); !slices.Equal(got, tc.wantEnum) {
				t.Errorf("source enum = %v, want %v", got, tc.wantEnum)
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(dl.Description, absent) {
					t.Errorf("description advertises disabled source %q:\n%s", absent, dl.Description)
				}
			}
		})
	}
}

// TestSearchNextSteps verifies the search follow-up guidance: with results it
// embeds executable get_details/download examples carrying the first result's
// real md5; with no results it returns recovery suggestions naming the topics.
func TestSearchNextSteps(t *testing.T) {
	withResults := searchNextSteps(SearchOutput{
		Results: []libgen.Result{{MD5: "0123456789abcdef0123456789abcdef", DOI: "10.1/x"}},
		Page:    1,
	})
	joined := strings.Join(withResults, "\n")
	if !strings.Contains(joined, "get_details") || !strings.Contains(joined, "download") {
		t.Errorf("next_steps should mention get_details and download; got %q", joined)
	}
	if !strings.Contains(joined, "0123456789abcdef0123456789abcdef") {
		t.Errorf("next_steps should embed the first result's md5; got %q", joined)
	}
	if !strings.Contains(joined, "10.1/x") {
		t.Errorf("next_steps should embed the first result's doi; got %q", joined)
	}

	empty := searchNextSteps(SearchOutput{Results: []libgen.Result{}})
	if len(empty) == 0 || !strings.Contains(empty[0], "No matches") {
		t.Errorf("empty search should suggest recovery; got %q", empty)
	}
	if !strings.Contains(empty[0], "comics") {
		t.Errorf("empty-search suggestion should list topics; got %q", empty[0])
	}
}

// TestDetailsNextSteps verifies the details follow-up prefers the record's md5,
// falls back to its doi, and always suggests download.
func TestDetailsNextSteps(t *testing.T) {
	byMD5 := detailsNextSteps(DetailsOutput{File: map[string]any{"md5": "abc"}})
	if len(byMD5) != 1 || !strings.Contains(byMD5[0], `"md5":"abc"`) {
		t.Errorf("md5 record should suggest download by md5; got %q", byMD5)
	}
	byDOI := detailsNextSteps(DetailsOutput{Edition: map[string]any{"doi": "10.1/y"}})
	if len(byDOI) != 1 || !strings.Contains(byDOI[0], `"doi":"10.1/y"`) {
		t.Errorf("doi record should suggest download by doi; got %q", byDOI)
	}
	none := detailsNextSteps(DetailsOutput{})
	if len(none) != 1 || !strings.Contains(none[0], "download") {
		t.Errorf("empty record should still suggest download; got %q", none)
	}
}

// TestDownloadNextSteps verifies the download follow-up names the saved path,
// size and source.
func TestDownloadNextSteps(t *testing.T) {
	steps := downloadNextSteps(libgen.DownloadResult{Path: "/tmp/book.pdf", SizeBytes: 123, Source: "libgen"})
	if len(steps) != 1 {
		t.Fatalf("want 1 step, got %d", len(steps))
	}
	for _, want := range []string{"/tmp/book.pdf", "123", "libgen"} {
		if !strings.Contains(steps[0], want) {
			t.Errorf("download step should mention %q; got %q", want, steps[0])
		}
	}
}

// TestSearchToolEmitsNextSteps verifies the registered search tool surfaces
// next_steps in its structured output over a real tools/call round-trip.
func TestSearchToolEmitsNextSteps(t *testing.T) {
	session := newSession(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "golang", "topics": []string{"nonfiction"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out SearchOutput
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatal(uerr)
	}
	if len(out.NextSteps) == 0 {
		t.Errorf("search output should carry next_steps; structured=%s", data)
	}
}

// textContent returns the concatenated text of a result's TextContent blocks.
func textContent(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// TestSearchToolReturnsMarkdownAndStructured verifies a search call carries BOTH
// channels: a human-readable Markdown text block (with a results table and the
// next-steps section) and the structured JSON output.
func TestSearchToolReturnsMarkdownAndStructured(t *testing.T) {
	session := newSession(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "golang", "topics": []string{"nonfiction"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	md := textContent(res)
	if !strings.Contains(md, "| # | Title") {
		t.Errorf("markdown should contain a results table header; got:\n%s", md)
	}
	if !strings.Contains(md, "Next steps") {
		t.Errorf("markdown should contain a next-steps section; got:\n%s", md)
	}
	if strings.HasPrefix(strings.TrimSpace(md), "{") {
		t.Errorf("text content should be markdown, not raw JSON; got:\n%s", md)
	}
	if res.StructuredContent == nil {
		t.Error("result should still carry structured JSON output alongside the markdown")
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
	for _, want := range []string{"search", "get_details", "download", "read"} {
		if !names[want] {
			t.Errorf("missing tool %q; registered: %v", want, names)
		}
	}
	if len(res.Tools) != 4 {
		t.Errorf("got %d tools, want 4", len(res.Tools))
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
	if !strings.Contains(string(data), "\"citations\"") || !strings.Contains(string(data), "@") {
		t.Errorf("handler did not populate citations: %s", data)
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

// TestResolveHelpers covers the resolve-only helper branches the end-to-end test
// does not reach: filename derivation, MIME mapping, header flattening, and the
// headers path of the guidance/markdown builders.
func TestResolveHelpers(t *testing.T) {
	// resolveFilename: explicit wins; meta builds "Author - Title (Year).ext";
	// doi falls back to a .pdf name; md5 falls back to md5+ext.
	if got := resolveFilename(libgen.Item{MD5: "x"}, "given.pdf", "pdf"); got != "given.pdf" {
		t.Errorf("explicit filename: %q", got)
	}
	meta := resolveFilename(libgen.Item{MD5: "x", Meta: &libgen.FileMeta{Title: "T/it:le", Author: "A", Year: "2020"}}, "", "epub")
	if meta != "A - T-it-le (2020).epub" {
		t.Errorf("meta filename: %q", meta)
	}
	if got := resolveFilename(libgen.Item{DOI: "10.1/x"}, "", ""); got != "10.1-x.pdf" {
		t.Errorf("doi fallback filename: %q", got)
	}
	if got := resolveFilename(libgen.Item{MD5: "abc"}, "", ""); got != "abc" {
		t.Errorf("md5 fallback filename: %q", got)
	}

	// mimeForExt across the mapped types + defaults.
	for ext, want := range map[string]string{
		"pdf": "application/pdf", "epub": "application/epub+zip", "mobi": "application/x-mobipocket-ebook",
		"djvu": "image/vnd.djvu", "cbr": "application/vnd.comicbook-rar", "cbz": "application/vnd.comicbook+zip",
		"txt": "text/plain", "zzz": "application/octet-stream",
	} {
		if got := mimeForExt(ext, libgen.Item{}); got != want {
			t.Errorf("mimeForExt(%q) = %q, want %q", ext, got, want)
		}
	}
	if mimeForExt("", libgen.Item{DOI: "10.1/x"}) != "application/pdf" {
		t.Error("empty ext + doi should default to pdf")
	}
	if mimeForExt("", libgen.Item{MD5: "x"}) != "application/octet-stream" {
		t.Error("empty ext + md5 should default to octet-stream")
	}

	// headerMap / headerList.
	if headerMap(nil) != nil {
		t.Error("nil header should map to nil")
	}
	hm := headerMap(http.Header{"Referer": {"https://h/"}, "Empty": {""}})
	if hm["Referer"] != "https://h/" || len(hm) != 1 {
		t.Errorf("headerMap dropped/kept wrong keys: %v", hm)
	}
	if got := headerList(map[string]string{"B": "2", "A": "1"}); got != "A: 1; B: 2" {
		t.Errorf("headerList order: %q", got)
	}

	// resolveNextSteps + renderResolvedMarkdown with headers present.
	link := ResolvedLink{
		URL: "https://x/y", Source: "scihub", Filename: "p.pdf",
		Headers: map[string]string{"Referer": "https://h/"}, VerifyMD5: true,
	}
	steps := strings.Join(resolveNextSteps(link), "\n")
	if !strings.Contains(steps, "Referer: https://h/") || !strings.Contains(steps, "verify") {
		t.Errorf("resolveNextSteps with headers: %q", steps)
	}
	md := renderResolvedMarkdown(link)
	if !strings.Contains(md, "scihub") || !strings.Contains(md, "https://x/y") || !strings.Contains(md, "Referer") {
		t.Errorf("renderResolvedMarkdown: %q", md)
	}
}

// TestDownloadToolResolveOnly verifies the resolve_only path returns a direct URL
// as a resource_link content block plus structured `resolved` output, WITHOUT
// writing a file to disk.
func TestDownloadToolResolveOnly(t *testing.T) {
	payload := []byte("%PDF-1.4 resolve-only payload")
	srv := downloadMirror(t, payload)
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])

	dir := t.TempDir()
	cfg := &config.Config{DownloadDir: dir, Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
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

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": wantMD5, "resolve_only": true},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("resolve_only returned a tool error: %+v", res.Content)
	}

	// A resource_link content block must carry the resolved URL.
	var linkURI string
	for _, c := range res.Content {
		if rl, ok := c.(*mcp.ResourceLink); ok {
			linkURI = rl.URI
		}
	}
	if !strings.Contains(linkURI, srv.URL) {
		t.Errorf("resource_link URI %q does not point at the resolved source", linkURI)
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
		t.Fatalf("structured output has no `resolved`; got %s", data)
	}
	if out.Resolved.Source != "libgen" || !out.Resolved.VerifyMD5 || !strings.Contains(out.Resolved.URL, srv.URL) {
		t.Errorf("resolved = %+v", out.Resolved)
	}
	if out.Path != "" {
		t.Errorf("resolve_only must not save a file, but Path=%q", out.Path)
	}

	// No file was written to the download dir.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("resolve_only wrote %d file(s) to disk, want 0", len(entries))
	}
}

// TestDownloadToolRemoteMode verifies WithRemoteDownloads makes the download tool
// always resolve a link (even without resolve_only) and never write a file, and
// that its description advertises the remote behavior.
func TestDownloadToolRemoteMode(t *testing.T) {
	payload := []byte("%PDF-1.4 remote-mode payload")
	srv := downloadMirror(t, payload)
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])

	dir := t.TempDir()
	cfg := &config.Config{DownloadDir: dir, Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	client := libgen.New(staticMirrors{srv.URL}, cfg)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	Register(server, client, cfg, WithRemoteDownloads())
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

	// No resolve_only in the arguments — remote mode must still resolve a link.
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "download", Arguments: map[string]any{"md5": wantMD5}})
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
	if out.Resolved == nil || !strings.Contains(out.Resolved.URL, srv.URL) {
		t.Fatalf("remote mode should resolve a link without resolve_only; got %s", data)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("remote mode wrote %d file(s) to disk, want 0", len(entries))
	}

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if tl.Name == "download" && !strings.Contains(tl.Description, "runs remotely") {
			t.Errorf("remote download description should note remote behavior; got:\n%s", tl.Description)
		}
	}
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

// TestDownloadDescriptionHasUntrustedNote verifies the download tool's prose
// carries an explicit caveat that downloaded content is untrusted third-party
// data, never instructions to follow.
func TestDownloadDescriptionHasUntrustedNote(t *testing.T) {
	desc := downloadToolDescription([]string{"libgen"}, []string{"scihub"})
	if !strings.Contains(desc, "untrusted") {
		t.Fatalf("download description should carry an untrusted-content caveat; got:\n%s", desc)
	}
}

// TestDownloadInputSchemaEmptyEnabled covers the branch where no sources are
// enabled: the schema is returned unconstrained (no enum) rather than restricted.
func TestDownloadInputSchemaEmptyEnabled(t *testing.T) {
	schema := downloadInputSchema(nil)
	if schema == nil {
		t.Fatal("downloadInputSchema(nil) returned nil")
	}
	if src := schema.Properties["source"]; src != nil && len(src.Enum) != 0 {
		t.Errorf("empty enabled should leave source enum unset; got %v", src.Enum)
	}
}

// TestDownloadInputSchemaInferenceError covers the defensive guard that returns a
// nil schema when jsonschema inference fails. Real inference of the static
// DownloadInput struct never errors, so the seam is overridden to force the path.
func TestDownloadInputSchemaInferenceError(t *testing.T) {
	orig := downloadSchemaFor
	t.Cleanup(func() { downloadSchemaFor = orig })
	downloadSchemaFor = func(*jsonschema.ForOptions) (*jsonschema.Schema, error) {
		return nil, errors.New("inference failed")
	}
	if got := downloadInputSchema([]string{"libgen"}); got != nil {
		t.Errorf("schema inference error should yield a nil schema; got %v", got)
	}
}

// TestValidateDownloadInputUnknownSource covers the unknown-source rejection arm
// of validateDownloadInput. This branch is unreachable through the registered tool
// (the input schema's source enum rejects unknown values before the handler runs),
// so it is exercised directly.
func TestValidateDownloadInputUnknownSource(t *testing.T) {
	_, _, _, err := validateDownloadInput(DownloadInput{
		MD5:    "87a4ebdaf21fa6cc70009a3dd63194ee",
		Source: "definitelynotasource",
	})
	if err == nil {
		t.Fatal("an unknown source should be rejected")
	}
	if !strings.Contains(err.Error(), "definitelynotasource") {
		t.Errorf("error should name the bad source; got %v", err)
	}
}

// emptyJSONClient builds a libgen client whose json.php always returns an empty
// object, so DetailsByMD5/DetailsByID surface their "no record found" errors.
func emptyJSONClient(t *testing.T) *libgen.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/json.php", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	return libgen.New(staticMirrors{srv.URL}, cfg)
}

// TestDetailsByMD5LookupError covers detailsByMD5's error return when the client's
// lookup fails (valid md5 syntax, but no record).
func TestDetailsByMD5LookupError(t *testing.T) {
	client := emptyJSONClient(t)
	if _, err := detailsByMD5(context.Background(), client, "87a4ebdaf21fa6cc70009a3dd63194ee"); err == nil {
		t.Fatal("detailsByMD5 should surface the lookup error when no record is found")
	}
}

// TestDetailsFallsBackToAnnas verifies get_details answers for an md5 the Library
// Genesis catalog never had. A search that escalated returns exactly such md5s, so
// without this fallback the tool the caller is told to use would always fail on
// them. The record must be labeled with its origin, since Anna's metadata is
// thinner than the catalog's and the caller should know which it is reading.
func TestDetailsFallsBackToAnnas(t *testing.T) {
	page := mustReadFile(t, "../discovery/testdata/annas_md5_zlib.html")
	annas := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/md5/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(page)
	}))
	t.Cleanup(annas.Close)

	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	handler := detailsHandler(emptyJSONClient(t), cfg, staticMirrors{annas.URL})
	_, out, err := handler(context.Background(), nil, DetailsInput{MD5: "00dd2b0b58e81e3c6e7cb9e7b72dee23"})
	if err != nil {
		t.Fatalf("get_details on an Anna's-only md5 should fall back, got error: %v", err)
	}
	if got := stringField(out.File, "title"); got != "Sejarah Indonesia Masa Persebaran Islam sampai Zaman VOC" {
		t.Errorf("file.title = %q, want the Anna's title", got)
	}
	if got := stringField(out.File, "origin"); got != "annas" {
		t.Errorf("file.origin = %q, want %q so the caller knows which index answered", got, "annas")
	}
	if strings.Join(out.NextSteps, "\n") == "" {
		t.Error("a fallback record should still carry download guidance")
	}
}

// TestDetailsSurfacesTheCatalogErrorWhenAnnasHasNothingEither verifies the catalog's
// error survives when the fallback finds nothing, so a genuinely unknown md5 is
// still reported as unknown rather than as an Anna's outage.
func TestDetailsSurfacesTheCatalogErrorWhenAnnasHasNothingEither(t *testing.T) {
	annas := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(annas.Close)

	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	handler := detailsHandler(emptyJSONClient(t), cfg, staticMirrors{annas.URL})
	_, _, err := handler(context.Background(), nil, DetailsInput{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"})
	if err == nil {
		t.Fatal("an md5 neither index knows should fail")
	}
	if !strings.Contains(err.Error(), "catalog") {
		t.Errorf("error %q should be the catalog's own miss", err)
	}
}

// TestDetailsByIDLookupError covers detailsByID's error return when the client's
// lookup fails, for both the edition (default) and file objects.
func TestDetailsByIDLookupError(t *testing.T) {
	client := emptyJSONClient(t)
	if _, err := detailsByID(context.Background(), client, "", "138281637"); err == nil {
		t.Fatal("detailsByID (edition) should surface the lookup error")
	}
	if _, err := detailsByID(context.Background(), client, "file", "93485370"); err == nil {
		t.Fatal("detailsByID (file) should surface the lookup error")
	}
}

// TestHeaderMapAllEmptyValues covers headerMap's post-filter nil return: a header
// whose only entries have empty values flattens to no usable keys.
func TestHeaderMapAllEmptyValues(t *testing.T) {
	if got := headerMap(http.Header{"Empty": {""}, "AlsoEmpty": {""}}); got != nil {
		t.Errorf("headerMap with only empty values should return nil; got %v", got)
	}
}

// TestDownloadResolveOnlyResolveError covers resolveDownload's error path: on the
// resolve_only route, a source that fails to resolve surfaces as a tool error.
func TestDownloadResolveOnlyResolveError(t *testing.T) {
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	session := newDownloadSession(t, cfg, staticMirrors{}, libgen.WithSources(md5ErrSource{}))
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": "87a4ebdaf21fa6cc70009a3dd63194ee", "resolve_only": true},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if !res.IsError {
		t.Fatal("resolve_only whose only source fails to resolve should be a tool error")
	}
}

// confirmMirror serves the full book download chain (ads.php → get.php → CDN) for a
// payload whose md5 it advertises, and separately counts HEAD probes and GET
// body-fetches of the CDN endpoint. The counters let the confirmation tests prove
// which requests each path makes: a size probe issues a HEAD, the actual download
// issues a GET, and the default (no-capability) path must issue neither a probe.
func confirmMirror(t *testing.T, payload []byte) (srv *httptest.Server, cdnGET, cdnHEAD *atomic.Int32) {
	t.Helper()
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])
	var getHits, headHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<html><a href="get.php?md5=%s&key=TESTKEY123">GET</a></html>`, wantMD5)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/cdn/file", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/cdn/file", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			headHits.Add(1)
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			return
		}
		getHits.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="book.pdf"`)
		_, _ = w.Write(payload)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &getHits, &headHits
}

// newConfirmSession registers the tools on a client backed by mirrors and connects
// an in-memory MCP client whose elicitation capability is governed by handler
// (nil = no capability, exercising the default/headless path). It is the download
// confirmation counterpart of newDownloadSession.
func newConfirmSession(t *testing.T, cfg *config.Config, mirrors libgen.MirrorLister, handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientSession {
	t.Helper()
	client := libgen.New(mirrors, cfg)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	Register(server, client, cfg)

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"},
		&mcp.ClientOptions{ElicitationHandler: handler})
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// confirmConfig returns a plain local-download config rooted at a fresh temp dir.
func confirmConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
}

// TestDownloadTool_ConfirmAccepted verifies the confirm-and-save path: with an
// elicitation-capable client that accepts the confirmation, a local md5 download is
// prompted (the elicitation handler is invoked exactly once) and the file is then
// downloaded and saved to disk.
func TestDownloadTool_ConfirmAccepted(t *testing.T) {
	payload := []byte("%PDF-1.4 confirm-accepted book payload")
	srv, cdnGET, _ := confirmMirror(t, payload)
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])

	cfg := confirmConfig(t)
	var elicits atomic.Int32
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		elicits.Add(1)
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
	}
	session := newConfirmSession(t, cfg, staticMirrors{srv.URL}, handler)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": wantMD5},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if res.IsError {
		t.Fatalf("an accepted download should not be a tool error: %+v", res.Content)
	}
	if elicits.Load() != 1 {
		t.Errorf("elicitation handler invoked %d times, want 1", elicits.Load())
	}
	out := decodeDownloadOutput(t, res)
	if out.Path == "" {
		t.Fatalf("accepted download should report a saved path; got %+v", out)
	}
	if _, statErr := os.Stat(out.Path); statErr != nil {
		t.Errorf("accepted download did not write the file: %v", statErr)
	}
	if cdnGET.Load() == 0 {
		t.Error("accepted download never fetched the file body (0 CDN GETs)")
	}
}

// TestDownloadTool_ConfirmDeclined verifies the decline path: with an
// elicitation-capable client that declines the confirmation, NO file is written
// (the CDN body endpoint gets 0 GETs), the result is NOT a tool error, and it
// carries guidance plus the resolved direct link so the user can fetch it later.
func TestDownloadTool_ConfirmDeclined(t *testing.T) {
	payload := []byte("%PDF-1.4 confirm-declined book payload")
	srv, cdnGET, _ := confirmMirror(t, payload)
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])

	cfg := confirmConfig(t)
	var elicits atomic.Int32
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		elicits.Add(1)
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": false}}, nil
	}
	session := newConfirmSession(t, cfg, staticMirrors{srv.URL}, handler)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": wantMD5},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if res.IsError {
		t.Fatalf("a declined download should be a non-error result; got %+v", res.Content)
	}
	if elicits.Load() != 1 {
		t.Errorf("elicitation handler invoked %d times, want 1", elicits.Load())
	}
	if cdnGET.Load() != 0 {
		t.Errorf("a declined download fetched the file body %d time(s), want 0", cdnGET.Load())
	}
	out := decodeDownloadOutput(t, res)
	if out.Path != "" {
		t.Errorf("a declined download must not save a file, but Path=%q", out.Path)
	}
	if out.Resolved == nil {
		t.Errorf("a declined download should still surface the resolved link; got %+v", out)
	}
	if entries, _ := os.ReadDir(cfg.DownloadDir); len(entries) != 0 {
		t.Errorf("a declined download wrote %d file(s) to disk, want 0", len(entries))
	}
}

// TestDownloadTool_NoElicitationDownloadsNormally proves default preservation: a
// client that did NOT advertise elicitation is never prompted and never triggers a
// size probe — the download proceeds and saves the file exactly as today, and the
// CDN endpoint sees ZERO HEAD probes (only the body GET).
func TestDownloadTool_NoElicitationDownloadsNormally(t *testing.T) {
	payload := []byte("%PDF-1.4 no-elicitation book payload")
	srv, cdnGET, cdnHEAD := confirmMirror(t, payload)
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])

	cfg := confirmConfig(t)
	// nil handler → the client advertises no elicitation capability.
	session := newConfirmSession(t, cfg, staticMirrors{srv.URL}, nil)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": wantMD5},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if res.IsError {
		t.Fatalf("a no-capability download should not be a tool error: %+v", res.Content)
	}
	out := decodeDownloadOutput(t, res)
	if out.Path == "" {
		t.Fatalf("download should report a saved path; got %+v", out)
	}
	if _, statErr := os.Stat(out.Path); statErr != nil {
		t.Errorf("download did not write the file: %v", statErr)
	}
	if cdnHEAD.Load() != 0 {
		t.Errorf("the no-capability path issued %d HEAD probe(s), want 0 (no probe without elicitation)", cdnHEAD.Load())
	}
	if cdnGET.Load() == 0 {
		t.Error("the no-capability path never fetched the file body (0 CDN GETs)")
	}
}

// TestDownloadTool_ResolveOnlyNoConfirm verifies that resolve_only never prompts,
// even with an elicitation-capable client: resolve_only never writes to disk, so
// there is nothing to confirm and the elicitation handler is not invoked.
func TestDownloadTool_ResolveOnlyNoConfirm(t *testing.T) {
	payload := []byte("%PDF-1.4 resolve-only no-confirm payload")
	srv, _, cdnHEAD := confirmMirror(t, payload)
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])

	cfg := confirmConfig(t)
	var elicits atomic.Int32
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		elicits.Add(1)
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
	}
	session := newConfirmSession(t, cfg, staticMirrors{srv.URL}, handler)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": wantMD5, "resolve_only": true},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if res.IsError {
		t.Fatalf("resolve_only returned a tool error: %+v", res.Content)
	}
	if elicits.Load() != 0 {
		t.Errorf("resolve_only invoked the elicitation handler %d times, want 0", elicits.Load())
	}
	if cdnHEAD.Load() != 0 {
		t.Errorf("resolve_only issued %d HEAD probe(s), want 0", cdnHEAD.Load())
	}
	out := decodeDownloadOutput(t, res)
	if out.Resolved == nil {
		t.Errorf("resolve_only should return a resolved link; got %+v", out)
	}
}

// TestDownloadTool_ConfirmCanceled verifies that an explicit cancel of the
// download confirmation aborts the save (same as a decline): the file body is
// never fetched, nothing is written, and the result is a non-error with the link.
func TestDownloadTool_ConfirmCanceled(t *testing.T) {
	payload := []byte("%PDF-1.4 confirm-canceled book payload")
	srv, cdnGET, _ := confirmMirror(t, payload)
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])

	cfg := confirmConfig(t)
	var elicits atomic.Int32
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		elicits.Add(1)
		return &mcp.ElicitResult{Action: "cancel"}, nil
	}
	session := newConfirmSession(t, cfg, staticMirrors{srv.URL}, handler)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": wantMD5},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if res.IsError {
		t.Fatalf("a canceled download should be a non-error result; got %+v", res.Content)
	}
	if elicits.Load() != 1 {
		t.Errorf("elicitation handler invoked %d times, want 1", elicits.Load())
	}
	if cdnGET.Load() != 0 {
		t.Errorf("a canceled download fetched the file body %d time(s), want 0", cdnGET.Load())
	}
	out := decodeDownloadOutput(t, res)
	if out.Path != "" {
		t.Errorf("a canceled download must not save a file, but Path=%q", out.Path)
	}
	if entries, _ := os.ReadDir(cfg.DownloadDir); len(entries) != 0 {
		t.Errorf("a canceled download wrote %d file(s) to disk, want 0", len(entries))
	}
}

// unpaywallElicitServer serves the Unpaywall lookup for the download-tool
// elicitation tests: it records how many lookups it received and the last email
// query parameter, and always replies with an open-access record. resolve_only never
// fetches the PDF, so the url_for_pdf value is a static placeholder.
func unpaywallElicitServer(t *testing.T) (base string, lookups *atomic.Int32, lastEmail *string) {
	t.Helper()
	var hits atomic.Int32
	var email string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		email = r.URL.Query().Get("email")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"is_oa":true,"best_oa_location":{"url_for_pdf":"https://cdn.example/oa.pdf"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &hits, &email
}

// newElicitDownloadSession registers the tools on a client (built with the given
// config and options) whose MCP client advertises elicitation via handler (nil = no
// capability, exercising the fallback path). It returns a live session for CallTool.
func newElicitDownloadSession(t *testing.T, cfg *config.Config, handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error), opts ...libgen.Option) *mcp.ClientSession {
	t.Helper()
	client := libgen.New(staticMirrors{}, cfg, opts...)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	Register(server, client, cfg)

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"},
		&mcp.ClientOptions{ElicitationHandler: handler})
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// elicitDownloadConfig is a config with NO Unpaywall email and only "unpaywall"
// enabled, so the default chain is empty and any Unpaywall resolution can only come
// from the on-demand per-call email path (never a live Sci-Hub call).
func elicitDownloadConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		DownloadDir:   t.TempDir(),
		Timeout:       5 * time.Second,
		RateRPS:       1000,
		RateBurst:     100,
		RetryAttempts: 1,
		Sources:       []string{"unpaywall"},
	}
}

// decodeDownloadOutput unmarshals a download tool result's structured content into
// the full DownloadOutput (including the resolve_only Resolved link).
func decodeDownloadOutput(t *testing.T, res *mcp.CallToolResult) DownloadOutput {
	t.Helper()
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out DownloadOutput
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatal(uerr)
	}
	return out
}

// TestDownloadTool_ElicitsUnpaywallEmailOnAccept verifies the on-demand email flow:
// with no configured Unpaywall email and a client that accepts the elicitation with
// an email, a resolve_only DOI download consults Unpaywall using the elicited email
// (for this request only) and resolves the link via the "unpaywall" source.
func TestDownloadTool_ElicitsUnpaywallEmailOnAccept(t *testing.T) {
	base, lookups, lastEmail := unpaywallElicitServer(t)
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"email": "asked@example.com"}}, nil
	}
	session := newElicitDownloadSession(t, elicitDownloadConfig(t), handler, libgen.WithUnpaywallBaseURL(base))

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"doi": "10.1/x", "resolve_only": true},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if res.IsError {
		t.Fatalf("download with an accepted email should not be a tool error: %+v", res.Content)
	}
	out := decodeDownloadOutput(t, res)
	if out.Resolved == nil || out.Resolved.Source != "unpaywall" {
		t.Fatalf("expected a resolved link from unpaywall, got %+v", out.Resolved)
	}
	if lookups.Load() != 1 {
		t.Errorf("Unpaywall lookups = %d, want 1", lookups.Load())
	}
	if *lastEmail != "asked@example.com" {
		t.Errorf("Unpaywall received email = %q, want the elicited %q", *lastEmail, "asked@example.com")
	}
}

// TestDownloadTool_ElicitDeclineFallsBack verifies the deterministic fallback: when
// the client supports elicitation but the user declines, no email is used, Unpaywall
// is not consulted (0 lookups), and the DOI download fails with no usable source —
// exactly today's behavior for a server with no configured email.
func TestDownloadTool_ElicitDeclineFallsBack(t *testing.T) {
	base, lookups, _ := unpaywallElicitServer(t)
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	session := newElicitDownloadSession(t, elicitDownloadConfig(t), handler, libgen.WithUnpaywallBaseURL(base))

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"doi": "10.1/x", "resolve_only": true},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if !res.IsError {
		t.Fatal("a DOI download with a declined email and no configured email should be a tool error")
	}
	if lookups.Load() != 0 {
		t.Errorf("Unpaywall lookups = %d after a decline, want 0", lookups.Load())
	}
}

// TestDownloadTool_NoElicitCapabilityFallsBack verifies that a client which did NOT
// advertise elicitation is never prompted: no elicitation is attempted, Unpaywall is
// not consulted (0 lookups), and the DOI download fails just as it does today. This
// guards the headless/CI path stays byte-identical.
func TestDownloadTool_NoElicitCapabilityFallsBack(t *testing.T) {
	base, lookups, _ := unpaywallElicitServer(t)
	// nil handler → the client advertises no elicitation capability.
	session := newElicitDownloadSession(t, elicitDownloadConfig(t), nil, libgen.WithUnpaywallBaseURL(base))

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"doi": "10.1/x", "resolve_only": true},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if !res.IsError {
		t.Fatal("a DOI download with no elicitation capability and no configured email should be a tool error")
	}
	if lookups.Load() != 0 {
		t.Errorf("Unpaywall lookups = %d without elicitation, want 0", lookups.Load())
	}
}

// TestLooksLikeEmail verifies the light email sanity check accepts plausible
// addresses and rejects malformed ones without over-validating.
func TestLooksLikeEmail(t *testing.T) {
	valid := []string{"a@b.co", "you@example.com", "x.y@sub.domain.org"}
	for _, e := range valid {
		if !looksLikeEmail(e) {
			t.Errorf("looksLikeEmail(%q) = false, want true", e)
		}
	}
	invalid := []string{"", "nope", "@example.com", "a@b", "a@b.", "a@.com"}
	for _, e := range invalid {
		if looksLikeEmail(e) {
			t.Errorf("looksLikeEmail(%q) = true, want false", e)
		}
	}
	// A trimmed, plausible address must survive the handler's TrimSpace + check.
	if !looksLikeEmail(strings.TrimSpace("  ok@ok.io  ")) {
		t.Error("looksLikeEmail should accept a trimmed plausible address")
	}
}

// TestEnrichmentNextStep_NoData verifies the helper returns an empty string when
// there is no Crossref enrichment to report (nil enrichment or nil Crossref).
func TestEnrichmentNextStep_NoData(t *testing.T) {
	if got := enrichmentNextStep(nil); got != "" {
		t.Errorf("nil enrichment: got %q, want empty", got)
	}
	if got := enrichmentNextStep(&libgen.Enrichment{}); got != "" {
		t.Errorf("nil Crossref: got %q, want empty", got)
	}
	// Crossref present but with no reportable fields → still empty.
	if got := enrichmentNextStep(&libgen.Enrichment{Crossref: &libgen.CrossrefWork{}}); got != "" {
		t.Errorf("empty Crossref: got %q, want empty", got)
	}
}

// TestEnrichmentNextStep_Facts verifies the helper names the journal, year and
// citation count so the model surfaces them, and escapes the untrusted journal.
func TestEnrichmentNextStep_Facts(t *testing.T) {
	step := enrichmentNextStep(&libgen.Enrichment{Crossref: &libgen.CrossrefWork{
		ContainerTitle: "Cell",
		PublishedYear:  2011,
		CitationCount:  56374,
	}})
	for _, want := range []string{"Cell", "2011", "56374", "journal"} {
		if !strings.Contains(step, want) {
			t.Errorf("next step %q should mention %q", step, want)
		}
	}
	// An untrusted journal title with a newline must be neutralized (no raw newline).
	evil := enrichmentNextStep(&libgen.Enrichment{Crossref: &libgen.CrossrefWork{ContainerTitle: "Evil\nJournal"}})
	if strings.Contains(evil, "Evil\nJournal") {
		t.Errorf("untrusted journal title must be escaped, got %q", evil)
	}
}

// TestDetailsEnrich_AppendsNextStep drives detailsEnrich against an httptest
// Crossref server: with a DOI in the edition record and enrichment enabled, it
// must populate out.Enrichment and append an enrichment next-step naming the
// journal and citation count, covering the enrichment wiring end to end.
func TestDetailsEnrich_AppendsNextStep(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","message":{` +
			`"container-title":["Cell"],"published":{"date-parts":[[2011,3,1]]},` +
			`"is-referenced-by-count":56374,"subject":["Oncology"]}}`))
	}))
	defer srv.Close()

	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	client := libgen.New(staticMirrors{"http://127.0.0.1:0"}, cfg,
		libgen.WithEnrichBaseURLs(srv.URL, "http://openlibrary.invalid"))

	out := DetailsOutput{Edition: map[string]any{"doi": "10.1016/j.cell.2011.02.013"}}
	detailsEnrich(context.Background(), client, &out)

	if out.Enrichment == nil || out.Enrichment.Crossref == nil {
		t.Fatalf("expected Crossref enrichment, got %+v", out.Enrichment)
	}
	if out.Enrichment.Crossref.ContainerTitle != "Cell" {
		t.Errorf("journal = %q, want Cell", out.Enrichment.Crossref.ContainerTitle)
	}
	joined := strings.Join(out.NextSteps, " ")
	for _, want := range []string{"Cell", "56374"} {
		if !strings.Contains(joined, want) {
			t.Errorf("next_steps should mention %q; got %q", want, joined)
		}
	}
}

// TestDetailsEnrich_NoDOINoStep verifies detailsEnrich adds nothing when the
// record carries no DOI/ISBN (Enrich returns nil, so no next-step is appended).
func TestDetailsEnrich_NoDOINoStep(t *testing.T) {
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	client := libgen.New(staticMirrors{"http://127.0.0.1:0"}, cfg)
	out := DetailsOutput{Edition: map[string]any{"title": "No identifiers here"}}
	detailsEnrich(context.Background(), client, &out)
	if out.Enrichment != nil {
		t.Errorf("no DOI/ISBN should yield nil enrichment, got %+v", out.Enrichment)
	}
	if len(out.NextSteps) != 0 {
		t.Errorf("no enrichment should append no next-step, got %v", out.NextSteps)
	}
}

// oaArxivFeed is a one-entry arXiv Atom feed carrying a DOI and an explicit PDF
// link, standing in for the live arXiv API in the open-access search tests.
const oaArxivFeed = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:arxiv="http://arxiv.org/schemas/atom">
  <entry>
    <id>http://arxiv.org/abs/2101.00001v1</id>
    <published>2021-01-15T00:00:00Z</published>
    <title>Attention Is All You Need</title>
    <author><name>Ashish Vaswani</name></author>
    <arxiv:doi>10.1000/xyz123</arxiv:doi>
    <link title="pdf" href="http://arxiv.org/pdf/2101.00001v1" rel="related" type="application/pdf"/>
  </entry>
</feed>`

// oaCrossrefWorks is a one-item Crossref works response used by the open-access
// search tests; it carries a distinct DOI so it is not deduped against arXiv.
const oaCrossrefWorks = `{"message":{"items":[
  {"DOI":"10.2000/crossref-only","title":["A Crossref Paper"],
   "author":[{"given":"Grace","family":"Hopper"}],
   "issued":{"date-parts":[[2019]]},
   "license":[{"URL":"http://creativecommons.org/licenses/by/4.0/"}]}
]}}`

// oaOpenLibraryDocs is a one-doc OpenLibrary search response used by the
// open-access search tests, resolving a title to an ISBN.
const oaOpenLibraryDocs = `{"docs":[
  {"title":"An OpenLibrary Book","author_name":["Ada Lovelace"],
   "first_publish_year":1843,"isbn":["9780000000001"],"key":"/works/OL1W"}
]}`

// oaDiscoveryServers spins up three httptest servers standing in for arXiv,
// Crossref and OpenLibrary, points the discovery package at them for the duration
// of the test, and returns a counter of the total discovery requests observed so a
// test can assert whether discovery was called at all.
func oaDiscoveryServers(t *testing.T) *int32 {
	t.Helper()
	var hits int32
	arxiv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(oaArxivFeed))
	}))
	crossref := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(oaCrossrefWorks))
	}))
	openLibrary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(oaOpenLibraryDocs))
	}))
	restore := discovery.SetBasesForTest(arxiv.URL, crossref.URL, openLibrary.URL)
	t.Cleanup(func() {
		restore()
		arxiv.Close()
		crossref.Close()
		openLibrary.Close()
	})
	return &hits
}

// oaSession builds a search-capable MCP session against the libgen book fixtures
// with the given extra-sources deployment default, so the escalation tests can
// drive the real search handler end to end.
func oaSession(t *testing.T, mode config.ExtraSourcesMode) *mcp.ClientSession {
	t.Helper()
	searchHTML := mustReadFile(t, "../libgen/testdata/search_books.html")
	mux := http.NewServeMux()
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(searchHTML) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := &config.Config{
		DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100,
		RetryAttempts: 1, UnpaywallEmail: "test@example.com", ExtraSources: mode,
	}
	return newDownloadSession(t, cfg, staticMirrors{srv.URL})
}

// oaSearchOutput calls the search tool and decodes the open_access slice from its
// structured content.
func oaSearchOutput(t *testing.T, session *mcp.ClientSession, args map[string]any) []discovery.DiscoveryResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "search", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("search tool error: %v", res.Content)
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		OpenAccess []discovery.DiscoveryResult `json:"open_access"`
	}
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatal(uerr)
	}
	return out.OpenAccess
}

// TestSearchTool_OpenAccessOptIn verifies the per-call opt-in: with
// extra_sources=always the search output carries origin-labeled OA hits and the
// discovery servers were called; with extra_sources=never the OA slice is empty
// and NO discovery request is made.
func TestSearchTool_OpenAccessOptIn(t *testing.T) {
	hits := oaDiscoveryServers(t)
	session := oaSession(t, config.ExtraSourcesAuto)

	oa := oaSearchOutput(t, session, map[string]any{"query": "golang", "extra_sources": "always"})
	if len(oa) == 0 {
		t.Fatalf("open_access should be populated when opted in, got none")
	}
	if atomic.LoadInt32(hits) == 0 {
		t.Errorf("discovery servers were never called despite opt-in")
	}
	origins := map[string]bool{}
	for _, r := range oa {
		origins[r.Origin] = true
	}
	if !origins["arxiv"] || !origins["crossref"] || !origins["openlibrary"] {
		t.Errorf("expected hits labeled by all three origins, got %v", origins)
	}

	atomic.StoreInt32(hits, 0)
	off := oaSearchOutput(t, session, map[string]any{"query": "golang", "extra_sources": "never"})
	if len(off) != 0 {
		t.Errorf("open_access should be empty when opted out, got %d", len(off))
	}
	if got := atomic.LoadInt32(hits); got != 0 {
		t.Errorf("discovery was called %d times when opted out, want 0", got)
	}
}

// TestSearchTool_OpenAccessDefaultOff verifies that in auto mode with catalog
// hits present, discovery stays off and unqueried — the common path stays cheap.
func TestSearchTool_OpenAccessDefaultOff(t *testing.T) {
	hits := oaDiscoveryServers(t)
	session := oaSession(t, config.ExtraSourcesAuto)
	oa := oaSearchOutput(t, session, map[string]any{"query": "golang"})
	if len(oa) != 0 {
		t.Errorf("open_access should be empty by default with catalog hits, got %d", len(oa))
	}
	if got := atomic.LoadInt32(hits); got != 0 {
		t.Errorf("discovery was called %d times by default, want 0", got)
	}
}

// TestShouldEscalate covers the trigger matrix across all three modes.
func TestShouldEscalate(t *testing.T) {
	cases := []struct {
		name string
		mode config.ExtraSourcesMode
		hits int
		err  error
		want bool
	}{
		{"auto, catalog answered", config.ExtraSourcesAuto, 3, nil, false},
		{"auto, catalog empty", config.ExtraSourcesAuto, 0, nil, true},
		{"auto, catalog failed", config.ExtraSourcesAuto, 0, errors.New("mirrors down"), true},
		{"always, catalog answered", config.ExtraSourcesAlways, 3, nil, true},
		{"always, catalog empty", config.ExtraSourcesAlways, 0, nil, true},
		{"never, catalog empty", config.ExtraSourcesNever, 0, nil, false},
		{"never, catalog failed", config.ExtraSourcesNever, 0, errors.New("down"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldEscalate(tc.mode, tc.hits, tc.err); got != tc.want {
				t.Fatalf("shouldEscalate = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestResolveExtraModePrecedence verifies an explicit per-call mode overrides the
// deployment default in either direction, that a blank value defers to it, and that
// an unrecognized value is rejected rather than silently ignored.
func TestResolveExtraModePrecedence(t *testing.T) {
	cfg := &config.Config{ExtraSources: config.ExtraSourcesAlways}

	if got, err := resolveExtraMode(SearchInput{}, cfg); err != nil || got != config.ExtraSourcesAlways {
		t.Fatalf("blank = (%q, %v), want the deployment default", got, err)
	}
	if got, err := resolveExtraMode(SearchInput{ExtraSources: "never"}, cfg); err != nil || got != config.ExtraSourcesNever {
		t.Fatalf("explicit never = (%q, %v), want never", got, err)
	}
	if _, err := resolveExtraMode(SearchInput{ExtraSources: "sometimes"}, cfg); err == nil {
		t.Fatal("an unknown per-call mode must be rejected")
	}
}

// TestDeploymentNeverCannotBeOverridden verifies never is a lock, not a default.
// It exists so a deployment can guarantee it never contacts the extra providers;
// a caller able to ask for them anyway would make that guarantee worthless — and a
// live evaluator run caught a model doing exactly that, retrying with always after
// an empty catalog search.
func TestDeploymentNeverCannotBeOverridden(t *testing.T) {
	cfg := &config.Config{ExtraSources: config.ExtraSourcesNever}
	for _, asked := range []string{"", "auto", "always", "never"} {
		got, err := resolveExtraMode(SearchInput{ExtraSources: asked}, cfg)
		if err != nil {
			t.Fatalf("extra_sources=%q returned an error: %v", asked, err)
		}
		if got != config.ExtraSourcesNever {
			t.Errorf("extra_sources=%q resolved to %q against a never deployment; want never", asked, got)
		}
	}
}

// TestForcedEscalationIsAlwaysModeOnly verifies only the always mode is forced.
// auto depends on the catalog's outcome and never must not run the extras at all,
// so neither may start before the catalog has answered.
func TestForcedEscalationIsAlwaysModeOnly(t *testing.T) {
	cases := map[config.ExtraSourcesMode]bool{
		config.ExtraSourcesAlways: true,
		config.ExtraSourcesAuto:   false,
		config.ExtraSourcesNever:  false,
	}
	for mode, want := range cases {
		if got := forcedEscalation(mode); got != want {
			t.Errorf("forcedEscalation(%q) = %v, want %v", mode, got, want)
		}
	}
}

// rendezvousTimeout bounds how long one side of the concurrency rendezvous waits
// for the other. A sequential implementation waits it out in full and then fails,
// so a regression reports a clear error instead of hanging the suite.
const rendezvousTimeout = 3 * time.Second

// awaitPeer blocks until peer is closed, reporting a failure if the wait times out.
// It is called from httptest handler goroutines, so it uses t.Errorf (safe from any
// goroutine) rather than t.Fatalf.
func awaitPeer(t *testing.T, peer <-chan struct{}, side string) {
	t.Helper()
	select {
	case <-peer:
	case <-time.After(rendezvousTimeout):
		t.Errorf("%s ran without its counterpart in flight: the forced path is still sequential", side)
	}
}

// TestForcedSearchQueriesExtrasConcurrently verifies the extra searchers are already
// in flight while the catalog is still being queried, so a forced search costs one
// round of latency rather than two.
//
// Both sides announce themselves and then wait for the other: run sequentially,
// whichever side goes first waits out rendezvousTimeout and fails; run concurrently,
// both proceed at once.
func TestForcedSearchQueriesExtrasConcurrently(t *testing.T) {
	catalogEntered, extraEntered := make(chan struct{}), make(chan struct{})
	var catalogOnce, extraOnce sync.Once

	searchHTML := mustReadFile(t, "../libgen/testdata/search_books.html")
	mux := http.NewServeMux()
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, _ *http.Request) {
		catalogOnce.Do(func() { close(catalogEntered) })
		awaitPeer(t, extraEntered, "the catalog search")
		_, _ = w.Write(searchHTML)
	})
	catalog := httptest.NewServer(mux)
	t.Cleanup(catalog.Close)

	// arXiv stands in for the extra searchers: Federate runs them concurrently, so
	// one of them reaching the rendezvous proves the whole set was started early.
	arxiv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		extraOnce.Do(func() { close(extraEntered) })
		awaitPeer(t, catalogEntered, "the extra searchers")
		_, _ = w.Write([]byte(oaArxivFeed))
	}))
	quiet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(func() { arxiv.Close(); quiet.Close() })
	restore := discovery.SetBasesForTest(arxiv.URL, quiet.URL, quiet.URL)
	t.Cleanup(restore)

	cfg := &config.Config{
		DownloadDir: t.TempDir(), Timeout: 10 * time.Second, RateRPS: 1000, RateBurst: 100,
		RetryAttempts: 1, UnpaywallEmail: "test@example.com", ExtraSources: config.ExtraSourcesAlways,
	}
	handler := searchHandler(libgen.New(staticMirrors{catalog.URL}, cfg), cfg, staticMirrors{quiet.URL})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, _, err := handler(ctx, nil, SearchInput{Query: "golang"}); err != nil {
		t.Fatalf("forced search failed: %v", err)
	}
}

// TestMergeExtraHitsSplitsByKeySpace verifies md5-keyed hits join the catalog
// results labeled by origin, DOI-keyed hits go to the open-access list, and an
// md5 already in the catalog is dropped so the richer catalog record survives.
func TestMergeExtraHitsSplitsByKeySpace(t *testing.T) {
	const dupMD5 = "d64efd386ed7227592499460aca2044b"
	out := &SearchOutput{Results: []libgen.Result{
		{MD5: dupMD5, Title: "Already in the catalog", Origin: "libgen", Extension: "pdf"},
	}}

	mergeExtraHits(out, []discovery.DiscoveryResult{
		{Origin: "annas", MD5: dupMD5, Title: "Duplicate from Anna's"},
		{Origin: "annas", MD5: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Title: "New book"},
		{Origin: "arxiv", DOI: "10.1/x", Title: "A paper", OpenAccess: true},
	})

	if len(out.Results) != 2 {
		t.Fatalf("Results = %d, want the catalog entry plus one new md5 hit", len(out.Results))
	}
	if out.Results[0].Title != "Already in the catalog" {
		t.Errorf("the catalog record must win the md5 collision, got %q", out.Results[0].Title)
	}
	if out.Results[1].Origin != "annas" || out.Results[1].MD5 != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("Results[1] = %+v, want the new Anna's hit labeled annas", out.Results[1])
	}
	if len(out.OpenAccess) != 1 || out.OpenAccess[0].Origin != "arxiv" {
		t.Errorf("OpenAccess = %+v, want only the DOI-keyed hit", out.OpenAccess)
	}
}

// mustReadFile reads a fixture file, failing the test on error.
func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestHumanBytes covers every arm of humanBytes: sub-KiB counts render as "N B",
// and larger counts step through the K/M/G prefixes with one decimal (base-1024).
func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{500, "500 B"},
		{1023, "1023 B"},
		{1536, "1.5 KB"},
		{5 * 1024 * 1024, "5.0 MB"},
		{2 * 1024 * 1024 * 1024, "2.0 GB"},
	}
	for _, tc := range cases {
		if got := humanBytes(tc.n); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// TestDeclinedDownload_ResolveError covers declinedDownload's resolve-failure arm:
// when the direct link cannot be resolved, it still returns the decline guidance
// (no Resolved link, a single next-step) rather than failing the request.
func TestDeclinedDownload_ResolveError(t *testing.T) {
	res, out := declinedDownload(context.Background(), failingReadClient(t),
		libgen.Item{MD5: "0123456789abcdef0123456789abcdef"}, "")
	if res == nil {
		t.Fatal("declinedDownload should always return a result")
	}
	if out.Resolved != nil {
		t.Errorf("a resolve failure should leave Resolved nil, got %+v", out.Resolved)
	}
	if len(out.NextSteps) != 1 {
		t.Errorf("resolve-failure decline should carry exactly the guidance step, got %v", out.NextSteps)
	}
}

// TestDetailsHandler_EnrichEnabled covers detailsHandler's enrichment arm: with
// Enrich requested and enabled, the handler invokes the enrichment path. The
// served record carries no doi/isbn, so enrichment resolves to nil without any
// network call, yet the enrich branch is still exercised and the record is
// returned normally.
func TestDetailsHandler_EnrichEnabled(t *testing.T) {
	const md5 = "0123456789abcdef0123456789abcdef"
	mux := http.NewServeMux()
	mux.HandleFunc("/json.php", func(w http.ResponseWriter, _ *http.Request) {
		// A single file record with a title but no editions and no doi/isbn.
		fmt.Fprintf(w, `{"777":{"md5":%q,"title":"Enrich Me"}}`, md5)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1, EnrichEnabled: true}
	client := libgen.New(staticMirrors{srv.URL}, cfg)
	h := detailsHandler(client, cfg, nil)

	res, out, err := h(context.Background(), &mcp.CallToolRequest{}, DetailsInput{MD5: md5, Enrich: true})
	if err != nil {
		t.Fatalf("detailsHandler(enrich) error = %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("enrich details must not be a tool error, got %+v", res)
	}
	if out.File == nil || stringField(out.File, "title") != "Enrich Me" {
		t.Errorf("handler should return the file record, got %+v", out.File)
	}
	if out.Enrichment != nil {
		t.Errorf("a record with no doi/isbn should yield no enrichment, got %+v", out.Enrichment)
	}
}

// newUnpaywallProbeSession wires an in-memory MCP server exposing a "uprobe" tool
// that calls elicitUnpaywallEmail with a config/input built from the request, so
// tests can exercise its capability-gated branches through a real round-trip. A
// nil handler means the client advertises no elicitation capability.
func newUnpaywallProbeSession(t *testing.T, handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "uprobe", Description: "exercises elicitUnpaywallEmail for tests"},
		func(ctx context.Context, req *mcp.CallToolRequest, in unpaywallProbeInput) (*mcp.CallToolResult, unpaywallProbeOutput, error) {
			cfg := &config.Config{UnpaywallEmail: in.ConfiguredEmail}
			email := elicitUnpaywallEmail(ctx, req, cfg, DownloadInput{DOI: in.DOI, Source: in.Source})
			return nil, unpaywallProbeOutput{Email: email}, nil
		})

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"},
		&mcp.ClientOptions{ElicitationHandler: handler})
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

type unpaywallProbeInput struct {
	DOI             string `json:"doi,omitempty"`
	Source          string `json:"source,omitempty"`
	ConfiguredEmail string `json:"configured_email,omitempty"`
}

type unpaywallProbeOutput struct {
	Email string `json:"email"`
}

// callUprobe drives the uprobe tool once and returns the email elicitUnpaywallEmail
// produced.
func callUprobe(t *testing.T, session *mcp.ClientSession, in unpaywallProbeInput) string {
	t.Helper()
	args, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshaling uprobe input: %v", err)
	}
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "uprobe", Arguments: json.RawMessage(args)})
	if err != nil {
		t.Fatalf("CallTool(uprobe) failed: %v", err)
	}
	if res.IsError {
		t.Fatalf("uprobe returned a tool error: %+v", res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshaling uprobe output: %v", err)
	}
	var out unpaywallProbeOutput
	if uerr := json.Unmarshal(raw, &out); uerr != nil {
		t.Fatalf("decoding uprobe output: %v", uerr)
	}
	return out.Email
}

// TestElicitUnpaywallEmail_NamedSource covers the named-source short-circuit: when
// a specific source is requested, the per-call Unpaywall prepend can never take
// effect, so elicitUnpaywallEmail returns "" without ever prompting (the handler,
// which would accept a valid email, must not be reached).
func TestElicitUnpaywallEmail_NamedSource(t *testing.T) {
	session := newUnpaywallProbeSession(t, acceptHandler(map[string]any{"email": "valid@example.com"}))
	if got := callUprobe(t, session, unpaywallProbeInput{DOI: "10.1/x", Source: "scihub"}); got != "" {
		t.Errorf("a named source should skip the prompt and return \"\", got %q", got)
	}
}

// TestElicitUnpaywallEmail_InvalidEmail covers the implausible-address arm: an
// accepted but malformed email fails the light sanity check, so the function
// collapses to "" and the caller falls back.
func TestElicitUnpaywallEmail_InvalidEmail(t *testing.T) {
	session := newUnpaywallProbeSession(t, acceptHandler(map[string]any{"email": "not-an-email"}))
	if got := callUprobe(t, session, unpaywallProbeInput{DOI: "10.1/x"}); got != "" {
		t.Errorf("an implausible email should yield \"\", got %q", got)
	}
}
