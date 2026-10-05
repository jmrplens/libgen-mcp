package extract

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

// textProbePages caps how many pages probePDFTextLayer reads before concluding
// that a PDF has no text layer. A scanned book is scanned throughout, so a
// sample settles the question; reading every page of a 900-page image scan to
// prove the obvious would cost more than the answer is worth.
const textProbePages = 20

// pdfTextState is what probePDFTextLayer found: usable text, no text at all, or
// a file it could not read.
type pdfTextState int

// The three states a text-layer probe can report.
const (
	pdfTextPresent pdfTextState = iota
	pdfTextAbsent
	pdfTextUnreadable
)

// invalidPDFReason is the diagnosis for a file whose bytes the PDF reader
// rejects outright. Shared so every read mode words it the same way.
func invalidPDFReason(err error) string {
	return fmt.Sprintf("not a valid PDF: %v", err)
}

// encryptedPDFReason is the diagnosis for a PDF the reader will not open, or
// would decrypt into other bytes, because of how it is encrypted.
// ledongthuc/pdf implements part of the standard security handler: RC4
// without crypt filters (V=1 and V=2), correctly only for a key of
// minRC4KeyBits or more, and an AES-128 crypt filter (V=4 with AESV2) whose
// /Length gives the key in bytes. It refuses AES-256 (V=5), RC4 under crypt
// filters (V=4 with V2), AES-128 whose crypt filter gives the length in bits,
// as pdfcpu writes it, and a certificate-based handler, and openPDF refuses
// the shorter RC4 keys. The reason says what the reader decrypts rather than
// naming what the file uses, because the reader's error does not always say:
// a refused V=4 file can be any of three of those. Such a file is valid, so
// calling it invalid would send the caller looking for a better copy of a file
// that is fine. Shared so every read mode words it the same way. One literal
// rather than a concatenation, like lockedPDFReason, for the reason
// noPDFOutlineReason gives.
const encryptedPDFReason = "cannot read PDF: it is encrypted in a way this reader cannot decrypt (it decrypts only RC4 with a key of 88 bits or more and AES-128 whose crypt filter gives the key length in bytes), so neither its text nor its table of contents can be read"

// minRC4KeyBits is the shortest RC4 file key ledongthuc/pdf decrypts
// correctly. The key for one object is the MD5 of the file key, the object
// number and the generation, cut to n+5 bytes for an n-byte file key and to
// no more than 16 (ISO 32000-1, 7.6.2, Algorithm 1). The reader never cuts
// it, so its key and the file's agree only once n+5 reaches 16, an 11-byte
// file key. Below that every string and stream decrypts into other bytes: the
// titles of a 40-bit file came back as noise, and its pages as text-free,
// which said "scanned" about a document that is not.
const minRC4KeyBits = 88

// rc4KeyTooShort reports whether r decrypts the file with an RC4 key shorter
// than minRC4KeyBits. Revision 2 always takes a 40-bit key, whatever /Length
// says, and so does the reader. From revision 3 the key is /Length bits long,
// 40 when /Length is absent, which reads here as 0 and is short as well. An
// AES crypt filter (V=4) is not RC4, and the AES-128 key it takes is whole.
func rc4KeyTooShort(r *pdf.Reader) bool {
	enc := r.Trailer().Key("Encrypt")
	if enc.Kind() != pdf.Dict || enc.Key("V").Int64() == 4 {
		return false
	}
	return enc.Key("R").Int64() == 2 || enc.Key("Length").Int64() < minRC4KeyBits
}

// lockedPDFReason is the diagnosis for a PDF that needs a password to open.
// The read tool takes none, so this is final for the file as it is. Shared so
// every read mode words it the same way.
const lockedPDFReason = "cannot read PDF: it needs a password to open, and this reader is given none, so neither its text nor its table of contents can be read"

// damagedPDFReason is the diagnosis for a file that opens as a PDF and whose
// structure the reader cannot follow: a truncated download, a cross-reference
// table that does not lead to the objects, a trailer cut short. ledongthuc/pdf
// does not rebuild a broken cross-reference table the way some viewers do, so
// a file one of them repairs on opening is refused here, and the hint that
// another copy may be intact is the useful part of the answer for a file this
// server downloaded. Shared so every read mode words it the same way.
func damagedPDFReason(err error) string {
	return fmt.Sprintf("cannot read PDF: the file is damaged (%s), and this reader does not repair one, "+
		"so neither its text nor its table of contents can be read; another copy of the file may be intact",
		pdfErrorDetail(err))
}

// maxPDFErrorDetail bounds how much of the reader's own error a diagnosis
// quotes. The reader's lexer puts the bytes it could not parse into its error,
// up to a buffer's worth of whatever the file holds, which is noise to the
// caller and untrusted text besides.
const maxPDFErrorDetail = 120

// pdfErrorDetail returns the reader's error as one short line: invalid UTF-8
// and control characters become spaces, runs of them collapse, and anything
// past maxPDFErrorDetail runes is cut and marked as cut.
func pdfErrorDetail(err error) string {
	detail := strings.Join(strings.FieldsFunc(strings.ToValidUTF8(err.Error(), " "), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
	if utf8.RuneCountInString(detail) <= maxPDFErrorDetail {
		return detail
	}
	return string([]rune(detail)[:maxPDFErrorDetail]) + "..."
}

// encryptionRefusalRE matches the start of every error ledongthuc/pdf returns
// when it will not decrypt a file: an encryption filter, version or revision
// it does not implement, a key length outside 40 to 128 bits, and an
// encryption dictionary without its O and U entries. It is anchored at the
// start, and the two messages that end with their words at the end, because
// the reader's other errors quote the bytes it could not parse, and a damaged
// file whose stray bytes say "encryption" is still a damaged file.
var encryptionRefusalRE = regexp.MustCompile(
	`^(unsupported PDF: encryption |malformed PDF: (-?\d+-bit encryption key$|encryption revision |missing O= or U= encryption parameters$))`,
)

// openPDFReason is the diagnosis for a file the PDF reader would not open.
//
// The reader reports a password it could not match with a sentinel, and the
// rest only in its error's words. Every refusal of an encryption it does not
// implement opens with words encryptionRefusalRE knows, and a file that does
// not even start as a PDF is "not a PDF file: invalid header"; anything else
// opened as a PDF and broke. The fixtures pin the matching: an AES-256, an RC4
// V=4 and a pdfcpu AES-128 file are held to encryptedPDFReason and a truncated
// one to damagedPDFReason, so a release of the reader that words it
// differently fails a test rather than changing a diagnosis in silence.
func openPDFReason(err error) string {
	msg := err.Error()
	switch {
	case errors.Is(err, pdf.ErrInvalidPassword):
		return lockedPDFReason
	case encryptionRefusalRE.MatchString(msg):
		return encryptedPDFReason
	case strings.HasPrefix(msg, "not a PDF file: invalid header"):
		return invalidPDFReason(err)
	default:
		return damagedPDFReason(err)
	}
}

// pdf20Header is how a PDF 2.0 file begins. ledongthuc/pdf opens a file only
// when its header names a version from 1.0 to 1.7, and refuses every 2.0 file
// as "not a PDF file: invalid header" before reading anything else, which
// called a valid file invalid and lost its outline. PDF 2.0 keeps the file
// structure of 1.7 (the cross-reference table or stream, the trailer, object
// streams, the page tree and the outline), so the reader is shown the file
// with a 1.7 header and reads the rest as it is. AES-256, the encryption 2.0
// asks a writer for, is still refused as one the reader does not decrypt.
const pdf20Header = "%PDF-2."

// shownVersion is the version a PDF 2.0 file's header names to the reader. It
// is written over the header's own version, which starts at versionAt, where
// "%PDF-" ends.
const (
	shownVersion = "1.7"
	versionAt    = int64(len("%PDF-"))
)

// headerView is a PDF 2.0 file as the reader is shown it: every byte as it
// is, except the header's version, which reads as shownVersion.
type headerView struct {
	io.ReaderAt
}

// ReadAt reads p from the file at off, and then writes over whatever part of
// p holds the header's version.
func (v headerView) ReadAt(p []byte, off int64) (int, error) {
	n, err := v.ReaderAt.ReadAt(p, off)
	for at := max(off, versionAt); at < min(off+int64(n), versionAt+int64(len(shownVersion))); at++ {
		p[at-off] = shownVersion[at-versionAt]
	}
	return n, err
}

// pdfBytes returns what the PDF reader is given for d: a PDF 2.0 file through
// headerView, and any other file as it is.
func pdfBytes(d document) io.ReaderAt {
	head := make([]byte, len(pdf20Header))
	n, _ := d.r.ReadAt(head, 0)
	if string(head[:n]) == pdf20Header {
		return headerView{d.r}
	}
	return d.r
}

// openPDF opens d with the PDF reader every mode uses and checks what each
// mode checks before it reads a page: that the reader opened the file, that
// it will decrypt the file into its own bytes, and that its page tree is safe
// to hand to Reader.Page. It returns the reader, or nil and the diagnosis for
// a file no mode can read. The reader can panic on malformed input, so a
// caller runs it behind recover().
func openPDF(d document) (r *pdf.Reader, why string) {
	r, err := pdf.NewReader(pdfBytes(d), d.size)
	if err != nil {
		return nil, openPDFReason(err)
	}
	if rc4KeyTooShort(r) {
		return nil, encryptedPDFReason
	}
	if cyclic := pageTreeReason(r); cyclic != "" {
		return nil, cyclic
	}
	return r, ""
}

// malformedPDFReason is the diagnosis for a PDF that made the reader panic —
// malformed or encrypted input. Shared so every read mode words it the same way.
func malformedPDFReason(rec any) string {
	return fmt.Sprintf("cannot read PDF (malformed or encrypted): %v", rec)
}

// probePDFTextLayer reports whether the PDF document d has any extractable text.
// It exists so the outline path can reach the same verdict as the text path
// about the same file: a scanned PDF has no table of contents *because* it has
// no text layer, and saying only "no table of contents" sends the caller off to
// read text that will never come.
//
// The reader can panic on malformed or encrypted input, so the probe is guarded
// by recover(): a panic becomes pdfTextUnreadable rather than a crash. Only ctx
// cancellation yields a non-nil error.
func probePDFTextLayer(ctx context.Context, d document) (state pdfTextState, reason string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			state, reason, err = pdfTextUnreadable, malformedPDFReason(rec), nil
		}
	}()
	r, why := openPDF(d)
	if why != "" {
		return pdfTextUnreadable, why, nil
	}

	for _, n := range probePageNumbers(r.NumPage(), textProbePages) {
		if e := ctx.Err(); e != nil {
			return pdfTextUnreadable, "", e
		}
		p := r.Page(n)
		if p.V.IsNull() {
			continue
		}
		if text, _ := p.GetPlainText(nil); strings.TrimSpace(text) != "" {
			return pdfTextPresent, "", nil
		}
	}
	return pdfTextAbsent, "", nil
}

// probePageNumbers picks at most budget 1-based page numbers to sample from a
// document of total pages. Short documents are read whole; a longer one is
// sampled at an even stride from page 1, so a book that opens on a scanned cover
// and plate section is not mistaken for a scan of itself.
func probePageNumbers(total, budget int) []int {
	if total < 1 || budget < 1 {
		return nil
	}
	stride := max(1, total/budget)
	pages := make([]int, 0, min(total, budget))
	for i := 1; i <= total && len(pages) < budget; i += stride {
		pages = append(pages, i)
	}
	return pages
}

// pdfScan holds the accumulated result of reading a range of PDF pages.
type pdfScan struct {
	text      string
	pageEnd   int
	nextPage  int
	hasMore   bool
	truncated bool
}

// extractPDF reads a page range from a PDF and returns a page-paginated Chunk.
// The ledongthuc/pdf reader can panic on malformed or encrypted input, so the
// whole read is guarded by recover(): a panic becomes a not-extractable Chunk
// rather than a crash. A canceled ctx yields the context error.
func extractPDF(ctx context.Context, d document, r Req) (Chunk, error) {
	return readPDFRange(ctx, d, pdfRange{start: r.StartPage, maxPages: r.MaxPages, maxChars: r.MaxChars})
}

// pdfRange is the page window one PDF read covers. last bounds the read at that
// page, as the end of a section does, and 0 means the document's last page.
// A non-positive start, maxPages or maxChars takes the package default.
type pdfRange struct {
	start    int
	last     int
	maxPages int
	maxChars int
}

// withDefaults fills the fields a caller left non-positive.
func (pr pdfRange) withDefaults() pdfRange {
	if pr.start <= 0 {
		pr.start = defaultStartPage
	}
	if pr.maxPages <= 0 {
		pr.maxPages = defaultMaxPages
	}
	if pr.maxChars <= 0 {
		pr.maxChars = defaultMaxChars
	}
	return pr
}

// readPDFRange reads one page window of a PDF behind recover(): the
// ledongthuc/pdf reader can panic on malformed or encrypted input, and a panic
// becomes a not-extractable Chunk rather than a crash.
func readPDFRange(ctx context.Context, d document, pr pdfRange) (chunk Chunk, err error) {
	if e := ctx.Err(); e != nil {
		return Chunk{}, e
	}
	defer func() {
		if rec := recover(); rec != nil {
			chunk = Chunk{Format: "pdf", Reason: malformedPDFReason(rec)}
			err = nil
		}
	}()
	return readPDFPages(ctx, d, pr.withDefaults())
}

// readPDFPages parses the PDF, scans the requested page range and assembles the
// final Chunk, including no-text-layer detection.
func readPDFPages(ctx context.Context, d document, pr pdfRange) (Chunk, error) {
	r, why := openPDF(d)
	if why != "" {
		return Chunk{Format: "pdf", Reason: why}, nil
	}

	total := r.NumPage()
	if total > 0 && pr.start > total {
		return Chunk{
			Format:     "pdf",
			TotalPages: total,
			Reason:     fmt.Sprintf("start page %d is beyond the document's last page (%d pages)", pr.start, total),
		}, nil
	}
	if pr.last <= 0 {
		pr.last = total
	}
	pr.last = min(pr.last, total)

	scan, err := scanPDFPages(ctx, r, pr)
	if err != nil {
		return Chunk{}, err
	}
	startPage := pr.start

	if strings.TrimSpace(scan.text) == "" {
		return Chunk{
			Format:     "pdf",
			TotalPages: total,
			Reason:     noTextLayerReason,
		}, nil
	}

	chunk := Chunk{
		Text:        scan.text,
		Format:      "pdf",
		Extractable: true,
		PageStart:   startPage,
		PageEnd:     scan.pageEnd,
		TotalPages:  total,
		HasMore:     scan.hasMore,
		Truncated:   scan.truncated,
	}
	chunk.NextCursor.Page = scan.nextPage
	return chunk, nil
}

// scanPDFPages iterates pages from pr.start to pr.last, accumulating plain text
// without ever splitting a page. It stops before a page when maxChars is
// already reached (marking Truncated) or after maxPages pages have been read,
// and checks ctx between pages. Nothing past pr.last counts as more: that is
// where the caller's window ends, whether or not the document does. pr has its
// defaults, so maxPages and maxChars are both positive.
func scanPDFPages(ctx context.Context, r *pdf.Reader, pr pdfRange) (pdfScan, error) {
	var sb strings.Builder
	var s pdfScan
	pagesRead := 0
	charCount := 0
	maxPages, maxChars := pr.maxPages, pr.maxChars

	for i := pr.start; i <= pr.last; i++ {
		if e := ctx.Err(); e != nil {
			return pdfScan{}, e
		}
		if charCount >= maxChars {
			s.hasMore = true
			s.truncated = true
			s.nextPage = i
			break
		}
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		text, _ := p.GetPlainText(nil)
		sb.WriteString(text)
		charCount += utf8.RuneCountInString(text)
		pagesRead++
		s.pageEnd = i
		if pagesRead >= maxPages {
			s.nextPage = i + 1
			s.hasMore = i < pr.last
			break
		}
	}

	s.text = sb.String()
	return s, nil
}
