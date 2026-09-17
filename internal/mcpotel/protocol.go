package mcpotel

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// metaProtocolVersionKey is where protocol revision 2026-07-28 puts the
// negotiated version on every request.
//
// Unlike the three trace-context keys, this one is DNS-prefixed, because it is
// an ordinary MCP _meta key rather than one of the reserved OpenTelemetry
// exceptions.
const metaProtocolVersionKey = "io.modelcontextprotocol/protocolVersion"

// protocolHeader carries the same value on the HTTP transport, for the
// revisions that predate the per-request _meta form.
const protocolHeader = "MCP-Protocol-Version"

// protocolVersionFor resolves the revision a request is speaking, or "" when it
// cannot be established or is not one this server admits.
//
// # Why an allow-list rather than whatever arrived
//
// The value is written by the caller, and it lands on a metric dimension. An
// unvalidated string from a client is an unbounded label space: a thousand
// clients sending a thousand distinct spellings mint a thousand time series,
// and the SDK's 2000-series limit turns that into an otel.metric.overflow
// bucket that silently swallows the real data. Bounding it to the revisions
// this server actually supports makes the dimension finite by construction,
// which is the same reasoning that governs the tool-name dimension.
//
// An unrecognized value is dropped rather than recorded as "unknown". A request
// speaking a revision this server does not support is refused before it ever
// reaches a handler, so the case does not arise in practice, and inventing a
// bucket for it would suggest otherwise.
//
// # Source order
//
// The per-request _meta key comes first because it is the form revision
// 2026-07-28 defines and the only one that works on stdio. The session's
// initialize parameters come next, which is where the older revisions record
// what was negotiated. The HTTP header is last: it is present only on one
// transport, and a client that sets it can disagree with what it negotiated.
func protocolVersionFor(req mcp.Request, allowed map[string]struct{}) string {
	if req == nil || len(allowed) == 0 {
		return ""
	}
	// In order, and the order is the point: each source is more authoritative
	// than the next. The allow-list is applied to all three alike, so no
	// candidate reaches an attribute without passing it.
	for _, candidate := range []func(mcp.Request) string{
		versionFromMeta,
		versionFromSession,
		versionFromHeader,
	} {
		version := candidate(req)
		if _, admitted := allowed[version]; admitted {
			return version
		}
	}
	return ""
}

// versionFromMeta reads the revision the request itself declares, which is what
// 2026-07-28 defines and the only source that works on stdio.
func versionFromMeta(req mcp.Request) string {
	params := paramsOf(req)
	if params == nil {
		return ""
	}
	version, _ := params.GetMeta()[metaProtocolVersionKey].(string)
	return version
}

// versionFromSession reads what the session negotiated, which is where the
// older revisions record it.
//
// A failed assertion yields a nil session, so the one nil check covers both a
// request of another kind and one built without a session.
func versionFromSession(req mcp.Request) string {
	session, _ := req.GetSession().(*mcp.ServerSession)
	if session == nil {
		return ""
	}
	params := session.InitializeParams()
	if params == nil {
		return ""
	}
	return params.ProtocolVersion
}

// versionFromHeader reads the HTTP header, which is last for two reasons: it is
// present on one transport only, and a client that sets it can disagree with
// what it actually negotiated.
//
// A nil Header reads as empty, and empty is the answer when nothing was found,
// so a request that carried no headers needs no branch of its own.
func versionFromHeader(req mcp.Request) string {
	extra := req.GetExtra()
	if extra == nil {
		return ""
	}
	return extra.Header.Get(protocolHeader)
}

// allowedVersions turns the configured list into a set, tolerating nil.
func allowedVersions(versions []string) map[string]struct{} {
	if len(versions) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(versions))
	for _, v := range versions {
		if v != "" {
			set[v] = struct{}{}
		}
	}
	return set
}
