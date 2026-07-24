// Package extract provides pure-Go text extraction from PDF, EPUB and plain
// text files with page-based (PDF) or character-based (EPUB/TXT) pagination.
//
// It has no CGO dependencies, so it preserves fully static builds. Scanned or
// image-only documents, and unsupported container formats (DjVu, comic
// archives, proprietary e-book formats), are reported as not extractable with
// an explanatory reason rather than failing the caller.
package extract

import (
	"bytes"
	"context"
	"io"
	"os"
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
// fields for EPUB/TXT. HasMore reports whether more content remains and
// NextCursor points at the resume position for the next call. Truncated is true
// only when this chunk was cut short mid-content by max_chars; a clean
// max_pages/offset boundary sets HasMore, not Truncated.
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

// capExceededNote is appended to Chunk.Reason (and Truncated is set) when a TXT
// or EPUB document is at least maxTextFileBytes, so its text is clipped at the
// extraction cap and content beyond it is silently unavailable. This is an
// honest signal only; seek-based streaming past the cap is not implemented.
const capExceededNote = "document exceeds the 8 MiB extraction cap; text beyond it is not available"

// sniffLen is how many leading bytes are read to identify a file by content. The
// signatures below all live in the first few bytes; 512 is generous and cheap.
const sniffLen = 512

// UnrecognizedReason explains why a file could not be dispatched to an extractor,
// naming its extension when it has one. It is shared by every entry point so the
// three modes report the same thing about the same file.
func UnrecognizedReason(ext string) string {
	if ext == "" {
		return "unrecognized content: the file carries no extension and its bytes match no supported format"
	}
	return "unsupported file extension " + ext + " and its bytes match no supported format (unrecognized)"
}

// sniffFormat reports the format of a file from its leading bytes, or "" when it
// matches nothing supported.
func sniffFormat(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, sniffLen)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	switch {
	case bytes.HasPrefix(head, []byte("%PDF-")):
		return "pdf"
	// An EPUB is a zip whose first entry is an uncompressed "mimetype" file naming
	// the format, which is what distinguishes it from any other zip container.
	case bytes.HasPrefix(head, []byte("PK\x03\x04")) && bytes.Contains(head, []byte("application/epub+zip")):
		return "epub"
	}
	// Plain text is deliberately not sniffed: almost anything decodes as text,
	// including a mirror's HTML error page, and "extracting" one of those would be
	// worse than reporting the file as unrecognized.
	return ""
}

// appendNote joins an existing reason with note using "; " as the separator,
// or returns note alone when reason is empty.
func appendNote(reason, note string) string {
	if reason == "" {
		return note
	}
	return reason + "; " + note
}

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
		// The name did not identify the file, so its bytes must: anything fetched by
		// content address, or from a CDN that announces no filename, arrives with no
		// extension, and a real book would otherwise be reported as unsupported.
		switch sniffFormat(path) {
		case "pdf":
			return extractPDF(ctx, path, r)
		case "epub":
			return extractEPUB(ctx, path, r)
		}
		return Chunk{Reason: UnrecognizedReason(ext)}, nil
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
