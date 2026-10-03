// document.go is the one way any reader in this package reaches a file's bytes.

package extract

import (
	"fmt"
	"io"
	"os"
)

// document is an open file as the readers in this package see it: a name to
// dispatch on and a source of bytes to read at any offset.
//
// It carries no path to open, deliberately. The read tool's containment is
// decided on a descriptor (internal/pathguard.OpenReadableFile), and a reader
// that reopened the file by its name would read whatever the name resolves to
// now, which a local principal writing in an allowed root can change after the
// check. Every reader here — the PDF parser, the zip reader, pdfcpu, the text
// leg and the format sniffer — reads from r, so nothing can reopen by name.
type document struct {
	// name is the file's name, used for its extension and in a diagnosis only.
	name string
	// r is the file's content. An *os.File's ReadAt is safe for concurrent use
	// and does not move a shared offset, so each reader gets its own view.
	r io.ReaderAt
	// size is the file's size when the document was made.
	size int64
}

// newDocument wraps an open file, reading its size from the descriptor.
func newDocument(f *os.File) (document, error) {
	info, err := f.Stat()
	if err != nil {
		return document{}, fmt.Errorf("stat %s: %w", f.Name(), err)
	}
	return document{name: f.Name(), r: f, size: info.Size()}, nil
}

// section returns a fresh reader over the whole document, positioned at its
// start, for readers that want a stream or a seeker rather than ReadAt.
func (d document) section() *io.SectionReader {
	return io.NewSectionReader(d.r, 0, d.size)
}
