package mirrors

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

// errReader is an io.Reader that always fails, used to exercise the html.Parse
// error path in Parse.
type errReader struct{}

// Read always returns an error so html.Parse propagates a read failure.
func (errReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }

// TestParseFixture verifies ParseFixture.
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

// TestParseNoMirrors verifies ParseNoMirrors.
func TestParseNoMirrors(t *testing.T) {
	if _, err := Parse(strings.NewReader("<html><body>nada</body></html>")); err == nil {
		t.Fatal("Parse() without mirrors should fail")
	}
}

// TestParseReadError verifies that Parse surfaces a reader failure instead of
// returning mirrors.
func TestParseReadError(t *testing.T) {
	if _, err := Parse(errReader{}); err == nil {
		t.Fatal("Parse() with a failing reader should error")
	}
}

// TestOrderPreferred verifies OrderPreferred.
func TestOrderPreferred(t *testing.T) {
	list := []string{"https://libgen.li", "https://libgen.vg"}
	got := orderPreferred(list, "https://libgen.vg")
	if got[0] != "https://libgen.vg" || len(got) != 2 {
		t.Errorf("orderPreferred() = %v", got)
	}
	// preferred absent from the list: it is prepended
	got = orderPreferred(list, "https://libgen.example")
	if got[0] != "https://libgen.example" || len(got) != 3 {
		t.Errorf("orderPreferred() with a new preferred = %v", got)
	}
}

// TestManagerFetchesAndCaches verifies ManagerFetchesAndCaches.
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
		t.Fatalf("cache not written: %v", err)
	}
}

// TestManagerUsesStaleCacheWhenSourceDown verifies ManagerUsesStaleCacheWhenSourceDown.
func TestManagerUsesStaleCacheWhenSourceDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	cachePath := filepath.Join(t.TempDir(), "mirrors.json")
	stale := cacheFile{FetchedAt: time.Now().Add(-48 * time.Hour), Mirrors: []string{"https://libgen.la"}}
	data, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(cachePath, data, 0o600)
	m := &Manager{SourceURL: srv.URL, CachePath: cachePath, Preferred: "https://libgen.la", HTTP: srv.Client()}
	got := m.Mirrors(context.Background())
	if got[0] != "https://libgen.la" {
		t.Errorf("Mirrors() with an expired cache = %v, want it to be used", got)
	}
}

// TestManagerRediscoversAfterFallback checks that a transient fallback does not
// stay pinned in memory: if the first call falls back (source down), a second
// call with the source recovered returns the live-discovered list.
func TestManagerRediscoversAfterFallback(t *testing.T) {
	page, _ := os.ReadFile("testdata/shadowlibraries.html")
	var up bool // starts down
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

	// First call: source down, no cache -> hardcoded fallback.
	got := m.Mirrors(context.Background())
	if !reflect.DeepEqual(got, DefaultFallback) {
		t.Fatalf("first call = %v, want fallback %v", got, DefaultFallback)
	}
	if !m.cachedFromFallback {
		t.Fatalf("the fallback should be marked as cachedFromFallback")
	}

	// The source recovers; the second call must NOT reuse the pinned fallback.
	up = true
	got = m.Mirrors(context.Background())
	if len(got) != 5 || got[0] != "https://libgen.li" {
		t.Fatalf("second call after recovery = %v, want discovered list", got)
	}
	if m.cachedFromFallback {
		t.Fatalf("a live discovery should not be marked as fallback")
	}
}

// TestManagerRediscoversAfterTTL checks that, even after memoizing a live discovery,
// the in-memory result is refreshed once it exceeds cacheTTL (long-lived server).
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
		t.Fatalf("first call: hits = %d, want 1", hits)
	}
	// Within TTL: uses memory, no new fetch.
	_ = m.Mirrors(context.Background())
	if hits != 1 {
		t.Fatalf("within TTL: hits = %d, should not refetch", hits)
	}
	// Age the in-memory result and the disk cache beyond TTL (white-box).
	m.cachedAt = time.Now().Add(-2 * cacheTTL)
	stale := cacheFile{FetchedAt: time.Now().Add(-2 * cacheTTL), Mirrors: []string{"https://libgen.la"}}
	data, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(cachePath, data, 0o600)
	_ = m.Mirrors(context.Background())
	if hits != 2 {
		t.Fatalf("after TTL expiry: hits = %d, want re-discovery (2)", hits)
	}
}

// TestNewManager verifies the Manager constructor: the preferred mirror comes
// from the config when set, and defaults to DefaultPreferred when the config
// leaves it empty.
func TestNewManager(t *testing.T) {
	m, err := NewManager(&config.Config{Mirror: "https://libgen.la", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if m.Preferred != "https://libgen.la" {
		t.Errorf("Preferred = %q, want the configured mirror", m.Preferred)
	}
	if m.SourceURL != DefaultSourceURL {
		t.Errorf("SourceURL = %q, want %q", m.SourceURL, DefaultSourceURL)
	}
	if m.CachePath == "" {
		t.Error("CachePath is empty, want a path under the OS cache dir")
	}

	def, err := NewManager(&config.Config{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if def.Preferred != DefaultPreferred {
		t.Errorf("Preferred = %q, want DefaultPreferred %q", def.Preferred, DefaultPreferred)
	}
}

// TestManagerFallsBackToHardcoded verifies ManagerFallsBackToHardcoded.
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

// TestManagerUsesFreshCache verifies that a fresh (non-expired) disk cache is
// used directly, without hitting the network source.
func TestManagerUsesFreshCache(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer srv.Close()
	cachePath := filepath.Join(t.TempDir(), "mirrors.json")
	fresh := cacheFile{FetchedAt: time.Now(), Mirrors: []string{"https://libgen.bz", "https://libgen.gl"}}
	data, err := json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(cachePath, data, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	m := &Manager{SourceURL: srv.URL, CachePath: cachePath, Preferred: "https://libgen.bz", HTTP: srv.Client()}
	got := m.Mirrors(context.Background())
	if hits != 0 {
		t.Fatalf("a fresh cache should not trigger a fetch, hits = %d", hits)
	}
	if len(got) != 2 || got[0] != "https://libgen.bz" {
		t.Fatalf("Mirrors() = %v, want the cached list preferred-first", got)
	}
}

// TestFetchInvalidURL verifies that fetch fails when the source URL cannot be
// turned into a request (the http.NewRequestWithContext error path).
func TestFetchInvalidURL(t *testing.T) {
	m := &Manager{SourceURL: "http://invalid\x7fhost/", HTTP: http.DefaultClient}
	if _, err := m.fetch(context.Background()); err == nil {
		t.Fatal("fetch() with an invalid URL should error")
	}
}

// TestFetchConnectionError verifies that fetch surfaces a transport error when
// the source is unreachable (the HTTP.Do error path).
func TestFetchConnectionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	client := srv.Client()
	srv.Close() // close before the request so the connection is refused
	m := &Manager{SourceURL: url, HTTP: client}
	if _, err := m.fetch(context.Background()); err == nil {
		t.Fatal("fetch() to a closed server should error")
	}
}

// TestReadCacheErrors covers the malformed and empty cache-file paths in readCache.
func TestReadCacheErrors(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"invalid-json", "{not valid json"},
		{"empty-mirrors", `{"fetched_at":"2020-01-01T00:00:00Z","mirrors":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mirrors.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			m := &Manager{CachePath: path}
			if _, err := m.readCache(); err == nil {
				t.Fatalf("readCache() with %s content should error", tc.name)
			}
		})
	}
}

// TestWriteCacheMkdirError verifies that writeCache silently gives up (writing no
// file) when the cache directory cannot be created.
func TestWriteCacheMkdirError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The parent of the cache dir is a regular file, so MkdirAll cannot create it.
	cachePath := filepath.Join(file, "sub", "mirrors.json")
	m := &Manager{CachePath: cachePath}
	m.writeCache([]string{"https://libgen.li"})
	if _, err := os.Stat(cachePath); err == nil {
		t.Fatal("writeCache() should not create a file when MkdirAll fails")
	}
}

// TestNewManagerCacheDirError verifies that NewManager fails when the OS cache
// directory cannot be resolved.
func TestNewManagerCacheDirError(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	if _, err := os.UserCacheDir(); err == nil {
		t.Skip("os.UserCacheDir still resolves a cache directory on this platform")
	}
	if _, err := NewManager(&config.Config{Timeout: time.Second}); err == nil {
		t.Fatal("NewManager() without a resolvable cache dir should fail")
	}
}
