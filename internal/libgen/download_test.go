package libgen

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	b := newBlockingCDN(t, []byte("%PDF-1.4 payload"))
	defer b.srv.Close()
	c := newTestClientConcurrency(staticMirrors{b.srv.URL}, 1)
	dir := t.TempDir()

	errs := make(chan error, 2)
	go func() {
		_, err := c.Download(context.Background(), testMD5, dir, "one.pdf")
		errs <- err
	}()
	// The first download reaches the CDN and holds the only slot.
	<-b.entered

	go func() {
		_, err := c.Download(context.Background(), testMD5, dir, "two.pdf")
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
	res, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", dir, "")
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
	srv := downloadTestServer(t, []byte("data"))
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()
	res, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", dir, "mi libro.pdf")
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
	res, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", dir, "")
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
