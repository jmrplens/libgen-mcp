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
