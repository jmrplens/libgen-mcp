package libgen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// tinyWaits is a fast start-retry schedule (microsecond-scale) so retry tests
// never wait the production seconds.
func tinyWaits(n int) []time.Duration {
	waits := make([]time.Duration, n)
	for i := range waits {
		waits[i] = time.Millisecond
	}
	return waits
}

// flakyStartCDN serves /cdn/file: the first failN requests answer 503 (a non-2xx
// that fails to start), and every later request serves payload as the file. It
// records how many times the CDN was hit so tests can assert the attempt count.
func flakyStartCDN(t *testing.T, wantMD5 string, payload []byte, failN int32, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	return md5CDNServer(t, wantMD5, func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) <= failN {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="book.pdf"`)
		_, _ = w.Write(payload)
	})
}

// TestDownloadStartRetrySucceedsAfterFailures covers case (a): a source that
// fails to start twice (503) then serves the file on the third attempt completes
// successfully once the start-retry schedule carries it past the transient
// failures.
func TestDownloadStartRetrySucceedsAfterFailures(t *testing.T) {
	payload := []byte("%PDF-1.4 " + strings.Repeat("start-retry recovery ", 40))
	want := md5Hex(payload)
	var hits atomic.Int32
	srv := flakyStartCDN(t, want, payload, 2, &hits)
	defer srv.Close()

	c := newTestClient(staticMirrors{srv.URL})
	c.startRetryWaits = tinyWaits(5)
	c.stallTimeout = 2 * time.Second
	dir := t.TempDir()

	res, err := c.Download(context.Background(), want, dir, "", nil)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if !res.Verified {
		t.Error("Verified = false, want true")
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("CDN hits = %d, want 3 (two 503s then success)", got)
	}
	data, rerr := os.ReadFile(res.Path)
	if rerr != nil || string(data) != string(payload) {
		t.Errorf("content mismatch after start-retry, err = %v", rerr)
	}
}

// TestDownloadStartRetryExhaustsActionableError covers case (b): a source that
// never starts (always 503) exhausts the schedule and surfaces the actionable
// errDownloadCouldNotStart, having made exactly len(waits)+1 attempts.
func TestDownloadStartRetryExhaustsActionableError(t *testing.T) {
	var hits atomic.Int32
	srv := md5CDNServer(t, testMD5, func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	})
	defer srv.Close()

	c := newTestClient(staticMirrors{srv.URL})
	c.startRetryWaits = tinyWaits(2) // 3 attempts total
	dir := t.TempDir()

	_, err := c.Download(context.Background(), testMD5, dir, "", nil)
	if err == nil {
		t.Fatal("a source that never starts should fail")
	}
	if !errors.Is(err, errDownloadCouldNotStart) {
		t.Errorf("err = %v, want errDownloadCouldNotStart", err)
	}
	if !strings.Contains(err.Error(), "retry now") || !strings.Contains(err.Error(), "ask the user") {
		t.Errorf("error is not actionable: %v", err)
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("CDN hits = %d, want 3 (1 initial + 2 retries)", got)
	}
}

// pacedServer serves payload as an octet-stream, writing it in chunk-sized pieces
// with gap between pieces and flushing each. When hang is true it writes one
// initial chunk, flushes, then blocks until the request context is canceled
// (simulating a stalled transfer). It bails out early if the client disconnects.
func pacedServer(t *testing.T, payload []byte, chunk int, gap time.Duration, hang bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter is not a Flusher")
			return
		}
		if hang {
			_, _ = w.Write(payload[:min(chunk, len(payload))])
			flusher.Flush()
			<-r.Context().Done() // stall: never send the rest
			return
		}
		for i := 0; i < len(payload); i += chunk {
			end := min(i+chunk, len(payload))
			if _, err := w.Write(payload[i:end]); err != nil {
				return
			}
			flusher.Flush()
			select {
			case <-time.After(gap):
			case <-r.Context().Done():
				return
			}
		}
	}))
}

// TestDownloadStallAborts covers case (c): once bytes are flowing, a server that
// stops sending is aborted after the stall window, the error is a clear stall,
// and the partial is kept so a later call can resume.
func TestDownloadStallAborts(t *testing.T) {
	// >512 bytes so the first-byte peek succeeds and streaming begins before the
	// server hangs.
	payload := []byte("%PDF-1.4 " + strings.Repeat("z", 600))
	srv := pacedServer(t, payload, 600, 0, true)
	defer srv.Close()

	c := newTestClient(staticMirrors{})
	c.sources = []DownloadSource{stubSource{name: "stall", supports: true, resolved: Resolved{FileURL: srv.URL, VerifyMD5: false}}}
	c.startRetryWaits = nil // streaming begins, so start-retries never apply
	c.stallTimeout = 50 * time.Millisecond
	dir := t.TempDir()

	start := time.Now()
	_, err := c.DownloadItem(context.Background(), Item{MD5: testMD5}, dir, "out.pdf")
	if err == nil {
		t.Fatal("a stalled download should fail")
	}
	if !errors.Is(err, errStalled) {
		t.Errorf("err = %v, want errStalled", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("stall abort took %v, want well under the 2s guard", elapsed)
	}
	// The partial is kept so a later call can resume from the stalled offset.
	part := filepath.Join(dir, ".libgen-mcp-stall-"+testMD5+".part")
	if _, statErr := os.Stat(part); statErr != nil {
		t.Errorf("stalled download should keep its .part at %s: %v", part, statErr)
	}
}

// TestDownloadSlowButProgressingCompletes covers case (d): a genuinely slow
// transfer (each inter-byte gap under the stall window, total duration well over
// it) is NOT aborted and completes with the full content — a wall-clock timeout
// must never kill a download that is still making progress.
func TestDownloadSlowButProgressingCompletes(t *testing.T) {
	payload := []byte("%PDF-1.4 " + strings.Repeat("progress ", 250)) // ~2.2 KB
	// Stream after the initial 512-byte peek spans many gaps: total >> stall window,
	// every individual gap << stall window.
	srv := pacedServer(t, payload, 100, 15*time.Millisecond, false)
	defer srv.Close()

	c := newTestClient(staticMirrors{})
	c.sources = []DownloadSource{stubSource{name: "slow", supports: true, resolved: Resolved{FileURL: srv.URL, VerifyMD5: false}}}
	c.stallTimeout = 100 * time.Millisecond
	dir := t.TempDir()

	res, err := c.DownloadItem(context.Background(), Item{MD5: testMD5}, dir, "out.pdf")
	if err != nil {
		t.Fatalf("a slow-but-progressing download should complete, error = %v", err)
	}
	if res.SizeBytes != int64(len(payload)) {
		t.Errorf("SizeBytes = %d, want %d", res.SizeBytes, len(payload))
	}
	data, rerr := os.ReadFile(res.Path)
	if rerr != nil || string(data) != string(payload) {
		t.Errorf("content mismatch after slow transfer, err = %v", rerr)
	}
}

// TestDownloadStartRetryContextCancelDuringWait covers case (e): a context
// canceled while the schedule is sleeping between attempts aborts the download
// immediately, well before the (long) wait elapses, and no further attempt runs.
func TestDownloadStartRetryContextCancelDuringWait(t *testing.T) {
	var hits atomic.Int32
	srv := md5CDNServer(t, testMD5, func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	})
	defer srv.Close()

	c := newTestClient(staticMirrors{srv.URL})
	c.startRetryWaits = []time.Duration{10 * time.Second} // a long wait we cancel through
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := c.Download(ctx, testMD5, dir, "", nil)
		errc <- err
	}()

	// Wait for the first attempt to fail and the schedule to enter its wait.
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("first start attempt never ran")
		}
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	cancel()

	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("cancel during wait took %v to abort, want immediate", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel during wait did not abort the download")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("CDN hits = %d, want 1 (the canceled wait prevented a second attempt)", got)
	}
}
