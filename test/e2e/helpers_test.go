//go:build e2e

// Package e2e holds the live, network-dependent end-to-end tests for libgen-mcp.
//
// The whole package is behind the `e2e` build tag, so it is invisible to a plain
// `go test ./...`. Even under the tag, every test SKIPS unless the LIBGEN_E2E
// environment variable is set to "1" AND the configured mirror is reachable, so
// the suite never fails CI or a PR when the live site is down. See requireLive.
package e2e

import (
	"bytes"
	"context"
	cryptomd5 "crypto/md5" //nolint:gosec // MD5 is the digest LibGen keys files by; used only for integrity matching.
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
)

const (
	// liveEnvVar gates the whole suite: tests run only when it equals "1".
	liveEnvVar = "1"
	// liveEnvName is the environment variable inspected by requireLive.
	liveEnvName = "LIBGEN_E2E"
	// probeTimeout bounds the reachability probe and the mirror discovery lookup.
	probeTimeout = 10 * time.Second
	// politeDelay is the courtesy pause inserted between successive live requests,
	// on top of the client's own rate limiter (1 rps by default).
	politeDelay = 1 * time.Second
	// maxE2EDownloadBytes caps live downloads so a size-parsing mistake can never
	// pull a large file. It keeps the suite a polite citizen of the public mirrors.
	maxE2EDownloadBytes = 25 << 20 // 25 MiB
	// minE2EDownloadBytes is the size FLOOR every live download and read target
	// must clear. A size-ascending catalog search leads with degenerate rows — the
	// first nonfiction "python" hit is an 87-byte .txt — and without a floor every
	// test that "downloaded" or "read" a file moved those 87 bytes: no pagination,
	// no in-document matches, no table of contents, no PDF path. 100 KiB clears the
	// stub rows (the catalog's real books start around it) while staying small
	// enough to be polite.
	minE2EDownloadBytes = 100 << 10 // 100 KiB
	// targetSearchPages bounds how many size-ascending pages are walked when
	// hunting for a target above the floor. Page one sits entirely below it, so at
	// least two are normally needed.
	targetSearchPages = 4
	// targetPageSize is the results-per-page used while hunting for a target, so
	// the floor is usually reached within two pages.
	targetPageSize = 100
	// minComicBytes is the floor for the not-extractable read case. The comics
	// collection has almost nothing between its stub rows and its multi-hundred-
	// megabyte scans, so it needs a lower bar than the rest of the suite: enough to
	// be a genuine archive with no text layer, which is all that case has to prove.
	minComicBytes = 32 << 10 // 32 KiB
	// upstreamProbeTimeout bounds a source's reachability precondition. It is
	// deliberately short: a host that blackholes connections must cost seconds, not
	// the test's whole timeout budget.
	upstreamProbeTimeout = 3 * time.Second
)

// md5Re matches a canonical lowercase LibGen md5 digest.
var md5Re = regexp.MustCompile(`^[a-f0-9]{32}$`)

// liveEnv bundles the shared state a live test needs: the built client and the
// configuration it was built from (so a test can rebuild a variant, e.g. with a
// download cap).
type liveEnv struct {
	cfg    *config.Config
	client *libgen.Client
}

// requireLive enforces the suite's gate and returns the shared live environment.
// It SKIPS (never fails) when LIBGEN_E2E != "1" or the configured mirror does not
// answer its search page with a 2xx/3xx within probeTimeout, so the suite is safe
// to wire into CI and PR checks. Genuine setup faults (config or manager
// construction) fail loudly, because under the gate they indicate a real bug.
func requireLive(t *testing.T) *liveEnv {
	t.Helper()
	if os.Getenv(liveEnvName) != liveEnvVar {
		t.Skipf("live e2e disabled; set %s=%s to run against the real site", liveEnvName, liveEnvVar)
	}
	cfg := loadLiveConfig(t)
	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		t.Fatalf("mirrors.NewManager: %v", err)
	}
	base := preferredMirror(t, mgr)
	if !reachable(t, base) {
		t.Skipf("mirror %s not reachable within %s; skipping live e2e", base, probeTimeout)
	}
	return &liveEnv{cfg: cfg, client: libgen.New(mgr, cfg)}
}

// e2eUnpaywallEmail is the contact address the suite sets when none is configured,
// so the DOI/article path actually exercises the unpaywall source (which is
// disabled by default unless LIBGEN_MCP_UNPAYWALL_EMAIL is set).
const e2eUnpaywallEmail = "libgen-mcp-e2e@jmrp.io"

// loadLiveConfig loads the real configuration and redirects downloads to a fresh
// temp dir so the suite never writes into the user's Downloads folder and does not
// need config.Validate. The default rate limit (1 rps) is preserved to stay polite.
// It also supplies a contact email when none is set, so unpaywall (email-gated and
// off by default) is exercised rather than silently skipped.
func loadLiveConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.DownloadDir = t.TempDir()
	if strings.TrimSpace(cfg.UnpaywallEmail) == "" {
		cfg.UnpaywallEmail = e2eUnpaywallEmail
	}
	// Shrink the download start-retry schedule so a source that cannot start fails
	// fast instead of consuming the whole test timeout: the default ~60 s schedule
	// would exhaust a 60 s test context before the fallback source is ever tried.
	// The staged-retry timing itself is covered by unit tests and the eval (S9).
	cfg.DownloadStartRetryWaits = []time.Duration{time.Second, 2 * time.Second}
	return cfg
}

// buildClient assembles a libgen.Client from cfg via a fresh mirror manager. It
// lets a test customize cfg (e.g. a download cap) before constructing the client.
func buildClient(t *testing.T, cfg *config.Config) *libgen.Client {
	t.Helper()
	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		t.Fatalf("mirrors.NewManager: %v", err)
	}
	return libgen.New(mgr, cfg)
}

// preferredMirror returns the manager's first (preferred) mirror base URL, falling
// back to the package default when discovery yields nothing.
func preferredMirror(t *testing.T, mgr *mirrors.Manager) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	list := mgr.Mirrors(ctx)
	if len(list) == 0 {
		return mirrors.DefaultPreferred
	}
	return list[0]
}

// reachable reports whether base answers its search page with a 2xx/3xx status
// within probeTimeout. Redirects are not followed, so a 3xx is observed directly.
func reachable(t *testing.T, base string) bool {
	t.Helper()
	status, err := probeStatus(base+"/index.php", probeTimeout)
	if err != nil {
		return false
	}
	return status >= 200 && status < 400
}

// probeStatus GETs url with a short budget and returns the status code it
// answered with. Redirects are not followed, so a 3xx is observed directly. It is
// the shared primitive behind both the mirror gate and the per-source upstream
// preconditions.
func probeStatus(url string, timeout time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "libgen-mcp-e2e-probe")
	client := &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

// requireUpstream SKIPS the calling test when a source's own upstream does not
// answer probeURL within upstreamProbeTimeout. ANY status counts as reachable —
// the question is whether the host answers at all, which a 404 settles as well as
// a 200.
//
// It exists because a blackholed upstream otherwise burns the test's entire
// timeout budget proving nothing: api.fatcat.wiki does not answer from every
// network, and the fatcat case spent 90 s — 27% of a 335 s suite — reaching that
// conclusion. Reachability is a PRECONDITION only: once the host answers, the
// test's failure classification stays strict.
func requireUpstream(t *testing.T, name, probeURL string) {
	t.Helper()
	if _, err := probeStatus(probeURL, upstreamProbeTimeout); err != nil {
		t.Skipf("upstream unreachable: %s did not answer %s within %s (%v)", name, probeURL, upstreamProbeTimeout, err)
	}
}

// pace inserts the courtesy pause between successive live requests.
func pace() { time.Sleep(politeDelay) }

// assertResultStructure asserts the structural invariants of a single search
// result: some identity (a title or an md5 — real magazine/comic rows carry an
// md5 with the human name in other fields and an empty Title), a canonical md5
// when present, and at least one download option. It checks shape, not exact
// values, which drift over time.
func assertResultStructure(t *testing.T, r libgen.Result) {
	t.Helper()
	if strings.TrimSpace(r.Title) == "" && r.MD5 == "" {
		t.Errorf("result has neither a title nor an md5: %+v", r)
	}
	if r.MD5 != "" && !md5Re.MatchString(r.MD5) {
		t.Errorf("md5 %q does not match %s", r.MD5, md5Re)
	}
	if len(r.Downloads) == 0 {
		t.Errorf("result %q (md5=%s) has no download options", r.Title, r.MD5)
	}
}

// hasNonEmptyField reports whether the details record carries at least one
// non-empty value beyond the synthetic file_id injected by the client.
func hasNonEmptyField(m map[string]any) bool {
	for k, v := range m {
		if k == "file_id" {
			continue
		}
		switch val := v.(type) {
		case string:
			if strings.TrimSpace(val) != "" {
				return true
			}
		case nil:
		default:
			return true
		}
	}
	return false
}

// firstMD5 runs a nonfiction search and returns the first result carrying a
// canonical md5. It skips the calling test when no such result is available.
func firstMD5(t *testing.T, ctx context.Context, c *libgen.Client, query string) string {
	t.Helper()
	page, _, err := c.Search(ctx, libgen.SearchParams{Query: query, Topics: []string{"nonfiction"}})
	if err != nil {
		t.Fatalf("Search(%q) error: %v", query, err)
	}
	for i := range page.Results {
		if md5Re.MatchString(page.Results[i].MD5) {
			return page.Results[i].MD5
		}
	}
	t.Skipf("no result with a valid md5 for query %q; cannot continue", query)
	return ""
}

// liveTarget describes the live catalog target a download or read test needs.
type liveTarget struct {
	// topic is the collection to search.
	topic string
	// query is the search query.
	query string
	// exts, when non-empty, restricts the target to these lowercase extensions —
	// used by the read cases, which need a format the extractors actually support.
	exts []string
	// minBytes overrides minE2EDownloadBytes for a collection whose real files sit
	// below it (comics jump straight from stub rows to hundreds of megabytes, and
	// nothing in between). Zero means the default floor.
	minBytes int64
}

// floor returns the size floor this target requires.
func (spec liveTarget) floor() int64 {
	if spec.minBytes > 0 {
		return spec.minBytes
	}
	return minE2EDownloadBytes
}

// readableExts are the extensions the read tool can extract text from, so a read
// case asks for one of these rather than gambling on whatever the catalog's size
// ordering happens to put first.
var readableExts = []string{"pdf", "epub"}

// findLiveTarget walks a size-ascending live search for the SMALLEST result that
// clears minE2EDownloadBytes, stays within maxE2EDownloadBytes, carries a
// canonical md5 and (when spec.exts is set) has one of the wanted extensions.
//
// It pages because page one of a size-ascending search sits entirely below the
// floor: the floor is what makes the download and read cases real, so reaching it
// is worth the extra request. It SKIPS the calling test when no page yields a
// qualifying target, which is a live-data condition rather than a code fault.
func findLiveTarget(t *testing.T, ctx context.Context, c *libgen.Client, spec liveTarget) libgen.Result {
	t.Helper()
	for p := 1; p <= targetSearchPages; p++ {
		page, _, err := c.Search(ctx, libgen.SearchParams{
			Query: spec.query, Topics: []string{spec.topic},
			Order: "size", OrderMode: "asc",
			ResultsPerPage: targetPageSize, Page: p,
		})
		if err != nil {
			t.Fatalf("Search(%q, page %d) error: %v", spec.query, p, err)
		}
		if target := qualifyingTarget(page.Results, spec); target.MD5 != "" {
			return target
		}
		if len(page.Results) < targetPageSize {
			break
		}
	}
	t.Skipf("no %s target for %q between %d and %d bytes (exts %v) across %d pages; skipping to stay polite",
		spec.topic, spec.query, spec.floor(), maxE2EDownloadBytes, spec.exts, targetSearchPages)
	return libgen.Result{}
}

// qualifyingTarget returns the first result on a size-ascending page that carries
// a canonical md5, has one of the wanted extensions (any, when exts is empty) and
// a parseable size inside [spec.floor(), maxE2EDownloadBytes]. It returns a zero
// Result when the page holds none.
func qualifyingTarget(results []libgen.Result, spec liveTarget) libgen.Result {
	for i := range results {
		r := results[i]
		if !md5Re.MatchString(r.MD5) || !hasExtension(r, spec.exts) {
			continue
		}
		if n, ok := parseSize(r.Size); ok && n >= spec.floor() && n <= maxE2EDownloadBytes {
			return r
		}
	}
	return libgen.Result{}
}

// hasExtension reports whether r's extension is one of want (case-insensitive).
// An empty want accepts any extension.
func hasExtension(r libgen.Result, want []string) bool {
	if len(want) == 0 {
		return true
	}
	got := strings.ToLower(strings.TrimSpace(r.Extension))
	return slices.Contains(want, got)
}

// parseSize converts a human-readable size such as "1.2 MB" or "820 KB" into a
// byte count. It reports ok=false when the input is not a "<number> <unit>" pair
// with a recognized unit.
func parseSize(s string) (int64, bool) {
	fields := strings.Fields(s)
	if len(fields) != 2 {
		return 0, false
	}
	num, err := strconv.ParseFloat(strings.ReplaceAll(fields[0], ",", "."), 64)
	if err != nil {
		return 0, false
	}
	mult := map[string]float64{"B": 1, "KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40}
	m, ok := mult[strings.ToUpper(fields[1])]
	if !ok {
		return 0, false
	}
	return int64(num * m), true
}

// sourceFailure is one KNOWN, diagnosed way a download source can fail against
// its live upstream.
//
// The pattern must pin BOTH the source that failed and the specific diagnosis.
// The bare substrings this suite used to match on — "requesting", "returned HTTP",
// "context deadline" — appear in almost every failure a download can produce,
// including a code regression that points a source at the wrong host: matching on
// them classified "we are calling it wrong" as "the upstream is down today", which
// is precisely the confusion the classified-outcome discipline exists to prevent.
type sourceFailure struct {
	// re matches the error text, anchored on the source's own message prefix.
	re *regexp.Regexp
	// why names the class in the SKIP message.
	why string
}

// diagnosed builds a failure class whose pattern requires the source's own error
// prefix ("<source>: ") immediately followed by diag, a regular-expression
// fragment naming the specific diagnosis. Every source in internal/libgen prefixes
// its errors this way, so the pair identifies the failure unambiguously.
func diagnosed(source, diag, why string) sourceFailure {
	return sourceFailure{re: regexp.MustCompile(regexp.QuoteMeta(source+": ") + diag), why: why}
}

// transportTo builds the transport-failure class for a source: the error must come
// from the source's own request wrapper AND name the host the source is supposed
// to be calling (Go's *url.Error carries the full URL). Pinning the host is what
// separates "the upstream is down today" from "we are calling the wrong upstream" —
// a source repointed at a bad host fails with identical wrapper text, and must
// FAIL here rather than skip.
func transportTo(source, wrapper, host string) sourceFailure {
	return sourceFailure{
		re: regexp.MustCompile(
			regexp.QuoteMeta(source+": "+wrapper) + `.*` + regexp.QuoteMeta(`"https://`+host+`/`)),
		why: "transport failure reaching " + host,
	}
}

// classifyOrFail requires a live source failure to match one of the KNOWN,
// diagnosed classes and SKIPs with that class's reason. An error outside the set
// FAILS: a new, unrecognized failure mode must surface here rather than be
// tolerated as flakiness and discovered by chance later.
func classifyOrFail(t *testing.T, source string, err error, classes []sourceFailure) {
	t.Helper()
	msg := err.Error()
	for _, c := range classes {
		if c.re.MatchString(msg) {
			t.Skipf("%s unavailable in a known way (%s): %v", source, c.why, err)
		}
	}
	t.Fatalf("%s failed in an undiagnosed way — update this test's classification only if this is a legitimate new outcome, not if we are calling the upstream wrong: %v", source, err)
}

// assertSourcePDF asserts the best case of a source-restricted article download:
// the named source served the file, it is non-empty, and the bytes really are a
// PDF. The source check matters because a chain bug could have another provider
// answer, and the PDF check because several sources omit the mimetype, so a
// non-PDF asset would still arrive under a .pdf name.
func assertSourcePDF(t *testing.T, source string, res *libgen.DownloadResult) {
	t.Helper()
	if res.SizeBytes <= 0 {
		t.Fatalf("%s reported a download of %d bytes", source, res.SizeBytes)
	}
	if res.Source != source {
		t.Errorf("Source = %q, want %q — another source answered a restricted download", res.Source, source)
	}
	assertPDF(t, res.Path)
	t.Logf("%s served a real PDF: bytes=%d", source, res.SizeBytes)
}

// assertFileMD5 asserts that the file at path exists, is non-empty, and hashes to
// wantMD5 (independent confirmation of an end-to-end integrity match).
func assertFileMD5(t *testing.T, path, wantMD5 string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("downloaded file missing: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("downloaded file is empty: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	digest := cryptomd5.New() //nolint:gosec // integrity match against the LibGen-provided md5.
	if _, copyErr := io.Copy(digest, f); copyErr != nil {
		t.Fatalf("hashing %s: %v", path, copyErr)
	}
	if got := hex.EncodeToString(digest.Sum(nil)); !strings.EqualFold(got, wantMD5) {
		t.Errorf("md5 mismatch: got %s want %s", got, wantMD5)
	}
}

// assertPDF asserts that the file at path begins with the %PDF magic bytes.
func assertPDF(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 5)
	n, _ := io.ReadFull(f, head)
	if n < 4 || !bytes.HasPrefix(head[:n], []byte("%PDF")) {
		t.Errorf("expected a PDF (%%PDF magic), got %q", head[:n])
	}
}

// proveChainResolves is the documented fallback for TestE2EDownloadSmall when a
// full small download is not available: it proves the ads.php -> get.php -> CDN
// chain resolves and that the CDN's first bytes are a real (non-HTML) file, then
// stops without pulling the whole payload. It skips the test on a transient
// resolution failure so a hiccup does not mask a working pipeline.
func proveChainResolves(t *testing.T, ctx context.Context, c *libgen.Client, md5hex string) {
	t.Helper()
	getURL, _, err := c.ResolveGetURL(ctx, md5hex)
	if err != nil {
		t.Skipf("could not resolve download chain for %s: %v", md5hex, err)
	}
	if getURL == "" {
		t.Fatalf("resolved an empty get.php URL for %s", md5hex)
	}
	head := fetchHead(t, ctx, getURL, 512)
	if len(head) == 0 {
		t.Skipf("CDN returned no bytes for %s", md5hex)
	}
	if looksLikeHTML(head) {
		t.Errorf("CDN returned an HTML page instead of a file for %s", md5hex)
	}
	t.Logf("chain resolved; first %d CDN bytes are a non-HTML file for %s", len(head), md5hex)
}

// fetchHead requests the first n bytes of url with a Range header and returns
// whatever the server delivered. It skips the calling test on any transport or
// status error, since a fetch hiccup is not a suite failure.
func fetchHead(t *testing.T, ctx context.Context, url string, n int) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Skipf("building head request: %v", err)
	}
	req.Header.Set("User-Agent", "libgen-mcp-e2e")
	req.Header.Set("Range", "bytes=0-"+strconv.Itoa(n-1))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("fetching head: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		t.Skipf("CDN returned status %d", resp.StatusCode)
	}
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, int64(n)))
	return buf
}

// looksLikeHTML reports whether b (a sniffed body header) begins, after trimming
// leading ASCII whitespace, with an HTML document marker.
func looksLikeHTML(b []byte) bool {
	trimmed := bytes.TrimLeft(b, " \t\r\n\f\v")
	lower := bytes.ToLower(trimmed)
	return bytes.HasPrefix(lower, []byte("<!doctype html")) ||
		bytes.HasPrefix(lower, []byte("<html")) ||
		bytes.HasPrefix(lower, []byte("<!--"))
}
