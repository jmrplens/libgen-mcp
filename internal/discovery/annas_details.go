package discovery

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	xhtml "golang.org/x/net/html"
)

// annasRecordMaxBody bounds how many bytes of a record page are read. Pages run
// from roughly 250 kB to 850 kB depending on how many source codes a record
// carries, so 4 MiB is a generous ceiling.
const annasRecordMaxBody = 4 << 20 // 4 MiB

// annasCodesTabClass marks the anchors that publish a record's metadata as
// label/value pairs. Reading them by label rather than by position keeps the
// parse stable when a record carries a different set of codes — which is the
// normal case, since each source collection contributes its own.
const annasCodesTabClass = "js-md5-codes-tabs-tab"

// AnnasRecord is the metadata Anna's Archive publishes for one md5. It is thinner
// than a Library Genesis catalog record and its fields vary by source collection:
// only Title, Author, Language, Year, ContentType, Collection, Filesize and
// Filepath are carried by every record observed, while ISBNs and an IPFS CID
// appear on a minority (most records have no CID, so the keyless IPFS download
// route is not available for them).
type AnnasRecord struct {
	MD5         string `json:"md5,omitempty"`
	Title       string `json:"title,omitempty"`
	Author      string `json:"author,omitempty"`
	Language    string `json:"language,omitempty"`
	Year        string `json:"year,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Collection  string `json:"collection,omitempty"`
	Extension   string `json:"extension,omitempty"`
	Filesize    string `json:"filesize,omitempty"`
	Filepath    string `json:"filepath,omitempty"`
	ISBN13      string `json:"isbn13,omitempty"`
	ISBN10      string `json:"isbn10,omitempty"`
	IPFSCID     string `json:"ipfs_cid,omitempty"`
}

// Details returns Anna's Archive's own metadata for an md5, tried mirror by
// mirror. It exists so a search result the Library Genesis catalog never had
// still has somewhere to look up metadata: the catalog's json.php knows nothing
// about an md5 that only Anna's indexes.
//
// A nil error guarantees a parsed record. An unreachable or unparseable set of
// mirrors is an error, so a caller can tell "Anna's has no such record" apart
// from "Anna's could not be reached".
func (p *AnnasProvider) Details(ctx context.Context, md5 string) (*AnnasRecord, error) {
	httpClient := p.http
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	var lastErr error
	for _, mirror := range p.mirrors.Mirrors(ctx) {
		base := strings.TrimRight(strings.TrimSpace(mirror), "/")
		body, err := p.fetchRecord(ctx, httpClient, base, md5)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			continue
		}
		if rec := parseAnnasRecord(body, md5); rec != nil {
			return rec, nil
		}
		lastErr = fmt.Errorf("annas record: %q served no record for md5 %s", base, md5)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("annas record: no mirror available for md5 %s", md5)
	}
	return nil, lastErr
}

// fetchRecord requests one mirror's record page and returns its body.
func (p *AnnasProvider) fetchRecord(ctx context.Context, httpClient *http.Client, base, md5 string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/md5/"+md5, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", discoveryUserAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("annas record: %q returned HTTP %d", base, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, annasRecordMaxBody))
}

// parseAnnasRecord extracts a record from a record page, or returns nil when the
// page carries no metadata pairs — which is how an error page, a block page or an
// unrelated page is rejected rather than returned as an empty record.
func parseAnnasRecord(body []byte, md5 string) *AnnasRecord {
	doc, err := xhtml.Parse(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	codes := map[string]string{}
	var title, description string
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		collectRecordNode(n, codes, &title, &description)
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if len(codes) == 0 {
		return nil
	}
	return buildAnnasRecord(md5, title, description, codes)
}

// collectRecordNode harvests one node's contribution to a record: the document
// title, the description meta tag (whose first block is the author), or a
// label/value metadata pair.
func collectRecordNode(n *xhtml.Node, codes map[string]string, title, description *string) {
	if n.Type != xhtml.ElementNode {
		return
	}
	switch n.Data {
	case "title":
		if *title == "" {
			*title = strings.TrimSpace(textOfAnchor(n))
		}
	case "meta":
		if attrValue(n, "name") == "description" {
			*description = attrValue(n, "content")
		}
	case "a":
		if strings.Contains(attrValue(n, "class"), annasCodesTabClass) {
			if label, value, ok := labelValuePair(n); ok {
				if _, seen := codes[label]; !seen {
					codes[label] = value
				}
			}
		}
	}
}

// labelValuePair reads a metadata anchor's two spans: the label and its value.
func labelValuePair(n *xhtml.Node) (label, value string, ok bool) {
	var spans []string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xhtml.ElementNode && c.Data == "span" {
			spans = append(spans, strings.TrimSpace(textOfAnchor(c)))
		}
	}
	if len(spans) < 2 {
		return "", "", false
	}
	return spans[0], spans[1], true
}

// buildAnnasRecord assembles the record from the harvested parts.
func buildAnnasRecord(md5, title, description string, codes map[string]string) *AnnasRecord {
	rec := &AnnasRecord{
		MD5:         md5,
		Title:       trimAnnasTitle(title),
		Author:      firstBlock(description),
		Language:    codes["Language"],
		Year:        codes["Year"],
		ContentType: codes["Content Type"],
		Collection:  codes["Collection"],
		Filesize:    codes["Filesize"],
		Filepath:    codes["Filepath"],
		ISBN13:      codes["ISBN-13"],
		ISBN10:      codes["ISBN-10"],
		IPFSCID:     codes["IPFS CID"],
	}
	rec.Extension = strings.TrimPrefix(strings.ToLower(path.Ext(rec.Filepath)), ".")
	return rec
}

// annasTitleSuffixes are the site-name suffixes appended to a record page's
// document title. The apostrophe is typographic on the live site; the ASCII form
// is accepted too so a mirror that normalizes it is still handled.
var annasTitleSuffixes = []string{" - Anna’s Archive", " - Anna's Archive"}

// trimAnnasTitle strips the site name from a record page's document title.
func trimAnnasTitle(title string) string {
	for _, suffix := range annasTitleSuffixes {
		if trimmed, found := strings.CutSuffix(title, suffix); found {
			return strings.TrimSpace(trimmed)
		}
	}
	return strings.TrimSpace(title)
}

// firstBlock returns the text up to the first blank line. A record page's
// description meta tag leads with the author and then appends free-form material
// (a publisher line, a table of contents, a PDF producer string), so only the
// first block is dependable.
func firstBlock(s string) string {
	block, _, _ := strings.Cut(strings.TrimSpace(s), "\n\n")
	return strings.TrimSpace(block)
}

// attrValue returns an element's attribute value, or "" when it has none.
func attrValue(n *xhtml.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
