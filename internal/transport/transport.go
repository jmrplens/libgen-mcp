package transport

import (
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
	// ServesTLS reports that this process terminates TLS itself rather than
	// sitting behind a proxy that does. It gates Strict-Transport-Security,
	// which is a claim only the endpoint that actually negotiated the
	// connection is in a position to make.
	ServesTLS bool
	// SessionTimeout closes a stateful session that has gone this long without
	// a request from its client. Zero — the default — never closes one.
	//
	// It is meaningful only when Stateless is false, because a stateless POST's
	// session ends with its own response and there is nothing left to idle.
	// cmd/server refuses the flag outright in stateless mode rather than
	// accepting a setting that cannot do what it says.
	SessionTimeout time.Duration
}

// DefaultOptions returns the shipped defaults (stateless on).
func DefaultOptions() Options { return Options{Stateless: true} }

// StreamableHTTP maps the flags onto the SDK options. Cancellation propagation
// is always on: client aborts cancel in-flight mirror fetches, and the SDK
// restricts it to protocol-2026-07-28 requests so legacy clients are unaffected.
//
// DisableLocalhostProtection turns off the SDK's own DNS-rebinding check, which
// this server answers itself in cmd/server's host guard. **The two halves belong
// in one change**: the SDK's rule is that a connection accepted on a loopback
// address may only carry a loopback Host, which refuses every reverse-proxy
// recipe in the getting-started guide — nginx forwards the client's Host and
// connects over loopback — and refuses it with plain text on a route a
// Streamable HTTP client reads as JSON. Switching it off without the guard
// removes the protection; adding the guard without switching it off leaves the
// SDK refusing what the guard permits, one layer further in.
func StreamableHTTP(opts Options) *mcp.StreamableHTTPOptions {
	return &mcp.StreamableHTTPOptions{
		Stateless:                    opts.Stateless,
		JSONResponse:                 opts.JSONResponse,
		MaxRequestBodyBytes:          opts.MaxRequestBodyBytes,
		SessionTimeout:               opts.SessionTimeout,
		PropagateRequestCancellation: true,
		DisableLocalhostProtection:   true,
	}
}
