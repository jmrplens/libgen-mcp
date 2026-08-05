// Package discovery federates keyless literature sources into a single result shape
// the search tool can present and the read/download tools can act on: the open-access
// providers (arXiv, Crossref, OpenLibrary, Project Gutenberg) plus the bibliographic
// indexes (dblp for computer science, PubMed for biomedicine), which describe a paper
// precisely without claiming it is free to read, and ERIC, which reaches the education
// grey literature — reports, theses, agency documents — that has no DOI and so appears
// in none of the others. Every provider is best-effort: a failing source
// degrades to an empty result rather than sinking a federated search, and no source
// requires an API key, account, or login.
package discovery

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/version"
)

// discoveryMaxBody bounds how many bytes of any provider response are read before
// parsing, guarding against an unexpectedly large or hostile body.
const discoveryMaxBody = 1 << 20 // 1 MiB

// discoveryTimeout is the per-provider wall-clock budget for a single Search call:
// the whole request (limiter wait plus HTTP round-trip plus body read) must finish
// within it, otherwise discovery degrades to empty rather than delaying search.
const discoveryTimeout = 6 * time.Second

// discoveryUserAgent identifies libgen-mcp to open-access APIs; sources such as
// arXiv request courtesy identification with a contact/repository URL.
// discoveryUserAgent is the User-Agent every provider request carries. It is a
// function rather than a constant because the version it reports is stamped into
// the binary and handed to internal/version during startup, after this package's
// variables are initialized.
func discoveryUserAgent() string { return version.UserAgent() }

// DiscoveryResult is one open-access hit from a discovery provider, in a shape
// the search tool can present and the read/download tools can act on. The name is
// the shared contract type referenced by every provider and the federation layer,
// so the revive stutter suggestion is intentionally waived here.
//
//nolint:revive // DiscoveryResult is the deliberate cross-package contract name.
type DiscoveryResult struct {
	Origin  string `json:"origin" jsonschema:"which provider produced this result: arxiv, crossref, openlibrary, gutenberg, dblp, pubmed, eric or annas"`
	Title   string `json:"title,omitempty" jsonschema:"record title"`
	Authors string `json:"authors,omitempty" jsonschema:"authors"`
	Year    string `json:"year,omitempty" jsonschema:"publication year"`
	DOI     string `json:"doi,omitempty" jsonschema:"article DOI; pass to read or download to fetch this paper"`
	ISBN    string `json:"isbn,omitempty" jsonschema:"ISBN; use it to refine a libgen search"`
	// Venue is the publication venue when the source states one: arXiv's journal_ref
	// (e.g. "Phys. Rev. Lett. 100, 012345 (2021)"), the conference or journal dblp
	// files a record under (e.g. "DAC", "Commun. ACM"), or PubMed's full journal name.
	// It is a short citation string, never an abstract, so it stays cheap to carry.
	Venue string `json:"venue,omitempty" jsonschema:"publication venue as the provider states it (arXiv journal_ref, dblp venue, PubMed journal): a short citation string, never an abstract"`
	// MD5 is the file digest when the provider is md5-keyed (Anna's Archive).
	// Empty for the DOI-keyed open-access providers.
	MD5 string `json:"md5,omitempty" jsonschema:"file md5 for an md5-keyed result (Anna's Archive); pass to get_details or download"`
	// Extension and Size describe the file behind an md5-keyed result, so it can be
	// compared with a catalog result on the two attributes people sort by. Both are
	// as the provider states them — the size is a human string like "12.0MB", not a
	// byte count — and both are empty when it states neither.
	Extension string `json:"extension,omitempty" jsonschema:"file extension (e.g. pdf, epub), as the provider states it"`
	Size      string `json:"size,omitempty" jsonschema:"human-readable file size (e.g. 12.0MB), as the provider states it"`
	// PDFURL is a candidate full-text PDF link. For arxiv, eric and gutenberg it is
	// the provider's own hosted file and is reliably fetchable; for crossref it is
	// the link the PUBLISHER advertises, which is unverified — many publishers 403
	// anonymous clients — so nothing may present it as proof the work is readable.
	PDFURL string `json:"pdf_url,omitempty" jsonschema:"candidate full-text PDF URL. For an arxiv, eric or gutenberg result it is the provider's own hosted file and is fetchable (and for eric it is the whole way to get the file, since ERIC grey literature has no DOI to pass to download). For a crossref result it is the link the publisher advertises and is UNVERIFIED: major publishers serve it only to subscribers or refuse automated clients outright, so do not present it as proof the work is readable — pass the doi to read/download instead and let the source chain try it"`
	// ArchiveURL is a free-to-read archive.org "details" page for a publicly
	// readable book (surfaced by OpenLibrary when ebook_access is "public"). Empty
	// for every other result, so it doubles as the "this book is freely readable"
	// signal in the locator column.
	ArchiveURL string `json:"archive_url,omitempty" jsonschema:"free-to-read archive.org details page for a publicly readable book"`
	// FullTextURL is a directly-fetchable book FILE (EPUB, plain text or PDF) for a
	// provider whose catalog is not keyed by any identifier the download tool
	// accepts — Project Gutenberg, whose ebooks have no DOI, ISBN or md5. It is the
	// whole value of such a hit: without it the record could only be described, not
	// obtained. Distinct from PDFURL, which is specifically an article PDF.
	FullTextURL string `json:"full_text_url,omitempty" jsonschema:"a directly-fetchable open-access book file (epub, txt or pdf), for a record with no doi/isbn/md5 to download by; fetch it with your own HTTP tool"`
	// OpenAccess states the record's LICENSING status as the provider reports it (a
	// Creative Commons license for crossref, a hosted free copy for the rest). It is
	// not a claim that the file can be fetched right now: an openly licensed article
	// can still sit behind a publisher that refuses automated clients.
	OpenAccess bool `json:"open_access" jsonschema:"true when the record is open access — a licensing fact (e.g. a Creative Commons license), not a guarantee the file can be fetched: an openly licensed article can still sit behind a publisher that blocks automated clients, so pass the doi to read/download to find out"`
}

// Provider is a keyless open-access discovery source.
type Provider interface {
	// Name reports the origin label this provider stamps on its results.
	Name() string
	// Search returns up to limit open-access results for the query, best-effort:
	// only a context error is returned; other failures degrade to an empty slice.
	Search(ctx context.Context, query string, limit int) ([]DiscoveryResult, error)
}

// newDiscoveryClient builds the shared *http.Client used by discovery providers,
// with a sane overall timeout so a stalled connection can never outlive the
// per-provider context budget.
func newDiscoveryClient() *http.Client {
	return &http.Client{Timeout: discoveryTimeout + time.Second}
}

// boundedGet issues a GET after setting the discovery User-Agent, and returns the
// response body bytes bounded to discoveryMaxBody together with the status code.
// It NEVER swallows a context error: a canceled or expired ctx propagates so the
// federation layer can distinguish "caller went away" from "source degraded". Any
// other transport failure surfaces as an error the caller may choose to soften to
// an empty result.
func boundedGet(ctx context.Context, client *http.Client, rawURL string) (status int, body []byte, err error) {
	return boundedGetUA(ctx, client, rawURL, discoveryUserAgent())
}

// boundedGetUA is boundedGet with an explicit User-Agent, letting a provider that
// must identify itself differently — e.g. OpenLibrary, whose etiquette grants a
// higher rate to a request carrying a contact email in its agent string — override
// the default discovery agent. Its context and body-bounding semantics are
// identical to boundedGet.
func boundedGetUA(ctx context.Context, client *http.Client, rawURL, userAgent string) (status int, body []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err = io.ReadAll(io.LimitReader(resp.Body, discoveryMaxBody))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// ProviderBases holds one base URL per network-backed discovery provider. It is the
// argument to SetBasesForTest: a struct rather than a positional list so a test names
// the provider it is standing in for, and so adding a provider does not silently
// shift another one's URL. A zero field points that provider at an unroutable empty
// base, which is how a test keeps a provider it does not care about quiet.
type ProviderBases struct {
	// Arxiv is the arXiv API root (arxivBase).
	Arxiv string
	// Crossref is the Crossref REST API root (crossrefBase).
	Crossref string
	// OpenLibrary is the OpenLibrary API root (openLibraryBase).
	OpenLibrary string
	// DBLP is the dblp bibliography root (dblpBase).
	DBLP string
	// PubMed is the NCBI E-utilities root (pubmedBase).
	PubMed string
	// Gutendex is the Gutendex API root serving the Project Gutenberg catalog
	// (gutendexBase).
	Gutendex string
	// ERIC is the Institute of Education Sciences API root (ericBase).
	ERIC string
}

// SetBasesForTest overrides every provider base URL and returns a restore func that
// reinstates the originals. It is a test-only seam used by callers in other packages
// to point discovery at httptest servers; production code never calls it.
//
// Every provider is overridden, including the ones a given test does not exercise:
// leaving one at its live default would have that test reach the real service.
func SetBasesForTest(bases ProviderBases) (restore func()) {
	old := ProviderBases{
		Arxiv:       arxivBase,
		Crossref:    crossrefBase,
		OpenLibrary: openLibraryBase,
		DBLP:        dblpBase,
		PubMed:      pubmedBase,
		Gutendex:    gutendexBase,
		ERIC:        ericBase,
	}
	arxivBase, crossrefBase, openLibraryBase = bases.Arxiv, bases.Crossref, bases.OpenLibrary
	dblpBase, pubmedBase = bases.DBLP, bases.PubMed
	gutendexBase, ericBase = bases.Gutendex, bases.ERIC
	return func() {
		arxivBase, crossrefBase, openLibraryBase = old.Arxiv, old.Crossref, old.OpenLibrary
		dblpBase, pubmedBase = old.DBLP, old.PubMed
		gutendexBase, ericBase = old.Gutendex, old.ERIC
	}
}

// firstNonEmpty returns the first trimmed non-empty string in the slice, or "".
func firstNonEmpty(values []string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
