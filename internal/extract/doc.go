// Package extract provides pure-Go text extraction from PDF, EPUB and plain
// text files with page-based (PDF) or character-based (EPUB/TXT) pagination.
//
// It has no CGO dependencies, so it preserves fully static builds. Scanned or
// image-only documents, and unsupported container formats (DjVu, comic
// archives, proprietary e-book formats), are reported as not extractable with
// an explanatory reason rather than failing the caller.
package extract
