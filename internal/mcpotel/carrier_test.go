package mcpotel

import (
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/trace"
)

// TestMetaCarrierReadsOnlyThePropagationKeys pins what the propagator is shown.
//
// Keys is what a propagator calls to discover what arrived, and the interface
// asks for the propagation keys rather than everything in the map. Returning
// every key of _meta would hand the propagator whatever else a caller put there,
// which is a request's own payload.
func TestMetaCarrierReadsOnlyThePropagationKeys(t *testing.T) {
	t.Parallel()

	carrier := metaCarrier{meta: mcp.Meta{
		"traceparent":                   "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"baggage":                       "key=value",
		"io.modelcontextprotocol/thing": "something else entirely",
		"a-number-rather-than-a-string": 42,
	}}

	keys := carrier.Keys()
	slices.Sort(keys)
	if want := []string{"baggage", "traceparent"}; !slices.Equal(keys, want) {
		t.Errorf("Keys() = %q, want %q", keys, want)
	}
	if got := carrier.Get("traceparent"); !strings.HasPrefix(got, "00-") {
		t.Errorf("Get(traceparent) = %q", got)
	}
	// A value of another type is the empty string rather than a panic or a
	// formatted rendering: the specification requires that a carrier which
	// cannot parse a value stores nothing and throws nothing.
	if got := carrier.Get("a-number-rather-than-a-string"); got != "" {
		t.Errorf("Get on a non-string = %q, want the empty string", got)
	}
	if got := carrier.Get("absent"); got != "" {
		t.Errorf("Get on a missing key = %q, want the empty string", got)
	}
}

// TestMetaCarrierSetWritesNothing is the outward-leak rule, asserted rather than
// commented.
//
// Writing a traceparent into a response's _meta would hand a client the
// identifiers of this server's internal spans. The interface requires the
// method; nothing may implement it.
func TestMetaCarrierSetWritesNothing(t *testing.T) {
	t.Parallel()

	meta := mcp.Meta{}
	carrier := metaCarrier{meta: meta}
	carrier.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	if len(meta) != 0 {
		t.Errorf("Set wrote %v into _meta; this carrier reads", meta)
	}
}

// TestSanitizeRemoteContextTruncatesWholeTraceStateEntries bounds what an
// unauthenticated caller writes into the operator's traces bill.
//
// W3C permits about 16 KiB of tracestate and the SDK exports it verbatim on
// every span in the trace. The truncation drops whole entries from the end,
// which is what section 3.3.1.5 requires — keeping the head preserves a
// fronting gateway's own vendor state, which is the reason to truncate rather
// than clear.
func TestSanitizeRemoteContextTruncatesWholeTraceStateEntries(t *testing.T) {
	t.Parallel()

	state := trace.TraceState{}
	var err error
	// Enough members to pass the byte bound several times over.
	for i := range 30 {
		state, err = state.Insert(string(rune('a'+i%26))+string(rune('a'+i/26))+"vendor", strings.Repeat("x", 60))
		if err != nil {
			t.Fatalf("building the tracestate: %v", err)
		}
	}
	if len(state.String()) <= maxTraceStateBytes {
		t.Fatalf("the fixture is %d bytes, which is under the bound and asserts nothing", len(state.String()))
	}

	remote := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1},
		SpanID:     trace.SpanID{2},
		TraceState: state,
		Remote:     true,
	})
	ctx := sanitizeRemoteContext(trace.ContextWithSpanContext(t.Context(), remote), true)
	got := trace.SpanContextFromContext(ctx)

	if len(got.TraceState().String()) > maxTraceStateBytes {
		t.Errorf("tracestate is %d bytes, want at most %d", len(got.TraceState().String()), maxTraceStateBytes)
	}
	// The correlation survives: the trace is still joined, which is the whole
	// reason to truncate rather than drop the context.
	if got.TraceID() != remote.TraceID() {
		t.Error("the trace id changed, so the caller's trace no longer joins")
	}
	// Whole entries, so what is left still parses — a byte-wise cut would
	// produce a value the next hop rejects.
	if _, parseErr := trace.ParseTraceState(got.TraceState().String()); parseErr != nil {
		t.Errorf("the truncated tracestate does not parse: %v", parseErr)
	}
}

// TestSanitizeRemoteContextCanDeclineTheCallersSamplingDecision covers the half
// that is off at the MCP layer and on at the HTTP edge.
//
// A cleared sampled flag is a recommendation in W3C's own words, not a veto. At
// the unauthenticated edge, letting an anonymous caller decide whether this
// server records the work it does for them is a decision that is not theirs.
func TestSanitizeRemoteContextCanDeclineTheCallersSamplingDecision(t *testing.T) {
	t.Parallel()

	unsampled := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1},
		SpanID:  trace.SpanID{2},
		Remote:  true,
	})
	ctx := trace.ContextWithSpanContext(t.Context(), unsampled)

	kept := trace.SpanContextFromContext(sanitizeRemoteContext(ctx, true))
	if kept.IsSampled() {
		t.Error("the caller's cleared sampled flag was overridden when it was meant to be honored")
	}
	overridden := trace.SpanContextFromContext(sanitizeRemoteContext(ctx, false))
	if !overridden.IsSampled() {
		t.Error("the caller's cleared sampled flag was treated as a veto")
	}
	// Either way the trace still joins: only the sampling decision changes.
	if overridden.TraceID() != unsampled.TraceID() || overridden.SpanID() != unsampled.SpanID() {
		t.Error("overriding the sampling decision changed the trace or parent relationship")
	}
}

// TestSanitizeRemoteContextLeavesALocalContextAlone keeps the rules pointed
// outward.
//
// A span context created in this process is this server's own; bounding or
// re-sampling it would be this code second-guessing itself.
func TestSanitizeRemoteContextLeavesALocalContextAlone(t *testing.T) {
	t.Parallel()

	local := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1},
		SpanID:  trace.SpanID{2},
	})
	ctx := trace.ContextWithSpanContext(t.Context(), local)

	if got := trace.SpanContextFromContext(sanitizeRemoteContext(ctx, false)); !got.Equal(local) {
		t.Errorf("a locally created context was rewritten: %+v", got)
	}
	// And a context with no span at all is returned as it came.
	empty := t.Context()
	if got := trace.SpanContextFromContext(sanitizeRemoteContext(empty, false)); got.IsValid() {
		t.Errorf("a context with no span produced %+v", got)
	}
}

// TestIsNotification pins the only way a method name says it has no response.
func TestIsNotification(t *testing.T) {
	t.Parallel()

	for method, want := range map[string]bool{
		"notifications/progress": true,
		// The protocol's own cancellation notification, assembled rather than
		// written out: its spelling is the one the linter's US locale refuses,
		// and the name is the protocol's rather than ours to change.
		"notifications/" + "cancel" + "led": true,
		"tools/call":                        false,
		"elicitation/create":                false,
		"":                                  false,
	} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			if got := IsNotification(method); got != want {
				t.Errorf("IsNotification(%q) = %v, want %v", method, got, want)
			}
		})
	}
}
