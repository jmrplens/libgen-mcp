package extract

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildEPUB writes a minimal valid EPUB (mimetype + container.xml + OPF with a
// 2-item spine + 2 XHTML chapters) into dir and returns the file path. The two
// chapters contain the sentences "Chapter one alpha sentence." and
// "Chapter two beta sentence." so tests can assert on extracted content. It is
// shared with the outline tests, which build TOC-less EPUBs from it.
func buildEPUB(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "sample.epub")
	f, err := os.Create(path)
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

	files := map[string]string{
		"META-INF/container.xml": `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`,
		"OEBPS/content.opf": `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid">
  <metadata/>
  <manifest>
    <item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
    <item id="c2" href="chapter2.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="c1"/>
    <itemref idref="c2"/>
  </spine>
</package>`,
		"OEBPS/chapter1.xhtml": `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>C1</title>
<style>.x{color:red}</style></head>
<body><h1>Chapter One</h1><p>Chapter one alpha sentence.</p>
<script>var ignore = 1;</script></body></html>`,
		"OEBPS/chapter2.xhtml": `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>C2</title></head>
<body><p>Chapter two beta sentence.</p></body></html>`,
	}
	for name, content := range files {
		w, cerr := zw.Create(name)
		if cerr != nil {
			t.Fatalf("create %s: %v", name, cerr)
		}
		if _, werr := w.Write([]byte(content)); werr != nil {
			t.Fatalf("write %s: %v", name, werr)
		}
	}
	if err = zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return path
}

// writeEPUB writes a minimal EPUB (mimetype first, then the given entries) into
// dir under name and returns the file path. Unlike buildEPUB it takes the
// container.xml, OPF and chapter documents verbatim, so a single helper can
// build the malformed and edge-case fixtures the EPUB tests need.
func writeEPUB(t *testing.T, dir, name string, files map[string]string) string {
	t.Helper()
	fp := filepath.Join(dir, name)
	f, err := os.Create(fp)
	if err != nil {
		t.Fatalf("create epub: %v", err)
	}
	defer func() { _ = f.Close() }()

	zw := zip.NewWriter(f)
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

// TestExtract_MalformedEPUB verifies that a ZIP archive missing the required
// META-INF/container.xml entry (a structurally invalid EPUB) is reported as
// not extractable with a non-empty reason and a nil error, rather than
// failing the caller with a hard error.
func TestExtract_MalformedEPUB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.epub")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create broken epub: %v", err)
	}
	zw := zip.NewWriter(f)
	// A valid ZIP archive, but with no META-INF/container.xml entry: the
	// structural EPUB parse must fail while the ZIP itself opens fine.
	w, err := zw.Create("README.txt")
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if _, err = w.Write([]byte("not an epub")); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	if err = zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err = f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	c, err := Extract(context.Background(), path, Req{})
	if err != nil {
		t.Fatalf("expected nil error for malformed EPUB, got %v", err)
	}
	if c.Extractable {
		t.Fatalf("expected not extractable, got %+v", c)
	}
	if c.Reason == "" {
		t.Fatal("expected a non-empty reason")
	}
}

// TestExtract_EPUB verifies that a minimal EPUB extracts XHTML chapter text in
// spine order, reporting the epub format.
func TestExtract_EPUB(t *testing.T) {
	path := buildEPUB(t, t.TempDir())
	c, err := Extract(context.Background(), path, Req{Offset: 0, MaxChars: 500})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Extractable || c.Format != "epub" || strings.TrimSpace(c.Text) == "" {
		t.Fatalf("expected epub text, got %+v", c)
	}
	if !strings.Contains(c.Text, "Chapter one alpha sentence") {
		t.Errorf("expected chapter-1 sentence, got %q", c.Text)
	}
	if strings.Contains(c.Text, "var ignore") {
		t.Errorf("script content must be skipped, got %q", c.Text)
	}
}

// TestExtract_EPUBPagination verifies char-based pagination on an EPUB: a small
// MaxChars truncates output, sets HasMore and NextCursor.Char, and a follow-up
// call at that offset continues the document.
func TestExtract_EPUBPagination(t *testing.T) {
	path := buildEPUB(t, t.TempDir())
	c, err := Extract(context.Background(), path, Req{Offset: 0, MaxChars: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(c.Text)) > 20 {
		t.Errorf("text longer than MaxChars: %d runes", len([]rune(c.Text)))
	}
	if !c.HasMore || c.NextCursor.Char != 20 {
		t.Fatalf("want HasMore and NextCursor.Char==20, got %+v", c)
	}
	c2, err := Extract(context.Background(), path, Req{Offset: c.NextCursor.Char, MaxChars: 20})
	if err != nil {
		t.Fatal(err)
	}
	if c2.CharStart != 20 || strings.TrimSpace(c2.Text) == "" {
		t.Errorf("continuation wrong, got %+v", c2)
	}
}

// TestExtract_EPUBNoRootfile verifies that an EPUB whose container.xml declares
// no rootfile is reported as not extractable with a reason, exercising the
// "container.xml has no rootfile" structural-failure branch.
func TestExtract_EPUBNoRootfile(t *testing.T) {
	files := map[string]string{
		"META-INF/container.xml": `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles></rootfiles>
</container>`,
	}
	path := writeEPUB(t, t.TempDir(), "no-rootfile.epub", files)
	c, err := Extract(context.Background(), path, Req{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if c.Extractable {
		t.Fatalf("expected not extractable, got %+v", c)
	}
	if !strings.Contains(c.Reason, "rootfile") {
		t.Errorf("reason should mention the missing rootfile, got %q", c.Reason)
	}
}

// TestExtract_EPUBMalformedOPF verifies that an EPUB whose OPF package document
// is not well-formed XML is reported as not extractable with a reason,
// exercising the parseOPF unmarshal-failure branch.
func TestExtract_EPUBMalformedOPF(t *testing.T) {
	files := map[string]string{
		"META-INF/container.xml": `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`,
		"OEBPS/content.opf": `<?xml version="1.0"?><package><manifest><item`,
	}
	path := writeEPUB(t, t.TempDir(), "bad-opf.epub", files)
	c, err := Extract(context.Background(), path, Req{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if c.Extractable {
		t.Fatalf("expected not extractable, got %+v", c)
	}
	if !strings.Contains(c.Reason, "not a readable EPUB") {
		t.Errorf("reason should note the unreadable EPUB, got %q", c.Reason)
	}
}

// TestExtract_EPUBMissingSpineItem verifies that a spine itemref whose idref is
// absent from the manifest is skipped, while a following valid chapter is still
// extracted: a dangling reference does not abort the read.
func TestExtract_EPUBMissingSpineItem(t *testing.T) {
	files := map[string]string{
		"META-INF/container.xml": `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`,
		"OEBPS/content.opf": `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid">
  <metadata/>
  <manifest>
    <item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine>
    <itemref idref="missing"/>
    <itemref idref="c1"/>
  </spine>
</package>`,
		"OEBPS/chapter1.xhtml": `<html><body><p>Only valid chapter present.</p></body></html>`,
	}
	path := writeEPUB(t, t.TempDir(), "missing-spine-item.epub", files)
	c, err := Extract(context.Background(), path, Req{MaxChars: 500})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !c.Extractable || c.Format != "epub" {
		t.Fatalf("expected extractable epub, got %+v", c)
	}
	if !strings.Contains(c.Text, "Only valid chapter present") {
		t.Errorf("expected the valid chapter's text, got %q", c.Text)
	}
}

// TestExtract_EPUBNoExtractableText verifies that an EPUB whose only spine item
// resolves to no text (a dangling reference) is reported as not extractable
// with a reason noting the empty spine, rather than an empty extractable chunk.
func TestExtract_EPUBNoExtractableText(t *testing.T) {
	files := map[string]string{
		"META-INF/container.xml": `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`,
		"OEBPS/content.opf": `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid">
  <metadata/>
  <manifest/>
  <spine>
    <itemref idref="missing"/>
  </spine>
</package>`,
	}
	path := writeEPUB(t, t.TempDir(), "empty-spine.epub", files)
	c, err := Extract(context.Background(), path, Req{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if c.Extractable {
		t.Fatalf("expected not extractable, got %+v", c)
	}
	if !strings.Contains(c.Reason, "no extractable text") {
		t.Errorf("reason should note the empty spine, got %q", c.Reason)
	}
}

// TestExtract_EPUBOverCapTruncated verifies that an EPUB chapter larger than the
// maxTextFileBytes cap is clipped and the returned Chunk is flagged Truncated
// with the extraction-cap note, so the dropped tail is signaled honestly. The
// oversized chapter is built in-test to avoid committing a large fixture.
func TestExtract_EPUBOverCapTruncated(t *testing.T) {
	big := "<html><body><p>" + strings.Repeat("a", maxTextFileBytes+1) + "</p></body></html>"
	files := map[string]string{
		"META-INF/container.xml": `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`,
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
		"OEBPS/chapter1.xhtml": big,
	}
	path := writeEPUB(t, t.TempDir(), "oversized.epub", files)
	c, err := Extract(context.Background(), path, Req{MaxChars: 100})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !c.Extractable {
		t.Fatalf("expected extractable chunk, got %+v", c)
	}
	if !c.Truncated {
		t.Error("Truncated = false, want true for a chapter over the 8 MiB cap")
	}
	if !strings.Contains(c.Reason, "8 MiB extraction cap") {
		t.Errorf("Reason should note the extraction cap, got %q", c.Reason)
	}
	// Sanity: the clipped chapter still yields some extracted text.
	if len([]rune(c.Text)) == 0 {
		t.Error("expected some extracted text from the clipped chapter")
	}
}
