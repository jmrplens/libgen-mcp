package extract

import (
	"context"
	"fmt"
	"strings"
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
		return pdfTextUnreadable, invalidPDFReason(oerr), nil
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
		return Chunk{Format: "pdf", Reason: invalidPDFReason(err)}, nil
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
