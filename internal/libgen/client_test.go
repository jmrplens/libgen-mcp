package libgen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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
