package libgen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// refusingTransport fails every request while naming the URL it was given, so a
// test can assert which URL a source built without letting the request out.
type refusingTransport struct{}

// RoundTrip refuses the request, reporting the URL it would have been sent to.
func (refusingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("refused %s", r.URL)
}

// refusingClient returns an HTTP client backed by refusingTransport.
func refusingClient() *http.Client { return &http.Client{Transport: refusingTransport{}} }

// dagstuhlPage renders a DROPS document page carrying the marker meta tag every
// such page has plus a citation_pdf_url naming pdfURL, which is the shape the
// source parses.
func dagstuhlPage(pdfURL string) string {
	return `<html><head>` +
		`<meta name="citation_title" content="A Paper">` +
		`<meta name="citation_pdf_url" content="` + pdfURL + `">` +
		`</head><body>ok</body></html>`
}

// dagstuhlStub is a DROPS stand-in: it answers the document path with whatever the
// test set on it, and records the path that was requested so the built lookup URL
// can be asserted.
type dagstuhlStub struct {
	srv *httptest.Server
	// page is the body served for a document request.
	page string
	// status is the status served for a document request.
	status int
	// gotPath is the path of the last document request received.
	gotPath string
}

// startDagstuhlStub starts a DROPS stand-in that serves a document page by default,
// registering its shutdown with the test.
func startDagstuhlStub(t *testing.T) *dagstuhlStub {
	t.Helper()
	stub := &dagstuhlStub{status: http.StatusOK}
	stub.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.gotPath = r.URL.Path
		w.WriteHeader(stub.status)
		_, _ = w.Write([]byte(stub.page))
	}))
	t.Cleanup(stub.srv.Close)
	stub.page = dagstuhlPage("https://example.invalid/paper.pdf")
	return stub
}

// source builds a dagstuhlSource pointed at the stub.
func (s *dagstuhlStub) source() dagstuhlSource {
	return dagstuhlSource{http: s.srv.Client(), baseURL: s.srv.URL}
}

// TestDagstuhlSupports verifies the source claims every 10.4230 DOI — LIPIcs,
// OASIcs and the Dagstuhl Reports alike, since all are served from the same
// publication server — refuses other registrants, and names itself "dagstuhl".
func TestDagstuhlSupports(t *testing.T) {
	s := dagstuhlSource{}
	cases := []struct {
		name string
		item Item
		want bool
	}{
		{name: "lipics", item: Item{DOI: "10.4230/LIPIcs.ICALP.2023.1"}, want: true},
		{name: "oasics", item: Item{DOI: "10.4230/OASIcs.SLATE.2019.5"}, want: true},
		{name: "dagstuhl reports", item: Item{DOI: "10.4230/DagRep.9.1.1"}, want: true},
		{name: "other registrant", item: Item{DOI: "10.17487/RFC9110"}, want: false},
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
	if s.Name() != "dagstuhl" {
		t.Errorf("Name() = %q, want %q", s.Name(), "dagstuhl")
	}
}

// TestDagstuhlResolve verifies the source looks the DOI up on the document path,
// with the DOI's slashes left literal, and returns the PDF URL the page advertises.
// The storage path is asserted verbatim because it embeds a volume number the DOI
// does not carry — the whole reason the page has to be fetched.
func TestDagstuhlResolve(t *testing.T) {
	stub := startDagstuhlStub(t)
	const pdf = "https://drops.dagstuhl.de/storage/00lipics/lipics-vol261-icalp2023/" +
		"LIPIcs.ICALP.2023.1/LIPIcs.ICALP.2023.1.pdf"
	stub.page = dagstuhlPage(pdf)

	got, err := stub.source().Resolve(context.Background(), Item{DOI: "10.4230/LIPIcs.ICALP.2023.1"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.FileURL != pdf {
		t.Errorf("FileURL = %q, want %q", got.FileURL, pdf)
	}
	if got.Ext != "pdf" {
		t.Errorf("Ext = %q, want %q", got.Ext, "pdf")
	}
	if got.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false for a DOI-keyed item")
	}
	if want := "/entities/document/10.4230/LIPIcs.ICALP.2023.1"; stub.gotPath != want {
		t.Errorf("looked up %q, want %q (the DOI's slashes must stay literal)", stub.gotPath, want)
	}
}

// TestDagstuhlResolveEscapesDOI verifies a DOI carrying a URL-unsafe character is
// percent-encoded into the lookup path while its slashes stay literal.
func TestDagstuhlResolveEscapesDOI(t *testing.T) {
	stub := startDagstuhlStub(t)
	if _, err := stub.source().Resolve(context.Background(), Item{DOI: "10.4230/LIPIcs A"}); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	// The stub's recorded path is already URL-decoded, so the assertion is that the
	// escaped request round-tripped to the intended path rather than a truncated one.
	if want := "/entities/document/10.4230/LIPIcs A"; stub.gotPath != want {
		t.Errorf("looked up %q, want %q", stub.gotPath, want)
	}
}

// TestDagstuhlResolveHTMLUnescapesURL verifies an entity-encoded separator in the
// meta tag is decoded before the URL is handed downstream, so a query-driven path
// is requested as written rather than literally.
func TestDagstuhlResolveHTMLUnescapesURL(t *testing.T) {
	stub := startDagstuhlStub(t)
	stub.page = dagstuhlPage("https://example.invalid/get?a=1&amp;b=2")

	got, err := stub.source().Resolve(context.Background(), Item{DOI: "10.4230/LIPIcs.X.1"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := "https://example.invalid/get?a=1&b=2"; got.FileURL != want {
		t.Errorf("FileURL = %q, want %q", got.FileURL, want)
	}
}

// TestDagstuhlResolveMisses verifies the outcomes that mean "DROPS does not have
// this" are reported as clean misses, so the chain advances without setting the
// source aside.
func TestDagstuhlResolveMisses(t *testing.T) {
	cases := []struct {
		name   string
		status int
		page   string
		doi    string
	}{
		{
			name:   "unknown doi",
			status: http.StatusNotFound,
			page:   "<html>not found</html>",
			doi:    "10.4230/LIPIcs.NOPE.2023.999",
		},
		{
			name:   "document page advertises no pdf",
			status: http.StatusOK,
			page:   `<html><head><meta name="citation_title" content="A Paper"></head></html>`,
			doi:    "10.4230/LIPIcs.X.1",
		},
		{
			name:   "advertised url is not absolute http",
			status: http.StatusOK,
			page:   dagstuhlPage("/relative/paper.pdf"),
			doi:    "10.4230/LIPIcs.X.1",
		},
		{
			name:   "wrong registrant reaches Resolve directly",
			status: http.StatusOK,
			page:   dagstuhlPage("https://example.invalid/x.pdf"),
			doi:    "10.17487/RFC9110",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := startDagstuhlStub(t)
			stub.status, stub.page = tc.status, tc.page

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

// TestDagstuhlResolveUnavailable verifies the outcomes that say nothing about the
// item are reported as the source being unavailable, so the chain sets it aside
// instead of reading a broken site as empty coverage.
func TestDagstuhlResolveUnavailable(t *testing.T) {
	t.Run("transient status", func(t *testing.T) {
		stub := startDagstuhlStub(t)
		stub.status = http.StatusServiceUnavailable
		stub.page = "down"

		_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.4230/LIPIcs.X.1"})
		if !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable", err)
		}
	})

	t.Run("200 that is not a document page", func(t *testing.T) {
		stub := startDagstuhlStub(t)
		stub.page = "<html><body>Checking your browser…</body></html>"

		_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.4230/LIPIcs.X.1"})
		if !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable", err)
		}
		if errors.Is(err, ErrNotIndexed) {
			t.Error("a challenge page must not read as the DOI being unheld")
		}
	})

	t.Run("transport failure", func(t *testing.T) {
		stub := startDagstuhlStub(t)
		s := stub.source()
		stub.srv.Close()

		_, err := s.Resolve(context.Background(), Item{DOI: "10.4230/LIPIcs.X.1"})
		if !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable", err)
		}
	})
}

// TestDagstuhlResolveRejectsUnbuildableRequest verifies a DOI that cannot go into a
// URL at all fails before any request is attempted, rather than being reported as
// the site being down.
func TestDagstuhlResolveRejectsUnbuildableRequest(t *testing.T) {
	s := dagstuhlSource{baseURL: "http://\x7f.invalid"}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.4230/LIPIcs.X.1"})
	if err == nil {
		t.Fatal("Resolve() should fail when the request cannot be built")
	}
	if !strings.Contains(err.Error(), "building request") {
		t.Errorf("error = %v, want a request-building failure", err)
	}
}

// TestDagstuhlResolveUsesProductionBase verifies a source with no base override
// targets the real DROPS root, so the default configuration reaches Dagstuhl
// rather than nothing. The URL is observed through a transport that refuses every
// request, so the default is asserted without a request leaving the machine.
func TestDagstuhlResolveUsesProductionBase(t *testing.T) {
	s := dagstuhlSource{http: refusingClient()}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.4230/LIPIcs.ICALP.2023.1"})
	if err == nil {
		t.Fatal("Resolve() should fail when every request is refused")
	}
	if want := "https://drops.dagstuhl.de/entities/document/10.4230/LIPIcs.ICALP.2023.1"; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want it to name %q", err, want)
	}
}

// TestDagstuhlResolveTruncatedBody verifies a document page whose body dies
// mid-transfer is reported as the source being unavailable rather than as the DOI
// being unheld: a page we could not finish reading is no evidence about the item.
func TestDagstuhlResolveTruncatedBody(t *testing.T) {
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

	s := dagstuhlSource{http: srv.Client(), baseURL: srv.URL}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.4230/LIPIcs.X.1"})
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable", err)
	}
	if !strings.Contains(err.Error(), "reading the document page") {
		t.Errorf("error = %v, want it to name the read failure", err)
	}
}

// discardLogger returns a logger that drops everything, so a test that deliberately
// aborts a handler does not print the server's panic notice.
func discardLogger() *log.Logger { return log.New(io.Discard, "", 0) }
