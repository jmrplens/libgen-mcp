// Package tools registers the server's MCP tools: search, get_details and download.
package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	Query          string   `json:"query" jsonschema:"search text,required"`
	Topics         []string `json:"topics,omitempty" jsonschema:"collections to search: nonfiction fiction articles magazines comics standards fiction_rus (omit for all)"`
	SearchIn       []string `json:"search_in,omitempty" jsonschema:"fields to match: title author series year publisher isbn (omit for all)"`
	ResultsPerPage int      `json:"results_per_page,omitempty" jsonschema:"results per page: 25 50 or 100"`
	Page           int      `json:"page,omitempty" jsonschema:"result page starting at 1"`
	Order          string   `json:"order,omitempty" jsonschema:"sort by: id time_added title author year size"`
	OrderMode      string   `json:"order_mode,omitempty" jsonschema:"asc or desc"`
}

// SearchOutput holds a page of search results plus pagination metadata.
type SearchOutput struct {
	Results        []libgen.Result `json:"results"`
	Page           int             `json:"page"`
	ResultsPerPage int             `json:"results_per_page"`
	TotalFiles     string          `json:"total_files,omitempty"`
	Reachable      int             `json:"reachable"`
	Truncated      bool            `json:"truncated"`
	Hint           string          `json:"hint,omitempty"`
	HasMore        bool            `json:"has_more"`
	Mirror         string          `json:"mirror"`
}

// DetailsInput holds the parameters for the get_details tool.
type DetailsInput struct {
	MD5    string `json:"md5,omitempty" jsonschema:"file md5 hash from a search result (use md5 OR id, not both)"`
	ID     string `json:"id,omitempty" jsonschema:"edition or file id (use md5 OR id, not both)"`
	Object string `json:"object,omitempty" jsonschema:"with id: edition (default) or file"`
}

// DetailsOutput holds the file and/or edition record returned by get_details.
type DetailsOutput struct {
	File    map[string]any `json:"file,omitempty"`
	Edition map[string]any `json:"edition,omitempty"`
}

// DownloadInput holds the parameters for the download tool. Provide md5 (books)
// or doi (articles); at least one is required.
type DownloadInput struct {
	MD5      string `json:"md5,omitempty" jsonschema:"file md5 hash from a book search result; provide md5 or doi"`
	DOI      string `json:"doi,omitempty" jsonschema:"DOI from an article search result; articles are fetched by DOI; provide md5 or doi"`
	Path     string `json:"path,omitempty" jsonschema:"destination directory (default: LIBGEN_MCP_DOWNLOAD_DIR or ~/Downloads)"`
	Filename string `json:"filename,omitempty" jsonschema:"destination filename (default: a clean name from the record metadata or the name the mirror announces)"`
	Source   string `json:"source,omitempty" jsonschema:"restrict the download to a single source instead of trying all: libgen or randombook for books (md5); unpaywall or scihub for articles (doi). Omit to try every compatible source in order with failover"`
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
		Description: "Full metadata for a Library Genesis record (description, identifiers, DOI, cover, related edition) via its JSON API. Look up by md5 (returns file + related edition) or by edition/file id.",
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
	b.WriteString("Returns the saved path, size and the source that served it.")
	return b.String()
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
		return nil, out, nil
	}
}

func detailsHandler(c *libgen.Client) mcp.ToolHandlerFor[DetailsInput, DetailsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DetailsInput) (*mcp.CallToolResult, DetailsOutput, error) {
		var zero DetailsOutput
		switch {
		case in.MD5 != "" && in.ID != "":
			return nil, zero, errors.New("provide md5 or id, not both")
		case in.MD5 != "":
			out, err := detailsByMD5(ctx, c, in.MD5)
			return nil, out, err
		case in.ID != "":
			out, err := detailsByID(ctx, c, in.Object, in.ID)
			return nil, out, err
		default:
			return nil, zero, errors.New("provide md5 or id")
		}
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

func downloadHandler(c *libgen.Client, cfg *config.Config) mcp.ToolHandlerFor[DownloadInput, libgen.DownloadResult] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in DownloadInput) (*mcp.CallToolResult, libgen.DownloadResult, error) {
		var zero libgen.DownloadResult
		md5, doi, source, err := validateDownloadInput(in)
		if err != nil {
			return nil, zero, err
		}
		dir := in.Path
		if dir == "" {
			dir = cfg.DownloadDir
		}
		item := libgen.Item{MD5: md5, DOI: doi, Source: source}
		// For a book download with no explicit name, fill bibliographic metadata so
		// the file lands under a clean "Author - Title (Year).ext" name. Best-effort:
		// a details lookup failure must not fail the download.
		if md5 != "" && in.Filename == "" {
			item.Meta = bookMeta(ctx, c, md5)
		}
		res, err := c.DownloadItem(ctx, item, dir, in.Filename, progressNotifier(ctx, req))
		if err != nil {
			return nil, zero, err
		}
		return nil, *res, nil
	}
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
