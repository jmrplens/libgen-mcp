package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
)

// fakeMirrors is a static MirrorLister used to drive the probe against local
// httptest servers without touching the live discovery page.
type fakeMirrors []string

func (f fakeMirrors) Mirrors(context.Context) []string { return f }

// fixture reads a shared libgen testdata fixture (reused verbatim from the
// internal/libgen package so the probe is exercised against real captures).
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "libgen", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// newMirrorServer builds a single httptest server that answers every route the
// probe exercises: search (index.php), details (json.php), and the download
// chain (ads.php → get.php). searchBody lets a caller swap in an empty-results
// page to drive the no-sample-md5 path.
func newMirrorServer(t *testing.T, searchBody []byte) *httptest.Server {
	t.Helper()
	fileJSON := fixture(t, "file_by_md5.json")
	editionJSON := fixture(t, "edition.json")
	ads := fixture(t, "ads.html")
	mux := http.NewServeMux()
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(searchBody)
	})
	mux.HandleFunc("/json.php", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("object") {
		case "f":
			_, _ = w.Write(fileJSON)
		case "e":
			_, _ = w.Write(editionJSON)
		default:
			http.Error(w, "bad object", http.StatusBadRequest)
		}
	})
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(ads)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="probe.pdf"`)
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("A"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// probeClient builds a libgen.Client wired to the given mirror, with test-fast
// settings (high rate limit, single attempt) so nothing waits.
func probeClient(mirror string) *libgen.Client {
	cfg := &config.Config{
		Timeout:                5 * time.Second,
		RateRPS:                1000,
		RateBurst:              100,
		RetryAttempts:          1,
		MaxConcurrentDownloads: 2,
	}
	c := libgen.New(fakeMirrors{mirror}, cfg)
	return c
}

// TestReport covers both branches of checker.report: the OK path and the FAIL
// path (which flips failed and prints the error).
func TestReport(t *testing.T) {
	var buf bytes.Buffer
	c := &checker{w: &buf}
	c.report("ok-check", nil, "all good")
	if c.failed {
		t.Error("failed flipped on an OK report")
	}
	c.report("bad-check", errors.New("boom"), "unused")
	if !c.failed {
		t.Error("failed not set after an error report")
	}
	out := buf.String()
	if !strings.Contains(out, "[OK]   ok-check: all good") {
		t.Errorf("missing OK line: %q", out)
	}
	if !strings.Contains(out, "[FAIL] bad-check: boom") {
		t.Errorf("missing FAIL line: %q", out)
	}
}

// TestRunSearchesSuccess drives runSearches against a mirror returning a populated
// search page: every topic reports OK and a sample md5 is captured.
func TestRunSearchesSuccess(t *testing.T) {
	srv := newMirrorServer(t, fixture(t, "search_books.html"))
	var buf bytes.Buffer
	c := &checker{w: &buf}
	md5 := c.runSearches(context.Background(), probeClient(srv.URL))
	if md5 == "" {
		t.Fatal("runSearches returned no sample md5 for a populated page")
	}
	if c.failed {
		t.Errorf("runSearches marked failure on success: %q", buf.String())
	}
	if strings.Contains(buf.String(), "[FAIL]") {
		t.Errorf("unexpected FAIL line: %q", buf.String())
	}
}

// TestRunSearchesEmpty drives runSearches against a mirror returning an
// empty-results page: no md5 is captured and every topic reports the
// "0 results" failure.
func TestRunSearchesEmpty(t *testing.T) {
	srv := newMirrorServer(t, fixture(t, "search_empty.html"))
	var buf bytes.Buffer
	c := &checker{w: &buf}
	md5 := c.runSearches(context.Background(), probeClient(srv.URL))
	if md5 != "" {
		t.Errorf("sample md5 = %q, want empty for a 0-result page", md5)
	}
	if !c.failed || !strings.Contains(buf.String(), "0 results") {
		t.Errorf("expected 0-results failure, got: %q", buf.String())
	}
}

// TestRunSearchesTransportError drives runSearches against a mirror that always
// errors: Search returns an error and every topic reports FAIL.
func TestRunSearchesTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	var buf bytes.Buffer
	c := &checker{w: &buf}
	md5 := c.runSearches(context.Background(), probeClient(srv.URL))
	if md5 != "" {
		t.Errorf("sample md5 = %q, want empty when every search errors", md5)
	}
	if !c.failed || !strings.Contains(buf.String(), "[FAIL] search") {
		t.Errorf("expected search FAIL lines, got: %q", buf.String())
	}
}

// TestCheckDownloadSuccess covers the happy path: a 206 with a Content-Disposition
// header yields a status message and no error.
func TestCheckDownloadSuccess(t *testing.T) {
	srv := newMirrorServer(t, nil)
	msg, err := checkDownload(context.Background(), srv.URL+"/get.php?md5=x&key=y")
	if err != nil {
		t.Fatalf("checkDownload() error = %v", err)
	}
	if !strings.Contains(msg, "status 206") || !strings.Contains(msg, "present=true") {
		t.Errorf("msg = %q, want 206 with content-disposition present", msg)
	}
}

// TestCheckDownloadStatusError covers the non-2xx/206 branch: a 404 is reported
// as a status error.
func TestCheckDownloadStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := checkDownload(context.Background(), srv.URL); err == nil {
		t.Error("checkDownload should fail on a 404")
	}
}

// TestCheckDownloadRequestError covers the request-construction failure branch: a
// URL with a control character cannot be built into a request.
func TestCheckDownloadRequestError(t *testing.T) {
	if _, err := checkDownload(context.Background(), "http://\x7f/x"); err == nil {
		t.Error("checkDownload should fail on an invalid URL")
	}
}

// TestCheckDownloadTransportError covers the transport-error branch: a closed
// server refuses the connection.
func TestCheckDownloadTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	if _, err := checkDownload(context.Background(), url+"/get.php"); err == nil {
		t.Error("checkDownload should fail when the connection is refused")
	}
}

// TestProbeHappyPath runs the whole probe against a full mirror server: every
// check passes and the exit code is 0.
func TestProbeHappyPath(t *testing.T) {
	srv := newMirrorServer(t, fixture(t, "search_books.html"))
	var buf bytes.Buffer
	code := probe(context.Background(), &buf, fakeMirrors{srv.URL}, probeClient(srv.URL))
	if code != 0 {
		t.Fatalf("probe() = %d, want 0; output:\n%s", code, buf.String())
	}
	out := buf.String()
	for _, want := range []string{"mirrors", "json.php details", "ads.php key", "CDN download"} {
		if !strings.Contains(out, "[OK]   "+want) {
			t.Errorf("missing OK line for %q; output:\n%s", want, out)
		}
	}
}

// TestProbeNoMirrors covers the empty-mirror-list branch: probe reports the
// mirror failure and exits 1 before any search.
func TestProbeNoMirrors(t *testing.T) {
	var buf bytes.Buffer
	code := probe(context.Background(), &buf, fakeMirrors{}, probeClient("http://127.0.0.1:0"))
	if code != 1 {
		t.Fatalf("probe() = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "[FAIL] mirrors") {
		t.Errorf("missing mirrors failure: %q", buf.String())
	}
}

// TestProbeNoSampleMD5 covers the no-sample-md5 branch: searches succeed at the
// HTTP level but return zero results, so the probe stops before details/download.
func TestProbeNoSampleMD5(t *testing.T) {
	srv := newMirrorServer(t, fixture(t, "search_empty.html"))
	var buf bytes.Buffer
	code := probe(context.Background(), &buf, fakeMirrors{srv.URL}, probeClient(srv.URL))
	if code != 1 {
		t.Fatalf("probe() = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "no sample md5 available") {
		t.Errorf("missing no-sample-md5 line: %q", buf.String())
	}
}

// TestProbeDetailsAndResolveFail covers the tail failure path: searches yield a
// sample md5, but json.php and ads.php fail, so details and key resolution report
// FAIL (skipping the CDN check) and the probe exits 1 via the failed flag.
func TestProbeDetailsAndResolveFail(t *testing.T) {
	books := fixture(t, "search_books.html")
	mux := http.NewServeMux()
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(books)
	})
	// json.php and ads.php return a permanent error so both checks fail.
	fail := func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}
	mux.HandleFunc("/json.php", fail)
	mux.HandleFunc("/ads.php", fail)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var buf bytes.Buffer
	code := probe(context.Background(), &buf, fakeMirrors{srv.URL}, probeClient(srv.URL))
	if code != 1 {
		t.Fatalf("probe() = %d, want 1; output:\n%s", code, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "[FAIL] json.php details") || !strings.Contains(out, "[FAIL] ads.php key") {
		t.Errorf("expected details and key failures; output:\n%s", out)
	}
	if strings.Contains(out, "CDN download") {
		t.Errorf("CDN download must be skipped when key resolution fails; output:\n%s", out)
	}
}

// TestRunHappyPath exercises run end to end offline: newManager is stubbed so the
// discovery source points at a local dead-end (forcing the fallback list) while
// the forced -mirror serves every route. It must exit 0.
func TestRunHappyPath(t *testing.T) {
	t.Setenv("LIBGEN_MCP_DOWNLOAD_DIR", t.TempDir())
	// Raise the limiter above its default of 1 rps so the ~10 probe requests do
	// not serialize into a multi-second run.
	t.Setenv("LIBGEN_MCP_RATE_RPS", "1000")
	t.Setenv("LIBGEN_MCP_RATE_BURST", "100")
	api := newMirrorServer(t, fixture(t, "search_books.html"))

	// A local server that 404s the discovery page so load() falls back without
	// ever reaching the real network; the forced mirror is tried first anyway.
	disco := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no page", http.StatusNotFound)
	}))
	defer disco.Close()

	restore := newManager
	newManager = func(cfg *config.Config) (*mirrors.Manager, error) {
		m, err := mirrors.NewManager(cfg)
		if err != nil {
			return nil, err
		}
		m.SourceURL = disco.URL
		m.CachePath = filepath.Join(t.TempDir(), "mirrors.json")
		return m, nil
	}
	defer func() { newManager = restore }()

	var buf bytes.Buffer
	code := run(&buf, []string{"-mirror", api.URL})
	if code != 0 {
		t.Fatalf("run() = %d, want 0; output:\n%s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "[OK]   CDN download") {
		t.Errorf("run() happy path missing CDN download OK; output:\n%s", buf.String())
	}
}

// TestRunFlagError covers the flag-parse failure branch: an unknown flag makes
// Parse fail and run exits 1.
func TestRunFlagError(t *testing.T) {
	var buf bytes.Buffer
	if code := run(&buf, []string{"-nope"}); code != 1 {
		t.Fatalf("run() = %d, want 1 on a bad flag", code)
	}
}

// TestRunConfigError covers the config.Load failure branch: an invalid duration
// env makes Load return an error.
func TestRunConfigError(t *testing.T) {
	t.Setenv("LIBGEN_MCP_TIMEOUT", "not-a-duration")
	var buf bytes.Buffer
	if code := run(&buf, nil); code != 1 {
		t.Fatalf("run() = %d, want 1 on a config error", code)
	}
	if !strings.Contains(buf.String(), "[FAIL] config") {
		t.Errorf("missing config failure line: %q", buf.String())
	}
}

// TestRunManagerError covers the manager-construction failure branch: newManager
// is stubbed to return an error.
func TestRunManagerError(t *testing.T) {
	t.Setenv("LIBGEN_MCP_DOWNLOAD_DIR", t.TempDir())
	restore := newManager
	newManager = func(*config.Config) (*mirrors.Manager, error) {
		return nil, errors.New("boom")
	}
	defer func() { newManager = restore }()

	var buf bytes.Buffer
	if code := run(&buf, nil); code != 1 {
		t.Fatalf("run() = %d, want 1 on a manager error", code)
	}
	if !strings.Contains(buf.String(), "[FAIL] mirrors manager") {
		t.Errorf("missing manager failure line: %q", buf.String())
	}
}
