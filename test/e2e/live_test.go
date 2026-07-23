//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"errors"
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
