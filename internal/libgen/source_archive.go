package libgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// archiveBase is the default Internet Archive root serving item metadata and file
// downloads. It is overridden per source instance (field iaBase) so tests can point
// at an httptest server.
const archiveBase = "https://archive.org"

// archiveMaxBody bounds how many bytes of an OpenLibrary or archive.org JSON
// response are read. An item's metadata lists every derivative of every scanned
// page, so a book can legitimately answer with a few hundred kilobytes.
const archiveMaxBody = 4 << 20 // 4 MiB

// archiveMaxCandidates bounds how many Internet Archive identifiers are inspected
// for one book. OpenLibrary can list hundreds of scans of a popular work, each
// costing a metadata request, and the leading entries are the ones it considers
// representative — so the head of the list is where a freely downloadable scan is
// found if there is one.
const archiveMaxCandidates = 4

// archiveTextsMediaType is the archive.org mediatype for a book. A candidate filed
// as anything else (a film, an audio recording) is not what the caller asked for.
const archiveTextsMediaType = "texts"

// archivePublicAccess is the OpenLibrary ebook_access tier meaning "freely readable
// in full", as opposed to "borrowable" (controlled digital lending), "printdisabled"
// and "no_ebook". Only this tier may be downloaded. It mirrors the constant the
// discovery package applies to the same field; the two packages do not share code,
// so a change to OpenLibrary's vocabulary belongs in both.
const archivePublicAccess = "public"

// archiveLendingCollections are the archive.org collections that mark an item as
// part of the controlled-digital-lending or print-disabled programs rather than
// freely downloadable. Membership is treated as restriction on its own, because it
// is set on some lending items whose access-restricted-item flag is absent.
var archiveLendingCollections = []string{"inlibrary", "printdisabled"}

// archiveSource resolves a public-domain book to a downloadable scan on the
// Internet Archive (https://archive.org), reached through OpenLibrary.
//
// It is keyless and keyed by ISBN, because that is what OpenLibrary — the catalog
// that maps a book to its Internet Archive scans — is queried by. The first hop
// asks OpenLibrary for the book's ebook_access tier and its ia identifiers; the
// second confirms each candidate scan directly with archive.org before anything is
// served.
//
// Both gates are load-bearing and neither is redundant. A large share of the
// Archive's book items are controlled-digital-lending copies: they advertise .pdf
// and .epub files exactly like a public-domain scan does, but downloading one
// either fails or yields a DRM-encrypted, unusable file — an outcome that LOOKS
// like success. So a book must be "public" to OpenLibrary AND its individual scan
// must carry no access-restricted-item flag and belong to no lending collection.
// A work can be publicly readable while a particular scan of it is restricted,
// which is why the per-item check cannot be skipped once the work-level tier is
// known.
//
// MD5 verification is disabled: an ISBN-keyed item carries no LibGen digest.
type archiveSource struct {
	// http is the client used for the OpenLibrary, metadata and probe requests;
	// when nil, http.DefaultClient is used.
	http *http.Client
	// olBase overrides the OpenLibrary API root (defaults to the package's
	// openLibraryBase); tests set it to a local httptest server.
	olBase string
	// iaBase overrides the Internet Archive root (defaults to archiveBase); tests
	// set it to a local httptest server.
	iaBase string
}

// Compile-time assertion that archiveSource satisfies the DownloadSource contract.
var _ DownloadSource = archiveSource{}

// archiveOpenLibraryResponse is the subset of an OpenLibrary search response the
// source reads.
type archiveOpenLibraryResponse struct {
	Docs []archiveOpenLibraryDoc `json:"docs"`
}

// archiveOpenLibraryDoc is one OpenLibrary search hit: the availability tier, the
// full-text flag, and the Internet Archive identifiers holding scans of the book.
type archiveOpenLibraryDoc struct {
	Title       string   `json:"title"`
	EbookAccess string   `json:"ebook_access"`
	HasFulltext bool     `json:"has_fulltext"`
	IA          []string `json:"ia"`
}

// archiveMetadataResponse is the subset of an archive.org /metadata/<id> response
// the source reads. An identifier the Archive does not hold answers with an empty
// object, which decodes to a zero value here.
type archiveMetadataResponse struct {
	Files    []archiveFile       `json:"files"`
	Metadata archiveItemMetadata `json:"metadata"`
}

// archiveItemMetadata is the item's own metadata block, carrying the two signals
// the lending gate reads plus the identifier that proves the item exists.
type archiveItemMetadata struct {
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	MediaType  string `json:"mediatype"`
	// AccessRestricted is archive.org's access-restricted-item flag, kept raw
	// because the Archive writes it as the string "true" on most items and as a
	// JSON boolean on others; any value other than an explicit false/null means the
	// item is lending-restricted.
	AccessRestricted json.RawMessage `json:"access-restricted-item"`
	// Collection is the item's collection membership, which the Archive writes as a
	// bare string when there is only one.
	Collection flexStrings `json:"collection"`
}

// archiveFile is one file attached to an Internet Archive item.
type archiveFile struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Size   string `json:"size"`
}

// flexStrings decodes a JSON value that archive.org writes either as a string or as
// an array of strings (its collection field is a bare string when an item belongs
// to exactly one collection). A strict []string would fail to decode the whole
// item and lose a downloadable book over a formatting detail.
type flexStrings []string

// UnmarshalJSON accepts a JSON string, an array of strings, or null.
func (f *flexStrings) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*f = nil
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err == nil {
		*f = many
		return nil
	}
	var one string
	if err := json.Unmarshal(data, &one); err != nil {
		return fmt.Errorf("archive: collection is neither a string nor a list: %w", err)
	}
	*f = []string{one}
	return nil
}

// Name identifies the Internet Archive source.
func (s archiveSource) Name() string { return "archive" }

// Supports reports that the source can resolve an item carrying a well-formed ISBN,
// and nothing else: the route in is an OpenLibrary ISBN lookup, so an md5- or
// DOI-keyed item has no way to address a scan.
func (s archiveSource) Supports(it Item) bool { return NormalizeISBN(it.ISBN) != "" }

// Resolve maps the item's ISBN to a publicly downloadable Internet Archive scan.
//
// A book OpenLibrary does not know, one it holds only for borrowing, and one whose
// every candidate scan is lending-restricted are all clean misses (ErrNotIndexed).
// A failed lookup on either hop is unavailability, and is never reported as a miss:
// "the Archive has no free copy" is a claim a broken request cannot support.
func (s archiveSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	isbn := NormalizeISBN(it.ISBN)
	if isbn == "" {
		return Resolved{}, errors.New("archive: item carries no usable ISBN")
	}
	doc, err := s.lookup(ctx, isbn)
	if err != nil {
		return Resolved{}, err
	}
	if gateErr := archivePubliclyReadable(doc, isbn); gateErr != nil {
		return Resolved{}, gateErr
	}
	return s.resolveCandidates(ctx, doc.IA, isbn)
}

// archivePubliclyReadable applies the work-level gate: OpenLibrary must report the
// book as freely readable in full ("public" with a full text held) and must name at
// least one Internet Archive scan. Anything else is a clean miss naming the tier
// that disqualified it, so the chain log explains why a book that visibly exists on
// archive.org was not served.
func archivePubliclyReadable(doc archiveOpenLibraryDoc, isbn string) error {
	if doc.EbookAccess != archivePublicAccess || !doc.HasFulltext {
		tier := doc.EbookAccess
		if tier == "" {
			tier = "no ebook"
		}
		return notIndexed(fmt.Errorf("archive: OpenLibrary reports %s as %q, not publicly downloadable", isbn, tier))
	}
	if len(doc.IA) == 0 {
		return notIndexed(fmt.Errorf("archive: OpenLibrary lists no Internet Archive scan for %s", isbn))
	}
	return nil
}

// resolveCandidates walks the head of the candidate list and returns the first scan
// that passes the per-item lending gate and offers a book file.
//
// A candidate that merely does not qualify is skipped silently; a candidate whose
// lookup BROKE is remembered, and that failure is surfaced when nothing resolved,
// so a metadata outage never masquerades as "no free copy exists".
func (s archiveSource) resolveCandidates(ctx context.Context, ids []string, isbn string) (Resolved, error) {
	var failure error
	for _, id := range ids[:min(len(ids), archiveMaxCandidates)] {
		res, err := s.resolveCandidate(ctx, id)
		if err == nil {
			return res, nil
		}
		if failure == nil && !errors.Is(err, ErrNotIndexed) {
			failure = err
		}
	}
	if failure != nil {
		return Resolved{}, failure
	}
	return Resolved{}, notIndexed(fmt.Errorf(
		"archive: no freely downloadable scan for %s (every candidate is lending-restricted or holds no book file)", isbn))
}

// resolveCandidate confirms one Internet Archive item and turns it into a Resolved.
// It fails with a miss when the item does not exist, is not a book, is
// lending-restricted, or offers no usable file.
func (s archiveSource) resolveCandidate(ctx context.Context, id string) (Resolved, error) {
	meta, err := s.metadata(ctx, id)
	if err != nil {
		return Resolved{}, err
	}
	if meta.Metadata.Identifier == "" {
		return Resolved{}, notIndexed(fmt.Errorf("archive: no item %q", id))
	}
	if !strings.EqualFold(meta.Metadata.MediaType, archiveTextsMediaType) {
		return Resolved{}, notIndexed(fmt.Errorf("archive: item %q is not a book", id))
	}
	if archiveIsRestricted(meta.Metadata) {
		return Resolved{}, notIndexed(fmt.Errorf("archive: item %q is lending-restricted", id))
	}
	file, ok := archivePickFile(meta.Files)
	if !ok {
		return Resolved{}, notIndexed(fmt.Errorf("archive: item %q offers no PDF or EPUB", id))
	}
	fileURL := s.downloadURL(id, file.Name)
	ext := archiveExt(file.Name)
	// A PDF is confirmed with the shared probe: it is the one cheap way to catch an
	// item that passed both gates and still answers with a borrow interstitial. An
	// EPUB cannot be probed the same way (no magic-number check applies), so it
	// rests on the two metadata gates.
	if ext == "pdf" && !probePDF(ctx, s.client(), fileURL) {
		return Resolved{}, unavailable(fmt.Errorf("archive: item %q does not serve %s", id, file.Name))
	}
	return Resolved{FileURL: fileURL, VerifyMD5: false, Ext: ext}, nil
}

// archiveIsRestricted reports whether an item is part of the Archive's lending or
// print-disabled programs rather than freely downloadable. It is deliberately
// conservative: any access-restricted-item value other than an explicit false, or
// membership in a lending collection, counts as restricted. Skipping a downloadable
// book costs the caller the next source in the chain; downloading a restricted one
// costs them a file that looks fine and is not.
func archiveIsRestricted(meta archiveItemMetadata) bool {
	if archiveFlagSet(meta.AccessRestricted) {
		return true
	}
	for _, c := range meta.Collection {
		for _, restricted := range archiveLendingCollections {
			if strings.EqualFold(strings.TrimSpace(c), restricted) {
				return true
			}
		}
	}
	return false
}

// archiveFlagSet reports whether a raw archive.org metadata flag is set, accepting
// both spellings the Archive uses (the string "true" and the JSON boolean true) and
// treating an absent, null or false value as unset.
func archiveFlagSet(raw json.RawMessage) bool {
	value := strings.ToLower(strings.Trim(strings.TrimSpace(string(raw)), `"`))
	switch value {
	case "", "null", "false", "0":
		return false
	default:
		return true
	}
}

// archiveFilePreference ranks the file types worth downloading, best first: the
// full-resolution text PDF, then the grayscale derivative, then the EPUB. A file
// matching none of these (page scans, DjVu text, cover images) is never chosen.
var archiveFilePreference = []func(name string) bool{
	func(name string) bool { return strings.HasSuffix(name, ".pdf") && !strings.HasSuffix(name, "_bw.pdf") },
	func(name string) bool { return strings.HasSuffix(name, ".pdf") },
	func(name string) bool { return strings.HasSuffix(name, ".epub") },
}

// archiveDRMMarkers are the filename markers archive.org gives its DRM-wrapped
// derivatives (Adobe-encrypted PDF, LCP-encrypted EPUB). Such a file downloads
// successfully and cannot be opened, so it is never a candidate.
var archiveDRMMarkers = []string{"_encrypted.", "_lcp."}

// archivePickFile chooses the best downloadable book file from an item's file list,
// preferring the full-resolution PDF and skipping DRM-wrapped derivatives. The bool
// reports whether anything usable was found.
func archivePickFile(files []archiveFile) (archiveFile, bool) {
	for _, wanted := range archiveFilePreference {
		for _, f := range files {
			name := strings.ToLower(f.Name)
			if wanted(name) && !archiveIsDRM(name) {
				return f, true
			}
		}
	}
	return archiveFile{}, false
}

// archiveIsDRM reports whether a lowercased filename names a DRM-wrapped
// derivative.
func archiveIsDRM(name string) bool {
	for _, marker := range archiveDRMMarkers {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

// archiveExt returns the download's fallback extension ("pdf" or "epub") derived
// from the chosen filename.
func archiveExt(name string) string {
	if strings.HasSuffix(strings.ToLower(name), ".epub") {
		return "epub"
	}
	return "pdf"
}

// lookup asks OpenLibrary which Internet Archive scans hold the ISBN, and how
// freely they may be read. The query is fielded (isbn:<value>) so it matches the
// identifier index rather than free text, and the projection is trimmed to the
// three fields the gate and the candidate list need.
func (s archiveSource) lookup(ctx context.Context, isbn string) (archiveOpenLibraryDoc, error) {
	q := url.Values{
		"q":      {"isbn:" + isbn},
		"fields": {"title,ebook_access,has_fulltext,ia"},
		"limit":  {"1"},
	}
	endpoint := s.openLibraryRoot() + "/search.json?" + q.Encode()

	var env archiveOpenLibraryResponse
	if err := s.getJSON(ctx, endpoint, isbn, &env); err != nil {
		return archiveOpenLibraryDoc{}, err
	}
	if len(env.Docs) == 0 {
		return archiveOpenLibraryDoc{}, notIndexed(fmt.Errorf("archive: OpenLibrary knows no book with ISBN %s", isbn))
	}
	return env.Docs[0], nil
}

// metadata fetches one Internet Archive item's metadata record.
func (s archiveSource) metadata(ctx context.Context, id string) (archiveMetadataResponse, error) {
	endpoint := s.archiveRoot() + "/metadata/" + url.PathEscape(id)
	var meta archiveMetadataResponse
	if err := s.getJSON(ctx, endpoint, id, &meta); err != nil {
		return archiveMetadataResponse{}, err
	}
	return meta, nil
}

// downloadURL builds the Archive's stable download URL for a file, which redirects
// to whichever CDN node currently holds the item.
func (s archiveSource) downloadURL(id, name string) string {
	return s.archiveRoot() + "/download/" + url.PathEscape(id) + "/" + url.PathEscape(name)
}

// getJSON fetches and decodes one OpenLibrary or archive.org response, tagging
// every failure with this source's name. subject names what was being looked up,
// for the message.
func (s archiveSource) getJSON(ctx context.Context, endpoint, subject string, out any) error {
	return jsonFetch{client: s.client(), source: "archive", subject: subject, maxBody: archiveMaxBody}.
		get(ctx, endpoint, out)
}

// openLibraryRoot returns the OpenLibrary API root without a trailing separator,
// defaulting to the same package-level base the enrichment path uses.
func (s archiveSource) openLibraryRoot() string {
	if s.olBase == "" {
		return openLibraryBase
	}
	return strings.TrimRight(s.olBase, "/")
}

// archiveRoot returns the Internet Archive root without a trailing separator,
// defaulting to the production host.
func (s archiveSource) archiveRoot() string {
	if s.iaBase == "" {
		return archiveBase
	}
	return strings.TrimRight(s.iaBase, "/")
}

// client returns the configured HTTP client, or the shared default when none was
// set.
func (s archiveSource) client() *http.Client { return httpClientOr(s.http) }
