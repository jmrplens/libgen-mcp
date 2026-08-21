package libgen

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
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

// rangeServingBody builds an httptest server that serves body at "/" with the
// given Content-Type and honors a single "bytes=<start>-<end>" Range request the
// way a real CDN does: a 206 carrying exactly the requested slice and a
// Content-Range header. A request without a Range gets the whole body with a 200.
func rangeServingBody(t *testing.T, body, contentType string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		spec := strings.TrimPrefix(r.Header.Get("Range"), "bytes=")
		if spec == "" {
			_, _ = io.WriteString(w, body)
			return
		}
		startStr, endStr, _ := strings.Cut(spec, "-")
		start, _ := strconv.Atoi(startStr)
		end, _ := strconv.Atoi(endStr)
		if end >= len(body) {
			end = len(body) - 1
		}
		if start > end {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		slice := body[start : end+1]
		w.Header().Set("Content-Range", "bytes "+strconv.Itoa(start)+"-"+strconv.Itoa(end)+"/"+strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, slice)
	}))
}

// TestProbePDFMagicBytes verifies the magic-number fallback probePDF relies on
// when a server does not announce a PDF Content-Type — the common shape for an
// institutional fileserver behind CORE, or Europe PMC's article render path.
//
// The Range-honoring case is the one that matters: the probe must request enough
// bytes for the "%PDF" marker to arrive. A one-byte range can never match it, so a
// live open-access PDF served as application/octet-stream would be judged not a
// PDF and its source dropped from the chain.
func TestProbePDFMagicBytes(t *testing.T) {
	const pdfBody = "%PDF-1.4\n1 0 obj\n"

	t.Run("range honored, non-pdf content type", func(t *testing.T) {
		srv := rangeServingBody(t, pdfBody, "application/octet-stream")
		defer srv.Close()
		if !probePDF(context.Background(), srv.Client(), srv.URL) {
			t.Error("probePDF() = false, want true (body starts with the %PDF magic number)")
		}
	})

	t.Run("range ignored, full body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, pdfBody)
		}))
		defer srv.Close()
		if !probePDF(context.Background(), srv.Client(), srv.URL) {
			t.Error("probePDF() = false, want true (server ignored Range and returned the whole PDF)")
		}
	})

	t.Run("non-pdf body", func(t *testing.T) {
		srv := rangeServingBody(t, "<html><body>gone</body></html>", "application/octet-stream")
		defer srv.Close()
		if probePDF(context.Background(), srv.Client(), srv.URL) {
			t.Error("probePDF() = true, want false (body is not a PDF)")
		}
	})

	t.Run("pdf content type short-circuits", func(t *testing.T) {
		srv := rangeServingBody(t, "not really a pdf", "application/pdf")
		defer srv.Close()
		if !probePDF(context.Background(), srv.Client(), srv.URL) {
			t.Error("probePDF() = false, want true (Content-Type names a PDF)")
		}
	})
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

// TestPartialKey verifies the partial-file key derivation for all four item
// shapes: md5-keyed (historical LibGen path), DOI-keyed, ISBN-keyed, and URL-only
// (no identifier at all), each yielding a stable, filesystem-safe token.
func TestPartialKey(t *testing.T) {
	if got := partialKey(Item{MD5: "abc"}, Resolved{}); got != "abc" {
		t.Errorf("partialKey(md5) = %q, want %q", got, "abc")
	}
	if got := partialKey(Item{DOI: "10.1/x"}, Resolved{}); !strings.HasPrefix(got, "doi-") {
		t.Errorf("partialKey(doi) = %q, want a doi- prefix", got)
	}
	// The ISBN must win over the resolved URL: an open-access book source picks
	// among candidate copies, so keying on the URL would lose a resumable partial
	// the moment a retry resolved a different candidate.
	isbnKey := partialKey(Item{ISBN: "9789286150616"}, Resolved{FileURL: "https://cdn.example/a"})
	if !strings.HasPrefix(isbnKey, "isbn-") {
		t.Errorf("partialKey(isbn) = %q, want an isbn- prefix", isbnKey)
	}
	if other := partialKey(Item{ISBN: "9789286150616"}, Resolved{FileURL: "https://cdn.example/b"}); other != isbnKey {
		t.Errorf("partialKey(isbn) changed with the resolved URL (%q vs %q); a resume would be lost", isbnKey, other)
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

// slowSource is a test DownloadSource whose Resolve blocks for delay unless the
// context is canceled first, in which case it reports the context error the way a
// well-behaved source does. It exists to prove a source cannot hold the chain for
// longer than its resolve budget.
type slowSource struct {
	name     string
	delay    time.Duration
	resolved Resolved
}

func (s slowSource) Name() string       { return s.name }
func (s slowSource) Supports(Item) bool { return true }
func (s slowSource) Resolve(ctx context.Context, _ Item) (Resolved, error) {
	timer := time.NewTimer(s.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return Resolved{}, ctx.Err()
	case <-timer.C:
		return s.resolved, nil
	}
}

// TestResolveBudgetBoundsEachSource verifies that a source which cannot resolve
// promptly is abandoned at its budget rather than being allowed to spend the whole
// request timeout (or, for a multi-hop source, several of them) while the sources
// behind it wait.
//
// The article chain grew to seven sources, most of which resolve over the same
// bounded-per-request client; without a per-source budget their worst cases add up
// serially in front of the last-resort source, which is the one most likely to
// actually serve the file. The chain must still advance and complete.
func TestResolveBudgetBoundsEachSource(t *testing.T) {
	payload := []byte("%PDF-1.4 budget payload")
	cdn := fileCDN(t, payload, `attachment; filename="b.pdf"`)
	defer cdn.Close()

	const budget = 150 * time.Millisecond
	// Timeout is deliberately left long: the bound under test is ResolveBudget's,
	// and the two are separate settings.
	cfg := &config.Config{
		Timeout:                30 * time.Second,
		ResolveBudget:          budget,
		RateRPS:                1000,
		RateBurst:              100,
		RetryAttempts:          1,
		MaxConcurrentDownloads: 1,
	}
	slow := slowSource{name: "slow", delay: 10 * time.Second}
	good := stubSource{name: "good", supports: true, resolved: Resolved{FileURL: cdn.URL + "/file"}}
	c := New(staticMirrors{}, cfg, WithSources(slow, good))

	started := time.Now()
	res, err := c.DownloadItem(context.Background(), Item{DOI: "10.1234/budget"}, t.TempDir(), "")
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("DownloadItem() error = %v, want the chain to advance past the slow source", err)
	}
	if res.Source != "good" {
		t.Errorf("Source = %q, want %q", res.Source, "good")
	}
	// Generous headroom over the budget so the assertion is about the bound, not
	// about scheduling jitter; the unbounded behavior takes the source's full 10s.
	if limit := 5 * time.Second; elapsed >= limit {
		t.Errorf("chain took %v, want under %v: the slow source was not bounded by its resolve budget", elapsed, limit)
	}
}

// TestResolveBudgetIsIndependentOfTimeout is the guard on the two settings staying
// decoupled: a source that needs longer than the per-request timeout to resolve
// must still be given its full ResolveBudget.
//
// They used to be one setting — the client took cfg.Timeout as each source's whole
// resolve budget — so shortening the per-request timeout from 30s to 10s silently
// cut every source's resolve budget with it. A resolve is a multi-hop conversation
// (Sci-Hub fetches an article page and then the file URL embedded in it), so a
// source that legitimately needs several requests' worth of time was at risk of
// being struck out of a chain it would have served. This test fails if the budget
// is ever derived from Timeout again.
func TestResolveBudgetIsIndependentOfTimeout(t *testing.T) {
	payload := []byte("%PDF-1.4 decoupled payload")
	cdn := fileCDN(t, payload, `attachment; filename="d.pdf"`)
	defer cdn.Close()

	// The source takes ten times the per-request timeout to resolve, and the budget
	// is generous enough to let it.
	cfg := &config.Config{
		Timeout:                20 * time.Millisecond,
		ResolveBudget:          5 * time.Second,
		RateRPS:                1000,
		RateBurst:              100,
		RetryAttempts:          1,
		MaxConcurrentDownloads: 1,
	}
	slow := slowSource{name: "slow", delay: 200 * time.Millisecond, resolved: Resolved{FileURL: cdn.URL + "/file"}}
	c := New(staticMirrors{}, cfg, WithSources(slow))

	res, err := c.DownloadItem(context.Background(), Item{DOI: "10.1234/decoupled"}, t.TempDir(), "")
	if err != nil {
		t.Fatalf("DownloadItem() error = %v, want the slow source to get its full resolve budget", err)
	}
	if res.Source != "slow" {
		t.Errorf("Source = %q, want %q", res.Source, "slow")
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

	t.Run("europepmc", func(t *testing.T) {
		search := europePMCSearchServer(t, "europepmc_oa.json", http.StatusOK, nil)
		defer search.Close()
		render := europePMCRenderServer(t)
		defer render.Close()
		s := europePMCSource{http: search.Client(), searchBase: search.URL, renderBase: render.URL}
		assertDeclaresExt(t, s, Item{DOI: doi})
	})

	t.Run("biorxiv", func(t *testing.T) {
		api := biorxivFixtureServer(t, map[string]string{"biorxiv": "biorxiv_hit.json"}, nil)
		defer api.Close()
		s := biorxivSource{http: api.Client(), apiBase: api.URL, contentBase: "https://content.example"}
		assertDeclaresExt(t, s, Item{DOI: "10.1101/known"})
	})

	t.Run("fatcat", func(t *testing.T) {
		stub := startFatcatStub(t)
		stub.files["/preserved.pdf"] = fatcatStubPDF
		stub.release = fatcatPage(stub.srv.URL + "/preserved.pdf")
		assertDeclaresExt(t, stub.source(), Item{DOI: doi})
	})

	t.Run("core", func(t *testing.T) {
		s, _ := coreLiveSource(t)
		assertDeclaresExt(t, s, Item{DOI: doi})
	})

	t.Run("rfc", func(t *testing.T) {
		assertDeclaresExt(t, rfcSource{}, Item{DOI: "10.17487/RFC9110"})
	})

	t.Run("nist", func(t *testing.T) {
		assertDeclaresExt(t, nistSource{}, Item{DOI: "10.6028/NIST.SP.800-53r5"})
	})

	t.Run("dagstuhl", func(t *testing.T) {
		stub := startDagstuhlStub(t)
		assertDeclaresExt(t, stub.source(), Item{DOI: "10.4230/LIPIcs.ICALP.2023.1"})
	})

	t.Run("acl", func(t *testing.T) {
		assertDeclaresExt(t, aclSource{}, Item{DOI: "10.18653/v1/N19-1423"})
	})

	t.Run("zenodo", func(t *testing.T) {
		// Zenodo is the one source whose extension varies per record, so the entry it
		// picks is what supplies it — and it must come from the file's name, since the
		// mimetype the listing reports is octet-stream for anything Zenodo does not
		// recognize and the file endpoint serves everything as octet-stream regardless.
		stub := startZenodoStub(t)
		stub.listings["3233986"] = zenodoListing(
			zenodoFile("paper.pdf", 10, "https://example.invalid/paper.pdf"),
		)
		assertDeclaresExt(t, stub.source(), Item{DOI: "10.5281/zenodo.3233986"})
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
