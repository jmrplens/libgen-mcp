//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/tools"
)

// allTopics lists every collection the search tool exposes, paired with a
// plausible query for that collection. Structure, not exact hits, is asserted.
var allTopics = []struct {
	topic string
	query string
}{
	{"nonfiction", "linux"},
	{"fiction", "tolkien"},
	{"articles", "cancer"},
	{"magazines", "national geographic"},
	{"comics", "batman"},
	{"standards", "iso"},
	{"fiction_rus", "пушкин"},
}

// TestE2ESearchAllTopics searches each of the seven collections against the live
// site and asserts structural invariants: a non-empty result set (or a non-empty
// total-files counter), and for each result a title, a canonical md5 when present,
// and at least one download option. It paces itself between topics.
func TestE2ESearchAllTopics(t *testing.T) {
	env := requireLive(t)
	for _, tc := range allTopics {
		t.Run(tc.topic, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			page, mirror, err := env.client.Search(ctx, libgen.SearchParams{
				Query:  tc.query,
				Topics: []string{tc.topic},
			})
			if err != nil {
				t.Fatalf("Search(%s) error: %v", tc.topic, err)
			}
			t.Logf("topic=%s mirror=%s results=%d total=%q", tc.topic, mirror, len(page.Results), page.TotalFiles)
			if len(page.Results) == 0 && (page.TotalFiles == "" || page.TotalFiles == "0") {
				t.Fatalf("topic %s: no results and empty total_files (layout changed or blocked)", tc.topic)
			}
			for i := range page.Results {
				assertResultStructure(t, page.Results[i])
			}
		})
		pace()
	}
}

// TestE2EGetDetails takes an md5 from a live nonfiction search and looks it up via
// the json.php details API, asserting a non-empty file record.
func TestE2EGetDetails(t *testing.T) {
	env := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	md5 := firstMD5(t, ctx, env.client, "linux")
	pace()

	file, _, err := env.client.DetailsByMD5(ctx, md5)
	if err != nil {
		t.Fatalf("DetailsByMD5(%s) error: %v", md5, err)
	}
	if len(file) == 0 {
		t.Fatalf("DetailsByMD5(%s) returned an empty file record", md5)
	}
	if !hasNonEmptyField(file) {
		t.Errorf("file record has no non-empty fields: %+v", file)
	}
	t.Logf("details md5=%s fields=%d", md5, len(file))
}

// TestE2EDownloadSmall downloads a genuinely small nonfiction file (found by
// ordering results by ascending size and filtering by a polite cap) into a temp
// dir, then asserts the download was integrity-verified and that the file on disk
// hashes to the requested md5.
//
// Small-target choice: rather than hardcode an md5 that may vanish, the test
// searches with order=size, order_mode=asc and picks the first result whose
// parsed size is non-zero and within maxE2EDownloadBytes. A per-client download
// cap enforces the same ceiling defensively. If the download cannot complete
// (expired key, blocked CDN), it falls back to proving the
// ads.php -> get.php -> CDN chain resolves and that the first bytes are a valid,
// non-HTML file, without pulling the whole payload.
func TestE2EDownloadSmall(t *testing.T) {
	requireLive(t)
	cfg := loadLiveConfig(t)
	cfg.MaxDownloadBytes = maxE2EDownloadBytes
	client := buildClient(t, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	page, _, err := client.Search(ctx, libgen.SearchParams{
		Query: "python", Topics: []string{"nonfiction"}, Order: "size", OrderMode: "asc",
	})
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	target := smallestDownloadable(page.Results)
	if target.MD5 == "" {
		t.Skip("no small downloadable target found; skipping to stay polite")
	}
	t.Logf("download target md5=%s size=%q title=%q", target.MD5, target.Size, target.Title)
	pace()

	res, err := client.Download(ctx, target.MD5, t.TempDir(), "", nil)
	if err != nil {
		proveChainResolves(t, ctx, client, target.MD5)
		return
	}
	if !res.Verified {
		t.Errorf("download not integrity-verified: %+v", res)
	}
	assertFileMD5(t, res.Path, target.MD5)
	t.Logf("downloaded md5=%s bytes=%d source=%s path=%s", target.MD5, res.SizeBytes, res.Source, res.Path)
}

// TestE2EArticleByDOI resolves a known open-access DOI through the
// unpaywall -> sci-hub chain and asserts a PDF is fetched. It skips gracefully
// when the chain cannot serve the article, since OA availability varies.
func TestE2EArticleByDOI(t *testing.T) {
	requireLive(t)
	cfg := loadLiveConfig(t)
	cfg.MaxDownloadBytes = maxE2EDownloadBytes
	client := buildClient(t, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// PLOS Medicine, reliably open access (Ioannidis 2005), so Unpaywall exposes a
	// PDF link for it.
	const oaDOI = "10.1371/journal.pmed.0020124"
	res, err := client.DownloadItem(ctx, libgen.Item{DOI: oaDOI}, t.TempDir(), "")
	if err != nil {
		t.Skipf("OA article download unavailable via unpaywall/sci-hub: %v", err)
	}
	info, statErr := os.Stat(res.Path)
	if statErr != nil {
		t.Fatalf("article file missing: %v", statErr)
	}
	if info.Size() == 0 {
		t.Fatalf("article file is empty: %s", res.Path)
	}
	assertPDF(t, res.Path)
	t.Logf("article doi=%s source=%s bytes=%d path=%s", oaDOI, res.Source, res.SizeBytes, res.Path)
}

// TestE2EMCPSearchTool drives the in-memory MCP server's `search` tool against the
// real site, exercising the full request wiring (tool schema, handler, client)
// end to end.
func TestE2EMCPSearchTool(t *testing.T) {
	env := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	server := mcp.NewServer(&mcp.Implementation{Name: "libgen-mcp-e2e", Version: "test"}, nil)
	tools.Register(server, env.client, env.cfg)

	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "e2e-client", Version: "test"}, nil)
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "linux", "topics": []string{"nonfiction"}},
	})
	if err != nil {
		t.Fatalf("CallTool(search) error: %v", err)
	}
	if res.IsError {
		t.Fatalf("search tool returned an error result: %+v", res.Content)
	}
	if len(res.Content) == 0 {
		t.Fatal("search tool returned no content")
	}

	// Both channels of the discoverability contract, against real data: a
	// human-readable Markdown block with a results table and download links, plus
	// structured output leading with a next_steps guidance list.
	md := textOf(res)
	if !strings.Contains(md, "| # | Title") {
		t.Errorf("search markdown should contain a results table header; got:\n%s", md)
	}
	if !strings.Contains(md, "](http") {
		t.Errorf("search markdown should include clickable download links; got:\n%s", md)
	}
	var out tools.SearchOutput
	decodeStructured(t, res, &out)
	if len(out.NextSteps) == 0 {
		t.Error("search structured output should carry next_steps")
	}
	if len(out.Results) > 0 && !hasDownloadLink(out.Results) {
		t.Error("search results should expose at least one download link")
	}
	t.Logf("mcp search tool: %d results, %d next_steps, markdown %d bytes", len(out.Results), len(out.NextSteps), len(md))
}

// TestE2EGetDetailsByID looks a record up by its edition id (taken from a live
// search result), exercising the id/object path of get_details against the real
// json.php API and asserting an edition record comes back.
func TestE2EGetDetailsByID(t *testing.T) {
	env := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	page, _, err := env.client.Search(ctx, libgen.SearchParams{Query: "linux", Topics: []string{"nonfiction"}})
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	var editionID string
	for i := range page.Results {
		if id := strings.TrimSpace(page.Results[i].EditionID); id != "" {
			editionID = id
			break
		}
	}
	if editionID == "" {
		t.Skip("no result carried an edition_id; cannot exercise the id path")
	}
	pace()

	rec, err := env.client.DetailsByID(ctx, "e", editionID)
	if err != nil {
		t.Fatalf("DetailsByID(e, %s) error: %v", editionID, err)
	}
	if len(rec) == 0 || !hasNonEmptyField(rec) {
		t.Errorf("edition record %s is empty: %+v", editionID, rec)
	}
	t.Logf("details by id=%s fields=%d", editionID, len(rec))
}

// textOf concatenates the text of a tool result's TextContent blocks.
func textOf(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// decodeStructured re-marshals a tool result's structured content into target.
func decodeStructured(t *testing.T, res *mcp.CallToolResult, target any) {
	t.Helper()
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if uerr := json.Unmarshal(data, target); uerr != nil {
		t.Fatalf("decode structured content: %v", uerr)
	}
}

// hasDownloadLink reports whether any result exposes a download URL.
func hasDownloadLink(results []libgen.Result) bool {
	for i := range results {
		for _, d := range results[i].Downloads {
			if strings.TrimSpace(d.URL) != "" {
				return true
			}
		}
	}
	return false
}
