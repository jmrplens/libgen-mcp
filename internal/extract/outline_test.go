package extract

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// buildOutlineEPUB writes a minimal EPUB (mimetype first + the given entries)
// into dir under name and returns the file path. Callers supply the
// container.xml, OPF and navigation documents so a single helper can build
// EPUB3-nav, EPUB2-NCX and no-TOC fixtures.
func buildOutlineEPUB(t *testing.T, dir, name string, files map[string]string) string {
	t.Helper()
	fp := filepath.Join(dir, name)
	f, err := os.Create(fp)
	if err != nil {
		t.Fatalf("create epub: %v", err)
	}
	defer func() { _ = f.Close() }()

	zw := zip.NewWriter(f)
	// mimetype must be first and stored (uncompressed) per the EPUB spec.
	mw, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		t.Fatalf("create mimetype: %v", err)
	}
	if _, err = mw.Write([]byte("application/epub+zip")); err != nil {
		t.Fatalf("write mimetype: %v", err)
	}
	for entry, content := range files {
		w, cerr := zw.Create(entry)
		if cerr != nil {
			t.Fatalf("create %s: %v", entry, cerr)
		}
		if _, werr := w.Write([]byte(content)); werr != nil {
			t.Fatalf("write %s: %v", entry, werr)
		}
	}
	if err = zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return fp
}

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
	path := buildOutlineEPUB(t, t.TempDir(), "nav.epub", files)

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
	path := buildOutlineEPUB(t, t.TempDir(), "ncx.epub", files)

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
	path := buildOutlineEPUB(t, t.TempDir(), "ncx-nested.epub", files)

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
	path := buildOutlineEPUB(t, t.TempDir(), "nav-no-toctype.epub", files)

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

// TestOutline_EPUBNoToc verifies that a valid EPUB with neither a nav document
// nor an NCX is reported as extractable with no entries and no error: a missing
// table of contents is valid, not a failure.
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

// TestOutline_PDFPlaceholder verifies the B2 placeholder: a PDF is reported as
// extractable with no entries and a reason noting outline extraction lands in a
// later step. Task B2 replaces this with real pdfcpu bookmark reading.
func TestOutline_PDFPlaceholder(t *testing.T) {
	res, err := Outline(context.Background(), "testdata/sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Extractable || res.Format != "pdf" {
		t.Fatalf("want extractable pdf placeholder, got %+v", res)
	}
	if len(res.Entries) != 0 {
		t.Errorf("want no entries in placeholder, got %+v", res.Entries)
	}
	if res.Reason == "" {
		t.Error("want a reason marking the placeholder")
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
