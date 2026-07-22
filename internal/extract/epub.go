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

	full, truncated, err := readEPUBText(ctx, zr)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Chunk{}, err
		}
		return Chunk{Format: "epub", Extractable: false, Reason: "not a readable EPUB: " + err.Error()}, nil
	}
	if strings.TrimSpace(full) == "" {
		return Chunk{Format: "epub", Reason: "no extractable text found in EPUB spine"}, nil
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
func readEPUBText(ctx context.Context, zr *zip.ReadCloser) (text string, truncated bool, err error) {
	opf, err := opfPath(zr)
	if err != nil {
		return "", false, err
	}
	pkg, err := parseOPF(zr, opf)
	if err != nil {
		return "", false, err
	}

	hrefByID := make(map[string]string, len(pkg.Items))
	for _, it := range pkg.Items {
		hrefByID[it.ID] = it.Href
	}
	baseDir := path.Dir(opf)

	var sb strings.Builder
	for _, ref := range pkg.Spine.ItemRefs {
		if e := ctx.Err(); e != nil {
			return "", false, e
		}
		href, ok := hrefByID[ref.IDRef]
		if !ok || href == "" {
			continue
		}
		name := path.Join(baseDir, href)
		data, clipped, rerr := readZipEntry(zr, name)
		if rerr != nil {
			continue
		}
		if clipped {
			truncated = true
		}
		sb.WriteString(htmlToText(strings.NewReader(string(data))))
		sb.WriteByte('\n')
	}
	return sb.String(), truncated, nil
}

// opfPath returns the OPF package path referenced by META-INF/container.xml.
func opfPath(zr *zip.ReadCloser) (string, error) {
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
func parseOPF(zr *zip.ReadCloser, name string) (opfPackage, error) {
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
func readZipEntry(zr *zip.ReadCloser, name string) (data []byte, clipped bool, err error) {
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
