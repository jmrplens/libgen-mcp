package libgen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

type staticMirrors []string

func (s staticMirrors) Mirrors(context.Context) []string { return s }

// newTestClient builds a Client with sane test defaults: a very high rate limiter
// (no waiting), a single attempt by default and a near-zero backoff. Tests that
// exercise retries override c.retry.
func newTestClient(m MirrorLister) *Client {
	cfg := &config.Config{
		Timeout:                5 * time.Second,
		RateRPS:                1000,
		RateBurst:              100,
		RetryAttempts:          1,
		MaxConcurrentDownloads: 2,
	}
	c := New(m, cfg)
	c.backoffBase = time.Millisecond
	// Pin the chain to the LibGen source so md5 unit tests never fall through to
	// the live randombook provider; source-chain wiring is covered separately by
	// TestNewWiresSourceChainFromConfig and per-source tests inject their own.
	c.sources = []DownloadSource{libgenSource{c: c}}
	return c
}

// TestGetFailsOver verifies GetFailsOver.
func TestGetFailsOver(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("req") != "golang" {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		w.Write([]byte("ok-body"))
	}))
	defer good.Close()

	c := newTestClient(staticMirrors{bad.URL, good.URL})
	body, base, err := c.get(context.Background(), "/index.php", url.Values{"req": {"golang"}})
	if err != nil {
		t.Fatalf("get() error = %v", err)
	}
	if string(body) != "ok-body" || base != good.URL {
		t.Errorf("get() = %q from %q, want ok-body from %q", body, base, good.URL)
	}
}

// TestGetAllMirrorsFailed verifies GetAllMirrorsFailed.
func TestGetAllMirrorsFailed(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer bad.Close()
	c := newTestClient(staticMirrors{bad.URL})
	_, _, err := c.get(context.Background(), "/index.php", nil)
	if !errors.Is(err, ErrAllMirrorsFailed) {
		t.Fatalf("err = %v, want ErrAllMirrorsFailed", err)
	}
}

// TestGetAllMirrorsPermanent: when every mirror returns a permanent error (404),
// the sweep exhausts without any transient failure. The result must NOT be
// classified as ErrAllMirrorsFailed (no "unreachable"/"VPN" alarm), but as
// ErrRequestRejected, and must not carry the connectivity message text.
func TestGetAllMirrorsPermanent(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer second.Close()

	c := newTestClient(staticMirrors{first.URL, second.URL})
	c.retry = 5
	_, _, err := c.get(context.Background(), "/json.php", url.Values{"md5": {"deadbeef"}})
	if err == nil {
		t.Fatal("get() error = nil, want error")
	}
	if errors.Is(err, ErrAllMirrorsFailed) {
		t.Errorf("err = %v, must NOT be ErrAllMirrorsFailed (all permanent, no transient)", err)
	}
	if !errors.Is(err, ErrRequestRejected) {
		t.Errorf("err = %v, want ErrRequestRejected", err)
	}
	for _, bad := range []string{"unreachable", "VPN", "DNS"} {
		if strings.Contains(err.Error(), bad) {
			t.Errorf("err = %q, must not contain connectivity text %q", err, bad)
		}
	}
}

// TestGetRetriesTransient: a mirror returns 503 twice and then 200; with
// RetryAttempts=3 the client must retry until it gets the 200 (3 hits).
func TestGetRetriesTransient(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) < 3 {
			http.Error(w, "temporarily down", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok-body"))
	}))
	defer srv.Close()

	c := newTestClient(staticMirrors{srv.URL})
	c.retry = 3
	body, _, err := c.get(context.Background(), "/index.php", nil)
	if err != nil {
		t.Fatalf("get() error = %v", err)
	}
	if string(body) != "ok-body" {
		t.Errorf("body = %q, want ok-body", body)
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("hits = %d, want 3 (two retries after 503)", got)
	}
}

// TestGetPermanentNoRetry: a 404 is a permanent error; it must not be retried
// even when attempts are available (a single hit) and the error propagates.
func TestGetPermanentNoRetry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := newTestClient(staticMirrors{srv.URL})
	c.retry = 5
	_, _, err := c.get(context.Background(), "/index.php", nil)
	if err == nil {
		t.Fatal("get() error = nil, want error")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("hits = %d, want 1 (no retry on a permanent error)", got)
	}
}

// TestGetFailsOverOnPermanent: a permanent error (404) on the first mirror must
// not abort the sweep; the client fails over to the second mirror, which responds
// 200. The first mirror is queried exactly once (no retry).
func TestGetFailsOverOnPermanent(t *testing.T) {
	var firstHits atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstHits.Add(1)
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok-body"))
	}))
	defer second.Close()

	c := newTestClient(staticMirrors{first.URL, second.URL})
	c.retry = 5
	body, base, err := c.get(context.Background(), "/index.php", nil)
	if err != nil {
		t.Fatalf("get() error = %v, want failover success", err)
	}
	if string(body) != "ok-body" || base != second.URL {
		t.Errorf("get() = %q from %q, want ok-body from %q", body, base, second.URL)
	}
	if got := firstHits.Load(); got != 1 {
		t.Errorf("firstHits = %d, want 1 (permanent: failover without retry)", got)
	}
}

// TestGetContextCanceled: a request whose context is already canceled must fail
// at the limiter wait without reaching any mirror.
func TestGetContextCanceled(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.get(ctx, "/index.php", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("mirror was hit %d times, want 0 (canceled before the request)", got)
	}
}

// TestGetContextCanceledDuringBackoff: after a transient failure the client waits
// a backoff before retrying; a context that expires during that wait aborts with
// the context error rather than looping.
func TestGetContextCanceledDuringBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	c.retry = 3
	c.backoffBase = 500 * time.Millisecond // long enough to outlast the ctx below
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, _, err := c.get(ctx, "/index.php", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded from the backoff wait", err)
	}
}

// TestGetTransientTransportError: a connection error (the server is closed, so
// the port refuses connections) is a transient failure and surfaces as
// ErrAllMirrorsFailed, not ErrRequestRejected.
func TestGetTransientTransportError(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // now deadURL refuses connections: a transport error, not an HTTP status
	c := newTestClient(staticMirrors{deadURL})
	_, _, err := c.get(context.Background(), "/index.php", nil)
	if !errors.Is(err, ErrAllMirrorsFailed) {
		t.Fatalf("err = %v, want ErrAllMirrorsFailed (transport error is transient)", err)
	}
}

// TestGetPermFailedSkippedOnRetry: a permanently-failed mirror (404) must stay
// out of later retry passes triggered by a second mirror's transient failure.
// The permanent mirror is queried exactly once even though a retry pass runs.
func TestGetPermFailedSkippedOnRetry(t *testing.T) {
	var permHits, transHits atomic.Int32
	perm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		permHits.Add(1)
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer perm.Close()
	trans := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		transHits.Add(1)
		http.Error(w, "temporarily down", http.StatusServiceUnavailable)
	}))
	defer trans.Close()

	c := newTestClient(staticMirrors{perm.URL, trans.URL})
	c.retry = 3
	if _, _, err := c.get(context.Background(), "/index.php", nil); err == nil {
		t.Fatal("get() should fail when one mirror is permanent and the other is transient")
	}
	if got := permHits.Load(); got != 1 {
		t.Errorf("permanent mirror hits = %d, want 1 (skipped on retry passes)", got)
	}
	if got := transHits.Load(); got < 2 {
		t.Errorf("transient mirror hits = %d, want >= 2 (retried across passes)", got)
	}
}

// TestWithEnrichBaseURLs verifies the WithEnrichBaseURLs option routes Enrich's
// Crossref and OpenLibrary lookups at the per-client override URLs (a test seam
// independent of the package-level defaults), exercising both the option and the
// crossrefURL/openLibraryURL override branches. The Crossref and OpenLibrary
// fixtures/handlers are shared with enrich_test.go.
func TestWithEnrichBaseURLs(t *testing.T) {
	crSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(crossrefFixture))
	}))
	defer crSrv.Close()
	olSrv := httptest.NewServer(olHandler(`"A tale of the override seam."`))
	defer olSrv.Close()

	c := New(staticMirrors{}, baseChainConfig(), WithEnrichBaseURLs(crSrv.URL, olSrv.URL))
	e := c.Enrich(context.Background(), "10.1000/x", "9780000000001")
	if e == nil || e.Crossref == nil || e.OpenLibrary == nil {
		t.Fatalf("Enrich() = %+v, want both Crossref and OpenLibrary via the override URLs", e)
	}
	if !strings.HasPrefix(e.OpenLibrary.OpenLibURL, olSrv.URL) {
		t.Errorf("OpenLibURL = %q, want it built from the override base %q", e.OpenLibrary.OpenLibURL, olSrv.URL)
	}
}

// TestWithSourcesOverridesChain verifies the WithSources option replaces the
// config-built download-source chain verbatim and in order.
func TestWithSourcesOverridesChain(t *testing.T) {
	cfg := &config.Config{
		Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1,
		MaxConcurrentDownloads: 1, UnpaywallEmail: "mail@jmrp.io", ScihubHosts: []string{"sci-hub.se"},
	}
	a := stubSource{name: "alpha", supports: true}
	b := stubSource{name: "beta", supports: true}
	c := New(staticMirrors{}, cfg, WithSources(a, b))
	if got := sourceNames(c); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Errorf("chain = %v, want [alpha beta]", got)
	}
}

// TestCooldownSkip: after failing once, the bad mirror enters cooldown; the
// second call must skip it and not query it again (bad mirror hits == 1).
func TestCooldownSkip(t *testing.T) {
	var badHits, goodHits atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		badHits.Add(1)
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		goodHits.Add(1)
		w.Write([]byte("ok"))
	}))
	defer good.Close()

	c := newTestClient(staticMirrors{bad.URL, good.URL})
	if _, _, err := c.get(context.Background(), "/x", nil); err != nil {
		t.Fatalf("first get error = %v", err)
	}
	if got := badHits.Load(); got != 1 {
		t.Fatalf("badHits after first call = %d, want 1", got)
	}

	if _, _, err := c.get(context.Background(), "/x", nil); err != nil {
		t.Fatalf("second get error = %v", err)
	}
	if got := badHits.Load(); got != 1 {
		t.Errorf("badHits after second call = %d, want 1 (cooldown skip)", got)
	}
	if got := goodHits.Load(); got != 2 {
		t.Errorf("goodHits = %d, want 2", got)
	}
}

// TestDoRequestBuildError covers doRequest's request-construction failure: a mirror
// base carrying a control character cannot be built into a request.
func TestDoRequestBuildError(t *testing.T) {
	c := newTestClient(staticMirrors{"http://\x7f"})
	if _, _, err := c.get(context.Background(), "/index.php", nil); err == nil {
		t.Error("get should fail when the request URL is invalid")
	}
}

// TestDoRequestBodyReadError covers doRequest's 200-with-read-error branch: a
// mirror that declares more bytes than it sends, then closes, makes reading the
// body fail even though the status is 200.
func TestDoRequestBodyReadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\nshort"))
		_ = conn.Close()
	}))
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	if _, _, err := c.get(context.Background(), "/index.php", nil); err == nil {
		t.Error("get should fail when a 200 body cannot be fully read")
	}
}

// sourceNames returns the Name() of every source in the client's chain, in order.
func sourceNames(c *Client) []string {
	names := make([]string, 0, len(c.sources))
	for _, s := range c.sources {
		names = append(names, s.Name())
	}
	return names
}

// baseChainConfig is a minimal config for exercising New's source wiring.
func baseChainConfig() *config.Config {
	return &config.Config{
		Timeout:                5 * time.Second,
		RateRPS:                1000,
		RateBurst:              100,
		RetryAttempts:          1,
		MaxConcurrentDownloads: 2,
		UnpaywallEmail:         "mail@jmrp.io",
		ScihubHosts:            []string{"sci-hub.se"},
	}
}

// TestNewWiresSourceChainFromConfig verifies New assembles the full ordered chain
// [unpaywall, scihub, libgen, randombook] and that Supports filters it into the
// right per-item order: articles get [unpaywall, scihub], books get
// [libgen, randombook].
func TestNewWiresSourceChainFromConfig(t *testing.T) {
	c := New(staticMirrors{}, baseChainConfig())

	if got, want := sourceNames(c), []string{"unpaywall", "scihub", "scidb", "libgen", "randombook"}; !slices.Equal(got, want) {
		t.Fatalf("chain = %v, want %v", got, want)
	}

	var book, article []string
	for _, s := range c.sources {
		if s.Supports(Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}) {
			book = append(book, s.Name())
		}
		if s.Supports(Item{DOI: "10.1/x"}) {
			article = append(article, s.Name())
		}
	}
	if want := []string{"libgen", "randombook"}; !slices.Equal(book, want) {
		t.Errorf("book chain = %v, want %v", book, want)
	}
	if want := []string{"unpaywall", "scihub", "scidb"}; !slices.Equal(article, want) {
		t.Errorf("article chain = %v, want %v", article, want)
	}
}

// TestEnabledSourceNames verifies EnabledSourceNames splits the enabled chain
// into book (md5) and article (doi) sources, and that an empty unpaywall email
// drops unpaywall from the article list.
func TestEnabledSourceNames(t *testing.T) {
	book, article := New(staticMirrors{}, baseChainConfig()).EnabledSourceNames()
	if want := []string{"libgen", "randombook"}; !slices.Equal(book, want) {
		t.Errorf("book = %v, want %v", book, want)
	}
	if want := []string{"unpaywall", "scihub", "scidb"}; !slices.Equal(article, want) {
		t.Errorf("article = %v, want %v", article, want)
	}

	noEmail := baseChainConfig()
	noEmail.UnpaywallEmail = ""
	book, article = New(staticMirrors{}, noEmail).EnabledSourceNames()
	if want := []string{"libgen", "randombook"}; !slices.Equal(book, want) {
		t.Errorf("book (no email) = %v, want %v", book, want)
	}
	if want := []string{"scihub", "scidb"}; !slices.Equal(article, want) {
		t.Errorf("article (no email) = %v, want %v", article, want)
	}
}

// TestNewSourcesFilter verifies LIBGEN_MCP_SOURCES disables sources by name while
// preserving the relative order of the remaining ones.
func TestNewSourcesFilter(t *testing.T) {
	cfg := baseChainConfig()
	cfg.Sources = []string{"libgen", "unpaywall"}
	c := New(staticMirrors{}, cfg)

	if got, want := sourceNames(c), []string{"unpaywall", "libgen"}; !slices.Equal(got, want) {
		t.Errorf("filtered chain = %v, want %v", got, want)
	}
}
