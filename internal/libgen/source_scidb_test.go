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

// scidbPage renders a SciDB article page the way the live site does: an absolute
// link to the PDF plus a pdf.js viewer iframe carrying the same URL percent-encoded.
func scidbPage(pdfURL string) string {
	return `<html><head><title>Some Paper - Anna&#8217;s Archive</title></head><body>` +
		`<a href="` + pdfURL + `">download</a>` +
		`<iframe src="/pdfjs/web/viewer.html?file=` + url.QueryEscape(pdfURL) + `"></iframe>` +
		`</body></html>`
}

// scidbStatusMirror starts an httptest server answering every request with status
// and a real SciDB page as the body, and returns its base URL. The body carries a
// genuine viewer link so a status gate that stopped holding would resolve here
// rather than fail for want of anything to scrape.
func scidbStatusMirror(t *testing.T, status int) string {
	t.Helper()
	body := scidbPage("https://cdn.example.net/stale.pdf")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestSciDBErrorClassification pins which failures Resolve reports as SciDB
// answering "I do not hold this" (ErrNotIndexed) and which as SciDB being unable to
// answer at all (ErrSourceUnavailable). The chain reads that verdict to decide
// between retrying the source and setting it aside for five minutes, so a source
// that reports a mirror outage as a missing article — or the reverse — degrades
// every subsequent download. The existing tests reached these branches but asserted
// only that an error came back.
func TestSciDBErrorClassification(t *testing.T) {
	const doi = "10.1016/j.cell.2016.01.043"

	t.Run("a 200 page embedding no PDF is a clean miss", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`<html><body>no article here</body></html>`))
		}))
		t.Cleanup(srv.Close)
		s := scidbSource{mirrors: staticMirrors{srv.URL}, http: srv.Client()}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertCleanMiss(t, err)
	})

	t.Run("a transient status is unavailability", func(t *testing.T) {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable} {
			s := scidbSource{mirrors: staticMirrors{scidbStatusMirror(t, status)}, http: http.DefaultClient}

			_, err := s.Resolve(context.Background(), Item{DOI: doi})
			assertUnavailable(t, err)
		}
	})

	t.Run("a transport failure is unavailability", func(t *testing.T) {
		s := scidbSource{mirrors: staticMirrors{"https://annas.invalid"}, http: refusingClient()}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertUnavailable(t, err)
	})

	t.Run("a 403 is neither", func(t *testing.T) {
		// Anna's serves a 403 challenge page under load. It is not proof the mirror is
		// down, so unavailableStatus leaves it untagged and the mirror keeps its place
		// in the chain; pinned so that stays a decision rather than a regression.
		s := scidbSource{mirrors: staticMirrors{scidbStatusMirror(t, http.StatusForbidden)}, http: http.DefaultClient}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		if err == nil {
			t.Fatal("a 403 must not resolve")
		}
		if errors.Is(err, ErrNotIndexed) {
			t.Error("a 403 challenge read as a clean miss")
		}
		if cooldownWorthy(context.Background(), err) {
			t.Error("a 403 challenge put the mirror family in cooldown")
		}
	})

	t.Run("no mirrors at all is neither", func(t *testing.T) {
		// An empty mirror list means discovery failed, which says nothing about the
		// DOI and nothing about SciDB's health either.
		s := scidbSource{mirrors: staticMirrors{}, http: http.DefaultClient}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		if err == nil {
			t.Fatal("Resolve with no mirrors must fail")
		}
		if errors.Is(err, ErrNotIndexed) {
			t.Error("having no mirror to ask read as the article being unheld")
		}
	})
}

// TestSciDBSupports verifies the source claims DOI-keyed items only and names
// itself "scidb".
func TestSciDBSupports(t *testing.T) {
	s := scidbSource{}
	if !s.Supports(Item{DOI: "10.1/x"}) {
		t.Error("Supports(DOI) = false, want true")
	}
	if s.Supports(Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}) {
		t.Error("Supports(md5-only) = true, want false")
	}
	if s.Name() != "scidb" {
		t.Errorf("Name() = %q, want %q", s.Name(), "scidb")
	}
}

// TestSciDBResolveExtractsPDF verifies a DOI resolves to the PDF URL embedded in
// the SciDB page, with the DOI's slashes kept literal in the request path, a pdf
// fallback extension, and MD5 verification disabled (DOI items carry no digest).
func TestSciDBResolveExtractsPDF(t *testing.T) {
	const pdfURL = "https://cdn.example.net/d3/x/paper.pdf"
	const doi = "10.1016/j.cell.2011.02.013"

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(scidbPage(pdfURL)))
	}))
	defer srv.Close()

	s := scidbSource{mirrors: staticMirrors{srv.URL}, http: srv.Client()}
	got, err := s.Resolve(context.Background(), Item{DOI: doi})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := "/scidb/" + doi; gotPath != want {
		t.Errorf("request path = %q, want %q (DOI slashes must stay literal)", gotPath, want)
	}
	if got.FileURL != pdfURL {
		t.Errorf("FileURL = %q, want %q", got.FileURL, pdfURL)
	}
	if got.Ext != "pdf" {
		t.Errorf("Ext = %q, want pdf", got.Ext)
	}
	if got.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false for a DOI-keyed item")
	}
}

// TestSciDBResolveFailsOverMirrors verifies a mirror that errors, and one that
// serves a page with no PDF, are both skipped in favor of the next mirror.
func TestSciDBResolveFailsOverMirrors(t *testing.T) {
	const pdfURL = "https://cdn.example.net/d3/x/paper.pdf"

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	noPDF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body>no pdf here</body></html>`))
	}))
	defer noPDF.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(scidbPage(pdfURL)))
	}))
	defer good.Close()

	s := scidbSource{mirrors: staticMirrors{bad.URL, noPDF.URL, good.URL}, http: good.Client()}
	got, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.FileURL != pdfURL {
		t.Errorf("FileURL = %q, want %q", got.FileURL, pdfURL)
	}
}

// TestSciDBResolveNoMirrorYieldsPDF verifies an error is returned (so the chain
// advances) when no mirror embeds a PDF, rather than an empty Resolved.
func TestSciDBResolveNoMirrorYieldsPDF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body>nothing</body></html>`))
	}))
	defer srv.Close()

	s := scidbSource{mirrors: staticMirrors{srv.URL}, http: srv.Client()}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Fatal("Resolve() should fail when no mirror embeds a PDF")
	}
}

// TestSciDBResolveNoMirrors verifies an empty mirror list yields an error rather
// than a panic.
func TestSciDBResolveNoMirrors(t *testing.T) {
	s := scidbSource{mirrors: staticMirrors{}}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Fatal("Resolve() with no mirrors should fail")
	}
}

// TestExtractSciDBPDFPrefersViewerParam verifies the pdf.js viewer's file
// parameter wins and is percent-decoded, since it is the marker present whenever
// SciDB actually serves the article.
func TestExtractSciDBPDFPrefersViewerParam(t *testing.T) {
	const want = "https://cdn.example.net/d3/x/a b.pdf"
	body := []byte(`<a href="https://decoy.example/other.pdf">x</a>` +
		`<iframe src="/pdfjs/web/viewer.html?file=` + url.QueryEscape(want) + `"></iframe>`)
	got, ok := extractSciDBPDF(body)
	if !ok || got != want {
		t.Fatalf("extractSciDBPDF() = %q, %v; want %q, true", got, ok, want)
	}
}

// TestExtractSciDBPDFFallsBackToAbsoluteURL verifies a page without the viewer
// iframe still yields the bare absolute PDF URL.
func TestExtractSciDBPDFFallsBackToAbsoluteURL(t *testing.T) {
	const want = "https://cdn.example.net/d3/x/paper.pdf"
	got, ok := extractSciDBPDF([]byte(`<a href="` + want + `">dl</a>`))
	if !ok || got != want {
		t.Fatalf("extractSciDBPDF() = %q, %v; want %q, true", got, ok, want)
	}
}

// TestExtractSciDBPDFNone verifies a page carrying no PDF reference reports miss.
func TestExtractSciDBPDFNone(t *testing.T) {
	if got, ok := extractSciDBPDF([]byte(`<html><body>nothing</body></html>`)); ok {
		t.Fatalf("extractSciDBPDF() = %q, true; want miss", got)
	}
}

// TestSciDBSetsRefererToWinningMirror verifies the resolved request carries a
// Referer pointing at the mirror that served the page, which the CDN expects.
func TestSciDBSetsRefererToWinningMirror(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(scidbPage("https://cdn.example.net/d3/x/paper.pdf")))
	}))
	defer srv.Close()

	s := scidbSource{mirrors: staticMirrors{srv.URL + "/"}, http: srv.Client()}
	got, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	referer := got.Header.Get("Referer")
	if !strings.HasPrefix(referer, srv.URL) {
		t.Errorf("Referer = %q, want it to point at %q", referer, srv.URL)
	}
}
