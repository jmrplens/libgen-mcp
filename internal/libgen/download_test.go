package libgen

import (
	"context"
	"crypto/md5" //nolint:gosec // tests compute the LibGen file digest for integrity assertions.
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

func TestExtractGetLinkFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/ads.html")
	if err != nil {
		t.Fatal(err)
	}
	link, err := ExtractGetLink(body)
	if err != nil {
		t.Fatalf("ExtractGetLink() error = %v", err)
	}
	if !strings.HasPrefix(link, "get.php?md5=87a4ebdaf21fa6cc70009a3dd63194ee&key=") {
		t.Errorf("link = %q", link)
	}
	if strings.Contains(link, "&amp;") {
		t.Errorf("link sin desescapar: %q", link)
	}
}

func TestExtractGetLinkMissing(t *testing.T) {
	if _, err := ExtractGetLink([]byte("<html>no hay enlace</html>")); err == nil {
		t.Fatal("debería fallar sin enlace get.php")
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"a/b\\c:d*e?f\"g<h>i|j.pdf": "a_b_c_d_e_f_g_h_i_j.pdf",
		"  normal.epub  ":           "normal.epub",
		"":                          "download",
		"...":                       "download",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func downloadTestServer(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, _ *http.Request) {
		// md5 fijo: los tests que usan este servidor descargan siempre el mismo.
		fmt.Fprint(w, `<html><a href="get.php?md5=87a4ebdaf21fa6cc70009a3dd63194ee&key=TESTKEY123">GET</a></html>`)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "TESTKEY123" {
			http.Error(w, "bad key", http.StatusForbidden)
			return
		}
		http.Redirect(w, r, srv.URL+"/cdn/file", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/cdn/file", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="Author - Title (2020).pdf"`)
		w.Write(payload)
	})
	srv = httptest.NewServer(mux)
	return srv
}

const testMD5 = "87a4ebdaf21fa6cc70009a3dd63194ee"

// newTestClientConcurrency builds a test client whose download concurrency
// semaphore is sized to n slots, keeping the same fast, network-free defaults
// as newTestClient.
func newTestClientConcurrency(m MirrorLister, n int) *Client {
	cfg := &config.Config{
		Timeout:                5 * time.Second,
		RateRPS:                1000,
		RateBurst:              100,
		RetryAttempts:          1,
		MaxConcurrentDownloads: n,
	}
	c := New(m, cfg)
	c.backoffBase = time.Millisecond
	return c
}

// blockingCDN is an httptest mirror whose CDN handler blocks each in-flight
// download until the test releases it, letting tests observe how many downloads
// run concurrently.
type blockingCDN struct {
	srv         *httptest.Server
	entered     chan struct{} // one signal per CDN handler entry
	release     chan struct{} // send one value to unblock one CDN handler
	ads         atomic.Int32  // number of /ads.php hits (download attempts started)
	started     atomic.Int32  // number of CDN handler entries
	inFlight    atomic.Int32  // CDN handlers currently blocked
	maxInFlight atomic.Int32  // high-water mark of concurrent CDN handlers
}

func newBlockingCDN(t *testing.T, payload []byte) *blockingCDN {
	t.Helper()
	b := &blockingCDN{
		entered: make(chan struct{}, 8),
		release: make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, _ *http.Request) {
		b.ads.Add(1)
		fmt.Fprintf(w, `<html><a href="get.php?md5=%s&key=TESTKEY123">GET</a></html>`, testMD5)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "TESTKEY123" {
			http.Error(w, "bad key", http.StatusForbidden)
			return
		}
		http.Redirect(w, r, b.srv.URL+"/cdn/file", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/cdn/file", func(w http.ResponseWriter, _ *http.Request) {
		cur := b.inFlight.Add(1)
		for {
			m := b.maxInFlight.Load()
			if cur <= m || b.maxInFlight.CompareAndSwap(m, cur) {
				break
			}
		}
		b.started.Add(1)
		b.entered <- struct{}{}
		<-b.release
		b.inFlight.Add(-1)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="file.pdf"`)
		w.Write(payload)
	})
	b.srv = httptest.NewServer(mux)
	return b
}

// TestConcurrencyLimit verifies that with MaxConcurrentDownloads=1 a second
// download does not reach the mirror until the first releases its slot.
func TestConcurrencyLimit(t *testing.T) {
	payload := []byte("%PDF-1.4 payload")
	wantMD5 := md5Hex(payload)
	b := newBlockingCDN(t, payload)
	defer b.srv.Close()
	c := newTestClientConcurrency(staticMirrors{b.srv.URL}, 1)
	dir := t.TempDir()

	errs := make(chan error, 2)
	go func() {
		_, err := c.Download(context.Background(), wantMD5, dir, "one.pdf")
		errs <- err
	}()
	// The first download reaches the CDN and holds the only slot.
	<-b.entered

	go func() {
		_, err := c.Download(context.Background(), wantMD5, dir, "two.pdf")
		errs <- err
	}()
	// While the first download holds the slot, the second must not reach the CDN.
	select {
	case <-b.entered:
		t.Fatal("second download started while the first held the only slot")
	case <-time.After(150 * time.Millisecond):
	}

	// Release the first download; the second may now acquire the slot and run.
	b.release <- struct{}{}
	<-b.entered
	b.release <- struct{}{}

	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("download error: %v", err)
		}
	}
	if got := b.maxInFlight.Load(); got != 1 {
		t.Errorf("max concurrent CDN downloads = %d, want 1", got)
	}
}

// TestConcurrencyContextCancel verifies that a download whose context is
// canceled while it waits for a slot returns the context error and never
// reaches the mirror.
func TestConcurrencyContextCancel(t *testing.T) {
	b := newBlockingCDN(t, []byte("%PDF-1.4 payload"))
	defer b.srv.Close()
	c := newTestClientConcurrency(staticMirrors{b.srv.URL}, 1)
	dir := t.TempDir()

	// Fill the single slot with a download that blocks in the CDN handler.
	held := make(chan error, 1)
	go func() {
		_, err := c.Download(context.Background(), testMD5, dir, "hold.pdf")
		held <- err
	}()
	<-b.entered

	// A second download waits for the slot; canceling its context must abort it.
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := c.Download(ctx, testMD5, dir, "wait.pdf")
		errc <- err
	}()
	// Give the second download time to block on the semaphore before canceling.
	time.Sleep(50 * time.Millisecond)
	cancel()

	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting download error = %v, want context.Canceled", err)
	}
	if got := b.started.Load(); got != 1 {
		t.Errorf("CDN saw %d downloads, want 1 (the canceled one never started)", got)
	}
	if got := b.ads.Load(); got != 1 {
		t.Errorf("ads.php saw %d hits, want 1 (the canceled one never resolved)", got)
	}

	// Cleanup: release the held download.
	b.release <- struct{}{}
	<-held
}

func TestDownload(t *testing.T) {
	payload := []byte("%PDF-1.4 fake book content")
	srv := downloadTestServer(t, payload)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()
	res, err := c.Download(context.Background(), md5Hex(payload), dir, "")
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if res.OriginalFilename != "Author - Title (2020).pdf" {
		t.Errorf("OriginalFilename = %q", res.OriginalFilename)
	}
	if res.Path != filepath.Join(dir, "Author - Title (2020).pdf") {
		t.Errorf("Path = %q", res.Path)
	}
	data, err := os.ReadFile(res.Path)
	if err != nil || string(data) != string(payload) {
		t.Errorf("contenido = %q, err = %v", data, err)
	}
	if res.SizeBytes != int64(len(payload)) {
		t.Errorf("SizeBytes = %d, want %d", res.SizeBytes, len(payload))
	}
	// sin ficheros temporales huérfanos
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("quedan %d entradas en dir, esperaba 1", len(entries))
	}
}

func TestDownloadCustomFilename(t *testing.T) {
	payload := []byte("data")
	srv := downloadTestServer(t, payload)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()
	res, err := c.Download(context.Background(), md5Hex(payload), dir, "mi libro.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(res.Path) != "mi libro.pdf" {
		t.Errorf("Path = %q", res.Path)
	}
}

func TestDownloadRejectsHTMLViaMagicBytes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a href="get.php?md5=87a4ebdaf21fa6cc70009a3dd63194ee&key=K1">x</a>`)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		// CDN error page sin text/html: se hace pasar por el fichero.
		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprint(w, "<!DOCTYPE html><html><body>blocked</body></html>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()
	if _, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", dir, ""); err == nil {
		t.Fatal("página HTML servida como octet-stream debería fallar")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("quedan %d entradas en dir, esperaba 0 (ni fichero ni temporal)", len(entries))
	}
}

func TestDownloadSizeCapContentLength(t *testing.T) {
	// Payload small enough to carry an explicit Content-Length header, but larger
	// than the configured cap: the download must fail before touching the disk.
	payload := []byte("%PDF-1.4 " + strings.Repeat("x", 500))
	srv := downloadTestServer(t, payload)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	c.maxDownloadBytes = 100
	dir := t.TempDir()
	if _, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", dir, ""); err == nil {
		t.Fatal("Content-Length above cap should fail")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("quedan %d entradas en dir, esperaba 0 (ni fichero ni temporal)", len(entries))
	}
}

func TestDownloadSizeCapStream(t *testing.T) {
	// Payload larger than the server's buffer (>2048 B) so the response is sent
	// chunked with no Content-Length: the cap must be enforced while streaming.
	payload := []byte("%PDF-1.4 " + strings.Repeat("x", 4000))
	srv := downloadTestServer(t, payload)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	c.maxDownloadBytes = 100
	dir := t.TempDir()
	if _, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", dir, ""); err == nil {
		t.Fatal("streamed body above cap should fail")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("quedan %d entradas en dir, esperaba 0 (ni fichero ni temporal)", len(entries))
	}
}

func TestDownloadDiskCheck(t *testing.T) {
	payload := []byte("%PDF-1.4 fake book content")
	srv := downloadTestServer(t, payload)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	orig := freeSpaceFn
	freeSpaceFn = func(string) (uint64, error) { return 1, nil } // simulate a nearly full disk
	t.Cleanup(func() { freeSpaceFn = orig })
	dir := t.TempDir()
	if _, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", dir, ""); err == nil {
		t.Fatal("insufficient free disk space should fail")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("quedan %d entradas en dir, esperaba 0 (ni fichero ni temporal)", len(entries))
	}
}

func TestDownloadUnderCap(t *testing.T) {
	// Regression: a normal download comfortably under the cap still succeeds.
	payload := []byte("%PDF-1.4 fake book content")
	srv := downloadTestServer(t, payload)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	c.maxDownloadBytes = 1 << 20 // 1 MiB, far above the payload
	dir := t.TempDir()
	res, err := c.Download(context.Background(), md5Hex(payload), dir, "")
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if res.SizeBytes != int64(len(payload)) {
		t.Errorf("SizeBytes = %d, want %d", res.SizeBytes, len(payload))
	}
}

func TestLooksLikeHTML(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want bool
	}{
		{"doctype", []byte("<!DOCTYPE html>"), true},
		{"leading ws html", []byte("  \n<html>"), true},
		{"comment", []byte("<!-- comment"), true},
		{"pdf", []byte("%PDF-1.4"), false},
		{"zip epub", []byte("PK\x03\x04"), false},
		{"binary", []byte{0x00, 0x01, 0x02, 0xff}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeHTML(tc.in); got != tc.want {
				t.Errorf("looksLikeHTML(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// md5Hex returns the hex-encoded MD5 digest of b.
func md5Hex(b []byte) string {
	sum := md5.Sum(b) //nolint:gosec // integrity digest, not a security primitive.
	return hex.EncodeToString(sum[:])
}

// md5CDNServer builds a mirror whose /ads.php advertises the given md5 and whose
// /cdn/file is served by the provided handler, letting each test control the CDN
// response (status, Range handling, body) independently.
func md5CDNServer(t *testing.T, wantMD5 string, cdn http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<html><a href="get.php?md5=%s&key=TESTKEY123">GET</a></html>`, wantMD5)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "TESTKEY123" {
			http.Error(w, "bad key", http.StatusForbidden)
			return
		}
		http.Redirect(w, r, srv.URL+"/cdn/file", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/cdn/file", cdn)
	srv = httptest.NewServer(mux)
	return srv
}

// partPathFor mirrors the stable partial path the downloader uses for an md5.
func partPathFor(dir, md5 string) string {
	return filepath.Join(dir, ".libgen-mcp-"+md5+".part")
}

// rangeStart parses the numeric start offset from a "bytes=<start>-" Range header.
func rangeStart(t *testing.T, header string) int64 {
	t.Helper()
	const prefix = "bytes="
	if !strings.HasPrefix(header, prefix) {
		t.Fatalf("unexpected Range header %q", header)
	}
	spec := strings.TrimPrefix(header, prefix)
	start, _, _ := strings.Cut(spec, "-")
	n, err := strconv.ParseInt(start, 10, 64)
	if err != nil {
		t.Fatalf("parsing Range start from %q: %v", header, err)
	}
	return n
}

func TestDownloadVerifiesMD5Match(t *testing.T) {
	payload := []byte("%PDF-1.4 " + strings.Repeat("real book bytes ", 64))
	want := md5Hex(payload)
	srv := md5CDNServer(t, want, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="book.pdf"`)
		w.Write(payload)
	})
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()

	res, err := c.Download(context.Background(), want, dir, "")
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if !res.Verified {
		t.Error("Verified = false, want true")
	}
	if res.Resumed {
		t.Error("Resumed = true, want false (fresh download)")
	}
	data, err := os.ReadFile(res.Path)
	if err != nil || string(data) != string(payload) {
		t.Errorf("content = %q, err = %v", data, err)
	}
	// The stable partial must be gone after a successful rename.
	if _, statErr := os.Stat(partPathFor(dir, want)); !os.IsNotExist(statErr) {
		t.Errorf(".part still present after success: %v", statErr)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want 1", len(entries))
	}
}

func TestDownloadMD5Mismatch(t *testing.T) {
	payload := []byte("%PDF-1.4 " + strings.Repeat("tampered bytes ", 64))
	// Request a different (but syntactically valid) md5 than the body's real one.
	want := md5Hex([]byte("some other content entirely"))
	if want == md5Hex(payload) {
		t.Fatal("test setup: md5s unexpectedly collide")
	}
	srv := md5CDNServer(t, want, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="book.pdf"`)
		w.Write(payload)
	})
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()

	if _, err := c.Download(context.Background(), want, dir, ""); err == nil {
		t.Fatal("md5 mismatch should fail")
	}
	// On integrity failure the partial is deleted and no final file is left.
	if _, statErr := os.Stat(partPathFor(dir, want)); !os.IsNotExist(statErr) {
		t.Errorf(".part still present after mismatch: %v", statErr)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("dir has %d entries, want 0 (no final file, no .part)", len(entries))
	}
}

func TestDownloadResume(t *testing.T) {
	full := []byte("%PDF-1.4 " + strings.Repeat("resumable content chunk ", 40))
	want := md5Hex(full)
	half := len(full) / 2

	var sawRange atomic.Bool
	srv := md5CDNServer(t, want, func(w http.ResponseWriter, r *http.Request) {
		rng := r.Header.Get("Range")
		if rng == "" {
			t.Errorf("resume: expected a Range header, got none")
			w.Write(full)
			return
		}
		sawRange.Store(true)
		start := rangeStart(t, rng)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="book.pdf"`)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(full)-1, len(full)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(full[start:])
	})
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()

	// Pre-create the partial with the first half already downloaded.
	if err := os.WriteFile(partPathFor(dir, want), full[:half], 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := c.Download(context.Background(), want, dir, "")
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if !sawRange.Load() {
		t.Error("CDN never saw a Range request")
	}
	if !res.Resumed {
		t.Error("Resumed = false, want true")
	}
	if !res.Verified {
		t.Error("Verified = false, want true")
	}
	data, err := os.ReadFile(res.Path)
	if err != nil || string(data) != string(full) {
		t.Errorf("content mismatch after resume (len=%d, want %d), err=%v", len(data), len(full), err)
	}
}

func TestDownloadResumeServerIgnoresRange(t *testing.T) {
	full := []byte("%PDF-1.4 " + strings.Repeat("restart content chunk ", 40))
	want := md5Hex(full)
	half := len(full) / 2

	srv := md5CDNServer(t, want, func(w http.ResponseWriter, _ *http.Request) {
		// The CDN ignores Range entirely: 200 with the full body.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="book.pdf"`)
		w.Write(full)
	})
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()

	if err := os.WriteFile(partPathFor(dir, want), full[:half], 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := c.Download(context.Background(), want, dir, "")
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	// A 200 despite the Range means the partial was discarded and restarted.
	if res.Resumed {
		t.Error("Resumed = true, want false (server ignored Range → restart)")
	}
	if !res.Verified {
		t.Error("Verified = false, want true")
	}
	data, err := os.ReadFile(res.Path)
	if err != nil || string(data) != string(full) {
		t.Errorf("content mismatch after restart (len=%d, want %d), err=%v", len(data), len(full), err)
	}
}

// TestDownloadSameMD5Concurrent runs two downloads for the SAME md5 into the SAME
// dir at once (MaxConcurrentDownloads=2, so the semaphore does not serialize
// them). The per-partial-path lock must serialize them so neither corrupts the
// shared .part file; both must succeed with a verified digest and the final file
// must carry the correct content.
func TestDownloadSameMD5Concurrent(t *testing.T) {
	payload := []byte("%PDF-1.4 " + strings.Repeat("concurrent same-md5 bytes ", 64))
	want := md5Hex(payload)
	srv := md5CDNServer(t, want, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="book.pdf"`)
		w.Write(payload)
	})
	defer srv.Close()
	c := newTestClientConcurrency(staticMirrors{srv.URL}, 2)
	dir := t.TempDir()

	type outcome struct {
		res *DownloadResult
		err error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			res, err := c.Download(context.Background(), want, dir, "")
			results <- outcome{res, err}
		}()
	}
	for range 2 {
		o := <-results
		if o.err != nil {
			t.Fatalf("Download() error = %v", o.err)
		}
		if !o.res.Verified {
			t.Error("Verified = false, want true")
		}
	}
	dest := filepath.Join(dir, "book.pdf")
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading final file: %v", err)
	}
	if got := md5Hex(data); got != want {
		t.Errorf("final file md5 = %s, want %s (corruption from concurrent same-md5 downloads?)", got, want)
	}
	if string(data) != string(payload) {
		t.Errorf("final file content mismatch (len=%d, want %d)", len(data), len(payload))
	}
}

// TestDownloadResumeWrongContentRange guards the Content-Range validation: the
// CDN replies 206 but with a Content-Range whose start (0) disagrees with the
// requested resume offset, and sends the FULL body. The downloader must restart
// from zero rather than append the full body onto the existing half (which would
// corrupt the file), and still finish with the correct md5.
func TestDownloadResumeWrongContentRange(t *testing.T) {
	full := []byte("%PDF-1.4 " + strings.Repeat("wrong content-range chunk ", 40))
	want := md5Hex(full)
	half := len(full) / 2

	srv := md5CDNServer(t, want, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="book.pdf"`)
		// Start offset 0 disagrees with resumeFrom (half); full body follows.
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(full)-1, len(full)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(full)
	})
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()

	if err := os.WriteFile(partPathFor(dir, want), full[:half], 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := c.Download(context.Background(), want, dir, "")
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if res.Resumed {
		t.Error("Resumed = true, want false (mismatched Content-Range → restart from zero)")
	}
	if !res.Verified {
		t.Error("Verified = false, want true")
	}
	data, err := os.ReadFile(res.Path)
	if err != nil || string(data) != string(full) {
		t.Errorf("content mismatch after restart (len=%d, want %d), err=%v", len(data), len(full), err)
	}
	if got := md5Hex(data); got != want {
		t.Errorf("final file md5 = %s, want %s", got, want)
	}
}

func TestDownloadRejectsHTMLResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a href="get.php?md5=87a4ebdaf21fa6cc70009a3dd63194ee&key=K1">x</a>`)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html>error page</html>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	if _, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", t.TempDir(), ""); err == nil {
		t.Fatal("respuesta HTML debería fallar")
	}
}
