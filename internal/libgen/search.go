package libgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// paginatorRe extracts the total page count and per-page size from the top
// Paginator init script, e.g. `new Paginator("paginator_example_top", 6, 25, ...`.
var paginatorRe = regexp.MustCompile(`new Paginator\("paginator_example_top",\s*(\d+),\s*(\d+),`)

// doiRe captures the DOI printed in an article's first cell, e.g. the text
// "DOI: 10.14311/nnw.2016.26.006". Article rows expose the DOI so the model can
// hand it to the download tool, which resolves articles by DOI.
var doiRe = regexp.MustCompile(`(?i)DOI:\s*(\S+)`)

// ErrLayoutChanged indicates that the page does not have the expected structure:
// not to be confused with "zero results".
var ErrLayoutChanged = errors.New("libgen page layout not recognized (site may have changed)")

var (
	topicCodes  = map[string]string{"nonfiction": "l", "fiction": "f", "articles": "a", "magazines": "m", "comics": "c", "standards": "s", "fiction_rus": "r"}
	columnCodes = map[string]string{"title": "t", "author": "a", "series": "s", "year": "y", "publisher": "p", "isbn": "i"}
	orderCodes  = map[string]string{"id": "f_id", "time_added": "time_added", "title": "title", "author": "author", "year": "year", "size": "filesize"}
)

// joinInts renders an int set the way allowed renders a map's keys, so both
// validation messages read the same.
func joinInts(values []int) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ", ")
}

func allowed[V any](m map[string]V) string {
	return strings.Join(sortedKeys(m), ", ")
}

// TopicNames returns the recognized search topic names in a stable order, for
// surfacing the collection choices to callers (e.g. in no-result guidance).
func TopicNames() []string {
	return []string{"nonfiction", "fiction", "articles", "magazines", "comics", "standards", "fiction_rus"}
}

// SearchFieldNames returns the recognized search_in field names, sorted. It reads
// the same map Validate checks against, so a schema enum built from it cannot
// drift away from what the validator will actually accept.
func SearchFieldNames() []string {
	return sortedKeys(columnCodes)
}

// OrderNames returns the recognized order names, sorted. Like SearchFieldNames it
// reads the validator's own map rather than restating the set.
func OrderNames() []string {
	return sortedKeys(orderCodes)
}

// orderModes and resultsPerPageValues are the closed sets Validate enforces and
// the schema advertises. They live here, as the maps above do, so the validator
// and the tool schema cannot drift: a value added to one is added to both.
var (
	orderModes           = []string{"asc", "desc"}
	resultsPerPageValues = []int{25, 50, 100}
)

// OrderModeNames returns the recognized order_mode values.
func OrderModeNames() []string {
	return slices.Clone(orderModes)
}

// ResultsPerPageValues returns the page sizes the catalog accepts.
func ResultsPerPageValues() []int {
	return slices.Clone(resultsPerPageValues)
}

// sortedKeys returns a map's keys in sorted order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// SearchParams holds the parameters of a catalog search.
type SearchParams struct {
	Query          string
	Topics         []string
	SearchIn       []string
	ResultsPerPage int
	Page           int
	Order          string
	OrderMode      string
}

// Validate reports whether the search parameters are well-formed, rejecting
// unknown topics, columns, ordering and out-of-range page sizes.
func (p SearchParams) Validate() error {
	if strings.TrimSpace(p.Query) == "" {
		return errors.New("query is required")
	}
	for _, t := range p.Topics {
		if _, ok := topicCodes[t]; !ok {
			return fmt.Errorf("unknown topic %q (allowed: %s)", t, allowed(topicCodes))
		}
	}
	for _, c := range p.SearchIn {
		if _, ok := columnCodes[c]; !ok {
			return fmt.Errorf("unknown search_in %q (allowed: %s)", c, allowed(columnCodes))
		}
	}
	if p.ResultsPerPage != 0 && !slices.Contains(resultsPerPageValues, p.ResultsPerPage) {
		return fmt.Errorf("results_per_page must be one of %s", joinInts(resultsPerPageValues))
	}
	if p.Order != "" {
		if _, ok := orderCodes[p.Order]; !ok {
			return fmt.Errorf("unknown order %q (allowed: %s)", p.Order, allowed(orderCodes))
		}
	}
	if p.OrderMode != "" && !slices.Contains(orderModes, p.OrderMode) {
		return fmt.Errorf("order_mode must be one of %s", strings.Join(orderModes, ", "))
	}
	return nil
}

func (p SearchParams) values() url.Values {
	v := url.Values{}
	v.Set("req", p.Query)
	for _, t := range p.Topics {
		v.Add("topics[]", topicCodes[t])
	}
	for _, c := range p.SearchIn {
		v.Add("columns[]", columnCodes[c])
	}
	if p.ResultsPerPage != 0 {
		v.Set("res", strconv.Itoa(p.ResultsPerPage))
	}
	if p.Page > 1 {
		v.Set("page", strconv.Itoa(p.Page))
	}
	if p.Order != "" {
		v.Set("order", orderCodes[p.Order])
	}
	if p.OrderMode != "" {
		v.Set("ordermode", p.OrderMode)
	}
	return v
}

// DownloadOption is a single labeled download link for a result.
type DownloadOption struct {
	Label string `json:"label" jsonschema:"human label for this download link"`
	URL   string `json:"url" jsonschema:"direct download URL for this option"`
}

// Result is one catalog entry from a search page, with its metadata and download
// options. The identifier fields are the pivot keys for the other tools.
type Result struct {
	EditionID string   `json:"edition_id,omitempty" jsonschema:"edition id; pass to get_details as id (with object=edition)"`
	FileID    string   `json:"file_id,omitempty" jsonschema:"file id; pass to get_details as id with object=file"`
	MD5       string   `json:"md5" jsonschema:"file MD5 hash (32 hex chars); pass to get_details or download to fetch this book"`
	DOI       string   `json:"doi,omitempty" jsonschema:"article DOI; pass to download to fetch this article"`
	Title     string   `json:"title" jsonschema:"record title"`
	Issue     string   `json:"issue,omitempty" jsonschema:"volume/issue designator for a journal, magazine or comic record (e.g. vol. 26 iss. 2); absent for books"`
	Edition   string   `json:"edition,omitempty" jsonschema:"edition marker for this record (e.g. 1, 1st ed), kept out of the title so the title compares cleanly"`
	ISBNs     []string `json:"isbns,omitempty" jsonschema:"ISBNs for this record, if any; absent for articles, whose identifier is the doi field"`
	Authors   string   `json:"authors,omitempty" jsonschema:"authors"`
	Publisher string   `json:"publisher,omitempty" jsonschema:"publisher"`
	Year      string   `json:"year,omitempty" jsonschema:"publication year"`
	Language  string   `json:"language,omitempty" jsonschema:"language"`
	Pages     string   `json:"pages,omitempty" jsonschema:"page count"`
	Size      string   `json:"size,omitempty" jsonschema:"human-readable file size"`
	Extension string   `json:"extension,omitempty" jsonschema:"file extension (e.g. pdf, epub)"`
	Type      string   `json:"type,omitempty" jsonschema:"record type"`
	// Origin labels which searcher produced this record, so a merged result list
	// can tell a catalog hit from one federated in from elsewhere. "libgen" for
	// catalog results; other searchers stamp their own name.
	Origin    string           `json:"origin,omitempty" jsonschema:"which searcher produced this record: libgen for the catalog, annas for Anna's Archive"`
	Downloads []DownloadOption `json:"downloads" jsonschema:"labeled download links; prefer the download tool, which handles mirrors and verification"`
}

// SearchPage is a parsed page of search results plus the total file count.
//
// libgen.li advertises TotalFiles as the full match count but only serves the
// first Reachable results across pages. When the advertised total exceeds that
// cap the search is Truncated and the caller should refine the query.
type SearchPage struct {
	Results    []Result `json:"results"`
	TotalFiles string   `json:"total_files,omitempty"`
	Reachable  int      `json:"reachable"`
	Truncated  bool     `json:"truncated"`
}

// Search runs the search and returns the parsed page and the mirror used.
func (c *Client) Search(ctx context.Context, p SearchParams) (*SearchPage, string, error) {
	if err := p.Validate(); err != nil {
		return nil, "", err
	}
	body, base, err := c.get(ctx, "/index.php", p.values())
	if err != nil {
		return nil, "", err
	}
	page, err := ParseSearch(bytes.NewReader(body), base)
	if err != nil {
		return nil, "", err
	}
	return page, base, nil
}

// ParseSearch parses the results page. base absolutizes the relative links.
func ParseSearch(r io.Reader, base string) (*SearchPage, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parsing search page: %w", err)
	}
	page := &SearchPage{TotalFiles: filesTabCount(doc)}
	page.Reachable = paginatorReach(doc)
	page.Truncated = isTruncated(page.TotalFiles, page.Reachable)
	table := findByID(doc, "tablelibgen")
	if table == nil {
		if page.TotalFiles == "0" {
			return page, nil // valid search with no results
		}
		return nil, ErrLayoutChanged
	}
	for _, tr := range elements(table, "tr") {
		cells := childElements(tr, "td")
		if len(cells) < 9 {
			continue // header or another auxiliary row
		}
		if res := parseRow(cells, base); res != nil {
			page.Results = append(page.Results, *res)
		}
	}
	return page, nil
}

// isTruncated reports whether a search returned more matches than are reachable
// across pages. totalFiles is the Files-tab counter, which libgen renders either
// as a plain number ("38514") or as a capped indicator ("1000+") when the true
// count is large. A trailing non-digit suffix (e.g. the '+') is stripped before
// parsing: if the parsed number exceeds reachable the search is truncated; and a
// counter that stays non-numeric yet non-empty while some results are reachable is
// itself a capped-display signal, so it is treated as truncated conservatively (a
// capped display implies there is more than what is shown).
func isTruncated(totalFiles string, reachable int) bool {
	if reachable <= 0 {
		return false
	}
	trimmed := strings.TrimRight(strings.TrimSpace(totalFiles), "+")
	if i := strings.IndexFunc(trimmed, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		trimmed = trimmed[:i]
	}
	if trimmed == "" {
		// Non-numeric but non-empty original (e.g. "many"): a capped display that
		// implies more results exist than are reachable.
		return strings.TrimSpace(totalFiles) != ""
	}
	total, err := strconv.Atoi(trimmed)
	if err != nil || total <= 0 {
		return false
	}
	return total > reachable
}

func parseRow(cells []*html.Node, base string) *Result {
	r := Result{Origin: "libgen"}
	parseIdentifiers(cells[0], &r)
	if m := doiRe.FindStringSubmatch(nodeText(cells[0])); m != nil {
		r.DOI = strings.TrimSpace(m[1])
	}
	r.Authors = strings.TrimSpace(nodeText(cells[1]))
	r.Publisher = strings.TrimSpace(nodeText(cells[2]))
	r.Year = strings.TrimSpace(nodeText(cells[3]))
	r.Language = strings.TrimSpace(nodeText(cells[4]))
	r.Pages = strings.TrimSpace(nodeText(cells[5]))
	r.Size = strings.TrimSpace(nodeText(cells[6]))
	for _, a := range elements(cells[6], "a") {
		if strings.Contains(attr(a, "href"), "file.php?id=") {
			r.FileID = queryParam(attr(a, "href"), "id")
			break
		}
	}
	r.Extension = strings.TrimSpace(nodeText(cells[7]))
	parseDownloads(cells[8], base, &r)
	if r.MD5 == "" && r.Title == "" {
		return nil
	}
	return &r
}

// editionLinkKind labels what an edition.php link in the first cell holds. The
// cell carries between one and three of them and their order is not stable across
// topics — a book leads with its title, an article or a comic leads with its
// issue designator — so each link is classified by its markup instead of by its
// position.
type editionLinkKind int

const (
	linkIgnore      editionLinkKind = iota // empty text, or a DOI already parsed from the cell
	linkTitle                              // part of the record's title
	linkIssue                              // a volume/issue designator, rendered wholly in italics
	linkIdentifiers                        // the green identifier list (ISBNs)
)

// parseIdentifiers extracts title, edition id, issue designator, ISBNs and type
// from the first cell. Title parts are joined in document order because a
// standards row splits its title across two links (the standard number and its
// subject); a row whose only designator is an issue (a magazine issue with no
// article title) keeps that designator as its title, since it is the record's
// only identity.
func parseIdentifiers(cell *html.Node, r *Result) {
	var titleParts []string
	for _, a := range elements(cell, "a") {
		href := attr(a, "href")
		if !strings.Contains(href, "edition.php?id=") {
			continue
		}
		if r.EditionID == "" {
			r.EditionID = queryParam(href, "id")
		}
		text := strings.TrimSpace(nodeText(a))
		switch classifyEditionLink(a, text) {
		case linkTitle:
			title, edition := splitEditionMarker(text, italicText(a))
			if title != "" {
				titleParts = append(titleParts, title)
			}
			if r.Edition == "" {
				r.Edition = edition
			}
		case linkIssue:
			if r.Issue == "" {
				r.Issue = text
			}
		case linkIdentifiers:
			if r.ISBNs == nil {
				r.ISBNs = splitISBNs(text)
			}
		case linkIgnore:
		}
	}
	r.Title = strings.Join(titleParts, " ")
	if r.Title == "" {
		r.Title, r.Issue = r.Issue, ""
	}
	if t := badgeType(cell); t != "" {
		r.Type = t
	}
}

// classifyEditionLink labels one edition.php link by its markup. The identifier
// link is the one libgen prints in green: it holds ISBNs for a book and the DOI
// for an article, and the DOI is already parsed from the cell as a whole, so it
// is ignored here rather than filed under ISBNs. An issue designator is rendered
// wholly in italics ("vol. 26 iss. 2", "#1966"), which is what distinguishes it
// from a title link that merely ends with an italic edition marker.
func classifyEditionLink(a *html.Node, text string) editionLinkKind {
	if text == "" {
		return linkIgnore
	}
	if isGreen(a) {
		if strings.HasPrefix(strings.ToUpper(text), "DOI:") {
			return linkIgnore
		}
		return linkIdentifiers
	}
	if italicText(a) == text {
		return linkIssue
	}
	return linkTitle
}

// splitEditionMarker separates a title link's trailing edition marker — the
// italic run libgen closes a title with, "1" or "1st ed" — from the title
// itself, so a title compares cleanly against a real one instead of carrying a
// stray ordinal. A link with no marker returns its text unchanged.
func splitEditionMarker(text, italic string) (title, edition string) {
	if italic == "" || !strings.HasSuffix(text, italic) {
		return text, ""
	}
	return strings.TrimSpace(strings.TrimSuffix(text, italic)), italic
}

// isGreen reports whether n contains a font element libgen colored green, the
// marker it uses for a row's identifier list.
func isGreen(n *html.Node) bool {
	for _, f := range elements(n, "font") {
		if strings.EqualFold(strings.TrimSpace(attr(f, "color")), "green") {
			return true
		}
	}
	return false
}

// italicText returns the whitespace-collapsed text of every italic element under
// n, joined in document order.
func italicText(n *html.Node) string {
	var parts []string
	for _, i := range elements(n, "i") {
		if t := strings.TrimSpace(nodeText(i)); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// splitISBNs splits a semicolon-separated identifier string into its non-empty,
// trimmed parts. It returns nil when no identifier is present.
func splitISBNs(text string) []string {
	var out []string
	for s := range strings.SplitSeq(text, ";") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// badgeType returns the trimmed text of the first span in cell whose class marks
// it as the primary badge (the result type), or "" when none is present.
func badgeType(cell *html.Node) string {
	for _, s := range elements(cell, "span") {
		if strings.Contains(attr(s, "class"), "badge-primary") {
			return strings.TrimSpace(nodeText(s))
		}
	}
	return ""
}

// parseDownloads extracts the md5 and download options from the last cell.
func parseDownloads(cell *html.Node, base string, r *Result) {
	for _, a := range elements(cell, "a") {
		href := attr(a, "href")
		label := attr(a, "title")
		// The primary libgen download link is rendered as ads.php?md5= for most
		// topics, but the comics topic (and the occasional row elsewhere) renders it
		// as a direct get.php?md5= link instead. Recognize either form so every
		// topic yields the md5 and a "libgen" download option.
		if strings.Contains(href, "ads.php?md5=") || strings.Contains(href, "get.php?md5=") {
			r.MD5 = strings.ToLower(queryParam(href, "md5"))
			if strings.HasPrefix(href, "/") {
				href = base + href
			}
			label = "libgen"
		}
		if href != "" {
			r.Downloads = append(r.Downloads, DownloadOption{Label: label, URL: href})
		}
	}
}

// filesTabCount returns the counter of the "Files" tab ("138", "1000+", "0")
// or "" if not found.
func filesTabCount(doc *html.Node) string {
	for _, a := range elements(doc, "a") {
		if !strings.Contains(attr(a, "class"), "nav-link") || !strings.Contains(attr(a, "href"), "curtab=f") {
			continue
		}
		for _, s := range elements(a, "span") {
			if strings.Contains(attr(s, "class"), "badge") {
				return strings.TrimSpace(nodeText(s))
			}
		}
	}
	return ""
}

// paginatorReach returns the number of results actually reachable across pages
// (totalPages * perPage) as declared by the top Paginator init script, or 0 if
// the script is absent or unparsable.
func paginatorReach(doc *html.Node) int {
	for _, s := range elements(doc, "script") {
		m := paginatorRe.FindStringSubmatch(nodeText(s))
		if m == nil {
			continue
		}
		pages, err1 := strconv.Atoi(m[1])
		per, err2 := strconv.Atoi(m[2])
		if err1 != nil || err2 != nil {
			return 0
		}
		return pages * per
	}
	return 0
}

// --- DOM helpers ---

func findByID(n *html.Node, id string) *html.Node {
	if n.Type == html.ElementNode && attr(n, "id") == id {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findByID(c, id); found != nil {
			return found
		}
	}
	return nil
}

// elements returns all descendants with the given tag.
func elements(n *html.Node, tag string) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(m *html.Node) {
		if m != n && m.Type == html.ElementNode && m.Data == tag {
			out = append(out, m)
		}
		for c := m.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

// childElements returns only the direct child elements with the given tag.
func childElements(n *html.Node, tag string) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == tag {
			out = append(out, c)
		}
	}
	return out
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(m *html.Node) {
		if m.Type == html.TextNode {
			b.WriteString(m.Data)
		}
		for c := m.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func queryParam(href, key string) string {
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	return u.Query().Get(key)
}
