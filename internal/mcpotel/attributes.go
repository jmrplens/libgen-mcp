package mcpotel

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// The attribute keys this package emits, written out rather than imported.
//
// The MCP semantic convention does not ship as a Go package. It lives in the
// open-telemetry/semantic-conventions-genai repository, not on
// opentelemetry.io, whose /docs/specs/semconv/mcp/ returns 404, and that
// repository has no tags and no releases. The frozen semconv/v1.41.0 module
// does contain some of these constants, but it is a snapshot of a convention
// that has since moved and been removed from Go semconv, so importing it would
// give a false sense of currency and would put two semconv versions in one
// build, which invites a schema URL conflict.
//
// The cost of writing them out is that a rename upstream produces no compile
// error. The mitigation is that they are all here, in one block, so a rename is
// a one-file change, and the convention is re-read before each release.
const (
	// TransportPipe and TransportTCP are the convention's own vocabulary for
	// network.transport, not names of our choosing: its "Recording MCP
	// transport" table gives "pipe" for stdio and "tcp" for streamable HTTP.
	TransportPipe = "pipe"
	TransportTCP  = "tcp"

	// AttrMCPMethodName is Required: every span and every measurement carries it.
	AttrMCPMethodName = attribute.Key("mcp.method.name")

	// AttrMCPProtocolVersion is Recommended: the negotiated revision string.
	AttrMCPProtocolVersion = attribute.Key("mcp.protocol.version")

	// AttrGenAIToolName is Conditionally Required when the operation relates to
	// a specific tool. Note that the convention reuses the gen_ai namespace
	// here; there is no mcp.tool.name, which is a thing to check rather than
	// assume, because the obvious guess is wrong.
	AttrGenAIToolName = attribute.Key("gen_ai.tool.name")

	// AttrMCPSessionID is Recommended, and its note is a condition rather than
	// a preference: "When the MCP request or notification is part of a
	// session." The default HTTP mode is stateless and has no session id, so
	// the condition is simply not met there and the attribute is omitted rather
	// than filled with a per-request invention. It is deliberately absent from
	// the metric, which the convention's own instrument table omits it from.
	AttrMCPSessionID = attribute.Key("mcp.session.id")

	// AttrGenAIPromptName is the same shape for prompts/get.
	AttrGenAIPromptName = attribute.Key("gen_ai.prompt.name")

	// AttrGenAIOperationName is Recommended, and its note is a SHOULD NOT as
	// well as a SHOULD: set to execute_tool when the operation describes a tool
	// call, and not set otherwise.
	AttrGenAIOperationName = attribute.Key("gen_ai.operation.name")

	// AttrErrorType is Stable, and the only Stable key in this list. It is
	// Conditionally Required "if and only if the operation fails", which is why
	// nothing here sets it on a success path.
	AttrErrorType = attribute.Key("error.type")

	// AttrRPCResponseStatusCode is Release Candidate. It records the JSON-RPC
	// error code whenever the response carries one, including for the five
	// codes that do not count as errors: the code is a fact about the response,
	// while error.type is a classification of a failure.
	AttrRPCResponseStatusCode = attribute.Key("rpc.response.status_code")

	// AttrNetworkTransport is Stable. The convention's note is explicit for
	// this protocol: tcp when the transport is HTTP, pipe when it is stdio.
	AttrNetworkTransport = attribute.Key("network.transport")
)

// Attribute keys this project invents, under one namespace.
//
// The guidance against extending an OpenTelemetry namespace is a bare
// recommendation rather than an RFC 2119 keyword, and it sanctions "the
// attribute name by your application name, provided that the application name
// is reasonably unique". libgen_mcp is that name. It is deliberately not mcp.
// or gen_ai., which upstream owns and may extend into.
//
// Once shipped these cannot be renamed without breaking every dashboard built
// on them, so the set is kept small and each one earns its place.
const (
	// AttrRefusalReason carries a value from this server's closed set of
	// refusal reasons, for the failures that are this server's own rather than
	// a mirror's.
	//
	// It sits alongside error.type rather than inside it, which is the shape
	// the error registry recommends for a domain-specific identifier, and it
	// keeps error.type predictable and low cardinality as that registry asks.
	AttrRefusalReason = attribute.Key("libgen_mcp.refusal_reason")

	// AttrSource names the download source that actually served an item — the
	// entry in the ordered chain that returned a link, not the one that was
	// asked first.
	//
	// It is the single most useful thing a trace of this server can carry, and
	// it is the one thing no layer above the chain can work out: a download that
	// succeeded through the fourth source and one that succeeded through the
	// first are the same result to a caller, and completely different to whoever
	// has to decide whether a mirror is dying.
	AttrSource = attribute.Key("libgen_mcp.source")

	// AttrSourceAttempts counts how many sources were tried before one served,
	// the winner included.
	//
	// Bounded by the length of the chain, so it is cheap as a dimension, and it
	// is what turns AttrSource from "which one served" into "how much of the
	// chain it cost" — a call that reached the fourth source is a call whose
	// first three are worth looking at.
	AttrSourceAttempts = attribute.Key("libgen_mcp.source_attempts")

	// AttrMirrorHost is the catalog mirror a call used, as a bare host.
	//
	// The host and never the URL: a resolved file URL on the member path is
	// itself a working credential, and the same rule that keeps it out of a log
	// keeps it off a span. It is recorded on the span only — the mirror list is
	// operator-configurable and discovery can rotate it, so as a metric
	// dimension it is unbounded in the way that matters.
	AttrMirrorHost = attribute.Key("libgen_mcp.mirror_host")
)

// error.type values this server emits.
//
// "Instrumentations SHOULD document the list of errors they report", so this is
// a closed set rather than a pattern, and it is published in the documentation
// as well as declared here.
const (
	// ErrorTypeToolError is the convention's own instruction for the case where
	// a JSON-RPC call succeeds and the failure is inside the result:
	// "When CallToolResult is returned with isError set to true, this attribute
	// SHOULD be set to tool_error."
	ErrorTypeToolError = "tool_error"

	// ErrorTypeOther is the registry's fallback, for a failure this server
	// cannot classify. Emitting it is better than inventing a value, and better
	// than omitting the attribute on a span whose status is Error.
	ErrorTypeOther = "_OTHER"
)

// RefusalReason is why this server declined a call.
//
// A named type rather than a string, and a closed set rather than a message.
// The value lands on a metric dimension, and a dimension cannot be withdrawn
// without breaking every dashboard built on it — so a free-form reason, or an
// error string, is a commitment to carry whatever anybody ever wrote. Making it
// a type means a reason outside the set does not compile.
type RefusalReason string

// The reasons this server has. Each one is a decision this process made, not a
// failure it observed: a mirror returning 500 is not a refusal.
const (
	// ReasonConsentDeclined is the user answering no to a download prompt.
	ReasonConsentDeclined RefusalReason = "consent_declined"
	// ReasonBlockedAddress is the outbound destination guard refusing a host:
	// loopback, link-local, private or metadata space reached through a URL a
	// third party supplied.
	ReasonBlockedAddress RefusalReason = "blocked_address"
	// ReasonDownloadTooLarge is the size cap refusing a transfer before it
	// starts.
	ReasonDownloadTooLarge RefusalReason = "download_too_large"
	// ReasonDownloadStalled is a transfer cut because no bytes arrived for the
	// stall window.
	ReasonDownloadStalled RefusalReason = "download_stalled"
	// ReasonSourceNotInChain is a caller naming a source this deployment does
	// not have enabled.
	ReasonSourceNotInChain RefusalReason = "source_not_in_chain"
	// ReasonInvalidParams is an argument this server rejected before doing any
	// work.
	ReasonInvalidParams RefusalReason = "invalid_params"
	// ReasonRateLimited is the inbound per-caller rate limit refusing a method.
	ReasonRateLimited RefusalReason = "rate_limited"
	// ReasonInflightCeiling is the per-caller or per-process ceiling on heavy
	// calls refusing one.
	ReasonInflightCeiling RefusalReason = "inflight_ceiling"
	// ReasonActionTimeout is the wall-clock cap on one tool call ending it.
	ReasonActionTimeout RefusalReason = "action_timeout"
)

// AllRefusalReasons is the closed set, for a test and for the documentation
// generator. Adding a reason means adding it here, which is what keeps the
// published list true.
// A source in cooldown is deliberately not in this set, and the absence is a
// finding rather than an omission. Cooldown in this server is *routing*, not
// refusal: internal/libgen's eligibleSources either skips a cooled source and
// continues down the chain, or — when every supporting source is cooled —
// bypasses the cooldown and tries them anyway. Neither declines a call, so a
// reason recorded there would put "refused" on the metric for calls that
// succeeded. Whether a skipped source deserves a dimension of its own is a
// different question from this one.
var AllRefusalReasons = []RefusalReason{
	ReasonConsentDeclined,
	ReasonBlockedAddress,
	ReasonDownloadTooLarge,
	ReasonDownloadStalled,
	ReasonSourceNotInChain,
	ReasonInvalidParams,
	ReasonRateLimited,
	ReasonInflightCeiling,
	ReasonActionTimeout,
}

// RecordRefusal marks the current span with why this server declined a call.
//
// Called from where the refusal is decided rather than from the middleware,
// because the middleware cannot know: a refusal travels as an error result,
// which is a successful JSON-RPC response carrying a failure meant for the
// model, and from outside the handler it is indistinguishable from a handler
// that ran and failed.
//
// A no-op when there is no recording span, which is the case with telemetry off
// and in every unit test that installs no provider.
func RecordRefusal(ctx context.Context, reason RefusalReason) {
	if reason == "" {
		return
	}
	// The holder first, because it is the one that can be missed: it is only in
	// the context when a middleware put it there, and a handler called from
	// somewhere else still gets the span attribute.
	//
	// It also decides. The holder keeps the FIRST reason — a refusal decided
	// deep in a call is the cause, and one recorded on the way out is a summary
	// — while a span attribute is last-write-wins, so setting the span
	// unconditionally would make the two signals disagree about the same call.
	// Where there is a holder, it is the authority.
	if holder, ok := ctx.Value(callHolderKey{}).(*callHolder); ok {
		if !holder.setReason(reason) {
			return
		}
	}
	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		span.SetAttributes(AttrRefusalReason.String(string(reason)))
	}
}

// RecordSource tells the middleware which download source served an item, and
// which mirror the catalog half of the call used.
//
// # Why the chain has to report this itself
//
// The download pipeline is an ordered chain with transparent failover, so from
// outside it a call that succeeded on the first source and one that succeeded on
// the fourth are the same result. The chain is the only layer that knows, and
// what it knows is exactly the question an operator has: which source is
// carrying this deployment, and how much of the chain is being spent to get
// there.
//
// # Why a later call does not simply overwrite an earlier one
//
// It counts instead. The sibling project's equivalent takes the last value on
// the grounds that a dispatcher rewrites its own route, which is right there and
// wrong here: in a failover chain every call before the last is a source that
// was tried and failed. Overwriting would record the winner and erase the three
// failures, which is the half of the picture worth having.
//
// The winner is the last source recorded, because the chain stops at the first
// one that serves. A host is remembered whenever one is given, so a call that
// resolved through the catalog and then downloaded elsewhere still names the
// mirror it searched.
//
// A no-op outside a request the middleware wraps.
func RecordSource(ctx context.Context, source, mirrorHost string) {
	holder, ok := ctx.Value(callHolderKey{}).(*callHolder)
	if !ok {
		return
	}
	holder.recordSource(source, mirrorHost)
}

// callHolderKey is the context key for the holder below. An empty struct type
// rather than a string, so nothing else can collide with it.
type callHolderKey struct{}

// callHolder carries back from the handler to the middleware what only the
// handler knows: why it declined the call, and how the source chain went.
//
// The middleware cannot learn either any other way. A refusal travels as a
// successful JSON-RPC response carrying a failure meant for the model, so from
// outside the handler it is indistinguishable from a handler that ran and
// failed; and which source served is a fact about a loop several layers down.
//
// # Why this one is synchronized and the sibling's is not
//
// One holder per request, reachable only through that request's context — but
// the search path fans out to several providers concurrently through
// discovery.Federate, and any of them may be the one that records a mirror. The
// sibling project's dispatcher is sequential and needs no lock; here the race
// detector would find one on the first federated search.
type callHolder struct {
	mu sync.Mutex

	reason RefusalReason

	// attempts counts every source that was tried, and source is the last one
	// recorded, which is the one that served.
	attempts   int
	source     string
	mirrorHost string
}

// setReason records why a call was declined, reporting whether it took it.
//
// The first reason wins: a refusal deep in the chain is the cause, and a later,
// vaguer one recorded on the way out would replace the specific answer with a
// general one. The boolean is what lets the span follow the same rule, since a
// span attribute is otherwise last-write-wins.
func (h *callHolder) setReason(reason RefusalReason) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.reason != "" {
		return false
	}
	h.reason = reason
	return true
}

// recordSource counts one source attempt and remembers the last one.
func (h *callHolder) recordSource(source, mirrorHost string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if source != "" {
		h.attempts++
		h.source = source
	}
	if mirrorHost != "" {
		h.mirrorHost = mirrorHost
	}
}

// snapshot reads the holder once the handler has returned.
//
// A value rather than the holder, so the middleware's own code cannot forget the
// lock — which is the mistake that makes a data race look like a flaky test
// rather than a defect.
func (h *callHolder) snapshot() holderSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return holderSnapshot{
		reason:     h.reason,
		attempts:   h.attempts,
		source:     h.source,
		mirrorHost: h.mirrorHost,
	}
}

// holderSnapshot is what the handler reported, read once.
type holderSnapshot struct {
	reason     RefusalReason
	attempts   int
	source     string
	mirrorHost string
}

// spanAttributes are what the snapshot adds to the span.
//
// The mirror host is here and not on the metric: the mirror list is
// operator-configurable and discovery rotates it, so as a dimension it grows
// with somebody else's infrastructure.
func (s holderSnapshot) spanAttributes() []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 3)
	if s.source != "" {
		attrs = append(attrs, AttrSource.String(s.source), AttrSourceAttempts.Int(s.attempts))
	}
	if s.mirrorHost != "" {
		attrs = append(attrs, AttrMirrorHost.String(s.mirrorHost))
	}
	return attrs
}

// metricAttributes are what the snapshot adds to the duration measurement.
//
// Bounded by construction: the source names are a fixed list in
// internal/config, the attempt count is bounded by the length of the chain, and
// the refusal reasons are a closed set — so this adds a fixed number of label
// combinations rather than growing with traffic. Counting them is the point of
// recording them: a deployment refusing every third call looks identical to a
// healthy one without it.
func (s holderSnapshot) metricAttributes() []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 3)
	if s.source != "" {
		attrs = append(attrs, AttrSource.String(s.source), AttrSourceAttempts.Int(s.attempts))
	}
	if s.reason != "" {
		attrs = append(attrs, AttrRefusalReason.String(string(s.reason)))
	}
	return attrs
}

// withCallHolder returns a context a handler can report through, and the
// holder to read afterwards.
func withCallHolder(ctx context.Context) (context.Context, *callHolder) {
	holder := &callHolder{}
	return context.WithValue(ctx, callHolderKey{}, holder), holder
}
