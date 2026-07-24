package libgen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubSource is a test DownloadSource whose behavior (support decision, resolve
// outcome) is fully controlled by its fields, letting tests assemble arbitrary
// source chains without any network resolution.
type stubSource struct {
	name       string
	supports   bool
	resolveErr error
	resolved   Resolved
}

func (s stubSource) Name() string       { return s.name }
func (s stubSource) Supports(Item) bool { return s.supports }
func (s stubSource) Resolve(context.Context, Item) (Resolved, error) {
	if s.resolveErr != nil {
		return Resolved{}, s.resolveErr
	}
	return s.resolved, nil
}

// fileCDN builds a bare httptest server that serves payload as an octet-stream at
// /file, with the given Content-Disposition (empty to omit it). Unlike
// md5CDNServer it has no ads.php/get.php: sources resolve straight to its /file.
func fileCDN(t *testing.T, payload []byte, disposition string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/file", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		if disposition != "" {
			w.Header().Set("Content-Disposition", disposition)
		}
		_, _ = w.Write(payload)
	})
	return httptest.NewServer(mux)
}

// TestEscapeDOIPath verifies that escapeDOIPath keeps a DOI's slashes literal
// (the DOI-keyed APIs require them unescaped) while percent-encoding other
// URL-unsafe characters that would otherwise corrupt the request path.
func TestEscapeDOIPath(t *testing.T) {
	tests := []struct {
		name string
		doi  string
		want string
	}{
		{name: "plain DOI keeps slash", doi: "10.1234/abc.def", want: "10.1234/abc.def"},
		{name: "multiple slashes preserved", doi: "10.1000/journal/issue/5", want: "10.1000/journal/issue/5"},
		{name: "space is encoded", doi: "10.1234/abc def", want: "10.1234/abc%20def"},
		{name: "hash is encoded but slash survives", doi: "10.1234/ab#cd", want: "10.1234/ab%23cd"},
		{name: "question mark is encoded", doi: "10.1234/ab?cd", want: "10.1234/ab%3Fcd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeDOIPath(tt.doi); got != tt.want {
				t.Errorf("escapeDOIPath(%q) = %q, want %q", tt.doi, got, tt.want)
			}
		})
	}
}

// TestPartialKey verifies the partial-file key derivation for all three item
// shapes: md5-keyed (historical LibGen path), DOI-keyed, and URL-only (neither
// md5 nor DOI), each yielding a stable, filesystem-safe token.
func TestPartialKey(t *testing.T) {
	if got := partialKey(Item{MD5: "abc"}, Resolved{}); got != "abc" {
		t.Errorf("partialKey(md5) = %q, want %q", got, "abc")
	}
	if got := partialKey(Item{DOI: "10.1/x"}, Resolved{}); !strings.HasPrefix(got, "doi-") {
		t.Errorf("partialKey(doi) = %q, want a doi- prefix", got)
	}
	got := partialKey(Item{}, Resolved{FileURL: "https://cdn.example/file"})
	if !strings.HasPrefix(got, "url-") {
		t.Errorf("partialKey(url-only) = %q, want a url- prefix", got)
	}
}

// TestSanitizeForPart verifies that unsafe characters in a source name are mapped
// to '_' while ASCII letters, digits and '-' survive for embedding in a .part name.
func TestSanitizeForPart(t *testing.T) {
	if got := sanitizeForPart("libgen"); got != "libgen" {
		t.Errorf("sanitizeForPart(libgen) = %q, want libgen", got)
	}
	if got := sanitizeForPart("a/b c.d"); got != "a_b_c_d" {
		t.Errorf("sanitizeForPart(%q) = %q, want a_b_c_d", "a/b c.d", got)
	}
}

// TestMirrorOf verifies the origin extraction, including the fallback that returns
// the raw string when the URL has no parseable host.
func TestMirrorOf(t *testing.T) {
	if got := mirrorOf("https://cdn.example.org/path/file.pdf"); got != "https://cdn.example.org" {
		t.Errorf("mirrorOf() = %q, want https://cdn.example.org", got)
	}
	if got := mirrorOf("not-a-url"); got != "not-a-url" {
		t.Errorf("mirrorOf(no host) = %q, want the raw string", got)
	}
}

// TestLibgenSourceResolveError verifies that when the ads.php lookup fails (the
// mirror returns 404), libgenSource.Resolve surfaces the error so the download
// chain can advance.
func TestLibgenSourceResolveError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	s := libgenSource{c: c}
	if _, err := s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}); err == nil {
		t.Fatal("Resolve() should fail when ads.php cannot be fetched")
	}
}

// TestDownloadSourceChainFallback verifies the source chain advances past a source
// whose Resolve fails and completes via the next source, tagging the result with
// the serving source's Name().
func TestDownloadSourceChainFallback(t *testing.T) {
	payload := []byte("%PDF-1.4 chain fallback payload")
	want := md5Hex(payload)
	cdn := fileCDN(t, payload, `attachment; filename="fb.pdf"`)
	defer cdn.Close()

	c := newTestClient(staticMirrors{})
	bad := stubSource{name: "bad", supports: true, resolveErr: errors.New("resolve boom")}
	good := stubSource{name: "good", supports: true, resolved: Resolved{FileURL: cdn.URL + "/file", VerifyMD5: true}}
	c.sources = []DownloadSource{bad, good}

	dir := t.TempDir()
	res, err := c.Download(context.Background(), want, dir, "", nil)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if res.Source != "good" {
		t.Errorf("Source = %q, want %q", res.Source, "good")
	}
	if !res.Verified {
		t.Error("Verified = false, want true")
	}
}

// TestVerifyMD5Conditional verifies that MD5 verification is gated by
// Resolved.VerifyMD5: a mismatch is tolerated when false (verification skipped)
// and rejected when true.
func TestVerifyMD5Conditional(t *testing.T) {
	payload := []byte("%PDF-1.4 conditional verify payload")
	wrongMD5 := md5Hex([]byte("some other content entirely"))
	if wrongMD5 == md5Hex(payload) {
		t.Fatal("test setup: md5s unexpectedly collide")
	}

	t.Run("skip verification", func(t *testing.T) {
		cdn := fileCDN(t, payload, `attachment; filename="nv.pdf"`)
		defer cdn.Close()
		c := newTestClient(staticMirrors{})
		c.sources = []DownloadSource{stubSource{name: "noverify", supports: true, resolved: Resolved{FileURL: cdn.URL + "/file", VerifyMD5: false}}}
		res, err := c.Download(context.Background(), wrongMD5, t.TempDir(), "", nil)
		if err != nil {
			t.Fatalf("Download() error = %v, want nil (verification skipped)", err)
		}
		if res.Verified {
			t.Error("Verified = true, want false (verification was skipped)")
		}
	})

	t.Run("enforce verification", func(t *testing.T) {
		cdn := fileCDN(t, payload, `attachment; filename="v.pdf"`)
		defer cdn.Close()
		c := newTestClient(staticMirrors{})
		c.sources = []DownloadSource{stubSource{name: "verify", supports: true, resolved: Resolved{FileURL: cdn.URL + "/file", VerifyMD5: true}}}
		if _, err := c.Download(context.Background(), wrongMD5, t.TempDir(), "", nil); err == nil {
			t.Fatal("Download() error = nil, want a verification failure (md5 mismatch)")
		}
	})
}

// TestSourcesThatKnowTheTypeDeclareIt verifies every source that can know what it
// is serving says so on Resolved.Ext.
//
// It exists because the fix for this had to be made twice. A file fetched by
// content address carries no name, so the saved file has no extension and the read
// tool has no extractor to choose; the Anna's source was fixed on its keyless path
// and still silently dropped the type on its member path. A source added later
// should fail here rather than in a live evaluator run.
//
// libgen and randombook are deliberately absent: they stream from a CDN that names
// the file in its content-disposition, so the type arrives with the bytes rather
// than being known in advance. The extractor's content sniffing is the backstop
// when even that is missing.
func TestSourcesThatKnowTheTypeDeclareIt(t *testing.T) {
	pdfServing := func(body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte(body))
		}))
	}
	const doi = "10.1234/known"

	t.Run("unpaywall", func(t *testing.T) {
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"is_oa":true,"best_oa_location":{"url_for_pdf":"https://example.invalid/a.pdf"}}`))
		}))
		defer api.Close()
		s := unpaywallSource{email: "e@example.com", http: api.Client(), baseURL: api.URL}
		assertDeclaresExt(t, s, Item{DOI: doi})
	})

	t.Run("scihub", func(t *testing.T) {
		page := pdfServing(`<html><body><embed id="pdf" src="//example.invalid/x.pdf"></body></html>`)
		defer page.Close()
		s := scihubSource{hosts: []string{strings.TrimPrefix(page.URL, "http://")}, scheme: "http"}
		assertDeclaresExt(t, s, Item{DOI: doi})
	})

	t.Run("scidb", func(t *testing.T) {
		page := pdfServing(`<html><body><a href="https://example.invalid/paper.pdf">pdf</a></body></html>`)
		defer page.Close()
		s := scidbSource{mirrors: staticMirrors{page.URL}}
		assertDeclaresExt(t, s, Item{DOI: doi})
	})
}

// assertDeclaresExt resolves an item and fails when the source announced no type.
func assertDeclaresExt(t *testing.T, s DownloadSource, it Item) {
	t.Helper()
	got, err := s.Resolve(context.Background(), it)
	if err != nil {
		t.Fatalf("%s did not resolve against its own stub: %v", s.Name(), err)
	}
	if got.Ext == "" {
		t.Errorf("%s resolved without declaring a file type; a caller cannot name the saved file", s.Name())
	}
}
