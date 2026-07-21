// Package tools registers the server's MCP tools: search, get_details and download.
package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/logging"
)

var md5Re = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

const searchDescription = `Search the Library Genesis catalog. Returns file results with
metadata, md5 hash and download options. Allowed values:
- topics: nonfiction, fiction, articles, magazines, comics, standards, fiction_rus (omit = all collections)
- search_in: title, author, series, year, publisher, isbn (omit = all fields)
- results_per_page: 25, 50, 100 (default 25)
- order: id, time_added, title, author, year, size; order_mode: asc, desc
Use get_details with a result md5 for full metadata, and download to fetch the file.`

// SearchInput holds the parameters for the search tool.
type SearchInput struct {
	Query          string   `json:"query" jsonschema:"search text (e.g. a title, author, or ISBN),required"`
	Topics         []string `json:"topics,omitempty" jsonschema:"array of collections to search: nonfiction fiction articles magazines comics standards fiction_rus (omit for all). Use fiction for novels comics for graphic novels articles for research papers"`
	SearchIn       []string `json:"search_in,omitempty" jsonschema:"array of fields to match: title author series year publisher isbn (omit to match all fields)"`
	ResultsPerPage int      `json:"results_per_page,omitempty" jsonschema:"a single number: 25 50 or 100 (default 25)"`
	Page           int      `json:"page,omitempty" jsonschema:"result page number starting at 1 (default 1)"`
	Order          string   `json:"order,omitempty" jsonschema:"a single value (not an array) to sort by: id time_added title author year or size"`
	OrderMode      string   `json:"order_mode,omitempty" jsonschema:"a single value (not an array): asc or desc"`
}

// SearchOutput holds a page of search results plus pagination metadata. NextSteps
// leads so the model sees what to do with the results before reading them.
type SearchOutput struct {
	NextSteps      []string        `json:"next_steps,omitempty" jsonschema:"suggested follow-up tool calls given these results (e.g. get_details or download with a result's md5/doi)"`
	Results        []libgen.Result `json:"results" jsonschema:"the file records on this page; each carries the md5/doi/id you pass to get_details or download"`
	Page           int             `json:"page" jsonschema:"the page number returned"`
	ResultsPerPage int             `json:"results_per_page" jsonschema:"the page size in effect"`
	TotalFiles     string          `json:"total_files,omitempty" jsonschema:"total matches the mirror reports (may be a capped indicator such as 1000+)"`
	Reachable      int             `json:"reachable" jsonschema:"how many results are actually reachable across all pages"`
	Truncated      bool            `json:"truncated" jsonschema:"true when total_files exceeds reachable, i.e. some matches cannot be paged to"`
	Hint           string          `json:"hint,omitempty" jsonschema:"present only when truncated: advises how to refine the query"`
	HasMore        bool            `json:"has_more" jsonschema:"true when this page is full, suggesting a next page may exist"`
	Mirror         string          `json:"mirror" jsonschema:"the mirror base URL that served this search"`
}

// DetailsInput holds the parameters for the get_details tool.
type DetailsInput struct {
	MD5    string `json:"md5,omitempty" jsonschema:"file md5 hash from a search result (use md5 OR id, not both). Get it from a prior search result's md5 field"`
	ID     string `json:"id,omitempty" jsonschema:"edition or file id from a search result (use md5 OR id, not both). Get it from a result's edition_id or file_id field"`
	Object string `json:"object,omitempty" jsonschema:"with id: a single value edition (default) or file"`
}

// DetailsOutput holds the file and/or edition record returned by get_details.
// NextSteps leads so the model sees the download follow-up before the payload.
type DetailsOutput struct {
	NextSteps []string       `json:"next_steps,omitempty" jsonschema:"suggested follow-up (e.g. download this record by its md5 or doi)"`
	File      map[string]any `json:"file,omitempty" jsonschema:"the file record (present for an md5 lookup, or an id lookup with object=file)"`
	Edition   map[string]any `json:"edition,omitempty" jsonschema:"the edition record (present for an md5 lookup's related edition, or an id lookup with object=edition)"`
}

// ResolvedLink is the result of a resolve-only download: a direct URL the caller
// fetches itself, instead of the server writing a file to its own disk. It is
// what a remote/hosted deployment returns, since the server cannot write to the
// client's machine — the client (or an agent's own fetch tool) retrieves the URL.
type ResolvedLink struct {
	URL       string            `json:"url" jsonschema:"the direct URL to download the file from"`
	Source    string            `json:"source" jsonschema:"the source that resolved the URL: libgen, randombook, unpaywall or scihub"`
	Filename  string            `json:"filename,omitempty" jsonschema:"a suggested filename for the saved file"`
	MIMEType  string            `json:"mime_type,omitempty" jsonschema:"the likely content type of the file"`
	Headers   map[string]string `json:"headers,omitempty" jsonschema:"request headers to set when fetching the URL (e.g. Referer for sci-hub); absent when the URL is fetchable as-is"`
	VerifyMD5 bool              `json:"verify_md5" jsonschema:"true when the fetched bytes should hash to the requested md5 (book downloads)"`
}

// DownloadOutput wraps the download result with leading NextSteps guidance. In
// the default (fetch) mode the embedded DownloadResult's fields are promoted (the
// saved file's path, size, source, …); in resolve-only mode Resolved carries the
// direct URL instead and the DownloadResult fields stay zero.
type DownloadOutput struct {
	NextSteps []string      `json:"next_steps,omitempty" jsonschema:"suggested follow-up now that the file is saved (or the link resolved)"`
	Resolved  *ResolvedLink `json:"resolved,omitempty" jsonschema:"present only when resolve_only was set: the direct URL to fetch instead of a saved file"`
	libgen.DownloadResult
}

// DownloadInput holds the parameters for the download tool. Provide md5 (books)
// or doi (articles); at least one is required.
type DownloadInput struct {
	MD5         string `json:"md5,omitempty" jsonschema:"file md5 hash from a book search result; provide md5 or doi"`
	DOI         string `json:"doi,omitempty" jsonschema:"DOI from an article search result; articles are fetched by DOI; provide md5 or doi"`
	Path        string `json:"path,omitempty" jsonschema:"destination directory (default: LIBGEN_MCP_DOWNLOAD_DIR or ~/Downloads). Ignored when resolve_only is true"`
	Filename    string `json:"filename,omitempty" jsonschema:"destination filename (default: a clean name from the record metadata or the name the mirror announces)"`
	Source      string `json:"source,omitempty" jsonschema:"restrict the download to a single source instead of trying all: libgen or randombook for books (md5); unpaywall or scihub for articles (doi). Omit to try every compatible source in order with failover"`
	ResolveOnly bool   `json:"resolve_only,omitempty" jsonschema:"when true, RESOLVE the direct download URL and return it as a link WITHOUT downloading — use this when the server runs remotely from the user (a hosted/HTTP deployment cannot write to the client's disk), or to hand the URL to your own fetch/HTTP tool. When false (default), the file is downloaded to the server's disk (correct for a local stdio/Docker server, where that is the user's machine)"`
}

// Register wires the search, get_details and download tools onto the MCP server,
// each wrapped with panic recovery and call metrics.
func Register(server *mcp.Server, client *libgen.Client, cfg *config.Config) {
	truthy, falsy := true, false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search",
		Title:       "Search Library Genesis",
		Description: searchDescription,
		Annotations: &mcp.ToolAnnotations{Title: "Search Library Genesis", ReadOnlyHint: true, OpenWorldHint: &truthy},
	}, withRecovery("search", searchHandler(client)))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_details",
		Title:       "Get record details",
		Description: "Full metadata for a Library Genesis record (description, identifiers, DOI, cover, related edition) via its JSON API. Look up by md5 (returns file + related edition) or by edition/file id. The md5/id come from a prior search result. See also: search (to find records), download (to fetch the file).",
		Annotations: &mcp.ToolAnnotations{Title: "Get record details", ReadOnlyHint: true, OpenWorldHint: &truthy},
	}, withRecovery("get_details", detailsHandler(client)))
	book, article := client.EnabledSourceNames()
	mcp.AddTool(server, &mcp.Tool{
		Name:        "download",
		Title:       "Download file",
		Description: downloadToolDescription(book, article),
		InputSchema: downloadInputSchema(orderedEnabledSources(book, article)),
		Annotations: &mcp.ToolAnnotations{Title: "Download file", DestructiveHint: &falsy, IdempotentHint: true, OpenWorldHint: &truthy},
	}, withRecovery("download", downloadHandler(client, cfg)))
}

// orderedEnabledSources merges the enabled book (md5) and article (doi) source
// names into a single list in canonical chain order (config.KnownSources), for
// the download tool's source enum.
func orderedEnabledSources(book, article []string) []string {
	present := make(map[string]bool, len(book)+len(article))
	for _, n := range book {
		present[n] = true
	}
	for _, n := range article {
		present[n] = true
	}
	out := make([]string, 0, len(present))
	for _, n := range config.KnownSources {
		if present[n] {
			out = append(out, n)
		}
	}
	return out
}

// downloadInputSchema infers the download tool's input schema from DownloadInput
// (exactly as AddTool would) and constrains the source property to the enabled
// sources: an enum so the model cannot select a disabled provider, plus a matching
// description. A nil result makes AddTool fall back to the default inferred schema
// (no enum), which only happens if inference of the static struct ever fails.
func downloadInputSchema(enabled []string) *jsonschema.Schema {
	schema, err := jsonschema.For[DownloadInput](nil)
	if err != nil {
		return nil
	}
	if src := schema.Properties["source"]; src != nil && len(enabled) > 0 {
		src.Enum = make([]any, len(enabled))
		for i, n := range enabled {
			src.Enum[i] = n
		}
		src.Description = "restrict the download to a single enabled source: " + strings.Join(enabled, ", ") +
			". Omit to try every compatible source in order with failover"
	}
	return schema
}

// sourceChainSep joins ordered source names in the download tool's prose, so the
// text reads "libgen then randombook".
const sourceChainSep = " then "

// downloadToolDescription renders the download tool's prose from the enabled book
// (md5) and article (doi) sources, so disabled providers are never advertised to
// the model. At least one source is always enabled.
func downloadToolDescription(book, article []string) string {
	var b strings.Builder
	b.WriteString("Download a file to a local directory. ")
	switch {
	case len(book) > 0 && len(article) > 0:
		b.WriteString("Provide md5 for a book or doi for an article (at least one is required). ")
		fmt.Fprintf(&b, "Books are tried against %s; articles against %s. ", strings.Join(book, sourceChainSep), strings.Join(article, sourceChainSep))
		b.WriteString("If both md5 and doi are given, article sources are tried first, then book sources. ")
	case len(book) > 0:
		b.WriteString("Provide the md5 of a book (article/doi sources are disabled). ")
		fmt.Fprintf(&b, "Books are tried against %s. ", strings.Join(book, sourceChainSep))
	case len(article) > 0:
		b.WriteString("Provide the doi of an article (book/md5 sources are disabled). ")
		fmt.Fprintf(&b, "Articles are tried against %s. ", strings.Join(article, sourceChainSep))
	}
	fmt.Fprintf(&b, "Set source to restrict the download to a single enabled provider (%s) instead of trying them all. ",
		strings.Join(orderedEnabledSources(book, article), ", "))
	b.WriteString("The md5/doi come from a prior search result. Returns the saved path, size and the source that served it. ")
	b.WriteString("Set resolve_only=true to instead get the direct download URL back (as a link) WITHOUT downloading — use this when the server runs remotely from you (it cannot write to your disk), or to fetch the file with your own tool. ")
	b.WriteString("See also: search (to find the md5/doi).")
	return b.String()
}

// hintIncludeLinks tells the model to surface the results' download links to the
// user when it presents them, so the links are not dropped from the reply.
const hintIncludeLinks = "When you present these results to the user, include each result's download links as clickable [label](url) Markdown links (they are in the results' downloads field and the Markdown table) so the user can navigate directly."

// resultsHaveLinks reports whether any result carries at least one download link.
func resultsHaveLinks(results []libgen.Result) bool {
	for _, r := range results {
		for _, d := range r.Downloads {
			if d.URL != "" {
				return true
			}
		}
	}
	return false
}

// searchNextSteps builds the follow-up guidance for a search result, embedding a
// concrete, ready-to-run example that uses the first result's real identifier so
// the model can pivot to get_details/download without guessing the argument shape.
// On zero results it returns recovery suggestions instead.
func searchNextSteps(out SearchOutput) []string {
	if len(out.Results) == 0 {
		return []string{
			"No matches. Broaden the query text, drop search_in field filters, or try other topics: " +
				strings.Join(libgen.TopicNames(), ", ") + ".",
		}
	}
	first := out.Results[0]
	steps := []string{}
	if first.MD5 != "" {
		steps = append(steps,
			fmt.Sprintf("For full metadata on a result, call get_details with its md5, e.g. {\"md5\":%q}.", first.MD5),
			fmt.Sprintf("To fetch a book, call download with its md5, e.g. {\"md5\":%q}.", first.MD5))
	}
	if first.DOI != "" {
		steps = append(steps,
			fmt.Sprintf("To fetch an article, call download with its doi, e.g. {\"doi\":%q}.", first.DOI))
	}
	if resultsHaveLinks(out.Results) {
		steps = append(steps, hintIncludeLinks)
	}
	if out.Truncated {
		steps = append(steps, "Many matches are unreachable; refine the query (add author/year or narrow topics) rather than deep-paging.")
	} else if out.HasMore {
		steps = append(steps, fmt.Sprintf("This page is full; request page %d for more results.", out.Page+1))
	}
	return steps
}

// detailsNextSteps suggests the download follow-up for a details record, using
// the md5/doi found on the record so the model can act without re-deriving them.
func detailsNextSteps(out DetailsOutput) []string {
	md5 := stringField(out.File, "md5")
	doi := stringField(out.File, "doi")
	if doi == "" {
		doi = stringField(out.Edition, "doi")
	}
	switch {
	case md5 != "":
		return []string{fmt.Sprintf("To download this book, call download with {\"md5\":%q}.", md5)}
	case doi != "":
		return []string{fmt.Sprintf("To download this article, call download with {\"doi\":%q}.", doi)}
	default:
		return []string{"To fetch the file, call download with this record's md5 (book) or doi (article)."}
	}
}

// downloadNextSteps confirms the saved file and points at the next natural action.
func downloadNextSteps(res libgen.DownloadResult) []string {
	return []string{
		fmt.Sprintf("File saved to %s (%d bytes) via %s; it is ready to open or read.", res.Path, res.SizeBytes, res.Source),
	}
}

// withRecovery wraps a typed MCP tool handler to make it panic-safe and
// observable. A panic is recovered and converted into an IsError tool result
// (with a nil Go error and a zero-value output) so it never escapes to kill the
// stdio JSON-RPC session. Every invocation, on any path, emits a ToolCall
// metric line with the elapsed time; a recovered panic is reported to that
// metric as a non-nil error so failures stay visible.
func withRecovery[In, Out any](name string, h mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (result *mcp.CallToolResult, output Out, err error) {
		start := time.Now()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("tool handler panicked", "tool", name, "panic", r, "stack", debug.Stack())
				var zero Out
				output = zero
				result = &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("tool %q failed unexpectedly: %v", name, r)}},
				}
				err = nil
				logging.ToolCall(name, start, fmt.Errorf("tool %q panicked: %v", name, r))
				return
			}
			logging.ToolCall(name, start, err)
		}()
		return h(ctx, req, in)
	}
}

func searchHandler(c *libgen.Client) mcp.ToolHandlerFor[SearchInput, SearchOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
		var zero SearchOutput
		params := libgen.SearchParams{
			Query:          in.Query,
			Topics:         in.Topics,
			SearchIn:       in.SearchIn,
			ResultsPerPage: in.ResultsPerPage,
			Page:           in.Page,
			Order:          in.Order,
			OrderMode:      in.OrderMode,
		}
		page, mirror, err := c.Search(ctx, params)
		if err != nil {
			return nil, zero, err
		}
		per := in.ResultsPerPage
		if per == 0 {
			per = 25
		}
		curPage := in.Page
		if curPage == 0 {
			curPage = 1
		}
		out := SearchOutput{
			Results:        page.Results,
			Page:           curPage,
			ResultsPerPage: per,
			TotalFiles:     page.TotalFiles,
			Reachable:      page.Reachable,
			Truncated:      page.Truncated,
			HasMore:        len(page.Results) >= per,
			Mirror:         mirror,
		}
		if page.Truncated {
			out.Hint = fmt.Sprintf("Only the first %d of %s results are reachable; "+
				"refine your query (add author/year, use title-only columns, or narrow topics).",
				page.Reachable, page.TotalFiles)
		}
		if out.Results == nil {
			out.Results = []libgen.Result{}
		}
		out.NextSteps = searchNextSteps(out)
		return markdownResult(renderSearchMarkdown(out)), out, nil
	}
}

// markdownResult wraps a human-readable Markdown rendering in a CallToolResult.
// The SDK keeps this Content and additionally sets StructuredContent to the
// output JSON, so the client receives both channels.
func markdownResult(md string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: md}}}
}

func detailsHandler(c *libgen.Client) mcp.ToolHandlerFor[DetailsInput, DetailsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DetailsInput) (*mcp.CallToolResult, DetailsOutput, error) {
		var zero DetailsOutput
		var (
			out DetailsOutput
			err error
		)
		switch {
		case in.MD5 != "" && in.ID != "":
			return nil, zero, errors.New("provide md5 or id, not both")
		case in.MD5 != "":
			out, err = detailsByMD5(ctx, c, in.MD5)
		case in.ID != "":
			out, err = detailsByID(ctx, c, in.Object, in.ID)
		default:
			return nil, zero, errors.New("provide md5 or id")
		}
		if err != nil {
			return nil, zero, err
		}
		out.NextSteps = detailsNextSteps(out)
		return markdownResult(renderDetailsMarkdown(out)), out, nil
	}
}

// detailsByMD5 validates the md5 and returns the file plus its related edition.
func detailsByMD5(ctx context.Context, c *libgen.Client, md5 string) (DetailsOutput, error) {
	if !md5Re.MatchString(md5) {
		return DetailsOutput{}, errors.New("md5 must be a 32-char hex string")
	}
	file, edition, err := c.DetailsByMD5(ctx, strings.ToLower(md5))
	if err != nil {
		return DetailsOutput{}, err
	}
	return DetailsOutput{File: file, Edition: edition}, nil
}

// detailsByID resolves a record by edition ("e", default) or file ("f") id,
// mapping the caller-facing object name to the API code.
func detailsByID(ctx context.Context, c *libgen.Client, objectName, id string) (DetailsOutput, error) {
	object := "e"
	switch objectName {
	case "", "edition":
	case "file":
		object = "f"
	default:
		return DetailsOutput{}, fmt.Errorf("object must be edition or file, got %q", objectName)
	}
	rec, err := c.DetailsByID(ctx, object, id)
	if err != nil {
		return DetailsOutput{}, err
	}
	if object == "f" {
		return DetailsOutput{File: rec}, nil
	}
	return DetailsOutput{Edition: rec}, nil
}

// validateDownloadInput normalizes and validates the download request, returning
// the cleaned md5, doi and source (source is "" when unset). At least one of md5
// or doi is required; md5 must be 32-hex; source, when set, must be a known one.
func validateDownloadInput(in DownloadInput) (md5, doi, source string, err error) {
	md5 = strings.ToLower(strings.TrimSpace(in.MD5))
	doi = strings.TrimSpace(in.DOI)
	source = strings.ToLower(strings.TrimSpace(in.Source))
	switch {
	case md5 == "" && doi == "":
		return "", "", "", errors.New("provide md5 (book) or doi (article)")
	case md5 != "" && !md5Re.MatchString(md5):
		return "", "", "", errors.New("md5 must be a 32-char hex string")
	case source != "" && !slices.Contains(config.KnownSources, source):
		return "", "", "", fmt.Errorf("source must be one of %s, got %q", strings.Join(config.KnownSources, ", "), in.Source)
	}
	return md5, doi, source, nil
}

func downloadHandler(c *libgen.Client, cfg *config.Config) mcp.ToolHandlerFor[DownloadInput, DownloadOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in DownloadInput) (*mcp.CallToolResult, DownloadOutput, error) {
		var zero DownloadOutput
		md5, doi, source, err := validateDownloadInput(in)
		if err != nil {
			return nil, zero, err
		}
		item := libgen.Item{MD5: md5, DOI: doi, Source: source}
		// For a book with no explicit name, fill bibliographic metadata so the file
		// gets a clean "Author - Title (Year).ext" name. Best-effort: a details
		// lookup failure must not fail the request.
		if md5 != "" && in.Filename == "" {
			item.Meta = bookMeta(ctx, c, md5)
		}

		if in.ResolveOnly {
			return resolveDownload(ctx, c, item, in.Filename)
		}

		dir := in.Path
		if dir == "" {
			dir = cfg.DownloadDir
		}
		res, err := c.DownloadItem(ctx, item, dir, in.Filename, progressNotifier(ctx, req))
		if err != nil {
			return nil, zero, err
		}
		out := DownloadOutput{NextSteps: downloadNextSteps(*res), DownloadResult: *res}
		return markdownResult(renderDownloadMarkdown(out)), out, nil
	}
}

// resolveDownload handles the resolve_only path: it resolves the direct URL
// without fetching, and returns it as a resource_link content block plus
// structured output, so a remote client or an agent's own fetch tool can retrieve
// the file itself.
func resolveDownload(ctx context.Context, c *libgen.Client, item libgen.Item, filename string) (*mcp.CallToolResult, DownloadOutput, error) {
	var zero DownloadOutput
	r, err := c.ResolveLink(ctx, item)
	if err != nil {
		return nil, zero, err
	}
	link := ResolvedLink{
		URL:       r.URL,
		Source:    r.Source,
		Filename:  resolveFilename(item, filename, r.Ext),
		MIMEType:  mimeForExt(r.Ext, item),
		Headers:   headerMap(r.Header),
		VerifyMD5: r.VerifyMD5,
	}
	out := DownloadOutput{NextSteps: resolveNextSteps(link), Resolved: &link}
	res := &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ResourceLink{URI: link.URL, Name: link.Filename, MIMEType: link.MIMEType, Title: link.Filename},
		&mcp.TextContent{Text: renderResolvedMarkdown(link)},
	}}
	return res, out, nil
}

// resolveFilename picks a filename for a resolved link: an explicit filename, a
// clean "Author - Title (Year).ext" from bibliographic metadata, or the
// identifier plus extension.
func resolveFilename(item libgen.Item, explicit, ext string) string {
	if explicit != "" {
		return explicit
	}
	if ext == "" {
		if item.DOI != "" {
			ext = "pdf" // articles resolve to PDFs
		}
	}
	if m := item.Meta; m != nil && strings.TrimSpace(m.Title) != "" {
		name := m.Title
		if strings.TrimSpace(m.Author) != "" {
			name = m.Author + " - " + m.Title
		}
		if strings.TrimSpace(m.Year) != "" {
			name += " (" + m.Year + ")"
		}
		return sanitizeName(name) + extSuffix(ext)
	}
	base := item.MD5
	if base == "" {
		base = sanitizeName(item.DOI)
	}
	return base + extSuffix(ext)
}

// extSuffix returns ".ext" for a non-empty extension, else "".
func extSuffix(ext string) string {
	if ext == "" {
		return ""
	}
	return "." + strings.TrimPrefix(ext, ".")
}

// sanitizeName strips path-hostile characters from a filename component.
func sanitizeName(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) {
			return '-'
		}
		return r
	}, strings.TrimSpace(s))
}

// mimeForExt maps a file extension (and the item kind) to a likely content type.
func mimeForExt(ext string, item libgen.Item) string {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "pdf":
		return "application/pdf"
	case "epub":
		return "application/epub+zip"
	case "mobi":
		return "application/x-mobipocket-ebook"
	case "djvu":
		return "image/vnd.djvu"
	case "cbr":
		return "application/vnd.comicbook-rar"
	case "cbz":
		return "application/vnd.comicbook+zip"
	case "txt":
		return "text/plain"
	case "":
		if item.DOI != "" {
			return "application/pdf" // articles resolve to PDFs
		}
		return "application/octet-stream"
	default:
		return "application/octet-stream"
	}
}

// headerMap flattens the required request headers into a plain map (first value
// per key), or nil when none are needed.
func headerMap(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k := range h {
		if v := h.Get(k); v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// resolveNextSteps guides the caller on how to fetch the resolved URL.
func resolveNextSteps(link ResolvedLink) []string {
	step := "Download the file by fetching this URL: " + link.URL
	if len(link.Headers) > 0 {
		step += " — set these request headers when fetching: " + headerList(link.Headers) + "."
	} else {
		step += " — it is fetchable directly (open it, or pass it to your HTTP/fetch tool)."
	}
	steps := []string{step}
	if link.VerifyMD5 {
		steps = append(steps, "After downloading, verify the bytes' MD5 matches the requested md5 (this is a book source).")
	}
	return steps
}

// renderResolvedMarkdown renders a resolved link as a short human-readable block.
func renderResolvedMarkdown(link ResolvedLink) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Resolved a download link via **%s** (not downloaded — fetch it yourself):\n", mdCell(link.Source))
	fmt.Fprintf(&b, "- URL: %s\n", link.URL)
	if link.Filename != "" {
		fmt.Fprintf(&b, "- Suggested filename: %s\n", mdCell(link.Filename))
	}
	if len(link.Headers) > 0 {
		fmt.Fprintf(&b, "- Required headers: %s\n", mdCell(headerList(link.Headers)))
	}
	writeNextSteps(&b, resolveNextSteps(link))
	return b.String()
}

// headerList renders a header map as "Key: value" pairs joined by "; ", in a
// stable order.
func headerList(h map[string]string) string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+h[k])
	}
	return strings.Join(parts, "; ")
}

// bookMeta fetches bibliographic fields for a book md5 to build a clean download
// filename. It is best-effort: any lookup error returns nil so the download still
// proceeds and falls back to the mirror-announced name or the md5. Title, author
// and year come from the related edition record; the extension from the file
// record.
func bookMeta(ctx context.Context, c *libgen.Client, md5 string) *libgen.FileMeta {
	file, edition, err := c.DetailsByMD5(ctx, md5)
	if err != nil {
		return nil
	}
	meta := &libgen.FileMeta{
		Title:  stringField(edition, "title"),
		Author: stringField(edition, "author"),
		Year:   stringField(edition, "year"),
		Ext:    stringField(file, "extension"),
	}
	if meta.Title == "" && meta.Author == "" && meta.Year == "" && meta.Ext == "" {
		return nil
	}
	return meta
}

// stringField reads a trimmed string value from a details record map, returning
// "" when the map is nil, the key is absent, or the value is not a string.
func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// progressNotifier builds a libgen.ProgressFunc that forwards download progress
// to the client as MCP notifications/progress, keyed by the progress token the
// client supplied in the request's _meta. When the client sent no token it
// returns nil (a no-op) so no notifications are emitted. Emission errors are
// ignored: progress is best-effort and must never fail the download.
func progressNotifier(ctx context.Context, req *mcp.CallToolRequest) libgen.ProgressFunc {
	token := req.Params.GetProgressToken()
	if token == nil {
		return nil
	}
	session := req.Session
	return func(done, total int64) {
		_ = session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
			ProgressToken: token,
			Progress:      float64(done),
			Total:         float64(total),
		})
	}
}
