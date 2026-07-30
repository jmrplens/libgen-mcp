package libgen

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// scieloDOIBase is the default DOI resolver root a SciELO article is looked up
// through. It is overridable per source so tests can point at an httptest server.
//
// The resolver hop cannot be skipped the way dagstuhlSource skips it. A SciELO DOI
// carries nothing that predicts the article's address: the registered URL is the
// legacy form http://www.scielo.br/scielo.php?script=sci_arttext&pid=<PID>, whose
// PID is derivable only from the older DOIs that embed it (10.1590/s0104-6632…)
// and not at all from the modern ones (10.1590/1982-2553202568745), and that URL
// in turn redirects to a /j/<journal>/a/<key>/ address whose key appears in no
// identifier the caller holds. SciELO's own ArticleMeta API does map a DOI to its
// PID, but its 88 KB legacy record carries no /j/…/a/… key either, so it would
// cost a request and still leave the article page to fetch.
const scieloDOIBase = "https://doi.org"

// scieloDOIPrefix is the DOI registrant prefix FapUNIFESP registers SciELO Brazil
// articles under; the source claims only DOIs under it. It holds about 500,000
// records — the largest Latin American open-access corpus.
const scieloDOIPrefix = "10.1590/"

// scieloHostSuffix is the domain every 10.1590 DOI resolved to when this source was
// measured. The landing page is only scanned when the resolver actually arrived
// there, so a redirect that leads somewhere else is declined instead of being
// scraped for a meta tag it was never meant to carry.
const scieloHostSuffix = "scielo.br"

// scieloMaxBody bounds how many bytes of an article page are read while scanning
// for the full-text meta tag. Real article pages run to about 150 KB.
const scieloMaxBody = 4 << 20 // 4 MiB

// scieloPDFMeta matches an article page's citation_pdf_url meta tag, whose content
// is the article's PDF URL. The attribute order is fixed by the server-rendered
// template and a page carries exactly one such tag, so a targeted scan is enough
// and no HTML parse is needed.
var scieloPDFMeta = regexp.MustCompile(`name="citation_pdf_url"\s+content="([^"]*)"`)

// scieloArticleMarker is the meta tag every article page carries. Its absence from
// a 200 means the response is not an article page — a challenge interstitial, or a
// changed layout — and that must be reported as the source being unavailable rather
// than as the DOI being unheld.
var scieloArticleMarker = []byte(`name="citation_title"`)

// scieloSource resolves a SciELO Brazil DOI to the article's PDF at scielo.br.
//
// SciELO is the open-access publishing platform of Latin American science: every
// article it carries is free to read, served without an account, and licensed
// Creative Commons. The Brazilian collection alone runs to roughly half a million
// articles across the health, agricultural, social and human sciences, much of it
// in Portuguese and Spanish — literature the rest of the chain covers thinly.
//
// It is a genuine gap rather than a second route, and the gap is systematic rather
// than random. Over fourteen real DOIs sampled from 1998 to 2025, Unpaywall reported
// is_oa=true for all fourteen but supplied a downloadable url_for_pdf for only
// eight, and for none of the four sampled from 2025 — leaving its best_oa_location
// pointing at the HTML article page, which read cannot extract. fatcat advertised a
// preserved copy for ten of the fourteen and answered 404 for the most recent ones,
// its ingest lagging publication as an archive's must. Three of the fourteen were
// reachable from no source in the chain at all, keys or no keys, while scielo.br
// served them at 1,542,793, 737,104 and 610,410 bytes.
//
// MD5 verification is disabled, as for every DOI-keyed item.
type scieloSource struct {
	// http is the client used for the resolver request; when nil,
	// http.DefaultClient is used. Its redirect following is what carries the
	// request from doi.org to the article page.
	http *http.Client
	// baseURL overrides the DOI resolver root (defaults to scieloDOIBase); tests set
	// it to a local httptest server.
	baseURL string
	// hostSuffix overrides the domain the resolver must land on (defaults to
	// scieloHostSuffix); tests set it to their httptest server's host so the landing
	// check runs against a local server exactly as it does against the real one.
	hostSuffix string
}

// Compile-time assertion that scieloSource satisfies the DownloadSource contract.
var _ DownloadSource = scieloSource{}

// Name identifies the SciELO source.
func (s scieloSource) Name() string { return "scielo" }

// Supports reports that this source can resolve a DOI-keyed item whose DOI carries
// the SciELO Brazil registrant prefix, and only such items.
//
// The whole prefix is claimed rather than a subset of its suffixes, because SciELO
// mints two unrelated suffix shapes — the legacy PID-derived one (s0104-6632…) and
// the modern free-form one (1982-2553202568745, 0100-5405/225610) — and neither
// predicts anything the source needs. Every 10.1590 DOI is a SciELO Brazil article.
func (s scieloSource) Supports(it Item) bool {
	return it.DOI != "" && strings.HasPrefix(strings.ToLower(it.DOI), scieloDOIPrefix)
}

// Resolve follows the DOI to the article page at scielo.br and returns the PDF URL
// it advertises.
//
// The outcomes are kept distinct so a failure stays diagnosable: a DOI the resolver
// does not know (404), a page that is not on a SciELO host, and an article page
// carrying no full-text tag are clean misses about one item, so the chain simply
// moves on; a transport failure or a transient status is the source being
// unavailable, and so is a 200 that is not an article page, which is a changed site
// rather than empty coverage.
//
// An article with no PDF at all is a real and correct miss rather than a defect:
// SciELO's oldest records are HTML-only, and one such page — the 1998 article
// 10.1590/s0104-66321998000100007 — carries no citation_pdf_url and answers
// "PDF do Artigo não encontrado" to a request for one. No source in the chain
// reaches it, and this one declines rather than handing the pipeline a 404.
func (s scieloSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	if !s.Supports(it) {
		return Resolved{}, notIndexed(fmt.Errorf("scielo: %q is not a SciELO DOI", it.DOI))
	}
	page, err := s.fetchArticlePage(ctx, it.DOI)
	if err != nil {
		return Resolved{}, err
	}
	if !bytes.Contains(page, scieloArticleMarker) {
		return Resolved{}, unavailable(fmt.Errorf(
			"scielo: the page for %q is not an article page (challenge or changed layout)", it.DOI))
	}
	fileURL, ok := scieloFulltextURL(page)
	if !ok {
		return Resolved{}, notIndexed(fmt.Errorf("scielo: %q advertises no full-text PDF", it.DOI))
	}
	return Resolved{
		FileURL:   fileURL,
		VerifyMD5: false,
		Ext:       "pdf",
	}, nil
}

// fetchArticlePage requests the DOI through the resolver and returns the body of
// the page it lands on, after checking that the landing really is a SciELO host.
//
// The host check is what keeps this from being a general-purpose scraper of
// whatever a DOI happens to redirect to. Every one of the fourteen sampled DOIs
// landed on www.scielo.br; a 10.1590 DOI that leads anywhere else is a record this
// source has no business reading, so it is reported as a clean miss.
func (s scieloSource) fetchArticlePage(ctx context.Context, doi string) ([]byte, error) {
	base := s.baseURL
	if base == "" {
		base = scieloDOIBase
	}
	// The DOI's slashes stay literal in the path (the resolver keys by the
	// unescaped DOI); escapeDOIPath encodes only other URL-unsafe characters.
	endpoint := strings.TrimRight(base, "/") + "/" + escapeDOIPath(doi)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("scielo: building request for %q: %w", doi, err)
	}
	req.Header.Set("User-Agent", userAgent())

	resp, err := httpClientOr(s.http).Do(req)
	if err != nil {
		return nil, unavailable(fmt.Errorf("scielo: requesting %q: %w", doi, err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, notIndexed(fmt.Errorf("scielo: %q is not a registered SciELO article", doi))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, unavailableStatus(resp.StatusCode,
			fmt.Errorf("scielo: %q returned HTTP %d", doi, resp.StatusCode))
	}
	if !scieloHost(resp.Request.URL, s.landingHost()) {
		return nil, notIndexed(fmt.Errorf(
			"scielo: %q resolved to %q, not a SciELO article", doi, resp.Request.URL.Host))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, scieloMaxBody))
	if err != nil {
		return nil, unavailable(fmt.Errorf("scielo: reading the article page for %q: %w", doi, err))
	}
	return body, nil
}

// landingHost returns the domain this source requires the resolver to land on.
func (s scieloSource) landingHost() string {
	if s.hostSuffix == "" {
		return scieloHostSuffix
	}
	return s.hostSuffix
}

// scieloHost reports whether u is on suffix or one of its subdomains. The suffix is
// matched on a label boundary so a lookalike domain ending in the same letters
// cannot pass.
func scieloHost(u *url.URL, suffix string) bool {
	if u == nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == suffix || strings.HasSuffix(host, "."+suffix)
}

// scieloFulltextURL extracts the PDF URL an article page advertises, reporting
// whether it carried a usable one.
//
// The value is HTML-unescaped before use, which is not optional here: SciELO's PDF
// address is query-driven (…/?lang=en&format=pdf) and the markup writes its
// separator as &amp;, so requesting the literal text would ask for a parameter
// named "amp;format". It is also checked to be an absolute http(s) URL so a layout
// change cannot put a relative path or a non-HTTP scheme into the download
// pipeline.
func scieloFulltextURL(page []byte) (string, bool) {
	m := scieloPDFMeta.FindSubmatch(page)
	if m == nil {
		return "", false
	}
	candidate := strings.TrimSpace(html.UnescapeString(string(m[1])))
	if !absoluteHTTPURL(candidate) {
		return "", false
	}
	return candidate, true
}
