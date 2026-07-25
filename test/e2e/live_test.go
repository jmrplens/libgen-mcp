//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/discovery"
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
// searches with order=size, order_mode=asc and picks the first result whose parsed
// size sits inside [minE2EDownloadBytes, maxE2EDownloadBytes]. The FLOOR matters as
// much as the ceiling: without it the ordering hands every run the catalog's
// smallest row — an 87-byte stub — and the download proves nothing. A per-client
// download cap enforces the ceiling defensively. If the download cannot complete
// (expired key, blocked CDN), it falls back to proving the
// ads.php -> get.php -> CDN chain resolves and that the first bytes are a valid,
// non-HTML file, without pulling the whole payload.
func TestE2EDownloadSmall(t *testing.T) {
	requireLive(t)
	cfg := loadLiveConfig(t)
	cfg.MaxDownloadBytes = maxE2EDownloadBytes
	client := buildClient(t, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	target := smallBookTarget(t, ctx, client, "python")
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
	if res.SizeBytes < minE2EDownloadBytes {
		t.Errorf("downloaded %d bytes for a target the catalog sized at %q; the size floor did not hold",
			res.SizeBytes, target.Size)
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

// europePMCLiveDOI is a reliably open-access PLOS Biology article whose full text
// Europe PMC holds (PMC4991899), verified served as application/pdf on 2026-07-24.
const europePMCLiveDOI = "10.1371/journal.pbio.1002533"

// biorxivLiveDOI is a real, CC0-licensed bioRxiv preprint (verified present in the
// details API on 2026-07-24), so the biorxiv source has a deterministic target.
const biorxivLiveDOI = "10.1101/2020.12.30.424878"

// fatcatLiveDOI is an open-access DOI whose Internet Archive Scholar release page
// advertises a Wayback capture that served real PDF bytes on 2026-07-25. It is
// deliberately NOT the DOI the other article cases use: that one's two preserved
// captures both answer a redirect loop today, which makes it a fine example of why
// candidates are probed but a poor target for asserting a download.
const fatcatLiveDOI = "10.1038/s41586-021-03819-2"

// downloadFromSource runs a source-restricted live download of doi and returns the
// outcome, so every classified-outcome case shares one harness: a size-capped
// client, a fresh temp dir, and no other source able to mask the one under test.
func downloadFromSource(t *testing.T, ctx context.Context, doi, source string) (*libgen.DownloadResult, error) {
	t.Helper()
	cfg := loadLiveConfig(t)
	cfg.MaxDownloadBytes = maxE2EDownloadBytes
	client := buildClient(t, cfg)
	return client.DownloadItem(ctx, libgen.Item{DOI: doi, Source: source}, t.TempDir(), "")
}

// europePMCFailures are the KNOWN, diagnosed ways the europepmc source can fail
// live. Each pattern pins the source's own error prefix plus a specific diagnosis,
// and the transport class additionally pins the EBI host, so a source repointed at
// the wrong upstream fails the test instead of skipping it.
var europePMCFailures = []sourceFailure{
	diagnosed("europepmc", `"[^"]*" is not indexed`, "DOI absent from Europe PMC"),
	diagnosed("europepmc", `"[^"]*" is indexed but has no open-access full text`, "indexed but not open access"),
	diagnosed("europepmc", `no reachable PDF endpoint for `, "both render endpoints unreachable"),
	diagnosed("europepmc", `"[^"]*" returned HTTP \d+`, "search API answered an unexpected status"),
	transportTo("europepmc", "requesting ", "www.ebi.ac.uk"),
}

// TestE2EEuropePMCClassifiedOutcome exercises the europepmc source end to end
// against the live Europe PMC APIs, restricted to source=europepmc so no other
// source can mask its behavior. On error the failure must be one of the known,
// diagnosed classes; anything else fails the test.
func TestE2EEuropePMCClassifiedOutcome(t *testing.T) {
	requireLive(t)
	requireUpstream(t, "europepmc", "https://www.ebi.ac.uk/europepmc/webservices/rest/search?query=test&format=json")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	res, err := downloadFromSource(t, ctx, europePMCLiveDOI, "europepmc")
	if err == nil {
		assertSourcePDF(t, "europepmc", res)
		return
	}
	classifyOrFail(t, "europepmc", err, europePMCFailures)
}

// biorxivFailures are the KNOWN, diagnosed ways the biorxiv source can fail live.
// The details-API classes pin api.biorxiv.org; the content-host class covers the
// interstitial bioRxiv serves in place of the PDF, which arrives from the download
// stream rather than the source, so it is matched on the chain's own wrapper.
var biorxivFailures = []sourceFailure{
	diagnosed("biorxiv", `"[^"]*" not found on bioRxiv or medRxiv`, "neither server carries the DOI"),
	diagnosed("biorxiv", `(bio|med)rxiv returned HTTP \d+`, "details API answered an unexpected status"),
	transportTo("biorxiv", "requesting ", "api.biorxiv.org"),
	{
		re:  regexp.MustCompile(`source biorxiv: .*HTML page instead of the file`),
		why: "the content host served an interstitial, not the PDF",
	},
}

// TestE2EBiorxivClassifiedOutcome exercises the biorxiv source end to end against
// the live bioRxiv details API and content host, restricted to source=biorxiv. On
// error the failure must be one of the known, diagnosed classes.
func TestE2EBiorxivClassifiedOutcome(t *testing.T) {
	requireLive(t)
	requireUpstream(t, "biorxiv", "https://api.biorxiv.org/details/biorxiv/"+biorxivLiveDOI)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	res, err := downloadFromSource(t, ctx, biorxivLiveDOI, "biorxiv")
	if err == nil {
		assertSourcePDF(t, "biorxiv", res)
		return
	}
	classifyOrFail(t, "biorxiv", err, biorxivFailures)
}

// fatcatScholarProbe is the DOI lookup on fatcat's own web frontend, which is what
// the source now drives: the JSON API it used to call (api.fatcat.wiki) resolves in
// DNS but never completes a TCP handshake, from any network or client tried, with no
// deprecation notice behind it. The classified-outcome case probes the frontend first
// so a network that cannot reach it skips instead of burning the full timeout budget.
const fatcatScholarProbe = "https://scholar.archive.org/fatcat/release/lookup?doi=" + fatcatLiveDOI

// fatcatFailures are the KNOWN, diagnosed ways the fatcat source can fail live. The
// transport class pins scholar.archive.org so a source repointed at some other host
// fails the test rather than passing as an upstream outage, and the release-page class
// is what a session challenge or a layout change surfaces as — the outcome that must
// never masquerade as empty coverage.
var fatcatFailures = []sourceFailure{
	diagnosed("fatcat", `"[^"]*" is unknown to fatcat`, "fatcat has no release for the DOI"),
	diagnosed("fatcat", `"[^"]*" has no preserved full text`, "release known but nothing preserved"),
	diagnosed("fatcat", `no preserved copy of "[^"]*" currently serves a PDF`, "every preserved capture is dead today"),
	diagnosed("fatcat", `returned no release page`, "a session challenge or a changed layout, not a miss"),
	diagnosed("fatcat", `"[^"]*" returned HTTP \d+`, "the frontend answered an unexpected status"),
	transportTo("fatcat", "requesting ", "scholar.archive.org"),
	{
		re:  regexp.MustCompile(`source fatcat: .*HTML page instead of the file`),
		why: "the Internet Archive served an interstitial, not the PDF",
	},
}

// TestE2EFatcatClassifiedOutcome exercises the fatcat source end to end against the
// live Internet Archive Scholar frontend and the Wayback capture it names, restricted
// to source=fatcat so no other source can mask its behavior. The DOI is one whose
// preserved copy really downloads, so the expected outcome is a real PDF; on error the
// failure must be one of the known, diagnosed classes.
func TestE2EFatcatClassifiedOutcome(t *testing.T) {
	requireLive(t)
	requireUpstream(t, "fatcat", fatcatScholarProbe)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	res, err := downloadFromSource(t, ctx, fatcatLiveDOI, "fatcat")
	if err == nil {
		// The PDF check matters more here than elsewhere: the release page names a
		// Wayback capture of whatever the publisher served, so an interstitial or an
		// error page captured under a .pdf URL would still arrive looking plausible.
		assertSourcePDF(t, "fatcat", res)
		return
	}
	classifyOrFail(t, "fatcat", err, fatcatFailures)
}

// scihubLiveDOI is a long-established, heavily-cited DOI: if Sci-Hub carries
// anything, it carries this.
const scihubLiveDOI = "10.1016/j.cell.2011.02.013"

// scihubFailures are the KNOWN, diagnosed ways the scihub source can fail live.
// Every class requires the outer "no mirror resolved" wrapper AND a specific inner
// diagnosis, and the transport class pins the sci-hub.<tld> host family, so a
// source pointed at some other host fails rather than skipping. Sci-Hub is the
// most fragile source in the chain — its mirrors rotate and block — which is
// exactly why the failure it reports has to stay legible.
var scihubFailures = []sourceFailure{
	diagnosed("scihub", `no mirror resolved "[^"]*": scihub: host "[^"]*" served no PDF link`,
		"mirrors reachable, article absent from Sci-Hub"),
	diagnosed("scihub", `no mirror resolved "[^"]*": scihub: host "[^"]*" returned HTTP \d+`,
		"every mirror answered an unexpected status"),
	diagnosed("scihub", `no mirror resolved "[^"]*": scihub: requesting "sci-hub\.[a-z]+"`,
		"every sci-hub mirror was unreachable"),
	diagnosed("scihub", `no hosts configured for `, "no Sci-Hub hosts configured"),
}

// TestE2ESciHubClassifiedOutcome exercises the scihub source end to end against the
// live Sci-Hub mirrors, restricted to source=scihub so no other source can mask its
// behavior. It was the only one of the ten known sources with no source-restricted
// live test, and the most fragile: its mirror list rotates, so a silent move from
// "the mirrors are blocking us today" to "we are asking them the wrong way" had
// nothing watching for it. On error the failure must be one of the known, diagnosed
// classes; anything else fails the test.
func TestE2ESciHubClassifiedOutcome(t *testing.T) {
	requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	res, err := downloadFromSource(t, ctx, scihubLiveDOI, "scihub")
	if err == nil {
		assertSourcePDF(t, "scihub", res)
		return
	}
	classifyOrFail(t, "scihub", err, scihubFailures)
}

// coreLiveDOI is an open-access DOI whose CORE record served a live PDF on
// 2026-07-24; CORE's download URLs go stale often, so the test tolerates a
// diagnosed "no live file today" outcome rather than pinning on permanence.
const coreLiveDOI = "10.1186/s12864-016-3299-5"

// coreFailures are the KNOWN, diagnosed ways the core source can fail live. The
// transport class pins api.core.ac.uk so a repointed source cannot pass as an
// upstream outage.
var coreFailures = []sourceFailure{
	diagnosed("core", `"[^"]*" has no downloadable open-access full text`, "CORE has no live file for this DOI today"),
	diagnosed("core", `"[^"]*" is not in CORE`, "CORE does not hold the DOI"),
	diagnosed("core", `API key rejected \(HTTP \d+\)`, "the API key is invalid or expired"),
	diagnosed("core", `"[^"]*" returned HTTP \d+`, "the API answered an unexpected status"),
	transportTo("core", "requesting ", "api.core.ac.uk"),
}

// TestE2ECoreClassifiedOutcome exercises the opt-in core source end to end against
// the live CORE API, restricted to source=core. It is skipped unless
// LIBGEN_MCP_CORE_KEY is configured (the source is out of the chain without a key).
// On error the failure must be one of the known, diagnosed classes; anything else
// fails the test.
func TestE2ECoreClassifiedOutcome(t *testing.T) {
	requireLive(t)
	if strings.TrimSpace(os.Getenv("LIBGEN_MCP_CORE_KEY")) == "" {
		t.Skip("LIBGEN_MCP_CORE_KEY not set; the core source is opt-in and out of the chain")
	}
	requireUpstream(t, "core", "https://api.core.ac.uk/v3/")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	res, err := downloadFromSource(t, ctx, coreLiveDOI, "core")
	if err == nil {
		assertSourcePDF(t, "core", res)
		return
	}
	classifyOrFail(t, "core", err, coreFailures)
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

// TestE2EDownloadToolHonorsSourceArgument proves the `source` argument works where
// a real client actually sets it: as a tool argument over MCP. Every other
// source-restricted case in this suite builds a libgen.Item{Source: …} directly,
// which bypasses validateDownloadInput and the dynamically-built enum entirely —
// so the argument a model would pass had no live coverage at all. It resolves the
// pinned Anna's-only md5 with source="annas" and asserts the resolved link really
// came from that source.
func TestE2EDownloadToolHonorsSourceArgument(t *testing.T) {
	env := requireLive(t)
	item := loadEscalationItem(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	server := mcp.NewServer(&mcp.Implementation{Name: "libgen-mcp-e2e", Version: "test"}, nil)
	tools.Register(server, env.client, env.cfg)
	session := connectInMemory(t, ctx, server, nil)

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": item.MD5, "source": "annas", "resolve_only": true},
	})
	if err != nil {
		t.Fatalf("CallTool(download source=annas) transport error: %v", err)
	}
	if res.IsError {
		// A live Anna's outage is tolerated; a rejected argument is not, so the
		// error text has to name a known unavailability rather than a bad input.
		skipIfAnnasUnavailable(t, errors.New(textOf(res)))
		t.Fatalf("download with source=annas failed in an undiagnosed way: %v", res.Content)
	}
	var out tools.DownloadOutput
	decodeStructured(t, res, &out)
	if out.Resolved == nil {
		t.Fatalf("resolve_only with source=annas returned no link: %+v", out)
	}
	if out.Resolved.Source != "annas" {
		t.Errorf("resolved.source = %q, want annas — the source argument did not pin the chain", out.Resolved.Source)
	}
	t.Logf("tool-layer source argument honored: resolved via %s", out.Resolved.Source)
}

// TestE2EDownloadToolAdvertisesEveryEnabledSource proves the download tool's
// `source` enum, as served over ListTools, names every source the deployment has
// enabled. The enum is the model's ONLY discovery path to a source — a provider
// missing from it is a provider no client will ever ask for by name — and it was
// covered by unit tests alone, never over a real tools/list round-trip.
func TestE2EDownloadToolAdvertisesEveryEnabledSource(t *testing.T) {
	env := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	server := mcp.NewServer(&mcp.Implementation{Name: "libgen-mcp-e2e", Version: "test"}, nil)
	tools.Register(server, env.client, env.cfg)
	session := connectInMemory(t, ctx, server, nil)

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}
	enum := downloadSourceEnum(t, list)
	for _, want := range expectedSourceEnum(env.cfg) {
		if !slices.Contains(enum, want) {
			t.Errorf("download source enum omits %q; a model has no way to ask for it. got %v", want, enum)
		}
	}
	t.Logf("download source enum advertises %v", enum)
}

// listedSchema is the slice of an advertised input schema this suite inspects: the
// `source` property's enum. A tools/list result carries the schema as decoded JSON,
// so it is re-marshaled through this shape rather than reaching into the server's
// own schema type.
type listedSchema struct {
	Properties struct {
		Source struct {
			Enum []string `json:"enum"`
		} `json:"source"`
	} `json:"properties"`
}

// downloadSourceEnum extracts the download tool's `source` enum from a tools/list
// result. It FAILS when the tool, the property, or the enum is missing: each is a
// discoverability guarantee, not an optional extra.
func downloadSourceEnum(t *testing.T, list *mcp.ListToolsResult) []string {
	t.Helper()
	for _, tool := range list.Tools {
		if tool.Name != "download" {
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshaling the download tool's input schema: %v", err)
		}
		var schema listedSchema
		if uerr := json.Unmarshal(raw, &schema); uerr != nil {
			t.Fatalf("decoding the download tool's input schema: %v", uerr)
		}
		if len(schema.Properties.Source.Enum) == 0 {
			t.Fatalf("the download tool advertises no source enum; every enabled source is undiscoverable. schema: %s", raw)
		}
		return schema.Properties.Source.Enum
	}
	t.Fatal("tools/list did not advertise a download tool")
	return nil
}

// expectedSourceEnum lists the sources this deployment must advertise: every
// KnownSource the configuration enables, minus the two that are credential-gated
// and legitimately absent when their credential is not set.
func expectedSourceEnum(cfg *config.Config) []string {
	want := make([]string, 0, len(config.KnownSources))
	for _, name := range config.KnownSources {
		if len(cfg.Sources) > 0 && !slices.Contains(cfg.Sources, name) {
			continue
		}
		if name == "unpaywall" && strings.TrimSpace(cfg.UnpaywallEmail) == "" {
			continue
		}
		if name == "core" && strings.TrimSpace(cfg.CoreKey) == "" {
			continue
		}
		want = append(want, name)
	}
	return want
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
	// The format and size are parsed out of the card's descriptor line, which only a
	// live check can prove still exists: the pinned fixture would keep passing for as
	// long as it sat there, however far the real page had moved on.
	var described int
	for _, r := range out.Results {
		if r.Origin == "annas" && r.Extension != "" && r.Size != "" {
			described++
		}
	}
	if described == 0 {
		t.Errorf("none of the %d Anna's results carried a format and size; the card layout may have changed", fromAnnas)
	}
	t.Logf("escalated: %d Anna's results, %d describing their file", fromAnnas, described)
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

// TestE2EAnnasFallbackEnrichesByISBN verifies the fallback record reaches the same
// keyless metadata a catalog record would. The pinned item carries an ISBN, which
// a minority of Anna's records do, so this is the case worth proving live: the
// enrichment path looked for an ISBN only on an edition, and a fallback returns a
// file with no edition at all.
func TestE2EAnnasFallbackEnrichesByISBN(t *testing.T) {
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
		Name: "get_details", Arguments: map[string]any{"md5": item.MD5, "enrich": true},
	})
	if err != nil || res.IsError {
		t.Fatalf("get_details on the escalated md5 failed: err=%v content=%v", err, res)
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
		t.Fatalf("file.origin = %q, want annas", got)
	}
	isbn, _ := out.File["isbn"].(string)
	if isbn == "" {
		t.Fatal("the pinned record no longer publishes an ISBN; re-pin it or drop this check")
	}
	if out.Enrichment != nil && out.Enrichment.OpenLibrary != nil {
		t.Logf("enriched the fallback record by its ISBN %s", isbn)
		return
	}
	// Empty enrichment is only acceptable because OpenLibrary has nothing for this
	// ISBN, which the test verifies itself rather than assuming. Skipping here would
	// let a real regression — the ISBN never reaching the lookup — pass unnoticed for
	// as long as the upstream happened to be missing the book.
	if openLibraryKnows(t, ctx, isbn) {
		t.Errorf("OpenLibrary has a record for ISBN %s, but no enrichment came back: %+v", isbn, out.Enrichment)
	}
	t.Logf("OpenLibrary has no record for ISBN %s, so there was nothing to enrich by", isbn)
}

// openLibraryKnows reports whether OpenLibrary holds a record for an ISBN, so a
// missing enrichment can be attributed to the upstream rather than assumed.
func openLibraryKnows(t *testing.T, ctx context.Context, isbn string) bool {
	t.Helper()
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, isbn)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://openlibrary.org/isbn/"+digits+".json", http.NoBody)
	if err != nil {
		t.Fatalf("building the OpenLibrary probe: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("could not reach OpenLibrary to check: %v", err)
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
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

// ericProbeURL is the ERIC search endpoint the classified-outcome test probes before
// spending its budget, so a network that cannot reach api.ies.ed.gov skips instead of
// burning the whole timeout proving it.
const ericProbeURL = "https://api.ies.ed.gov/eric/?search=test&format=json&rows=1"

// ericGreyLiteratureQuery is a topic whose ERIC coverage is grey literature — agency
// reports and evaluations that carry no DOI and appear in none of the other providers.
// It is deliberately not a journal topic: a query arXiv or Crossref also answer would
// be deduped by DOI before ERIC's own contribution could be observed.
const ericGreyLiteratureQuery = "rural school district technology professional development"

// ericMinFullTextBytes is the floor a served full text must clear to count as a
// document rather than an error page that happens to start with the PDF marker.
const ericMinFullTextBytes = 1 << 10

// TestE2EERICSurfacesFetchableGreyLiterature exercises the whole ERIC path live: the
// search tool escalates to the provider, the provider returns education grey
// literature, and the full-text URL it advertises really serves a PDF.
//
// The last step is the one that matters. ERIC is wired as a discovery provider with no
// matching download source, so the pdf_url on a hit IS the delivery mechanism — if that
// URL does not serve bytes, the integration surfaces records nobody can obtain. The URL
// is derived from the accession number rather than looked up, so only a live fetch can
// prove the derivation still holds.
func TestE2EERICSurfacesFetchableGreyLiterature(t *testing.T) {
	env := requireLive(t)
	requireUpstream(t, "eric", ericProbeURL)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	out := callSearch(t, ctx, env, map[string]any{
		"query": ericGreyLiteratureQuery, "extra_sources": "always",
	})
	hosted, indexed := partitionERICHits(out.OpenAccess)
	t.Logf("eric: %d hosted, %d index-only, out of %d beyond-catalog hits",
		len(hosted), len(indexed), len(out.OpenAccess))
	if len(hosted)+len(indexed) == 0 {
		t.Fatalf("no eric-origin hits for an education grey-literature query; the provider "+
			"is not reaching the index (beyond-catalog hits=%d)", len(out.OpenAccess))
	}
	assertERICIndexOnlyMakesNoClaim(t, indexed)
	if len(hosted) == 0 {
		t.Fatal("no eric hit advertised a pdf_url; the hosted-full-text flag is no longer being read")
	}
	assertERICFullTextServesPDF(ctx, t, hosted[0])
}

// partitionERICHits splits the beyond-catalog hits into the ERIC records that advertise
// a hosted full text and the ERIC records that are bibliographic only.
func partitionERICHits(hits []discovery.DiscoveryResult) (hosted, indexed []discovery.DiscoveryResult) {
	for _, h := range hits {
		switch {
		case h.Origin != "eric":
		case h.PDFURL != "":
			hosted = append(hosted, h)
		default:
			indexed = append(indexed, h)
		}
	}
	return hosted, indexed
}

// assertERICIndexOnlyMakesNoClaim verifies a record ERIC merely indexes is never
// presented as free to read. files.eric.ed.gov answers 404 for exactly these, so an
// open-access flag without a pdf_url would be an invitation to a dead link.
func assertERICIndexOnlyMakesNoClaim(t *testing.T, indexed []discovery.DiscoveryResult) {
	t.Helper()
	for _, h := range indexed {
		if h.OpenAccess {
			t.Errorf("index-only eric hit %q claims open access with no pdf_url", h.Title)
		}
	}
}

// assertERICFullTextServesPDF fetches the hit's advertised full-text URL and asserts it
// is a real, non-trivial PDF, logging the byte count actually transferred.
func assertERICFullTextServesPDF(ctx context.Context, t *testing.T, hit discovery.DiscoveryResult) {
	t.Helper()
	if !hit.OpenAccess {
		t.Errorf("eric hit %q advertises a pdf_url but is not flagged open access", hit.Title)
	}
	if !strings.HasPrefix(hit.PDFURL, "https://files.eric.ed.gov/fulltext/") {
		t.Fatalf("eric pdf_url %q is not an ERIC-hosted full text", hit.PDFURL)
	}
	body, status, ctype := fetchERICFullText(ctx, t, hit.PDFURL)
	t.Logf("eric full text: url=%s title=%q content-type=%s bytes=%d",
		hit.PDFURL, hit.Title, ctype, len(body))
	if status != http.StatusOK {
		t.Fatalf("eric advertised %s but it answered HTTP %d; the hosted-full-text flag no "+
			"longer predicts availability", hit.PDFURL, status)
	}
	if !bytes.HasPrefix(body, []byte("%PDF")) {
		t.Fatalf("%s did not serve a PDF (first bytes %q)", hit.PDFURL, body[:min(len(body), 16)])
	}
	if len(body) < ericMinFullTextBytes {
		t.Fatalf("%s served only %d bytes; that is not a document", hit.PDFURL, len(body))
	}
}

// fetchERICFullText GETs an ERIC full-text URL and returns the bounded body, the status
// and the content type. An unreachable host skips rather than fails, matching the
// upstream-precondition discipline of the rest of the suite; anything the host actually
// answers is the caller's to judge.
//
// Accept is set explicitly: Go sends none, and a content-negotiating CDN can read that
// as a browser asking for a page rather than a client asking for the file.
func fetchERICFullText(ctx context.Context, t *testing.T, rawURL string) (body []byte, status int, contentType string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		t.Fatalf("building request for %s: %v", rawURL, err)
	}
	req.Header.Set("User-Agent", "libgen-mcp-e2e-probe")
	req.Header.Set("Accept", "*/*")
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		t.Skipf("eric full text unreachable: %s (%v)", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxE2EDownloadBytes))
	if err != nil {
		t.Fatalf("reading %s: %v", rawURL, err)
	}
	return body, resp.StatusCode, resp.Header.Get("Content-Type")
}
