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

// loadLiveConfig loads the real configuration and redirects downloads to a fresh
// temp dir so the suite never writes into the user's Downloads folder and does not
// need config.Validate. The default rate limit (1 rps) is preserved to stay polite.
func loadLiveConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.DownloadDir = t.TempDir()
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
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/index.php", http.NoBody)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "libgen-mcp-e2e-probe")
	client := &http.Client{
		Timeout:       probeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
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

// smallestDownloadable returns the first result (from a size-ascending search)
// that has a canonical md5 and a parseable, non-zero size within the polite cap.
// It returns a zero Result when none qualifies.
func smallestDownloadable(results []libgen.Result) libgen.Result {
	for i := range results {
		r := results[i]
		if !md5Re.MatchString(r.MD5) {
			continue
		}
		if n, ok := parseSize(r.Size); ok && n > 0 && n <= maxE2EDownloadBytes {
			return r
		}
	}
	return libgen.Result{}
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
