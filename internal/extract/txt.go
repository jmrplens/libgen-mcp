package extract

import (
	"context"
	"fmt"
	"io"
	"os"
)

// cannotOpenTextReason is the diagnosis for a plain-text file that cannot be
// opened. Shared so every read mode words it the same way.
func cannotOpenTextReason(err error) string {
	return fmt.Sprintf("cannot open text file: %v", err)
}

// cannotReadTextReason is the diagnosis for a plain-text file that opens but
// cannot be read. Shared so every read mode words it the same way.
func cannotReadTextReason(err error) string {
	return fmt.Sprintf("cannot read text file: %v", err)
}

// extractTXT reads a plain-text file (bounded by maxTextFileBytes) and returns
// a character-paginated Chunk. A read failure yields a not-extractable Chunk.
func extractTXT(ctx context.Context, path string, r Req) (Chunk, error) {
	if err := ctx.Err(); err != nil {
		return Chunk{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Chunk{Format: "txt", Reason: cannotOpenTextReason(err)}, nil
	}
	defer func() { _ = f.Close() }()

	// Read one byte past the cap so a saturated LimitReader is detectable, then
	// clip back to the cap before paginating.
	data, err := io.ReadAll(io.LimitReader(f, maxTextFileBytes+1))
	if err != nil {
		return Chunk{Format: "txt", Reason: cannotReadTextReason(err)}, nil
	}
	truncated := len(data) > maxTextFileBytes
	if truncated {
		data = data[:maxTextFileBytes]
	}
	c := paginateChars(string(data), "txt", r)
	if truncated {
		c.Truncated = true
		c.Reason = appendNote(c.Reason, capExceededNote)
	}
	return c, nil
}
