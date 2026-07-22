// Package extract provides pure-Go text extraction from PDF, EPUB and plain
// text files with page-based (PDF) or character-based (EPUB/TXT) pagination.
//
// It has no CGO dependencies, so it preserves fully static builds. Scanned or
// image-only documents, and unsupported container formats (DjVu, comic
// archives, proprietary e-book formats), are reported as not extractable with
// an explanatory reason rather than failing the caller.
package extract

import (
	"context"
	"path/filepath"
	"strings"
)

// Cursor marks where a follow-up Extract call should resume. Page is used by
// page-paginated formats (PDF); Char is used by character-paginated formats
// (EPUB, TXT).
type Cursor struct {
	Page int
	Char int
}

// Req describes a single extraction request. Zero or negative fields fall back
// to internal defaults. StartPage and MaxPages apply to page-paginated formats;
// Offset and MaxChars apply to character-paginated formats. MaxChars also caps
// the accumulated text of page-paginated formats.
type Req struct {
	StartPage int
	MaxPages  int
	Offset    int
	MaxChars  int
}

// Chunk is the result of an extraction. When Extractable is false, Reason
// explains why and Text is empty. Page fields are populated for PDFs; Char
// fields for EPUB/TXT. HasMore reports whether more content remains, Truncated
// reports whether this chunk was cut short by a limit, and NextCursor points at
// the resume position for the next call.
type Chunk struct {
	Text        string
	Format      string
	Extractable bool
	Reason      string
	PageStart   int
	PageEnd     int
	TotalPages  int
	CharStart   int
	CharEnd     int
	HasMore     bool
	Truncated   bool
	NextCursor  Cursor
}

// Internal pagination defaults, applied when the corresponding Req field is
// less than or equal to zero.
const (
	defaultMaxPages  = 5
	defaultMaxChars  = 6000
	defaultStartPage = 1
	maxTextFileBytes = 8 << 20 // 8 MiB cap for plain-text reads.
)

// Extract reads path and returns a paginated Chunk of its text. It dispatches
// on the lowercased file extension: PDF, EPUB and TXT are extracted; DjVu,
// comic archives and proprietary e-book formats are reported as unsupported.
// A canceled ctx yields the context error.
func Extract(ctx context.Context, path string, r Req) (Chunk, error) {
	if err := ctx.Err(); err != nil {
		return Chunk{}, err
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf":
		return extractPDF(ctx, path, r)
	case ".epub":
		return extractEPUB(ctx, path, r)
	case ".txt":
		return extractTXT(ctx, path, r)
	case ".djvu", ".cbr", ".cbz", ".mobi", ".azw", ".azw3":
		return Chunk{
			Format: strings.TrimPrefix(ext, "."),
			Reason: "unsupported format " + ext + ": text extraction is not available (comic/scanned/proprietary container)",
		}, nil
	default:
		return Chunk{
			Reason: "unsupported file extension " + ext,
		}, nil
	}
}

// paginateChars applies character-based pagination to fullText for the given
// format. It operates on runes so a multi-byte UTF-8 character is never split,
// applies the MaxChars default when needed, and fills the Char/HasMore/
// Truncated/NextCursor fields of the returned Chunk.
func paginateChars(fullText, format string, r Req) Chunk {
	maxChars := r.MaxChars
	if maxChars <= 0 {
		maxChars = defaultMaxChars
	}
	offset := max(r.Offset, 0)

	runes := []rune(fullText)
	total := len(runes)
	start := min(offset, total)
	end := min(start+maxChars, total)

	c := Chunk{
		Text:        string(runes[start:end]),
		Format:      format,
		Extractable: true,
		CharStart:   start,
		CharEnd:     end,
		HasMore:     end < total,
		Truncated:   end < total,
	}
	c.NextCursor.Char = end
	return c
}
