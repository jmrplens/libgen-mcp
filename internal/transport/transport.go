// Package transport maps this server's HTTP-transport flags onto the SDK's
// streamable-HTTP options, so the binary, the tests and the e2e harness all
// serve over an identically configured transport.
package transport

import "github.com/modelcontextprotocol/go-sdk/mcp"

// Options carries the HTTP-transport tuning flags.
type Options struct {
	// Stateless serves sessionless streamable HTTP (SEP-2567): no
	// Mcp-Session-Id tracking, every POST self-contained, GET/DELETE → 405.
	// Required for MCP protocol 2026-07-28 over HTTP; false restores the
	// legacy session-based transport.
	Stateless bool
	// JSONResponse returns application/json bodies instead of SSE.
	JSONResponse bool
	// MaxRequestBodyBytes caps request bodies; 0 = SDK default (4 MiB).
	MaxRequestBodyBytes int64
	// TrustedOrigins are the browser origins this deployment vouches for, as
	// absolute "scheme://host[:port]" strings, or the single entry AnyOrigin.
	// Empty — the default — refuses every cross-origin browser request, which
	// leaves non-browser clients unaffected since they send no Origin at all.
	TrustedOrigins []string
	// BasePath is the URL path prefix this server answers on, normalized to
	// either "" (the root) or a "/prefix" with no trailing slash. It exists so a
	// deployment behind a reverse proxy that forwards the prefix verbatim —
	// rather than rewriting it away — still resolves its own routes, instead of
	// depending on the proxy to make the paths line up.
	BasePath string
}

// DefaultOptions returns the shipped defaults (stateless on).
func DefaultOptions() Options { return Options{Stateless: true} }

// StreamableHTTP maps the flags onto the SDK options. Cancellation propagation
// is always on: client aborts cancel in-flight mirror fetches, and the SDK
// restricts it to protocol-2026-07-28 requests so legacy clients are unaffected.
func StreamableHTTP(opts Options) *mcp.StreamableHTTPOptions {
	return &mcp.StreamableHTTPOptions{
		Stateless:                    opts.Stateless,
		JSONResponse:                 opts.JSONResponse,
		MaxRequestBodyBytes:          opts.MaxRequestBodyBytes,
		PropagateRequestCancellation: true,
	}
}
