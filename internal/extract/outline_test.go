package extract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

const outlineContainerXML = `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`

// TestOutline_EPUB3Nav verifies that an EPUB3 whose OPF manifest declares a nav
// document (properties="nav") is parsed into ordered OutlineEntry values: two
// top-level chapters with a nested section whose Level is exactly one deeper
// than its parent, reported with Format "epub" and Extractable true.
func TestOutline_EPUB3Nav(t *testing.T) {
	files := map[string]string{
		"META-INF/container.xml": outlineContainerXML,
		"OEBPS/content.opf": `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid">
  <metadata/>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
    <item id="c2" href="chapter2.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="c1"/>
    <itemref idref="c2"/>
  </spine>
</package>`,
		"OEBPS/nav.xhtml": `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>TOC</title></head>
<body>
<nav epub:type="toc">
  <h1>Contents</h1>
  <ol>
    <li><a href="chapter1.xhtml">Chapter One</a>
      <ol>
        <li><a href="chapter1.xhtml#s1">Section 1.1</a></li>
      </ol>
    </li>
    <li><a href="chapter2.xhtml">Chapter Two</a></li>
  </ol>
</nav>
</body></html>`,
		"OEBPS/chapter1.xhtml": `<html><body><p>one</p></body></html>`,
		"OEBPS/chapter2.xhtml": `<html><body><p>two</p></body></html>`,
	}
	path := writeEPUB(t, t.TempDir(), "nav.epub", files)

	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "epub" {
		t.Fatalf("want extractable epub, got %+v", res)
	}
	if len(res.Entries) != 3 {
		t.Fatalf("want 3 entries, got %d: %+v", len(res.Entries), res.Entries)
	}
	want := []OutlineEntry{
		{Title: "Chapter One", Level: 0},
		{Title: "Section 1.1", Level: 1},
		{Title: "Chapter Two", Level: 0},
	}
	for i, w := range want {
		if res.Entries[i].Title != w.Title || res.Entries[i].Level != w.Level {
			t.Errorf("entry %d: want %+v, got %+v", i, w, res.Entries[i])
		}
	}
}

// TestOutline_EPUB2NCX verifies that an EPUB with no nav document falls back to
// the NCX (media-type application/x-dtbncx+xml) and parses its navMap into
// two top-level entries with the correct titles and Level 0.
func TestOutline_EPUB2NCX(t *testing.T) {
	files := map[string]string{
		"META-INF/container.xml": outlineContainerXML,
		"OEBPS/content.opf": `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="bookid">
  <metadata/>
  <manifest>
    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
    <item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
    <item id="c2" href="chapter2.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine toc="ncx">
    <itemref idref="c1"/>
    <itemref idref="c2"/>
  </spine>
</package>`,
		"OEBPS/toc.ncx": `<?xml version="1.0" encoding="utf-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
<navMap>
  <navPoint id="np1" playOrder="1"><navLabel><text>First NCX Chapter</text></navLabel><content src="chapter1.xhtml"/></navPoint>
  <navPoint id="np2" playOrder="2"><navLabel><text>Second NCX Chapter</text></navLabel><content src="chapter2.xhtml"/></navPoint>
</navMap>
</ncx>`,
		"OEBPS/chapter1.xhtml": `<html><body><p>one</p></body></html>`,
		"OEBPS/chapter2.xhtml": `<html><body><p>two</p></body></html>`,
	}
	path := writeEPUB(t, t.TempDir(), "ncx.epub", files)

	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "epub" {
		t.Fatalf("want extractable epub, got %+v", res)
	}
	if len(res.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(res.Entries), res.Entries)
	}
	if res.Entries[0].Title != "First NCX Chapter" || res.Entries[0].Level != 0 {
		t.Errorf("entry 0 wrong: %+v", res.Entries[0])
	}
	if res.Entries[1].Title != "Second NCX Chapter" || res.Entries[1].Level != 0 {
		t.Errorf("entry 1 wrong: %+v", res.Entries[1])
	}
}

// TestOutline_EPUB2NCXViaSpineToc verifies ncxHref's fallback path: when no
// manifest item carries the NCX media-type, the NCX referenced by the spine's
// toc id is used instead, and its navMap is parsed into entries.
func TestOutline_EPUB2NCXViaSpineToc(t *testing.T) {
	files := map[string]string{
		"META-INF/container.xml": outlineContainerXML,
		"OEBPS/content.opf": `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="bookid">
  <metadata/>
  <manifest>
    <item id="ncx" href="toc.ncx" media-type="text/xml"/>
    <item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine toc="ncx">
    <itemref idref="c1"/>
  </spine>
</package>`,
		"OEBPS/toc.ncx": `<?xml version="1.0" encoding="utf-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
<navMap>
  <navPoint id="np1" playOrder="1"><navLabel><text>Spine-Toc Chapter</text></navLabel><content src="chapter1.xhtml"/></navPoint>
</navMap>
</ncx>`,
		"OEBPS/chapter1.xhtml": `<html><body><p>one</p></body></html>`,
	}
	path := writeEPUB(t, t.TempDir(), "ncx-spine-toc.epub", files)

	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "epub" {
		t.Fatalf("want extractable epub, got %+v", res)
	}
	if len(res.Entries) != 1 {
		t.Fatalf("want 1 entry, got %d: %+v", len(res.Entries), res.Entries)
	}
	if res.Entries[0].Title != "Spine-Toc Chapter" || res.Entries[0].Level != 0 {
		t.Errorf("entry 0 wrong: %+v", res.Entries[0])
	}
}

// TestOutline_EPUB2NCXNested verifies that flattenNCX recurses into nested
// navPoints: a chapter navPoint containing a child navPoint (sub-section)
// produces two entries in document order, with the child's Level exactly one
// deeper than its parent's.
func TestOutline_EPUB2NCXNested(t *testing.T) {
	files := map[string]string{
		"META-INF/container.xml": outlineContainerXML,
		"OEBPS/content.opf": `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="bookid">
  <metadata/>
  <manifest>
    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
    <item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine toc="ncx">
    <itemref idref="c1"/>
  </spine>
</package>`,
		"OEBPS/toc.ncx": `<?xml version="1.0" encoding="utf-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
<navMap>
  <navPoint id="np1" playOrder="1"><navLabel><text>Chapter With Sub-Section</text></navLabel><content src="chapter1.xhtml"/>
    <navPoint id="np1-1" playOrder="2"><navLabel><text>Nested Sub-Section</text></navLabel><content src="chapter1.xhtml#s1"/></navPoint>
  </navPoint>
</navMap>
</ncx>`,
		"OEBPS/chapter1.xhtml": `<html><body><p>one</p></body></html>`,
	}
	path := writeEPUB(t, t.TempDir(), "ncx-nested.epub", files)

	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "epub" {
		t.Fatalf("want extractable epub, got %+v", res)
	}
	if len(res.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(res.Entries), res.Entries)
	}
	if res.Entries[0].Title != "Chapter With Sub-Section" || res.Entries[0].Level != 0 {
		t.Errorf("entry 0 wrong: %+v", res.Entries[0])
	}
	if res.Entries[1].Title != "Nested Sub-Section" || res.Entries[1].Level != res.Entries[0].Level+1 {
		t.Errorf("entry 1 wrong: %+v", res.Entries[1])
	}
}

// TestOutline_EPUB3NavWithoutTocType verifies findTocNav's fallback: when the
// nav document's <nav> element has no epub:type="toc" attribute, Outline still
// extracts the entries from that first <nav>.
func TestOutline_EPUB3NavWithoutTocType(t *testing.T) {
	files := map[string]string{
		"META-INF/container.xml": outlineContainerXML,
		"OEBPS/content.opf": `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid">
  <metadata/>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="c1"/>
  </spine>
</package>`,
		"OEBPS/nav.xhtml": `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<head><title>TOC</title></head>
<body>
<nav>
  <h1>Contents</h1>
  <ol>
    <li><a href="chapter1.xhtml">Untyped Nav Chapter</a></li>
  </ol>
</nav>
</body></html>`,
		"OEBPS/chapter1.xhtml": `<html><body><p>one</p></body></html>`,
	}
	path := writeEPUB(t, t.TempDir(), "nav-no-toctype.epub", files)

	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "epub" {
		t.Fatalf("want extractable epub, got %+v", res)
	}
	if len(res.Entries) != 1 {
		t.Fatalf("want 1 entry, got %d: %+v", len(res.Entries), res.Entries)
	}
	if res.Entries[0].Title != "Untyped Nav Chapter" || res.Entries[0].Level != 0 {
		t.Errorf("entry 0 wrong: %+v", res.Entries[0])
	}
}

// TestOutline_EPUB3NavMissingFile verifies that when the OPF declares a nav
// document (properties="nav") whose file is absent from the archive and there
// is no NCX, Outline reports an extractable EPUB with no entries: a missing nav
// document reads as "no TOC", not a failure. Exercises navEntries' nil-data
// branch and the NCX fallback.
func TestOutline_EPUB3NavMissingFile(t *testing.T) {
	files := map[string]string{
		"META-INF/container.xml": outlineContainerXML,
		"OEBPS/content.opf": `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid">
  <metadata/>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="c1"/>
  </spine>
</package>`,
		"OEBPS/chapter1.xhtml": `<html><body><p>one</p></body></html>`,
	}
	path := writeEPUB(t, t.TempDir(), "nav-missing.epub", files)

	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "epub" {
		t.Fatalf("want extractable epub, got %+v", res)
	}
	if len(res.Entries) != 0 {
		t.Errorf("want no entries for a missing nav document, got %+v", res.Entries)
	}
}

// TestOutline_EPUB3NavNoList verifies that a nav document whose <nav> element
// contains no <ol> yields no entries (firstOL returns nil), reported as an
// extractable EPUB.
func TestOutline_EPUB3NavNoList(t *testing.T) {
	files := map[string]string{
		"META-INF/container.xml": outlineContainerXML,
		"OEBPS/content.opf": `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid">
  <metadata/>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="c1"/>
  </spine>
</package>`,
		"OEBPS/nav.xhtml": `<html xmlns:epub="http://www.idpf.org/2007/ops"><body>
<nav epub:type="toc"><h1>Contents</h1><p>No list here.</p></nav>
</body></html>`,
		"OEBPS/chapter1.xhtml": `<html><body><p>one</p></body></html>`,
	}
	path := writeEPUB(t, t.TempDir(), "nav-no-ol.epub", files)

	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "epub" {
		t.Fatalf("want extractable epub, got %+v", res)
	}
	if len(res.Entries) != 0 {
		t.Errorf("want no entries when the nav has no list, got %+v", res.Entries)
	}
}

// TestOutline_EPUB3NavSpanAndBareLI verifies two liTitle behaviors in one nav:
// a <li> whose label is a <span> (not an <a>) is titled from that span, and a
// wrapper <li> with no <a>/<span> of its own contributes no title but its
// nested <ol> child is still walked one level deeper.
func TestOutline_EPUB3NavSpanAndBareLI(t *testing.T) {
	files := map[string]string{
		"META-INF/container.xml": outlineContainerXML,
		"OEBPS/content.opf": `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid">
  <metadata/>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="c1"/>
  </spine>
</package>`,
		"OEBPS/nav.xhtml": `<html xmlns:epub="http://www.idpf.org/2007/ops"><body>
<nav epub:type="toc">
  <ol>
    <li><span>Span Titled Part</span></li>
    <li>
      <ol>
        <li><a href="chapter1.xhtml#s1">Nested Under Bare LI</a></li>
      </ol>
    </li>
  </ol>
</nav>
</body></html>`,
		"OEBPS/chapter1.xhtml": `<html><body><p>one</p></body></html>`,
	}
	path := writeEPUB(t, t.TempDir(), "nav-span-bare.epub", files)

	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "epub" {
		t.Fatalf("want extractable epub, got %+v", res)
	}
	if len(res.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(res.Entries), res.Entries)
	}
	if res.Entries[0].Title != "Span Titled Part" || res.Entries[0].Level != 0 {
		t.Errorf("entry 0 wrong: %+v", res.Entries[0])
	}
	if res.Entries[1].Title != "Nested Under Bare LI" || res.Entries[1].Level != 1 {
		t.Errorf("entry 1 wrong (bare-LI nested one level deeper): %+v", res.Entries[1])
	}
}

// TestOutline_EPUBNoToc verifies that a valid EPUB with neither a nav document
// nor an NCX is reported as extractable with no entries and no error: a missing
// table of contents is valid, not a failure. Its reason must say that and only
// that — the spine text is readable, so the caller should read on rather than
// give up on the file.
func TestOutline_EPUBNoToc(t *testing.T) {
	path := buildEPUB(t, t.TempDir())
	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "epub" {
		t.Fatalf("want extractable epub, got %+v", res)
	}
	if len(res.Entries) != 0 {
		t.Errorf("want no entries, got %+v", res.Entries)
	}
	if res.Reason != noEPUBOutlineReason {
		t.Errorf("Reason = %q, want the no-table-of-contents diagnosis", res.Reason)
	}
}

// TestOutline_EPUBNoTocAndNoText verifies the EPUB twin of the scanned-PDF bug:
// an EPUB whose spine yields no text at all has no table of contents *because*
// there is nothing in it, and outline mode must report the text path's diagnosis
// rather than the far more encouraging "no table of contents".
func TestOutline_EPUBNoTocAndNoText(t *testing.T) {
	path := writeEPUB(t, t.TempDir(), "empty.epub", map[string]string{
		"META-INF/container.xml": outlineContainerXML,
		"OEBPS/content.opf": `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid">
  <metadata/>
  <manifest>
    <item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="c1"/>
  </spine>
</package>`,
		"OEBPS/chapter1.xhtml": `<html><body><img src="scan-001.png"/></body></html>`,
	})
	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Extractable {
		t.Fatalf("an EPUB with no text must not be reported as merely TOC-less, got %+v", res)
	}
	chunk, err := Extract(context.Background(), path, Req{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != chunk.Reason {
		t.Errorf("outline and text paths must give one diagnosis for one cause:\n outline: %q\n text:    %q",
			res.Reason, chunk.Reason)
	}
}

// TestOutline_TXTMissingFile verifies the TXT twin: outline mode used to declare
// any path ending in .txt a readable document with no table of contents, without
// so much as opening it. An unreadable file must be reported exactly as the text
// path reports it.
func TestOutline_TXTMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.txt")
	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Extractable {
		t.Fatalf("expected not extractable, got %+v", res)
	}
	chunk, err := Extract(context.Background(), path, Req{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != chunk.Reason {
		t.Errorf("outline and text paths must give one diagnosis for one cause:\n outline: %q\n text:    %q",
			res.Reason, chunk.Reason)
	}
}

// TestOutline_DiagnosisMatchesTextPath is the standing guard over the property
// this whole area exists to hold: for one file, one cause, one answer. Whatever
// a caller asks for, Outline and Extract must agree on whether the document can
// be read at all, and when it cannot they must name the reason in the very same
// words — a model that gets "no table of contents" from one and "no extractable
// text layer" from the other pays an extra round trip to learn what the first
// call already knew.
//
// The one deliberate exception is an EPUB that carries navigation but no body
// text: its table of contents is real information and is still worth returning,
// so no such fixture appears here.
func TestOutline_DiagnosisMatchesTextPath(t *testing.T) {
	dir := t.TempDir()
	notAZip := filepath.Join(dir, "notazip.epub")
	if err := os.WriteFile(notAZip, []byte("this is not a zip archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	brokenPDF := filepath.Join(dir, "broken.pdf")
	if err := os.WriteFile(brokenPDF, []byte("%PDF-1.7 definitely not a pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	graphics := filepath.Join(dir, "graphics.pdf")
	if err := os.WriteFile(graphics, graphicsOnlyPDF(), 0o600); err != nil {
		t.Fatal(err)
	}

	paths := map[string]string{
		"text pdf":        "testdata/sample.pdf",
		"bookmarked pdf":  "testdata/bookmarked.pdf",
		"scanned pdf":     "testdata/scanned.pdf",
		"graphics pdf":    graphics,
		"broken pdf":      brokenPDF,
		"plain text":      "testdata/sample.txt",
		"comic archive":   "testdata/unsupported.djvu",
		"unknown suffix":  "testdata/whatever.xyz",
		"epub with text":  buildEPUB(t, dir),
		"not a zip":       notAZip,
		"missing txt":     filepath.Join(dir, "gone.txt"),
		"extensionless":   sniffableCopy(t, dir, "testdata/scanned.pdf", "noextension"),
		"missing pdf":     filepath.Join(dir, "gone.pdf"),
		"blank then text": writeBytes(t, dir, "coverthentext.pdf", blankThenTextPDF()),
		"null page pdf":   writeBytes(t, dir, "nullpage.pdf", nullPagePDF()),
	}
	for name, p := range paths {
		outline, err := Outline(context.Background(), p)
		if err != nil {
			t.Fatalf("%s: Outline: %v", name, err)
		}
		chunk, err := Extract(context.Background(), p, Req{})
		if err != nil {
			t.Fatalf("%s: Extract: %v", name, err)
		}
		if outline.Extractable != chunk.Extractable {
			t.Errorf("%s: Extractable disagrees: outline=%t text=%t (outline %q / text %q)",
				name, outline.Extractable, chunk.Extractable, outline.Reason, chunk.Reason)
			continue
		}
		if !outline.Extractable && outline.Reason != chunk.Reason {
			t.Errorf("%s: same cause, different words:\n outline: %q\n text:    %q",
				name, outline.Reason, chunk.Reason)
		}
	}
}

// TestEpubNoOutlineResult_CtxCancelledDuringTextProbe verifies that a context
// canceled while the no-navigation branch probes the spine for text propagates
// out as an error: the fourth ctx check of the chain is the first spine document
// read inside epubNoOutlineResult, so a context that survives three checks
// exercises exactly that guard.
func TestEpubNoOutlineResult_CtxCancelledDuringTextProbe(t *testing.T) {
	path := buildEPUB(t, t.TempDir())
	if _, err := epubOutline(passErr(3), path); err == nil {
		t.Fatal("expected the context error to propagate from the text probe, got nil")
	}
}

// writeBytes writes content into dir under name and returns the path.
func writeBytes(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, content, 0o600); err != nil { //nolint:gosec // dir is the test's own temp dir and name is a literal.
		t.Fatal(err)
	}
	return p
}

// sniffableCopy copies src into dir under an extensionless name, so a fixture
// can exercise the by-content dispatch both entry points share.
func sniffableCopy(t *testing.T, dir, src, name string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	return writeBytes(t, dir, name, data)
}

// TestOutline_EPUBMalformed verifies that a structurally broken EPUB (no
// container.xml) is reported as not extractable with a reason, exercising
// epubOutline's structural-failure branch.
func TestOutline_EPUBMalformed(t *testing.T) {
	path := writeEPUB(t, t.TempDir(), "broken.epub", map[string]string{
		"README.txt": "not an epub",
	})
	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Extractable {
		t.Fatalf("expected not extractable, got %+v", res)
	}
	if !strings.Contains(res.Reason, "not a readable EPUB") {
		t.Errorf("reason should note the unreadable EPUB, got %q", res.Reason)
	}
}

// TestOutline_TXT verifies that a plain-text file is reported as extractable
// with no entries and no error: plain text has no outline.
func TestOutline_TXT(t *testing.T) {
	res, err := Outline(context.Background(), "testdata/sample.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "txt" {
		t.Fatalf("want extractable txt, got %+v", res)
	}
	if len(res.Entries) != 0 {
		t.Errorf("want no entries, got %+v", res.Entries)
	}
}

// TestOutline_UnsupportedFormat verifies that a DjVu container is reported as
// not extractable with a non-empty reason.
func TestOutline_UnsupportedFormat(t *testing.T) {
	res, err := Outline(context.Background(), "testdata/unsupported.djvu")
	if err != nil {
		t.Fatal(err)
	}
	if res.Extractable {
		t.Fatalf("djvu must be not extractable, got %+v", res)
	}
	if res.Reason == "" {
		t.Error("want a non-empty reason")
	}
}

// TestOutline_UnsupportedExtension verifies that an unrecognized extension is
// reported as not extractable with a reason naming it, exercising Outline's
// default dispatch branch.
func TestOutline_UnsupportedExtension(t *testing.T) {
	res, err := Outline(context.Background(), "testdata/whatever.xyz")
	if err != nil {
		t.Fatal(err)
	}
	if res.Extractable {
		t.Fatalf("expected not extractable, got %+v", res)
	}
	if !strings.Contains(res.Reason, "unsupported file extension") {
		t.Errorf("reason should name the unsupported extension, got %q", res.Reason)
	}
}

// TestOutline_ContextCancelled verifies that a canceled context causes Outline
// to return the context error rather than a result.
func TestOutline_ContextCancelled(t *testing.T) {
	path := buildEPUB(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Outline(ctx, path)
	if err == nil {
		t.Fatal("expected a context error, got nil")
	}
}

// epub3NavOPF is the OPF package document for an EPUB3 that declares a nav
// document (properties="nav") plus one chapter; the outline nav edge-case tests
// vary only the nav markup, not this manifest.
const epub3NavOPF = `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid">
  <metadata/>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="c1"/>
  </spine>
</package>`

// epub3NavFiles builds the file set for an EPUB3 whose OPF declares a nav
// document, embedding navInner as the body of nav.xhtml so each test varies only
// the navigation markup. It reuses outlineContainerXML and epub3NavOPF; it is
// not an EPUB writer (writeEPUB does the archiving).
func epub3NavFiles(navInner string) map[string]string {
	return map[string]string{
		"META-INF/container.xml": outlineContainerXML,
		"OEBPS/content.opf":      epub3NavOPF,
		"OEBPS/nav.xhtml": `<html xmlns:epub="http://www.idpf.org/2007/ops"><body>` +
			navInner + `</body></html>`,
		"OEBPS/chapter1.xhtml": `<html><body><p>one</p></body></html>`,
	}
}

// epub2Files builds an EPUB2 file set from a given OPF and (optional) NCX
// document, reusing outlineContainerXML. An empty ncx omits toc.ncx so the NCX
// missing-file branch can be exercised. It is not an EPUB writer.
func epub2Files(opf, ncx string) map[string]string {
	files := map[string]string{
		"META-INF/container.xml": outlineContainerXML,
		"OEBPS/content.opf":      opf,
		"OEBPS/chapter1.xhtml":   `<html><body><p>one</p></body></html>`,
	}
	if ncx != "" {
		files["OEBPS/toc.ncx"] = ncx
	}
	return files
}

// ncxOPF is an EPUB2 OPF referencing an NCX via the NCX media-type, used by the
// NCX context-cancellation tests.
const ncxOPF = `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="bookid">
  <metadata/>
  <manifest>
    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
    <item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine toc="ncx">
    <itemref idref="c1"/>
  </spine>
</package>`

// TestEpubOutline_ContextCancelledDirect verifies epubOutline's own entry guard:
// called directly with an already-canceled context it returns the context error
// before opening the archive (Outline's guard short-circuits this in practice).
func TestEpubOutline_ContextCancelledDirect(t *testing.T) {
	path := buildEPUB(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := epubOutline(ctx, path); err == nil {
		t.Fatal("expected a context error, got nil")
	}
}

// TestOutline_EPUBNotAZip verifies epubOutline's zip.OpenReader failure branch: a
// .epub whose bytes are not a valid ZIP archive is reported as not extractable
// with the same "cannot open EPUB archive" reason the text and find paths give
// (distinct from the valid-ZIP structural failure that TestOutline_EPUBMalformed
// exercises, which all three modes call "not a readable EPUB").
func TestOutline_EPUBNotAZip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notazip.epub")
	if err := os.WriteFile(path, []byte("this is not a zip archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Extractable {
		t.Fatalf("expected not extractable, got %+v", res)
	}
	if !strings.Contains(res.Reason, "cannot open EPUB archive") {
		t.Errorf("reason should note the unreadable EPUB, got %q", res.Reason)
	}
}

// TestEpubOutline_CtxCancelledDuringNav verifies the context-cancellation chain
// through navEntries: a context that survives epubOutline's entry guard but is
// canceled by navEntries' entry check propagates the error up through
// readEPUBOutline and epubOutline (covering navEntries' guard, readEPUBOutline's
// nav-error return, and epubOutline's context-error branch).
func TestEpubOutline_CtxCancelledDuringNav(t *testing.T) {
	path := buildEPUB(t, t.TempDir())
	if _, err := epubOutline(passErr(1), path); err == nil {
		t.Fatal("expected a context error propagated from navEntries, got nil")
	}
}

// TestOutline_EPUBMalformedOPFViaOutline verifies readEPUBOutline's parseOPF
// error branch on the Outline path: a valid container.xml pointing at a
// malformed OPF makes Outline report a not-extractable "not a readable EPUB".
func TestOutline_EPUBMalformedOPFViaOutline(t *testing.T) {
	files := map[string]string{
		"META-INF/container.xml": outlineContainerXML,
		"OEBPS/content.opf":      `<?xml version="1.0"?><package><manifest><item`,
	}
	path := writeEPUB(t, t.TempDir(), "bad-opf.epub", files)
	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Extractable {
		t.Fatalf("expected not extractable, got %+v", res)
	}
	if !strings.Contains(res.Reason, "not a readable EPUB") {
		t.Errorf("reason should note the unreadable EPUB, got %q", res.Reason)
	}
}

// TestOutline_EPUB3NavNoNavElement verifies navEntries' nil-nav branch: a nav
// document that contains no <nav> element at all (findTocNav returns nil) yields
// an extractable EPUB with no entries.
func TestOutline_EPUB3NavNoNavElement(t *testing.T) {
	files := epub3NavFiles(`<div><p>No nav element here.</p></div>`)
	path := writeEPUB(t, t.TempDir(), "nav-no-navel.epub", files)
	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "epub" {
		t.Fatalf("want extractable epub, got %+v", res)
	}
	if len(res.Entries) != 0 {
		t.Errorf("want no entries when there is no <nav>, got %+v", res.Entries)
	}
}

// TestOutline_EPUB3NavOLInWrapper verifies firstOL's recursion branch: when the
// <ol> is nested inside a wrapper element under <nav> (not a direct child), the
// depth-first search still finds it and its entry is extracted.
func TestOutline_EPUB3NavOLInWrapper(t *testing.T) {
	files := epub3NavFiles(
		`<nav epub:type="toc"><div><ol><li><a href="chapter1.xhtml">Wrapped Chapter</a></li></ol></div></nav>`)
	path := writeEPUB(t, t.TempDir(), "nav-ol-wrapper.epub", files)
	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Title != "Wrapped Chapter" {
		t.Fatalf("want one wrapped entry, got %+v", res.Entries)
	}
}

// TestWalkOL_CtxCancelledEntry verifies the cancellation chain through walkOL: a
// context canceled at walkOL's entry guard propagates up through navEntries'
// walk-error return to epubOutline. Uses a nav with a nested <ol> so the walk is
// actually entered.
func TestWalkOL_CtxCancelledEntry(t *testing.T) {
	files := epub3NavFiles(
		`<nav epub:type="toc"><ol><li><a href="chapter1.xhtml">One</a>` +
			`<ol><li><a href="chapter1.xhtml#s">Sub</a></li></ol></li></ol></nav>`)
	path := writeEPUB(t, t.TempDir(), "nav-walk-cancel.epub", files)
	if _, err := epubOutline(passErr(2), path); err == nil {
		t.Fatal("expected a context error from walkOL, got nil")
	}
}

// TestAppendLI_CtxCancelledNested verifies the cancellation chain through
// appendLI: a context that survives the top-level walk but is canceled when
// appendLI recurses into a nested <ol> propagates the error back through walkOL.
func TestAppendLI_CtxCancelledNested(t *testing.T) {
	files := epub3NavFiles(
		`<nav epub:type="toc"><ol><li><a href="chapter1.xhtml">One</a>` +
			`<ol><li><a href="chapter1.xhtml#s">Sub</a></li></ol></li></ol></nav>`)
	path := writeEPUB(t, t.TempDir(), "nav-appendli-cancel.epub", files)
	if _, err := epubOutline(passErr(3), path); err == nil {
		t.Fatal("expected a context error from appendLI's nested walk, got nil")
	}
}

// TestNavIsToc_NamespacedAttr verifies navIsToc's namespaced-attribute branch:
// when the parser records epub:type as a namespaced attribute (Namespace set),
// the reconstructed "epub:type" key is still recognized as a TOC marker.
func TestNavIsToc_NamespacedAttr(t *testing.T) {
	n := &html.Node{
		Type: html.ElementNode,
		Data: "nav",
		Attr: []html.Attribute{{Namespace: "epub", Key: "type", Val: "toc"}},
	}
	if !navIsToc(n) {
		t.Fatal("want navIsToc true for a namespaced epub:type=\"toc\" attribute")
	}
}

// TestNcxEntries_CtxCancelledEntry verifies the cancellation chain through
// ncxEntries: with no nav document, navEntries returns empty without canceling,
// and the context is canceled at ncxEntries' entry guard, propagating up.
func TestNcxEntries_CtxCancelledEntry(t *testing.T) {
	ncx := `<?xml version="1.0" encoding="utf-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
<navMap><navPoint id="np1"><navLabel><text>Chapter</text></navLabel><content src="chapter1.xhtml"/></navPoint></navMap>
</ncx>`
	path := writeEPUB(t, t.TempDir(), "ncx-entry-cancel.epub", epub2Files(ncxOPF, ncx))
	if _, err := epubOutline(passErr(2), path); err == nil {
		t.Fatal("expected a context error from ncxEntries, got nil")
	}
}

// TestNcxEntries_CtxCancelledFlatten verifies the cancellation chain through
// flattenNCX: the context survives ncxEntries' entry guard but is canceled at
// flattenNCX's entry guard, propagating up through ncxEntries.
func TestNcxEntries_CtxCancelledFlatten(t *testing.T) {
	ncx := `<?xml version="1.0" encoding="utf-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
<navMap><navPoint id="np1"><navLabel><text>Chapter</text></navLabel><content src="chapter1.xhtml"/></navPoint></navMap>
</ncx>`
	path := writeEPUB(t, t.TempDir(), "ncx-flatten-cancel.epub", epub2Files(ncxOPF, ncx))
	if _, err := epubOutline(passErr(3), path); err == nil {
		t.Fatal("expected a context error from flattenNCX, got nil")
	}
}

// TestFlattenNCX_CtxCancelledRecursion verifies flattenNCX's recursion-error
// branch: with a nested navPoint, the context survives the top-level flatten but
// is canceled when flattenNCX recurses into the child, propagating up.
func TestFlattenNCX_CtxCancelledRecursion(t *testing.T) {
	ncx := `<?xml version="1.0" encoding="utf-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
<navMap>
  <navPoint id="np1"><navLabel><text>Parent</text></navLabel><content src="chapter1.xhtml"/>
    <navPoint id="np1-1"><navLabel><text>Child</text></navLabel><content src="chapter1.xhtml#s"/></navPoint>
  </navPoint>
</navMap>
</ncx>`
	path := writeEPUB(t, t.TempDir(), "ncx-recurse-cancel.epub", epub2Files(ncxOPF, ncx))
	if _, err := epubOutline(passErr(4), path); err == nil {
		t.Fatal("expected a context error from flattenNCX recursion, got nil")
	}
}

// TestOutline_NCXMissingFile verifies ncxEntries' nil-data branch: an OPF that
// references an NCX (via the NCX media-type) whose toc.ncx file is absent from
// the archive, with no nav document, yields an extractable EPUB with no entries.
func TestOutline_NCXMissingFile(t *testing.T) {
	path := writeEPUB(t, t.TempDir(), "ncx-missing-file.epub", epub2Files(ncxOPF, ""))
	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "epub" {
		t.Fatalf("want extractable epub, got %+v", res)
	}
	if len(res.Entries) != 0 {
		t.Errorf("want no entries for a missing NCX file, got %+v", res.Entries)
	}
}

// TestOutline_NCXHrefNoMatch verifies ncxHref's final empty-string return: no
// manifest item carries the NCX media-type and the spine's toc id matches no
// item, so ncxHref yields "" and the EPUB is extractable with no entries.
func TestOutline_NCXHrefNoMatch(t *testing.T) {
	opf := `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="bookid">
  <metadata/>
  <manifest>
    <item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine toc="missing">
    <itemref idref="c1"/>
  </spine>
</package>`
	path := writeEPUB(t, t.TempDir(), "ncx-href-nomatch.epub", epub2Files(opf, ""))
	res, err := Outline(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "epub" {
		t.Fatalf("want extractable epub, got %+v", res)
	}
	if len(res.Entries) != 0 {
		t.Errorf("want no entries when the spine toc id matches nothing, got %+v", res.Entries)
	}
}
