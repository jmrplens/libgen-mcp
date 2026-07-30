package libgen

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// scihubFixture loads the captured Sci-Hub article page used across tests.
func scihubFixture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/scihub_article.html")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return body
}

// TestScihubExtractPDF verifies that the PDF URL is pulled from the page's
// id="pdf" element (not reconstructed from the DOI), with the viewer fragment
// dropped.
func TestScihubExtractPDF(t *testing.T) {
	url, ok := extractScihubPDF(scihubFixture(t))
	if !ok {
		t.Fatal("extractScihubPDF() found no PDF in the article fixture")
	}
	const want = "https://sci.bban.top/pdf/10.1016/j.cell.2016.01.043.pdf"
	if url != want {
		t.Errorf("extractScihubPDF() = %q, want %q", url, want)
	}
}

// TestPDFElementSrcParseError covers pdfElementSrc's parse-failure branch by
// overriding the htmlParse seam to return an error. The real html.Parse never
// errors on in-memory bytes, so this guard is otherwise unreachable.
func TestPDFElementSrcParseError(t *testing.T) {
	orig := htmlParse
	htmlParse = func(io.Reader) (*html.Node, error) { return nil, errors.New("forced parse error") }
	t.Cleanup(func() { htmlParse = orig })

	if src, ok := pdfElementSrc([]byte(`<iframe id="pdf" src="x.pdf"></iframe>`)); ok || src != "" {
		t.Errorf("pdfElementSrc on a parse error = (%q, %v), want (\"\", false)", src, ok)
	}
}

// TestScihubExtractVariants exercises backslash unescaping and protocol-relative
// normalization on representative id="pdf" snippets that live mirrors emit.
func TestScihubExtractVariants(t *testing.T) {
	cases := []struct {
		name string
		html string
		want string
	}{
		{
			name: "backslash-escaped",
			html: `<iframe id="pdf" src="https:\/\/sci.bban.top\/pdf\/10.1x\/y.pdf#view=FitH"></iframe>`,
			want: "https://sci.bban.top/pdf/10.1x/y.pdf",
		},
		{
			name: "protocol-relative",
			html: `<embed id="pdf" src="//sci.bban.top/pdf/10.1x/z.pdf"></embed>`,
			want: "https://sci.bban.top/pdf/10.1x/z.pdf",
		},
		{
			name: "location-href-fallback",
			html: `<div><a onclick="location.href='https:\/\/sci.bban.top\/pdf\/10.1x\/w.pdf?download=true'">save</a></div>`,
			want: "https://sci.bban.top/pdf/10.1x/w.pdf?download=true",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url, ok := extractScihubPDF([]byte(tc.html))
			if !ok {
				t.Fatalf("extractScihubPDF() found no PDF in %q", tc.name)
			}
			if url != tc.want {
				t.Errorf("extractScihubPDF() = %q, want %q", url, tc.want)
			}
		})
	}
}

// scihubHostServer starts an httptest server returning body for any path and
// returns its bare host:port (the value that goes into scihubSource.hosts).
func scihubHostServer(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// TestScihubResolveFirstHostWins verifies that a host serving a challenge page
// with no id="pdf" is skipped and the next host serving the article wins, with
// the Referer header pointing at the winning host.
func TestScihubResolveFirstHostWins(t *testing.T) {
	noPDF := scihubHostServer(t, "<html><body>captcha, please solve</body></html>")
	withPDF := scihubHostServer(t, string(scihubFixture(t)))

	s := scihubSource{hosts: []string{noPDF, withPDF}, http: http.DefaultClient, scheme: "http"}
	res, err := s.Resolve(context.Background(), Item{DOI: "10.1016/j.cell.2016.01.043"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	const wantURL = "https://sci.bban.top/pdf/10.1016/j.cell.2016.01.043.pdf"
	if res.FileURL != wantURL {
		t.Errorf("FileURL = %q, want %q", res.FileURL, wantURL)
	}
	if res.VerifyMD5 {
		t.Error("VerifyMD5 = true, want false (DOI-keyed)")
	}
	if res.Ext != "pdf" {
		t.Errorf("Ext = %q, want %q", res.Ext, "pdf")
	}
	if got := res.Header.Get("Referer"); got != "http://"+withPDF+"/" {
		t.Errorf("Referer = %q, want %q", got, "http://"+withPDF+"/")
	}
}

// TestScihubNoArticle verifies that when no host yields an id="pdf", Resolve
// returns an error so the download chain falls through.
func TestScihubNoArticle(t *testing.T) {
	a := scihubHostServer(t, "<html><body>not found</body></html>")
	b := scihubHostServer(t, "<html><body>solve the captcha</body></html>")

	s := scihubSource{hosts: []string{a, b}, http: http.DefaultClient, scheme: "http"}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Fatal("Resolve() with no id=pdf on any host should return an error")
	}
}

// TestScihubRejectsNon200WithPDF verifies the 200 gate: a host that serves a
// valid id="pdf" element but replies with a non-200 status is skipped, so a stale
// PDF link on a challenge/error page is never handed back.
func TestScihubRejectsNon200WithPDF(t *testing.T) {
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write(scihubFixture(t)) // real id="pdf", but behind a 403
	}))
	t.Cleanup(blocked.Close)
	host := strings.TrimPrefix(blocked.URL, "http://")

	s := scihubSource{hosts: []string{host}, http: http.DefaultClient, scheme: "http"}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1016/j.cell.2016.01.043"}); err == nil {
		t.Fatal("Resolve() must reject a PDF scraped from a non-200 response")
	}
}

// TestScihubNoHosts covers the "no hosts configured" branch: with an empty host
// list and no per-host error, Resolve reports that nothing could be tried.
func TestScihubNoHosts(t *testing.T) {
	s := scihubSource{hosts: nil, http: http.DefaultClient, scheme: "http"}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Error("Resolve with no hosts configured should fail")
	}
}

// TestScihubDefaultScheme covers the default-scheme branch: an empty scheme
// defaults to https, which cannot complete against a plain-http test host, so the
// host is skipped and Resolve fails.
func TestScihubDefaultScheme(t *testing.T) {
	host := scihubHostServer(t, "<html></html>")
	s := scihubSource{hosts: []string{host}, http: http.DefaultClient} // scheme "" -> https
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Error("Resolve should fail when https is attempted against an http host")
	}
}

// TestScihubDefaultClient covers the default-client branch: with a nil http client
// Resolve uses http.DefaultClient, which resolves the article fixture over http.
func TestScihubDefaultClient(t *testing.T) {
	host := scihubHostServer(t, string(scihubFixture(t)))
	s := scihubSource{hosts: []string{host}, scheme: "http"} // http nil -> default client
	res, err := s.Resolve(context.Background(), Item{DOI: "10.1016/j.cell.2016.01.043"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if res.FileURL == "" {
		t.Error("Resolve() returned an empty FileURL")
	}
}

// TestScihubRequestBuildError covers tryHost's request-construction failure: a host
// containing a control character cannot be turned into a request.
func TestScihubRequestBuildError(t *testing.T) {
	s := scihubSource{hosts: []string{"\x7fbad"}, http: http.DefaultClient, scheme: "http"}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Error("Resolve should fail when a host yields an unbuildable request")
	}
}

// TestScihubBodyReadError covers tryHost's body-read failure: a mirror that
// declares more bytes than it sends, then closes, makes reading the body fail.
func TestScihubBodyReadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		// Declare 1000 bytes but send only 5, then close: the client's read of the
		// body fails with an unexpected EOF.
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\nshort"))
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	s := scihubSource{hosts: []string{host}, http: http.DefaultClient, scheme: "http"}
	if _, err := s.Resolve(context.Background(), Item{DOI: "10.1/x"}); err == nil {
		t.Error("Resolve should fail when the mirror body cannot be fully read")
	}
}

// scihubStatusHost starts an httptest server answering every request with status
// and the real article fixture as the body, and returns its bare host:port. The
// body is a genuine id="pdf" page on purpose: a status gate that stopped holding
// would resolve here instead of failing, so the classification assertion doubles
// as a check that a non-200 is never scraped.
func scihubStatusHost(t *testing.T, status int) string {
	t.Helper()
	body := scihubFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// TestScihubErrorClassification pins which failures Resolve reports as Sci-Hub
// answering "I do not hold this" (ErrNotIndexed) and which as Sci-Hub being unable
// to answer at all (ErrSourceUnavailable).
//
// The chain acts on the difference and the cost of getting it wrong is asymmetric:
// a miss tagged as unavailability sidelines a healthy mirror for five minutes,
// while an outage tagged as a miss is returned unwrapped from startAttempt and so
// skips the retry that would have ridden out the blip. Everything here was
// previously exercised by tests that only asserted that *some* error came back.
func TestScihubErrorClassification(t *testing.T) {
	const doi = "10.1016/j.cell.2016.01.043"

	t.Run("a 200 page with no PDF link is a clean miss", func(t *testing.T) {
		host := scihubHostServer(t, "<html><body>article not found</body></html>")
		s := scihubSource{hosts: []string{host}, http: http.DefaultClient, scheme: "http"}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertCleanMiss(t, err)
	})

	t.Run("a transient status is unavailability", func(t *testing.T) {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
			host := scihubStatusHost(t, status)
			s := scihubSource{hosts: []string{host}, http: http.DefaultClient, scheme: "http"}

			_, err := s.Resolve(context.Background(), Item{DOI: doi})
			assertUnavailable(t, err)
		}
	})

	t.Run("a transport failure is unavailability", func(t *testing.T) {
		s := scihubSource{hosts: []string{"sci-hub.invalid"}, http: refusingClient(), scheme: "http"}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertUnavailable(t, err)
	})

	t.Run("a body that cannot be read is unavailability", func(t *testing.T) {
		// A mirror that promises more bytes than it sends taught us nothing about the
		// article, only that the connection broke mid-response.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hj, ok := w.(http.Hijacker)
			if !ok {
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\nshort"))
			_ = conn.Close()
		}))
		t.Cleanup(srv.Close)
		s := scihubSource{hosts: []string{strings.TrimPrefix(srv.URL, "http://")}, http: http.DefaultClient, scheme: "http"}

		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertUnavailable(t, err)
	})

	t.Run("a 403 or 404 is neither", func(t *testing.T) {
		// unavailableStatus tags only 5xx and 429, so a mirror's challenge page (403)
		// or missing-article page (404) comes back unclassified. That is deliberate
		// for the 403 — a challenge is not proof the service is down — and it costs
		// the 404 the ErrNotIndexed short-circuit, so the chain's last resort spends
		// its full start-retry schedule re-asking. Pinned here so the trade-off is a
		// decision on the record rather than an accident.
		for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
			host := scihubStatusHost(t, status)
			s := scihubSource{hosts: []string{host}, http: http.DefaultClient, scheme: "http"}

			_, err := s.Resolve(context.Background(), Item{DOI: doi})
			if err == nil {
				t.Fatalf("HTTP %d must not resolve", status)
			}
			if errors.Is(err, ErrNotIndexed) {
				t.Errorf("HTTP %d read as a clean miss", status)
			}
			if errors.Is(err, ErrSourceUnavailable) || cooldownWorthy(context.Background(), err) {
				t.Errorf("HTTP %d put the source in cooldown", status)
			}
		}
	})
}

// TestScihubClassificationIsTakenFromTheLastHostTried documents that Resolve keeps
// only the most recent host's error, so when hosts disagree the verdict belongs to
// whichever was tried last. It matters because the chain reads that verdict: the
// same pair of mirrors in the opposite order cools the source down or does not.
func TestScihubClassificationIsTakenFromTheLastHostTried(t *testing.T) {
	const doi = "10.1016/j.cell.2016.01.043"
	down := scihubStatusHost(t, http.StatusServiceUnavailable)
	empty := scihubHostServer(t, "<html><body>nothing here</body></html>")

	t.Run("miss last", func(t *testing.T) {
		s := scihubSource{hosts: []string{down, empty}, http: http.DefaultClient, scheme: "http"}
		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertCleanMiss(t, err)
	})

	t.Run("outage last", func(t *testing.T) {
		s := scihubSource{hosts: []string{empty, down}, http: http.DefaultClient, scheme: "http"}
		_, err := s.Resolve(context.Background(), Item{DOI: doi})
		assertUnavailable(t, err)
	})
}

// TestScihubSupports verifies the source claims DOI-keyed items only.
func TestScihubSupports(t *testing.T) {
	s := scihubSource{}
	if s.Supports(Item{DOI: ""}) {
		t.Error("Supports(empty DOI) = true, want false")
	}
	if !s.Supports(Item{DOI: "10.1/x"}) {
		t.Error("Supports(non-empty DOI) = false, want true")
	}
	if s.Name() != "scihub" {
		t.Errorf("Name() = %q, want %q", s.Name(), "scihub")
	}
}
