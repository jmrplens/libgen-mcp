package libgen

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

// adsCountingServer builds a libgen-style mirror (ads.php → get.php → CDN) that
// serves payload for testMD5 and counts every /ads.php hit, so a test can assert
// whether a second fetch re-downloaded (counter grows) or was served from cache
// (counter unchanged).
func adsCountingServer(t *testing.T, payload []byte, adsHits *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, _ *http.Request) {
		adsHits.Add(1)
		fmt.Fprintf(w, `<html><a href="get.php?md5=%s&key=TESTKEY123">GET</a></html>`, testMD5)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "TESTKEY123" {
			http.Error(w, "bad key", http.StatusForbidden)
			return
		}
		http.Redirect(w, r, srv.URL+"/cdn/file", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/cdn/file", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="book.pdf"`)
		w.Write(payload)
	})
	srv = httptest.NewServer(mux)
	return srv
}

// newFetchTempClient builds a test client wired to a single mirror with a small
// temp cache, keeping the fast, network-free defaults used elsewhere.
func newFetchTempClient(m MirrorLister) *Client {
	cfg := &config.Config{
		Timeout:                5_000_000_000,
		RateRPS:                1000,
		RateBurst:              100,
		RetryAttempts:          1,
		MaxConcurrentDownloads: 2,
	}
	c := New(m, cfg)
	c.backoffBase = 1_000_000
	c.sources = []DownloadSource{libgenSource{c: c}}
	return c
}

// TestFetchToTemp_DownloadsThenReusesCache verifies that the first FetchToTemp
// downloads the item to a temp file whose bytes match, and that a second fetch
// for the same md5 returns the SAME path from cache without re-downloading (the
// ads.php hit counter stays at 1).
func TestFetchToTemp_DownloadsThenReusesCache(t *testing.T) {
	payload := []byte("%PDF-1.4 fetch-to-temp payload " + string(make([]byte, 64)))
	want := md5Hex(payload) // the libgen source verifies bytes against item.MD5
	var adsHits atomic.Int32
	srv := adsCountingServer(t, payload, &adsHits)
	defer srv.Close()
	c := newFetchTempClient(staticMirrors{srv.URL})

	path1, release1, err := c.FetchToTemp(context.Background(), Item{MD5: want})
	if err != nil {
		t.Fatalf("first FetchToTemp error = %v", err)
	}
	defer release1()
	data, err := os.ReadFile(path1)
	if err != nil {
		t.Fatalf("reading temp file: %v", err)
	}
	if string(data) != string(payload) {
		t.Errorf("temp file content mismatch (len=%d, want %d)", len(data), len(payload))
	}
	if got := adsHits.Load(); got != 1 {
		t.Fatalf("ads.php hits after first fetch = %d, want 1", got)
	}
	release1()

	path2, release2, err := c.FetchToTemp(context.Background(), Item{MD5: want})
	if err != nil {
		t.Fatalf("second FetchToTemp error = %v", err)
	}
	defer release2()
	if path2 != path1 {
		t.Errorf("second fetch path = %q, want cached %q", path2, path1)
	}
	if got := adsHits.Load(); got != 1 {
		t.Errorf("ads.php hits after cached fetch = %d, want 1 (must not re-download)", got)
	}
}

// TestFetchToTemp_NoIdentifier verifies that an item with neither md5 nor doi
// returns an error and a non-nil no-op release.
func TestFetchToTemp_NoIdentifier(t *testing.T) {
	c := newFetchTempClient(staticMirrors{})
	path, release, err := c.FetchToTemp(context.Background(), Item{})
	if err == nil {
		t.Fatal("FetchToTemp with no identifier should error")
	}
	if path != "" {
		t.Errorf("path = %q, want empty", path)
	}
	if release == nil {
		t.Fatal("release must be non-nil (safe to call) even on error")
	}
	release() // must not panic
}

// TestFetchToTemp_DownloadError verifies FetchToTemp's download-failure path: an
// item whose identifier no enabled source can serve (a DOI against a libgen-only
// chain) surfaces the download error with an empty path and a safe no-op release,
// and leaves no per-fetch temp dir behind.
func TestFetchToTemp_DownloadError(t *testing.T) {
	c := newFetchTempClient(staticMirrors{}) // libgen source only; does not support DOIs
	path, release, err := c.FetchToTemp(context.Background(), Item{DOI: "10.1/x"})
	if err == nil {
		t.Fatal("FetchToTemp for an unservable identifier should error")
	}
	if path != "" {
		t.Errorf("path = %q, want empty on error", path)
	}
	if release == nil {
		t.Fatal("release must be non-nil even on error")
	}
	release() // no-op, must not panic
}

// TestFetchToTemp_ConcurrentLoserDiscardsDownload exercises the double-fetch race:
// two goroutines fetch the SAME md5 at once with a download concurrency of 2, so
// both miss the cache and both download before either stores. getOrPut lets one win
// and the other discard its duplicate temp dir; both callers still receive the same
// cached path and a working file.
func TestFetchToTemp_ConcurrentLoserDiscardsDownload(t *testing.T) {
	payload := []byte("%PDF-1.4 concurrent fetch-to-temp payload " + strings.Repeat("x", 64))
	want := md5Hex(payload)
	b := newBlockingCDN(t, payload)
	defer b.srv.Close()
	c := newFetchTempClient(staticMirrors{b.srv.URL})

	type out struct {
		path    string
		release func()
		err     error
	}
	results := make(chan out, 2)
	for range 2 {
		go func() {
			p, r, err := c.FetchToTemp(context.Background(), Item{MD5: want})
			results <- out{p, r, err}
		}()
	}
	// Both downloads reach the CDN (both missed the cache before either stored);
	// release them together so both proceed to getOrPut.
	<-b.entered
	<-b.entered
	b.release <- struct{}{}
	b.release <- struct{}{}

	paths := make([]string, 0, 2)
	for range 2 {
		o := <-results
		if o.err != nil {
			t.Fatalf("FetchToTemp error = %v", o.err)
		}
		defer o.release()
		paths = append(paths, o.path)
	}
	if paths[0] != paths[1] {
		t.Errorf("concurrent fetches returned different paths %q and %q, want the same cached file", paths[0], paths[1])
	}
	data, err := os.ReadFile(paths[0])
	if err != nil || string(data) != string(payload) {
		t.Errorf("cached file content mismatch (err=%v)", err)
	}
}
