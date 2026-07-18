package libgen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
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
