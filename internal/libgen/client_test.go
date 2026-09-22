package libgen

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	attributekey "go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/jmrplens/libgen-mcp/v2/internal/config"
	"github.com/jmrplens/libgen-mcp/v2/internal/mcpotel"
	"github.com/jmrplens/libgen-mcp/v2/internal/netguard"
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
		t.Run(bad, func(t *testing.T) {
			if strings.Contains(err.Error(), bad) {
				t.Errorf("err = %q, must not contain connectivity text %q", err, bad)
			}
		})
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
// in config.KnownSources order (the keyless open-access sources are wired, core is
// left out for want of a key) and that Supports filters it into the right per-item
// order: articles get the doi sources, md5 books the shadow libraries, and ISBN
// books the open-access book sources.
func TestNewWiresSourceChainFromConfig(t *testing.T) {
	c := New(staticMirrors{}, baseChainConfig())

	wantChain := []string{
		"unpaywall", "openalex", "europepmc", "biorxiv", "rfc", "nist", "dagstuhl", "acl", "zenodo",
		"scielo", "fao", "fatcat", "crossref", "oapen", "archive",
		"scihub", "scidb", "libgen", "randombook", "annas",
	}
	if got := sourceNames(c); !slices.Equal(got, wantChain) {
		t.Fatalf("chain = %v, want %v", got, wantChain)
	}

	var book, article, isbn []string
	for _, s := range c.sources {
		if s.Supports(Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}) {
			book = append(book, s.Name())
		}
		// A preprint DOI is a valid DOI every prefix-agnostic article source accepts,
		// so it counts the prefix-restricted biorxiv source alongside them. The other
		// prefix-restricted sources (rfc, nist, dagstuhl, acl, zenodo, scielo, fao) claim
		// only their
		// own registrants and are correctly absent here; EnabledSourceNames probes each
		// prefix in turn.
		if s.Supports(Item{DOI: "10.1101/2020.01.01.000000"}) {
			article = append(article, s.Name())
		}
		if s.Supports(Item{ISBN: "9789286150616"}) {
			isbn = append(isbn, s.Name())
		}
	}
	if want := []string{"libgen", "randombook", "annas"}; !slices.Equal(book, want) {
		t.Errorf("book chain = %v, want %v", book, want)
	}
	// oapen claims monograph DOIs too, and sits ahead of the shadow libraries.
	if want := []string{"unpaywall", "openalex", "europepmc", "biorxiv", "fatcat", "crossref", "oapen", "scihub", "scidb"}; !slices.Equal(article, want) {
		t.Errorf("article chain = %v, want %v", article, want)
	}
	if want := []string{"oapen", "archive"}; !slices.Equal(isbn, want) {
		t.Errorf("isbn chain = %v, want %v", isbn, want)
	}
}

// TestNewWiresCoreWhenKeyed verifies the opt-in CORE source joins the article chain
// once an API key is configured, in its canonical position before scihub.
func TestNewWiresCoreWhenKeyed(t *testing.T) {
	cfg := baseChainConfig()
	cfg.CoreKey = "test-key"
	_, article := New(staticMirrors{}, cfg).EnabledSourceNames()
	want := []string{
		"unpaywall", "openalex", "europepmc", "biorxiv", "rfc", "nist", "dagstuhl", "acl", "zenodo",
		"scielo", "fao", "fatcat", "core", "crossref", "oapen", "scihub", "scidb",
	}
	if !slices.Equal(article, want) {
		t.Errorf("article chain (keyed) = %v, want %v", article, want)
	}
}

// TestEnabledSourceNames verifies EnabledSourceNames splits the enabled chain
// into book (md5) and article (doi) sources, and that an empty unpaywall email
// drops unpaywall from the article list.
func TestEnabledSourceNames(t *testing.T) {
	book, article := New(staticMirrors{}, baseChainConfig()).EnabledSourceNames()
	if want := []string{"libgen", "randombook", "annas"}; !slices.Equal(book, want) {
		t.Errorf("book = %v, want %v", book, want)
	}
	if want := []string{
		"unpaywall", "openalex", "europepmc", "biorxiv", "rfc", "nist", "dagstuhl", "acl", "zenodo",
		"scielo", "fao", "fatcat", "crossref", "oapen", "scihub", "scidb",
	}; !slices.Equal(article, want) {
		t.Errorf("article = %v, want %v", article, want)
	}

	noEmail := baseChainConfig()
	noEmail.UnpaywallEmail = ""
	book, article = New(staticMirrors{}, noEmail).EnabledSourceNames()
	if want := []string{"libgen", "randombook", "annas"}; !slices.Equal(book, want) {
		t.Errorf("book (no email) = %v, want %v", book, want)
	}
	if want := []string{
		"openalex", "europepmc", "biorxiv", "rfc", "nist", "dagstuhl", "acl", "zenodo",
		"scielo", "fao", "fatcat", "crossref", "oapen", "scihub", "scidb",
	}; !slices.Equal(article, want) {
		t.Errorf("article (no email) = %v, want %v", article, want)
	}
}

// TestEnabledISBNSources verifies the ISBN-keyed sources are reported separately
// and in chain order, and that disabling them empties the list rather than folding
// them into the md5 book chain.
func TestEnabledISBNSources(t *testing.T) {
	got := New(staticMirrors{}, baseChainConfig()).EnabledISBNSources()
	if want := []string{"oapen", "archive"}; !slices.Equal(got, want) {
		t.Errorf("EnabledISBNSources() = %v, want %v", got, want)
	}

	md5Only := baseChainConfig()
	md5Only.Sources = []string{"libgen"}
	if none := New(staticMirrors{}, md5Only).EnabledISBNSources(); len(none) != 0 {
		t.Errorf("EnabledISBNSources() with only libgen enabled = %v, want none", none)
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

// TestChainMirrorErrorsWithNoFailures covers the case errors.Join produces a nil
// from: an empty mirror list means nothing was tried, so there are no per-mirror
// errors to chain.
//
// Before this, the nil went to a %w verb and fmt rendered the literal
// "%!w(<nil>)" into the message — which says the formatting broke rather than
// what happened, in a string that reaches the operator's log and the model's
// transcript alike. It was found by the platform matrix, in the output of an
// unrelated Windows failure.
func TestChainMirrorErrorsWithNoFailures(t *testing.T) {
	for _, sentinel := range []error{ErrRequestRejected, ErrAllMirrorsFailed} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			err := chainMirrorErrors(sentinel, nil)

			if !errors.Is(err, sentinel) {
				t.Errorf("errors.Is(err, %v) = false; the sentinel a caller matches on was lost", sentinel)
			}
			if strings.Contains(err.Error(), "%!w") {
				t.Errorf("err = %q, want a message rather than a formatting complaint", err)
			}
			if !strings.Contains(err.Error(), "empty") {
				t.Errorf("err = %q, want it to say why there are no per-mirror errors", err)
			}
		})
	}
}

// TestChainMirrorErrorsKeepsTheCauses pins the ordinary path: with per-mirror
// failures, both the sentinel and every cause stay matchable.
func TestChainMirrorErrorsKeepsTheCauses(t *testing.T) {
	first := errors.New("mirror one refused")
	second := errors.New("mirror two timed out")

	err := chainMirrorErrors(ErrAllMirrorsFailed, errors.Join(first, second))

	for _, want := range []error{ErrAllMirrorsFailed, first, second} {
		t.Run(want.Error(), func(t *testing.T) {
			if !errors.Is(err, want) {
				t.Errorf("errors.Is(err, %v) = false", want)
			}
		})
	}
}

// TestGetWithNoMirrorsReportsCleanly drives the real path rather than the helper,
// because the helper is only correct if get actually routes through it.
func TestGetWithNoMirrorsReportsCleanly(t *testing.T) {
	c := newTestClient(staticMirrors{})

	_, _, err := c.get(context.Background(), "/json.php", nil)
	if err == nil {
		t.Fatal("get() with no mirrors = nil, want an error")
	}
	if strings.Contains(err.Error(), "%!w") {
		t.Errorf("err = %q, want a message rather than a formatting complaint", err)
	}
	if !errors.Is(err, ErrRequestRejected) {
		t.Errorf("err = %v, want it to wrap ErrRequestRejected", err)
	}
}

// TestClientsCarryTheOperatorNamedDestinations pins the wiring this package owes
// the destination guard.
//
// Both clients matter and for the same reason: c.http asks the mirror questions
// and c.dl streams files from it, and each also reaches URLs a third party
// deposited. One boolean could not tell those apart, which is why the policy
// travels per request — but it only travels if New hands the operator-named set
// in, and that is what this asserts.
//
// The suite lifts the private tier for its own loopback fixtures (see TestMain),
// so this test puts it back for the clients it builds. Without that it would
// pass whatever New did, which is no test at all.
func TestClientsCarryTheOperatorNamedDestinations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	restore := netguard.SetAllowPrivateForTest(false)
	defer restore()

	named := New(staticMirrors{srv.URL}, &config.Config{Mirror: srv.URL, Timeout: 5 * time.Second})
	for _, tc := range []struct {
		name   string
		client *http.Client
	}{
		{name: "http", client: named.http},
		{name: "dl", client: named.dl},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := fetchThrough(t, tc.client, srv.URL)
			if err != nil {
				t.Errorf("%s client could not reach the configured mirror: %v", tc.name, err)
				return
			}
			_ = resp.Body.Close()
		})
	}

	// The same address, from a deployment that configured no mirror at all, is
	// the class the guard exists for and stays refused.
	anonymous := New(staticMirrors{srv.URL}, &config.Config{Timeout: 5 * time.Second})
	resp, err := fetchThrough(t, anonymous.http, srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a private address nobody configured was reached")
	}
	if !errors.Is(err, netguard.ErrBlockedAddress) {
		t.Errorf("err = %v, want netguard.ErrBlockedAddress", err)
	}
}

// fetchThrough issues a context-carrying GET on the given client.
func fetchThrough(t *testing.T, c *http.Client, rawURL string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		t.Fatalf("NewRequestWithContext(%q) error = %v", rawURL, err)
	}
	return c.Do(req)
}

// collectResourceMetrics runs work against a fresh meter provider and returns
// what one collection gathered.
//
// The registrations are removed afterwards, which is not tidiness: a callback
// holds the client it reads and the provider holds the callback, so a
// registration left in place has every later test's collection reporting on this
// test's client.
func collectResourceMetrics(t *testing.T, work func()) metricdata.ResourceMetrics {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		mcpotel.UnobserveAll()
		otel.SetMeterProvider(previous)
	})

	work()

	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatalf("collecting: %v", err)
	}
	return metrics
}

// gaugeValue returns the single value of a named observable gauge.
//
// More than one point means two resources answered under one name, which is the
// registration leak [mcpotel.ObserveBounded] exists to prevent, so it fails here
// rather than picking one of them.
func gaugeValue(t *testing.T, metrics metricdata.ResourceMetrics, name string) int64 {
	t.Helper()

	for _, scope := range metrics.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			data, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("%s is %T, want an int64 gauge", name, m.Data)
			}
			if len(data.DataPoints) != 1 {
				t.Fatalf("%s has %d points, want one", name, len(data.DataPoints))
			}
			return data.DataPoints[0].Value
		}
	}
	t.Fatalf("no gauge named %s was collected", name)
	return 0
}

// counterValues returns a named counter's points keyed by one dimension.
func counterValues(t *testing.T, metrics metricdata.ResourceMetrics, name, dimension string) map[string]int64 {
	t.Helper()

	out := make(map[string]int64)
	for _, scope := range metrics.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			data, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s is %T, want an int64 sum", name, m.Data)
			}
			for _, point := range data.DataPoints {
				key := ""
				if value, present := point.Attributes.Value(attributekey.Key(dimension)); present {
					key = value.String()
				}
				out[key] += point.Value
			}
		}
	}
	return out
}

// namedSource is a source these tests only ever name: they measure which sources
// the chain set aside, never what one resolved.
func namedSource(name string) countingSource {
	return countingSource{name: name, attempts: new(atomic.Int32)}
}

// TestTheClientsGaugesReadTheResourceTheyName is what makes the three pairs worth
// publishing: each one has to be wired to its own resource and to its own bound.
//
// Three resources with the same shape are three chances to read the wrong one,
// and every such mistake produces a plausible number — a capacity where a current
// value belongs reports a cache that is permanently full, and reads as an
// exhausted deployment on every dashboard built on it.
func TestTheClientsGaugesReadTheResourceTheyName(t *testing.T) {
	metrics := collectResourceMetrics(t, func() {
		cfg := &config.Config{
			Timeout:                time.Second,
			RateRPS:                1000,
			RateBurst:              100,
			RetryAttempts:          1,
			MaxConcurrentDownloads: 3,
			ReadCacheBytes:         4096,
			ReadCacheTTL:           time.Hour,
		}
		c := New(staticMirrors{}, cfg)
		// Two named sources, so the cooldown table's denominator is the chain
		// length rather than whatever the ambient configuration assembled.
		c.sources = []DownloadSource{
			namedSource("one"),
			namedSource("two"),
		}

		// One cached file of a known size, one download slot taken, one source
		// set aside: every number below is distinct from every other, so a
		// crossed pair cannot pass.
		c.tempCache.entries["md5-a"] = &tempEntry{path: "unread", size: 1200, refs: 1, atime: time.Now()}
		c.dlSem <- struct{}{}
		c.markSourceCooldown("one")
	})

	for _, tc := range []struct {
		name string
		want int64
	}{
		{mcpotel.InstrumentReadCacheBytes, 1200},
		{mcpotel.InstrumentReadCacheCapacity, 4096},
		{mcpotel.InstrumentDownloadsInflight, 1},
		{mcpotel.InstrumentDownloadsCapacity, 3},
		{mcpotel.InstrumentSourceCooldownEntries, 1},
		{mcpotel.InstrumentSourceCooldownCapacity, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := gaugeValue(t, metrics, tc.name); got != tc.want {
				t.Errorf("%s = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}

// TestTheCooldownBypassIsCountedOncePerChainRunAndByKind covers the one cooldown
// value worth alerting on.
//
// One source in cooldown is routing working; every capable source in cooldown is
// an outage wearing routing's clothes, and the chain tries them anyway. Counting
// it once per run rather than once per cooled source is what keeps a long chain
// from looking worse than a short one for the same outage, and the kind is what
// says which of the two chains — md5 or DOI — is the one that is down.
func TestTheCooldownBypassIsCountedOncePerChainRunAndByKind(t *testing.T) {
	metrics := collectResourceMetrics(t, func() {
		chain := []DownloadSource{
			namedSource("a"),
			namedSource("b"),
		}
		c := cooldownChainClient(chain...)
		c.markSourceCooldown("a")
		c.markSourceCooldown("b")

		if got := srcNames(c.eligibleSources(t.Context(), Item{MD5: testMD5}, chain)); len(got) != 2 {
			t.Fatalf("eligible sources = %v, want the whole chain: nothing was bypassed, so nothing is being counted", got)
		}
		// A second md5 run and a single DOI one. The counts are deliberately
		// unequal: two runs of one kind and one of the other is what tells a
		// dimension that is merely present from one that is right, and an
		// inverted kind — the md5 chain reported as the DOI chain — reads as a
		// perfectly plausible outage on the wrong half of the server.
		c.eligibleSources(t.Context(), Item{MD5: testMD5}, chain)
		c.eligibleSources(t.Context(), Item{DOI: "10.1000/xyz123"}, chain)
	})

	got := counterValues(t, metrics, mcpotel.InstrumentSourceCooldownBypassed, "kind")
	if got["book"] != 2 || got["article"] != 1 {
		t.Errorf("bypasses = %v, want two book runs and one article run: two cooled sources are one outage, not two", got)
	}
}

// TestARoutedChainRunCountsNoBypass is the other half, and the one that keeps the
// counter alertable: a source passed over while another can still serve is the
// feature working.
func TestARoutedChainRunCountsNoBypass(t *testing.T) {
	metrics := collectResourceMetrics(t, func() {
		chain := []DownloadSource{
			namedSource("cooled"),
			namedSource("live"),
		}
		c := cooldownChainClient(chain...)
		c.markSourceCooldown("cooled")

		if got := srcNames(c.eligibleSources(t.Context(), Item{MD5: testMD5}, chain)); len(got) != 1 {
			t.Fatalf("eligible sources = %v, want only the live one", got)
		}
	})

	if got := counterValues(t, metrics, mcpotel.InstrumentSourceCooldownBypassed, "kind"); len(got) != 0 {
		t.Errorf("bypasses = %v, want none: one source in cooldown is the chain routing around it", got)
	}
}

// TestReadCacheEvictionsCarryTheReasonThatCausedThem is the counter's whole
// point.
//
// A total says a cache is evicting; the reason says which knob is wrong. A cache
// full because nothing expires wants a shorter TTL and a cache full because it is
// busy wants more bytes, and the two are indistinguishable without this
// dimension — so a reason recorded on the wrong path is worse than no counter.
func TestReadCacheEvictionsCarryTheReasonThatCausedThem(t *testing.T) {
	underCap, size := writeTempFile(t, "small enough to keep")
	overCap, overSize := writeTempFile(t, "more bytes than the cap allows")

	metrics := collectResourceMetrics(t, func() {
		// ttl=0 expires anything unreferenced; the cap is far away, so only the
		// TTL pass can be what removes it.
		lapsing := newTempCache(1<<30, 0)
		lapsing.put(t.Context(), "idle", underCap, size)
		lapsing.release("idle")
		lapsing.evict(t.Context())

		// The mirror image: an hour of TTL, and a cap the single entry is over.
		full := newTempCache(overSize-1, time.Hour)
		full.put(t.Context(), "big", overCap, overSize)
		full.release("big")
		full.evict(t.Context())
	})

	got := counterValues(t, metrics, mcpotel.InstrumentReadCacheEvictions, "reason")
	if got["ttl"] != 1 || got["size_pressure"] != 1 {
		t.Errorf("read-cache evictions = %v, want one of each reason", got)
	}
}
