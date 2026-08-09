package libgen

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// NameOrigin says where a saved file's name came from. It travels back to the
// caller on DownloadResult so a derived name is never mistaken for one the
// serving source actually announced — see chooseFileName for why that
// distinction is load-bearing rather than cosmetic.
type NameOrigin string

// The four places a download's name can come from, in the order chooseFileName
// considers them.
const (
	// NameFromCaller is a filename the caller passed in; it is used as given
	// (path-sanitized, never "cleaned").
	NameFromCaller NameOrigin = "caller"
	// NameFromAnnounced is the name the serving source sent in Content-Disposition,
	// stripped of mirror/scraper marks but otherwise preserved.
	NameFromAnnounced NameOrigin = "announced"
	// NameFromMetadata is a name built from the record's author/title/year.
	NameFromMetadata NameOrigin = "metadata"
	// NameFromIdentifier is a name built from the requested md5, DOI or ISBN,
	// because nothing better was available.
	NameFromIdentifier NameOrigin = "identifier"
)

// Derived reports whether the name was constructed by this server rather than
// announced by the source that served the bytes. On an unverified download a
// derived name is NOT evidence of what was delivered, and the download tool says
// so in its result.
func (o NameOrigin) Derived() bool {
	return o == NameFromMetadata || o == NameFromIdentifier
}

// maxFilenameBytes caps a generated filename at 200 BYTES, not runes.
//
// The limit that actually bites is the filesystem's, and every common one counts
// bytes, not characters: ext4, XFS, btrfs and APFS all cap a single path
// component at 255 bytes of UTF-8, so a 200-rune CJK title is ~600 bytes and the
// create fails with ENAMETOOLONG. (NTFS counts 255 UTF-16 code units instead;
// 200 bytes of UTF-8 can never exceed 200 UTF-16 units, so the byte cap is the
// stricter of the two and satisfies both.) The 55 bytes of headroom below 255
// leave room for the collision discriminator uniqueDest may append.
const maxFilenameBytes = 200

// fallbackFilename names a download whose every naming input was empty or unsafe.
const fallbackFilename = "download"

// nameRequest bundles everything chooseFileName may consult when naming a
// download.
type nameRequest struct {
	// explicit is the filename the caller asked for; it wins outright.
	explicit string
	// announced is the raw Content-Disposition filename the source sent, if any.
	announced string
	// sniffed is the extension implied by the first bytes actually on the wire
	// ("" when they match no format this server recognizes).
	sniffed string
	// ext is the serving source's own extension hint (from the resolved URL).
	ext string
	// item carries the identifiers that were REQUESTED plus any bibliographic
	// metadata looked up for them.
	item Item
	// verified reports that the streamed bytes are being MD5-checked against
	// item.MD5, so the file provably IS the requested record.
	verified bool
}

// chooseFileName picks the name a download is saved under, and reports where
// that name came from.
//
// # Why verified and unverified downloads are named differently
//
// Renaming a file after the record that was REQUESTED can mask a wrong delivery,
// so this function does it only when the bytes prove they are that record.
//
// A download by md5 is checked against that digest before the file is renamed
// into place (streamToPartAndVerify), so on the verified path the content *is*
// the requested record and a metadata-built name is safe — and better than what
// the mirror announces, which is where the "[Incerto] Taleb… [10.1371_journal…] -
// libgen.li.epub" names come from.
//
// A download by DOI or ISBN has no digest to check against. There the name the
// source announced is EVIDENCE of what was actually served, and it must not be
// replaced by the bibliographic record of what was asked for. This is not
// hypothetical: fatcat once served an unrelated preprint for a PLoS DOI, and a
// corrupt LibGen record served Taleb's *Antifragile* for an Ioannidis article
// DOI. Both were caught because the filename on disk disagreed with the request.
// Rename those by the requested record and the wrong file arrives wearing the
// right name, and the only signal is gone.
//
// So, in order:
//
//   - an explicit caller filename always wins, untouched beyond path sanitizing;
//   - verified: metadata name, else the cleaned announced name, else the md5;
//   - unverified: the cleaned announced name, else the identifier that was
//     requested (a neutral label that asserts nothing about the contents), else
//     metadata as a last resort.
//
// On the unverified path the announced name is only discarded when it carries no
// evidence at all — it is empty, it is a placeholder like "download.pdf", or it
// is just the md5 (see usableAnnounced). Whenever a name has to be derived, the
// origin returned is NameFromMetadata or NameFromIdentifier and the caller
// reports that in the download result.
//
// The extension never comes from the catalog: it is taken from the bytes on the
// wire, else from the announced name, else from the source's own URL hint. A
// record's format column is third-party data that is frequently wrong, and a
// .epub that is really a PDF is worse than no extension at all.
func chooseFileName(r nameRequest) (string, NameOrigin) {
	if explicit := strings.TrimSpace(r.explicit); explicit != "" {
		// Sanitized, not cleaned: the caller gets the name they asked for, but a
		// name reaching this server from an MCP client is still untrusted input
		// that becomes a path, so "../../.ssh/authorized_keys" is not honored.
		return sanitizeFilename(explicit), NameFromCaller
	}
	ext := r.extension()
	if r.verified {
		return verifiedName(r, ext)
	}
	return unverifiedName(r, ext)
}

// verifiedName names a download whose bytes matched the requested md5: the
// metadata-built name is preferred, because the content is provably the record
// those metadata describe.
func verifiedName(r nameRequest, ext string) (string, NameOrigin) {
	if name := metadataName(r.item.Meta, ext); name != "" {
		return name, NameFromMetadata
	}
	if name := announcedName(r.announced, ext, r.item.MD5); name != "" {
		return name, NameFromAnnounced
	}
	if name := identifierName([]string{r.item.MD5, r.item.DOI, r.item.ISBN}, ext); name != "" {
		return name, NameFromIdentifier
	}
	return sanitizeFilename(fallbackFilename + extSuffix(ext)), NameFromIdentifier
}

// unverifiedName names a download with no digest to check against (by DOI or
// ISBN). The name the source announced comes first and is kept whenever it says
// anything at all, because it is the only record of what was actually served.
func unverifiedName(r nameRequest, ext string) (string, NameOrigin) {
	if name := announcedName(r.announced, ext, r.item.MD5); name != "" {
		return name, NameFromAnnounced
	}
	// The identifier is preferred over the metadata here: it labels the file with
	// what was ASKED FOR without asserting anything about what is inside it, so a
	// mis-served file still reads as "this is what came back for that DOI".
	if name := identifierName([]string{r.item.DOI, r.item.ISBN, r.item.MD5}, ext); name != "" {
		return name, NameFromIdentifier
	}
	if name := metadataName(r.item.Meta, ext); name != "" {
		return name, NameFromMetadata
	}
	return sanitizeFilename(fallbackFilename + extSuffix(ext)), NameFromIdentifier
}

// SuggestFilename names a file that has NOT been fetched — the resolve-only path,
// where the server hands back a URL for the caller to retrieve, and the download
// confirmation prompt, which names the file before a byte is downloaded. It
// applies exactly the rule chooseFileName documents, with verified standing for
// "the caller will be told to hash-check these bytes against the md5".
func SuggestFilename(item Item, explicit, ext string, verified bool) string {
	name, _ := chooseFileName(nameRequest{explicit: explicit, ext: ext, item: item, verified: verified})
	return name
}

// extension returns the extension to put on a generated name: the format sniffed
// from the bytes on the wire, else the one on the announced name, else the
// serving source's hint. The catalog's own extension field is deliberately not a
// candidate — see chooseFileName.
func (r nameRequest) extension() string {
	if r.sniffed != "" {
		return r.sniffed
	}
	if e := extensionOf(r.announced); e != "" {
		return e
	}
	return normalizeExt(r.ext)
}

// extensionOf returns the normalized extension of a filename, or "" when it has
// none this server is willing to treat as one.
func extensionOf(name string) string {
	return normalizeExt(path.Ext(name))
}

// normalizeExt lowercases an extension and accepts it only if it looks like one:
// two to five ASCII alphanumerics starting with a letter. That rejects the tail
// of a dotted identifier ("10.1371_journal.pmed.0020124" does not end in a
// ".0020124" extension) while accepting pdf, epub, mobi, djvu, cbz and azw3.
func normalizeExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ext), ".")))
	if len(ext) < 2 || len(ext) > 5 {
		return ""
	}
	for i, r := range ext {
		alnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !alnum || (i == 0 && r >= '0' && r <= '9') {
			return ""
		}
	}
	return ext
}

// extSuffix returns ".ext" for a non-empty extension, else "".
func extSuffix(ext string) string {
	if ext == "" {
		return ""
	}
	return "." + strings.TrimPrefix(ext, ".")
}

// metadataName builds "<Author> - <Title> (<Year>).<ext>" from a record, omitting
// any empty piece, or "" when there is no title to build around.
//
// The shape is chosen so a download directory sorts by author and so the year
// can never be confused with part of the title; it is also what the mirrors get
// right when they get anything right ("The Catcher in the Rye (2010).epub").
func metadataName(m *FileMeta, ext string) string {
	if m == nil {
		return ""
	}
	title := namePiece(m.Title)
	if title == "" {
		return ""
	}
	var b strings.Builder
	if author := namePiece(primaryAuthor(m.Author)); author != "" {
		b.WriteString(author)
		b.WriteString(" - ")
	}
	b.WriteString(title)
	if year := namePiece(m.Year); year != "" {
		b.WriteString(" (")
		b.WriteString(year)
		b.WriteString(")")
	}
	return sanitizeFilename(b.String() + extSuffix(ext))
}

// primaryAuthor reduces a catalog author field to its first author plus "et al."
// when it lists several. Catalog records routinely carry a dozen semicolon- or
// comma-separated names, which would spend the whole length budget before the
// title is reached.
func primaryAuthor(author string) string {
	parts := strings.Split(author, ";")
	first := strings.TrimSpace(parts[0])
	if first == "" || len(parts) == 1 {
		return first
	}
	return first + " et al."
}

// namePiece prepares one segment of a built filename: whitespace collapsed, a
// title's colon turned into the " -" separator a filesystem can actually store
// (":" is illegal on Windows and displays as "/" in the macOS Finder), and the
// separator characters trimmed off both ends.
func namePiece(s string) string {
	s = strings.ReplaceAll(s, ":", " -")
	s = strings.Join(strings.Fields(s), " ")
	// Trailing dots survive on purpose: "Taleb, N. et al." and "2nd ed." end in one.
	return strings.Trim(s, " -_")
}

// announcedName cleans the name the source announced and returns it with the
// chosen extension, or "" when the announced name carries no evidence worth
// keeping (empty, a placeholder, or just the md5 restated).
func announcedName(announced, ext, md5 string) string {
	stem := announcedStem(announced)
	if !usableAnnounced(stem, md5) {
		return ""
	}
	return sanitizeFilename(stem + extSuffix(ext))
}

// announcedStem reduces a Content-Disposition filename to its bare stem: any
// directory part a hostile header may have smuggled in is dropped, a recognized
// extension is split off, and the mirror/scraper marks are stripped.
func announcedStem(announced string) string {
	base := path.Base(filepath.ToSlash(strings.TrimSpace(announced)))
	if base == "." || base == ".." || base == "/" {
		return ""
	}
	if ext := path.Ext(base); normalizeExt(ext) != "" {
		base = base[:len(base)-len(ext)]
	}
	return stripSourceMarks(base)
}

// usableAnnounced reports whether a cleaned announced stem still says anything
// about what was delivered. A placeholder the CDN hands to every file
// ("download", "fulltext") and a stem that merely restates the md5 identify
// nothing, so the caller may derive a better name; anything else — including an
// ugly publisher id like "npre2007361-1" — is kept, because it is the only
// evidence of what the source actually served.
func usableAnnounced(stem, md5 string) bool {
	if stem == "" {
		return false
	}
	if md5 != "" && strings.EqualFold(stem, md5) {
		return false
	}
	return !isPlaceholderStem(stem)
}

// placeholderStems are the generic names a CDN or a download endpoint gives every
// file it serves. They are the only announced names this server is willing to
// throw away, because they identify nothing: "download.pdf" is what the Internet
// Archive returns for every ISBN.
//
// The list is kept narrow on purpose. Every entry costs the evidence that name
// would have carried on an unverified download, so a term that could plausibly be
// somebody's actual title — "book", "ebook", "manual" — is left out even though a
// CDN might well emit it.
var placeholderStems = map[string]bool{
	"attachment": true, "blob": true, "content": true, "data": true,
	"default": true, "document": true, "download": true, "downloadfile": true,
	"downloads": true, "file": true, "files": true, "fulltext": true,
	"full-text": true, "get": true, "getfile": true, "index": true, "output": true,
	"temp": true, "tmp": true, "unknown": true, "untitled": true, "view": true,
	"pdf": true, "epub": true, "djvu": true, "mobi": true, "txt": true,
}

// placeholderCounter matches the " (2)" / "_3" a browser or CDN appends to a
// repeated placeholder name, so "download (1)" is recognized as one too.
var placeholderCounter = regexp.MustCompile(`[\s_-]*\(?\d{1,3}\)?$`)

// isPlaceholderStem reports whether a stem is one of the generic CDN names, with
// or without a repetition counter. The counter is only stripped for the lookup,
// so a real title like "Volume 2" is unaffected.
func isPlaceholderStem(stem string) bool {
	key := strings.ToLower(strings.TrimSpace(stem))
	if placeholderStems[key] {
		return true
	}
	return placeholderStems[strings.TrimSpace(placeholderCounter.ReplaceAllString(key, ""))]
}

// shadowSites is the alternation of the shadow-library and scraper hostnames that
// get glued onto filenames. It is deliberately an explicit list rather than a
// "strip anything after the last dash" heuristic: the names being cleaned are
// third-party titles, and a heuristic eats real content — "[Incerto]" is the name
// of Taleb's series, not a mirror's watermark.
const shadowSites = `libgen(?:\.[a-z]{2,3})?|library ?genesis|anna'?s[ _-]?archive(?:\.[a-z]{2,3})?|` +
	`z-?lib(?:rary)?(?:\.[a-z]{2,3})?|b-?ok(?:\.[a-z]{2,3})?|1lib(?:\.[a-z]{2,6})?|sci-?hub(?:\.[a-z]{2,3})?`

// sourceMarks are the fragments a mirror or a scraper glues onto a filename, each
// anchored so it can only match where such a mark actually appears — at the very
// end of the stem, or (for the prefix form) at the very start. Bracketed groups
// are only stripped when their CONTENTS look like an identifier, never for being
// bracketed, which is what keeps a series name such as "[Incerto]" intact.
var sourceMarks = []*regexp.Regexp{
	// " - libgen.li", " -- Anna's Archive", "_ Z-Library"
	regexp.MustCompile(`(?i)\s*[-–—_]{1,3}\s*(?:` + shadowSites + `)\s*$`),
	// "(Z-Library)", "[libgen.li]"
	regexp.MustCompile(`(?i)\s*[(\[{]\s*(?:` + shadowSites + `)\s*[)\]}]\s*$`),
	// "[10.1371_journal.pmed.0020124]" — a DOI parked at the end in brackets.
	regexp.MustCompile(`(?i)\s*[(\[{]\s*10\.\d{4,9}[^)\]}]{0,120}[)\]}]\s*$`),
	// "[9780123456789]" / "(0306406152)" — a bracketed ISBN.
	regexp.MustCompile(`(?i)\s*[(\[{]\s*(?:97[89][\d\- ]{10,17}|\d{9}[\dx])\s*[)\]}]\s*$`),
	// " -- 9780123456789" — Anna's Archive's dash-separated ISBN field.
	regexp.MustCompile(`(?i)[\s_]*[-–—]{2,3}[\s_]*(?:97[89]\d{10}|\d{9}[\dx])\s*$`),
	// A trailing md5, bare or bracketed, with or without a separator in front.
	regexp.MustCompile(`(?i)[\s_]*[-–—]{0,3}[\s_]*[(\[{]?[0-9a-f]{32}[)\]}]?\s*$`),
	// "libgen.li_Title", "annas-archive - Title" — the same marks as a prefix.
	regexp.MustCompile(`(?i)^\s*(?:` + shadowSites + `)\s*[_\-–—]+\s*`),
}

// stripSourceMarkPasses bounds the repeated cleaning sweep. Names carry at most a
// handful of stacked marks ("… [DOI] - libgen.li"), and a bound guarantees the
// loop terminates whatever a pattern does.
const stripSourceMarkPasses = 6

// stripSourceMarks removes the mirror and scraper marks from a filename stem,
// sweeping repeatedly because they stack, and collapses the whitespace and
// dangling separators the removals leave behind. Everything the patterns do not
// match is preserved verbatim: on an unverified download this stem is the only
// evidence of what the source really served.
func stripSourceMarks(stem string) string {
	for range stripSourceMarkPasses {
		before := stem
		for _, re := range sourceMarks {
			stem = re.ReplaceAllString(stem, "")
		}
		stem = strings.Trim(strings.Join(strings.Fields(stem), " "), " -_")
		if stem == before {
			break
		}
	}
	return stem
}

// identifierName names a file after the first non-empty identifier in ids, which
// the caller supplies in the order that suits the download. It returns "" when
// every identifier is empty.
func identifierName(ids []string, ext string) string {
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			return sanitizeFilename(id + extSuffix(ext))
		}
	}
	return ""
}

// windowsReservedStems are the device names Windows refuses to create a file for,
// with or without an extension: "NUL.pdf" is still the null device. The server
// runs on several operating systems and its downloads travel between them, so a
// name is fixed up everywhere rather than only where it would fail today.
var windowsReservedStems = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com0": true, "com1": true, "com2": true, "com3": true, "com4": true,
	"com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt0": true, "lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true,
	"lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// sanitizeFilename turns an arbitrary string into a single, safe path component.
//
// Everything it guards against comes from the same fact: the strings being named
// are third-party titles, authors and CDN headers, and the result is written to
// the user's disk. In order, it
//
//   - normalizes to NFC, so the same title cannot produce two different byte
//     sequences (and so the byte cap below measures a stable encoding);
//   - drops control characters and every Unicode format character, which covers
//     the bidi overrides (U+202E and friends) that make "exe.pdf" render as
//     "fdp.exe";
//   - replaces the path separators and the Windows-illegal punctuation
//     (/ \ : * ? " < > |) with "_", so a name can never become a path;
//   - collapses whitespace runs and trims spaces and dots off both ends, which
//     also disposes of "." and ".." and of leading-dot hidden names;
//   - prefixes an underscore to a Windows reserved device stem (CON, NUL, COM1…);
//   - caps the result at maxFilenameBytes without splitting a rune.
//
// An input that survives none of that becomes fallbackFilename, so the caller
// always gets a usable component back.
func sanitizeFilename(s string) string {
	s = strings.Map(safeNameRune, norm.NFC.String(s))
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, " .")
	if s == "" {
		return fallbackFilename
	}
	s = escapeReservedStem(s)
	// Re-trim after the cap, which can expose a dot or a space at either end. The
	// result cannot be empty: the pre-cap value was non-empty and started with a
	// character that is neither a dot nor a space, and truncation keeps a prefix.
	return strings.Trim(capFilenameBytes(s, maxFilenameBytes), " .")
}

// safeNameRune maps one rune for sanitizeFilename: whitespace is flattened to a
// space, control and format characters are dropped, path-hostile punctuation
// becomes "_", everything else is kept.
func safeNameRune(r rune) rune {
	switch {
	// Whitespace of any flavor (tabs, newlines, NBSP) becomes a plain space rather
	// than vanishing, so a line break cannot silently weld two words together.
	case unicode.IsSpace(r):
		return ' '
	case unicode.Is(unicode.Cc, r), unicode.Is(unicode.Cf, r), r == 0x7f:
		return -1
	case strings.ContainsRune(`/\:*?"<>|`, r):
		return '_'
	default:
		return r
	}
}

// escapeReservedStem prefixes an underscore to a name whose stem is a Windows
// reserved device name, leaving every other name untouched.
func escapeReservedStem(s string) string {
	stem, _, _ := strings.Cut(s, ".")
	if windowsReservedStems[strings.ToLower(stem)] {
		return "_" + s
	}
	return s
}

// capFilenameBytes truncates a name to limit bytes, keeping its extension and
// cutting only the stem, and never splitting a multi-byte rune in half.
func capFilenameBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	ext := path.Ext(s)
	if normalizeExt(ext) == "" || len(ext) >= limit {
		return strings.Trim(truncateBytes(s, limit), " .")
	}
	stem := truncateBytes(s[:len(s)-len(ext)], limit-len(ext))
	return strings.Trim(stem, " .-_") + ext
}

// truncateBytes returns the longest prefix of s that fits in limit bytes and ends
// on a rune boundary.
func truncateBytes(s string, limit int) string {
	// i is the byte offset of each rune, so the first rune that would cross the
	// limit marks the cut — which is, by construction, on a rune boundary.
	for i, r := range s {
		if i+utf8.RuneLen(r) > limit {
			return s[:i]
		}
	}
	return s
}

// maxCollisionSuffix bounds the numeric discriminators uniqueDest will try before
// giving up, so a pathological directory cannot spin.
const maxCollisionSuffix = 100

// uniqueDest returns the path a finished download should be renamed to, so that a
// DIFFERENT file already sitting there is never silently replaced.
//
// Two editions of one book, or two sources' renderings of one article, produce
// the same generated name; the rename that publishes the download would overwrite
// whichever arrived first. When something is already at dest, this appends the
// first eight hex digits of the downloaded bytes' own MD5 — the content's digest,
// not the requested one — and then a counter if even that is taken.
//
// A file of exactly the same size is treated as the same download arriving again
// and is replaced, which keeps re-downloading one item idempotent instead of
// littering the directory with copies.
func uniqueDest(dest string, size int64, digest string) (string, error) {
	if freeOrSame(dest, size) {
		return dest, nil
	}
	ext := path.Ext(dest)
	stem := dest[:len(dest)-len(ext)]
	if len(digest) >= 8 {
		if cand := stem + " (" + digest[:8] + ")" + ext; freeOrSame(cand, size) {
			return cand, nil
		}
		stem += " (" + digest[:8] + ")"
	}
	for n := 2; n < maxCollisionSuffix; n++ {
		if cand := fmt.Sprintf("%s (%d)%s", stem, n, ext); freeOrSame(cand, size) {
			return cand, nil
		}
	}
	return "", fmt.Errorf("cannot save the download: %s and its alternatives are all taken by other files", dest)
}

// freeOrSame reports whether candidate can be written without destroying
// something else: nothing is there, or what is there is a regular file of exactly
// the size just downloaded (the same item fetched again).
func freeOrSame(candidate string, size int64) bool {
	info, err := os.Stat(candidate)
	if err != nil {
		return true // absent, or unreadable — the rename itself will report a real problem
	}
	return info.Mode().IsRegular() && info.Size() == size
}

// sniffExt identifies the file format from the first bytes on the wire, returning
// the extension for it or "" when they match nothing it recognizes.
//
// This is the only trustworthy source of an extension: the catalog's format
// column is third-party data that is often wrong, and the mirror's announced name
// is only as good as the mirror. Formats that share a container are deliberately
// NOT guessed — a bare ZIP could be a cbz, a docx or an epub without its mimetype
// entry, and an extension invented from that would be worse than none.
func sniffExt(head []byte) string {
	switch {
	case hasPrefix(head, "%PDF-"):
		return "pdf"
	case isEPUBHead(head):
		return "epub"
	case len(head) >= 68 && string(head[60:68]) == "BOOKMOBI":
		return "mobi"
	case hasPrefix(head, "AT&TFORM") && len(head) >= 16 && (string(head[12:16]) == "DJVU" || string(head[12:16]) == "DJVM"):
		return "djvu"
	case hasPrefix(head, `{\rtf`):
		return "rtf"
	case hasPrefix(head, "\x00\x00\x00\x0cjP  "):
		return "jp2"
	default:
		return ""
	}
}

// hasPrefix reports whether the sniffed head begins with the literal prefix.
func hasPrefix(head []byte, prefix string) bool {
	return len(head) >= len(prefix) && string(head[:len(prefix)]) == prefix
}

// isEPUBHead reports whether the sniffed head is an EPUB: the OCF container spec
// requires the "mimetype" entry to be first and stored uncompressed, so the
// media type appears verbatim within the first few dozen bytes.
func isEPUBHead(head []byte) bool {
	if !hasPrefix(head, "PK\x03\x04") {
		return false
	}
	limit := min(len(head), 120)
	return strings.Contains(string(head[:limit]), "mimetypeapplication/epub+zip")
}
