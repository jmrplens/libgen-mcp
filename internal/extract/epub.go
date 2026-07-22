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

	"golang.org/x/net/html"
)

// containerXML models META-INF/container.xml, whose first rootfile points at
// the OPF package document.
type containerXML struct {
	Rootfiles []struct {
		FullPath string `xml:"full-path,attr"`
	} `xml:"rootfiles>rootfile"`
}

// opfPackage models the OPF manifest (id to href) and spine (reading order).
type opfPackage struct {
	Items []struct {
		ID   string `xml:"id,attr"`
		Href string `xml:"href,attr"`
	} `xml:"manifest>item"`
	ItemRefs []struct {
		IDRef string `xml:"idref,attr"`
	} `xml:"spine>itemref"`
}

// extractEPUB reads an EPUB as a ZIP archive, concatenates the text of its
// spine documents in reading order and returns a character-paginated Chunk. A
// malformed archive yields a not-extractable Chunk.
func extractEPUB(ctx context.Context, filePath string, r Req) (Chunk, error) {
	if err := ctx.Err(); err != nil {
		return Chunk{}, err
	}
	zr, err := zip.OpenReader(filePath)
	if err != nil {
		return Chunk{Format: "epub", Reason: fmt.Sprintf("cannot open EPUB archive: %v", err)}, nil
	}
	defer func() { _ = zr.Close() }()

	full, err := readEPUBText(ctx, zr)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Chunk{}, err
		}
		return Chunk{Format: "epub", Extractable: false, Reason: "not a readable EPUB: " + err.Error()}, nil
	}
	if strings.TrimSpace(full) == "" {
		return Chunk{Format: "epub", Reason: "no extractable text found in EPUB spine"}, nil
	}
	return paginateChars(full, "epub", r), nil
}

// readEPUBText resolves the OPF, walks the spine in order and returns the
// concatenated plain text of all chapter documents.
func readEPUBText(ctx context.Context, zr *zip.ReadCloser) (string, error) {
	opf, err := opfPath(zr)
	if err != nil {
		return "", err
	}
	pkg, err := parseOPF(zr, opf)
	if err != nil {
		return "", err
	}

	hrefByID := make(map[string]string, len(pkg.Items))
	for _, it := range pkg.Items {
		hrefByID[it.ID] = it.Href
	}
	baseDir := path.Dir(opf)

	var sb strings.Builder
	for _, ref := range pkg.ItemRefs {
		if e := ctx.Err(); e != nil {
			return "", e
		}
		href, ok := hrefByID[ref.IDRef]
		if !ok || href == "" {
			continue
		}
		name := path.Join(baseDir, href)
		data, rerr := readZipEntry(zr, name)
		if rerr != nil {
			continue
		}
		sb.WriteString(htmlToText(strings.NewReader(string(data))))
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

// opfPath returns the OPF package path referenced by META-INF/container.xml.
func opfPath(zr *zip.ReadCloser) (string, error) {
	data, err := readZipEntry(zr, "META-INF/container.xml")
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
func parseOPF(zr *zip.ReadCloser, name string) (opfPackage, error) {
	data, err := readZipEntry(zr, name)
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
// avoid unbounded memory use on a malicious archive.
func readZipEntry(zr *zip.ReadCloser, name string) ([]byte, error) {
	var entry *zip.File
	for _, f := range zr.File {
		if f.Name == name {
			entry = f
			break
		}
	}
	if entry == nil {
		return nil, fmt.Errorf("entry not found: %s", name)
	}
	rc, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(io.LimitReader(rc, maxTextFileBytes))
}

// htmlToText tokenizes an XHTML document and returns its visible text,
// skipping the contents of <script> and <style> elements.
func htmlToText(r io.Reader) string {
	z := html.NewTokenizer(r)
	var sb strings.Builder
	skipDepth := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			return sb.String()
		case html.StartTagToken:
			if name, _ := z.TagName(); isSkippedTag(name) {
				skipDepth++
			}
		case html.EndTagToken:
			if name, _ := z.TagName(); isSkippedTag(name) && skipDepth > 0 {
				skipDepth--
			}
		case html.TextToken:
			if skipDepth == 0 {
				sb.Write(z.Text())
			}
		}
	}
}

// isSkippedTag reports whether the tag's text content should be excluded from
// extracted output.
func isSkippedTag(name []byte) bool {
	s := string(name)
	return s == "script" || s == "style"
}
