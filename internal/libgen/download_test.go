package libgen

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // tests compute the LibGen file digest for integrity assertions.
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

// TestExtractGetLinkFixture verifies ExtractGetLinkFixture.
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
		t.Errorf("link not unescaped: %q", link)
	}
}

// TestExtractGetLinkMissing verifies ExtractGetLinkMissing.
func TestExtractGetLinkMissing(t *testing.T) {
	if _, err := ExtractGetLink([]byte("<html>no link</html>")); err == nil {
		t.Fatal("should fail without a get.php link")
	}
}

// noDispositionCDN builds a mirror whose CDN serves payload with NO
// Content-Disposition header, so the output name must come from metadata (or the
// explicit filename), never from the mirror.
func noDispositionCDN(t *testing.T, wantMD5 string, payload []byte) *httptest.Server {
	t.Helper()
	return md5CDNServer(t, wantMD5, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(payload)
	})
}

// TestDownloadUsesCleanNameWhenNoDisposition verifies that when the CDN announces
// no Content-Disposition, the file is named from the supplied metadata.
func TestDownloadUsesCleanNameWhenNoDisposition(t *testing.T) {
	payload := []byte("%PDF-1.4 clean-name payload")
	want := md5Hex(payload)
	srv := noDispositionCDN(t, want, payload)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()

	meta := &FileMeta{Author: "Jane Doe", Title: "Great Book", Year: "2020", Ext: "pdf"}
	res, err := c.Download(context.Background(), want, dir, "", meta)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	const wantName = "Jane Doe - Great Book (2020).pdf"
	if got := filepath.Base(res.Path); got != wantName {
		t.Errorf("Path base = %q, want %q", got, wantName)
	}
}

// TestDownloadExplicitFilenameBeatsMeta verifies the explicit filename still wins
// over metadata even when no Content-Disposition is present.
func TestDownloadExplicitFilenameBeatsMeta(t *testing.T) {
	payload := []byte("%PDF-1.4 explicit-wins payload")
	want := md5Hex(payload)
	srv := noDispositionCDN(t, want, payload)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()

	meta := &FileMeta{Author: "Jane Doe", Title: "Great Book", Year: "2020", Ext: "pdf"}
	res, err := c.Download(context.Background(), want, dir, "explicit.pdf", meta)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if got := filepath.Base(res.Path); got != "explicit.pdf" {
		t.Errorf("Path base = %q, want %q (explicit filename must win over meta)", got, "explicit.pdf")
	}
}

// TestDownloadMetaBeatsDispositionWhenVerified verifies that on an md5 download —
// the one whose bytes are checked against the requested digest — the
// metadata-built name wins over the mirror's Content-Disposition, and that the
// announced name is still reported as OriginalFilename.
func TestDownloadMetaBeatsDispositionWhenVerified(t *testing.T) {
	payload := []byte("%PDF-1.4 disposition-vs-meta payload")
	want := md5Hex(payload)
	srv := md5CDNServer(t, want, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="[Incerto] Jane Doe - Great Book [`+want+`] - libgen.li.pdf"`)
		w.Write(payload)
	})
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()

	meta := &FileMeta{Author: "Jane Doe", Title: "Great Book", Year: "2020", Ext: "pdf"}
	res, err := c.Download(context.Background(), want, dir, "", meta)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if got := filepath.Base(res.Path); got != "Jane Doe - Great Book (2020).pdf" {
		t.Errorf("Path base = %q, want the metadata-built name (the digest matched, so it is safe)", got)
	}
	if res.NameOrigin != NameFromMetadata {
		t.Errorf("NameOrigin = %q, want %q", res.NameOrigin, NameFromMetadata)
	}
	if !strings.Contains(res.OriginalFilename, "libgen.li") {
		t.Errorf("OriginalFilename = %q, want the mirror's announced name kept verbatim", res.OriginalFilename)
	}
}

// TestDownloadUnverifiedKeepsAnnouncedName verifies the rule that keeps a
// mis-delivery detectable: a download with no digest to check against (a DOI) is
// saved under the name the source announced — cleaned of the mirror's own marks —
// and is NOT renamed after the record that was requested.
func TestDownloadUnverifiedKeepsAnnouncedName(t *testing.T) {
	payload := []byte("%PDF-1.4 the wrong article, as it happens")
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="npre2007361-1.pdf"`)
		_, _ = w.Write(payload)
	}))
	defer cdn.Close()
	c := newTestClient(staticMirrors{cdn.URL})
	c.sources = []DownloadSource{stubDOISource{url: cdn.URL + "/file"}}
	dir := t.TempDir()

	item := Item{
		DOI:  "10.1371/journal.pmed.0020124",
		Meta: &FileMeta{Author: "John P. A. Ioannidis", Title: "Why Most Published Research Findings Are False", Year: "2005"},
	}
	res, err := c.DownloadItem(context.Background(), item, dir, "")
	if err != nil {
		t.Fatalf("DownloadItem() error = %v", err)
	}
	if got := filepath.Base(res.Path); got != "npre2007361-1.pdf" {
		t.Errorf("Path base = %q, want the announced name kept (an unverified download must not be renamed after the request)", got)
	}
	if res.NameOrigin != NameFromAnnounced {
		t.Errorf("NameOrigin = %q, want %q", res.NameOrigin, NameFromAnnounced)
	}
}

// TestDownloadUnverifiedPlaceholderNameFallsBackToIdentifier verifies that when
// the source announces a name that identifies nothing ("download.pdf", which is
// what the Internet Archive returns for every ISBN), the file is named after the
// identifier that was requested and the result says the name was derived.
func TestDownloadUnverifiedPlaceholderNameFallsBackToIdentifier(t *testing.T) {
	payload := []byte("%PDF-1.4 an openly licensed book")
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="download.pdf"`)
		_, _ = w.Write(payload)
	}))
	defer cdn.Close()
	c := newTestClient(staticMirrors{cdn.URL})
	c.sources = []DownloadSource{stubISBNSource{url: cdn.URL + "/file"}}
	dir := t.TempDir()

	res, err := c.DownloadItem(context.Background(), Item{ISBN: "9789286150616"}, dir, "")
	if err != nil {
		t.Fatalf("DownloadItem() error = %v", err)
	}
	if got := filepath.Base(res.Path); got != "9789286150616.pdf" {
		t.Errorf("Path base = %q, want the ISBN (the announced name was a placeholder)", got)
	}
	if !res.NameOrigin.Derived() {
		t.Errorf("NameOrigin = %q, want a derived origin so the caller is told the name was not announced", res.NameOrigin)
	}
}

// stubDOISource resolves any DOI item to a fixed URL with no MD5 verification,
// standing in for the article sources in a naming test.
type stubDOISource struct{ url string }

// Name identifies the stub source.
func (s stubDOISource) Name() string { return "unpaywall" }

// Supports accepts any item carrying a DOI.
func (s stubDOISource) Supports(item Item) bool { return item.DOI != "" }

// Resolve returns the stub's fixed URL, unverified, as the article sources do.
func (s stubDOISource) Resolve(context.Context, Item) (Resolved, error) {
	return Resolved{FileURL: s.url, VerifyMD5: false}, nil
}

// stubISBNSource resolves any ISBN item to a fixed URL with no MD5 verification,
// standing in for the open-access book sources in a naming test.
type stubISBNSource struct{ url string }

// Name identifies the stub source.
func (s stubISBNSource) Name() string { return "archive" }

// Supports accepts any item carrying an ISBN.
func (s stubISBNSource) Supports(item Item) bool { return item.ISBN != "" }

// Resolve returns the stub's fixed URL, unverified, as the ISBN sources do.
func (s stubISBNSource) Resolve(context.Context, Item) (Resolved, error) {
	return Resolved{FileURL: s.url, VerifyMD5: false}, nil
}

func downloadTestServer(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, _ *http.Request) {
		// Fixed md5: tests using this server always download the same one.
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
		_, err := c.Download(context.Background(), wantMD5, dir, "one.pdf", nil)
		errs <- err
	}()
	// The first download reaches the CDN and holds the only slot.
	<-b.entered

	go func() {
		_, err := c.Download(context.Background(), wantMD5, dir, "two.pdf", nil)
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
		_, err := c.Download(context.Background(), testMD5, dir, "hold.pdf", nil)
		held <- err
	}()
	<-b.entered

	// A second download waits for the slot; canceling its context must abort it.
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := c.Download(ctx, testMD5, dir, "wait.pdf", nil)
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

// TestDownload verifies Download.
func TestDownload(t *testing.T) {
	payload := []byte("%PDF-1.4 fake book content")
	srv := downloadTestServer(t, payload)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()
	res, err := c.Download(context.Background(), md5Hex(payload), dir, "", nil)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if res.OriginalFilename != "Author - Title (2020).pdf" {
		t.Errorf("OriginalFilename = %q", res.OriginalFilename)
	}
	// The default chain serves md5 items through the LibGen source.
	if res.Source != "libgen" {
		t.Errorf("Source = %q, want %q", res.Source, "libgen")
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
	// no orphaned temporary files
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("%d entries left in dir, want 1", len(entries))
	}
}

// TestDownloadCustomFilename verifies DownloadCustomFilename.
func TestDownloadCustomFilename(t *testing.T) {
	payload := []byte("data")
	srv := downloadTestServer(t, payload)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()
	res, err := c.Download(context.Background(), md5Hex(payload), dir, "mi libro.pdf", nil)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(res.Path) != "mi libro.pdf" {
		t.Errorf("Path = %q", res.Path)
	}
}

// TestDownloadRejectsHTMLViaMagicBytes verifies DownloadRejectsHTMLViaMagicBytes.
func TestDownloadRejectsHTMLViaMagicBytes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a href="get.php?md5=87a4ebdaf21fa6cc70009a3dd63194ee&key=K1">x</a>`)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		// CDN error page without text/html: masquerades as the file.
		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprint(w, "<!DOCTYPE html><html><body>blocked</body></html>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()
	if _, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", dir, "", nil); err == nil {
		t.Fatal("an HTML page served as octet-stream should fail")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("%d entries left in dir, want 0 (neither file nor temp)", len(entries))
	}
}

// TestDownloadRejectsErrorPageFixture verifies the magic-byte HTML sniff against
// the captured error_page.html fixture served as application/octet-stream (no
// text/html Content-Type): the sniffer must still reject it and leave no file.
func TestDownloadRejectsErrorPageFixture(t *testing.T) {
	errPage, err := os.ReadFile("testdata/error_page.html")
	if err != nil {
		t.Fatal(err)
	}
	srv := md5CDNServer(t, testMD5, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream") // masquerades as the file
		_, _ = w.Write(errPage)
	})
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()
	if _, derr := c.Download(context.Background(), testMD5, dir, "", nil); derr == nil {
		t.Fatal("an HTML error page served as octet-stream should be rejected")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("%d entries left in dir, want 0 (neither file nor temp)", len(entries))
	}
}

// TestDownloadResumeServerError verifies resumeDecision's failure branch: with a
// partial already on disk, a mirror that answers neither 206 nor 200 (here 500)
// is a download failure, and the partial is kept for a later retry.
func TestDownloadResumeServerError(t *testing.T) {
	full := []byte("%PDF-1.4 " + strings.Repeat("resume error chunk ", 40))
	want := md5Hex(full)
	half := len(full) / 2
	srv := md5CDNServer(t, want, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	})
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()
	if err := os.WriteFile(partPathFor(dir, want), full[:half], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Download(context.Background(), want, dir, "", nil); err == nil {
		t.Fatal("a resume that gets a 500 (neither 206 nor 200) should fail")
	}
	if _, statErr := os.Stat(partPathFor(dir, want)); os.IsNotExist(statErr) {
		t.Error("the partial should be kept after a transient resume failure")
	}
}

// TestDownloadRoutesAroundAnOccupiedName verifies that a destination name already
// taken by something else does not sink the download: the file lands beside it
// under a discriminated name rather than failing or overwriting.
func TestDownloadRoutesAroundAnOccupiedName(t *testing.T) {
	payload := []byte("%PDF-1.4 " + strings.Repeat("rename clash ", 64))
	want := md5Hex(payload)
	srv := md5CDNServer(t, want, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(payload)
	})
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()
	// Occupy the destination name ("<md5>") with a non-empty directory.
	dest := filepath.Join(dir, want)
	if err := os.MkdirAll(filepath.Join(dest, "occupied"), 0o750); err != nil {
		t.Fatal(err)
	}
	res, err := c.Download(context.Background(), want, dir, "", nil)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if res.Path == dest {
		t.Errorf("Path = %q, want a name that does not collide with the occupied one", res.Path)
	}
	if _, statErr := os.Stat(res.Path); statErr != nil {
		t.Errorf("the download should exist at %q: %v", res.Path, statErr)
	}
}

// TestDownloadNeverOverwritesADifferentFile verifies the collision guarantee
// end to end: a different file already carrying the name the download would take
// is left byte-for-byte intact, and the new file lands beside it.
func TestDownloadNeverOverwritesADifferentFile(t *testing.T) {
	payload := []byte("%PDF-1.4 " + strings.Repeat("second edition ", 32))
	want := md5Hex(payload)
	srv := noDispositionCDN(t, want, payload)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()

	meta := &FileMeta{Author: "Jane Doe", Title: "Great Book", Year: "2020"}
	occupied := filepath.Join(dir, "Jane Doe - Great Book (2020).pdf")
	incumbent := []byte("%PDF-1.4 the first edition, which must survive")
	if err := os.WriteFile(occupied, incumbent, 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := c.Download(context.Background(), want, dir, "", meta)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if res.Path == occupied {
		t.Fatalf("Path = %q, want a discriminated name beside the incumbent", res.Path)
	}
	kept, err := os.ReadFile(occupied)
	if err != nil || string(kept) != string(incumbent) {
		t.Errorf("the incumbent file was disturbed: %q, err=%v", kept, err)
	}
	if !strings.Contains(filepath.Base(res.Path), want[:8]) {
		t.Errorf("Path = %q, want the content digest as the discriminator", res.Path)
	}
}

// TestStreamToPartAndVerifyRenameError covers the rename-failure branch: a valid
// transfer whose destination directory does not exist surfaces the error and
// keeps the partial for a retry.
func TestStreamToPartAndVerifyRenameError(t *testing.T) {
	c := newTestClient(staticMirrors{})
	dir := t.TempDir()
	part := filepath.Join(dir, "x.part")
	dest := filepath.Join(dir, "missing-subdir", "dest.pdf")
	if _, _, err := c.streamToPartAndVerify(part, dest, "", strings.NewReader("x"), streamOpts{}); err == nil {
		t.Error("a rename into a nonexistent directory should fail")
	}
	if _, statErr := os.Stat(part); statErr != nil {
		t.Error("the partial should be kept when only the rename failed")
	}
}

// TestDownloadSizeCapContentLength verifies DownloadSizeCapContentLength.
func TestDownloadSizeCapContentLength(t *testing.T) {
	// Payload small enough to carry an explicit Content-Length header, but larger
	// than the configured cap: the download must fail before touching the disk.
	payload := []byte("%PDF-1.4 " + strings.Repeat("x", 500))
	srv := downloadTestServer(t, payload)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	c.maxDownloadBytes = 100
	dir := t.TempDir()
	_, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", dir, "", nil)
	if err == nil {
		t.Fatal("Content-Length above cap should fail")
	}
	// The sentinel, not just "an error": a cap breach and an out-of-disk-space
	// failure both leave an empty directory, and only the sentinel tells the caller
	// which one happened.
	if !errors.Is(err, errDownloadTooLarge) {
		t.Errorf("err = %v, want errDownloadTooLarge", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("%d entries left in dir, want 0 (neither file nor temp)", len(entries))
	}
}

// TestDownloadSizeCapStream verifies DownloadSizeCapStream.
func TestDownloadSizeCapStream(t *testing.T) {
	// Payload larger than the server's buffer (>2048 B) so the response is sent
	// chunked with no Content-Length: the cap must be enforced while streaming.
	payload := []byte("%PDF-1.4 " + strings.Repeat("x", 4000))
	srv := downloadTestServer(t, payload)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	c.maxDownloadBytes = 100
	dir := t.TempDir()
	_, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", dir, "", nil)
	if err == nil {
		t.Fatal("streamed body above cap should fail")
	}
	// The streaming cap is enforced by a different branch from the Content-Length
	// one, and it is the branch that also has to delete the .part it had begun
	// writing; assert the sentinel so the two paths cannot diverge unnoticed.
	if !errors.Is(err, errDownloadTooLarge) {
		t.Errorf("err = %v, want errDownloadTooLarge", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("%d entries left in dir, want 0 (neither file nor temp)", len(entries))
	}
}

// TestDownloadDiskCheck verifies DownloadDiskCheck.
func TestDownloadDiskCheck(t *testing.T) {
	payload := []byte("%PDF-1.4 fake book content")
	srv := downloadTestServer(t, payload)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	orig := freeSpaceFn
	freeSpaceFn = func(string) (uint64, error) { return 1, nil } // simulate a nearly full disk
	t.Cleanup(func() { freeSpaceFn = orig })
	dir := t.TempDir()
	if _, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", dir, "", nil); err == nil {
		t.Fatal("insufficient free disk space should fail")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("%d entries left in dir, want 0 (neither file nor temp)", len(entries))
	}
}

// TestDownloadUnderCap verifies DownloadUnderCap.
func TestDownloadUnderCap(t *testing.T) {
	// Regression: a normal download comfortably under the cap still succeeds.
	payload := []byte("%PDF-1.4 fake book content")
	srv := downloadTestServer(t, payload)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	c.maxDownloadBytes = 1 << 20 // 1 MiB, far above the payload
	dir := t.TempDir()
	res, err := c.Download(context.Background(), md5Hex(payload), dir, "", nil)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if res.SizeBytes != int64(len(payload)) {
		t.Errorf("SizeBytes = %d, want %d", res.SizeBytes, len(payload))
	}
}

// TestLooksLikeHTML covers LooksLikeHTML with table-driven subtests.
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

// TestDownloadProgressCallback verifies DownloadProgressCallback.
func TestDownloadProgressCallback(t *testing.T) {
	payload := []byte("%PDF-1.4 some fake but non-trivial book payload for progress")
	srv := downloadTestServer(t, payload)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()

	type call struct{ done, total int64 }
	var calls []call
	res, err := c.Download(context.Background(), md5Hex(payload), dir, "", nil, func(done, total int64) {
		calls = append(calls, call{done, total})
	})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if len(calls) == 0 {
		t.Fatal("progress callback was never invoked, want at least one call")
	}
	last := calls[len(calls)-1]
	if last.done != int64(len(payload)) {
		t.Errorf("last progress done = %d, want %d", last.done, len(payload))
	}
	if last.total != int64(len(payload)) {
		t.Errorf("last progress total = %d, want %d", last.total, len(payload))
	}
	if last.done != last.total {
		t.Errorf("last progress done = %d, total = %d, want done == total", last.done, last.total)
	}
	if res.SizeBytes != int64(len(payload)) {
		t.Errorf("SizeBytes = %d, want %d", res.SizeBytes, len(payload))
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

// partPathFor mirrors the stable partial path the downloader uses for an md5
// served by the libgen source (the source that wins the md5 chain in these tests).
// The partial path embeds the serving source's name so each source owns a distinct
// .part.
func partPathFor(dir, md5 string) string {
	return filepath.Join(dir, ".libgen-mcp-libgen-"+md5+".part")
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

// TestDownloadVerifiesMD5Match verifies DownloadVerifiesMD5Match.
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

	res, err := c.Download(context.Background(), want, dir, "", nil)
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

// TestDownloadMD5Mismatch verifies DownloadMD5Mismatch.
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

	_, err := c.Download(context.Background(), want, dir, "", nil)
	if err == nil {
		t.Fatal("md5 mismatch should fail")
	}
	// The sentinel distinguishes "the bytes were wrong" from every other late
	// failure that also cleans up after itself (a rename failure, a disk error).
	// It is the difference between "this mirror served a corrupt copy" and "your
	// filesystem is broken", and nothing else in the suite pins it.
	if !errors.Is(err, errIntegrityCheckFailed) {
		t.Errorf("err = %v, want errIntegrityCheckFailed", err)
	}
	// An integrity failure happened with bytes already flowing, so it must NOT be
	// retryable: re-running the same schedule against a mirror serving a corrupt
	// copy just downloads the corruption again.
	if errors.Is(err, errStartFailed) {
		t.Error("an integrity failure was tagged retryable; the retry schedule will re-fetch the bad copy")
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

// TestDownloadResume verifies DownloadResume.
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

	res, err := c.Download(context.Background(), want, dir, "", nil)
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

// TestDownloadResumeServerIgnoresRange verifies DownloadResumeServerIgnoresRange.
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

	res, err := c.Download(context.Background(), want, dir, "", nil)
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
			res, err := c.Download(context.Background(), want, dir, "", nil)
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

// TestPartialLockReleased verifies the refcounted partial-lock map does not leak:
// after several sequential downloads of distinct md5s complete, no lock entries
// remain. This guards against the previous sync.Map that grew unbounded.
func TestPartialLockReleased(t *testing.T) {
	dir := t.TempDir()
	c := newTestClient(staticMirrors{}) // mirror set is swapped per download below
	const n = 5
	for i := range n {
		payload := fmt.Appendf(nil, "%%PDF-1.4 distinct payload %d %s", i, strings.Repeat("x", 32))
		want := md5Hex(payload)
		srv := md5CDNServer(t, want, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="book.pdf"`)
			w.Write(payload)
		})
		c.mirrors = staticMirrors{srv.URL}
		res, err := c.Download(context.Background(), want, dir, fmt.Sprintf("book-%d.pdf", i), nil)
		srv.Close()
		if err != nil {
			t.Fatalf("Download(%d) error = %v", i, err)
		}
		if !res.Verified {
			t.Errorf("Download(%d) Verified = false, want true", i)
		}
	}
	if got := c.partialLockCount(); got != 0 {
		t.Errorf("partialLockCount() = %d after %d downloads, want 0 (lock map leaked)", got, n)
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

	res, err := c.Download(context.Background(), want, dir, "", nil)
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

// TestDownloadRejectsHTMLResponse verifies DownloadRejectsHTMLResponse.
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
	dir := t.TempDir()
	_, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", dir, "", nil)
	if err == nil {
		t.Fatal("an HTML response should fail")
	}
	// The Content-Type gate must name what it rejected. Without this the branch is
	// satisfied by any failure at all, including one raised before the response was
	// ever inspected.
	if !strings.Contains(err.Error(), "HTML") && !strings.Contains(err.Error(), "html") {
		t.Errorf("err = %v, want it to name the HTML page it refused", err)
	}
	// A mirror that answers with an error page is answering, not failing to start:
	// tagging this errStartFailed would spend the whole retry schedule re-fetching
	// the same page before the chain is allowed to move to the next source.
	if errors.Is(err, errStartFailed) {
		t.Error("an HTML error page was tagged retryable; the retry schedule will re-fetch it")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("%d entries left in dir, want 0 after refusing an HTML page", len(entries))
	}
}

// partialFailCDN serves content over a hijacked connection: it declares a
// Content-Length of len(content) but only writes the first half before closing,
// so the client sees an unexpected EOF and keeps the partial for a later resume.
// The first half is >512 bytes so the HTML-sniff Peek succeeds and the failure
// occurs while streaming (leaving a .part) rather than at validation.
func partialFailCDN(t *testing.T, content []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("ResponseWriter is not a Hijacker")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		header := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nContent-Length: %d\r\n\r\n", len(content))
		if _, werr := conn.Write([]byte(header)); werr != nil {
			return
		}
		_, _ = conn.Write(content[:len(content)/2]) // partial body, then close mid-stream
	}))
}

// rangeAwareCDN serves content in full on a plain GET, or honors a Range request
// with a 206 (appending onto whatever the client already has). It records whether
// it ever saw a Range header.
func rangeAwareCDN(t *testing.T, content []byte, sawRange *atomic.Bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		if rng := r.Header.Get("Range"); rng != "" {
			sawRange.Store(true)
			start := rangeStart(t, rng)
			if start < 0 || start > int64(len(content)) {
				http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(content)-1, len(content)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(content[start:])
			return
		}
		w.Write(content)
	}))
}

// TestDownloadNoCrossSourceResumeContamination is the regression test for the
// cross-source partial contamination bug. Two sources serve DIFFERENT byte streams
// for the SAME logical item (a DOI, so VerifyMD5 is false for both — the vulnerable
// case). Source A streams a partial then fails mid-stream, leaving a .part; the
// chain advances to source B, which serves different, complete content and honors
// Range requests with a 206. Because each source now owns a distinct .part path
// (keyed by source name), B must start fresh — it must NOT resume from A's bytes
// and append its own onto them. The final file must equal B's FULL content.
func TestDownloadNoCrossSourceResumeContamination(t *testing.T) {
	contentA := []byte("%PDF-1.4 SOURCE-A " + strings.Repeat("aaaa", 512)) // >1KB; half is >512
	contentB := []byte("%PDF-1.7 SOURCE-B " + strings.Repeat("bbbb", 300)) // different length and bytes

	srvA := partialFailCDN(t, contentA)
	defer srvA.Close()
	var bSawRange atomic.Bool
	srvB := rangeAwareCDN(t, contentB, &bSawRange)
	defer srvB.Close()

	// VerifyMD5 is false for both: this is the vulnerable case (DOI-keyed sources),
	// where no digest catches a cross-source append.
	c := newTestClient(staticMirrors{})
	c.sources = []DownloadSource{
		stubSource{name: "srca", supports: true, resolved: Resolved{FileURL: srvA.URL + "/file", VerifyMD5: false}},
		stubSource{name: "srcb", supports: true, resolved: Resolved{FileURL: srvB.URL + "/file", VerifyMD5: false}},
	}
	dir := t.TempDir()

	res, err := c.DownloadItem(context.Background(), Item{DOI: "10.1234/cross.source"}, dir, "out.pdf")
	if err != nil {
		t.Fatalf("DownloadItem() error = %v", err)
	}
	if res.Source != "srcb" {
		t.Errorf("Source = %q, want %q (A fails, B serves)", res.Source, "srcb")
	}
	// The core assertion: B started from zero and served its full content, rather
	// than appending onto A's leftover bytes (which would corrupt the file).
	data, rerr := os.ReadFile(res.Path)
	if rerr != nil {
		t.Fatalf("reading final file: %v", rerr)
	}
	if string(data) != string(contentB) {
		t.Fatalf("final content is not B's full content: got len=%d, want len=%d (cross-source append?)", len(data), len(contentB))
	}
	if bSawRange.Load() {
		t.Error("source B received a Range request: it resumed from A's partial (.part not distinct)")
	}
	// A wrote to its own distinct, source-keyed path — proven above by B never
	// receiving a Range request — and that abandoned partial is swept once B
	// delivers the file (see TestDownloadSweepsPartialsOfFailedLegs).
	item := Item{DOI: "10.1234/cross.source"}
	aPart := filepath.Join(dir, ".libgen-mcp-srca-"+partialKey(item, Resolved{})+".part")
	if _, statErr := os.Stat(aPart); !os.IsNotExist(statErr) {
		t.Errorf("A's abandoned .part still at %s (stat err = %v), want it swept", aPart, statErr)
	}
}

// TestDownloadSweepsPartialsOfFailedLegs verifies that once any source completes
// the download, the partials left behind by the legs that failed before it are
// removed. A partial is kept while the item is still unresolved, because the next
// call can resume from it; once the item is on disk it is dead weight the caller
// has to clean up by hand, in the directory they asked the file to land in.
func TestDownloadSweepsPartialsOfFailedLegs(t *testing.T) {
	contentA := []byte("%PDF-1.4 SOURCE-A " + strings.Repeat("aaaa", 512))
	contentB := []byte("%PDF-1.7 SOURCE-B " + strings.Repeat("bbbb", 300))

	srvA := partialFailCDN(t, contentA)
	defer srvA.Close()
	var bSawRange atomic.Bool
	srvB := rangeAwareCDN(t, contentB, &bSawRange)
	defer srvB.Close()

	c := newTestClient(staticMirrors{})
	c.sources = []DownloadSource{
		stubSource{name: "srca", supports: true, resolved: Resolved{FileURL: srvA.URL + "/file"}},
		stubSource{name: "srcb", supports: true, resolved: Resolved{FileURL: srvB.URL + "/file"}},
	}
	dir := t.TempDir()

	res, err := c.DownloadItem(context.Background(), Item{DOI: "10.1234/sweep"}, dir, "out.pdf")
	if err != nil {
		t.Fatalf("DownloadItem() error = %v", err)
	}
	if res.Source != "srcb" {
		t.Fatalf("Source = %q, want srcb (A fails, B serves)", res.Source)
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatalf("reading download dir: %v", rerr)
	}
	var leftovers []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".part") {
			leftovers = append(leftovers, e.Name())
		}
	}
	if len(leftovers) != 0 {
		t.Errorf("partials left behind after a successful download: %v", leftovers)
	}
	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want 1 (the downloaded file)", len(entries))
	}
}

// TestDownloadKeepsPartialsWhenEveryLegFails verifies the other half of the sweep
// rule: while the item is still unresolved, every leg's partial survives so a
// later call can resume from it.
func TestDownloadKeepsPartialsWhenEveryLegFails(t *testing.T) {
	srvA := partialFailCDN(t, []byte("%PDF-1.4 SOURCE-A "+strings.Repeat("aaaa", 512)))
	defer srvA.Close()
	srvB := partialFailCDN(t, []byte("%PDF-1.7 SOURCE-B "+strings.Repeat("bbbb", 512)))
	defer srvB.Close()

	c := newTestClient(staticMirrors{})
	c.sources = []DownloadSource{
		stubSource{name: "srca", supports: true, resolved: Resolved{FileURL: srvA.URL + "/file"}},
		stubSource{name: "srcb", supports: true, resolved: Resolved{FileURL: srvB.URL + "/file"}},
	}
	dir := t.TempDir()

	item := Item{DOI: "10.1234/keep"}
	if _, err := c.DownloadItem(context.Background(), item, dir, "out.pdf"); err == nil {
		t.Fatal("DownloadItem() error = nil, want a failure (every source truncates)")
	}
	// Each source owns its own partial, and both must survive: either one can be
	// the offset a later call resumes from.
	for _, name := range []string{"srca", "srcb"} {
		part := filepath.Join(dir, ".libgen-mcp-"+name+"-"+partialKey(item, Resolved{})+".part")
		if _, statErr := os.Stat(part); statErr != nil {
			t.Errorf("%s's partial missing at %s: %v (an unresolved item must stay resumable)", name, part, statErr)
		}
	}
}

// TestSweepPartialsOnAClientThatNeverLocked verifies the sweep works on a client
// whose partial-lock table has never been touched — the ordinary case for a
// download whose first leg succeeds, where nothing else has taken a lock yet.
func TestSweepPartialsOnAClientThatNeverLocked(t *testing.T) {
	c := newTestClient(staticMirrors{})
	part := filepath.Join(t.TempDir(), ".libgen-mcp-srca-fresh.part")
	if err := os.WriteFile(part, []byte("partial bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &attemptedPartials{}
	a.add(part)

	c.sweepPartials(a)
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Errorf("partial still present after the sweep (stat err = %v)", err)
	}
	if got := c.partialLockCount(); got != 0 {
		t.Errorf("partialLockCount = %d, want 0 (the sweep leaked a lock entry)", got)
	}
}

// TestSweepPartialsLeavesOneAnotherDownloadHolds verifies that the sweep never
// unlinks a partial another download is streaming to. The partial path is
// deterministic, so a second call for the same item in the same directory can be
// resuming from the very file this call is about to discard; removing it under
// that download would fail its final rename and throw away its resume state. A
// partial whose lock is held is left alone, and swept on a later call.
func TestSweepPartialsLeavesOneAnotherDownloadHolds(t *testing.T) {
	c := newTestClient(staticMirrors{})
	part := filepath.Join(t.TempDir(), ".libgen-mcp-srca-held.part")
	if err := os.WriteFile(part, []byte("partial bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	release := c.acquirePartialLock(part) // stand in for the download that owns it
	held := &attemptedPartials{}
	held.add(part)
	c.sweepPartials(held)
	if _, err := os.Stat(part); err != nil {
		t.Fatalf("swept a partial another download holds: %v", err)
	}
	release()

	free := &attemptedPartials{}
	free.add(part)
	c.sweepPartials(free)
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Errorf("partial still present after its holder released (stat err = %v)", err)
	}
	if got := c.partialLockCount(); got != 0 {
		t.Errorf("partialLockCount = %d, want 0 (the sweep leaked a lock entry)", got)
	}
}

// errReader is an io.Reader that always fails, used to drive the read-error
// branches of body-consuming helpers.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("simulated read error") }

// failWriter is an io.Writer that always fails, used to drive the write-error
// branch of the resume re-hash in openPartForStream.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("simulated write error") }

// TestParseContentRangeStart covers every branch of the Content-Range parser: a
// well-formed header, a wrong prefix, a missing dash, an unparseable offset and
// an empty header.
func TestParseContentRangeStart(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"bytes 100-199/200", 100, true},
		{"bytes 0-0/1", 0, true},
		{"items 0-1/2", 0, false},    // wrong unit prefix
		{"bytes 100", 0, false},      // no dash
		{"bytes abc-9/10", 0, false}, // unparseable start
		{"", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseContentRangeStart(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parseContentRangeStart(%q) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// TestFilenameFromDisposition covers the empty header, a valid attachment
// filename, a malformed media type (ParseMediaType error) and a header with no
// filename parameter.
func TestFilenameFromDisposition(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{`attachment; filename="book.pdf"`, "book.pdf"},
		{"attachment; badparam", ""}, // parameter without '=' is malformed
		{"attachment", ""},           // no filename parameter
	}
	for _, tc := range cases {
		if got := filenameFromDisposition(tc.in); got != tc.want {
			t.Errorf("filenameFromDisposition(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestShouldEmit covers the elapsed-interval branch, the byte-advance branch, and
// the throttled (no emit) case of the progress writer.
func TestShouldEmit(t *testing.T) {
	if pw := (&progressWriter{lastAt: time.Now().Add(-time.Hour)}); !pw.shouldEmit() {
		t.Error("shouldEmit() = false after the progress interval elapsed, want true")
	}
	if pw := (&progressWriter{lastAt: time.Now(), total: 100, done: 10, lastDone: 0}); !pw.shouldEmit() {
		t.Error("shouldEmit() = false after advancing a full fraction, want true")
	}
	if pw := (&progressWriter{lastAt: time.Now(), total: 100, done: 1, lastDone: 0}); pw.shouldEmit() {
		t.Error("shouldEmit() = true while recent and barely advanced, want false")
	}
}

// expectFetchFileError fails the test if fetchFile unexpectedly succeeded, closing
// the body if one came back so the check is body-leak clean.
func expectFetchFileError(t *testing.T, resp *http.Response, err error, msg string) {
	t.Helper()
	if err == nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		t.Error(msg)
	}
}

// TestFetchFileLimiterError covers fetchFile's rate-limiter guard: a limiter with
// a zero burst can never grant a token, so Wait fails before any request is built.
func TestFetchFileLimiterError(t *testing.T) {
	c := newTestClient(staticMirrors{})
	c.limiter = rate.NewLimiter(rate.Every(time.Hour), 0)
	resp, err := c.fetchFile(context.Background(), "http://127.0.0.1:0/x", 0, nil)
	expectFetchFileError(t, resp, err, "fetchFile should fail when the limiter cannot grant a token")
}

// TestFetchFileRequestErrors covers fetchFile's request-construction failure (an
// invalid URL) and its transport failure (a dead address).
func TestFetchFileRequestErrors(t *testing.T) {
	c := newTestClient(staticMirrors{})
	resp, err := c.fetchFile(context.Background(), "http://\x7f/x", 0, nil)
	expectFetchFileError(t, resp, err, "fetchFile with an invalid URL should fail at request construction")
	resp, err = c.fetchFile(context.Background(), "http://127.0.0.1:0/x", 0, nil)
	expectFetchFileError(t, resp, err, "fetchFile against a dead address should fail at transport")
}

// TestFetchFileAppliesHeadersAndRange covers the source-header loop and the resume
// Range header: fetchFile must forward a supplied header and, when resuming, add a
// bytes= Range for the CDN.
func TestFetchFileAppliesHeadersAndRange(t *testing.T) {
	var gotReferer, gotRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("Referer")
		gotRange = r.Header.Get("Range")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := newTestClient(staticMirrors{})
	resp, err := c.fetchFile(context.Background(), srv.URL, 5, http.Header{"Referer": {"http://example.test/"}})
	if err != nil {
		t.Fatalf("fetchFile() error = %v", err)
	}
	_ = resp.Body.Close()
	if gotReferer != "http://example.test/" {
		t.Errorf("Referer = %q, want %q", gotReferer, "http://example.test/")
	}
	if gotRange != "bytes=5-" {
		t.Errorf("Range = %q, want %q", gotRange, "bytes=5-")
	}
}

// TestValidateFileResponsePeekError covers validateFileResponse's peek-error
// branch: a body that fails on read (not EOF) surfaces as an error.
func TestValidateFileResponsePeekError(t *testing.T) {
	c := newTestClient(staticMirrors{})
	resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(errReader{})}
	if _, _, err := c.validateFileResponse(resp, -1); err == nil {
		t.Error("validateFileResponse should surface a body read error")
	}
}

// TestValidateFileResponseNoBytes covers validateFileResponse's no-first-byte
// branch: a 2xx whose body yields no bytes at all is a "did not begin" failure
// (tagged for the start-retry schedule) rather than a valid empty file.
func TestValidateFileResponseNoBytes(t *testing.T) {
	c := newTestClient(staticMirrors{})
	resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}
	_, _, err := c.validateFileResponse(resp, -1)
	if err == nil {
		t.Fatal("validateFileResponse should fail when the response yields no bytes")
	}
	if !errors.Is(err, errStartFailed) {
		t.Errorf("err = %v, want it tagged as a start failure", err)
	}
}

// TestStreamToPartOpenError covers openPartForStream's OpenFile failure (and its
// propagation through streamToPartAndVerify): a partial path that is an existing
// directory cannot be opened for writing.
func TestStreamToPartOpenError(t *testing.T) {
	c := newTestClient(staticMirrors{})
	dir := t.TempDir()
	_, _, err := c.streamToPartAndVerify(dir, filepath.Join(dir, "dest"), "", strings.NewReader("x"), streamOpts{})
	if err == nil {
		t.Error("streamToPartAndVerify should fail when the partial path is a directory")
	}
}

// TestOpenPartForStreamRehashError covers the resume re-hash failure: when copying
// the existing bytes into the digest fails, the partial is closed and the error is
// wrapped for a later resume.
func TestOpenPartForStreamRehashError(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, "p.part")
	if err := os.WriteFile(part, []byte("existing bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openPartForStream(part, streamOpts{resume: true, existingSize: 14}, failWriter{}); err == nil {
		t.Error("openPartForStream should fail when re-hashing the partial errors")
	}
}

// TestStreamToPartTruncated covers the short-read branch: when contentLength is
// known but the body delivers fewer bytes, the transfer is a truncated download
// and the partial is kept so a later call can resume.
func TestStreamToPartTruncated(t *testing.T) {
	c := newTestClient(staticMirrors{})
	dir := t.TempDir()
	part := filepath.Join(dir, "p.part")
	_, _, err := c.streamToPartAndVerify(part, filepath.Join(dir, "dest"), "", strings.NewReader("short"), streamOpts{contentLength: 999})
	if err == nil {
		t.Fatal("streamToPartAndVerify should fail on a truncated transfer")
	}
	if _, statErr := os.Stat(part); os.IsNotExist(statErr) {
		t.Error("a truncated transfer should keep the .part for a later resume")
	}
}

// TestResolveGetURLNoLink covers ResolveGetURL's extraction-failure branch: an
// ads.php page without a get.php link yields an error.
func TestResolveGetURLNoLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "<html><body>no download link here</body></html>")
	}))
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	if _, _, err := c.ResolveGetURL(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee"); err == nil {
		t.Error("ResolveGetURL should fail when ads.php carries no get.php link")
	}
}

// TestDownloadItemNoSupportingSource covers the "no source supports the item"
// branch: an item with neither an md5 nor a DOI is claimed by no configured
// source, so DownloadItem fails before any resolution is attempted.
func TestDownloadItemNoSupportingSource(t *testing.T) {
	c := newTestClient(staticMirrors{})
	_, err := c.DownloadItem(context.Background(), Item{}, t.TempDir(), "")
	if err == nil {
		t.Fatal("DownloadItem with no supporting source should fail")
	}
	if !strings.Contains(err.Error(), "no download source supports") {
		t.Errorf("err = %v, want a 'no download source supports' message", err)
	}
}

// TestStreamResolvedFetchError covers streamResolved's fetch-failure branch: when
// the resolved file URL is unreachable, the download fails.
func TestStreamResolvedFetchError(t *testing.T) {
	c := newTestClient(staticMirrors{})
	c.sources = []DownloadSource{stubSource{name: "dead", supports: true, resolved: Resolved{FileURL: "http://127.0.0.1:0/f"}}}
	if _, err := c.DownloadItem(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}, t.TempDir(), ""); err == nil {
		t.Error("download should fail when the resolved file URL is unreachable")
	}
}

// TestStreamResolvedMkdirError covers streamResolved's MkdirAll-failure branch:
// when the destination directory cannot be created (a regular file blocks the
// path), the download fails after a valid transfer is fetched and validated.
func TestStreamResolvedMkdirError(t *testing.T) {
	payload := []byte("%PDF-1.4 mkdir clash payload")
	cdn := fileCDN(t, payload, "")
	defer cdn.Close()
	c := newTestClient(staticMirrors{})
	c.sources = []DownloadSource{stubSource{name: "cdn", supports: true, resolved: Resolved{FileURL: cdn.URL + "/file"}}}

	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	badDir := filepath.Join(blocker, "sub") // a file, not a directory, sits in the path
	if _, err := c.DownloadItem(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}, badDir, ""); err == nil {
		t.Error("download should fail when the destination directory cannot be created")
	}
}

// cancelingSource is a DownloadSource that cancels the download context while
// resolving, then fails, letting a test drive DownloadItem's cancellation-aware
// break out of the source loop.
type cancelingSource struct{ cancel context.CancelFunc }

func (cancelingSource) Name() string       { return "canceling" }
func (cancelingSource) Supports(Item) bool { return true }
func (s cancelingSource) Resolve(context.Context, Item) (Resolved, error) {
	s.cancel()
	return Resolved{}, errors.New("boom")
}

// trackingSource records whether it was resolved, so a test can assert a later
// source in the chain was never reached.
type trackingSource struct{ ran *bool }

func (trackingSource) Name() string       { return "tracking" }
func (trackingSource) Supports(Item) bool { return true }
func (s trackingSource) Resolve(context.Context, Item) (Resolved, error) {
	*s.ran = true
	return Resolved{}, errors.New("nope")
}

// TestDownloadItemContextCanceledBreak covers the loop's cancellation guard: once
// the context is canceled, DownloadItem stops trying further sources rather than
// pressing on. The second source must never run.
func TestDownloadItemContextCanceledBreak(t *testing.T) {
	c := newTestClient(staticMirrors{})
	ctx, cancel := context.WithCancel(context.Background())
	var secondRan bool
	c.sources = []DownloadSource{
		cancelingSource{cancel: cancel},
		trackingSource{ran: &secondRan},
	}
	if _, err := c.DownloadItem(ctx, Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}, t.TempDir(), ""); err == nil {
		t.Fatal("DownloadItem should fail after the context is canceled")
	}
	if secondRan {
		t.Error("the second source should not have been reached after cancellation")
	}
}

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

// serveStaticBytes returns an httptest server that serves content at any path.
func serveStaticBytes(t *testing.T, content []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(content)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestDownloadItemRestrictToSource verifies that Item.Source restricts the
// download to a single named source instead of walking the whole chain, and
// that an unknown/disabled source name is a clear error.
func TestDownloadItemRestrictToSource(t *testing.T) {
	srvA := serveStaticBytes(t, []byte("%PDF-1.4 AAAA "+strings.Repeat("a", 600)))
	srvB := serveStaticBytes(t, []byte("%PDF-1.7 BBBB "+strings.Repeat("b", 600)))

	newClient := func() *Client {
		c := newTestClient(staticMirrors{})
		c.sources = []DownloadSource{
			stubSource{name: "srca", supports: true, resolved: Resolved{FileURL: srvA.URL + "/f", VerifyMD5: false}},
			stubSource{name: "srcb", supports: true, resolved: Resolved{FileURL: srvB.URL + "/f", VerifyMD5: false}},
		}
		return c
	}

	// Restrict to srcb even though srca is first and would serve on its own.
	res, err := newClient().DownloadItem(context.Background(), Item{DOI: "10.1/x", Source: "srcb"}, t.TempDir(), "o.pdf")
	if err != nil {
		t.Fatalf("restrict to srcb: %v", err)
	}
	if res.Source != "srcb" {
		t.Errorf("Source = %q, want srcb", res.Source)
	}

	// Restrict to srca.
	res, err = newClient().DownloadItem(context.Background(), Item{DOI: "10.1/x", Source: "srca"}, t.TempDir(), "o.pdf")
	if err != nil {
		t.Fatalf("restrict to srca: %v", err)
	}
	if res.Source != "srca" {
		t.Errorf("Source = %q, want srca", res.Source)
	}

	// An unknown / disabled source name is a clear error, not a silent fallback.
	_, uerr := newClient().DownloadItem(context.Background(), Item{DOI: "10.1/x", Source: "nope"}, t.TempDir(), "o.pdf")
	if uerr == nil || !strings.Contains(uerr.Error(), "not enabled") {
		t.Errorf("unknown source error = %v, want it to mention 'not enabled'", uerr)
	}
}

// TestDownloadItemRestrictIncompatibleSource verifies that restricting to a
// source that does not support the item type returns a targeted error rather
// than the generic "no source supports" message.
func TestDownloadItemRestrictIncompatibleSource(t *testing.T) {
	c := newTestClient(staticMirrors{})
	c.sources = []DownloadSource{stubSource{name: "onlydoi", supports: false}}
	_, err := c.DownloadItem(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee", Source: "onlydoi"}, t.TempDir(), "")
	if err == nil || !strings.Contains(err.Error(), "cannot serve") {
		t.Errorf("incompatible source error = %v, want it to mention 'cannot serve'", err)
	}
}

// unpaywallDownloadServer serves the on-demand Unpaywall flow for a DOI download: a
// lookup path returns OA JSON pointing at its own /pdf endpoint, which serves the
// given PDF bytes. It records how many lookups (not /pdf fetches) it received so a
// test can assert Unpaywall was — or was not — consulted.
func unpaywallDownloadServer(t *testing.T, pdf []byte) (base string, lookups *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/pdf", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdf)
	})
	// The DOI lookup is served at any other path (the source builds /v2/<doi>).
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"is_oa":true,"best_oa_location":{"url_for_pdf":%q}}`, srv.URL+"/pdf")
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, &hits
}

// newPerCallEmailClient builds a client that has NO configured Unpaywall email and
// whose only configured source is "unpaywall" (disabled without an email, so the
// chain starts empty). The on-demand Unpaywall base URL is pointed at the test
// server so the ad-hoc prepended source resolves against it rather than the live API.
func newPerCallEmailClient(t *testing.T, unpaywallBase string) *Client {
	t.Helper()
	cfg := &config.Config{
		Timeout:                5 * time.Second,
		RateRPS:                1000,
		RateBurst:              100,
		RetryAttempts:          1,
		MaxConcurrentDownloads: 2,
		UnpaywallEmail:         "", // no configured email → unpaywall not in the default chain
		Sources:                []string{"unpaywall"},
	}
	c := New(staticMirrors{}, cfg, WithUnpaywallBaseURL(unpaywallBase))
	c.backoffBase = time.Millisecond
	return c
}

// TestDownloadItem_PerCallEmailPrependsUnpaywall verifies that a per-call email
// pulls the Unpaywall source into a DOI download even when the server configured no
// email (Unpaywall is absent from the default chain): with Item.Email set the
// download succeeds via the ad-hoc Unpaywall source; with Item.Email empty the same
// DOI download does NOT consult Unpaywall (0 lookups) and fails with no usable source,
// proving the default behavior is unchanged.
func TestDownloadItem_PerCallEmailPrependsUnpaywall(t *testing.T) {
	pdf := []byte("%PDF-1.4 open-access article via unpaywall")
	base, lookups := unpaywallDownloadServer(t, pdf)
	c := newPerCallEmailClient(t, base)

	// With a per-call email, the ad-hoc Unpaywall source is prepended and serves it.
	res, err := c.DownloadItem(context.Background(), Item{DOI: "10.1/x", Email: "caller@example.com"}, t.TempDir(), "")
	if err != nil {
		t.Fatalf("DownloadItem with a per-call email error = %v", err)
	}
	if res.Source != "unpaywall" {
		t.Errorf("Source = %q, want %q", res.Source, "unpaywall")
	}
	if res.SizeBytes != int64(len(pdf)) {
		t.Errorf("SizeBytes = %d, want %d", res.SizeBytes, len(pdf))
	}
	if lookups.Load() != 1 {
		t.Errorf("Unpaywall lookups = %d, want 1", lookups.Load())
	}

	// With no per-call email the chain is empty (unpaywall disabled): no lookup, error.
	lookups.Store(0)
	if _, noEmailErr := c.DownloadItem(context.Background(), Item{DOI: "10.1/x"}, t.TempDir(), ""); noEmailErr == nil {
		t.Fatal("DownloadItem for a DOI with no email should fail (no usable source)")
	}
	if lookups.Load() != 0 {
		t.Errorf("Unpaywall lookups = %d without a per-call email, want 0", lookups.Load())
	}
}

// srcNames returns the Name() of every source in a plain slice, for chain-shape
// assertions on withPerCallUnpaywall's return value.
func srcNames(ss []DownloadSource) []string {
	names := make([]string, len(ss))
	for i, s := range ss {
		names[i] = s.Name()
	}
	return names
}

// TestWithPerCallUnpaywall covers the guard branches of withPerCallUnpaywall that
// the end-to-end download test does not reach: when Unpaywall is already present in
// the chain the slice is returned unchanged (no duplicate ad-hoc prepend), and when
// the item lacks an email, lacks a DOI, or names a specific source the chain is
// likewise untouched. A final positive case confirms the prepend still happens.
func TestWithPerCallUnpaywall(t *testing.T) {
	c := newTestClient(staticMirrors{})
	withUP := []DownloadSource{stubSource{name: "unpaywall", supports: true}, stubSource{name: "libgen", supports: true}}
	plain := []DownloadSource{stubSource{name: "libgen", supports: true}}
	emailDOI := Item{DOI: "10.1/x", Email: "caller@example.com"}

	// Unpaywall already enabled: returned unchanged (its Resolve honors the email).
	if got := c.withPerCallUnpaywall(emailDOI, withUP); len(got) != 2 || got[0].Name() != "unpaywall" {
		t.Errorf("chain with unpaywall present should be unchanged, got %v", srcNames(got))
	}

	// No email, no DOI, or a named source: each leaves the chain untouched.
	unchanged := []Item{
		{DOI: "10.1/x"},               // no email
		{Email: "caller@example.com"}, // no DOI
		{DOI: "10.1/x", Email: "caller@example.com", Source: "libgen"}, // named source
	}
	for _, it := range unchanged {
		if got := c.withPerCallUnpaywall(it, plain); len(got) != 1 || got[0].Name() != "libgen" {
			t.Errorf("withPerCallUnpaywall(%+v) altered the chain: %v", it, srcNames(got))
		}
	}

	// Positive case: email + DOI + no named source + no unpaywall present → prepend.
	if got := c.withPerCallUnpaywall(emailDOI, plain); len(got) != 2 || got[0].Name() != "unpaywall" {
		t.Errorf("email+DOI+no-source should prepend unpaywall, got %v", srcNames(got))
	}
}

// TestResolveLink verifies the resolve-only path returns the first resolving
// source's URL, headers and flags without downloading, fails over past a source
// that errors, and errors when nothing supports the item.
func TestResolveLink(t *testing.T) {
	cfg := baseChainConfig()
	good := stubSource{
		name: "libgen", supports: true,
		resolved: Resolved{FileURL: "https://cdn/x.pdf", VerifyMD5: true, Ext: "pdf", Header: http.Header{"Referer": {"https://h/"}}},
	}

	c := New(staticMirrors{}, cfg, WithSources(good))
	r, err := c.ResolveLink(context.Background(), Item{MD5: "abc"})
	if err != nil {
		t.Fatalf("ResolveLink: %v", err)
	}
	if r.URL != "https://cdn/x.pdf" || r.Source != "libgen" || !r.VerifyMD5 || r.Ext != "pdf" {
		t.Errorf("resolved = %+v", r)
	}
	if r.Header.Get("Referer") != "https://h/" {
		t.Error("required header not carried through")
	}

	bad := stubSource{name: "bad", supports: true, resolveErr: errors.New("boom")}
	c2 := New(staticMirrors{}, cfg, WithSources(bad, good))
	if r2, err2 := c2.ResolveLink(context.Background(), Item{MD5: "abc"}); err2 != nil || r2.Source != "libgen" {
		t.Errorf("failover: got %+v err=%v", r2, err2)
	}

	c3 := New(staticMirrors{}, cfg, WithSources(stubSource{name: "x", supports: false}))
	if _, err3 := c3.ResolveLink(context.Background(), Item{MD5: "abc"}); err3 == nil {
		t.Error("want error when no source supports the item")
	}

	// A named source that is not in the chain surfaces the selectSources error
	// straight out of ResolveLink (before any resolution is attempted).
	if _, err4 := c.ResolveLink(context.Background(), Item{MD5: "abc", Source: "nope"}); err4 == nil {
		t.Error("want error when the named source is not enabled")
	}

	// When every supporting source errors, the per-source errors are joined and
	// returned.
	c5 := New(staticMirrors{}, cfg, WithSources(bad))
	if _, err5 := c5.ResolveLink(context.Background(), Item{MD5: "abc"}); err5 == nil {
		t.Error("want joined error when all sources fail to resolve")
	}

	// A canceled context stops the failover loop after the first erroring source
	// rather than trying the rest.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	c6 := New(staticMirrors{}, cfg, WithSources(bad, good))
	r6, err6 := c6.ResolveLink(canceled, Item{MD5: "abc"})
	if err6 == nil {
		t.Error("want error when the context is canceled mid-chain")
	}
	if r6.Source == "libgen" {
		t.Error("a canceled context should stop before the second source resolves")
	}
}

// TestSelectSourcesUnpaywallHint verifies that asking for the unpaywall source when
// it is not enabled yields the actionable error naming its email gate.
func TestSelectSourcesUnpaywallHint(t *testing.T) {
	c := New(staticMirrors{}, baseChainConfig(), WithSources(stubSource{name: "libgen", supports: true}))

	if _, err := c.selectSources("scihub"); err == nil {
		t.Error("want error when a non-unpaywall source is not enabled")
	}

	_, err := c.selectSources("unpaywall")
	if err == nil {
		t.Fatal("want error when unpaywall is not enabled")
	}
	if !strings.Contains(err.Error(), "LIBGEN_MCP_UNPAYWALL_EMAIL") {
		t.Errorf("unpaywall error %q should point at the email gate", err)
	}
}

// TestPerCallCredentialsRespectTheSourceAllowlist verifies a per-call credential
// cannot bring back a source the deployment removed. LIBGEN_MCP_SOURCES is how an
// operator says which sources this server may use at all; a caller able to lift
// that by supplying a key or an email would make it advisory, which is the same
// hole the extra-sources never mode had.
func TestPerCallCredentialsRespectTheSourceAllowlist(t *testing.T) {
	cfg := &config.Config{
		DownloadDir: t.TempDir(), Timeout: time.Second, RateRPS: 1000, RateBurst: 10,
		RetryAttempts: 1,
		// The operator allows the catalog and nothing else.
		Sources: []string{"libgen"},
	}
	c := New(staticMirrors{"http://127.0.0.1:0"}, cfg)

	withKey := c.withPerCallAnnas(Item{MD5: "d64efd386ed7227592499460aca2044b", AnnasKey: "secret"}, c.sources)
	for _, s := range withKey {
		if s.Name() == "annas" {
			t.Error("a per-call Anna's key brought back a source the deployment removed")
		}
	}

	withEmail := c.withPerCallUnpaywall(Item{DOI: "10.1/x", Email: "someone@example.com"}, c.sources)
	for _, s := range withEmail {
		if s.Name() == "unpaywall" {
			t.Error("a per-call email brought back a source the deployment removed")
		}
	}
}

// TestPerCallEmailStillEnablesUnpaywallWhenAllowed verifies the intended case still
// works: unpaywall is absent because no contact email was configured, not because
// the operator excluded it, so a per-call email pulls it in.
func TestPerCallEmailStillEnablesUnpaywallWhenAllowed(t *testing.T) {
	cfg := &config.Config{
		DownloadDir: t.TempDir(), Timeout: time.Second, RateRPS: 1000, RateBurst: 10,
		RetryAttempts: 1, // no Sources list: every source allowed; no email: unpaywall inactive
	}
	c := New(staticMirrors{"http://127.0.0.1:0"}, cfg)

	got := c.withPerCallUnpaywall(Item{DOI: "10.1/x", Email: "someone@example.com"}, c.sources)
	var found bool
	for _, s := range got {
		if s.Name() == "unpaywall" {
			found = true
		}
	}
	if !found {
		t.Error("a per-call email should enable unpaywall when the deployment allows the source")
	}
}

// namedFailingSource fails to resolve and says which source it is, so a test can
// assert the chain reported the attempt under the right name.
type namedFailingSource struct{ name string }

func (s namedFailingSource) Name() string     { return s.name }
func (namedFailingSource) Supports(Item) bool { return true }
func (s namedFailingSource) Resolve(context.Context, Item) (Resolved, error) {
	return Resolved{}, errors.New("nothing here")
}

// TestDownloadLogsEachSourceAttempt verifies the chain says what it tried. Without
// it a slow or failed download is a black box: the record shows the total duration
// and nothing about which source spent it, which is the first question anyone asks
// when a download misbehaves.
func TestDownloadLogsEachSourceAttempt(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	c := newTestClient(staticMirrors{})
	c.sources = []DownloadSource{
		namedFailingSource{name: "first"},
		namedFailingSource{name: "second"},
	}
	_, _ = c.DownloadItem(context.Background(),
		Item{MD5: "d64efd386ed7227592499460aca2044b"}, t.TempDir(), "")

	log := buf.String()
	for _, want := range []string{`"source":"first"`, `"source":"second"`} {
		if !strings.Contains(log, want) {
			t.Errorf("the chain never logged an attempt for %s; log=%s", want, log)
		}
	}
	if !strings.Contains(log, "source failed, advancing") {
		t.Errorf("the chain never logged why it moved on; log=%s", log)
	}
}

// countingDeadSource never starts and counts how often it was asked to.
type countingDeadSource struct {
	name     string
	attempts *atomic.Int32
}

func (s countingDeadSource) Name() string     { return s.name }
func (countingDeadSource) Supports(Item) bool { return true }
func (s countingDeadSource) Resolve(context.Context, Item) (Resolved, error) {
	s.attempts.Add(1)
	// An unreachable host is what the schedule is built to outlast, so this is the
	// failure shape that triggers a retry rather than an immediate advance.
	return Resolved{FileURL: "http://127.0.0.1:0/x"}, nil
}

// TestChainDoesNotSpendTheFullScheduleOnEverySource verifies a failing source does
// not hold up the next one. The schedule exists to outlast a blip on the source
// that is going to serve the file; spending it on every source in turn makes a
// download another source would answer in seconds take a minute — measured at 235
// seconds for an item the catalog does not carry, of which under two were transfer.
func TestChainDoesNotSpendTheFullScheduleOnEverySource(t *testing.T) {
	payload := []byte("%PDF-1.4 " + strings.Repeat("served by the second source ", 20))
	want := md5Hex(payload)
	var hits atomic.Int32
	srv := flakyStartCDN(t, want, payload, 0, &hits)
	defer srv.Close()

	var attempts atomic.Int32
	c := newTestClient(staticMirrors{srv.URL})
	c.startRetryWaits = tinyWaits(5)
	c.stallTimeout = 2 * time.Second
	c.sources = []DownloadSource{
		countingDeadSource{name: "dead", attempts: &attempts},
		libgenSource{c: c},
	}

	if _, err := c.DownloadItem(context.Background(), Item{MD5: want}, t.TempDir(), ""); err != nil {
		t.Fatalf("the second source should have served the file: %v", err)
	}
	if got := attempts.Load(); got > 1 {
		t.Errorf("the dead source was attempted %d times while another source was waiting; want 1", got)
	}
}

// TestLastSourceStillGetsTheFullSchedule verifies the restraint does not cost the
// schedule its purpose. With nothing behind it, a source that would have recovered
// on a later attempt must still be given one: outlasting a transient failure is
// exactly what the schedule is for.
func TestLastSourceStillGetsTheFullSchedule(t *testing.T) {
	payload := []byte("%PDF-1.4 " + strings.Repeat("recovered on a later attempt ", 20))
	want := md5Hex(payload)
	var hits atomic.Int32
	srv := flakyStartCDN(t, want, payload, 2, &hits) // fails twice, then serves
	defer srv.Close()

	c := newTestClient(staticMirrors{srv.URL})
	c.startRetryWaits = tinyWaits(5)
	c.stallTimeout = 2 * time.Second

	if _, err := c.Download(context.Background(), want, t.TempDir(), "", nil); err != nil {
		t.Fatalf("the only source should have recovered on its third attempt: %v", err)
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("CDN hits = %d, want 3 — the last source must keep its retries", got)
	}
}

// TestRetryEverySourceRestoresTheOldBehavior verifies the escape hatch works, so a
// deployment that prefers every source to get the full schedule can have it.
func TestRetryEverySourceRestoresTheOldBehavior(t *testing.T) {
	payload := []byte("%PDF-1.4 " + strings.Repeat("second source ", 20))
	want := md5Hex(payload)
	var hits atomic.Int32
	srv := flakyStartCDN(t, want, payload, 0, &hits)
	defer srv.Close()

	var attempts atomic.Int32
	c := newTestClient(staticMirrors{srv.URL})
	c.startRetryWaits = tinyWaits(3)
	c.stallTimeout = 2 * time.Second
	c.retryEverySource = true
	c.sources = []DownloadSource{
		countingDeadSource{name: "dead", attempts: &attempts},
		libgenSource{c: c},
	}

	if _, err := c.DownloadItem(context.Background(), Item{MD5: want}, t.TempDir(), ""); err != nil {
		t.Fatalf("the second source should still have served the file: %v", err)
	}
	if got := attempts.Load(); got != 4 {
		t.Errorf("dead source attempts = %d, want 4 (one per wait plus the first)", got)
	}
}

// retryProbeSchedule is a two-wait start-retry schedule short enough for a unit
// test. newTestClient configures no waits at all, so a test about retrying has to
// supply its own or it silently measures nothing.
var retryProbeSchedule = []time.Duration{time.Millisecond, time.Millisecond}

// TestCleanMissIsNotRetried verifies a source that correctly answers "I do not
// hold this item" is asked exactly once. The start-retry schedule exists to
// outlast a transport blip; a clean miss is a settled answer, so re-asking only
// burns the schedule. Measured in a live evaluator run: a pinned source that did
// not hold the requested DOI spent 87.7s before reporting the miss.
func TestCleanMissIsNotRetried(t *testing.T) {
	var attempts atomic.Int32
	c := cooldownChainClient(countingSource{
		name:     "only",
		err:      notIndexed(errors.New(`"10.1/nope" is not held by this source`)),
		attempts: &attempts,
	})
	c.startRetryWaits = retryProbeSchedule

	if _, err := c.DownloadItem(context.Background(), Item{DOI: "10.1/nope"}, t.TempDir(), ""); err == nil {
		t.Fatal("a source that holds nothing must fail the download")
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("Resolve called %d time(s), want 1: a settled miss must not be retried", got)
	}
}

// TestCleanMissKeepsItsOwnDiagnosis verifies the error surfaced for a clean miss
// still says the source does not hold the item, rather than being relabeled as a
// connection failure. The retry wrapper's message blames "resolve/connect/
// first-byte", which is actively misleading when nothing failed to connect.
func TestCleanMissKeepsItsOwnDiagnosis(t *testing.T) {
	var attempts atomic.Int32
	c := cooldownChainClient(countingSource{
		name:     "only",
		err:      notIndexed(errors.New(`"10.1/nope" is not held by this source`)),
		attempts: &attempts,
	})
	c.startRetryWaits = retryProbeSchedule

	_, err := c.DownloadItem(context.Background(), Item{DOI: "10.1/nope"}, t.TempDir(), "")
	if err == nil {
		t.Fatal("a source that holds nothing must fail the download")
	}
	if !errors.Is(err, ErrNotIndexed) {
		t.Errorf("error lost its ErrNotIndexed tag: %v", err)
	}
	if strings.Contains(err.Error(), "could not be started after the retry schedule") {
		t.Errorf("a clean miss was reported as a start failure: %v", err)
	}
}

// TestUnavailableSourceStillGetsTheRetrySchedule guards the other side of the
// split: a genuine transport failure is exactly what the schedule is for, so
// narrowing the retry must not disable it.
func TestUnavailableSourceStillGetsTheRetrySchedule(t *testing.T) {
	var attempts atomic.Int32
	c := cooldownChainClient(countingSource{
		name:     "only",
		err:      unavailable(errors.New("dial tcp: connection refused")),
		attempts: &attempts,
	})
	c.startRetryWaits = retryProbeSchedule

	if _, err := c.DownloadItem(context.Background(), Item{DOI: "10.1/x"}, t.TempDir(), ""); err == nil {
		t.Fatal("an unreachable source must fail the download")
	}
	if got, want := int(attempts.Load()), len(retryProbeSchedule)+1; got != want {
		t.Errorf("Resolve called %d time(s), want %d: the schedule must still apply", got, want)
	}
}

// TestDownloadResultKeepsProvenanceOffTheWire pins the field tags that decide what
// a caller is told about where its file came from.
//
// Source used to be serialized, with a jsonschema description enumerating every
// source that could serve a file — and that description drifted twice, because a
// struct tag is prose to every compiler and linter in the build. The list is gone
// now for a better reason than drift: the serving source is a fact about the
// operator's configuration and the user's activity, it reaches the client's
// inference provider on every download, and it answers no question the caller
// asked. Both fields stay on the struct because the chain's cooldown bookkeeping
// and the operator's log still read them; only the json tag changed, and a tag is
// exactly the kind of thing that gets reverted by accident.
//
// The flag that briefly replaced Source is checked as ABSENT for a different
// reason: it was never a disclosure risk, it was empty. selectSources narrows a
// pinned call to that one source, so a pinned download either returns that
// source's file or errors out, and the flag could report nothing but true.
func TestDownloadResultKeepsProvenanceOffTheWire(t *testing.T) {
	for _, name := range []string{"Source", "Mirror"} {
		field, ok := reflect.TypeFor[DownloadResult]().FieldByName(name)
		if !ok {
			t.Fatalf("DownloadResult has no %s field; the chain and the log still need it", name)
		}
		if got := field.Tag.Get("json"); got != "-" {
			t.Errorf(`DownloadResult.%s json tag = %q, want "-": provenance must not be serialized to the caller`, name, got)
		}
	}
	if _, present := reflect.TypeFor[DownloadResult]().FieldByName("ServedByRequestedSource"); present {
		t.Error("DownloadResult carries ServedByRequestedSource again: selectSources narrows a pinned " +
			"call to that one source, so the flag can only ever be true and carries no information")
	}
}

// TestDownloadRefusalFailsFastAndPointsAtTheBrowser covers the third start outcome
// beside "started" and "kept failing": a host that REFUSES. A 403 used to run the
// whole start-retry schedule and then be reported as errDownloadCouldNotStart —
// "retry later once the mirror recovers" — for a publisher that was never going to
// serve an automated client and never going to recover from anything. It must fail
// on the first attempt and say what is actually left to try.
func TestDownloadRefusalFailsFastAndPointsAtTheBrowser(t *testing.T) {
	var hits atomic.Int32
	srv := md5CDNServer(t, testMD5, func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	defer srv.Close()

	c := newTestClient(staticMirrors{srv.URL})
	c.startRetryWaits = tinyWaits(2) // would be 3 attempts, if the status were retryable
	dir := t.TempDir()

	_, err := c.Download(context.Background(), testMD5, dir, "", nil)
	if err == nil {
		t.Fatal("a refused download should fail")
	}
	if errors.Is(err, errDownloadCouldNotStart) {
		t.Errorf("err = %v, want a refusal rather than the retry-schedule error", err)
	}
	if !strings.Contains(err.Error(), "refused automated access") {
		t.Errorf("err = %v, want it to name the refusal", err)
	}
	if !strings.Contains(err.Error(), "browser") {
		t.Errorf("err = %v, want it to name the browser as the remaining route", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("CDN hits = %d, want 1 (a refusal is final, so the schedule must be skipped)", got)
	}
}

// TestHostRefusal covers the status taxonomy directly: the authorization refusals
// are final, and everything else stays retryable so a genuine blip still gets the
// schedule it exists for.
func TestHostRefusal(t *testing.T) {
	tests := []struct {
		status  int
		refusal bool
	}{
		{http.StatusUnauthorized, true},
		{http.StatusForbidden, true},
		{http.StatusUnavailableForLegalReasons, true},
		{http.StatusNotFound, false},
		{http.StatusTooManyRequests, false},
		{http.StatusServiceUnavailable, false},
		{http.StatusInternalServerError, false},
	}
	for _, tt := range tests {
		got := hostRefusal(tt.status, "https://pubs.example.org/a.pdf")
		if (got != nil) != tt.refusal {
			t.Errorf("hostRefusal(%d) = %v, want refusal = %v", tt.status, got, tt.refusal)
		}
	}
}

// TestRedactQuery verifies an error naming a URL never carries its query string: a
// resolved file URL can be signed with an account key or a one-time token, and
// error text travels into logs and into a model's context.
func TestRedactQuery(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"drops a signed query", "https://cdn.example.org/f.pdf?key=s3cret&exp=1", "https://cdn.example.org/f.pdf"},
		{"drops a fragment", "https://e.org/f.pdf#page=2", "https://e.org/f.pdf"},
		{"keeps a plain url", "https://e.org/f.pdf", "https://e.org/f.pdf"},
		{"unparseable is still truncated", "://nope?key=s3cret", "://nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactQuery(tt.in); got != tt.want {
				t.Errorf("redactQuery(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if strings.Contains(redactQuery(tt.in), "s3cret") && !strings.Contains(tt.want, "s3cret") {
				t.Errorf("redactQuery(%q) leaked the secret", tt.in)
			}
		})
	}
}

// TestDownloadOutlivesRequestTimeout is the guard on the per-request timeout
// being short: a transfer that takes many times longer than cfg.Timeout must
// still complete in full.
//
// LIBGEN_MCP_TIMEOUT bounds ONE question to a mirror — a search, a lookup, a
// resolve hop — all of which have failover behind them, which is why its default
// is measured in seconds. The bytes of a file are governed by something else
// entirely: the download client is built with no timeout at all, and a stream is
// cut only by its context or by the progress-resetting stall window. A 15 MB book
// over a slow link, or a two-minute transfer, is not this setting's business, and
// this test fails if it ever becomes so.
func TestDownloadOutlivesRequestTimeout(t *testing.T) {
	const chunks, chunkDelay = 8, 25 * time.Millisecond
	payload := bytes.Repeat([]byte("slow-but-progressing payload;"), 64)
	chunkSize := len(payload) / chunks

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		flusher, _ := w.(http.Flusher)
		for off := 0; off < len(payload); off += chunkSize {
			_, _ = w.Write(payload[off:min(off+chunkSize, len(payload))])
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(chunkDelay)
		}
	}))
	t.Cleanup(srv.Close)

	// A per-request timeout far shorter than the transfer will take, and a stall
	// window comfortably longer than the gap between chunks.
	cfg := &config.Config{
		Timeout:                20 * time.Millisecond,
		RateRPS:                1000,
		RateBurst:              100,
		RetryAttempts:          1,
		MaxConcurrentDownloads: 2,
		DownloadStallTimeout:   5 * time.Second,
	}
	src := stubSource{name: "libgen", supports: true, resolved: Resolved{FileURL: srv.URL + "/file"}}
	c := New(staticMirrors{}, cfg, WithSources(src))

	started := time.Now()
	res, err := c.DownloadItem(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}, t.TempDir(), "book.pdf")
	if err != nil {
		t.Fatalf("a transfer longer than the per-request timeout must still finish: %v", err)
	}
	if elapsed := time.Since(started); elapsed <= cfg.Timeout {
		t.Fatalf("transfer took %v, which is under the %v timeout — the test proves nothing", elapsed, cfg.Timeout)
	}
	if res.SizeBytes != int64(len(payload)) {
		t.Errorf("saved %d bytes, want the whole %d-byte file", res.SizeBytes, len(payload))
	}
	got, rerr := os.ReadFile(res.Path)
	if rerr != nil {
		t.Fatalf("reading the saved file: %v", rerr)
	}
	if !bytes.Equal(got, payload) {
		t.Error("the saved file does not match the served bytes")
	}
}
