package libgen

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// oapenBase is the default root of OAPEN's DSpace REST API. It is overridden per
// source instance (field base) so tests can point at an httptest server.
const oapenBase = "https://library.oapen.org"

// oapenMaxBody bounds how many bytes of an OAPEN REST response are read. An item
// expanded with its full Dublin Core metadata runs to a few tens of kilobytes, so
// this leaves ample headroom while still capping an unexpectedly large body.
const oapenMaxBody = 1 << 21 // 2 MiB

// oapenMaxCandidates bounds how many search hits are inspected before giving up.
// /rest/search is a free-text search with no exact-identifier mode, so a hit list
// is a ranked guess: the wanted record is first when OAPEN holds it, and a long
// tail of unrelated monographs follows when it does not. Each candidate costs an
// item fetch, so only the head of the list is worth confirming.
const oapenMaxCandidates = 3

// oapenPDFBundle is the DSpace bundle holding an item's published file, as opposed
// to EXPORT (MARC/ONIX/RIS records), TEXT (extracted plain text) and THUMBNAIL
// (cover images). A PDF in this bundle is the monograph itself.
const oapenPDFBundle = "ORIGINAL"

// oapenSource resolves an open-access monograph from OAPEN
// (https://library.oapen.org), the Directory of Open Access Books' hosting
// platform, where scholarly publishers deposit the full text of openly licensed
// books.
//
// It is keyless and keyed by DOI or ISBN: the identifier is submitted to the
// DSpace REST search, the candidate item is fetched with its bitstreams and
// metadata, and the item's own oapen.identifier.doi / oapen.identifier.isbn are
// checked against the requested identifier before anything is served. That check
// is not a formality — /rest/search is free text, so an identifier OAPEN does not
// hold still returns a page of unrelated monographs, and serving the top hit would
// hand the caller a different book while reporting success.
//
// Everything OAPEN hosts is openly licensed, so this is a legal open-access source
// and sits ahead of the shadow libraries in the chain. MD5 verification is
// disabled: an ISBN- or DOI-keyed item carries no LibGen digest.
type oapenSource struct {
	// http is the client used for the search, item and probe requests; when nil,
	// http.DefaultClient is used.
	http *http.Client
	// base overrides the REST API root (defaults to oapenBase); tests set it to a
	// local httptest server.
	base string
}

// Compile-time assertion that oapenSource satisfies the DownloadSource contract.
var _ DownloadSource = oapenSource{}

// oapenHit is the subset of one /rest/search result consulted here: the item's
// uuid, which addresses the follow-up item lookup, and its name for error text.
type oapenHit struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// oapenItem is the subset of an expanded /rest/items/<uuid> response consulted
// here: the files attached to the record and the metadata that states its
// identifiers.
type oapenItem struct {
	UUID       string           `json:"uuid"`
	Name       string           `json:"name"`
	Bitstreams []oapenBitstream `json:"bitstreams"`
	Metadata   []oapenMetadata  `json:"metadata"`
}

// oapenBitstream is one file attached to an OAPEN item — the monograph PDF, but
// also its MARC/ONIX exports, extracted text and cover thumbnail.
type oapenBitstream struct {
	UUID         string `json:"uuid"`
	Name         string `json:"name"`
	MimeType     string `json:"mimeType"`
	BundleName   string `json:"bundleName"`
	RetrieveLink string `json:"retrieveLink"`
	SizeBytes    int64  `json:"sizeBytes"`
}

// oapenMetadata is one Dublin Core (or OAPEN-schema) metadata field of an item.
type oapenMetadata struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Name identifies the OAPEN source.
func (s oapenSource) Name() string { return "oapen" }

// Supports reports that OAPEN can resolve an item keyed by ISBN or by DOI. It
// claims no md5-keyed item: OAPEN indexes books by publisher identifier and knows
// nothing of LibGen digests.
func (s oapenSource) Supports(it Item) bool {
	return NormalizeISBN(it.ISBN) != "" || strings.TrimSpace(it.DOI) != ""
}

// Resolve searches OAPEN for the item's identifier, confirms a candidate record
// actually states that identifier, and returns the retrieve URL of its PDF.
//
// A search that returns nothing, a hit list in which no record states the
// identifier, and a confirmed record that carries no PDF are all clean misses
// (ErrNotIndexed): OAPEN answered correctly and simply does not hold a
// downloadable copy. Transport failures, transient statuses and a bitstream that
// does not serve a PDF are the source being unavailable.
func (s oapenSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	query, matches, err := oapenIdentity(it)
	if err != nil {
		return Resolved{}, err
	}
	hits, err := s.search(ctx, query)
	if err != nil {
		return Resolved{}, err
	}
	if len(hits) == 0 {
		return Resolved{}, notIndexed(fmt.Errorf("oapen: no catalog entry for %q", query))
	}
	return s.resolveFirstMatch(ctx, hits, query, matches)
}

// oapenIdentity derives the search query and the record matcher from the item's
// identifiers. The query is the DOI when present (the more specific statement of
// "this exact record") and the normalized ISBN otherwise, while the matcher accepts
// a record stating EITHER identifier the item carries, so a DOI-keyed search whose
// record happens to state only the ISBN still matches.
func oapenIdentity(it Item) (query string, matches func(oapenItem) bool, err error) {
	doi := strings.TrimSpace(it.DOI)
	isbn := NormalizeISBN(it.ISBN)
	switch {
	case doi != "":
		query = doi
	case isbn != "":
		query = isbn
	default:
		return "", nil, errors.New("oapen: item carries neither a DOI nor an ISBN")
	}
	return query, func(item oapenItem) bool { return oapenItemStates(item, doi, isbn) }, nil
}

// resolveFirstMatch walks the head of the hit list, fetching each candidate item
// and returning the first one whose metadata confirms the requested identifier.
//
// A candidate that could not be fetched is remembered but not fatal — the next hit
// may still be the right record — and that retained failure is surfaced when no
// candidate matched, so a broken lookup is never laundered into "OAPEN does not
// hold this book".
func (s oapenSource) resolveFirstMatch(ctx context.Context, hits []oapenHit, query string, matches func(oapenItem) bool) (Resolved, error) {
	var failure error
	for _, h := range hits[:min(len(hits), oapenMaxCandidates)] {
		item, err := s.item(ctx, h.UUID)
		if err != nil {
			if failure == nil {
				failure = err
			}
			continue
		}
		if !matches(item) {
			continue
		}
		return s.resolveItem(ctx, item)
	}
	if failure != nil {
		return Resolved{}, failure
	}
	return Resolved{}, notIndexed(fmt.Errorf("oapen: no catalog entry states %q", query))
}

// resolveItem turns a confirmed item into a Resolved by picking its PDF bitstream
// and probing that the retrieve endpoint really serves a PDF.
func (s oapenSource) resolveItem(ctx context.Context, item oapenItem) (Resolved, error) {
	bs, ok := oapenPickPDF(item.Bitstreams)
	if !ok {
		return Resolved{}, notIndexed(fmt.Errorf("oapen: %q has no PDF bitstream", item.Name))
	}
	fileURL := s.bitstreamURL(bs)
	if fileURL == "" {
		return Resolved{}, notIndexed(fmt.Errorf("oapen: %q has a PDF bitstream with no retrieve link", item.Name))
	}
	if !probePDF(ctx, s.client(), fileURL) {
		// The catalog says this bitstream is the book's PDF, so an endpoint that
		// answers with anything else is the platform misbehaving, not a statement
		// about the book.
		return Resolved{}, unavailable(fmt.Errorf("oapen: bitstream %s does not serve a PDF", bs.UUID))
	}
	return Resolved{FileURL: fileURL, VerifyMD5: false, Ext: "pdf"}, nil
}

// search submits the identifier to /rest/search and returns the hit list.
func (s oapenSource) search(ctx context.Context, query string) ([]oapenHit, error) {
	endpoint := s.root() + "/rest/search?" + url.Values{"query": {query}}.Encode()
	var hits []oapenHit
	if err := s.getJSON(ctx, endpoint, query, &hits); err != nil {
		return nil, err
	}
	return hits, nil
}

// item fetches one record expanded with the two facets the source reads: its
// bitstreams (to find the PDF) and its metadata (to confirm the identifier).
func (s oapenSource) item(ctx context.Context, uuid string) (oapenItem, error) {
	endpoint := s.root() + "/rest/items/" + url.PathEscape(uuid) + "?expand=bitstreams,metadata"
	var item oapenItem
	if err := s.getJSON(ctx, endpoint, uuid, &item); err != nil {
		return oapenItem{}, err
	}
	return item, nil
}

// getJSON fetches and decodes one REST response, tagging every failure with this
// source's name. subject names what was being looked up, for the message.
func (s oapenSource) getJSON(ctx context.Context, endpoint, subject string, out any) error {
	return jsonFetch{client: s.client(), source: "oapen", subject: subject, maxBody: oapenMaxBody}.
		get(ctx, endpoint, out)
}

// oapenItemStates reports whether the record's own metadata states the requested
// DOI or the requested ISBN. Either identifier confirming is enough; an item that
// states neither is a different book that merely ranked well in a free-text search.
func oapenItemStates(item oapenItem, doi, isbn string) bool {
	for _, m := range item.Metadata {
		key := strings.ToLower(m.Key)
		switch {
		case doi != "" && strings.HasSuffix(key, ".doi") && sameDOI(m.Value, doi):
			return true
		case isbn != "" && strings.HasSuffix(key, ".isbn") && oapenValueHasISBN(m.Value, isbn):
			return true
		}
	}
	return false
}

// oapenValueHasISBN reports whether an isbn metadata value states the wanted ISBN.
// A record may list several ISBNs (print and electronic) in one field, so the value
// is split on the usual separators and each part compared in its thirteen-digit
// form.
func oapenValueHasISBN(value, want string) bool {
	for _, part := range strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	}) {
		if isbnMatches(part, want) {
			return true
		}
	}
	return false
}

// sameDOI compares a DOI as a record states it with the requested one. Catalogs
// write the same DOI as a bare identifier, prefixed with "doi:", or as a resolver
// URL (OAPEN uses https://doi.org/… for some records and the bare form for
// others), so both sides are reduced to the bare, lowercased identifier first.
func sameDOI(a, b string) bool {
	x, y := bareDOI(a), bareDOI(b)
	return x != "" && x == y
}

// doiPrefixes are the resolver and scheme prefixes stripped from a DOI to leave the
// bare identifier, longest first so a match consumes the whole prefix.
var doiPrefixes = []string{
	"https://doi.org/", "http://doi.org/",
	"https://dx.doi.org/", "http://dx.doi.org/",
	"doi.org/", "doi:",
}

// bareDOI reduces a DOI to its lowercased identifier, stripping any resolver URL or
// "doi:" scheme prefix and surrounding whitespace.
func bareDOI(doi string) string {
	s := strings.ToLower(strings.TrimSpace(doi))
	for _, p := range doiPrefixes {
		if rest, found := strings.CutPrefix(s, p); found {
			return rest
		}
	}
	return s
}

// oapenPickPDF chooses the bitstream holding the monograph itself: a PDF in the
// ORIGINAL bundle wins, a PDF in any other bundle is the fallback, and everything
// else (MARC/ONIX exports, extracted text, cover thumbnails) is ignored. The bool
// reports whether any PDF was found.
func oapenPickPDF(bitstreams []oapenBitstream) (oapenBitstream, bool) {
	var fallback oapenBitstream
	found := false
	for _, b := range bitstreams {
		if !oapenIsPDF(b) {
			continue
		}
		if strings.EqualFold(b.BundleName, oapenPDFBundle) {
			return b, true
		}
		if !found {
			fallback, found = b, true
		}
	}
	return fallback, found
}

// oapenIsPDF reports whether a bitstream is a PDF, by its declared mimetype or —
// for a server that files an upload as application/octet-stream — by its filename.
func oapenIsPDF(b oapenBitstream) bool {
	return strings.Contains(strings.ToLower(b.MimeType), "pdf") ||
		strings.HasSuffix(strings.ToLower(b.Name), ".pdf")
}

// bitstreamURL builds the absolute retrieve URL for a bitstream: the API's own
// site-absolute retrieveLink joined onto the configured base, falling back to the
// canonical /rest/bitstreams/<uuid>/retrieve path when the response omits it. It
// returns "" when the bitstream carries neither.
func (s oapenSource) bitstreamURL(b oapenBitstream) string {
	if link := strings.TrimSpace(b.RetrieveLink); link != "" {
		return s.root() + "/" + strings.TrimPrefix(link, "/")
	}
	if b.UUID == "" {
		return ""
	}
	return s.root() + "/rest/bitstreams/" + url.PathEscape(b.UUID) + "/retrieve"
}

// root returns the REST API base without a trailing separator, defaulting to the
// production host when none was configured.
func (s oapenSource) root() string {
	if s.base == "" {
		return oapenBase
	}
	return strings.TrimRight(s.base, "/")
}

// client returns the configured HTTP client, or the shared default when none was
// set.
func (s oapenSource) client() *http.Client { return httpClientOr(s.http) }
