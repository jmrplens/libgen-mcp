package libgen

import "errors"

// ErrServerFetchDisabled is returned by the two entry points that pull a file's
// BODY — DownloadItem (the download tool's save-to-disk) and FetchToTemp (the
// read tool's fetch) — on a deployment whose operator has not allowed the server
// to fetch files. LIBGEN_MCP_SERVER_FETCH governs it, defaulting to off for a
// remote/hosted server and on for a local stdio one; internal/config's
// ResolveServerFetch holds why those defaults differ.
//
// The message names both the variable that lifts the refusal and the tool that
// still works, because an operator reading it in a log and a model reading it in
// a tool result need different halves of the same sentence. On a deployment that
// hides read this error should be unreachable through MCP: the tool is not
// registered, so nothing can call it. It is the second line, not the first.
//
// # Why resolving a link is deliberately NOT gated
//
// The guard stops the file body and nothing else. Resolving still talks to the
// mirrors — libgen's key= token is read off ads.php, randombook discovers a
// mirror first, and the article sources ask their registrars where a PDF lives —
// and search and get_details query the catalogs outright. All of that stays
// allowed, for three reasons:
//
//   - It is what the property is actually about. A hosted deployment's egress IP
//     is shared by everyone it serves, and what gets such an address throttled or
//     blocked is sustained multi-megabyte transfers, not a handful of small HTML
//     and redirect requests. Moving the body to the client confines a block to
//     whoever provoked it; moving a page fetch would not.
//   - Without it download cannot work at all. The URL it returns is only
//     obtainable by asking the mirror for it, so a gate on resolution would leave
//     the tool with nothing to do and the deployment with nothing to offer.
//   - The catalog queries cannot move client-side either without emptying the
//     server of its purpose, so gating resolution while keeping search would draw
//     the line in a place that buys nothing.
var ErrServerFetchDisabled = errors.New(
	"this deployment does not fetch file bytes (LIBGEN_MCP_SERVER_FETCH is off): " +
		"use the download tool, which returns a link for you to fetch yourself",
)

// ensureFetchAllowed reports whether this client may pull a file's body,
// returning ErrServerFetchDisabled when it may not. It is checked before any
// network work so a refused call makes no request at all.
func (c *Client) ensureFetchAllowed() error {
	if !c.serverFetch {
		return ErrServerFetchDisabled
	}
	return nil
}
