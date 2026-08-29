package tools

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // tests compute the LibGen file digest for integrity assertions.
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
		},
	)
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
			name:   "default without email or core key disables unpaywall and core",
			mutate: func(*config.Config) {},
			wantEnum: []string{
				"openalex", "europepmc", "biorxiv", "rfc", "nist", "dagstuhl", "acl", "zenodo", "scielo", "fao",
				"fatcat", "crossref", "oapen", "archive", "scihub", "scidb", "libgen", "randombook", "annas",
			},
			wantAbsent: []string{"unpaywall", "core"},
		},
		{
			name:   "unpaywall enabled once an email is set",
			mutate: func(c *config.Config) { c.UnpaywallEmail = "me@example.com" },
			wantEnum: []string{
				"unpaywall", "openalex", "europepmc", "biorxiv", "rfc", "nist", "dagstuhl", "acl", "zenodo", "scielo", "fao",
				"fatcat", "crossref", "oapen", "archive", "scihub", "scidb", "libgen", "randombook", "annas",
			},
			wantAbsent: nil,
		},
		{
			name:   "core joins the enum once its key is set",
			mutate: func(c *config.Config) { c.UnpaywallEmail = "me@example.com"; c.CoreKey = "k" },
			wantEnum: []string{
				"unpaywall", "openalex", "europepmc", "biorxiv", "rfc", "nist", "dagstuhl", "acl", "zenodo", "scielo", "fao",
				"fatcat", "core", "crossref", "oapen", "archive", "scihub", "scidb", "libgen", "randombook", "annas",
			},
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
	}, false, config.ExtraSourcesAuto)
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

	empty := searchNextSteps(SearchOutput{Results: []libgen.Result{}}, false, config.ExtraSourcesAuto)
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

// TestDownloadNextSteps verifies the download follow-up names the saved path and
// size — and not the source that served them, which the result withholds.
func TestDownloadNextSteps(t *testing.T) {
	steps := downloadNextSteps(libgen.DownloadResult{Path: "/tmp/book.pdf", SizeBytes: 123, Source: "libgen"})
	if len(steps) != 1 {
		t.Fatalf("want 1 step, got %d", len(steps))
	}
	for _, want := range []string{"/tmp/book.pdf", "123"} {
		if !strings.Contains(steps[0], want) {
			t.Errorf("download step should mention %q; got %q", want, steps[0])
		}
	}
	if strings.Contains(steps[0], "libgen") {
		t.Errorf("download step must not name the serving source; got %q", steps[0])
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

// TestDownloadFailureCarriesNextSteps is the guard on the one dead end the surface
// had: a failed download used to return the joined source errors and nothing else,
// while every other result carries a "Next steps" block. The failure must arrive
// flagged IsError, with the reason and the guidance in the text the model reads.
//
// It must arrive with NO structuredContent, which is what
// TestDownloadFailureSendsNoStructuredContent pins: the guidance travels in the
// document, not beside a DownloadOutput describing a download that never ran.
func TestDownloadFailureCarriesNextSteps(t *testing.T) {
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
		t.Fatal("a failed download must still be flagged IsError")
	}
	text := contentText(res)
	if !strings.Contains(text, "Next steps") {
		t.Errorf("the failure markdown carries no Next steps block:\n%s", text)
	}
	if !strings.Contains(text, "resolve failed") {
		t.Errorf("the failure markdown drops the underlying reason:\n%s", text)
	}
	if !strings.Contains(text, "nothing left to pin") {
		t.Errorf("the failure markdown drops the recovery guidance:\n%s", text)
	}
}

// TestDownloadFailureSendsNoStructuredContent pins the shape of a failed download:
// an IsError result whose only channel is the failure document.
//
// The handler used to return a zero DownloadOutput alongside it, which the output
// schema then rendered as path="", size_bytes=0 and verified=false — required fields
// asserting the results of a download that never happened, with path being exactly
// the field a model reads to locate the file. The SDK marshals and validates that
// output for an IsError result just as it does for a successful one, so the day any
// of those fields gains a constraint the failure would become a JSON-RPC protocol
// error and the actionable message would be lost.
func TestDownloadFailureSendsNoStructuredContent(t *testing.T) {
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
		t.Fatal("a failed download must still be flagged IsError")
	}
	if res.StructuredContent != nil {
		t.Errorf("a failed download must send no structuredContent; got %v", res.StructuredContent)
	}
	if len(res.Content) != 1 {
		t.Fatalf("a failed download should carry exactly one content block, got %d", len(res.Content))
	}
	if !strings.Contains(contentText(res), "no file was saved") {
		t.Errorf("the single content block should be the failure document; got:\n%s", contentText(res))
	}
}

// TestDownloadFailureErrorUnwrapsTheChainError verifies the failure error keeps the
// source chain's own error reachable, so errors.Is still classifies it — the
// transient-source guidance is selected that way, and a fmt.Errorf carrying only the
// rendered document would have severed it.
func TestDownloadFailureErrorUnwrapsTheChainError(t *testing.T) {
	cause := fmt.Errorf("source scihub: %w", libgen.ErrSourceUnavailable)
	_, out, err := downloadFailure(libgen.Item{DOI: "10.1/x"}, cause, config.ExtraSourcesAuto)
	if err == nil {
		t.Fatal("downloadFailure must return a Go error")
	}
	if !errors.Is(err, libgen.ErrSourceUnavailable) {
		t.Errorf("errors.Is(err, ErrSourceUnavailable) = false; the cause was dropped")
	}
	if !strings.Contains(err.Error(), "Next steps") {
		t.Errorf("the error message should be the whole failure document; got %q", err.Error())
	}
	if len(out.NextSteps) != 0 || out.Path != "" {
		t.Errorf("the failure output must stay zero; got %+v", out)
	}
}

// contentText joins the text content blocks of a tool result.
func contentText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// TestDownloadFailureSteps_PinnedSourceVersusExhaustedChain pins the distinction
// the guidance exists to make. A pinned source that fails has the whole untried
// chain behind it and the fix is to drop the pin; an exhausted chain has nothing
// left to pin, and advising a source there would send the model back through
// sources that already failed — which is precisely the by-hand walk (three calls,
// 3.8 seconds) a live run performed before dropping the pin on its own.
func TestDownloadFailureSteps_PinnedSourceVersusExhaustedChain(t *testing.T) {
	chainErr := errors.New("source unpaywall: mirror returned an HTML page instead of the file")

	pinned := strings.Join(downloadFailureSteps(libgen.Item{DOI: "10.1/x", Source: "unpaywall"}, chainErr, config.ExtraSourcesAuto), "\n")
	if !strings.Contains(pinned, "NO source field") {
		t.Errorf("a pinned failure must tell the model to drop the pin; got %q", pinned)
	}
	if !strings.Contains(pinned, "one at a time by hand") {
		t.Errorf("a pinned failure must warn against walking the sources by hand; got %q", pinned)
	}

	exhausted := strings.Join(downloadFailureSteps(libgen.Item{DOI: "10.1/x"}, chainErr, config.ExtraSourcesAuto), "\n")
	if strings.Contains(exhausted, "NO source field") {
		t.Errorf("an exhausted chain must not repeat the drop-the-pin advice; got %q", exhausted)
	}
	if !strings.Contains(exhausted, "nothing left to pin") {
		t.Errorf("an exhausted chain must say there is nothing left to pin; got %q", exhausted)
	}
	if !strings.Contains(exhausted, "10.1/x") {
		t.Errorf("the exhausted-chain advice should name the identifier to re-check; got %q", exhausted)
	}

	// Both paths must forbid inventing a result.
	for name, steps := range map[string]string{"pinned": pinned, "exhausted": exhausted} {
		if !strings.Contains(steps, "never state or imply that anything was saved") {
			t.Errorf("%s guidance drops the do-not-claim-success guardrail; got %q", name, steps)
		}
	}
}

// TestDownloadFailureSteps_ByIdentifier verifies the recovery check named once the
// whole chain is exhausted differs by identifier, since the likely cause does: an
// article is most often a wrong or unindexed DOI, a book usually has another copy
// under a different md5, and an ISBN only ever matched the openly licensed sources.
func TestDownloadFailureSteps_ByIdentifier(t *testing.T) {
	cases := []struct {
		name string
		item libgen.Item
		want string
	}{
		{"doi", libgen.Item{DOI: "10.1/x"}, "get_details"},
		{"md5", libgen.Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}, "different md5"},
		{"isbn", libgen.Item{ISBN: "9780000000001"}, "openly\nlicensed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(downloadFailureSteps(tc.item, errors.New("boom"), config.ExtraSourcesAuto), "\n")
			want := strings.ReplaceAll(tc.want, "\n", " ")
			if !strings.Contains(got, want) {
				t.Errorf("guidance for %s should mention %q; got %q", tc.name, want, got)
			}
		})
	}
}

// TestDownloadFailureSteps_NeverPolicyDoesNotAdviseAnIgnoredArgument verifies the
// download failure path obeys the same rule as the search guidance: a deployment
// that discards extra_sources must not be the one recommending it, or the fall-back
// advice sends the model into the very loop that rule exists to prevent.
func TestDownloadFailureSteps_NeverPolicyDoesNotAdviseAnIgnoredArgument(t *testing.T) {
	item := libgen.Item{DOI: "10.1/x"}
	boom := errors.New("boom")

	allowed := strings.Join(downloadFailureSteps(item, boom, config.ExtraSourcesAuto), "\n")
	if !strings.Contains(allowed, `extra_sources="always"`) {
		t.Errorf("a deployment that honors the argument should suggest it; got %q", allowed)
	}

	restricted := strings.Join(downloadFailureSteps(item, boom, config.ExtraSourcesNever), "\n")
	if strings.Contains(restricted, "extra_sources") {
		t.Errorf("a never-policy deployment must not name an argument it ignores; got %q", restricted)
	}
	if !strings.Contains(restricted, "cannot look beyond it") {
		t.Errorf("a never-policy deployment should say the search cannot be widened; got %q", restricted)
	}
}

// TestDownloadFailureSteps_TransientSourceEarnsARetry verifies a chain that failed
// because a source was unreachable — as opposed to answering that it does not hold
// the item — says so, since only the first case is worth retrying.
func TestDownloadFailureSteps_TransientSourceEarnsARetry(t *testing.T) {
	transient := fmt.Errorf("source scihub: %w", libgen.ErrSourceUnavailable)
	if got := strings.Join(downloadFailureSteps(libgen.Item{DOI: "10.1/x"}, transient, config.ExtraSourcesAuto), "\n"); !strings.Contains(got, "transient") {
		t.Errorf("an unavailable source should earn a retry suggestion; got %q", got)
	}
	settled := errors.New("source scihub: not indexed")
	if got := strings.Join(downloadFailureSteps(libgen.Item{DOI: "10.1/x"}, settled, config.ExtraSourcesAuto), "\n"); strings.Contains(got, "transient") {
		t.Errorf("a settled miss should not be described as transient; got %q", got)
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

// TestIntField covers reading a numeric column out of a catalog record. The
// catalog encodes them as strings, but the record is third-party data, so a JSON
// number is read too and everything else — a missing key, a nil map, a non-numeric
// string, a negative, absurd or fractional value — reports 0, i.e. "not known",
// which is what drops the size clause from the save confirmation.
func TestIntField(t *testing.T) {
	record := map[string]any{
		"filesize": "18298205",
		"padded":   "  4096 ",
		"json":     float64(2048),
		"words":    "unknown",
		"negative": "-1",
		"huge":     1e19, // beyond int64
		// Exactly 2^63: one past the largest int64, and the value math.MaxInt64
		// rounds to when it is converted to float64 — so a > math.MaxInt64 bound
		// would wave it through into an out-of-range conversion.
		"boundary": float64(1 << 63),
		// The largest float64 that is a valid int64, i.e. the last value that must
		// still be read rather than rejected.
		"boundaryOK": float64(1<<63 - 1024),
		// A fractional number is a corrupt record, not a roundable size: truncating
		// it would report a byte count the catalog never stated.
		"fraction":     12.5,
		"tinyFraction": 0.5,
		"negFraction":  -0.5,
		"wrong":        []any{1},
	}
	tests := []struct {
		key  string
		want int64
	}{
		{"filesize", 18298205},
		{"padded", 4096},
		{"json", 2048},
		{"words", 0},
		{"negative", 0},
		{"huge", 0},
		{"boundary", 0},
		{"boundaryOK", 1<<63 - 1024},
		{"fraction", 0},
		{"tinyFraction", 0},
		{"negFraction", 0},
		{"wrong", 0},
		{"absent", 0},
	}
	for _, tt := range tests {
		if got := intField(record, tt.key); got != tt.want {
			t.Errorf("intField(%q) = %d, want %d", tt.key, got, tt.want)
		}
	}
	if got := intField(nil, "filesize"); got != 0 {
		t.Errorf("intField(nil) = %d, want 0", got)
	}
}

// TestBookMetaEmptyReturnsNil verifies bookMeta returns no metadata when the
// record lookup yields no usable bibliographic fields, so naming falls back to the
// md5 — while still reporting the size the same record carries.
func TestBookMetaEmptyReturnsNil(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/json.php", func(w http.ResponseWriter, _ *http.Request) {
		// A file record with no title/author/year/extension and no related edition.
		_, _ = w.Write([]byte(`{"93485370":{"md5":"87a4ebdaf21fa6cc70009a3dd63194ee","filesize":"4096"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	client := libgen.New(staticMirrors{srv.URL}, cfg)
	details := bookMeta(context.Background(), client, "87a4ebdaf21fa6cc70009a3dd63194ee")
	if details.Meta != nil {
		t.Errorf("bookMeta().Meta = %+v, want nil (no usable fields)", details.Meta)
	}
	if details.Size != 4096 {
		t.Errorf("bookMeta().Size = %d, want 4096 (the catalog's filesize)", details.Size)
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
	// resolveFilename: explicit wins; a verified (md5) link is named from the
	// record; an unverified one is named from the identifier, never from the
	// requested record, so a mis-delivery is not disguised.
	if got := resolveFilename(libgen.Item{MD5: "x"}, "given.pdf", "pdf", true); got != "given.pdf" {
		t.Errorf("explicit filename: %q", got)
	}
	meta := resolveFilename(libgen.Item{MD5: "x", Meta: &libgen.FileMeta{Title: "T/it:le", Author: "A", Year: "2020"}}, "", "epub", true)
	if meta != "A - T_it -le (2020).epub" {
		t.Errorf("meta filename: %q", meta)
	}
	unverified := resolveFilename(libgen.Item{DOI: "10.1/x", Meta: &libgen.FileMeta{Title: "Some Paper", Author: "A"}}, "", "", false)
	if unverified != "10.1_x.pdf" {
		t.Errorf("unverified filename = %q, want the DOI rather than the requested record's title", unverified)
	}
	if got := resolveFilename(libgen.Item{ISBN: "9789286150616"}, "", "pdf", false); got != "9789286150616.pdf" {
		t.Errorf("resolveFilename(isbn) = %q, want the ISBN as the name", got)
	}
	if got := resolveFilename(libgen.Item{DOI: "10.1/x"}, "", "", false); got != "10.1_x.pdf" {
		t.Errorf("doi fallback filename: %q", got)
	}
	if got := resolveFilename(libgen.Item{MD5: "abc"}, "", "", true); got != "abc" {
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

	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(progresses)
	}
	if count() == 0 {
		reportMissingProgress(t, "download", count)
	}

	mu.Lock()
	defer mu.Unlock()
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
// through the (injected) DOI source and saves the bytes it serves.
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
	if out.SizeBytes != int64(len(payload)) {
		t.Errorf("SizeBytes = %d, want %d", out.SizeBytes, len(payload))
	}
	if !strings.HasSuffix(out.Path, ".pdf") {
		t.Errorf("Path = %q, want a .pdf name", out.Path)
	}
}

// downloadStructuredKeys returns the top-level keys of a download result's
// structured content, so a test can assert on what the wire actually carries
// rather than on what a typed decode happens to keep.
func downloadStructuredKeys(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if uerr := json.Unmarshal(data, &raw); uerr != nil {
		t.Fatal(uerr)
	}
	return raw
}

// downloadByDOISession wires a session whose only source is a DOI stub with the
// given name, serving payload from a local CDN. It is the fixture the provenance
// tests share: both need a download that a NAMED source demonstrably served.
func downloadByDOISession(t *testing.T, name string, payload []byte) *mcp.ClientSession {
	t.Helper()
	cdn := fileCDNServer(t, payload, "")
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	return newDownloadSession(t, cfg, staticMirrors{},
		libgen.WithSources(doiStubSource{name: name, fileURL: cdn.URL + "/file"}))
}

// TestDownloadWithholdsProvenance verifies that a download which named no source
// gets a result naming none either: no source and no mirror.
//
// The rule is that a result may only reveal what the call already revealed. The
// provenance of a file is a fact about the operator's configuration and the user's
// activity, and every field of a tool result is shipped to the client's inference
// provider, so a source name that answers no question the caller asked is a
// disclosure with no counterpart benefit. Both channels are checked, because the
// Markdown block is the one the model actually reads.
func TestDownloadWithholdsProvenance(t *testing.T) {
	session := downloadByDOISession(t, "scihub", []byte("%PDF-1.4 article fetched by DOI"))
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
	raw := downloadStructuredKeys(t, res)
	for _, banned := range []string{"source", "mirror"} {
		if v, present := raw[banned]; present {
			t.Errorf("structured output must not carry %q; got %v", banned, v)
		}
	}
	text := textContent(res)
	if strings.Contains(text, "scihub") {
		t.Errorf("the Markdown block must not name the serving source; got:\n%s", text)
	}
}

// TestDownloadPinnedCallGetsNoProvenanceEither verifies that pinning a source buys
// the caller no provenance in the result: no source name, and no flag about the pin.
//
// The flag existed here until it was noticed that it could not be false. A pinned
// call runs against that one source and nothing else, so the file arriving IS the
// answer — it came from the source the caller named — and an error is the other
// answer. Reporting a bit that is true whenever it is present tells the model
// nothing it did not already know from getting a file at all.
func TestDownloadPinnedCallGetsNoProvenanceEither(t *testing.T) {
	session := downloadByDOISession(t, "scihub", []byte("%PDF-1.4 article fetched by DOI"))
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"doi": "10.1371/journal.pone.0000217", "source": "scihub"},
	})
	if err != nil {
		t.Fatalf("CallTool(download) error = %v", err)
	}
	if res.IsError {
		t.Fatalf("download returned tool error: %+v", res.Content)
	}
	raw := downloadStructuredKeys(t, res)
	for _, banned := range []string{"source", "mirror", "served_by_requested_source"} {
		if v, present := raw[banned]; present {
			t.Errorf("a pinned call must not get %q back; got %v", banned, v)
		}
	}
	if text := textContent(res); strings.Contains(text, "scihub") || strings.Contains(text, "source you asked for") {
		t.Errorf("the Markdown block should say nothing about the pin or the source; got:\n%s", text)
	}
}

// TestRedactUnaskedAccount verifies the membership allowance is reported to the
// call that opted in and withheld from the one that did not.
//
// The withheld case is the one that matters. A server the operator configured a
// key on serves every book download over that membership, so without the gate a
// caller who never mentioned a membership learns that the operator holds a paid
// account and how much of today's allowance has been spent — neither of which it
// asked about.
func TestRedactUnaskedAccount(t *testing.T) {
	account := &libgen.AccountInfo{DownloadsLeft: 30, DownloadsPerDay: 50}

	asked := libgen.DownloadResult{Account: account}
	redactUnaskedAccount(&asked, DownloadInput{AnnasMember: true})
	if asked.Account == nil {
		t.Error("a call that set annas_member asked about the membership and must be told its allowance")
	}

	unasked := libgen.DownloadResult{Account: account}
	redactUnaskedAccount(&unasked, DownloadInput{})
	if unasked.Account != nil {
		t.Errorf("a call that never opted in must not be told the operator's allowance; got %+v", unasked.Account)
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
// metadata-built name ("Author - Title (Year).ext") from get_details, digest
// verified.
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

// TestToolDescriptionsHaveUntrustedNote verifies that every tool's prose carries
// an explicit caveat that what it returns is untrusted third-party data, never
// instructions to follow.
//
// All four descriptions ship one, and until this became a table only download
// and read were pinned — search's and get_details' could have been deleted with
// every test still green. The caveat is the server's whole defense against
// prompt injection through a fetched document, and it is the one sentence a
// model sees before it ever calls anything.
func TestToolDescriptionsHaveUntrustedNote(t *testing.T) {
	for _, tc := range []struct {
		name string
		desc string
	}{
		{"search", searchDescription},
		{"get_details", detailsDescription},
		{"read", readToolDescription},
		{
			"download",
			downloadToolDescription([]string{"libgen"}, []string{"oapen"}, []string{"scihub"}, false),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lower := strings.ToLower(tc.desc)
			if !strings.Contains(lower, "untrusted") {
				t.Fatalf("%s description should carry an untrusted-content caveat; got:\n%s", tc.name, tc.desc)
			}
			if !strings.Contains(lower, "never") && !strings.Contains(lower, "not as instructions") {
				t.Errorf("%s says content is untrusted but never says not to follow it; got:\n%s", tc.name, tc.desc)
			}
		})
	}
}

// TestDownloadDescriptionDisclosesShadowLibraries verifies the download tool says
// outright that its chain reaches Library Genesis and Anna's Archive mirrors, spells
// out which source name is which, and states where in the order they are reached.
//
// download is the only tool that retrieves a file, so the mapping is what lets a
// caller understand the bare names in the source argument's enum before it pins one;
// the read-only tools deliberately carry none, which is what
// TestReadOnlyToolsLeadWithTheirCapability guards. A chain with no shadow library in
// it says nothing, because then there is nothing to name.
func TestDownloadDescriptionDisclosesShadowLibraries(t *testing.T) {
	desc := downloadToolDescription(
		[]string{"libgen", "randombook", "annas"}, []string{"oapen"}, []string{"unpaywall", "scihub", "scidb"}, false,
	)
	for _, want := range []string{
		"shadow-library", "libgen is a Library Genesis mirror", "annas is Anna's Archive", "scihub is Sci-Hub",
		"randombook is a Library Genesis frontend", "scidb is Anna's Archive's SciDB article viewer",
		// The order is a mechanic, and it is the mechanic that decides whether a
		// mirror is reached at all, so it is disclosed with the names.
		"Openly licensed and open-access sources are tried first",
		"only when none of them serves the item",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("download description must disclose %q; got:\n%s", want, desc)
		}
	}
	clean := downloadToolDescription(nil, []string{"oapen"}, []string{"unpaywall"}, false)
	if strings.Contains(clean, "shadow-library") {
		t.Errorf("a chain with no shadow library needs no disclosure; got:\n%s", clean)
	}
}

// TestDownloadDescriptionDoesNotPrejudgeTheCall verifies the disclosure states
// mechanics rather than a verdict, because a verdict in the tool definition is read
// before the call is made and the call has not chosen a source yet.
//
// The description used to assert that the mirrors "host copyrighted works without
// the rightsholder's permission". That is a judgement, not a mechanic; it is also
// wrong about the public-domain and openly licensed material those mirrors carry,
// and it was applied to every download — including the majority that the open-access
// sources serve without a mirror being touched at all. What replaced it is the three
// facts a caller actually lacks: the order, that the serving source is not known
// until the call runs, and that the operator's source and credential configuration
// is invisible from here.
//
// The middle fact has since been revised three times. A saved file still does not
// disclose the serving source once the call HAS run; a resolved link does, because
// the URL handed back names the provider by its own hostname and a remote deployment
// resolves every call. The flag that briefly stood in for it is gone
// too, because a pin narrows the chain to that one source, so the flag could only
// ever be true. What the description carries in its place is the contract that makes
// that so — a pinned download is served by the pinned source or it fails — which is
// the routing fact a caller can act on and the one thing the source argument's own
// enum never explained. Both retired clauses are checked as banned text rather than
// merely dropped from the wanted list: each describes a field that no longer exists,
// and nothing else in the build can catch a description that lies.
func TestDownloadDescriptionDoesNotPrejudgeTheCall(t *testing.T) {
	desc := downloadToolDescription(
		[]string{"libgen", "annas"}, []string{"oapen"}, []string{"unpaywall", "scihub", "scidb"}, false,
	)
	for _, banned := range []string{
		"without the rightsholder's permission", "copyrighted works",
		"and named in the result", "whether a source you pinned served the file",
	} {
		if strings.Contains(desc, banned) {
			t.Errorf("download description must not pass judgement on the chain (%q); got:\n%s", banned, desc)
		}
	}
	for _, want := range []string{
		// The source is not selected when the description is read.
		"chosen while resolving",
		// Nor disclosed after it has been, with one exception the description must state
		// rather than paper over: a resolved link names its source, because the URL beside
		// it identifies the provider anyway. A saved file still names nothing, and what
		// stands in place of provenance is the pin's own contract, stated where the source
		// argument is introduced.
		"is named back only beside a resolved link, or in the optional account block",
		"restrict the download to one provider instead of all of them, with no substitution",
		"a failure means it could not serve the item",
		// The operator's configuration is the thing that settles this, and it is
		// not visible to the caller.
		"is set by the operator and is not visible to you",
		"do not infer from this list whether a given request is licensed",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("download description must state %q; got:\n%s", want, desc)
		}
	}
}

// TestReadOnlyToolsLeadWithTheirCapability verifies that the three read-only tools
// open on what they do — federated bibliographic search, metadata and citations,
// text extraction — rather than on the name of one catalog behind them.
//
// A tool that only reads metadata or text is chosen by capability, and leading with
// a shadow library's name in the very first line has clients decline bibliographic
// work the surface performs perfectly legally. The catalogs are still named further
// down, and in the field descriptions, where they explain behavior; only download
// leads with the chain's identity, because only download fetches a file.
func TestReadOnlyToolsLeadWithTheirCapability(t *testing.T) {
	for name, desc := range map[string]string{
		"search":      searchDescription,
		"get_details": detailsDescription,
		"read":        readToolDescription,
	} {
		lead := desc
		if para, _, found := strings.Cut(lead, "\n\n"); found {
			lead = para
		}
		if sentence, _, found := strings.Cut(lead, ". "); found {
			lead = sentence
		}
		for _, banned := range []string{"Library Genesis", "Anna's Archive", "Sci-Hub"} {
			if strings.Contains(lead, banned) {
				t.Errorf("%s must not lead with %q; opening line is:\n%s", name, banned, lead)
			}
		}
	}
}

// TestDownloadDescriptionNamesEachKeysChain verifies the prose keeps the three
// identifier chains apart, so the model never pins an ISBN-only source for an md5
// download (or the reverse), and mentions a key only when a source serves it.
func TestDownloadDescriptionNamesEachKeysChain(t *testing.T) {
	desc := downloadToolDescription([]string{"libgen", "annas"}, []string{"oapen", "archive"}, []string{"scihub"}, false)
	for _, want := range []string{
		"md5 (book)", "isbn (book)", "doi (article)",
		"- md5 (book): libgen then annas",
		"- isbn (book): oapen then archive",
		"- doi (article): scihub",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("description should contain %q; got:\n%s", want, desc)
		}
	}

	noISBN := downloadToolDescription([]string{"libgen"}, nil, []string{"scihub"}, false)
	if strings.Contains(noISBN, "isbn") {
		t.Errorf("description should not mention isbn when no source serves it; got:\n%s", noISBN)
	}
}

// TestDownloadDescriptionUsesParagraphs pins the opening's structure against
// the sixteen-way article fallback chain and the six other topics it used to
// share a single 2,100-character paragraph with: the resolution order renders
// as a "- key: chain" line per identifier, and the description as a whole is
// more than one paragraph, matching the convention search already sets.
func TestDownloadDescriptionUsesParagraphs(t *testing.T) {
	desc := downloadToolDescription(
		[]string{"libgen"}, []string{"oapen"},
		[]string{
			"unpaywall", "openalex", "europepmc", "biorxiv", "rfc", "nist", "dagstuhl", "acl", "zenodo",
			"scielo", "fao", "fatcat", "crossref", "oapen", "scihub", "scidb",
		},
		false,
	)
	paragraphs := strings.Split(desc, "\n\n")
	if len(paragraphs) < 4 {
		t.Fatalf("download description has %d paragraphs, want at least 4 (one dense block regressed); got:\n%s",
			len(paragraphs), desc)
	}
	if !strings.Contains(desc, "Resolution order, by identifier:\n- md5 (book): libgen\n- isbn (book): oapen") {
		t.Errorf("resolution order should list one identifier per line; got:\n%s", desc)
	}
}

// TestDownloadDescriptionMatchesTheDeploymentsContract is the regression test
// for the GEO-audit finding this rewrite fixes: a local server's opening claim
// ("Download a file... Returns the saved path and size") was false for the only
// publicly hosted deployment (mcp.jmrp.io, remote), corrected only by a note 359
// words later. The opening paragraph must now state the contract the running
// deployment actually honors, with no leftover claim from the other mode.
func TestDownloadDescriptionMatchesTheDeploymentsContract(t *testing.T) {
	local := downloadToolDescription([]string{"libgen"}, []string{"oapen"}, []string{"scihub"}, false)
	if !strings.Contains(local, "Returns the saved path and size") {
		t.Errorf("local description must state it returns the saved path and size; got:\n%s", local)
	}
	if strings.Contains(local, "ALWAYS returns a direct link") {
		t.Errorf("local description must not claim it always returns a link; got:\n%s", local)
	}

	remote := downloadToolDescription([]string{"libgen"}, []string{"oapen"}, []string{"scihub"}, true)
	if !strings.Contains(remote, "ALWAYS returns a direct link") ||
		!strings.Contains(remote, "cannot write to your disk") ||
		!strings.Contains(remote, "resolve_only is implied") {
		t.Errorf("remote description must state the link-only contract up front; got:\n%s", remote)
	}
	if strings.Contains(remote, "Returns the saved path and size") {
		t.Errorf("remote description must not carry the local mode's saved-path claim; got:\n%s", remote)
	}

	// The opening paragraph — what a model reads before deciding whether to call
	// the tool — is where the two descriptions must diverge, not a footnote.
	localOpen, _, _ := strings.Cut(local, "\n\n")
	remoteOpen, _, _ := strings.Cut(remote, "\n\n")
	if localOpen == remoteOpen {
		t.Error("local and remote opening paragraphs must differ in the return contract they state")
	}
}

// TestValidateDownloadInputISBN verifies the isbn argument is normalized for the
// sources (separators stripped) and that a value that is not an ISBN is rejected
// with a message saying so, rather than being sent to a provider as a junk query.
func TestValidateDownloadInputISBN(t *testing.T) {
	ids, err := validateDownloadInput(DownloadInput{ISBN: "978-92-86-15061-6"})
	if err != nil {
		t.Fatalf("validateDownloadInput(isbn) error = %v", err)
	}
	if ids.isbn != "9789286150616" {
		t.Errorf("isbn = %q, want the normalized 9789286150616", ids.isbn)
	}
	if _, badErr := validateDownloadInput(DownloadInput{ISBN: "12345"}); badErr == nil {
		t.Error("a malformed isbn should be rejected")
	}
	if _, emptyErr := validateDownloadInput(DownloadInput{}); emptyErr == nil {
		t.Error("a request with no identifier at all should be rejected")
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
	_, err := validateDownloadInput(DownloadInput{
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
	// ConfirmDownloads must be set explicitly: config.Load defaults it to true,
	// but a struct literal like this one gets the false zero value, which reads
	// as "the operator turned confirmations off" and skips the prompt entirely.
	return &config.Config{
		DownloadDir: t.TempDir(), Timeout: 5 * time.Second,
		RateRPS: 1000, RateBurst: 100, RetryAttempts: 1,
		ConfirmDownloads: true,
	}
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

// catalogConfirmMirror serves the whole book path a confirmed download walks — the
// json.php catalog record (file, then its edition), then ads.php → get.php → CDN —
// and counts the resolve step (ads.php) and the CDN's HEAD and GET separately. The
// counters are what let a test prove how many times ONE tool call resolved the
// item: the elicitation protocol runs the handler twice, so a prompt that resolves
// eagerly shows up here as a second ads.php hit.
//
// The record's filesize matches the payload, so the size the confirmation quotes
// can be checked against the file that is actually served.
func catalogConfirmMirror(t *testing.T, payload []byte) (srv *httptest.Server, ads, cdnGET, cdnHEAD *atomic.Int32) {
	t.Helper()
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])
	var adsHits, getHits, headHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/json.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("object") == "e" {
			_, _ = w.Write([]byte(`{"55":{"title":"Confirmed Book","author":"A. Author","year":"2020"}}`))
			return
		}
		fmt.Fprintf(w, `{"93485370":{"md5":%q,"extension":"pdf","filesize":"%d","editions":{"55":{"e_id":"55"}}}}`,
			wantMD5, len(payload))
	})
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, _ *http.Request) {
		adsHits.Add(1)
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
		_, _ = w.Write(payload)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &adsHits, &getHits, &headHits
}

// TestDownloadTool_ConfirmResolvesItemOnce is the regression test for the double
// resolution: one confirmed download must reach the mirror's resolve endpoint
// (ads.php) exactly ONCE, and must never probe the CDN with a HEAD.
//
// The elicitation protocol runs the download handler twice for a single tool call
// — once to put the question, once to act on the answer — and the confirmation
// prompt used to measure the file live, resolving the item through the whole
// mirror chain on each pass. That cost every confirmed download a duplicate
// resolution and doubled the traffic this server puts on a third party. The size
// now comes from the catalog record the call already fetches to name the file, so
// the asking pass makes no request of its own at all.
func TestDownloadTool_ConfirmResolvesItemOnce(t *testing.T) {
	payload := []byte("%PDF-1.4 resolve-once book payload")
	srv, ads, cdnGET, cdnHEAD := catalogConfirmMirror(t, payload)
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])

	cfg := confirmConfig(t)
	var elicits atomic.Int32
	var prompt string
	handler := func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		elicits.Add(1)
		prompt = req.Params.Message
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
		t.Fatalf("elicitation handler invoked %d times, want 1", elicits.Load())
	}
	if got := ads.Load(); got != 1 {
		t.Errorf("one confirmed download resolved the mirror %d time(s), want exactly 1", got)
	}
	if got := cdnHEAD.Load(); got != 0 {
		t.Errorf("the confirmation issued %d live size probe(s), want 0 (size comes from the catalog record)", got)
	}
	if got := cdnGET.Load(); got != 1 {
		t.Errorf("the file body was fetched %d time(s), want exactly 1", got)
	}
	// The prompt still states a size — the catalog's, which here matches the bytes
	// actually served.
	if want := humanBytes(int64(len(payload))); !strings.Contains(prompt, "("+want+")") {
		t.Errorf("confirmation prompt = %q, want it to quote the catalog size %q", prompt, want)
	}
	out := decodeDownloadOutput(t, res)
	if out.Path == "" {
		t.Fatalf("accepted download should report a saved path; got %+v", out)
	}
	if _, statErr := os.Stat(out.Path); statErr != nil {
		t.Errorf("accepted download did not write the file: %v", statErr)
	}
}

// TestDownloadTool_ConfirmUnknownSizeOmitsClause covers the other half of the
// size clause: a catalog record with no usable filesize leaves the prompt without
// one, rather than sending the server off to measure the file. The download still
// proceeds normally on acceptance.
func TestDownloadTool_ConfirmUnknownSizeOmitsClause(t *testing.T) {
	payload := []byte("%PDF-1.4 unknown-size book payload")
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])
	var cdnHEAD atomic.Int32
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/json.php", func(w http.ResponseWriter, _ *http.Request) {
		// A record whose filesize is not a number this server will read as one.
		fmt.Fprintf(w, `{"93485370":{"md5":%q,"extension":"pdf","filesize":"unknown"}}`, wantMD5)
	})
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<html><a href="get.php?md5=%s&key=TESTKEY123">GET</a></html>`, wantMD5)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/cdn/file", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/cdn/file", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			cdnHEAD.Add(1)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(payload)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var prompt string
	handler := func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		prompt = req.Params.Message
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
	}
	session := newConfirmSession(t, confirmConfig(t), staticMirrors{srv.URL}, handler)

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
	if strings.Contains(prompt, "(") {
		t.Errorf("confirmation prompt = %q, want no size clause when the catalog reports none", prompt)
	}
	if got := cdnHEAD.Load(); got != 0 {
		t.Errorf("an unknown size triggered %d live probe(s), want 0", got)
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

// oaDblpHits is a one-hit dblp search response used by the extra-source search
// tests; it carries a distinct DOI so it is not deduped against the others.
const oaDblpHits = `{"result":{"hits":{"@total":"1","hit":[{
  "info":{"authors":{"author":{"text":"Edsger W. Dijkstra"}},
  "title":"A Conference Paper.","venue":"DAC","year":"2018",
  "doi":"10.3000/dblp-only","ee":"https://doi.org/10.3000/dblp-only"}}]}}}`

// oaPubMedSearch is a one-PMID esearch response, and oaPubMedSummary the matching
// esummary record, used by the extra-source search tests. The DOI is distinct so the
// hit is not deduped against the others.
const (
	oaPubMedSearch  = `{"esearchresult":{"count":"1","idlist":["12345678"]}}`
	oaPubMedSummary = `{"result":{"uids":["12345678"],
  "12345678":{"uid":"12345678","title":"A Biomedical Paper.","pubdate":"2020 Mar 4",
   "fulljournalname":"Journal of Tests","authors":[{"name":"Doudna JA"}],
   "articleids":[{"idtype":"pubmed","value":"12345678"},
                 {"idtype":"doi","value":"10.4000/pubmed-only"}]}}}`
)

// oaEricDocs is a one-doc ERIC search response used by the extra-source search tests.
// It is a hosted ED report: no DOI at all, so it is deduped against nothing, and an
// e_fulltextauth flag of 1 so the hit must reach the caller carrying the deterministic
// files.eric.ed.gov URL that is the only way to obtain it.
const oaEricDocs = `{"response":{"numFound":1,"start":0,"docs":[
  {"id":"ED427241","title":"An Education Technical Report.",
   "author":["Drennon, Cassandra"],"publicationdateyear":1998,
   "institution":["Virginia Commonwealth Univ., Richmond."],"e_fulltextauth":1}
]}}`

// oaDiscoveryServers spins up an httptest server for every network-backed discovery
// provider, points the discovery package at them for the duration of the test, and
// returns a counter of the total discovery requests observed so a test can assert
// whether discovery was called at all.
func oaDiscoveryServers(t *testing.T) *int32 {
	t.Helper()
	var hits int32
	body := func(payload string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&hits, 1)
			_, _ = w.Write([]byte(payload))
		}
	}
	arxiv := httptest.NewServer(body(oaArxivFeed))
	crossref := httptest.NewServer(body(oaCrossrefWorks))
	openLibrary := httptest.NewServer(body(oaOpenLibraryDocs))
	dblp := httptest.NewServer(body(oaDblpHits))
	// PubMed takes two hops against distinct paths on one host.
	pubmedMux := http.NewServeMux()
	pubmedMux.HandleFunc("/esearch.fcgi", body(oaPubMedSearch))
	pubmedMux.HandleFunc("/esummary.fcgi", body(oaPubMedSummary))
	pubmed := httptest.NewServer(pubmedMux)
	eric := httptest.NewServer(body(oaEricDocs))

	restore := discovery.SetBasesForTest(discovery.ProviderBases{
		Arxiv: arxiv.URL, Crossref: crossref.URL, OpenLibrary: openLibrary.URL,
		DBLP: dblp.URL, PubMed: pubmed.URL, ERIC: eric.URL,
	})
	t.Cleanup(func() {
		restore()
		arxiv.Close()
		crossref.Close()
		openLibrary.Close()
		dblp.Close()
		pubmed.Close()
		eric.Close()
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

// assertEricHitIsFetchable checks the property the ERIC integration rests on: the
// hosted report reaches the caller with no DOI — download and read have no key for it
// — but with the deterministic files.eric.ed.gov URL and an open-access flag, which is
// the stated way to obtain the file.
func assertEricHitIsFetchable(t *testing.T, hits []discovery.DiscoveryResult) {
	t.Helper()
	for _, h := range hits {
		if h.Origin != "eric" {
			continue
		}
		if h.DOI != "" {
			t.Errorf("eric hit DOI = %q, want empty for a report with no DOI", h.DOI)
		}
		if h.PDFURL != "https://files.eric.ed.gov/fulltext/ED427241.pdf" {
			t.Errorf("eric hit PDFURL = %q, want the hosted full-text URL", h.PDFURL)
		}
		if !h.OpenAccess {
			t.Error("eric hit OpenAccess = false, want true for a hosted full text")
		}
		return
	}
	t.Error("no eric hit in the open-access list")
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
	for _, want := range []string{"arxiv", "crossref", "openlibrary", "dblp", "pubmed", "eric"} {
		if !origins[want] {
			t.Errorf("expected a hit labeled %q, got origins %v", want, origins)
		}
	}
	assertEricHitIsFetchable(t, oa)

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

// TestProgressNotifierLogsAnUndeliveredNotification covers the send-failure
// branch: emission is best-effort, so a notification that cannot be delivered
// must not panic, block or surface to the caller — but it must no longer vanish
// silently, because a drop in transit is otherwise indistinguishable from a
// notification that was never emitted.
//
// A canceled context is the deterministic way to make the send fail; provoking a
// real transport failure would race the connection's teardown.
//
// It does not call t.Parallel: it swaps the process-wide default logger.
func TestProgressNotifierLogsAnUndeliveredNotification(t *testing.T) {
	logs := captureToolsLog(t)

	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.1"}, nil)
	st, ct := mcp.NewInMemoryTransports()
	session, err := server.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil).
		Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	params := &mcp.CallToolParamsRaw{Name: "download"}
	params.SetProgressToken("tok-undeliverable")
	notify := progressNotifier(ctx, &mcp.CallToolRequest{Session: session, Params: params})
	if notify == nil {
		t.Fatal("progressNotifier returned nil for a request carrying a progress token")
	}

	notify(7, 11) // must return normally despite the send failing

	if got := logs.String(); !strings.Contains(got, "progress notification not delivered") {
		t.Errorf("an undelivered notification was not logged; got:\n%s", got)
	}
}

// captureToolsLog redirects slog to a buffer for the duration of the test and
// returns it.
func captureToolsLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return &buf
}

// progressGrace bounds the extra wait a failing progress assertion allows itself
// before it reports. It is diagnostic only — the assertion fails either way.
const progressGrace = 3 * time.Second

// reportMissingProgress fails the calling test with the diagnosis a flaking run
// needs, having waited up to progressGrace for a notification the assertion may
// simply have raced.
//
// The distinction is the whole point. One that lands during the grace period
// means delivery lost a race with CallTool returning, which is a problem with
// this assertion. One that never lands means it was never emitted or was dropped
// in transit, which is a problem with the server — and progressNotifier now logs
// a failed send, so a run that hits this carries that half of the answer too.
//
// Neither case is tolerated: this always fails. TestDownloadToolWithProgressToken
// flaked once in CI on 2026-08-21 and could not be reproduced locally across the
// coverage and GOMAXPROCS variations, so the next occurrence has to explain
// itself rather than be waited out.
func reportMissingProgress(t *testing.T, what string, count func() int) {
	t.Helper()
	deadline := time.Now().Add(progressGrace)
	for time.Now().Before(deadline) {
		if count() > 0 {
			t.Fatalf("%s: no progress notification had arrived when the tool call returned, "+
				"but one landed within %s — the assertion raced delivery", what, progressGrace)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s: no progress notification arrived at all, even %s after the tool call returned "+
		"— it was never emitted, or it was dropped in transit", what, progressGrace)
}

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
	restore := discovery.SetBasesForTest(discovery.ProviderBases{
		Arxiv: arxiv.URL, Crossref: quiet.URL, OpenLibrary: quiet.URL,
		DBLP: quiet.URL, PubMed: quiet.URL,
	})
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
		func(_ context.Context, req *mcp.CallToolRequest, in unpaywallProbeInput) (*mcp.CallToolResult, unpaywallProbeOutput, error) {
			cfg := &config.Config{UnpaywallEmail: in.ConfiguredEmail}
			round := newInputRound(req)
			email := elicitUnpaywallEmail(round, cfg, DownloadInput{DOI: in.DOI, Source: in.Source})
			if pending := round.needsInput(); pending != nil {
				return pending, unpaywallProbeOutput{}, nil
			}
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

// annasProbeInput drives the "aprobe" tool below: it carries the DownloadInput
// fields elicitAnnasKey branches on, plus the key the server is configured with.
type annasProbeInput struct {
	MD5           string `json:"md5,omitempty"`
	Source        string `json:"source,omitempty"`
	AnnasMember   bool   `json:"annas_member,omitempty"`
	ConfiguredKey string `json:"configured_key,omitempty"`
}

// annasProbeOutput carries the key elicitAnnasKey produced back to the test.
type annasProbeOutput struct {
	Key string `json:"key"`
}

// newAnnasProbeSession wires an in-memory MCP server exposing an "aprobe" tool
// that calls elicitAnnasKey with a config/input built from the request, so tests
// can exercise its capability-gated branches through a real round-trip. A nil
// handler means the client advertises no elicitation capability. It mirrors
// newUnpaywallProbeSession, the harness for the sibling credential prompt.
func newAnnasProbeSession(t *testing.T, handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "aprobe", Description: "exercises elicitAnnasKey for tests"},
		func(_ context.Context, req *mcp.CallToolRequest, in annasProbeInput) (*mcp.CallToolResult, annasProbeOutput, error) {
			cfg := &config.Config{AnnasKey: in.ConfiguredKey}
			round := newInputRound(req)
			key := elicitAnnasKey(round, cfg, DownloadInput{
				MD5: in.MD5, Source: in.Source, AnnasMember: in.AnnasMember,
			})
			if pending := round.needsInput(); pending != nil {
				return pending, annasProbeOutput{}, nil
			}
			return nil, annasProbeOutput{Key: key}, nil
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

// callAprobe drives the aprobe tool once and returns the key elicitAnnasKey
// produced.
func callAprobe(t *testing.T, session *mcp.ClientSession, in annasProbeInput) string {
	t.Helper()
	args, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshaling aprobe input: %v", err)
	}
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "aprobe", Arguments: json.RawMessage(args)})
	if err != nil {
		t.Fatalf("CallTool(aprobe) failed: %v", err)
	}
	if res.IsError {
		t.Fatalf("aprobe returned a tool error: %+v", res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshaling aprobe output: %v", err)
	}
	var out annasProbeOutput
	if uerr := json.Unmarshal(raw, &out); uerr != nil {
		t.Fatalf("decoding aprobe output: %v", uerr)
	}
	return out.Key
}

// annasMemberBook is the input shape that makes elicitAnnasKey prompt: an md5
// book download that explicitly opted in to the member tier.
var annasMemberBook = annasProbeInput{MD5: "0123456789abcdef0123456789abcdef", AnnasMember: true}

// TestElicitAnnasKey_AcceptedKeyIsUsed covers the happy path: an opted-in book
// download against a keyless server prompts the client, and the accepted secret
// comes back trimmed for use on this request only.
func TestElicitAnnasKey_AcceptedKeyIsUsed(t *testing.T) {
	session := newAnnasProbeSession(t, acceptHandler(map[string]any{"key": "  SECRET123  "}))
	if got := callAprobe(t, session, annasMemberBook); got != "SECRET123" {
		t.Errorf("an accepted key should come back trimmed, got %q", got)
	}
}

// TestElicitAnnasKey_PinnedAnnasSourceStillPrompts covers the one named source the
// per-call key can still take effect for: pinning source="annas" replaces the
// configured (keyless) source with one carrying the elicited key, so the prompt
// must still fire.
func TestElicitAnnasKey_PinnedAnnasSourceStillPrompts(t *testing.T) {
	session := newAnnasProbeSession(t, acceptHandler(map[string]any{"key": "SECRET123"}))
	in := annasMemberBook
	in.Source = "ANNAS" // case-insensitive by contract
	if got := callAprobe(t, session, in); got != "SECRET123" {
		t.Errorf("source=annas should still prompt for a key, got %q", got)
	}
}

// TestElicitAnnasKey_SkippedBranches covers every arm that must collapse to ""
// WITHOUT prompting. The handler would accept a key, so a non-empty result proves
// the prompt fired where it should not have: each of these is a dead-end question
// the user would be asked for nothing.
func TestElicitAnnasKey_SkippedBranches(t *testing.T) {
	cases := []struct {
		name string
		in   annasProbeInput
		why  string
	}{
		{"no opt-in", annasProbeInput{MD5: annasMemberBook.MD5}, "the keyless IPFS path already works, so an unrequested prompt is a nag"},
		{"no md5", annasProbeInput{AnnasMember: true}, "the member tier is a book path; there is nothing to fetch without an md5"},
		{"server has a key", annasProbeInput{MD5: annasMemberBook.MD5, AnnasMember: true, ConfiguredKey: "CONFIGURED"}, "the configured key already covers the request"},
		{"other source pinned", annasProbeInput{MD5: annasMemberBook.MD5, AnnasMember: true, Source: "libgen"}, "the per-call key can never reach a non-annas source"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := newAnnasProbeSession(t, acceptHandler(map[string]any{"key": "SECRET123"}))
			if got := callAprobe(t, session, tc.in); got != "" {
				t.Errorf("%s: expected no prompt and \"\", got %q", tc.why, got)
			}
		})
	}
}

// TestElicitAnnasKey_NoCapability covers the fallback path: a client that never
// advertised elicitation is not prompted, so the annas source stays keyless and
// resolves over IPFS exactly as it does today.
func TestElicitAnnasKey_NoCapability(t *testing.T) {
	session := newAnnasProbeSession(t, nil)
	if got := callAprobe(t, session, annasMemberBook); got != "" {
		t.Errorf("a client without the elicitation capability should yield \"\", got %q", got)
	}
}

// TestElicitAnnasKey_DeclineAndEmpty covers the two answers that must leave the
// download keyless: a declined prompt, and an accepted prompt with a blank key
// (the prompt itself offers "leave empty to download over IPFS instead").
func TestElicitAnnasKey_DeclineAndEmpty(t *testing.T) {
	declined := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	if got := callAprobe(t, newAnnasProbeSession(t, declined), annasMemberBook); got != "" {
		t.Errorf("a declined prompt should yield \"\", got %q", got)
	}
	blank := acceptHandler(map[string]any{"key": "   "})
	if got := callAprobe(t, newAnnasProbeSession(t, blank), annasMemberBook); got != "" {
		t.Errorf("a blank key should yield \"\", got %q", got)
	}
}

// TestSearchNextStepsForbidsInventingResults verifies an empty search tells the
// model not to fill the gap. The recovery advice alone leaves the door open: a
// model that has been asked to find something and told only "try broadening" can
// still answer as if it had found it.
func TestSearchNextStepsForbidsInventingResults(t *testing.T) {
	joined := strings.ToLower(strings.Join(searchNextSteps(SearchOutput{Results: []libgen.Result{}}, false, config.ExtraSourcesAuto), "\n"))
	if !strings.Contains(joined, "do not") {
		t.Errorf("empty-search guidance must state plainly what not to do; got %q", joined)
	}
	if !strings.Contains(joined, "were not returned") && !strings.Contains(joined, "did not receive") {
		t.Errorf("empty-search guidance must name the thing not to invent; got %q", joined)
	}
}

// TestSearchNextStepsPinsTheSourceForEscalatedResults verifies an Anna's-origin
// result is told to download with source="annas". Without it the chain starts at
// libgen and burns its whole start-retry schedule on an md5 the catalog does not
// have: a live run measured 235 seconds for a download that takes seconds once the
// source is pinned.
func TestSearchNextStepsPinsTheSourceForEscalatedResults(t *testing.T) {
	const annasMD5 = "00dd2b0b58e81e3c6e7cb9e7b72dee23"
	escalated := strings.Join(searchNextSteps(SearchOutput{
		Results: []libgen.Result{{MD5: annasMD5, Origin: "annas"}},
		Page:    1,
	}, false, config.ExtraSourcesAuto), "\n")
	if !strings.Contains(escalated, `"source":"annas"`) {
		t.Errorf("an annas-origin result should be downloaded with the source pinned; got %q", escalated)
	}

	// A catalog result must not be pinned: the chain's ordinary order is right for it.
	catalog := strings.Join(searchNextSteps(SearchOutput{
		Results: []libgen.Result{{MD5: "0123456789abcdef0123456789abcdef", Origin: "libgen"}},
		Page:    1,
	}, false, config.ExtraSourcesAuto), "\n")
	if strings.Contains(catalog, `"source"`) {
		t.Errorf("a catalog result should not pin a source; got %q", catalog)
	}
}

// TestDetailsByDOIFallsBackToEnrichment verifies a DOI the catalog does not carry
// still answers with what is available. An open-access hit carries a DOI the
// catalog has never heard of — a live run caught a model taking a Crossref DOI to
// get_details, receiving a hard error, and spending a turn recovering, when the
// journal and citation metadata it had asked for was a keyless lookup away.
func TestDetailsByDOIFallsBackToEnrichment(t *testing.T) {
	crossref := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"container-title":["Cell"],"is-referenced-by-count":42,
			"title":["Hallmarks of Cancer"],"published":{"date-parts":[[2011]]}}}`))
	}))
	t.Cleanup(crossref.Close)
	restore := discovery.SetBasesForTest(discovery.ProviderBases{Crossref: crossref.URL})
	t.Cleanup(restore)

	// The catalog answers every json.php lookup with an empty array: no record.
	mux := http.NewServeMux()
	mux.HandleFunc("/json.php", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100,
		RetryAttempts: 1, EnrichEnabled: true, UnpaywallEmail: "test@example.com",
	}
	handler := detailsHandler(libgen.New(staticMirrors{srv.URL}, cfg), cfg, nil)
	_, out, err := handler(context.Background(), nil, DetailsInput{DOI: "10.1016/j.cell.2011.02.013", Enrich: true})
	if err != nil {
		t.Fatalf("a DOI the catalog lacks should still answer from Crossref, got: %v", err)
	}
	if out.Enrichment == nil {
		t.Fatal("no enrichment returned; the fallback produced nothing useful")
	}
	if out.Enrichment.Crossref == nil || out.Enrichment.Crossref.ContainerTitle != "Cell" {
		t.Errorf("enrichment did not carry the Crossref journal: %+v", out.Enrichment)
	}
}

// TestEnrichKillSwitchCannotBeLifted verifies LIBGEN_MCP_ENRICH=false holds against
// a per-call enrich=true. It is documented as a deployment kill-switch, and a
// switch a caller can flip is not one — the same hole found in the extra-sources
// never mode and in the per-call credential paths.
func TestEnrichKillSwitchCannotBeLifted(t *testing.T) {
	crossref := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"container-title":["Cell"],"is-referenced-by-count":1}}`))
	}))
	t.Cleanup(crossref.Close)
	restore := discovery.SetBasesForTest(discovery.ProviderBases{Crossref: crossref.URL})
	t.Cleanup(restore)

	mux := http.NewServeMux()
	mux.HandleFunc("/json.php", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"1":{"md5":"87a4ebdaf21fa6cc70009a3dd63194ee","doi":"10.1/x"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100,
		RetryAttempts: 1, EnrichEnabled: false, UnpaywallEmail: "test@example.com",
	}
	handler := detailsHandler(libgen.New(staticMirrors{srv.URL}, cfg), cfg, nil)
	_, out, err := handler(context.Background(), nil, DetailsInput{
		MD5: "87a4ebdaf21fa6cc70009a3dd63194ee", Enrich: true,
	})
	if err != nil {
		t.Fatalf("the lookup itself should still work: %v", err)
	}
	if out.Enrichment != nil {
		t.Errorf("enrichment was produced against a deployment that forbids it: %+v", out.Enrichment)
	}
}

// TestSingleValueFieldsSayTheyAreNotArrays verifies every string field on the
// search input warns it is not an array when array-valued fields sit beside it.
// A model sent extra_sources=["always"], having pattern-matched topics and
// search_in; order and order_mode already carried the warning, evidently for the
// same reason, and the field added later did not.
func TestSingleValueFieldsSayTheyAreNotArrays(t *testing.T) {
	schema, err := jsonschema.For[SearchInput](nil)
	if err != nil {
		t.Fatal(err)
	}
	var arrays []string
	for name, prop := range schema.Properties {
		if prop.Items != nil {
			arrays = append(arrays, name)
		}
	}
	// No arrays would mean nothing can be confused with one — and would also mean
	// this test has stopped testing anything, so it is a failure, not a skip.
	if len(arrays) == 0 {
		t.Fatal("no array-valued fields found; the schema shape changed and this check is now vacuous")
	}
	// query is free text: nobody sends a sentence as an array. Every other string
	// field carries a short token, which is exactly what gets mistaken for one of
	// the array-valued fields beside it.
	freeText := map[string]bool{"query": true}
	for name, prop := range schema.Properties {
		if prop.Items != nil || prop.Type != "string" || freeText[name] {
			continue
		}
		if !strings.Contains(strings.ToLower(prop.Description), "single value") {
			t.Errorf("%q is a token-valued string beside %v but never says it is not an array: %q",
				name, arrays, prop.Description)
		}
	}
}

// TestFieldErrorsNameTheField verifies an invalid argument is reported against the
// argument, not against something the caller never touched. The extra-sources mode
// error named LIBGEN_MCP_EXTRA_SOURCES even when the value came from the search
// call, sending a model to inspect an environment variable it had never set.
func TestFieldErrorsNameTheField(t *testing.T) {
	cfg := &config.Config{ExtraSources: config.ExtraSourcesAuto}
	_, err := resolveExtraMode(SearchInput{ExtraSources: "sometimes"}, cfg)
	if err == nil {
		t.Fatal("an unrecognized mode should be rejected")
	}
	if !strings.Contains(err.Error(), "extra_sources") {
		t.Errorf("error %q should name the argument the caller set", err)
	}
	if strings.Contains(err.Error(), "LIBGEN_MCP_") {
		t.Errorf("error %q points the caller at an environment variable it never set", err)
	}
}

// TestMergeCarriesFormatAndSizeFromAnnas verifies an escalated result keeps what
// Anna's said about the file. The merge builds a catalog-shaped result from a
// discovery hit, and dropping the format and size there would undo the parsing
// entirely — the caller would still be unable to compare an escalated result with
// a catalog one.
func TestMergeCarriesFormatAndSizeFromAnnas(t *testing.T) {
	out := SearchOutput{Results: []libgen.Result{}}
	mergeExtraHits(&out, []discovery.DiscoveryResult{{
		Origin: "annas", MD5: "00dd2b0b58e81e3c6e7cb9e7b72dee23",
		Title: "Some Book", Extension: "pdf", Size: "1.3MB",
	}})
	if len(out.Results) != 1 {
		t.Fatalf("expected the hit to merge into results, got %d", len(out.Results))
	}
	if got := out.Results[0]; got.Extension != "pdf" || got.Size != "1.3MB" {
		t.Errorf("merged result lost the file description: ext=%q size=%q", got.Extension, got.Size)
	}
}

// TestAnnasFallbackEnrichesByISBN verifies an Anna's record with an ISBN reaches
// the same keyless metadata a catalog record would. The fallback already returns
// what Anna's knows about the file; stopping there leaves a lookup on the table
// that costs nothing and that the caller explicitly asked for.
func TestAnnasFallbackEnrichesByISBN(t *testing.T) {
	openLibrary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"title":"Sejarah Indonesia","subjects":["History"],
			"description":"A survey.","covers":[12345]}`))
	}))
	t.Cleanup(openLibrary.Close)
	// The tools layer enriches through the libgen client, whose API roots are its
	// own; pointing discovery's at the stub would leave this hitting the live site.
	t.Cleanup(libgen.SetEnrichBasesForTest("", openLibrary.URL))

	page := mustReadFile(t, "../discovery/testdata/annas_md5_zlib.html")
	annas := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/md5/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(page)
	}))
	t.Cleanup(annas.Close)

	// The catalog knows nothing, so the Anna's fallback is the only route.
	mux := http.NewServeMux()
	mux.HandleFunc("/json.php", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100,
		RetryAttempts: 1, EnrichEnabled: true, UnpaywallEmail: "test@example.com",
	}
	handler := detailsHandler(libgen.New(staticMirrors{srv.URL}, cfg), cfg, staticMirrors{annas.URL})
	_, out, err := handler(context.Background(), nil, DetailsInput{
		MD5: "00dd2b0b58e81e3c6e7cb9e7b72dee23", Enrich: true,
	})
	if err != nil {
		t.Fatalf("the Anna's fallback should have answered: %v", err)
	}
	if got := stringField(out.File, "origin"); got != "annas" {
		t.Fatalf("file.origin = %q, want annas", got)
	}
	if out.Enrichment == nil || out.Enrichment.OpenLibrary == nil {
		t.Fatalf("the record carries an ISBN but no OpenLibrary metadata came back: %+v", out.Enrichment)
	}
}

// TestSearchNextStepsSeparatesOpenAccessFromTheCatalog verifies the guidance says
// which results are open access and which are not. Asked for open-access papers, a
// model that received only OpenLibrary hits — a book catalog, carrying no DOI and
// no PDF — answered with articles from the catalog results instead, listing
// Sci-Hub links under an "Open-Access Papers" heading. Nothing in the response had
// told it those are different things.
func TestSearchNextStepsSeparatesOpenAccessFromTheCatalog(t *testing.T) {
	joined := strings.ToLower(strings.Join(searchNextSteps(SearchOutput{
		Results: []libgen.Result{{MD5: "0123456789abcdef0123456789abcdef", Origin: "libgen"}},
		OpenAccess: []discovery.DiscoveryResult{
			{Origin: "openlibrary", Title: "A Book", ISBN: "9780000000001"},
		},
		Page: 1,
	}, true, config.ExtraSourcesAuto), "\n"))
	if !strings.Contains(joined, "open_access") {
		t.Errorf("guidance never names the open_access list; got %q", joined)
	}
	if !strings.Contains(joined, "not open access") {
		t.Errorf("guidance never says the catalog results are not open access; got %q", joined)
	}
}

// confirmWantedProbeOutput reports what wantConfirmation decided, so the
// decision can be asserted through a real session that genuinely does (or does
// not) advertise elicitation.
type confirmWantedProbeOutput struct {
	Wanted bool `json:"wanted"`
}

// runConfirmationWanted drives wantConfirmation inside a live MCP session.
// elicit selects whether the client advertises the elicitation capability, which
// is the one input that cannot be faked from outside a session.
func runConfirmationWanted(t *testing.T, elicit bool, cfg *config.Config, consent *downloadConsent, in DownloadInput, preRemember bool) bool {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "probe", Description: "reports wantConfirmation for tests"},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, confirmWantedProbeOutput, error) {
			if preRemember {
				consent.remember(req.Session)
			}
			return nil, confirmWantedProbeOutput{Wanted: wantConfirmation(false, cfg, consent, req, in)}, nil
		})

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	var copts mcp.ClientOptions
	if elicit {
		copts.ElicitationHandler = func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
		}
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, &copts).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "probe", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out confirmWantedProbeOutput
	if uerr := json.Unmarshal(raw, &out); uerr != nil {
		t.Fatal(uerr)
	}
	return out.Wanted
}

// TestConfirmationWanted covers every way the download prompt can be switched
// off, and the one case where it must still appear. The three opt-outs are
// independent: each alone is enough.
func TestConfirmationWanted(t *testing.T) {
	asking := &config.Config{ConfirmDownloads: true}
	silent := &config.Config{ConfirmDownloads: false}

	for _, tc := range []struct {
		name        string
		elicit      bool
		cfg         *config.Config
		in          DownloadInput
		preRemember bool
		want        bool
	}{
		{"default: a capable client is asked", true, asking, DownloadInput{}, false, true},
		{"env var off silences it", true, silent, DownloadInput{}, false, false},
		{"this session already opted out", true, asking, DownloadInput{}, true, false},
		{"a client that cannot be asked is never prompted", false, asking, DownloadInput{}, false, false},
		{"opt-outs combine without conflicting", true, silent, DownloadInput{}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runConfirmationWanted(t, tc.elicit, tc.cfg, &downloadConsent{}, tc.in, tc.preRemember)
			if got != tc.want {
				t.Fatalf("wantConfirmation = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestConfirmationWanted_NilConfigStillAsks pins the defensive branch: a missing
// config must not be read as "the operator disabled confirmations".
func TestConfirmationWanted_NilConfigStillAsks(t *testing.T) {
	if !runConfirmationWanted(t, true, nil, &downloadConsent{}, DownloadInput{}, false) {
		t.Fatal("a nil config must fall back to asking, not to silence")
	}
}

// TestSearchNextSteps_EmptyResultsPointBeyondTheCatalog covers the recovery a
// zero-result search used to omit. The advice has to depend on whether the extra
// searchers already ran: suggesting extra_sources="always" after it just ran and
// found nothing sends the model to repeat a query that cannot succeed.
//
// It also has to depend on whether they CAN run. Under a deployment policy of
// never they never do, so extrasRan is false forever and the escalation advice used
// to be handed out on every empty search — for an argument the server then
// discarded, which returned the same advice again. The last case is the guard on
// that loop.
func TestSearchNextSteps_EmptyResultsPointBeyondTheCatalog(t *testing.T) {
	for _, tc := range []struct {
		name       string
		extrasRan  bool
		policy     config.ExtraSourcesMode
		wantSubstr string
		notWant    string
	}{
		{"extras not consulted", false, config.ExtraSourcesAuto, `extra_sources="always"`, "also returned nothing"},
		{"extras already ran", true, config.ExtraSourcesAuto, "also returned nothing", `extra_sources="always"`},
		{"policy forbids them", false, config.ExtraSourcesNever, "restricts search to the Library Genesis catalog", "Retry with"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := searchNextSteps(SearchOutput{}, tc.extrasRan, tc.policy)
			joined := strings.Join(steps, "\n")
			if !strings.Contains(joined, tc.wantSubstr) {
				t.Fatalf("guidance missing %q:\n%s", tc.wantSubstr, joined)
			}
			if strings.Contains(joined, tc.notWant) {
				t.Fatalf("guidance should not contain %q here:\n%s", tc.notWant, joined)
			}
			// The anti-hallucination guardrail must survive on every path.
			if !strings.Contains(joined, "do not present titles") {
				t.Fatalf("the do-not-invent-results guardrail was dropped:\n%s", joined)
			}
		})
	}
}

// TestSearchTool_NeverPolicyDoesNotAdviseAnIgnoredArgument is the end-to-end guard
// on the loop, and on the wiring that causes it: the deployment policy has to reach
// the guidance builder, not just the escalation decision.
//
// Under LIBGEN_MCP_EXTRA_SOURCES=never an empty search used to recommend
// extra_sources="always"; resolveExtraMode discards that argument under this
// policy, so the retry returned the same empty result and the same recommendation.
// A live run survived it only because the model gave up after the second attempt.
func TestSearchTool_NeverPolicyDoesNotAdviseAnIgnoredArgument(t *testing.T) {
	emptyHTML, err := os.ReadFile("../libgen/testdata/search_empty.html")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, _ *http.Request) { w.Write(emptyHTML) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100,
		RetryAttempts: 1, ExtraSources: config.ExtraSourcesNever,
	}
	session := newDownloadSession(t, cfg, staticMirrors{srv.URL})

	// Ask for the escalation explicitly: the policy overrides it, so the guidance
	// must not turn round and recommend it again.
	res, cerr := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "nothingmatches", "extra_sources": "always"},
	})
	if cerr != nil {
		t.Fatal(cerr)
	}
	if res.IsError {
		t.Fatalf("empty search returned a tool error: %v", res.Content)
	}
	text := contentText(res)
	if strings.Contains(text, `extra_sources="always"`) {
		t.Errorf("a never-policy deployment must not recommend an argument it ignores:\n%s", text)
	}
	if !strings.Contains(text, "restricts search to the Library Genesis catalog") {
		t.Errorf("guidance should say the deployment restricts the search:\n%s", text)
	}
}

// TestSearchNextSteps_ExtrasRanButFoundNothing covers the branch that fires when
// the beyond-catalog searchers ran alongside catalog results and returned
// nothing. The guidance matters: without it a model can present a catalog hit as
// though the wider open-access search had endorsed it.
func TestSearchNextSteps_ExtrasRanButFoundNothing(t *testing.T) {
	out := SearchOutput{Results: []libgen.Result{{MD5: "d41d8cd98f00b204e9800998ecf8427e"}}}
	joined := strings.Join(searchNextSteps(out, true, config.ExtraSourcesAuto), "\n")
	if !strings.Contains(joined, "extra searchers returned nothing") {
		t.Fatalf("missing the empty-extras guidance:\n%s", joined)
	}
	if !strings.Contains(joined, "not open access") {
		t.Fatalf("missing the do-not-claim-open-access warning:\n%s", joined)
	}
}

// TestSearchTool_RejectsAnUnknownExtraSourcesMode pins the validation path: a
// bad extra_sources value must fail the call outright rather than silently
// falling back to a default, which would answer a different question than the
// one asked.
func TestSearchTool_RejectsAnUnknownExtraSourcesMode(t *testing.T) {
	cfg := confirmConfig(t)
	session := newConfirmSession(t, cfg, staticMirrors{"http://127.0.0.1:0"}, nil)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "x", "extra_sources": "sometimes"},
	})
	if err == nil && (res == nil || !res.IsError) {
		t.Fatalf("an unknown extra_sources mode must be rejected, got res=%+v err=%v", res, err)
	}
}

// TestDownloadToolAsksForTheEmailItLacks is the guard for the failure that made
// the SDK upgrade dangerous. Every helper here collapses "could not ask" into
// its fall-back answer, so a server that has lost the ability to ask the user
// anything still builds, still passes its tests, and still downloads — it just
// silently stops asking, and nobody finds out until a user notices a prompt that
// never appears. This drives the REAL download tool and asserts the question
// reaches the client: a DOI download against a server with no contact email
// configured must come back asking for one.
func TestDownloadToolAsksForTheEmailItLacks(t *testing.T) {
	cfg := &config.Config{
		DownloadDir: t.TempDir(), Timeout: 5 * time.Second,
		RateRPS: 1000, RateBurst: 100, RetryAttempts: 1,
		// No contact email, and no save confirmation: the email is then the only
		// question this call has to ask, and nothing touches the network before it.
		UnpaywallEmail: "", ConfirmDownloads: false,
		// unpaywall is the only enabled source, and without a contact email it is
		// not in the chain at all — so a server that has stopped asking fails here
		// offline and fast, instead of quietly downloading the article from
		// somewhere else and leaving the lost prompt invisible.
		Sources: []string{"unpaywall"},
	}
	client := libgen.New(staticMirrors{"http://127.0.0.1:0"}, cfg)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	Register(server, client, cfg)

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	// The client advertises elicitation (so the server may ask) but answers no
	// round trip automatically, so the question itself is observable.
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, &mcp.ClientOptions{
		ElicitationHandler: acceptHandler(map[string]any{"email": "reader@example.com"}),
		MultiRoundTrip:     &mcp.MultiRoundTripOptions{Disabled: true},
	})
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"doi": "10.1371/journal.pmed.0020124"},
	})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if !res.NeedsInput() {
		t.Fatalf("download must ask for the contact email it has none of; got %+v", res)
	}
	if _, ok := res.InputRequests["unpaywall_email"]; !ok {
		t.Errorf("the question is not the contact email: %+v", res.InputRequests)
	}

	// The answered call is deliberately not driven here: fulfilling it would run
	// a real DOI download, and this suite stays offline. That the answer reaches
	// the handler is covered by TestInputRound_AsksThroughTheResult, and end to
	// end by the gated e2e and eval suites.
}

// TestNoParamHeaderAnnotations pins a deliberate omission: no tool argument
// carries an SEP-2243 `x-mcp-header` annotation.
//
// It is tempting to put one on md5 — it addresses exactly one file, so it looks
// like the routable key of this surface. Measured against the real transport,
// the annotation is a mirroring *contract*, not a hint: on a protocol-2026-07-28
// tools/call the server answers -32020 when the argument arrives without its
// Mcp-Param-* header. Two consequences sink it here. A browser-based client
// cannot send the header at all — dynamically named headers cannot be
// allow-listed for credentialed CORS, which every MCP SDK documents as a known
// limitation — so download and get_details by md5 become uncallable from one.
// And a client only learns the annotation from tools/list, so one calling
// straight from a persisted catalog is rejected too.
//
// That buys nothing: no gateway fronts this server. See
// docs/decisions/2026-07-31-no-param-header-routing.md.
func TestNoParamHeaderAnnotations(t *testing.T) {
	session := newSession(t)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			data, mErr := json.Marshal(tool.InputSchema)
			if mErr != nil {
				t.Fatal(mErr)
			}
			if strings.Contains(string(data), "x-mcp-header") {
				t.Errorf("%s carries an x-mcp-header annotation; see this test's doc comment for why it must not", tool.Name)
			}
		})
	}
}

// requiredGroupKeys flattens a list of single-key required branches into the
// keys they name, failing on any branch that is not exactly that shape — the
// shape is the contract, since a multi-key branch would mean AND, not OR. Each
// branch must also constrain its key's value (pattern or minLength): required
// alone counts key presence, so a blank identifier would satisfy a branch its
// handler ignores, and schema and validator would disagree on inputs like
// {"md5":"","doi":"…"}.
func requiredGroupKeys(t *testing.T, branches []*jsonschema.Schema) []string {
	t.Helper()
	keys := make([]string, 0, len(branches))
	for i, b := range branches {
		if b == nil || len(b.Required) != 1 {
			t.Fatalf("branch %d = %+v, want exactly one required key", i, b)
		}
		key := b.Required[0]
		prop := b.Properties[key]
		if prop == nil || (prop.Pattern == "" && prop.MinLength == nil) {
			t.Errorf("branch %d (%s) does not constrain the value; a blank %s would satisfy it while the handler ignores blanks", i, key, key)
		}
		keys = append(keys, key)
	}
	return keys
}

// TestIdentifierGroupsMatchTheirValidators pins each input schema's
// required-group to the rule its handler enforces, so the two cannot drift:
// download's handler accepts any non-empty combination of md5/isbn/doi
// (anyOf), get_details' handler demands exactly one of md5/id/doi (oneOf), and
// read's accepts md5/doi/path locally while a remote server rejects any call
// that sets path — so remotely the property itself is gone, not merely
// unlisted.
func TestIdentifierGroupsMatchTheirValidators(t *testing.T) {
	t.Run("download states at least one of md5, isbn, doi", func(t *testing.T) {
		schema := downloadInputSchema([]string{"libgen"})
		if schema == nil {
			t.Fatal("downloadInputSchema() = nil")
		}
		if got, want := requiredGroupKeys(t, schema.AnyOf), []string{"md5", "isbn", "doi"}; !slices.Equal(got, want) {
			t.Errorf("anyOf keys = %v, want %v", got, want)
		}
		if schema.OneOf != nil {
			t.Errorf("oneOf = %v, want none: the handler accepts several identifiers at once", schema.OneOf)
		}
	})

	t.Run("get_details states exactly one of md5, id, doi", func(t *testing.T) {
		schema := detailsInputSchema()
		if schema == nil {
			t.Fatal("detailsInputSchema() = nil")
		}
		if got, want := requiredGroupKeys(t, schema.OneOf), []string{"md5", "id", "doi"}; !slices.Equal(got, want) {
			t.Errorf("oneOf keys = %v, want %v", got, want)
		}
		if schema.AnyOf != nil {
			t.Errorf("anyOf = %v, want none: the handler refuses more than one identifier", schema.AnyOf)
		}
	})

	t.Run("read local offers path, remote removes it", func(t *testing.T) {
		local := readInputSchema([]string{"libgen"}, false)
		if local == nil {
			t.Fatal("readInputSchema(local) = nil")
		}
		if got, want := requiredGroupKeys(t, local.AnyOf), []string{"md5", "doi", "path"}; !slices.Equal(got, want) {
			t.Errorf("local anyOf keys = %v, want %v", got, want)
		}
		if local.Properties["path"] == nil {
			t.Error("local schema lost the path property")
		}

		remote := readInputSchema([]string{"libgen"}, true)
		if got, want := requiredGroupKeys(t, remote.AnyOf), []string{"md5", "doi"}; !slices.Equal(got, want) {
			t.Errorf("remote anyOf keys = %v, want %v", got, want)
		}
		if remote.Properties["path"] != nil {
			t.Error("remote schema still offers path, which validateReadInput rejects whenever it is set")
		}
	})
}

// TestDetailsObjectEnumMatchesItsValidator asserts get_details advertises the
// object values its handler actually accepts.
//
// object names a closed set — edition or file — that used to live only in the
// tool's prose, leaving a model free to invent a third value and learn it was
// wrong from an error. Pinning the enum only helps if the schema and the switch
// in detailsByID stay the same list, which is what this checks from both ends.
func TestDetailsObjectEnumMatchesItsValidator(t *testing.T) {
	schema := detailsInputSchema()
	if schema == nil {
		t.Fatal("detailsInputSchema() = nil")
	}
	object := schema.Properties["object"]
	if object == nil {
		t.Fatal("schema has no object property")
	}
	got := make([]string, len(object.Enum))
	for i, v := range object.Enum {
		got[i], _ = v.(string)
	}
	if want := detailsObjectNames(); !slices.Equal(got, want) {
		t.Errorf("object enum = %v, want %v", got, want)
	}

	// The enum is only worth pinning if it matches what the handler will
	// actually accept, so drive the handler with every advertised value and
	// with one it does not advertise. The context is already canceled, so a
	// value that clears the switch fails on the lookup instead — which is
	// the point: the only error this asserts on is the validation one.
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	client := libgen.New(staticMirrors{}, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, name := range object.Enum {
		value, _ := name.(string)
		if _, err := detailsByID(ctx, client, value, "1"); err != nil &&
			strings.Contains(err.Error(), "object must be") {
			t.Errorf("handler rejects advertised object %q: %v", value, err)
		}
	}
	// Omitting the argument must keep working: the schema leaves it optional
	// and the handler reads an absent value as the default.
	if _, err := detailsByID(ctx, client, "", "1"); err != nil &&
		strings.Contains(err.Error(), "object must be") {
		t.Errorf("handler rejects an omitted object: %v", err)
	}
	if _, err := detailsByID(ctx, client, "chapter", "1"); err == nil ||
		!strings.Contains(err.Error(), "object must be") {
		t.Errorf("handler accepted an object the schema does not advertise: err = %v", err)
	}
}

// TestReadOnlyToolsDeclareIdempotent asserts every read-only tool also declares
// idempotentHint over a real listing: a pure read re-run with the same
// arguments has no additional effect, and a client may gate retries on either
// hint independently, so declaring one without the other made the surface's
// only writing tool look safer to repeat than its reads.
func TestReadOnlyToolsDeclareIdempotent(t *testing.T) {
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	client := libgen.New(staticMirrors{}, cfg)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	Register(server, client, cfg)

	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	seen := 0
	for tool, err := range session.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		seen++
		if tool.Annotations == nil {
			t.Errorf("%s: no annotations", tool.Name)
			continue
		}
		if !tool.Annotations.IdempotentHint {
			t.Errorf("%s: idempotentHint = false, want true", tool.Name)
		}
	}
	if seen != 4 {
		t.Errorf("listed %d tools, want 4", seen)
	}
}

// TestDetailsHandlerEnforcesExactlyOneIdentifier covers the runtime half of the
// rule get_details' schema declares.
//
// The oneOf pinned in TestIdentifierGroupsMatchTheirValidators states the rule
// to a client; this asserts the handler actually imposes it, which is what a
// client that ignores the schema — or sends whitespace the schema counts as
// present — runs into. Declaring a constraint and enforcing it are two
// different things, and only one of them was being tested.
func TestDetailsHandlerEnforcesExactlyOneIdentifier(t *testing.T) {
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	handler := detailsHandler(emptyJSONClient(t), cfg, nil)

	cases := []struct {
		name string
		in   DetailsInput
	}{
		{name: "no identifier at all", in: DetailsInput{}},
		// Whitespace is absence to both sides: countKeys trims, and the
		// schema's branches demand a non-blank value.
		{name: "only whitespace", in: DetailsInput{MD5: "   "}},
		{name: "whitespace in every field", in: DetailsInput{MD5: " ", ID: "\t", DOI: "  "}},
		{name: "md5 and doi", in: DetailsInput{MD5: "00dd2b0b58e81e3c6e7cb9e7b72dee23", DOI: "10.1/x"}},
		{name: "md5 and id", in: DetailsInput{MD5: "00dd2b0b58e81e3c6e7cb9e7b72dee23", ID: "42"}},
		{name: "id and doi", in: DetailsInput{ID: "42", DOI: "10.1/x"}},
		{name: "all three", in: DetailsInput{MD5: "00dd2b0b58e81e3c6e7cb9e7b72dee23", ID: "42", DOI: "10.1/x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := handler(t.Context(), nil, tc.in)
			if err == nil {
				t.Fatalf("handler accepted %+v; the schema says exactly one", tc.in)
			}
			if !strings.Contains(err.Error(), "exactly one of md5, id or doi") {
				t.Errorf("error = %q, want it to state the rule", err)
			}
		})
	}
}

// TestDetailsHandlerAgreesWithItsSchemaOnBlanks pins the input where the two
// used to disagree, in the direction that matters.
//
// The schema's oneOf demands a non-blank value per branch, so
// {md5:"   ", doi:"10.…"} matches exactly the doi branch and validates. The
// handler used to reject it: countKeys trimmed and counted one identifier, then
// the md5 arm compared against "" and took it anyway, failing on the format of
// a value the caller never meant to send. A schema that accepts what the handler
// refuses is the same defect as prose that promises what the code does not.
func TestDetailsHandlerAgreesWithItsSchemaOnBlanks(t *testing.T) {
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	handler := detailsHandler(emptyJSONClient(t), cfg, nil)

	_, _, err := handler(t.Context(), nil, DetailsInput{MD5: "   ", DOI: "10.1371/journal.pone.0173664"})
	if err != nil && strings.Contains(err.Error(), "md5 must be") {
		t.Fatalf("a blank md5 beside a usable doi was judged as an md5: %v", err)
	}
	// Reaching the DOI path is the assertion; whether the lookup succeeds
	// against a stub client is not what this test is about.
	if err != nil && strings.Contains(err.Error(), "exactly one") {
		t.Errorf("a blank md5 counted as an identifier: %v", err)
	}
}
