// protocolversion.go answers one branch the SDK still refuses in plain text.

package main

import (
	"net/http"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// protocolVersionHeader is the header the transport specification negotiates
// the revision with.
const protocolVersionHeader = "MCP-Protocol-Version"

// protocolVersionStatelessOnly is the first revision the streamable transport
// serves only in stateless mode.
const protocolVersionStatelessOnly = "2026-07-28"

// negotiableProtocolVersions is what this deployment can actually negotiate:
// the SDK's own list, narrowed the way the SDK narrows it.
//
// The list is read from [mcp.SupportedProtocolVersions] rather than copied, so
// an SDK bump that adds or drops a revision moves this with it instead of
// leaving a stale constant that advertises a version nothing serves.
//
// The narrowing is the SDK's rule from StreamableServerTransport
// .SupportsProtocolVersion: every revision at or above 2026-07-28 needs a
// stateless transport, because SEP-2575 has no session concept to fall back on.
// A stateful deployment that listed one would be handing a client the single
// answer that cannot work — and the whole point of the error below is to say
// what to retry with.
func negotiableProtocolVersions(stateless bool) []string {
	supported := mcp.SupportedProtocolVersions()
	if stateless {
		return supported
	}
	narrowed := make([]string, 0, len(supported))
	for _, version := range supported {
		if version < protocolVersionStatelessOnly {
			narrowed = append(narrowed, version)
		}
	}
	return narrowed
}

// protocolVersionGuarded answers an unsupported MCP-Protocol-Version with the
// error the transport specification names, in place of the SDK's plain text.
//
// The specification says a server that does not implement the requested version
// MUST respond 400 with an UnsupportedProtocolVersionError listing what it does
// support. The SDK produces that natively for revisions sorting at or above
// 2026-07-28; anything older gets `http.Error`. That difference is the one that
// matters, because the specification's own backward-compatibility rule tells a
// client that a 400 whose body is not a recognizable JSON-RPC error means an
// initialization-era server — so it falls back to the withdrawn HTTP+SSE
// transport, issues a GET, and a stateless deployment answers 405. The client
// ends with no transport rather than with the one retry the list would have
// given it.
//
// A request with **no** version header passes through untouched, and that is
// the whole subtlety: it may be an initialize for any revision, which is
// exactly what the SDK's own condition says.
func protocolVersionGuarded(stateless bool, next http.Handler) http.Handler {
	supported := negotiableProtocolVersions(stateless)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested := r.Header.Get(protocolVersionHeader)
		if requested == "" || slices.Contains(supported, requested) {
			next.ServeHTTP(w, r)
			return
		}
		refusal{
			status:  http.StatusBadRequest,
			code:    codeUnsupportedProtocolVersion,
			message: "unsupported protocol version",
			data: unsupportedVersionData{
				Supported: supported,
				Requested: requested,
			},
		}.write(w, r)
	})
}

// unsupportedVersionData carries what the client needs to retry: the revisions
// this deployment negotiates, and the one it asked for.
type unsupportedVersionData struct {
	Supported []string `json:"supported"`
	Requested string   `json:"requested"`
}
