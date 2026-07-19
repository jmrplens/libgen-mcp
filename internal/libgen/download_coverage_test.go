package libgen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

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

// TestChooseFileName covers the name-selection precedence and, in particular, the
// fallback-extension branch (appending the source's extension when the chosen
// name carries none) and its skip when a name already has an extension.
func TestChooseFileName(t *testing.T) {
	meta := &FileMeta{Author: "A", Title: "T", Year: "2020", Ext: "epub"}
	cases := []struct {
		name        string
		filename    string
		disposition string
		meta        *FileMeta
		md5         string
		ext         string
		want        string
	}{
		{"explicit filename wins", "explicit.pdf", "disp.pdf", meta, "md5", "pdf", "explicit.pdf"},
		{"disposition when no filename", "", "disp.pdf", meta, "md5", "pdf", "disp.pdf"},
		{"meta when no filename or disposition", "", "", meta, "md5", "pdf", "A - T (2020).epub"},
		{"md5 with fallback extension", "", "", nil, "abcdef", "pdf", "abcdef.pdf"},
		{"fallback extension skipped when name has one", "have.mobi", "", nil, "md5", "pdf", "have.mobi"},
		{"no extension and no fallback", "", "", nil, "deadbeef", "", "deadbeef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chooseFileName(tc.filename, tc.disposition, tc.meta, tc.md5, tc.ext)
			if got != tc.want {
				t.Errorf("chooseFileName() = %q, want %q", got, tc.want)
			}
		})
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

// TestStreamToPartOpenError covers openPartForStream's OpenFile failure (and its
// propagation through streamToPartAndVerify): a partial path that is an existing
// directory cannot be opened for writing.
func TestStreamToPartOpenError(t *testing.T) {
	c := newTestClient(staticMirrors{})
	dir := t.TempDir()
	_, err := c.streamToPartAndVerify(dir, filepath.Join(dir, "dest"), "", strings.NewReader("x"), streamOpts{})
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
	_, err := c.streamToPartAndVerify(part, filepath.Join(dir, "dest"), "", strings.NewReader("short"), streamOpts{contentLength: 999})
	if err == nil {
		t.Fatal("streamToPartAndVerify should fail on a truncated transfer")
	}
	if _, statErr := os.Stat(part); os.IsNotExist(statErr) {
		t.Error("a truncated transfer should keep the .part for a later resume")
	}
}

// TestSanitizeFilenameLong covers the length-cap branch: an over-long name is
// truncated to 200 runes.
func TestSanitizeFilenameLong(t *testing.T) {
	got := sanitizeFilename(strings.Repeat("a", 250))
	if n := len([]rune(got)); n != 200 {
		t.Errorf("sanitizeFilename(len 250) has %d runes, want 200", n)
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
