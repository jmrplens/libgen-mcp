package libgen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

type staticMirrors []string

func (s staticMirrors) Mirrors(context.Context) []string { return s }

// newTestClient construye un Client con defaults sanos para tests: rate limiter
// muy alto (sin espera), un solo intento por defecto y backoff casi nulo. Los
// tests que ejercitan el retry sobrescriben c.retry.
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
		t.Errorf("get() = %q desde %q, esperaba ok-body desde %q", body, base, good.URL)
	}
}

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

// TestGetRetriesTransient: un mirror devuelve 503 dos veces y luego 200; con
// RetryAttempts=3 el cliente debe reintentar hasta lograr el 200 (3 hits).
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
		t.Errorf("hits = %d, want 3 (dos reintentos tras 503)", got)
	}
}

// TestGetPermanentNoRetry: un 404 es un error permanente; no debe reintentarse
// aunque haya intentos disponibles (un solo hit) y el error se propaga.
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
		t.Errorf("hits = %d, want 1 (sin reintento en error permanente)", got)
	}
}

// TestGetFailsOverOnPermanent: un error permanente (404) en el primer mirror no
// debe abortar el sweep; el cliente hace failover al segundo mirror, que responde
// 200. El primer mirror se consulta exactamente una vez (sin reintento).
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
		t.Errorf("get() = %q desde %q, esperaba ok-body desde %q", body, base, second.URL)
	}
	if got := firstHits.Load(); got != 1 {
		t.Errorf("firstHits = %d, want 1 (permanente: failover sin reintento)", got)
	}
}

// TestCooldownSkip: tras fallar una vez, el mirror malo entra en cooldown; la
// segunda llamada debe saltarlo y no volver a consultarlo (hits del malo == 1).
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
