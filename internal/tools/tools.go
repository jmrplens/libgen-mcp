// Package tools registers the server's MCP tools: search, get_details and download.
package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

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

// DownloadInput holds the parameters for the download tool.
type DownloadInput struct {
	MD5      string `json:"md5" jsonschema:"file md5 hash from a search result,required"`
	Path     string `json:"path,omitempty" jsonschema:"destination directory (default: LIBGEN_MCP_DOWNLOAD_DIR or ~/Downloads)"`
	Filename string `json:"filename,omitempty" jsonschema:"destination filename (default: name announced by the mirror)"`
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
	mcp.AddTool(server, &mcp.Tool{
		Name:        "download",
		Title:       "Download file",
		Description: "Download a file by md5 to a local directory, resolving the libgen mirror download chain (ads.php key + CDN redirect). Returns the saved path and size.",
		Annotations: &mcp.ToolAnnotations{Title: "Download file", DestructiveHint: &falsy, IdempotentHint: true, OpenWorldHint: &truthy},
	}, withRecovery("download", downloadHandler(client, cfg)))
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
			if !md5Re.MatchString(in.MD5) {
				return nil, zero, errors.New("md5 must be a 32-char hex string")
			}
			file, edition, err := c.DetailsByMD5(ctx, strings.ToLower(in.MD5))
			if err != nil {
				return nil, zero, err
			}
			return nil, DetailsOutput{File: file, Edition: edition}, nil
		case in.ID != "":
			object := "e"
			switch in.Object {
			case "", "edition":
			case "file":
				object = "f"
			default:
				return nil, zero, fmt.Errorf("object must be edition or file, got %q", in.Object)
			}
			rec, err := c.DetailsByID(ctx, object, in.ID)
			if err != nil {
				return nil, zero, err
			}
			if object == "f" {
				return nil, DetailsOutput{File: rec}, nil
			}
			return nil, DetailsOutput{Edition: rec}, nil
		default:
			return nil, zero, errors.New("provide md5 or id")
		}
	}
}

func downloadHandler(c *libgen.Client, cfg *config.Config) mcp.ToolHandlerFor[DownloadInput, libgen.DownloadResult] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in DownloadInput) (*mcp.CallToolResult, libgen.DownloadResult, error) {
		var zero libgen.DownloadResult
		if !md5Re.MatchString(in.MD5) {
			return nil, zero, errors.New("md5 must be a 32-char hex string")
		}
		dir := in.Path
		if dir == "" {
			dir = cfg.DownloadDir
		}
		res, err := c.Download(ctx, strings.ToLower(in.MD5), dir, in.Filename, progressNotifier(ctx, req))
		if err != nil {
			return nil, zero, err
		}
		return nil, *res, nil
	}
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
