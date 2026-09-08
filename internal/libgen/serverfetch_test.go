package libgen

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

// noFetchClient builds a test client wired to one mirror with file fetching
// switched off, the shape a hosted deployment runs in.
func noFetchClient(m MirrorLister) *Client {
	off := false
	cfg := &config.Config{
		Timeout:                5_000_000_000,
		RateRPS:                1000,
		RateBurst:              100,
		RetryAttempts:          1,
		MaxConcurrentDownloads: 2,
		ServerFetch:            &off,
	}
	c := New(m, cfg)
	c.backoffBase = 1_000_000
	c.sources = []DownloadSource{libgenSource{c: c}}
	return c
}

// TestDownloadItemRefusesWhenFetchDisabled asserts the body guard: with fetching
// off, DownloadItem returns ErrServerFetchDisabled and the mirror is never
// touched at all — not even the ads.php page a resolve would read.
func TestDownloadItemRefusesWhenFetchDisabled(t *testing.T) {
	payload := []byte("%PDF-1.4 must never be fetched")
	var adsHits atomic.Int32
	srv := adsCountingServer(t, payload, &adsHits)
	defer srv.Close()
	c := noFetchClient(staticMirrors{srv.URL})

	dir := t.TempDir()
	res, err := c.DownloadItem(context.Background(), Item{MD5: md5Hex(payload)}, dir, "")
	if !errors.Is(err, ErrServerFetchDisabled) {
		t.Fatalf("DownloadItem error = %v, want ErrServerFetchDisabled", err)
	}
	if res != nil {
		t.Errorf("result = %+v, want nil", res)
	}
	if got := adsHits.Load(); got != 0 {
		t.Errorf("mirror was contacted %d time(s); a refused download must make no request", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a refused download wrote %d file(s), want 0", len(entries))
	}
}

// TestFetchToTempRefusesWhenFetchDisabled asserts the same guard on the read
// tool's entry point, with a release that is still safe to call and no temp
// directory left behind.
func TestFetchToTempRefusesWhenFetchDisabled(t *testing.T) {
	payload := []byte("%PDF-1.4 must never be fetched")
	var adsHits atomic.Int32
	srv := adsCountingServer(t, payload, &adsHits)
	defer srv.Close()
	c := noFetchClient(staticMirrors{srv.URL})

	// FetchToTemp creates its per-fetch directory under TMPDIR, so pointing that
	// at the test's own directory makes "created no temp dir" observable.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	path, release, err := c.FetchToTemp(context.Background(), Item{MD5: md5Hex(payload)})
	if !errors.Is(err, ErrServerFetchDisabled) {
		t.Fatalf("FetchToTemp error = %v, want ErrServerFetchDisabled", err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty", path)
	}
	if release == nil {
		t.Fatal("release must be non-nil (safe to call) even on error")
	}
	release() // must not panic
	if got := adsHits.Load(); got != 0 {
		t.Errorf("mirror was contacted %d time(s); a refused read must make no request", got)
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("a refused fetch left %d temp entrie(s) behind, want 0", len(entries))
	}
}

// TestErrServerFetchDisabledNamesTheWayOut asserts the refusal is actionable: it
// names the variable that lifts it and the tool that still works, because this
// text is what a caller sees if the guard is ever reached through a surface that
// forgot to hide the tool.
func TestErrServerFetchDisabledNamesTheWayOut(t *testing.T) {
	msg := ErrServerFetchDisabled.Error()
	for _, want := range []string{"LIBGEN_MCP_SERVER_FETCH", "download"} {
		if !strings.Contains(msg, want) {
			t.Errorf("ErrServerFetchDisabled = %q, want it to name %q", msg, want)
		}
	}
}

// TestResolveLinkStillWorksWhenFetchDisabled pins the deliberate boundary of the
// guard: resolving a link is not fetching a body, so download keeps working on a
// server that will not pull files. Without this the tool would have nothing left
// to do, and the reason it is allowed is recorded on ErrServerFetchDisabled.
func TestResolveLinkStillWorksWhenFetchDisabled(t *testing.T) {
	payload := []byte("%PDF-1.4 resolvable but never fetched")
	var adsHits, bodyHits atomic.Int32
	// A mirror that answers the light half of a download (the ads page carrying
	// the key) and counts, rather than serves, any request for the file itself.
	mux := http.NewServeMux()
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, _ *http.Request) {
		adsHits.Add(1)
		fmt.Fprintf(w, `<html><a href="get.php?md5=%s&key=TESTKEY123">GET</a></html>`, md5Hex(payload))
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, _ *http.Request) {
		bodyHits.Add(1)
		http.Error(w, "the body must not be fetched", http.StatusTeapot)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := noFetchClient(staticMirrors{srv.URL})

	got, err := c.ResolveLink(context.Background(), Item{MD5: md5Hex(payload)})
	if err != nil {
		t.Fatalf("ResolveLink error = %v, want a resolved link on a no-fetch server", err)
	}
	if got.URL == "" {
		t.Error("resolved URL is empty")
	}
	if bodyHits.Load() != 0 {
		t.Errorf("resolving fetched the body %d time(s), want 0", bodyHits.Load())
	}
	if adsHits.Load() == 0 {
		t.Error("resolving made no request to the mirror; the guard must not block resolution")
	}
}
