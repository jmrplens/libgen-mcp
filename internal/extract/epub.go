package extract

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// containerXML models META-INF/container.xml, whose first rootfile points at
// the OPF package document.
type containerXML struct {
	Rootfiles []struct {
		FullPath string `xml:"full-path,attr"`
	} `xml:"rootfiles>rootfile"`
}

// opfPackage models the OPF manifest (id to href, with the media-type and
// properties needed to locate the navigation document) and spine (reading order
// plus its optional NCX toc reference).
type opfPackage struct {
	Items []struct {
		ID         string `xml:"id,attr"`
		Href       string `xml:"href,attr"`
		MediaType  string `xml:"media-type,attr"`
		Properties string `xml:"properties,attr"`
	} `xml:"manifest>item"`
	Spine struct {
		TOC      string `xml:"toc,attr"`
		ItemRefs []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
}

// cannotOpenEPUBReason is the diagnosis for a file whose bytes are not a
// readable ZIP archive at all. Shared so every read mode words it the same way.
func cannotOpenEPUBReason(err error) string {
	return fmt.Sprintf("cannot open EPUB archive: %v", err)
}

// notReadableEPUBReason is the diagnosis for a readable archive whose EPUB
// structure (container.xml, OPF) is broken. Shared so every read mode words it
// the same way. It formats err with %v rather than calling err.Error(), matching
// its sibling above: three read modes reach these two helpers, and one of them
// growing a path that reports a broken EPUB without an error to name should
// produce a lame diagnosis, not a panic.
func notReadableEPUBReason(err error) string {
	return fmt.Sprintf("not a readable EPUB: %v", err)
}

// extractEPUB reads an EPUB as a ZIP archive, concatenates the text of its
// spine documents in reading order and returns a character-paginated Chunk. A
// malformed archive yields a not-extractable Chunk.
func extractEPUB(ctx context.Context, d document, r Req) (Chunk, error) {
	if err := ctx.Err(); err != nil {
		return Chunk{}, err
	}
	zr, err := zip.NewReader(d.r, d.size)
	if err != nil {
		return Chunk{Format: "epub", Reason: cannotOpenEPUBReason(err)}, nil
	}

	full, truncated, err := readEPUBText(ctx, zr)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Chunk{}, err
		}
		return Chunk{Format: "epub", Extractable: false, Reason: notReadableEPUBReason(err)}, nil
	}
	if strings.TrimSpace(full) == "" {
		return Chunk{Format: "epub", Reason: noEPUBTextReason}, nil
	}
	c := paginateChars(full, "epub", r)
	if truncated {
		c.Truncated = true
		c.Reason = appendNote(c.Reason, capExceededNote)
	}
	return c, nil
}

// readEPUBText resolves the OPF, walks the spine in order and returns the
// concatenated plain text of all chapter documents. The returned bool reports
// whether any spine document was clipped at the per-entry maxTextFileBytes cap,
// so its remaining text is unavailable.
func readEPUBText(ctx context.Context, zr *zip.Reader) (text string, truncated bool, err error) {
	st, err := readSpine(ctx, zr, false)
	return st.text, st.truncated, err
}

// spineText is an EPUB's reading-order text. When anchors were asked for, it
// also records where each content document and each id inside one starts in
// that text, as a rune offset: the key is the document's archive name, or the
// name plus "#id". Rune offsets are what every other EPUB position in this
// package counts in, so an anchor can be handed to paginateChars as is.
type spineText struct {
	text      string
	truncated bool
	anchors   map[string]int
}

// readSpine is readEPUBText's work, with the anchor map filled when
// withAnchors is set. The text is identical either way.
func readSpine(ctx context.Context, zr *zip.Reader, withAnchors bool) (spineText, error) {
	opf, err := opfPath(zr)
	if err != nil {
		return spineText{}, err
	}
	pkg, err := parseOPF(zr, opf)
	if err != nil {
		return spineText{}, err
	}

	hrefByID := make(map[string]string, len(pkg.Items))
	for _, it := range pkg.Items {
		hrefByID[it.ID] = it.Href
	}
	baseDir := path.Dir(opf)

	var st spineText
	if withAnchors {
		st.anchors = make(map[string]int)
	}
	var sb strings.Builder
	runes := 0
	for _, ref := range pkg.Spine.ItemRefs {
		if e := ctx.Err(); e != nil {
			return spineText{}, e
		}
		name, data, clipped, ok := readSpineEntry(zr, baseDir, hrefByID[ref.IDRef])
		if !ok {
			continue
		}
		st.truncated = st.truncated || clipped
		text := htmlText(strings.NewReader(string(data)), st.anchorRecorder(name, runes))
		sb.WriteString(text)
		sb.WriteByte('\n')
		runes += utf8.RuneCountInString(text) + 1
	}
	st.text = sb.String()
	return st, nil
}

// anchorRecorder returns the callback htmlText reports ids to, recording each
// at base plus its offset in the document, or nil when no anchors are kept. The
// document's own start is recorded here too. A document the spine lists twice,
// or an id repeated, keeps its first position, which is where a reader
// following the link lands.
func (st *spineText) anchorRecorder(name string, base int) func(id string, at int) {
	if st.anchors == nil {
		return nil
	}
	if _, seen := st.anchors[name]; !seen {
		st.anchors[name] = base
	}
	return func(id string, at int) {
		key := name + "#" + id
		if _, seen := st.anchors[key]; !seen {
			st.anchors[key] = base + at
		}
	}
}

// readSpineEntry reads the content document a spine item's href names. The
// href is decoded first, since it is a URL, and an archive that stored the
// name with its escapes intact is read under the href as written.
//
// The name it reports is the decoded one whichever spelling was read, because
// it keys the anchors and an outline link is resolved to the decoded name too
// (linkTarget). Keying them by the archive's literal spelling instead would
// leave a document Extract reads unreachable from every outline entry. ok is
// false when the item names nothing readable, which the spine walk skips.
func readSpineEntry(zr *zip.Reader, baseDir, href string) (name string, data []byte, clipped, ok bool) {
	if href == "" {
		return "", nil, false, false
	}
	name = archiveName(baseDir, href)
	for _, candidate := range []string{name, path.Join(baseDir, href)} {
		body, cut, err := readZipEntry(zr, candidate)
		if err == nil {
			return name, body, cut, true
		}
	}
	return "", nil, false, false
}

// opfPath returns the OPF package path referenced by META-INF/container.xml.
func opfPath(zr *zip.Reader) (string, error) {
	data, _, err := readZipEntry(zr, "META-INF/container.xml")
	if err != nil {
		return "", fmt.Errorf("read container.xml: %w", err)
	}
	var c containerXML
	if err = xml.Unmarshal(data, &c); err != nil {
		return "", fmt.Errorf("parse container.xml: %w", err)
	}
	if len(c.Rootfiles) == 0 || c.Rootfiles[0].FullPath == "" {
		return "", errors.New("container.xml has no rootfile")
	}
	return c.Rootfiles[0].FullPath, nil
}

// parseOPF reads and unmarshals the OPF package document at name.
func parseOPF(zr *zip.Reader, name string) (opfPackage, error) {
	data, _, err := readZipEntry(zr, name)
	if err != nil {
		return opfPackage{}, fmt.Errorf("read OPF: %w", err)
	}
	var pkg opfPackage
	if err = xml.Unmarshal(data, &pkg); err != nil {
		return opfPackage{}, fmt.Errorf("parse OPF: %w", err)
	}
	return pkg, nil
}

// readZipEntry reads the named entry from the archive, capping the read to
// avoid unbounded memory use on a malicious archive. The returned bool reports
// whether the entry was clipped at maxTextFileBytes (its content is at least
// that large), so remaining bytes were dropped.
func readZipEntry(zr *zip.Reader, name string) (data []byte, clipped bool, err error) {
	var entry *zip.File
	for _, f := range zr.File {
		if f.Name == name {
			entry = f
			break
		}
	}
	if entry == nil {
		return nil, false, fmt.Errorf("entry not found: %s", name)
	}
	rc, err := entry.Open()
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rc.Close() }()
	// Read one byte past the cap so a saturated LimitReader is detectable, then
	// clip back to the cap.
	data, err = io.ReadAll(io.LimitReader(rc, maxTextFileBytes+1))
	if err != nil {
		return nil, false, err
	}
	clipped = len(data) > maxTextFileBytes
	if clipped {
		data = data[:maxTextFileBytes]
	}
	return data, clipped, nil
}

// htmlText tokenizes an XHTML document and returns its visible text, skipping
// the contents of the elements isSkippedTag names. When onAnchor is non-nil it
// also reports every element id (and every <a name>, the older spelling of a
// link target) together with the rune offset in the returned text where that
// element starts. An outline link's fragment names one of these, and the
// offset is where the entry's text begins.
func htmlText(r io.Reader, onAnchor func(id string, at int)) string {
	w := textWalk{z: html.NewTokenizer(r), onAnchor: onAnchor}
	for {
		switch tt := w.z.Next(); tt {
		case html.ErrorToken:
			return w.sb.String()
		case html.StartTagToken, html.SelfClosingTagToken:
			w.startTag(tt == html.StartTagToken)
		case html.EndTagToken:
			w.endTag()
		case html.TextToken:
			w.text()
		}
	}
}

// textWalk is htmlText's state: the text so far, its length in runes, and how
// deep inside skipped elements the tokenizer is.
type textWalk struct {
	z         *html.Tokenizer
	onAnchor  func(id string, at int)
	sb        strings.Builder
	runes     int
	skipDepth int
}

// startTag reports the tag's anchors and enters a skipped element. A
// self-closing tag has no content to skip.
func (w *textWalk) startTag(opens bool) {
	name, hasAttr := w.z.TagName()
	if hasAttr && w.onAnchor != nil {
		reportAnchors(w.z, string(name) == "a", w.runes, w.onAnchor)
	}
	if opens && isSkippedTag(name) {
		w.skipDepth++
	}
}

// endTag leaves a skipped element.
func (w *textWalk) endTag() {
	if name, _ := w.z.TagName(); isSkippedTag(name) && w.skipDepth > 0 {
		w.skipDepth--
	}
}

// text keeps a text token unless it is inside a skipped element.
func (w *textWalk) text() {
	if w.skipDepth > 0 {
		return
	}
	t := w.z.Text()
	w.sb.Write(t)
	w.runes += utf8.RuneCount(t)
}

// reportAnchors hands the current tag's id to onAnchor at the given offset, and
// its name too when the tag is an <a>: a name on any other element (a <meta>,
// a form control) is not a link target.
func reportAnchors(z *html.Tokenizer, isLink bool, at int, onAnchor func(id string, at int)) {
	for {
		key, val, more := z.TagAttr()
		if k := string(key); (k == "id" || (isLink && k == "name")) && len(val) > 0 {
			onAnchor(string(val), at)
		}
		if !more {
			return
		}
	}
}

// isSkippedTag reports whether the tag's text content should be excluded from
// extracted output. A document's <title> is metadata in its head, not text on
// the page, and an EPUB often repeats the book's title in every chapter file:
// kept, it lands in front of each chapter's heading, and a section read that
// ends where the next chapter starts would end on the next file's title.
func isSkippedTag(name []byte) bool {
	s := string(name)
	return s == "script" || s == "style" || s == "title"
}
