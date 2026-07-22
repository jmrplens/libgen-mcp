package extract

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/ledongthuc/pdf"
)

// Match is a single query hit within a document. Page is the 1-based PDF page
// the hit is on (0 for EPUB/TXT). CharOffset is the rune offset of the hit into
// the page text (PDF) or into the whole document (EPUB/TXT). Snippet is a
// one-line context window around the hit.
type Match struct {
	Page       int    `json:"page,omitempty"`
	CharOffset int    `json:"char_offset"`
	Snippet    string `json:"snippet"`
}

// SearchOpts tunes a Search. MaxMatches caps how many matches a single call
// returns (default 10). StartMatch is the 0-based index into the full match
// list to resume from. SnippetChars is the total snippet width in runes
// (default 160). CaseSensitive switches off the default case-insensitive match.
type SearchOpts struct {
	MaxMatches    int
	StartMatch    int
	SnippetChars  int
	CaseSensitive bool
}

// SearchResult is the outcome of a Search. When Extractable is false, Reason
// explains why and Matches is empty. TotalMatches is the number of hits across
// the whole document; Matches holds only the requested window. HasMore reports
// whether hits remain past this window, and NextMatch is the index to resume
// from when HasMore is true.
type SearchResult struct {
	Format       string  `json:"format,omitempty"`
	Extractable  bool    `json:"extractable"`
	Reason       string  `json:"reason,omitempty"`
	Matches      []Match `json:"matches,omitempty"`
	TotalMatches int     `json:"total_matches"`
	HasMore      bool    `json:"has_more"`
	NextMatch    int     `json:"next_match,omitempty"`
}

// Search defaults, applied when the corresponding SearchOpts field is less than
// or equal to zero.
const (
	defaultMaxMatches   = 10
	defaultSnippetChars = 160
	noTextLayerReason   = "no extractable text layer (likely a scanned or image-only PDF); OCR is not supported"
)

// snippetReplacer collapses line breaks and tabs to single spaces so a snippet
// renders on one line.
var snippetReplacer = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ")

// Search finds occurrences of query within the document at path and returns
// them as Matches with one-line context snippets. It dispatches on the
// lowercased file extension: PDF, EPUB and TXT are searched; DjVu, comic
// archives and proprietary e-book formats are reported as not extractable. A
// scanned or text-free PDF is likewise reported as not extractable. A canceled
// ctx yields the context error. An empty or whitespace-only query yields zero
// matches without an error.
func Search(ctx context.Context, path, query string, o SearchOpts) (SearchResult, error) {
	if err := ctx.Err(); err != nil {
		return SearchResult{}, err
	}
	if o.MaxMatches <= 0 {
		o.MaxMatches = defaultMaxMatches
	}
	if o.SnippetChars <= 0 {
		o.SnippetChars = defaultSnippetChars
	}
	if o.StartMatch < 0 {
		o.StartMatch = 0
	}

	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf":
		return searchPDF(ctx, path, query, o)
	case ".epub":
		return searchEPUB(ctx, path, query, o)
	case ".txt":
		return searchTXT(ctx, path, query, o)
	case ".djvu", ".cbr", ".cbz", ".mobi", ".azw", ".azw3":
		return SearchResult{
			Format: strings.TrimPrefix(ext, "."),
			Reason: "unsupported format " + ext + ": text extraction is not available (comic/scanned/proprietary container)",
		}, nil
	default:
		return SearchResult{Reason: "unsupported file extension " + ext}, nil
	}
}

// searchPDF searches a PDF page by page. The ledongthuc/pdf reader can panic on
// malformed or encrypted input, so the read is guarded by recover(): a panic
// becomes a not-extractable result rather than a crash.
func searchPDF(ctx context.Context, path, query string, o SearchOpts) (result SearchResult, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			result = SearchResult{Format: "pdf", Reason: fmt.Sprintf("cannot read PDF (malformed or encrypted): %v", rec)}
			err = nil
		}
	}()
	return scanPDFMatches(ctx, path, query, o)
}

// scanPDFMatches opens the PDF, collects every match across all pages (recording
// each hit's page and in-page rune offset), and windows the result. If no page
// yields any text, it reports the scanned/no-text-layer condition.
func scanPDFMatches(ctx context.Context, path, query string, o SearchOpts) (SearchResult, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return SearchResult{Format: "pdf", Reason: fmt.Sprintf("not a valid PDF: %v", err)}, nil
	}
	defer func() { _ = f.Close() }()

	var all []Match
	anyText := false
	total := r.NumPage()
	for i := 1; i <= total; i++ {
		if e := ctx.Err(); e != nil {
			return SearchResult{}, e
		}
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		text, _ := p.GetPlainText(nil)
		if strings.TrimSpace(text) != "" {
			anyText = true
		}
		all = append(all, findMatches(text, query, o.CaseSensitive, i, o.SnippetChars)...)
	}

	if !anyText {
		return SearchResult{Format: "pdf", Reason: noTextLayerReason}, nil
	}
	res := windowMatches(all, o)
	res.Format = "pdf"
	res.Extractable = true
	return res, nil
}

// searchTXT searches a plain-text file over its full rune slice.
func searchTXT(ctx context.Context, path, query string, o SearchOpts) (SearchResult, error) {
	if e := ctx.Err(); e != nil {
		return SearchResult{}, e
	}
	f, err := os.Open(path)
	if err != nil {
		return SearchResult{Format: "txt", Reason: fmt.Sprintf("cannot open text file: %v", err)}, nil
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, maxTextFileBytes))
	if err != nil {
		return SearchResult{Format: "txt", Reason: fmt.Sprintf("cannot read text file: %v", err)}, nil
	}
	all := findMatches(string(data), query, o.CaseSensitive, 0, o.SnippetChars)
	res := windowMatches(all, o)
	res.Format = "txt"
	res.Extractable = true
	return res, nil
}

// searchEPUB searches an EPUB over the concatenated plain text of its spine.
func searchEPUB(ctx context.Context, path, query string, o SearchOpts) (SearchResult, error) {
	if e := ctx.Err(); e != nil {
		return SearchResult{}, e
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return SearchResult{Format: "epub", Reason: fmt.Sprintf("cannot open EPUB archive: %v", err)}, nil
	}
	defer func() { _ = zr.Close() }()

	full, err := readEPUBText(ctx, zr)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return SearchResult{}, err
		}
		return SearchResult{Format: "epub", Reason: "not a readable EPUB: " + err.Error()}, nil
	}
	all := findMatches(full, query, o.CaseSensitive, 0, o.SnippetChars)
	res := windowMatches(all, o)
	res.Format = "epub"
	res.Extractable = true
	return res, nil
}

// findMatches scans text for every occurrence of query and returns a Match per
// hit, tagged with the given page. Matching is case-insensitive unless
// caseSensitive is set. An empty query yields no matches. Offsets are rune
// indices into text.
func findMatches(text, query string, caseSensitive bool, page, snippetChars int) []Match {
	runes := []rune(text)
	needle := []rune(query)
	// A query that is empty or only whitespace matches nothing, matching the
	// documented behavior (a run of spaces is not a meaningful search term).
	if strings.TrimSpace(query) == "" {
		return nil
	}
	hay := runes
	nd := needle
	if !caseSensitive {
		hay = lowerRunes(runes)
		nd = lowerRunes(needle)
	}

	var matches []Match
	m := len(nd)
	for i := 0; i+m <= len(hay); i++ {
		if !matchAt(hay[i:], nd) {
			continue
		}
		matches = append(matches, Match{
			Page:       page,
			CharOffset: i,
			Snippet:    buildSnippet(runes, i, m, snippetChars),
		})
		i += m - 1
	}
	return matches
}

// matchAt reports whether the run of runes starting at hay matches needle. The
// caller guarantees hay is at least as long as needle.
func matchAt(hay, needle []rune) bool {
	for i := range needle {
		if hay[i] != needle[i] {
			return false
		}
	}
	return true
}

// lowerRunes returns a lowercased copy of rs, preserving a one-to-one rune
// mapping so offsets stay aligned with the original.
func lowerRunes(rs []rune) []rune {
	out := make([]rune, len(rs))
	for i, r := range rs {
		out[i] = unicode.ToLower(r)
	}
	return out
}

// buildSnippet returns a one-line context window of roughly snippetChars runes
// centered on the match at matchStart (matchLen runes long), operating on runes
// so a UTF-8 character is never split.
func buildSnippet(runes []rune, matchStart, matchLen, snippetChars int) string {
	half := snippetChars / 2
	start := max(matchStart-half, 0)
	end := min(matchStart+matchLen+half, len(runes))
	return strings.TrimSpace(snippetReplacer.Replace(string(runes[start:end])))
}

// windowMatches applies StartMatch/MaxMatches windowing to the full match list
// and fills TotalMatches, HasMore and NextMatch.
func windowMatches(all []Match, o SearchOpts) SearchResult {
	total := len(all)
	start := min(o.StartMatch, total)
	end := min(start+o.MaxMatches, total)

	res := SearchResult{
		Matches:      all[start:end],
		TotalMatches: total,
		HasMore:      end < total,
	}
	if res.HasMore {
		res.NextMatch = end
	}
	return res
}
