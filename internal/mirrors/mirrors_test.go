package mirrors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseFixture(t *testing.T) {
	f, err := os.Open("testdata/shadowlibraries.html")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := Parse(f)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := []string{"https://libgen.li", "https://libgen.vg", "https://libgen.la", "https://libgen.bz", "https://libgen.gl"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse() = %v, want %v", got, want)
	}
}

func TestParseNoMirrors(t *testing.T) {
	if _, err := Parse(strings.NewReader("<html><body>nada</body></html>")); err == nil {
		t.Fatal("Parse() sin mirrors debería fallar")
	}
}

func TestOrderPreferred(t *testing.T) {
	list := []string{"https://libgen.li", "https://libgen.vg"}
	got := orderPreferred(list, "https://libgen.vg")
	if got[0] != "https://libgen.vg" || len(got) != 2 {
		t.Errorf("orderPreferred() = %v", got)
	}
	// preferido ausente de la lista: se añade delante
	got = orderPreferred(list, "https://libgen.example")
	if got[0] != "https://libgen.example" || len(got) != 3 {
		t.Errorf("orderPreferred() con preferido nuevo = %v", got)
	}
}

func TestManagerFetchesAndCaches(t *testing.T) {
	page, _ := os.ReadFile("testdata/shadowlibraries.html")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(page)
	}))
	defer srv.Close()
	cachePath := filepath.Join(t.TempDir(), "mirrors.json")
	m := &Manager{SourceURL: srv.URL, CachePath: cachePath, Preferred: "https://libgen.li", HTTP: srv.Client()}
	got := m.Mirrors(context.Background())
	if len(got) != 5 || got[0] != "https://libgen.li" {
		t.Fatalf("Mirrors() = %v", got)
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("caché no escrita: %v", err)
	}
}

func TestManagerUsesStaleCacheWhenSourceDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	cachePath := filepath.Join(t.TempDir(), "mirrors.json")
	stale := cacheFile{FetchedAt: time.Now().Add(-48 * time.Hour), Mirrors: []string{"https://libgen.la"}}
	data, _ := json.Marshal(stale)
	os.WriteFile(cachePath, data, 0o644)
	m := &Manager{SourceURL: srv.URL, CachePath: cachePath, Preferred: "https://libgen.la", HTTP: srv.Client()}
	got := m.Mirrors(context.Background())
	if got[0] != "https://libgen.la" {
		t.Errorf("Mirrors() con caché caducada = %v, esperaba usarla", got)
	}
}

// TestManagerRediscoversAfterFallback prueba que un fallback transitorio no queda
// anclado en memoria: si la primera llamada cae al fallback (fuente caída), una
// segunda llamada con la fuente ya recuperada devuelve la lista descubierta en vivo.
func TestManagerRediscoversAfterFallback(t *testing.T) {
	page, _ := os.ReadFile("testdata/shadowlibraries.html")
	var up bool // arranca caído
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Write(page)
	}))
	defer srv.Close()
	cachePath := filepath.Join(t.TempDir(), "mirrors.json")
	m := &Manager{SourceURL: srv.URL, CachePath: cachePath, Preferred: "https://libgen.li", HTTP: srv.Client()}

	// Primera llamada: fuente caída, sin caché -> fallback hardcodeado.
	got := m.Mirrors(context.Background())
	if !reflect.DeepEqual(got, DefaultFallback) {
		t.Fatalf("primera llamada = %v, esperaba fallback %v", got, DefaultFallback)
	}
	if !m.cachedFromFallback {
		t.Fatalf("el fallback debería marcarse como cachedFromFallback")
	}

	// La fuente se recupera; la segunda llamada NO debe reutilizar el fallback anclado.
	up = true
	got = m.Mirrors(context.Background())
	if len(got) != 5 || got[0] != "https://libgen.li" {
		t.Fatalf("segunda llamada tras recuperación = %v, esperaba lista descubierta", got)
	}
	if m.cachedFromFallback {
		t.Fatalf("una discovery viva no debería marcarse como fallback")
	}
}

// TestManagerRediscoversAfterTTL prueba que, aun tras memoizar una discovery viva,
// el resultado en memoria se refresca cuando supera cacheTTL (servidor de larga vida).
func TestManagerRediscoversAfterTTL(t *testing.T) {
	page, _ := os.ReadFile("testdata/shadowlibraries.html")
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write(page)
	}))
	defer srv.Close()
	cachePath := filepath.Join(t.TempDir(), "mirrors.json")
	m := &Manager{SourceURL: srv.URL, CachePath: cachePath, Preferred: "https://libgen.li", HTTP: srv.Client()}

	_ = m.Mirrors(context.Background())
	if hits != 1 {
		t.Fatalf("primera llamada: hits = %d, esperaba 1", hits)
	}
	// Dentro de TTL: usa memoria, sin nuevo fetch.
	_ = m.Mirrors(context.Background())
	if hits != 1 {
		t.Fatalf("dentro de TTL: hits = %d, no debería refetchear", hits)
	}
	// Envejecemos la memoria y la caché de disco mas allá de TTL (white-box).
	m.cachedAt = time.Now().Add(-2 * cacheTTL)
	stale := cacheFile{FetchedAt: time.Now().Add(-2 * cacheTTL), Mirrors: []string{"https://libgen.la"}}
	data, _ := json.Marshal(stale)
	os.WriteFile(cachePath, data, 0o644)
	_ = m.Mirrors(context.Background())
	if hits != 2 {
		t.Fatalf("tras expirar TTL: hits = %d, esperaba re-discovery (2)", hits)
	}
}

func TestManagerFallsBackToHardcoded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	m := &Manager{SourceURL: srv.URL, CachePath: filepath.Join(t.TempDir(), "mirrors.json"), Preferred: "https://libgen.li", HTTP: srv.Client()}
	got := m.Mirrors(context.Background())
	if !reflect.DeepEqual(got, DefaultFallback) {
		t.Errorf("Mirrors() = %v, want fallback %v", got, DefaultFallback)
	}
}
