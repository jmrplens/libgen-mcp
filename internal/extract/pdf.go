package extract

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

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
func extractPDF(ctx context.Context, path string, r Req) (chunk Chunk, err error) {
	if e := ctx.Err(); e != nil {
		return Chunk{}, e
	}
	startPage := r.StartPage
	if startPage <= 0 {
		startPage = defaultStartPage
	}
	maxPages := r.MaxPages
	if maxPages <= 0 {
		maxPages = defaultMaxPages
	}
	maxChars := r.MaxChars
	if maxChars <= 0 {
		maxChars = defaultMaxChars
	}

	defer func() {
		if rec := recover(); rec != nil {
			chunk = Chunk{Format: "pdf", Reason: fmt.Sprintf("cannot read PDF (malformed or encrypted): %v", rec)}
			err = nil
		}
	}()

	return readPDFPages(ctx, path, startPage, maxPages, maxChars)
}

// readPDFPages opens the PDF, scans the requested page range and assembles the
// final Chunk, including no-text-layer detection.
func readPDFPages(ctx context.Context, path string, startPage, maxPages, maxChars int) (Chunk, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return Chunk{Format: "pdf", Reason: fmt.Sprintf("not a valid PDF: %v", err)}, nil
	}
	defer func() { _ = f.Close() }()

	total := r.NumPage()
	if total > 0 && startPage > total {
		return Chunk{
			Format:     "pdf",
			TotalPages: total,
			Reason:     fmt.Sprintf("start page %d is beyond the document's last page (%d pages)", startPage, total),
		}, nil
	}

	scan, err := scanPDFPages(ctx, r, total, startPage, maxPages, maxChars)
	if err != nil {
		return Chunk{}, err
	}

	if strings.TrimSpace(scan.text) == "" {
		return Chunk{
			Format:     "pdf",
			TotalPages: total,
			Reason:     "no extractable text layer (likely a scanned or image-only PDF); OCR is not supported",
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

// scanPDFPages iterates pages from startPage, accumulating plain text without
// ever splitting a page. It stops before a page when MaxChars is already
// reached (marking Truncated) or after MaxPages pages have been read, and
// checks ctx between pages.
func scanPDFPages(ctx context.Context, r *pdf.Reader, total, startPage, maxPages, maxChars int) (pdfScan, error) {
	var sb strings.Builder
	var s pdfScan
	pagesRead := 0
	charCount := 0

	for i := startPage; i <= total; i++ {
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
			s.hasMore = i < total
			break
		}
	}

	s.text = sb.String()
	return s, nil
}
