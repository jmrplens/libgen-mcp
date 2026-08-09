package extract

import (
	"context"
	"os"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// noPDFOutlineReason is reported for a PDF that has a readable text layer but no
// usable bookmarks — the document really is readable, it simply carries no table
// of contents. It deliberately covers "none present" and "present but
// unreadable" together: pdfcpu cannot tell the two apart, and inventing a
// distinction it cannot support would only mislead.
const noPDFOutlineReason = "no embedded table of contents (none found, or it could not be read); " +
	"the text layer is readable, so read the text sequentially or use find"

// pdfOutline reads a PDF's embedded bookmarks best-effort via pdfcpu and returns
// them as a flat, in-order OutlineResult. pdfcpu can panic or error on malformed
// input, so the read is guarded: any panic or error becomes an absent outline
// rather than a crash. A canceled ctx yields the context error.
//
// When no bookmarks come back, the file is not simply declared TOC-less: it is
// probed for a text layer first, so a scanned PDF gets the same diagnosis here
// as it gets from Extract and Search. Reporting "no table of contents" for a
// document that has no text at all is worse than an error — it tells the caller
// the pages are readable and costs it another round trip to find out they are
// not.
func pdfOutline(ctx context.Context, filePath string) (OutlineResult, error) {
	if err := ctx.Err(); err != nil {
		return OutlineResult{}, err
	}
	entries := pdfBookmarkEntries(filePath)
	if len(entries) > 0 {
		return OutlineResult{Format: "pdf", Extractable: true, Entries: entries}, nil
	}
	return pdfNoOutlineResult(ctx, filePath)
}

// pdfBookmarkEntries opens the PDF and flattens whatever bookmarks pdfcpu can
// read from it. Every failure — a file that will not open, a bookmark read that
// errors or panics — yields no entries rather than a diagnosis of its own: the
// text-layer probe that follows produces one, in the same words the text path
// would use for the same file.
func pdfBookmarkEntries(filePath string) []OutlineEntry {
	f, err := os.Open(filePath)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	bms, ok := readBookmarks(f)
	if !ok || len(bms) == 0 {
		return nil
	}
	var entries []OutlineEntry
	flattenBookmarks(bms, 0, &entries)
	return entries
}

// pdfNoOutlineResult decides what to report for a PDF that yielded no bookmarks,
// by asking the same question the text path asks: does this file have a text
// layer? A readable one is extractable with no entries (a valid document without
// a TOC); a text-free one is reported as scanned; an unreadable one carries the
// reader's own diagnosis.
func pdfNoOutlineResult(ctx context.Context, filePath string) (OutlineResult, error) {
	state, reason, err := probePDFTextLayer(ctx, filePath)
	if err != nil {
		return OutlineResult{}, err
	}
	switch state {
	case pdfTextAbsent:
		return OutlineResult{Format: "pdf", Reason: noTextLayerReason}, nil
	case pdfTextUnreadable:
		return OutlineResult{Format: "pdf", Reason: reason}, nil
	default:
		return OutlineResult{Format: "pdf", Extractable: true, Reason: noPDFOutlineReason}, nil
	}
}

// readBookmarks calls pdfcpu's bookmark reader inside a recover()-guarded
// closure so a panic on malformed input becomes ok=false ("no outline") rather
// than a crash. A non-nil pdfcpu error is likewise reported as ok=false.
func readBookmarks(f *os.File) (bms []pdfcpu.Bookmark, ok bool) {
	defer func() {
		if recover() != nil {
			bms, ok = nil, false
		}
	}()
	got, err := api.Bookmarks(f, model.NewDefaultConfiguration())
	if err != nil {
		return nil, false
	}
	return got, true
}

// flattenBookmarks appends one OutlineEntry per bookmark at the given level,
// recursing into Kids at level+1 to flatten the tree in document order. Page is
// the bookmark's 1-based PageFrom.
func flattenBookmarks(bms []pdfcpu.Bookmark, level int, out *[]OutlineEntry) {
	for _, bm := range bms {
		if title := strings.TrimSpace(bm.Title); title != "" {
			*out = append(*out, OutlineEntry{Title: title, Level: level, Page: bm.PageFrom})
		}
		flattenBookmarks(bm.Kids, level+1, out)
	}
}
