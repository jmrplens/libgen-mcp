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

// encryptedPDFReason is the diagnosis for a PDF whose encryption keeps every
// read mode out: AES-256 (V=5), a certificate-based handler, and a file whose
// text partlyEncryptedPDFReason refuses when its outline could not be read
// either, because it sits in a compressed object stream. Such a file is
// valid, so calling it invalid would send the caller looking for a better copy
// of a file that is fine. Shared so every read mode words it the same way. One
// literal rather than a concatenation, like lockedPDFReason, for the reason
// noPDFOutlineReason gives.
const encryptedPDFReason = "cannot read PDF: it is encrypted in a way this reader cannot decrypt, so neither its text nor its table of contents can be read"

// partlyEncryptedPDFReason is the diagnosis the text modes give for a PDF whose
// text the reader cannot decrypt and whose outline the outline walk can.
// ledongthuc/pdf implements part of the standard security handler: RC4
// without crypt filters (V=1 and V=2), correctly only for a key of
// minRC4KeyBits or more, and an AES-128 crypt filter (V=4 with AESV2), whose
// /Length openReader shows it in bytes when pdfcpu wrote it in bits, in a file
// that encrypts its metadata. It refuses RC4 under a crypt filter (V=4 with
// V2), takes a file that leaves its metadata unencrypted for one that needs a
// password, and openPDF refuses the shorter RC4 keys, which the reader would
// decrypt into other bytes. The outline walk decrypts the strings of all three
// itself (selfDecrypting), so the caller is pointed at outline mode, which lists the
// table of contents unless it is in a compressed object stream, whose stream
// only the reader could decrypt. In outline mode, when the walk could not list
// one, the reason is encryptedPDFReason.
const partlyEncryptedPDFReason = "cannot read PDF text: it is encrypted in a way this reader cannot decrypt for its text, though outline mode may still list its table of contents"

// minRC4KeyBits is the shortest RC4 file key ledongthuc/pdf decrypts
// correctly. The key for one object is the MD5 of the file key, the object
// number and the generation, cut to n+5 bytes for an n-byte file key and to
// no more than 16 (ISO 32000-1, 7.6.2, Algorithm 1). The reader never cuts
// it, so its key and the file's agree only once n+5 reaches 16, an 11-byte
// file key. Below that every string and stream decrypts into other bytes: the
// titles of a 40-bit file came back as noise, and its pages as text-free,
// which said "scanned" about a document that is not. stringCrypt.objectKey
// cuts the key as the standard does, which is how the outline walk reads
// such a file's titles.
const minRC4KeyBits = 88

// rc4KeyTooShort reports whether r decrypts the file with an RC4 key shorter
// than minRC4KeyBits. Revision 2 always takes a 40-bit key, whatever /Length
// says, and so does the reader. From revision 3 the key is /Length bits long,
// 40 when /Length is absent, which reads here as 0 and is short as well. A
// file that is not encrypted has no version, and an AES crypt filter (V=4) is
// not RC4: the AES-128 key it takes is whole.
func rc4KeyTooShort(r *pdf.Reader) bool {
	enc := encryptionOf(r)
	if enc.version == 0 || enc.version == 4 {
		return false
	}
	return enc.revision == 2 || enc.bits < minRC4KeyBits
}

// encryption is what the open checks and the outline walk need to know of how
// a file is encrypted: the version, revision and key length its encryption
// dictionary states.
type encryption struct {
	version, revision, bits int64
}

// encryptionOf reads r's encryption dictionary, and returns all zeros for a
// file that is not encrypted.
//
// The dictionary is read through the reader, which decrypts every string in
// an object it reads, the dictionary's own strings among them, which are not
// encrypted. The numbers read here are not strings and come back whole, but
// under AES the reader panics on a string shorter than one block, and pikepdf
// writes an empty /OE and /UE into an AES-128 dictionary. The reader opened
// the file by reading that same dictionary before it had a key, so it parses,
// and decrypting with RC4 cannot panic: a panic here is AES, which the reader
// decrypts only under V=4, and is answered as V=4.
func encryptionOf(r *pdf.Reader) (enc encryption) {
	defer func() {
		if recover() != nil {
			enc = encryption{version: 4}
		}
	}()
	dict := r.Trailer().Key("Encrypt")
	return encryption{version: dict.Key("V").Int64(), revision: dict.Key("R").Int64(), bits: dict.Key("Length").Int64()}
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
// opened as a PDF and broke. The fixtures pin the matching: an AES-256 file is
// held to encryptedPDFReason, an RC4 V=4 one, which openPDF tells apart
// before asking, to partlyEncryptedPDFReason, one that needs a password to
// lockedPDFReason, and a truncated one to damagedPDFReason, so a release of
// the reader that words it differently fails a test rather than changing a
// diagnosis in silence.
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
// asks a writer for, is still refused as one the reader does not decrypt, and
// a title in UTF-8, which 2.0 adds, is decoded by textString.
const pdf20Header = "%PDF-2."

// shownVersion is the version a PDF 2.0 file's header names to the reader. It
// is written over the header's own version, which starts at versionAt, where
// "%PDF-" ends.
const (
	shownVersion = "1.7"
	versionAt    = int64(len("%PDF-"))
)

// overwritten is a file as the reader is shown it: every byte as it is,
// except the ones from at on, which read as with. A PDF 2.0 file's version
// and the keys and numbers of an encryption dictionary the reader would
// misread are shown to it that way, without a copy of the file.
type overwritten struct {
	io.ReaderAt
	at   int64
	with string
}

// ReadAt reads p from the file at off, and then writes over whatever part of
// p holds the bytes from at on.
func (v overwritten) ReadAt(p []byte, off int64) (int, error) {
	n, err := v.ReaderAt.ReadAt(p, off)
	for i := max(off, v.at); i < min(off+int64(n), v.at+int64(len(v.with))); i++ {
		p[i-off] = v.with[i-v.at]
	}
	return n, err
}

// pdfBytes returns what the PDF reader is given for d: a PDF 2.0 file with
// its version read as shownVersion, and any other file as it is.
func pdfBytes(d document) io.ReaderAt {
	head := make([]byte, len(pdf20Header))
	n, _ := d.r.ReadAt(head, 0)
	if string(head[:n]) == pdf20Header {
		return overwritten{ReaderAt: d.r, at: versionAt, with: shownVersion}
	}
	return d.r
}

// openReader opens d with the PDF reader, through pdfBytes. A crypt filter
// the reader refuses is tried once more through aesLengthFixed, since pdfcpu
// writes an AES-128 filter's /Length in bits where the reader takes bytes,
// and the reader then decrypts the whole file, text and object streams
// included. If it still refuses the file, the first refusal is returned.
func openReader(d document) (*pdf.Reader, error) {
	r, err := pdf.NewReader(pdfBytes(d), d.size)
	if err == nil || !strings.HasPrefix(err.Error(), v4Refusal) {
		return r, err
	}
	if fixed, fixedErr := pdf.NewReader(aesLengthFixed(d), d.size); fixedErr == nil {
		return fixed, nil
	}
	return nil, err
}

// openPDF opens d with the PDF reader the text modes use and checks what each
// of them checks before it reads a page: that the reader opened the file, that
// it will decrypt the file into its own bytes, and that its page tree is safe
// to hand to Reader.Page. It returns the reader, or nil and the diagnosis for
// a file whose text cannot be read. A file encrypted in a way the reader
// decrypts wrongly or refuses, and whose strings selfDecrypting decrypts, gets
// partlyEncryptedPDFReason, which points at outline mode. The reader can
// panic on malformed input, so a caller runs it behind recover().
func openPDF(d document) (r *pdf.Reader, why string) {
	r, err := openReader(d)
	if err != nil {
		return nil, refusedPDFReason(d, err)
	}
	if rc4KeyTooShort(r) {
		return nil, partlyEncryptedPDFReason
	}
	if cyclic := pageTreeReason(r); cyclic != "" {
		return nil, cyclic
	}
	return r, ""
}

// refusedPDFReason is the diagnosis for d, a file the reader refused with
// err: partlyEncryptedPDFReason when the refusal is one selfDecryptable names
// and selfDecrypting then reads the file's strings, and openPDFReason's
// otherwise.
func refusedPDFReason(d document, err error) string {
	if selfDecryptable(err) && selfDecrypts(d) {
		return partlyEncryptedPDFReason
	}
	return openPDFReason(err)
}

// selfDecrypts reports whether selfDecrypting opens d.
func selfDecrypts(d document) bool {
	r, _ := selfDecrypting(d)
	return r != nil
}

// selfDecryptable reports whether err, the reader's refusal of a file, is one
// the standard security handler may still open with the empty password, so
// that selfDecrypting is worth asking: a crypt filter (V=4) the reader does
// not take, and a password it could not match. The second is what a file that
// leaves its metadata unencrypted gives it, Acrobat's "encrypt all contents
// except metadata", since the reader derives the key as if it did not; a file
// that does need a password fails selfDecrypting's check as well.
func selfDecryptable(err error) bool {
	return errors.Is(err, pdf.ErrInvalidPassword) || strings.HasPrefix(err.Error(), v4Refusal)
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
