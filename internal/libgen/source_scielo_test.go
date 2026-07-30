package libgen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// scieloPage renders a SciELO article page carrying the marker meta tag every such
// page has plus a citation_pdf_url naming pdfURL, which is the shape the source
// parses.
func scieloPage(pdfURL string) string {
	return `<html><head>` +
		`<meta name="citation_title" content="Um Artigo">` +
		`<meta name="citation_pdf_url" content="` + pdfURL + `">` +
		`</head><body>ok</body></html>`
}

// scieloStub is a SciELO stand-in reached through a stand-in DOI resolver: it
// answers any path with whatever the test set on it, and records the path that was
// requested so the built lookup URL can be asserted.
type scieloStub struct {
	srv *httptest.Server
	// page is the body served for an article request.
	page string
	// status is the status served for an article request.
	status int
	// gotPath is the path of the last request received.
	gotPath string
}

// startScieloStub starts a resolver stand-in that serves an article page by
// default, registering its shutdown with the test.
func startScieloStub(t *testing.T) *scieloStub {
	t.Helper()
	stub := &scieloStub{status: http.StatusOK}
	stub.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.gotPath = r.URL.Path
		w.WriteHeader(stub.status)
		_, _ = w.Write([]byte(stub.page))
	}))
	t.Cleanup(stub.srv.Close)
	stub.page = scieloPage("https://example.invalid/a.pdf?format=pdf")
	return stub
}

// source builds a scieloSource pointed at the stub, with the landing-host check
// aimed at the stub's own host so it runs exactly as it does in production.
func (s *scieloStub) source() scieloSource {
	return scieloSource{http: s.srv.Client(), baseURL: s.srv.URL, hostSuffix: "127.0.0.1"}
}

// TestScieloSupports verifies the source claims every 10.1590 DOI — both the legacy
// PID-derived suffixes and the modern free-form ones — matches the prefix
// case-insensitively as a DOI's specification requires, refuses other registrants,
// and names itself "scielo".
func TestScieloSupports(t *testing.T) {
	s := scieloSource{}
	cases := []struct {
		name string
		item Item
		want bool
	}{
		{name: "legacy pid suffix", item: Item{DOI: "10.1590/s0102-311x2005000400016"}, want: true},
		{name: "modern suffix", item: Item{DOI: "10.1590/1982-2553202568745"}, want: true},
		{name: "suffix with a slash", item: Item{DOI: "10.1590/0100-5405/225610"}, want: true},
		{name: "uppercase prefix", item: Item{DOI: "10.1590/S0100-40422008000100001"}, want: true},
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
	if s.Name() != "scielo" {
		t.Errorf("Name() = %q, want %q", s.Name(), "scielo")
	}
}

// TestScieloResolve verifies the source looks the DOI up through the resolver with
// its slashes left literal and returns the PDF URL the article page advertises. The
// article address is asserted verbatim because it embeds a key the DOI does not
// carry — the whole reason the page has to be fetched.
func TestScieloResolve(t *testing.T) {
	stub := startScieloStub(t)
	const pdf = "https://www.scielo.br/j/cagro/a/YJssX5y4B53dfsNWg5BYSfw/?lang=en&format=pdf"
	stub.page = scieloPage(pdf)

	got, err := stub.source().Resolve(context.Background(), Item{DOI: "10.1590/1413-7054202549009425"})
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
	if want := "/10.1590/1413-7054202549009425"; stub.gotPath != want {
		t.Errorf("looked up %q, want %q (the DOI's slashes must stay literal)", stub.gotPath, want)
	}
}

// TestScieloResolveEscapesDOI verifies a DOI carrying a URL-unsafe character is
// percent-encoded into the lookup path while its slashes stay literal.
func TestScieloResolveEscapesDOI(t *testing.T) {
	stub := startScieloStub(t)
	if _, err := stub.source().Resolve(context.Background(), Item{DOI: "10.1590/a b"}); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	// The stub's recorded path is already URL-decoded, so the assertion is that the
	// escaped request round-tripped to the intended path rather than a truncated one.
	if want := "/10.1590/a b"; stub.gotPath != want {
		t.Errorf("looked up %q, want %q", stub.gotPath, want)
	}
}

// TestScieloResolveHTMLUnescapesURL verifies the entity-encoded separator SciELO
// writes into the tag is decoded before the URL is handed downstream. This is not
// cosmetic: the PDF address is query-driven, so the literal text would request a
// parameter named "amp;format" and get an HTML page.
func TestScieloResolveHTMLUnescapesURL(t *testing.T) {
	stub := startScieloStub(t)
	stub.page = scieloPage("https://www.scielo.br/j/gal/a/GZfNj/?lang=pt&amp;format=pdf")

	got, err := stub.source().Resolve(context.Background(), Item{DOI: "10.1590/1982-2553202568745"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := "https://www.scielo.br/j/gal/a/GZfNj/?lang=pt&format=pdf"; got.FileURL != want {
		t.Errorf("FileURL = %q, want %q", got.FileURL, want)
	}
}

// TestScieloResolveMisses verifies the outcomes that mean "SciELO does not have a
// PDF for this" are reported as clean misses, so the chain advances without setting
// the source aside. The article-with-no-PDF case is a real one: SciELO's oldest
// records are HTML-only and answer "PDF do Artigo não encontrado" when asked for a
// file.
func TestScieloResolveMisses(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		page       string
		doi        string
		hostSuffix string
	}{
		{
			name:   "unregistered doi",
			status: http.StatusNotFound,
			page:   "<html>not found</html>",
			doi:    "10.1590/nope-9999",
		},
		{
			name:   "html-only article advertises no pdf",
			status: http.StatusOK,
			page:   `<html><head><meta name="citation_title" content="Um Artigo"></head></html>`,
			doi:    "10.1590/s0104-66321998000100007",
		},
		{
			name:   "advertised url is not absolute http",
			status: http.StatusOK,
			page:   scieloPage("/j/x/a/y/?format=pdf"),
			doi:    "10.1590/1982-2553202568745",
		},
		{
			name:   "wrong registrant reaches Resolve directly",
			status: http.StatusOK,
			page:   scieloPage("https://example.invalid/x.pdf"),
			doi:    "10.4230/LIPIcs.ICALP.2023.1",
		},
		{
			name:       "resolver lands somewhere that is not SciELO",
			status:     http.StatusOK,
			page:       scieloPage("https://example.invalid/x.pdf"),
			doi:        "10.1590/1982-2553202568745",
			hostSuffix: "scielo.br",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := startScieloStub(t)
			stub.status, stub.page = tc.status, tc.page
			s := stub.source()
			if tc.hostSuffix != "" {
				s.hostSuffix = tc.hostSuffix
			}

			_, err := s.Resolve(context.Background(), Item{DOI: tc.doi})
			if !errors.Is(err, ErrNotIndexed) {
				t.Fatalf("Resolve() error = %v, want ErrNotIndexed", err)
			}
			if errors.Is(err, ErrSourceUnavailable) {
				t.Error("a clean miss must not be tagged unavailable")
			}
		})
	}
}

// TestScieloResolveUnavailable verifies the outcomes that say nothing about the item
// are reported as the source being unavailable, so the chain sets it aside instead
// of reading a broken site as empty coverage.
func TestScieloResolveUnavailable(t *testing.T) {
	t.Run("transient status", func(t *testing.T) {
		stub := startScieloStub(t)
		stub.status = http.StatusServiceUnavailable
		stub.page = "down"

		_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.1590/x"})
		if !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable", err)
		}
	})

	t.Run("200 that is not an article page", func(t *testing.T) {
		stub := startScieloStub(t)
		stub.page = "<html><body>Pardon Our Interruption</body></html>"

		_, err := stub.source().Resolve(context.Background(), Item{DOI: "10.1590/x"})
		if !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable", err)
		}
		if errors.Is(err, ErrNotIndexed) {
			t.Error("a challenge page must not read as the DOI being unheld")
		}
	})

	t.Run("transport failure", func(t *testing.T) {
		stub := startScieloStub(t)
		s := stub.source()
		stub.srv.Close()

		_, err := s.Resolve(context.Background(), Item{DOI: "10.1590/x"})
		if !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable", err)
		}
	})
}

// TestScieloResolveRejectsUnbuildableRequest verifies a DOI that cannot go into a
// URL at all fails before any request is attempted, rather than being reported as
// the site being down.
func TestScieloResolveRejectsUnbuildableRequest(t *testing.T) {
	s := scieloSource{baseURL: "http://\x7f.invalid"}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.1590/x"})
	if err == nil {
		t.Fatal("Resolve() should fail when the request cannot be built")
	}
	if !strings.Contains(err.Error(), "building request") {
		t.Errorf("error = %v, want a request-building failure", err)
	}
}

// TestScieloResolveUsesProductionBase verifies a source with no base override goes
// through the real DOI resolver, so the default configuration reaches SciELO rather
// than nothing. The URL is observed through a transport that refuses every request,
// so the default is asserted without a request leaving the machine.
func TestScieloResolveUsesProductionBase(t *testing.T) {
	s := scieloSource{http: refusingClient()}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.1590/1982-2553202568745"})
	if err == nil {
		t.Fatal("Resolve() should fail when every request is refused")
	}
	if want := "https://doi.org/10.1590/1982-2553202568745"; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want it to name %q", err, want)
	}
}

// TestScieloResolveTruncatedBody verifies an article page whose body dies
// mid-transfer is reported as the source being unavailable rather than as the DOI
// being unheld: a page we could not finish reading is no evidence about the item.
func TestScieloResolveTruncatedBody(t *testing.T) {
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

	s := scieloSource{http: srv.Client(), baseURL: srv.URL, hostSuffix: "127.0.0.1"}
	_, err := s.Resolve(context.Background(), Item{DOI: "10.1590/x"})
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("Resolve() error = %v, want ErrSourceUnavailable", err)
	}
	if !strings.Contains(err.Error(), "reading the article page") {
		t.Errorf("error = %v, want it to name the read failure", err)
	}
}

// TestScieloLandingHost verifies the landing-host check defaults to the real SciELO
// domain and accepts a subdomain of it while rejecting a lookalike that merely ends
// in the same letters.
func TestScieloLandingHost(t *testing.T) {
	if got := (scieloSource{}).landingHost(); got != scieloHostSuffix {
		t.Errorf("landingHost() = %q, want %q", got, scieloHostSuffix)
	}
	if got := (scieloSource{hostSuffix: "example.test"}).landingHost(); got != "example.test" {
		t.Errorf("landingHost() = %q, want the override", got)
	}
	cases := []struct {
		raw  string
		want bool
	}{
		{raw: "https://scielo.br/x", want: true},
		{raw: "https://www.scielo.br/j/gal/a/x/", want: true},
		{raw: "https://WWW.SCIELO.BR/j/gal/", want: true},
		{raw: "https://notscielo.br/x", want: false},
		{raw: "https://scielo.br.evil.test/x", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			u, err := url.Parse(tc.raw)
			if err != nil {
				t.Fatalf("url.Parse(%q): %v", tc.raw, err)
			}
			if got := scieloHost(u, scieloHostSuffix); got != tc.want {
				t.Errorf("scieloHost(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
	if scieloHost(nil, scieloHostSuffix) {
		t.Error("scieloHost(nil) = true, want false")
	}
}
