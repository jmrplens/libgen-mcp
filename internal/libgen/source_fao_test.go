package libgen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// faoPage renders an FAO Knowledge Repository item page: the Angular root element
// every server-rendered page of the repository carries, plus a citation_pdf_url
// naming pdfURL, which is the shape the source parses.
func faoPage(pdfURL string) string {
	return `<html><head>` +
		`<meta name="citation_pdf_url" content="` + pdfURL + `">` +
		`</head><body><ds-root ng-version="16"></ds-root></body></html>`
}

// faoShell renders the repository page with no citation meta tag at all — the shape
// DSpace serves, with HTTP 200, for a handle it does not hold.
func faoShell() string {
	return `<html><head><title>FAO Knowledge Repository</title></head>` +
		`<body><ds-root>No item found for the identifier</ds-root></body></html>`
}

// faoStub is a repository stand-in: it answers any path with whatever the test set
// on it, and records the path that was requested so the built item URL can be
// asserted.
type faoStub struct {
	srv *httptest.Server
	// page is the body served for an item request.
	page string
	// status is the status served for an item request.
	status int
	// gotPath is the path of the last request received.
	gotPath string
}

// startFAOStub starts a repository stand-in that serves an item page by default,
// registering its shutdown with the test.
func startFAOStub(t *testing.T) *faoStub {
	t.Helper()
	stub := &faoStub{status: http.StatusOK}
	stub.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.gotPath = r.URL.Path
		w.WriteHeader(stub.status)
		_, _ = w.Write([]byte(stub.page))
	}))
	t.Cleanup(stub.srv.Close)
	stub.page = faoPage("http://openknowledge.fao.org/bitstreams/" +
		"c2d36375-2b2b-445c-bdcf-70289e71e85b/download")
	return stub
}

// source builds a faoSource pointed at the stub.
func (s *faoStub) source() faoSource {
	return faoSource{http: s.srv.Client(), baseURL: s.srv.URL}
}

// TestFAOSupports verifies the source claims a well-formed 10.4060 DOI, matches the
// prefix case-insensitively as a DOI's specification requires, refuses a suffix that
// could not be a handle's local part, refuses other registrants, and names itself
// "fao".
func TestFAOSupports(t *testing.T) {
	s := faoSource{}
	cases := []struct {
		name string
		item Item
		want bool
	}{
		{name: "report", item: Item{DOI: "10.4060/cc7949en"}, want: true},
		{name: "french edition", item: Item{DOI: "10.4060/ca5348fr"}, want: true},
		{name: "component with a hyphen", item: Item{DOI: "10.4060/cc2212en-fig07"}, want: true},
		{name: "uppercase prefix", item: Item{DOI: "10.4060/CC7949EN"}, want: true},
		{name: "suffix with a slash", item: Item{DOI: "10.4060/cc7949en/extra"}, want: false},
		{name: "suffix that is a parent traversal", item: Item{DOI: "10.4060/.."}, want: false},
		{name: "suffix with no letter or digit", item: Item{DOI: "10.4060/-.-"}, want: false},
		{name: "empty suffix", item: Item{DOI: "10.4060/"}, want: false},
		{name: "other registrant", item: Item{DOI: "10.4230/LIPIcs.ICALP.2023.1"}, want: false},
		{name: "md5 only", item: Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}, want: false},
		{name: "empty", item: Item{}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.Supports(tc.item); got != tc.want {
				t.Errorf("Supports(%+v) = %v, want %v", tc.item, got, tc.want)
			}
		})
	}
	if s.Name() != "fao" {
		t.Errorf("Name() = %q, want %q", s.Name(), "fao")
	}
}

// TestFAOResolve verifies the source derives the item page's address from the DOI's
// own suffix — no doi.org hop — and rewrites the advertised frontend download URL
// into the backend content endpoint, which is the only one that serves the file to a
// plain HTTP client and the only one the repository's robots.txt allows.
func TestFAOResolve(t *testing.T) {
	stub := startFAOStub(t)

	got, err := stub.source().Resolve(context.Background(), Item{DOI: "10.4060/cc7949en"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	want := stub.srv.URL + "/server/api/core/bitstreams/c2d36375-2b2b-445c-bdcf-70289e71e85b/content"
	if got.FileURL != want {
		t.Errorf("FileURL = %q, want %q", got.FileURL, want)
	}
	if got.Ext != "pdf" {
		t.Errorf("Ext = %q, want %q", got.Ext, "pdf")
	}
	if got.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false for a DOI-keyed item")
	}
	if wantPath := "/handle/20.500.14283/cc7949en"; stub.gotPath != wantPath {
		t.Errorf("looked up %q, want %q", stub.gotPath, wantPath)
	}
}

// TestFAOResolveHTMLUnescapesURL verifies an entity-encoded advertised URL is
// decoded before the bitstream id is lifted out of it.
func TestFAOResolveHTMLUnescapesURL(t *testing.T) {
	stub := startFAOStub(t)
	stub.page = faoPage("http://openknowledge.fao.org/bitstreams/" +
		"8cceeba6-743c-4901-a9d8-6c48fa3e4e92/download?x=1&amp;y=2")

	got, err := stub.source().Resolve(context.Background(), Item{DOI: "10.4060/cb0211en"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	want := stub.srv.URL + "/server/api/core/bitstreams/8cceeba6-743c-4901-a9d8-6c48fa3e4e92/content"
	if got.FileURL != want {
		t.Errorf("FileURL = %q, want %q", got.FileURL, want)
	}
}

// TestFAOResolveMisses verifies the outcomes that mean "the repository cannot serve
// this" are reported as clean misses, so the chain advances without setting the
// source aside. The unheld-handle case matters most: DSpace answers an unknown
// handle with HTTP 200 and its ordinary shell, so the missing meta tag is the only
// signal there is.
func TestFAOResolveMisses(t *testing.T) {
	cases := []struct {
		name string
		page string
		doi  string
	}{
		{
			name: "handle the repository does not hold",
			page: faoShell(),
			doi:  "10.4060/zzzz9999xx",
		},
		{
			name: "metadata-only component advertises no pdf",
			page: `<html><body><ds-root>Figure 7</ds-root></body></html>`,
			doi:  "10.4060/cc2212en-fig07",
		},
		{
			name: "advertised url is not a bitstream download",
			page: faoPage("https://example.invalid/some/other/file.pdf"),
			doi:  "10.4060/cc7949en",
		},
		{
			name: "wrong registrant reaches Resolve directly",
			page: faoPage("http://openknowledge.fao.org/bitstreams/" +
				"c2d36375-2b2b-445c-bdcf-70289e71e85b/download"),
			doi: "10.4230/LIPIcs.ICALP.2023.1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := startFAOStub(t)
			stub.page = tc.page

			_, err := stub.source().Resolve(context.Background(), Item{DOI: tc.doi})
			if !errors.Is(err, ErrNotIndexed) {
				t.Fatalf("Resolve() error = %v, want ErrNotIndexed", err)
			}
			if errors.Is(err, ErrSourceUnavailable) {
				t.Error("a clean miss must not be tagged unavailable")
			}
		})
	}
}

// TestFAOResolveUnavailable verifies the outcomes that say nothing about the item
// are reported as the source being unavailable, so the chain sets it aside instead
// of reading a broken site as empty coverage.
func TestFAOResolveUnavailable(t *testing.T) {
	t.Run("transient status", func(t *testing.T) {
		stub := startFAOStub(t)
		stub.status = http.StatusServiceUnavailable
		stub.page = "down"

		_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.4060/cc7949en"})
		if !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable", err)
		}
	})

	t.Run("200 that did not come from the repository", func(t *testing.T) {
		stub := startFAOStub(t)
		stub.page = "<html><body>Checking your browser…</body></html>"

		_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.4060/cc7949en"})
		if !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable", err)
		}
		if errors.Is(err, ErrNotIndexed) {
			t.Error("a challenge page must not read as the DOI being unheld")
		}
	})

	t.Run("transport failure", func(t *testing.T) {
		stub := startFAOStub(t)
		s := stub.source()
		stub.srv.Close()

		_, err := s.Resolve(context.Background(), Item{DOI: "10.4060/cc7949en"})
		if !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable", err)
		}
	})
}

// TestFAOResolveRejectsUnbuildableRequest verifies a base URL that cannot go into a
// request fails before anything is attempted, rather than being reported as the site
// being down.
func TestFAOResolveRejectsUnbuildableRequest(t *testing.T) {
	s := faoSource{baseURL: "http://\x7f.invalid"}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.4060/cc7949en"})
	if err == nil {
		t.Fatal("Resolve() should fail when the request cannot be built")
	}
	if !strings.Contains(err.Error(), "building request") {
		t.Errorf("error = %v, want a request-building failure", err)
	}
}

// TestFAOResolveUsesProductionBase verifies a source with no base override targets
// the real repository, so the default configuration reaches the FAO rather than
// nothing. The URL is observed through a transport that refuses every request, so
// the default is asserted without a request leaving the machine.
func TestFAOResolveUsesProductionBase(t *testing.T) {
	s := faoSource{http: refusingClient()}
	if got := s.root(); got != faoBase {
		t.Errorf("root() = %q, want %q", got, faoBase)
	}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.4060/cc7949en"})
	if err == nil {
		t.Fatal("Resolve() should fail when every request is refused")
	}
	if want := "https://openknowledge.fao.org/handle/20.500.14283/cc7949en"; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want it to name %q", err, want)
	}
}

// TestFAOResolveTruncatedBody verifies an item page whose body dies mid-transfer is
// reported as the source being unavailable rather than as the DOI being unheld: a
// page we could not finish reading is no evidence about the item.
func TestFAOResolveTruncatedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>"))
		// Flush so the client sees a good response and starts reading, then abort
		// without sending the promised bytes so the read itself is what fails.
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	defer srv.Close()
	srv.Config.ErrorLog = discardLogger()

	s := faoSource{http: srv.Client(), baseURL: srv.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.4060/cc7949en"})
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable", err)
	}
	if !strings.Contains(err.Error(), "reading the item page") {
		t.Errorf("error = %v, want it to name the read failure", err)
	}
}

// TestFAOSourceRootTrimsTrailingSlash verifies a configured base URL with a trailing
// slash does not produce a doubled separator in the built paths.
func TestFAOSourceRootTrimsTrailingSlash(t *testing.T) {
	if got := (faoSource{baseURL: "https://example.test/"}).root(); got != "https://example.test" {
		t.Errorf("root() = %q, want the trailing slash trimmed", got)
	}
}
