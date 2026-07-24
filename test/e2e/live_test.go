//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"errors"
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

	// This test covers the CONFIGURED-email Unpaywall path: loadLiveConfig always
	// supplies a contact email, so Unpaywall is in the chain for the DOI below.
	if strings.TrimSpace(cfg.UnpaywallEmail) == "" {
		t.Fatal("expected a configured Unpaywall email; loadLiveConfig should always supply one")
	}
	t.Logf("configured-email Unpaywall path in effect (email set: %v)", cfg.UnpaywallEmail != "")

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

// randombookProbeQueries are search queries likely to surface distinct real
// books, tried in order until one yields an md5 to probe randombook with —
// making the test robust to randombook.org's per-book coverage gaps rather
// than to any code defect.
var randombookProbeQueries = []string{"python", "history", "science", "chemistry", "physics"}

// syntheticMD5NeverIndexed is a well-formed but unallocated md5 (all zeros) that
// no real book can carry, guaranteeing a deterministic "not indexed" miss from
// the live randombook.org API — unlike the mirror-resolution outcome, which
// depends on the live mirror ecosystem's current state and cannot be forced on
// demand, the not-indexed miss is reliably reproducible against the real API on
// every run.
const syntheticMD5NeverIndexed = "00000000000000000000000000000000"

// TestE2ERandombookNotIndexedIsClean verifies, against the live randombook.org
// API, that an md5 it cannot possibly have indexed yields a clean "not indexed"
// error — not a transport error, not ErrLayoutChanged, and not a hang — so the
// by-id lookup's normal-miss path is deterministically exercised on every run,
// independent of the live mirror ecosystem's current state.
func TestE2ERandombookNotIndexedIsClean(t *testing.T) {
	env := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := env.client.DownloadItem(ctx, libgen.Item{MD5: syntheticMD5NeverIndexed, Source: "randombook"}, t.TempDir(), "")
	if err == nil {
		t.Fatal("DownloadItem for a synthetic, never-allocated md5 unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "not indexed") {
		t.Fatalf("want a clean \"not indexed\" miss for an unallocated md5, got: %v", err)
	}
}

// TestE2ERandombookClassifiedOutcome exercises the randombook download source
// end to end against the live randombook.org API and whatever mirrors it
// currently discovers, restricting the download to source=randombook so no
// other source in the chain can mask its behavior.
//
// This is the test that would have caught the bug found in this package on
// 2026-07-23: randombook.org was observed returning mirror hostnames
// resolveViaMirror cannot use (three libgen.<tld> hosts migrated to a
// client-rendered SPA frontend, plus an unrelated annas-archive.gl host using
// a different URL scheme entirely) — and the code surfaced that as a bare,
// unclassified "HTTP 404" indistinguishable from ordinary live flakiness.
// Nothing in the e2e suite caught it; it was found only by chance while
// reading an unrelated LLM-eval transcript.
//
// The test therefore does not merely tolerate a download failure: on error, it
// requires the failure to be one of the KNOWN, diagnosed classes below. An
// error outside that set fails the test, so a new, unrecognized failure mode
// is caught here instead of discovered by chance later.
func TestE2ERandombookClassifiedOutcome(t *testing.T) {
	env := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	md5 := firstRandombookProbeMD5(t, ctx, env)
	res, err := env.client.DownloadItem(ctx, libgen.Item{MD5: md5, Source: "randombook"}, t.TempDir(), "")
	if err == nil {
		assertRandombookDownloadOK(t, md5, res)
		return
	}
	skipRandombookDiagnosedError(t, md5, err)
}

// firstRandombookProbeMD5 searches randombookProbeQueries in order and returns
// the first valid md5 found, so the caller has a real book to probe randombook
// with. It skips (never fails) on a live search error or if no query yields any
// md5-carrying result, since that reflects live-data/site conditions, not the
// randombook code under test.
func firstRandombookProbeMD5(t *testing.T, ctx context.Context, env *liveEnv) string {
	t.Helper()
	for _, q := range randombookProbeQueries {
		page, _, err := env.client.Search(ctx, libgen.SearchParams{Query: q, Topics: []string{"nonfiction"}})
		if err != nil {
			t.Skipf("search(%q) failed live: %v", q, err)
		}
		for i := range page.Results {
			if md5Re.MatchString(page.Results[i].MD5) {
				return page.Results[i].MD5
			}
		}
		pace()
	}
	t.Skip("could not find a book with a valid md5 to probe randombook with")
	return ""
}

// assertRandombookDownloadOK verifies the best-case outcome: a genuine
// libgen-family mirror was currently available and served the classic ads.php
// flow, so the file must be a real, MD5-verified randombook download.
func assertRandombookDownloadOK(t *testing.T, md5 string, res *libgen.DownloadResult) {
	t.Helper()
	if res.Path == "" {
		t.Fatal("DownloadItem succeeded but returned no path")
	}
	if !res.Verified {
		t.Error("randombook download succeeded but was not MD5-verified")
	}
	if res.Source != "randombook" {
		t.Errorf("Source = %q, want %q", res.Source, "randombook")
	}
	t.Logf("randombook served a real download: md5=%s bytes=%d", md5, res.SizeBytes)
}

// skipRandombookDiagnosedError classifies a randombook download failure into
// one of the known, diagnosed outcome classes and SKIPs with a clear reason.
// An error outside that set is NOT a recognized live-data condition: it FAILS
// the test, so a new, unclassified failure mode is caught here rather than
// discovered by chance later (see the package doc comment above this test).
func skipRandombookDiagnosedError(t *testing.T, md5 string, err error) {
	t.Helper()
	switch {
	case strings.Contains(err.Error(), "not indexed"):
		// A normal, expected miss for this particular book: randombook's
		// catalog does not cover everything.
		t.Skipf("md5 %s not indexed by randombook.org (normal per-book miss): %v", md5, err)
	case strings.Contains(err.Error(), "no usable mirrors discovered"):
		// Every discovered candidate was outside the libgen.<tld> family (see
		// filterLibgenFamily in internal/libgen), so nothing was even
		// attempted — a diagnosed, expected outcome given randombook.org's
		// current candidate mix.
		t.Skipf("randombook discovered no libgen-family mirror candidates for md5 %s: %v", md5, err)
	case errors.Is(err, libgen.ErrMirrorClientRendered):
		// A libgen-family host answered, but with its client-rendered SPA
		// shell instead of the classic ads.php page — diagnosed and
		// monitorable (see ErrMirrorClientRendered's doc comment).
		t.Skipf("randombook's only libgen-family mirror candidate has migrated to a client-rendered frontend: %v", err)
	case strings.Contains(err.Error(), "requesting") || strings.Contains(err.Error(), "returned HTTP"):
		// A transport-level failure reaching randombook.org itself or a
		// discovered mirror (network flakiness), consistent with the suite's
		// SKIP-not-fail philosophy.
		t.Skipf("randombook.org API or a discovered mirror was unreachable live: %v", err)
	default:
		// Anything else is an UNRECOGNIZED failure class: fail loudly rather
		// than silently tolerating it, so a future regression of this kind is
		// caught here instead of by chance in an unrelated eval run.
		t.Fatalf("randombook download failed with an unclassified error (update this test's classification if this is a legitimate new outcome): %v", err)
	}
}

// scidbLiveDOI is a long-established, heavily-mirrored DOI verified served by
// SciDB on 2026-07-23, which keeps this live check deterministic.
const scidbLiveDOI = "10.1016/j.cell.2011.02.013"

// TestE2ESciDBClassifiedOutcome exercises the scidb source end to end against the
// live Anna's Archive mirrors, restricting the download to source=scidb so no
// other source in the chain can mask its behavior. On error the failure must be
// one of the known, diagnosed classes; anything else fails the test, so a new
// unrecognized failure mode surfaces here instead of hiding as flakiness.
func TestE2ESciDBClassifiedOutcome(t *testing.T) {
	env := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	res, err := env.client.DownloadItem(ctx, libgen.Item{DOI: scidbLiveDOI, Source: "scidb"}, t.TempDir(), "")
	if err == nil {
		if res.SizeBytes <= 0 {
			t.Fatalf("scidb reported a download of %d bytes", res.SizeBytes)
		}
		t.Logf("scidb served a real download: bytes=%d", res.SizeBytes)
		return
	}
	known := []string{
		"embedded no PDF",      // mirror reachable, article absent from SciDB
		"no mirror resolved",   // every mirror down or serving no PDF
		"no mirrors available", // discovery yielded nothing
		"context deadline",     // a slow mirror inside the timeout budget
	}
	for _, k := range known {
		if strings.Contains(err.Error(), k) {
			t.Skipf("scidb unavailable in a known way: %v", err)
		}
	}
	t.Fatalf("scidb failed in an undiagnosed way: %v", err)
}

// TestE2EAnnasClassifiedOutcome exercises the annas book source end to end,
// restricted to source=annas. It probes the keyless IPFS path unless
// LIBGEN_MCP_ANNAS_KEY is set in the environment, in which case the member
// fast-download API is attempted first with IPFS still the fallback.
func TestE2EAnnasClassifiedOutcome(t *testing.T) {
	env := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	md5 := firstRandombookProbeMD5(t, ctx, env)
	res, err := env.client.DownloadItem(ctx, libgen.Item{MD5: md5, Source: "annas"}, t.TempDir(), "")
	if err == nil {
		if res.SizeBytes <= 0 {
			t.Fatalf("annas reported a download of %d bytes", res.SizeBytes)
		}
		t.Logf("annas served a real download: md5=%s bytes=%d verified=%v", md5, res.SizeBytes, res.Verified)
		return
	}
	skipIfAnnasUnavailable(t, err)
	t.Fatalf("annas failed in an undiagnosed way: %v", err)
}

// escalationItem is the pinned catalog-miss / Anna's-hit fixture.
type escalationItem struct {
	Query string `json:"query"`
	MD5   string `json:"md5"`
	Title string `json:"title"`
	Note  string `json:"note"`
}

// loadEscalationItem reads the pinned fixture describing an item Anna's carries
// and the Library Genesis catalog does not.
func loadEscalationItem(t *testing.T) escalationItem {
	t.Helper()
	b, err := os.ReadFile("testdata/escalation_item.json")
	if err != nil {
		t.Fatalf("reading escalation fixture: %v", err)
	}
	var it escalationItem
	if uerr := json.Unmarshal(b, &it); uerr != nil {
		t.Fatalf("decoding escalation fixture: %v", uerr)
	}
	return it
}

// callSearch registers the tools on an in-process MCP server, calls search with
// the given arguments, and decodes the structured output — following the same
// pattern as newMCPDownloadEnv but for the search tool.
func callSearch(t *testing.T, ctx context.Context, env *liveEnv, args map[string]any) tools.SearchOutput {
	t.Helper()
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
	t.Cleanup(func() { _ = session.Close() })

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search", Arguments: args})
	if err != nil {
		t.Fatalf("search tool error: %v", err)
	}
	if res.IsError {
		t.Fatalf("search tool returned error: %v", res.Content)
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshaling structured content: %v", err)
	}
	var out tools.SearchOutput
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatalf("decoding search output: %v", uerr)
	}
	return out
}

// TestE2ESearchEscalatesOnCatalogMiss is the core proof: a query for an item the
// catalog does not carry must still return it, sourced from Anna's, without the
// caller asking for anything special.
func TestE2ESearchEscalatesOnCatalogMiss(t *testing.T) {
	env := requireLive(t)
	item := loadEscalationItem(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// auto is passed explicitly: the deployment default is configurable, so an
	// omitted argument could silently exercise always or never instead.
	out := callSearch(t, ctx, env, map[string]any{"query": item.Query, "extra_sources": "auto"})

	var fromAnnas int
	var foundPinned bool
	for _, r := range out.Results {
		if r.Origin == "annas" {
			fromAnnas++
		}
		if strings.EqualFold(r.MD5, item.MD5) {
			foundPinned = true
		}
	}
	if fromAnnas == 0 {
		t.Fatalf("no Anna's-origin results for a query the catalog misses; escalation did not happen (results=%d)", len(out.Results))
	}
	// The query is the item's own title, so Anna's ranking it off the page means
	// the fixture no longer describes reality and must be re-pinned.
	if !foundPinned {
		t.Fatalf("pinned md5 %s absent from %d Anna's results for its own title; re-pin the fixture", item.MD5, fromAnnas)
	}
}

// TestE2ESearchDoesNotEscalateOnCatalogHit verifies the common path stays cheap: a
// query the catalog answers must not pull in extra sources, so ordinary searches
// neither slow down nor add third-party traffic.
func TestE2ESearchDoesNotEscalateOnCatalogHit(t *testing.T) {
	env := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	out := callSearch(t, ctx, env, map[string]any{"query": "python", "extra_sources": "auto"})
	if len(out.Results) == 0 {
		t.Skip("the catalog returned nothing for a broad query today; cannot assert the no-escalation path")
	}
	for _, r := range out.Results {
		if r.Origin != "" && r.Origin != "libgen" {
			t.Fatalf("catalog hit still escalated: found a %q result", r.Origin)
		}
	}
}

// TestE2ESearchAlwaysMode verifies extra_sources=always consults the extra searchers
// even when the catalog already answered.
func TestE2ESearchAlwaysMode(t *testing.T) {
	env := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	out := callSearch(t, ctx, env, map[string]any{"query": "python", "extra_sources": "always"})
	if len(out.Results) == 0 && len(out.OpenAccess) == 0 {
		t.Skip("no source answered for this query today")
	}
	var extra int
	for _, r := range out.Results {
		if r.Origin != "" && r.Origin != "libgen" {
			extra++
		}
	}
	if extra == 0 && len(out.OpenAccess) == 0 {
		t.Fatal("extra_sources=always produced no extra-origin results at all")
	}
}

// TestE2ESearchNeverMode verifies extra_sources=never is honored even when the catalog
// returns nothing, so a caller or deployment can demand catalog-only behavior.
func TestE2ESearchNeverMode(t *testing.T) {
	env := requireLive(t)
	item := loadEscalationItem(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	out := callSearch(t, ctx, env, map[string]any{"query": item.Query, "extra_sources": "never"})
	for _, r := range out.Results {
		if r.Origin != "" && r.Origin != "libgen" {
			t.Fatalf("extra_sources=never still returned a %q result", r.Origin)
		}
	}
}

// TestE2ENeverIsALockNotADefault verifies a deployment configured to never cannot
// be talked out of it. The setting exists so an operator can guarantee the server
// contacts no extra provider; a caller able to ask for them anyway would make that
// guarantee worthless. A live evaluator run caught exactly that — a model retried
// with always after an empty catalog search and reached Anna's.
func TestE2ENeverIsALockNotADefault(t *testing.T) {
	env := requireLive(t)
	item := loadEscalationItem(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	locked := *env.cfg
	locked.ExtraSources = config.ExtraSourcesNever
	lockedEnv := &liveEnv{cfg: &locked, client: env.client}

	// The query is a known catalog miss and the call explicitly asks for the extras,
	// so anything other than an empty, catalog-only page means the lock leaked.
	out := callSearch(t, ctx, lockedEnv, map[string]any{"query": item.Query, "extra_sources": "always"})
	for _, r := range out.Results {
		if r.Origin != "" && r.Origin != "libgen" {
			t.Fatalf("a never deployment returned a %q result despite the lock", r.Origin)
		}
	}
	if len(out.OpenAccess) > 0 {
		t.Fatalf("a never deployment returned %d open-access hits despite the lock", len(out.OpenAccess))
	}
}

// TestE2EEscalatedDownloadKeepsItsFileType verifies an escalated download lands as
// a usable file. Anna's serves bytes over IPFS, which addresses content and
// announces no name, so the type has to come from the record: without it the file
// saves extensionless and every reader downstream is blind to what it is.
func TestE2EEscalatedDownloadKeepsItsFileType(t *testing.T) {
	env := requireLive(t)
	item := loadEscalationItem(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	res, err := env.client.DownloadItem(ctx, libgen.Item{MD5: item.MD5, Source: "annas"}, t.TempDir(), "")
	if err != nil {
		skipIfAnnasUnavailable(t, err)
		t.Fatalf("escalated item failed to download in an undiagnosed way: %v", err)
	}
	if ext := strings.ToLower(filepath.Ext(res.Path)); ext == "" {
		t.Fatalf("saved %q with no extension; read cannot choose an extractor for it", res.Path)
	}
	t.Logf("escalated item saved as %s (%d bytes)", filepath.Base(res.Path), res.SizeBytes)
}

// TestE2EReadEscalatedItem verifies the whole escalated chain ends somewhere
// useful: an item the catalog does not carry is found, fetched from Anna's and
// read. It is the strictest of these tests — a pass means search, the Anna's
// download path, the file type and text extraction all held together.
func TestE2EReadEscalatedItem(t *testing.T) {
	requireLive(t)
	item := loadEscalationItem(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	_, session := newReadSession(t, ctx)
	out := callRead(t, ctx, session, map[string]any{"md5": item.MD5})
	if !out.Extractable {
		t.Fatalf("escalated item was not extractable: %s", out.Reason)
	}
	if strings.TrimSpace(out.Text) == "" {
		t.Fatal("escalated item reported extractable but yielded no text")
	}
	t.Logf("read %d characters from an item the catalog does not carry", len(out.Text))
}

// TestE2EGetDetailsByDOI verifies a DOI resolves exactly, and to something
// downloadable. A live evaluator run showed a model reaching for get_details with
// a DOI, being rejected, and spending three more turns searching its way to the
// md5 the catalog could have handed it straight away.
func TestE2EGetDetailsByDOI(t *testing.T) {
	env := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const doi = "10.1016/j.cell.2011.02.013" // Hallmarks of Cancer: The Next Generation
	edition, file, err := env.client.DetailsByDOI(ctx, doi)
	if err != nil {
		t.Fatalf("DetailsByDOI(%s) error: %v", doi, err)
	}
	if got, _ := edition["doi"].(string); !strings.EqualFold(got, doi) {
		t.Errorf("edition.doi = %q, want %q — the lookup must be exact, not a text match", got, doi)
	}
	if file == nil {
		t.Fatal("no file beside the edition; a DOI lookup must yield an md5 to download")
	}
	if md5, _ := file["md5"].(string); !md5Re.MatchString(md5) {
		t.Errorf("file.md5 = %q, want a 32-hex digest", md5)
	}
	t.Logf("doi=%s edition=%v md5=%v", doi, edition["title"], file["md5"])
}

// TestE2EExtensionlessFileStillReads verifies content decides when the name does
// not. A file fetched by content address, or from a CDN that announces no
// filename, lands with no extension; dispatching on the name alone reported real
// books as unsupported, and a model handed that answered with an invented table
// of contents. The pinned escalation item comes over IPFS, which is exactly that
// case.
func TestE2EExtensionlessFileStillReads(t *testing.T) {
	requireLive(t)
	item := loadEscalationItem(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	_, session := newReadSession(t, ctx)
	out := callRead(t, ctx, session, map[string]any{"md5": item.MD5})
	if !out.Extractable {
		t.Fatalf("an IPFS-fetched PDF must still be recognized by content: %s", out.Reason)
	}
	if out.Format != "pdf" {
		t.Errorf("Format = %q, want pdf — the format was not recovered from the bytes", out.Format)
	}
}

// skipIfAnnasUnavailable skips on the known ways Anna's and the public IPFS
// gateways fail live, and returns otherwise so the caller can fail on anything
// undiagnosed rather than tolerating a new failure mode silently.
func skipIfAnnasUnavailable(t *testing.T, err error) {
	t.Helper()
	known := []string{
		"embedded no IPFS CID",   // item not pinned to IPFS
		"no IPFS gateway served", // every gateway down or lacking the block
		"no mirror resolved",     // every Anna's mirror down
		"no mirrors available",   // discovery yielded nothing
		"member API rejected",    // key absent or expired AND IPFS also failed
		"context deadline",       // IPFS retrieval is legitimately slow
	}
	for _, k := range known {
		if strings.Contains(err.Error(), k) {
			t.Skipf("annas unavailable in a known way: %v", err)
		}
	}
}

// TestE2EGetDetailsFallsBackToAnnas verifies the follow-up a search suggests works
// on an escalated result: the Library Genesis catalog has no record for the pinned
// md5, so get_details must answer from Anna's Archive instead of failing. It goes
// through the MCP tools layer, since that is the only path a real client takes.
func TestE2EGetDetailsFallsBackToAnnas(t *testing.T) {
	env := requireLive(t)
	item := loadEscalationItem(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	t.Cleanup(func() { _ = session.Close() })

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_details", Arguments: map[string]any{"md5": item.MD5},
	})
	if err != nil {
		t.Fatalf("get_details tool error: %v", err)
	}
	if res.IsError {
		t.Fatalf("get_details on an Anna's-only md5 returned an error: %v", res.Content)
	}
	data, merr := json.Marshal(res.StructuredContent)
	if merr != nil {
		t.Fatalf("marshaling structured content: %v", merr)
	}
	var out tools.DetailsOutput
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatalf("decoding details output: %v", uerr)
	}
	if got, _ := out.File["origin"].(string); got != "annas" {
		t.Fatalf("file.origin = %q, want annas — the catalog should not have answered", got)
	}
	if got, _ := out.File["title"].(string); got == "" {
		t.Fatal("the fallback record carries no title")
	}
	t.Logf("annas fallback record: title=%v collection=%v size=%v",
		out.File["title"], out.File["collection"], out.File["filesize"])
}

// TestE2ESearchEscalatedResultIsDownloadable closes the loop: an item found only via
// escalation must actually download through the annas source, proving search and
// download line up rather than each half working alone.
func TestE2ESearchEscalatedResultIsDownloadable(t *testing.T) {
	env := requireLive(t)
	item := loadEscalationItem(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	res, err := env.client.DownloadItem(ctx, libgen.Item{MD5: item.MD5, Source: "annas"}, t.TempDir(), "")
	if err == nil {
		if res.SizeBytes <= 0 {
			t.Fatalf("downloaded %d bytes", res.SizeBytes)
		}
		t.Logf("escalated item downloaded: bytes=%d verified=%v", res.SizeBytes, res.Verified)
		return
	}
	skipIfAnnasUnavailable(t, err)
	t.Fatalf("escalated item failed to download in an undiagnosed way: %v", err)
}
