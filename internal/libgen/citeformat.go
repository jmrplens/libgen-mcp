// Formatted citations and registry records, negotiated from doi.org.

package libgen

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// doiOrgBase is the DOI resolver root. doi.org forwards a negotiated request to
// the registration agency that owns the DOI (Crossref, DataCite, mEDRA...), and
// each of them formats its own records, which is what makes one request shape
// work for every DOI. It is a package variable so tests can repoint it.
var doiOrgBase = "https://doi.org"

// doiOrgURL returns the DOI resolver root for this client.
func (c *Client) doiOrgURL() string {
	if c.doiOrgBaseOverride != "" {
		return c.doiOrgBaseOverride
	}
	return doiOrgBase
}

// CiteStyleCSLJSON is the style name that asks for the record itself as
// CSL-JSON rather than for a formatted reference.
const CiteStyleCSLJSON = "csl-json"

// citeStyleIDs maps each style a caller may ask for to the CSL style id doi.org
// is asked for. The ids were chosen by asking doi.org for each candidate on
// 2026-10-03, for a Crossref journal article, a Crossref book chapter, a
// DataCite dataset and a mEDRA article:
//
//   - chicago is the author-date style. chicago-notes-bibliography answered
//     the mEDRA DOI with an empty 204.
//   - harvard is Elsevier's. harvard-cite-them-right, like
//     modern-language-association, renders an "Edited by ," for an editor
//     Crossref records with no name, and Elsevier's has no editor slot.
//   - vancouver is Elsevier's too. doi.org knows no style called vancouver,
//     and the Taylor and Francis NLM style shows the same empty editor.
//
// mla keeps the artifact, as no MLA style avoids it, and
// cleanNegotiatedCitation removes it.
var citeStyleIDs = map[string]string{
	"apa":       "apa",
	"mla":       "modern-language-association",
	"chicago":   "chicago-author-date",
	"harvard":   "elsevier-harvard",
	"vancouver": "elsevier-vancouver",
	"ieee":      "ieee",
}

// CiteStyleNames lists the styles get_details accepts in cite_as, in the order
// its schema advertises them.
func CiteStyleNames() []string {
	return []string{"apa", "mla", "chicago", "harvard", "vancouver", "ieee", CiteStyleCSLJSON}
}

// negotiateTimeout bounds a whole set of negotiated formats. The formats are
// fetched concurrently, and each answered in 0.3 to 1.2 seconds when measured,
// so this is a ceiling for a stalled registry rather than a budget the normal
// case comes near.
const negotiateTimeout = 8 * time.Second

// negotiatedMaxBody bounds a formatted reference. One reference is a few
// hundred bytes, so anything near this is not a reference.
const negotiatedMaxBody = 64 << 10

// FormatDOI asks doi.org for the given styles of a DOI, concurrently, and
// returns the text of each style that answered. A style missing from the map
// failed (no answer, an unknown style, an empty body): the caller decides what
// to fall back on. It never returns an error, as every failure is per style.
//
// The text is the registry's and is untrusted: markup and entities are
// removed, control bytes dropped and whitespace collapsed to one line, but a
// renderer must still escape it for where it lands.
func (c *Client) FormatDOI(ctx context.Context, doi string, styles []string) map[string]string {
	ctx, cancel := context.WithTimeout(ctx, negotiateTimeout)
	defer cancel()
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out = make(map[string]string, len(styles))
	)
	for _, style := range styles {
		wg.Go(func() {
			text := c.negotiateStyle(ctx, doi, style)
			if text == "" {
				return
			}
			mu.Lock()
			out[style] = text
			mu.Unlock()
		})
	}
	wg.Wait()
	return out
}

// negotiateStyle fetches one style of one DOI, "" on any failure.
func (c *Client) negotiateStyle(ctx context.Context, doi, style string) string {
	if style == CiteStyleCSLJSON {
		item := c.FetchCSL(ctx, doi)
		if item == nil {
			return ""
		}
		return item.JSON()
	}
	id, ok := citeStyleIDs[style]
	if !ok {
		return ""
	}
	resp := c.enrichGetAccept(ctx, c.doiOrgURL()+"/"+escapeDOIPath(doi),
		"text/x-bibliography; style="+id, c.enrichLimiter)
	if resp == nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	// A resolver that does not negotiate answers with the landing page, which
	// is no reference however much of it survives the markup stripping.
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); !strings.HasPrefix(ct, "text/x-bibliography") &&
		!strings.HasPrefix(ct, "text/plain") {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, negotiatedMaxBody))
	if err != nil {
		return ""
	}
	return cleanNegotiatedCitation(string(body))
}

// negotiatedMarkup is an HTML tag in a formatted reference. DataCite answers
// text/x-bibliography with <i> around titles and &amp; for the ampersand,
// where Crossref answers plain text, so both are reduced to plain text.
var negotiatedMarkup = regexp.MustCompile(`<[^>]*>`)

// negotiatedNumberLabel is the citation number a numeric style puts in front
// of an entry ("[1]", "1."). It counts a position in a reference list this
// entry is not in, so it is dropped.
var negotiatedNumberLabel = regexp.MustCompile(`^(\[\d+\]|\d+\.)\s*`)

// negotiatedEmptyEditor is the editor phrase Crossref's formatter writes for
// an editor recorded with no name: "Nature, edited by , vol. 500". It is never
// a real editor, since a name would stand between the words and the comma.
var negotiatedEmptyEditor = regexp.MustCompile(`(?i),\s*edited by\s*,`)

// cleanNegotiatedCitation reduces a negotiated reference to one plain line:
// markup and entities out, whitespace collapsed, control and format characters
// (a bidirectional override among them) dropped, the
// citation number and an empty editor phrase removed. An answer that is not a
// reference (a JSON error document, an HTML page) comes back empty.
func cleanNegotiatedCitation(s string) string {
	s = negotiatedMarkup.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	s = negotiatedNumberLabel.ReplaceAllString(s, "")
	s = negotiatedEmptyEditor.ReplaceAllString(s, ",")
	if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
		return ""
	}
	return s
}

// CSLItem is the part of a registry's CSL-JSON record this server passes on:
// the CSL variables a reference is built from, without the reference list,
// the licenses, the indexing dates and the rest of what Crossref sends beside
// them.
type CSLItem map[string]any

// cslKeptVariables are the CSL-JSON variables a CSLItem keeps.
var cslKeptVariables = []string{
	"type", "DOI", "title", "subtitle", "container-title", "collection-title",
	"author", "editor", "issued", "volume", "issue", "page", "number", "edition",
	"publisher", "publisher-place", "ISBN", "ISSN", "URL", "version", "genre", "language",
}

// cslPersonKeys are the name parts kept for each author or editor, dropping
// Crossref's affiliations, sequence flags and role vocabularies.
var cslPersonKeys = []string{"family", "given", "literal", "suffix", "non-dropping-particle", "dropping-particle"}

// FetchCSL asks doi.org for a DOI's record as CSL-JSON, which every major
// registration agency serves, and keeps the CSL variables that build a
// reference. It returns nil when nothing usable answered.
func (c *Client) FetchCSL(ctx context.Context, doi string) *CSLItem {
	ctx, cancel := context.WithTimeout(ctx, negotiateTimeout)
	defer cancel()
	resp := c.enrichGetAccept(ctx, c.doiOrgURL()+"/"+escapeDOIPath(doi),
		"application/vnd.citationstyles.csl+json", c.enrichLimiter)
	if resp == nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	return parseCSL(io.LimitReader(resp.Body, enrichMaxBody))
}

// parseCSL decodes a CSL-JSON record and keeps the variables a reference is
// built from. A record with no title is no record to cite, so it is nil.
func parseCSL(r io.Reader) *CSLItem {
	var raw map[string]any
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil
	}
	item := CSLItem{}
	for _, key := range cslKeptVariables {
		v, ok := raw[key]
		if !ok || v == nil || v == "" {
			continue
		}
		if key == "author" || key == "editor" {
			if people := cslPeople(v); len(people) > 0 {
				item[key] = people
			}
			continue
		}
		item[key] = v
	}
	if item.String("title") == "" {
		return nil
	}
	return &item
}

// cslPeople keeps the name parts of each person in a CSL name list.
func cslPeople(v any) []any {
	list, _ := v.([]any)
	out := make([]any, 0, len(list))
	for _, p := range list {
		person, ok := p.(map[string]any)
		if !ok {
			continue
		}
		kept := map[string]any{}
		for _, k := range cslPersonKeys {
			if s, isString := person[k].(string); isString && strings.TrimSpace(s) != "" {
				kept[k] = s
			}
		}
		if len(kept) > 0 {
			out = append(out, kept)
		}
	}
	return out
}

// String returns a CSL variable as one line of text. Crossref sends title and
// container-title as strings in CSL-JSON, other agencies as arrays, so the
// first element of an array is taken.
func (m CSLItem) String(key string) string {
	switch v := m[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// Year returns the first date-part of issued, 0 when there is none.
func (m CSLItem) Year() int {
	issued, _ := m["issued"].(map[string]any)
	parts, _ := issued["date-parts"].([]any)
	if len(parts) == 0 {
		return 0
	}
	first, _ := parts[0].([]any)
	if len(first) == 0 {
		return 0
	}
	year, _ := first[0].(float64)
	return int(year)
}

// Authors returns the authors as "Given Family" names, a literal name as it
// is.
func (m CSLItem) Authors() []string {
	people, _ := m["author"].([]any)
	names := make([]string, 0, len(people))
	for _, p := range people {
		person, _ := p.(map[string]any)
		if lit, _ := person["literal"].(string); lit != "" {
			names = append(names, lit)
			continue
		}
		given, _ := person["given"].(string)
		family, _ := person["family"].(string)
		if name := strings.TrimSpace(given + " " + family); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// IsArticle reports whether the record is a journal or proceedings article.
// Crossref writes its own type name (journal-article) where the CSL
// specification says article-journal, so both spellings are read.
func (m CSLItem) IsArticle() bool {
	switch m.String("type") {
	case "article-journal", "journal-article", "paper-conference", "proceedings-article", "article":
		return true
	}
	return false
}

// JSON is the item as compact CSL-JSON, with keys in a stable order.
func (m CSLItem) JSON() string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]any(m)); err != nil {
		return ""
	}
	return strings.TrimSpace(b.String())
}
