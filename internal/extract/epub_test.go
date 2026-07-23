package extract

import (
	"archive/zip"
	"context"
	"hash/crc32"
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

// writeRawZip writes a ZIP archive under name in dir containing a single entry
// created with CreateRaw, so the test controls the stored CRC and compression
// method. It underpins the readZipEntry error-branch tests, which need an entry
// that opens as a ZIP file but fails when its bytes are read or decompressed.
func writeRawZip(t *testing.T, dir, name, entry string, hdr *zip.FileHeader, content []byte) string {
	t.Helper()
	fp := filepath.Join(dir, name)
	f, err := os.Create(fp)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer func() { _ = f.Close() }()
	zw := zip.NewWriter(f)
	hdr.Name = entry
	hdr.UncompressedSize64 = uint64(len(content))
	hdr.CompressedSize64 = uint64(len(content))
	w, err := zw.CreateRaw(hdr)
	if err != nil {
		t.Fatalf("create raw entry: %v", err)
	}
	if _, err = w.Write(content); err != nil {
		t.Fatalf("write raw entry: %v", err)
	}
	if err = zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return fp
}

// TestExtractEPUB_ContextCancelledDirect verifies extractEPUB's own entry guard:
// called directly with an already-canceled context it returns the context error
// before opening the archive, a checkpoint Extract normally short-circuits.
func TestExtractEPUB_ContextCancelledDirect(t *testing.T) {
	path := buildEPUB(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := extractEPUB(ctx, path, Req{}); err == nil {
		t.Fatal("expected a context error, got nil")
	}
}

// TestExtractEPUB_NotAZip verifies that a .epub whose bytes are not a valid ZIP
// archive is reported as not extractable with a reason noting the archive could
// not be opened, exercising extractEPUB's zip.OpenReader failure branch (which
// TestExtract_MalformedEPUB, using a valid ZIP, does not reach).
func TestExtractEPUB_NotAZip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notazip.epub")
	if err := os.WriteFile(path, []byte("this is not a zip archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Extract(context.Background(), path, Req{})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if c.Extractable {
		t.Fatalf("expected not extractable, got %+v", c)
	}
	if !strings.Contains(c.Reason, "cannot open EPUB archive") {
		t.Errorf("reason should note the archive open failure, got %q", c.Reason)
	}
}

// TestExtractEPUB_ContextCancelledMidSpine verifies that a context which is live
// at extractEPUB's entry but canceled by the time the spine loop runs is
// propagated as the context error: the per-chapter guard in readEPUBText fires
// and extractEPUB returns it rather than a chunk.
func TestExtractEPUB_ContextCancelledMidSpine(t *testing.T) {
	path := buildEPUB(t, t.TempDir())
	if _, err := extractEPUB(passErr(1), path, Req{}); err == nil {
		t.Fatal("expected a context error propagated from the spine walk, got nil")
	}
}

// TestExtractEPUB_MissingChapterFile verifies readEPUBText's read-error skip: a
// spine itemref whose manifest href points at a file absent from the archive is
// skipped (readZipEntry errors), and with no other chapter the EPUB reports no
// extractable text rather than failing.
func TestExtractEPUB_MissingChapterFile(t *testing.T) {
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
		// chapter1.xhtml is deliberately absent from the archive.
	}
	path := writeEPUB(t, t.TempDir(), "missing-chapter.epub", files)
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

// TestExtractEPUB_MalformedContainerXML verifies opfPath's XML-parse failure
// branch: a container.xml that is not well-formed XML makes the EPUB not
// extractable with a "not a readable EPUB" reason.
func TestExtractEPUB_MalformedContainerXML(t *testing.T) {
	files := map[string]string{
		"META-INF/container.xml": `<?xml version="1.0"?><container><rootfiles><rootfile`,
	}
	path := writeEPUB(t, t.TempDir(), "bad-container.epub", files)
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

// TestExtractEPUB_MissingOPFFile verifies parseOPF's read-error branch: a
// container.xml pointing at an OPF path that is absent from the archive makes
// the EPUB not extractable with a "not a readable EPUB" reason.
func TestExtractEPUB_MissingOPFFile(t *testing.T) {
	files := map[string]string{
		"META-INF/container.xml": `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`,
		// OEBPS/content.opf is deliberately absent.
	}
	path := writeEPUB(t, t.TempDir(), "missing-opf.epub", files)
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

// TestReadZipEntry_ChecksumError verifies readZipEntry's read-failure branch: an
// entry stored with a deliberately wrong CRC opens but fails its integrity check
// on read, so readZipEntry returns a non-nil error.
func TestReadZipEntry_ChecksumError(t *testing.T) {
	content := []byte("some chapter bytes")
	path := writeRawZip(t, t.TempDir(), "badcrc.epub", "OEBPS/c1.xhtml",
		&zip.FileHeader{Method: zip.Store, CRC32: 0xDEADBEEF}, content)
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer func() { _ = zr.Close() }()
	if _, _, err = readZipEntry(zr, "OEBPS/c1.xhtml"); err == nil {
		t.Fatal("expected a checksum error, got nil")
	}
}

// TestReadZipEntry_OpenError verifies readZipEntry's open-failure branch: an
// entry stored with an unregistered compression method fails when the archive
// tries to open its decompressor, so readZipEntry returns a non-nil error.
func TestReadZipEntry_OpenError(t *testing.T) {
	content := []byte("hello")
	path := writeRawZip(t, t.TempDir(), "badmethod.epub", "OEBPS/c1.xhtml",
		&zip.FileHeader{Method: 99, CRC32: crc32.ChecksumIEEE(content)}, content)
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer func() { _ = zr.Close() }()
	if _, _, err = readZipEntry(zr, "OEBPS/c1.xhtml"); err == nil {
		t.Fatal("expected an open/decompress error, got nil")
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
