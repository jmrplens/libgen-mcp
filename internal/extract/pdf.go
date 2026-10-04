package extract

import (
	"context"
	"errors"
	"fmt"
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

// encryptedPDFReason is the diagnosis for a PDF the reader will not open
// because of how it is encrypted. ledongthuc/pdf decrypts the standard security
// handler's RC4 (V=1 and V=2) and its AES-128 crypt filter (V=4 with AESV2), and
// nothing else: not AES-256 (V=5), not RC4 under crypt filters (V=4 with V2),
// and not a certificate-based handler. Such a file is valid, so calling it
// invalid would send the caller looking for a better copy of a file that is
// fine. Shared so every read mode words it the same way. One literal rather
// than a concatenation, like lockedPDFReason, for the reason
// noPDFOutlineReason gives.
const encryptedPDFReason = "cannot read PDF: it is encrypted in a way this reader cannot decrypt (AES-256, RC4 under crypt filters, or a certificate-based security handler), so neither its text nor its table of contents can be read"

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

// openPDFReason is the diagnosis for a file the PDF reader would not open.
//
// The reader reports a password it could not match with a sentinel, and the
// rest only in its error's words. Every refusal of an encryption it does not
// implement names the encryption ("unsupported PDF: encryption version V=5",
// "256-bit encryption key" and the rest), and a file that does not even start
// as a PDF is "not a PDF file: invalid header"; anything else opened as a PDF
// and broke. The fixtures pin the matching: an AES-256 and an RC4 V=4 file are
// held to encryptedPDFReason and a truncated one to damagedPDFReason, so a
// release of the reader that words it differently fails a test rather than
// changing a diagnosis in silence.
func openPDFReason(err error) string {
	msg := err.Error()
	switch {
	case errors.Is(err, pdf.ErrInvalidPassword):
		return lockedPDFReason
	case strings.Contains(msg, "encryption"):
		return encryptedPDFReason
	case strings.HasPrefix(msg, "not a PDF file: invalid header"):
		return invalidPDFReason(err)
	default:
		return damagedPDFReason(err)
	}
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
	r, oerr := pdf.NewReader(d.r, d.size)
	if oerr != nil {
		return pdfTextUnreadable, openPDFReason(oerr), nil
	}

	if cyclic := pageTreeReason(r); cyclic != "" {
		return pdfTextUnreadable, cyclic, nil
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
	if total <= 0 || budget <= 0 {
		return nil
	}
	stride := 1
	if total > budget {
		stride = total / budget
	}
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
	r, err := pdf.NewReader(d.r, d.size)
	if err != nil {
		return Chunk{Format: "pdf", Reason: openPDFReason(err)}, nil
	}

	if cyclic := pageTreeReason(r); cyclic != "" {
		return Chunk{Format: "pdf", Reason: cyclic}, nil
	}

	total := r.NumPage()
	if total > 0 && pr.start > total {
		return Chunk{
			Format:     "pdf",
			TotalPages: total,
			Reason:     fmt.Sprintf("start page %d is beyond the document's last page (%d pages)", pr.start, total),
		}, nil
	}
	if pr.last <= 0 || pr.last > total {
		pr.last = total
	}

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
// where the caller's window ends, whether or not the document does.
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
		if maxChars > 0 && charCount >= maxChars {
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
