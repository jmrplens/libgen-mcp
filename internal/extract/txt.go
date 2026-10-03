package extract

import (
	"context"
	"fmt"
	"io"
)

// cannotReadTextReason is the diagnosis for a plain-text file that opens but
// cannot be read. Shared so every read mode words it the same way.
func cannotReadTextReason(err error) string {
	return fmt.Sprintf("cannot read text file: %v", err)
}

// extractTXT reads a plain-text document (bounded by maxTextFileBytes) and
// returns a character-paginated Chunk. A read failure yields a not-extractable
// Chunk.
func extractTXT(ctx context.Context, d document, r Req) (Chunk, error) {
	if err := ctx.Err(); err != nil {
		return Chunk{}, err
	}
	// Read one byte past the cap so a saturated LimitReader is detectable, then
	// clip back to the cap before paginating.
	data, err := io.ReadAll(io.LimitReader(d.section(), maxTextFileBytes+1))
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
