package extract

import (
	"context"
	"os"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// pdfOutline reads a PDF's embedded bookmarks best-effort via pdfcpu and returns
// them as a flat, in-order OutlineResult. A PDF without an outline (common for
// scanned or older files) is extractable with no entries, which is not a
// failure. pdfcpu can panic or error on malformed input, so the read is guarded:
// any panic or error becomes an empty outline rather than a crash. A canceled
// ctx yields the context error.
func pdfOutline(ctx context.Context, filePath string) (OutlineResult, error) {
	if err := ctx.Err(); err != nil {
		return OutlineResult{}, err
	}
	f, err := os.Open(filePath)
	if err != nil {
		return OutlineResult{Format: "pdf", Reason: "not a readable PDF: " + err.Error()}, nil
	}
	defer func() { _ = f.Close() }()

	bms, ok := readBookmarks(f)
	if !ok {
		return OutlineResult{
			Format:      "pdf",
			Extractable: true,
			Reason:      "no embedded outline (or it could not be read)",
		}, nil
	}
	if len(bms) == 0 {
		return OutlineResult{
			Format:      "pdf",
			Extractable: true,
			Reason:      "no embedded outline (scanned/older PDFs often have none)",
		}, nil
	}

	var entries []OutlineEntry
	flattenBookmarks(bms, 0, &entries)
	return OutlineResult{Format: "pdf", Extractable: true, Entries: entries}, nil
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
