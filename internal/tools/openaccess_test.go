package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/discovery"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
)

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
// with the given open-access deployment default, so the open-access tests can
// drive the real search handler end to end.
func oaSession(t *testing.T, openAccessDefault bool) *mcp.ClientSession {
	t.Helper()
	searchHTML := mustReadFile(t, "../libgen/testdata/search_books.html")
	mux := http.NewServeMux()
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(searchHTML) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := &config.Config{
		DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100,
		RetryAttempts: 1, UnpaywallEmail: "test@example.com", OpenAccessEnabled: openAccessDefault,
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
// include_open_access=true the search output carries origin-labeled OA hits and the
// discovery servers were called; with the flag off (and the deployment default
// off) the OA slice is empty and NO discovery request is made.
func TestSearchTool_OpenAccessOptIn(t *testing.T) {
	hits := oaDiscoveryServers(t)
	session := oaSession(t, false)

	oa := oaSearchOutput(t, session, map[string]any{"query": "golang", "include_open_access": true})
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
	off := oaSearchOutput(t, session, map[string]any{"query": "golang", "include_open_access": false})
	if len(off) != 0 {
		t.Errorf("open_access should be empty when opted out, got %d", len(off))
	}
	if got := atomic.LoadInt32(hits); got != 0 {
		t.Errorf("discovery was called %d times when opted out, want 0", got)
	}
}

// TestSearchTool_OpenAccessDefaultOff verifies that with neither a per-call flag
// nor the deployment default set, OA discovery stays off and unqueried.
func TestSearchTool_OpenAccessDefaultOff(t *testing.T) {
	hits := oaDiscoveryServers(t)
	session := oaSession(t, false)
	oa := oaSearchOutput(t, session, map[string]any{"query": "golang"})
	if len(oa) != 0 {
		t.Errorf("open_access should be empty by default, got %d", len(oa))
	}
	if got := atomic.LoadInt32(hits); got != 0 {
		t.Errorf("discovery was called %d times by default, want 0", got)
	}
}

// TestSearchTool_OpenAccessDedupVsLibgen verifies that an OA hit whose DOI or
// title+year matches a libgen result is dropped from the OA slice, so the same work
// is never presented twice.
func TestSearchTool_OpenAccessDedupVsLibgen(t *testing.T) {
	oaDiscoveryServers(t)
	// A libgen result sharing the arXiv fixture's DOI must suppress that OA hit.
	out := SearchOutput{Results: []libgen.Result{
		{Title: "Some Book", DOI: "10.1000/XYZ123", Year: "2021"},
	}}
	cfg := &config.Config{UnpaywallEmail: "test@example.com", OpenAccessEnabled: true}
	force := true
	appendOpenAccess(context.Background(), cfg, SearchInput{Query: "golang", IncludeOpenAccess: &force}, &out)
	for _, r := range out.OpenAccess {
		if discovery.NormalizeDOI(r.DOI) == "10.1000/xyz123" {
			t.Errorf("OA hit sharing a libgen DOI should have been deduped, got %+v", r)
		}
	}
	// The distinct Crossref hit must survive the libgen dedup.
	var keptCrossref bool
	for _, r := range out.OpenAccess {
		if r.Origin == "crossref" {
			keptCrossref = true
		}
	}
	if !keptCrossref {
		t.Errorf("distinct crossref OA hit should survive libgen dedup, got %+v", out.OpenAccess)
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
