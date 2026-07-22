package extract

import (
	"context"
	"fmt"
	"io"
	"os"
)

// extractTXT reads a plain-text file (bounded by maxTextFileBytes) and returns
// a character-paginated Chunk. A read failure yields a not-extractable Chunk.
func extractTXT(ctx context.Context, path string, r Req) (Chunk, error) {
	if err := ctx.Err(); err != nil {
		return Chunk{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Chunk{Format: "txt", Reason: fmt.Sprintf("cannot open text file: %v", err)}, nil
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, maxTextFileBytes))
	if err != nil {
		return Chunk{Format: "txt", Reason: fmt.Sprintf("cannot read text file: %v", err)}, nil
	}
	return paginateChars(string(data), "txt", r), nil
}
