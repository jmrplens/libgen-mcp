package extract

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/net/html"
)

// OutlineEntry is a single table-of-contents entry. Level is 0 for a top-level
// entry and increases by one per nesting depth. Page is the 1-based PDF page (0
// for EPUB); CharOffset is reserved for EPUB precise jumps (0 for now).
type OutlineEntry struct {
	Title      string `json:"title"`
	Level      int    `json:"level"`
	Page       int    `json:"page,omitempty"`
	CharOffset int    `json:"char_offset,omitempty"`
}

// OutlineResult is the outcome of an Outline call. When Extractable is false,
// Reason explains why and Entries is empty. A supported document with no table
// of contents is Extractable with empty Entries, which is not an error.
type OutlineResult struct {
	Format      string         `json:"format,omitempty"`
	Extractable bool           `json:"extractable"`
	Reason      string         `json:"reason,omitempty"`
	Entries     []OutlineEntry `json:"entries,omitempty"`
}

// ncxDoc models an EPUB2 NCX document's navMap.
type ncxDoc struct {
	NavPoints []ncxNavPoint `xml:"navMap>navPoint"`
}

// ncxNavPoint models a single (recursively nestable) NCX navigation point.
type ncxNavPoint struct {
	Label struct {
		Text string `xml:"text"`
	} `xml:"navLabel"`
	Points []ncxNavPoint `xml:"navPoint"`
}

// Outline reads path and returns its table of contents. It dispatches on the
// lowercased file extension: EPUB outlines are parsed natively; PDF returns a
// placeholder pending Task B2; TXT has no outline; DjVu, comic archives and
// proprietary e-book formats are reported as unsupported. A canceled ctx yields
// the context error.
func Outline(ctx context.Context, filePath string) (OutlineResult, error) {
	if err := ctx.Err(); err != nil {
		return OutlineResult{}, err
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".epub":
		return epubOutline(ctx, filePath)
	case ".pdf":
		return OutlineResult{
			Format:      "pdf",
			Extractable: true,
			Reason:      "PDF outline extraction is added in a later step",
		}, nil
	case ".txt":
		return OutlineResult{Format: "txt", Extractable: true}, nil
	case ".djvu", ".cbr", ".cbz", ".mobi", ".azw", ".azw3":
		return OutlineResult{
			Format: strings.TrimPrefix(ext, "."),
			Reason: "unsupported format " + ext + ": outline extraction is not available (comic/scanned/proprietary container)",
		}, nil
	default:
		return OutlineResult{Reason: "unsupported file extension " + ext}, nil
	}
}

// epubOutline opens the EPUB archive and resolves its navigation. A canceled
// ctx yields the context error; a structurally broken archive is reported as
// not extractable; an EPUB with no navigation is extractable with no entries.
func epubOutline(ctx context.Context, filePath string) (OutlineResult, error) {
	if err := ctx.Err(); err != nil {
		return OutlineResult{}, err
	}
	zr, err := zip.OpenReader(filePath)
	if err != nil {
		return OutlineResult{Format: "epub", Reason: "not a readable EPUB: " + err.Error()}, nil
	}
	defer func() { _ = zr.Close() }()

	entries, err := readEPUBOutline(ctx, zr)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return OutlineResult{}, err
		}
		return OutlineResult{Format: "epub", Reason: "not a readable EPUB: " + err.Error()}, nil
	}
	return OutlineResult{Format: "epub", Extractable: true, Entries: entries}, nil
}

// readEPUBOutline resolves the OPF, then returns the EPUB3 nav entries if
// present, else the EPUB2 NCX entries, else nil (a valid EPUB with no TOC). A
// non-nil error is either a structural OPF failure or ctx cancellation.
func readEPUBOutline(ctx context.Context, zr *zip.ReadCloser) ([]OutlineEntry, error) {
	opf, err := opfPath(zr)
	if err != nil {
		return nil, err
	}
	pkg, err := parseOPF(zr, opf)
	if err != nil {
		return nil, err
	}
	baseDir := path.Dir(opf)

	entries, err := navEntries(ctx, zr, pkg, baseDir)
	if err != nil {
		return nil, err
	}
	if len(entries) > 0 {
		return entries, nil
	}
	return ncxEntries(ctx, zr, pkg, baseDir)
}

// navEntries parses the EPUB3 navigation document (manifest item with the "nav"
// property). A missing or malformed nav document yields nil entries; only ctx
// cancellation yields a non-nil error.
func navEntries(ctx context.Context, zr *zip.ReadCloser, pkg opfPackage, baseDir string) ([]OutlineEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	href := navHref(pkg)
	if href == "" {
		return nil, nil
	}
	data := entryBytes(zr, path.Join(baseDir, href))
	if data == nil {
		return nil, nil
	}
	// html.Parse only errors on a failing reader; a bytes.Reader never fails.
	doc, _ := html.Parse(bytes.NewReader(data))
	nav := findTocNav(doc)
	if nav == nil {
		return nil, nil
	}
	ol := firstOL(nav)
	if ol == nil {
		return nil, nil
	}
	var entries []OutlineEntry
	if werr := walkOL(ctx, ol, 0, &entries); werr != nil {
		return nil, werr
	}
	return entries, nil
}

// entryBytes reads the named archive entry, returning nil when it is absent so
// a missing navigation document reads as "no outline" rather than an error.
func entryBytes(zr *zip.ReadCloser, name string) []byte {
	data, _, err := readZipEntry(zr, name)
	if err != nil {
		return nil
	}
	return data
}

// navHref returns the href of the manifest item whose properties include "nav",
// or "" when no such item exists.
func navHref(pkg opfPackage) string {
	for _, it := range pkg.Items {
		if strings.Contains(it.Properties, "nav") {
			return it.Href
		}
	}
	return ""
}

// walkOL appends one OutlineEntry per <li> in ol at the given level, recursing
// into any nested <ol> at level+1 to flatten the tree in document order.
func walkOL(ctx context.Context, ol *html.Node, level int, out *[]OutlineEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for li := ol.FirstChild; li != nil; li = li.NextSibling {
		if li.Type != html.ElementNode || li.Data != "li" {
			continue
		}
		if err := appendLI(ctx, li, level, out); err != nil {
			return err
		}
	}
	return nil
}

// appendLI appends the <li>'s own title (if any) then recurses into its nested
// <ol> children at level+1.
func appendLI(ctx context.Context, li *html.Node, level int, out *[]OutlineEntry) error {
	if title := liTitle(li); title != "" {
		*out = append(*out, OutlineEntry{Title: title, Level: level})
	}
	for child := li.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode || child.Data != "ol" {
			continue
		}
		if err := walkOL(ctx, child, level+1, out); err != nil {
			return err
		}
	}
	return nil
}

// liTitle returns the trimmed text of the first <a> or <span> child of li, which
// is the entry's label; nested list content is ignored because it lives under
// the li's <ol> children, not its <a>/<span>.
func liTitle(li *html.Node) string {
	for c := li.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && (c.Data == "a" || c.Data == "span") {
			return strings.TrimSpace(nodeText(c))
		}
	}
	return ""
}

// nodeText returns the concatenated text of n and all its descendants.
func nodeText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			sb.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

// findTocNav returns the first <nav epub:type="toc"> in the document, or the
// first <nav> of any kind if none is explicitly the TOC, or nil when the
// document has no <nav> element.
func findTocNav(doc *html.Node) *html.Node {
	var first, found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "nav" {
			if first == nil {
				first = n
			}
			if navIsToc(n) {
				found = n
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if found != nil {
		return found
	}
	return first
}

// navIsToc reports whether n carries epub:type="toc" (parsed either as a
// namespaced attribute or a literal "epub:type" key, depending on the parser).
func navIsToc(n *html.Node) bool {
	for _, a := range n.Attr {
		key := a.Key
		if a.Namespace != "" {
			key = a.Namespace + ":" + a.Key
		}
		if key == "epub:type" && strings.Contains(a.Val, "toc") {
			return true
		}
	}
	return false
}

// firstOL returns the first <ol> element found in a depth-first walk of n's
// descendants, or nil when there is none.
func firstOL(n *html.Node) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "ol" {
			return c
		}
		if found := firstOL(c); found != nil {
			return found
		}
	}
	return nil
}

// ncxEntries parses the EPUB2 NCX navMap into a flat, in-order slice. A missing
// or malformed NCX yields nil entries; only ctx cancellation yields a non-nil
// error.
func ncxEntries(ctx context.Context, zr *zip.ReadCloser, pkg opfPackage, baseDir string) ([]OutlineEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	href := ncxHref(pkg)
	if href == "" {
		return nil, nil
	}
	data := entryBytes(zr, path.Join(baseDir, href))
	if data == nil {
		return nil, nil
	}
	// A malformed NCX still yields whatever navPoints parsed; ignore the error.
	var doc ncxDoc
	_ = xml.Unmarshal(data, &doc)
	var entries []OutlineEntry
	if ferr := flattenNCX(ctx, doc.NavPoints, 0, &entries); ferr != nil {
		return nil, ferr
	}
	return entries, nil
}

// ncxHref returns the NCX document's href: the manifest item with the NCX
// media-type, or failing that the item referenced by the spine's toc id.
func ncxHref(pkg opfPackage) string {
	for _, it := range pkg.Items {
		if it.MediaType == "application/x-dtbncx+xml" {
			return it.Href
		}
	}
	if pkg.Spine.TOC == "" {
		return ""
	}
	for _, it := range pkg.Items {
		if it.ID == pkg.Spine.TOC {
			return it.Href
		}
	}
	return ""
}

// flattenNCX appends one OutlineEntry per navPoint at the given level, recursing
// into nested navPoints at level+1 to flatten the tree in document order.
func flattenNCX(ctx context.Context, points []ncxNavPoint, level int, out *[]OutlineEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, p := range points {
		if title := strings.TrimSpace(p.Label.Text); title != "" {
			*out = append(*out, OutlineEntry{Title: title, Level: level})
		}
		if err := flattenNCX(ctx, p.Points, level+1, out); err != nil {
			return err
		}
	}
	return nil
}
