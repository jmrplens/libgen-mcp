package libgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
)

// zenodoBase is the default Zenodo root. The source appends zenodoFilesPath and
// zenodoRecordPath to it. It is overridable per source so tests can point at an
// httptest server.
const zenodoBase = "https://zenodo.org"

// zenodoFilesPath and zenodoRecordPath are the two paths this source uses, and the
// pair is chosen to stay inside what Zenodo's robots.txt permits.
//
// That file disallows /api wholesale and then carves one exception back out:
//
//	Disallow: /api
//	Allow: /api/records/*/files
//	Disallow: /api/records/*/files-archive
//
// So the file listing is explicitly permitted while the record-metadata endpoint
// (/api/records/<id>) and the bulk files-archive endpoint are not, and neither of
// those is touched here. The human-facing record page (/records/<id>) carries no
// Disallow at all — only /records/*/preview does — so the version hop below is
// permitted too. robots.txt also sets Crawl-delay: 10; a resolve makes at most two
// requests for one caller-initiated download rather than crawling, and the
// client's own rate limiting governs the rest.
const (
	zenodoFilesPath  = "/api/records/"
	zenodoRecordPath = "/records/"
)

// zenodoDOIPrefix is the DOI prefix Zenodo mints records under: the CERN
// registrant prefix 10.5281 plus the fixed "zenodo." token that opens every
// suffix. The source claims only DOIs under it, which is narrower than the bare
// registrant prefix and deliberately so — 10.5281 is CERN's and not every DOI in
// it is a Zenodo record.
const zenodoDOIPrefix = "10.5281/zenodo."

// zenodoMaxBody bounds how many bytes of a file listing are read. A record with
// hundreds of files still lists well under this.
const zenodoMaxBody = 4 << 20 // 4 MiB

// zenodoReadableExts are the file extensions the read tool can extract text from,
// in the order this source prefers them when a record holds several files.
var zenodoReadableExts = []string{"pdf", "epub", "txt"}

// errZenodoNoRecord marks the one outcome the version hop acts on: Zenodo answered
// normally and holds no record under the id asked for. It is distinguished from
// transport, status and decode failures so the hop is never attempted on the
// strength of a request that merely broke.
var errZenodoNoRecord = errors.New("no such record")

// zenodoSource resolves a Zenodo DOI to a file in the deposited record.
//
// Zenodo is CERN's open repository: preprints, published papers, datasets,
// software releases, theses and conference material, everything served without an
// account. Nothing else in the chain reaches it — Europe PMC returns zero hits for
// a Zenodo DOI, fatcat answers 404 to the lookup, and Sci-Hub serves a stub with no
// PDF — so this is new corpus rather than another route to a file already reachable.
//
// MD5 verification is disabled, as for every DOI-keyed item.
type zenodoSource struct {
	// http is the client used for the listing and version-hop requests; when nil,
	// http.DefaultClient is used.
	http *http.Client
	// baseURL overrides the Zenodo root (defaults to zenodoBase); tests set it to a
	// local httptest server.
	baseURL string
}

// Compile-time assertion that zenodoSource satisfies the DownloadSource contract.
var _ DownloadSource = zenodoSource{}

// zenodoFilesResponse is the subset of the files endpoint's response consulted
// here: the record's file entries, empty for a metadata-only deposit.
type zenodoFilesResponse struct {
	Entries []zenodoEntry `json:"entries"`
}

// zenodoEntry is the subset of one file entry consulted here.
type zenodoEntry struct {
	// Key is the file's name within the record, and the authority on its format —
	// see zenodoExt for why the mimetype is not.
	Key string `json:"key"`
	// Size is the file's length in bytes, used to pick the payload of a record that
	// holds no directly readable file.
	Size int64 `json:"size"`
	// Links carries the endpoints for this entry; only the content URL is used.
	Links zenodoLinks `json:"links"`
}

// zenodoLinks is the subset of an entry's links consulted here.
type zenodoLinks struct {
	// Content is the direct download URL for the file's bytes.
	Content string `json:"content"`
}

// Name identifies the Zenodo source.
func (s zenodoSource) Name() string { return "zenodo" }

// Supports reports that this source can resolve a DOI-keyed item whose DOI is a
// well-formed Zenodo record DOI, and only such items. A 10.5281 DOI whose suffix is
// not "zenodo.<digits>" is refused here rather than looked up under a record id
// that cannot exist.
func (s zenodoSource) Supports(it Item) bool {
	_, ok := zenodoRecordID(it.DOI)
	return ok
}

// Resolve lists the record's files and returns the best one to download.
//
// A DOI whose record id the files endpoint does not know is retried once against
// the record's latest version, because roughly half of all Zenodo DOIs name a
// concept rather than a version — see s.latestVersion. The outcomes are otherwise
// kept distinct so a failure stays diagnosable: an unknown record, a restricted
// record and a metadata-only record with no files are clean misses about one item,
// while a transport failure or a transient status is the source being unavailable.
func (s zenodoSource) Resolve(ctx context.Context, it Item) (Resolved, error) {
	id, ok := zenodoRecordID(it.DOI)
	if !ok {
		return Resolved{}, notIndexed(fmt.Errorf("zenodo: %q is not a Zenodo DOI", it.DOI))
	}
	entries, err := s.listFiles(ctx, id)
	if errors.Is(err, errZenodoNoRecord) {
		entries, err = s.listVersionFiles(ctx, id)
	}
	if err != nil {
		return Resolved{}, err
	}
	entry, ok := zenodoPickFile(entries)
	if !ok {
		return Resolved{}, notIndexed(fmt.Errorf("zenodo: record %s holds no downloadable file", id))
	}
	return Resolved{
		FileURL:   entry.Links.Content,
		VerifyMD5: false,
		Ext:       zenodoExt(entry.Key),
	}, nil
}

// listVersionFiles handles the concept-DOI case: it asks Zenodo which version the
// id resolves to and, when that is a different record, lists that record's files
// instead. A concept id that leads nowhere is reported as the clean miss the
// original listing already was.
func (s zenodoSource) listVersionFiles(ctx context.Context, id string) ([]zenodoEntry, error) {
	version, err := s.latestVersion(ctx, id)
	if err != nil {
		return nil, err
	}
	if version == id {
		return nil, notIndexed(fmt.Errorf("zenodo: record %s does not exist", id))
	}
	return s.listFiles(ctx, version)
}

// listFiles requests the record's file listing.
//
// The three answers Zenodo gives are kept apart, because they mean different
// things and all three were observed on real records. A 404 is reported as
// errZenodoNoRecord so Resolve can try the version hop — that is the shape a
// concept id comes back in. A 403 means Zenodo holds the record but its files are
// restricted, which is a final and correct answer about this item and so a clean
// miss rather than a fault. A transport failure or a transient status is the
// source being unavailable.
//
// The request is written out rather than delegated to jsonFetch because that
// helper reports a bad status without saying which one, and here the status is the
// whole signal.
func (s zenodoSource) listFiles(ctx context.Context, id string) ([]zenodoEntry, error) {
	endpoint := s.root() + zenodoFilesPath + id + "/files"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("zenodo: building request for record %s: %w", id, err)
	}
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set("Accept", "application/json")

	resp, err := httpClientOr(s.http).Do(req)
	if err != nil {
		return nil, unavailable(fmt.Errorf("zenodo: requesting record %s: %w", id, err))
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("zenodo: %w for %s", errZenodoNoRecord, id)
	case http.StatusForbidden:
		return nil, notIndexed(fmt.Errorf("zenodo: the files of record %s are restricted", id))
	default:
		return nil, unavailableStatus(resp.StatusCode,
			fmt.Errorf("zenodo: record %s returned HTTP %d", id, resp.StatusCode))
	}

	var out zenodoFilesResponse
	if decErr := json.NewDecoder(io.LimitReader(resp.Body, zenodoMaxBody)).Decode(&out); decErr != nil {
		return nil, fmt.Errorf("zenodo: decoding the file listing of record %s: %w", id, decErr)
	}
	return out.Entries, nil
}

// latestVersion resolves a record id to the id of the version Zenodo serves for it,
// returning the same id unchanged when the record is already a version (or does not
// exist).
//
// This is what makes concept DOIs work, and they are not a corner case: Zenodo
// mints two DOIs per deposit — one naming the specific version and one naming the
// deposit across all its versions — and it is the concept DOI that its own "Cite
// all versions" affordance hands out. A concept id has no file listing of its own
// and answers 404 to the files endpoint, so without this hop about half of all
// Zenodo DOIs would resolve to nothing. Nor is the version id derivable: it is
// usually the concept id plus one (21698240 → 21698241) but not always
// (19978417 → 21676215), so it must be asked for.
//
// The request is a HEAD against the human-facing record page, which answers 302 to
// the version for a concept id, 200 for a version id and 404 for neither — measured
// on all three. HEAD is used rather than GET because the page body is ~100 KB of
// markup this has no interest in, and unlike NIST's repository (see nistSource)
// Zenodo answers HEAD faithfully. The client's own redirect following supplies the
// answer, so no custom redirect policy is needed: the final request's path names the
// record that was reached.
func (s zenodoSource) latestVersion(ctx context.Context, id string) (string, error) {
	endpoint := s.root() + zenodoRecordPath + id
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("zenodo: building version request for %s: %w", id, err)
	}
	req.Header.Set("User-Agent", userAgent())

	resp, err := httpClientOr(s.http).Do(req)
	if err != nil {
		return "", unavailable(fmt.Errorf("zenodo: resolving the version of %s: %w", id, err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		// Neither a version nor a concept: Zenodo has never held this id. That is a
		// correct, final answer about the item, so it is a clean miss rather than a
		// fault — the files endpoint's own 404 could not say as much on its own,
		// because a concept id answers 404 there too.
		return "", notIndexed(fmt.Errorf("zenodo: record %s does not exist", id))
	}
	if resp.StatusCode != http.StatusOK {
		return "", unavailableStatus(resp.StatusCode,
			fmt.Errorf("zenodo: the version of %s returned HTTP %d", id, resp.StatusCode))
	}
	reached := path.Base(resp.Request.URL.Path)
	if !isDigits(reached) {
		return "", notIndexed(fmt.Errorf("zenodo: %s led to %q, not a record", id, reached))
	}
	return reached, nil
}

// root returns the Zenodo base URL this source is configured against, without a
// trailing slash.
func (s zenodoSource) root() string {
	if s.baseURL == "" {
		return zenodoBase
	}
	return strings.TrimRight(s.baseURL, "/")
}

// zenodoPickFile chooses which file of a record to download, reporting whether the
// record held a usable one.
//
// A Zenodo record is a deposit rather than a document, so it can hold one file or
// fourteen, and the caller asked for the record by DOI without naming a file. The
// preference is for something the read tool can then extract text from — a PDF,
// an EPUB or plain text, in that order, first match wins — and failing that for the
// largest file in the record, on the reasoning that a deposit's payload is its
// biggest file and the rest are READMEs, checksums and manifests. A record whose
// files all lack a usable content URL is treated as holding nothing.
func zenodoPickFile(entries []zenodoEntry) (zenodoEntry, bool) {
	usable := make([]zenodoEntry, 0, len(entries))
	for _, e := range entries {
		if absoluteHTTPURL(e.Links.Content) {
			usable = append(usable, e)
		}
	}
	if len(usable) == 0 {
		return zenodoEntry{}, false
	}
	for _, want := range zenodoReadableExts {
		for _, e := range usable {
			if zenodoExt(e.Key) == want {
				return e, true
			}
		}
	}
	largest := usable[0]
	for _, e := range usable[1:] {
		if e.Size > largest.Size {
			largest = e
		}
	}
	return largest, true
}

// zenodoExt returns a file's lowercased extension without its dot, or "" when the
// name carries none.
//
// The extension is taken from the file's name and never from the entry's mimetype,
// because the mimetype is not reliable in either direction. In the listing, Zenodo
// reports .md, .xlsx and other formats it does not recognize as
// application/octet-stream; and on the wire it is worse, because the file endpoint
// serves *everything* as application/octet-stream — three PDFs fetched from it came
// back as octet-stream with %PDF as their first bytes, as did a .zip. The name is
// the only field that says what the file actually is.
func zenodoExt(key string) string {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(key), "."))
	return ext
}

// zenodoRecordID extracts the numeric record id from a Zenodo DOI, reporting
// whether the DOI is one at all. It requires the "10.5281/zenodo." prefix
// (matched case-insensitively, since a DOI is case-insensitive by specification and
// a caller may reasonably pass 10.5281/ZENODO.123) followed by digits and nothing
// else, so a DOI naming something other than a record under CERN's prefix is
// rejected rather than turned into a guess.
func zenodoRecordID(doi string) (string, bool) {
	if len(doi) <= len(zenodoDOIPrefix) || !strings.EqualFold(doi[:len(zenodoDOIPrefix)], zenodoDOIPrefix) {
		return "", false
	}
	id := doi[len(zenodoDOIPrefix):]
	if !isDigits(id) {
		return "", false
	}
	return id, true
}

// isDigits reports whether s is a non-empty run of ASCII decimal digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
