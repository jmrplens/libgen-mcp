// boundary.go is the one place a context that came from a caller becomes a
// request this server makes.

package mcpotel

import (
	"net/http"

	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"
)

// propagationHeaders are the headers that carry trace context and baggage
// between processes.
//
// Written out rather than asked of the propagator, because the propagator only
// knows what it is configured to inject and the point here is to remove what
// anything might have injected — including a library that installed its own.
var propagationHeaders = []string{"traceparent", "tracestate", "baggage"}

// StripOutbound removes every propagation header from a request this server is
// about to make, and clears both the baggage and the span context from the
// context it carries.
//
// # Why this is an action rather than a default
//
// It is tempting to assume nothing forwards baggage or trace context unless
// asked. That is false. propagation.Baggage.Inject writes
// baggage.FromContext(ctx).String() unconditionally whenever it is non-empty,
// and propagation.TraceContext.Inject writes a traceparent for any valid span
// context — and the global propagator this server installs contains both. So the
// moment an outbound transport is instrumented, which adopting traces implies,
// and the inbound request's context reaches it, which is the ordinary Go idiom,
// a caller's baggage and this server's trace identifiers ride outbound.
//
// **Nobody has to opt in for the leak; somebody has to opt out for it not to
// happen.** This is that opt-out, and it is a named function called at the
// boundary rather than a habit, because a habit is what the next transport
// forgets.
//
// # Why the trace context goes too, which is where this server differs
//
// The sibling project clears baggage and leaves traceparent, and that is right
// there: its single downstream is the instance the operator named, so joining
// the trace across the call is a service to the same person.
//
// This server's downstreams are dozens of third parties, and several of them are
// named **by another third party** — a publisher link deposited with Crossref, a
// repository URL republished by an open-access index, a citation_pdf_url scraped
// off a page. Injecting a W3C traceparent toward a Sci-Hub mirror or a publisher
// hands a stable correlation handle to a party the operator did not choose and
// cannot audit, which is the same disclosure the url.full refusal declines.
//
// The revisit trigger is concrete: an operator-instrumented egress proxy in
// front of these requests would be a downstream they do choose, and then joining
// the trace is worth something. Until then the trace stops here, and the child
// span this package records is what connects an outbound fetch to the call that
// caused it.
//
// # This declines a documented SHOULD, on purpose
//
// W3C says "A system receiving a baggage request header SHOULD send it to
// outgoing requests." We do not. The same section provides the escape hatch in
// the same breath ("Any key/value pair MAY be deleted"), and the OTel Baggage
// API requires the facility used here to exist for exactly this reason: "To
// avoid sending any name/value pairs to an untrusted process, the Baggage API
// MUST provide a way to remove all baggage entries from a context." Declining a
// SHOULD is legitimate when the reason is recorded, and the reason is that the
// process downstream of us belongs to someone who is not the operator and not
// the caller.
//
// # Both halves, because either alone is a hole
//
// The context is cleared so nothing downstream can inject from it, and the
// headers are cleared so an injection that already happened does not survive.
// One without the other is a rule that holds until the next library is added.
func StripOutbound(req *http.Request) *http.Request {
	if req == nil {
		return nil
	}
	// Cloned rather than mutated: a RoundTripper "should not modify the
	// request", and the download pipeline reuses request objects across its own
	// retry schedule — a header deleted in place would stay deleted for a retry
	// that was meant to carry it.
	//
	// The span context goes with the baggage, on the clone alone. Deleting the
	// headers is enough for the transport this server installs, which does not
	// inject — but NewTransport wraps whatever RoundTripper it is given, and a
	// lower one that propagates from the context would put the trace back on the
	// wire behind this function's own promise. What is cut is cut at the
	// boundary, not at the one implementation that happens to be underneath it.
	//
	// The original request keeps its context: the caller's span is what the
	// outbound child span is recorded under, and that decision belongs to the
	// caller rather than to this clone.
	ctx := trace.ContextWithSpanContext(
		baggage.ContextWithoutBaggage(req.Context()), trace.SpanContext{},
	)
	stripped := req.Clone(ctx)
	for _, header := range propagationHeaders {
		stripped.Header.Del(header)
	}
	return stripped
}
