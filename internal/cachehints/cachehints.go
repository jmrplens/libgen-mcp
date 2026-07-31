// Package cachehints stamps SEP-2549 cache hints onto the catalog results this
// server returns, so clients and intermediaries can stop re-fetching a listing
// that only ever changes with a release.
package cachehints

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CatalogTTLMs is how long (in milliseconds) a client may treat the tool and
// prompt catalog as fresh. The surface is compiled in — four tools and a fixed
// prompt set — so it changes only when a new binary ships, and an hour is
// short enough that a redeployed server is picked up promptly anyway.
const CatalogTTLMs = 3_600_000

// Middleware returns receiving middleware that attaches CatalogTTLMs to the
// catalog listings. It deliberately leaves CacheScope alone: the SDK defaults it
// to "public", which is right for a server with no authentication — every client
// is served the identical catalog, so a shared intermediary caching it is a win
// rather than a leak. Results that are not catalog listings pass through
// untouched, since a tool call's payload is live data.
func Middleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if err != nil {
				return res, err
			}
			// The getters have value receivers, so the TTL has to be written
			// through the concrete pointer type rather than the interface.
			switch r := res.(type) {
			case *mcp.ListToolsResult:
				r.TTLMs = CatalogTTLMs
			case *mcp.ListPromptsResult:
				r.TTLMs = CatalogTTLMs
			case *mcp.DiscoverResult:
				r.TTLMs = CatalogTTLMs
			}
			return res, nil
		}
	}
}
