package mcpotel

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// recorded is what one drive of the middleware produced.
type recorded struct {
	spans   []sdktrace.ReadOnlySpan
	metrics metricdata.ResourceMetrics
}

// spanNamed returns the one span with this name, failing when there is not
// exactly one.
func (r recorded) spanNamed(t *testing.T, name string) sdktrace.ReadOnlySpan {
	t.Helper()

	var found []sdktrace.ReadOnlySpan
	for _, span := range r.spans {
		if span.Name() == name {
			found = append(found, span)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d spans named %q, want exactly one (all: %s)", len(found), name, r.spanNames())
	}
	return found[0]
}

// spanNames lists what was recorded, for a failure message.
func (r recorded) spanNames() string {
	names := make([]string, 0, len(r.spans))
	for _, span := range r.spans {
		names = append(names, span.Name())
	}
	return strings.Join(names, ", ")
}

// attrsOf renders a span's attributes as a map.
func attrsOf(span sdktrace.ReadOnlySpan) map[string]string {
	got := make(map[string]string)
	for _, attr := range span.Attributes() {
		got[string(attr.Key)] = attr.Value.String()
	}
	return got
}

// durationAttrs returns the attribute sets of every mcp.server.operation.duration
// data point recorded.
func (r recorded) durationAttrs(t *testing.T) []map[string]string {
	t.Helper()
	return r.histogramAttrs(t, "mcp.server.operation.duration")
}

// histogramAttrs returns the attribute sets of one named histogram's points.
func (r recorded) histogramAttrs(t *testing.T, name string) []map[string]string {
	t.Helper()

	var out []map[string]string
	for _, scope := range r.metrics.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("%s is %T, want a float histogram", name, m.Data)
			}
			for _, point := range hist.DataPoints {
				set := make(map[string]string)
				for _, attr := range point.Attributes.ToSlice() {
					set[string(attr.Key)] = attr.Value.String()
				}
				out = append(out, set)
			}
		}
	}
	return out
}

// drive runs one request through the middleware with a real SDK behind it, and
// returns what was recorded.
//
// A real tracer and meter rather than a mock: the middleware's whole job is what
// the SDK ends up with, and a fake would assert the calls rather than the
// result.
func drive(t *testing.T, opts Options, method string, req mcp.Request, handler mcp.MethodHandler) recorded {
	t.Helper()

	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	previousTracer, previousMeter := otel.GetTracerProvider(), otel.GetMeterProvider()
	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousTracer)
		otel.SetMeterProvider(previousMeter)
	})

	if handler == nil {
		handler = func(context.Context, string, mcp.Request) (mcp.Result, error) {
			return &mcp.CallToolResult{}, nil
		}
	}
	_, _ = Middleware(opts)(handler)(t.Context(), method, req)

	if err := tracerProvider.ForceFlush(t.Context()); err != nil {
		t.Fatalf("flushing spans: %v", err)
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	return recorded{spans: spanRecorder.Ended(), metrics: metrics}
}

// toolCall builds a tools/call request for a tool name.
func toolCall(name string) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: name}}
}

// TestMiddlewareRecordsOneSpanAndOneMeasurement is the shape, asserted on both
// signals at once so the two cannot drift.
func TestMiddlewareRecordsOneSpanAndOneMeasurement(t *testing.T) {
	got := drive(t, Options{Transport: TransportTCP}, "tools/call", toolCall("download"), nil)

	span := got.spanNamed(t, "tools/call download")
	if span.SpanKind() != trace.SpanKindServer {
		t.Errorf("span kind = %v, want server: this server is the receiver", span.SpanKind())
	}
	attrs := attrsOf(span)
	for key, want := range map[string]string{
		string(AttrMCPMethodName):      "tools/call",
		string(AttrGenAIToolName):      "download",
		string(AttrGenAIOperationName): "execute_tool",
		string(AttrNetworkTransport):   TransportTCP,
	} {
		t.Run(key, func(t *testing.T) {
			if attrs[key] != want {
				t.Errorf("span %s = %q, want %q", key, attrs[key], want)
			}
		})
	}

	points := got.durationAttrs(t)
	if len(points) != 1 {
		t.Fatalf("%d duration points, want one per request", len(points))
	}
	if points[0][string(AttrGenAIToolName)] != "download" {
		t.Errorf("the measurement does not name the tool: %v", points[0])
	}
}

// TestMiddlewareNeverPutsIdentityOnAMetric is the rule that cannot be relaxed.
//
// Every distinct label combination is a time series somebody stores and pays
// for, and a per-caller dimension is unbounded by construction: on a public
// endpoint it grows with the internet. The SDK's answer to an exhausted series
// budget is not an error but an overflow bucket that swallows everything after
// the limit, so getting this wrong destroys the real data silently.
func TestMiddlewareNeverPutsIdentityOnAMetric(t *testing.T) {
	identity := CallerAttributerFunc(func(context.Context, mcp.Request) []attribute.KeyValue {
		return []attribute.KeyValue{
			attribute.String("user.hash", "0123456789abcdef"),
			attribute.String("client.address", "203.0.113.7"),
		}
	})

	got := drive(t, Options{Transport: TransportTCP, Callers: identity}, "tools/call", toolCall("search"), nil)

	// The span carries it, which is the point of recording it at all.
	if attrsOf(got.spanNamed(t, "tools/call search"))["user.hash"] != "0123456789abcdef" {
		t.Error("the span does not carry the identity the policy allowed")
	}
	for _, point := range got.durationAttrs(t) {
		for _, forbidden := range []string{"user.hash", "client.address", "user.id", "user.name"} {
			t.Run(forbidden, func(t *testing.T) {
				if _, present := point[forbidden]; present {
					t.Errorf("%s reached a metric dimension: %v", forbidden, point)
				}
			})
		}
	}
}

// TestMiddlewareRecordsTheSourceChain is the reason this instrumentation earns
// its cost on this server.
//
// A download served by the first source and one served by the fourth are the
// same result to a caller and completely different to whoever has to decide
// whether a mirror is dying. Nothing above the chain can tell them apart.
func TestMiddlewareRecordsTheSourceChain(t *testing.T) {
	handler := func(ctx context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
		// Three failures and then a winner, which is what a failover chain
		// looks like from inside.
		RecordSource(ctx, "libgen", "libgen.li")
		RecordSource(ctx, "annas", "")
		RecordSource(ctx, "scihub", "")
		RecordSource(ctx, "scidb", "")
		return &mcp.CallToolResult{}, nil
	}

	got := drive(t, Options{Transport: TransportTCP}, "tools/call", toolCall("download"), handler)

	attrs := attrsOf(got.spanNamed(t, "tools/call download"))
	if attrs[string(AttrSource)] != "scidb" {
		t.Errorf("%s = %q, want the source that served", AttrSource, attrs[string(AttrSource)])
	}
	if attrs[string(AttrSourceAttempts)] != "4" {
		t.Errorf("%s = %q, want 4: recording only the winner erases the three that failed first",
			AttrSourceAttempts, attrs[string(AttrSourceAttempts)])
	}
	if attrs[string(AttrMirrorHost)] != "libgen.li" {
		t.Errorf("%s = %q, want the mirror the catalog half used", AttrMirrorHost, attrs[string(AttrMirrorHost)])
	}

	points := got.durationAttrs(t)
	if len(points) != 1 {
		t.Fatalf("%d duration points, want one", len(points))
	}
	if points[0][string(AttrSource)] != "scidb" || points[0][string(AttrSourceAttempts)] != "4" {
		t.Errorf("the measurement does not carry the chain: %v", points[0])
	}
	// The mirror host is a span attribute and not a metric dimension: the
	// mirror list is operator-configurable and discovery rotates it.
	if _, present := points[0][string(AttrMirrorHost)]; present {
		t.Errorf("the mirror host reached a metric dimension: %v", points[0])
	}
}

// TestMiddlewareRecordsARefusal covers the outcome the middleware cannot see
// for itself.
//
// A refusal travels as a successful JSON-RPC response carrying a failure meant
// for the model, so from outside the handler it is indistinguishable from a
// handler that ran and succeeded — which is exactly what it would look like on
// the metric without this.
func TestMiddlewareRecordsARefusal(t *testing.T) {
	handler := func(ctx context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
		RecordRefusal(ctx, ReasonConsentDeclined)
		return &mcp.CallToolResult{IsError: true}, nil
	}

	got := drive(t, Options{Transport: TransportPipe}, "tools/call", toolCall("download"), handler)

	attrs := attrsOf(got.spanNamed(t, "tools/call download"))
	if attrs[string(AttrRefusalReason)] != string(ReasonConsentDeclined) {
		t.Errorf("%s = %q, want %q", AttrRefusalReason, attrs[string(AttrRefusalReason)], ReasonConsentDeclined)
	}
	// isError:true is the convention's own instruction for a failure inside a
	// successful result.
	if attrs[string(AttrErrorType)] != ErrorTypeToolError {
		t.Errorf("%s = %q, want %q", AttrErrorType, attrs[string(AttrErrorType)], ErrorTypeToolError)
	}

	points := got.durationAttrs(t)
	if len(points) != 1 || points[0][string(AttrRefusalReason)] != string(ReasonConsentDeclined) {
		t.Errorf("the measurement does not carry the refusal: %v", points)
	}
}

// TestFirstRefusalWins keeps the specific answer.
//
// A refusal decided deep in a call is the cause; one recorded on the way out is
// a summary. Taking the last would replace "the user declined" with whatever
// generic reason a later layer happened to name.
func TestFirstRefusalWins(t *testing.T) {
	handler := func(ctx context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
		RecordRefusal(ctx, ReasonConsentDeclined)
		RecordRefusal(ctx, ReasonInvalidParams)
		return &mcp.CallToolResult{IsError: true}, nil
	}

	got := drive(t, Options{}, "tools/call", toolCall("download"), handler)
	if reason := attrsOf(got.spanNamed(t, "tools/call download"))[string(AttrRefusalReason)]; reason != string(ReasonConsentDeclined) {
		t.Errorf("%s = %q, want the first reason recorded", AttrRefusalReason, reason)
	}
}

// TestRecordRefusalAndRecordSourceAreNoOpsOutsideARequest keeps every call site
// safe to add.
//
// They are called from deep inside the download chain and from refusal paths
// that also run from tests, from cmd/probe and from a handler reached some other
// way. None of those has a holder, and a panic there would be a defect
// introduced by instrumentation.
func TestRecordRefusalAndRecordSourceAreNoOpsOutsideARequest(t *testing.T) {
	t.Parallel()

	RecordRefusal(t.Context(), ReasonRateLimited)
	RecordSource(t.Context(), "libgen", "libgen.li")
	RecordRefusal(t.Context(), "")
}

// TestMiddlewareBoundsANameTheCallerInvented is the series-budget defense.
//
// gen_ai.tool.name is copied off the request and the convention puts it on the
// duration metric. A prompts/get for a prompt that does not exist would
// otherwise record the invented name as a dimension value, letting any client
// mint one time series per string it types.
func TestMiddlewareBoundsANameTheCallerInvented(t *testing.T) {
	handler := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		// What the SDK answers for a prompt that is not registered.
		return nil, &jsonrpc.Error{Code: -32602, Message: "invalid params"}
	}
	req := &mcp.GetPromptRequest{Params: &mcp.GetPromptParams{Name: "a-name-nobody-registered"}}

	got := drive(t, Options{}, "prompts/get", req, handler)

	// The span keeps it exactly: a span has no series budget, and the name is
	// what an operator debugging the client needs to see.
	if name := attrsOf(got.spanNamed(t, "prompts/get a-name-nobody-registered"))[string(AttrGenAIPromptName)]; name != "a-name-nobody-registered" {
		t.Errorf("the span did not keep the name the caller sent: %q", name)
	}
	points := got.durationAttrs(t)
	if len(points) != 1 {
		t.Fatalf("%d duration points, want one", len(points))
	}
	if got := points[0][string(AttrGenAIPromptName)]; got != ErrorTypeOther {
		t.Errorf("the metric carried %q, want it bounded to %q", got, ErrorTypeOther)
	}
}

// TestMiddlewareKeepsAVerifiedName is the other half: the bound applies to a
// name that named nothing, not to every name.
func TestMiddlewareKeepsAVerifiedName(t *testing.T) {
	got := drive(t, Options{}, "tools/call", toolCall("search"), nil)

	points := got.durationAttrs(t)
	if len(points) != 1 || points[0][string(AttrGenAIToolName)] != "search" {
		t.Errorf("a successful call did not keep its tool name on the metric: %v", points)
	}
}

// TestMiddlewareRecordsOnlyAdmittedProtocolVersions keeps a caller-chosen string
// off a metric dimension.
func TestMiddlewareRecordsOnlyAdmittedProtocolVersions(t *testing.T) {
	admitted := "2025-06-18"

	t.Run("an admitted version is recorded", func(t *testing.T) {
		req := toolCall("search")
		req.Params.Meta = mcp.Meta{metaProtocolVersionKey: admitted}
		got := drive(t, Options{ProtocolVersions: []string{admitted}}, "tools/call", req, nil)

		if v := attrsOf(got.spanNamed(t, "tools/call search"))[string(AttrMCPProtocolVersion)]; v != admitted {
			t.Errorf("%s = %q, want %q", AttrMCPProtocolVersion, v, admitted)
		}
	})

	t.Run("anything else is not", func(t *testing.T) {
		req := toolCall("search")
		req.Params.Meta = mcp.Meta{metaProtocolVersionKey: "9999-01-01"}
		got := drive(t, Options{ProtocolVersions: []string{admitted}}, "tools/call", req, nil)

		if v, present := attrsOf(got.spanNamed(t, "tools/call search"))[string(AttrMCPProtocolVersion)]; present {
			t.Errorf("%s = %q for a version this server does not admit", AttrMCPProtocolVersion, v)
		}
	})
}

// TestMiddlewareParentsFromTheRequestAndLinksTheAmbientSpan is the relationship
// the convention asks for, and the reason it is not the obvious one.
//
// One MCP request can be served by several HTTP requests when a client retries,
// and one streamable HTTP request can carry more than one MCP request, so
// parenting to the transport would attach an operation to whichever round trip
// happened to carry it. The ambient span is linked instead, which is what keeps
// "which HTTP request carried this" answerable.
func TestMiddlewareParentsFromTheRequestAndLinksTheAmbientSpan(t *testing.T) {
	// A remote context in the request's _meta, and a different ambient span
	// around the call.
	req := toolCall("search")
	req.Params.Meta = mcp.Meta{
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}

	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tracerProvider)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	previousPropagator := otel.GetTextMapPropagator()
	// The real W3C propagator, which is what the server installs: a stand-in
	// would assert this test's own extraction rather than the one that ships.
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previousPropagator) })

	ctx, ambient := tracerProvider.Tracer("test").Start(t.Context(), "the-http-request")
	handler := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return &mcp.CallToolResult{}, nil
	}
	_, _ = Middleware(Options{})(handler)(ctx, "tools/call", req)
	ambient.End()

	if err := tracerProvider.ForceFlush(t.Context()); err != nil {
		t.Fatalf("flushing: %v", err)
	}
	got := recorded{spans: spanRecorder.Ended()}
	span := got.spanNamed(t, "tools/call search")

	if want := "4bf92f3577b34da6a3ce929d0e0e4736"; span.SpanContext().TraceID().String() != want {
		t.Errorf("trace id = %s, want the one from _meta (%s): the span is parented to the transport",
			span.SpanContext().TraceID(), want)
	}
	links := span.Links()
	if len(links) != 1 {
		t.Fatalf("%d links, want the ambient span linked", len(links))
	}
	if links[0].SpanContext.SpanID() != ambient.SpanContext().SpanID() {
		t.Error("the link does not point at the ambient span")
	}
}

// TestOutcomeClassification pins which JSON-RPC codes count as this server's
// failures and which are the caller's.
//
// The five caller-fault codes are exempt server-side because a client sending a
// malformed request is not this server erroring — but the code is still a fact
// about the response, and recording it is what lets an operator see a broken
// client without it showing up as an outage.
func TestOutcomeClassification(t *testing.T) {
	for _, tc := range []struct {
		name          string
		err           error
		wantFailed    bool
		wantErrorType string
		wantStatus    string
	}{
		{name: "invalid params is the caller's fault", err: &jsonrpc.Error{Code: -32602}, wantStatus: "-32602"},
		{name: "method not found is the caller's fault", err: &jsonrpc.Error{Code: -32601}, wantStatus: "-32601"},
		{
			name:          "an internal error is ours",
			err:           &jsonrpc.Error{Code: -32603},
			wantFailed:    true,
			wantErrorType: "-32603",
			wantStatus:    "-32603",
		},
		{
			name:          "a plain error is ours and unclassifiable",
			err:           errors.New("something went wrong"),
			wantFailed:    true,
			wantErrorType: ErrorTypeOther,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(nil, tc.err)
			if got.failed != tc.wantFailed {
				t.Errorf("failed = %v, want %v", got.failed, tc.wantFailed)
			}
			if got.errorType != tc.wantErrorType {
				t.Errorf("errorType = %q, want %q", got.errorType, tc.wantErrorType)
			}
			if got.statusCode != tc.wantStatus {
				t.Errorf("statusCode = %q, want %q", got.statusCode, tc.wantStatus)
			}
		})
	}
}

// TestAllRefusalReasonsIsTheClosedSet is the drift gate on a vocabulary that
// cannot be withdrawn.
//
// Each value lands on a metric dimension, and a dimension cannot be removed
// without breaking every dashboard built on it. The list is written out rather
// than derived, so adding a constant without deciding it is publishable fails
// here.
func TestAllRefusalReasonsIsTheClosedSet(t *testing.T) {
	t.Parallel()

	want := []string{
		"action_timeout",
		"blocked_address",
		"consent_declined",
		"download_stalled",
		"download_too_large",
		"inflight_ceiling",
		"invalid_params",
		"rate_limited",
		"source_not_in_chain",
	}
	got := make([]string, 0, len(AllRefusalReasons))
	for _, reason := range AllRefusalReasons {
		got = append(got, string(reason))
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("AllRefusalReasons = %q, want %q", got, want)
	}
}

// TestHandlerRunsInsideTheSpanContext is the one-line mistake this middleware
// is easiest to make.
//
// tracer.Start returns a new context, and passing the *original* one onward
// compiles, runs, and produces a flat trace with every outbound fetch as a root.
// On this server that destroys the one thing the tree is for: a federated search
// fans out to several providers concurrently, and the shape of that fan-out is
// the answer to "why was this search slow".
func TestHandlerRunsInsideTheSpanContext(t *testing.T) {
	handler := func(ctx context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
		// What an outbound call does: start a span on the context it was given.
		_, inner := otel.Tracer("test").Start(ctx, "an-outbound-fetch")
		inner.End()
		return &mcp.CallToolResult{}, nil
	}

	got := drive(t, Options{}, "tools/call", toolCall("search"), handler)

	parent := got.spanNamed(t, "tools/call search")
	child := got.spanNamed(t, "an-outbound-fetch")

	if child.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Errorf("the outbound span is not a child of the request span: parent=%s, request=%s",
			child.Parent().SpanID(), parent.SpanContext().SpanID())
	}
	if child.SpanContext().TraceID() != parent.SpanContext().TraceID() {
		t.Error("the outbound span is in a different trace, which is what a flat trace looks like")
	}
}

// TestSessionTrackerSkipsASessionThatNeverHandshook is the discover-session
// correction, driven against a real session rather than a stand-in.
//
// Under a stateful transport the SDK creates a throwaway session for a
// server/discover probe — which is what a 1.8.0 client sends before it
// handshakes — so without this condition every connecting client would put a
// near-zero sample into mcp.server.session.duration and the distribution would
// describe the probe rather than the session.
//
// The discriminator is the SDK's own: InitializeParams is persisted only when
// the transport can serve the protocol, so a discover session has none.
func TestSessionTrackerSkipsASessionThatNeverHandshook(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")
	// Pipe, so the session-id check is not what does the skipping: an in-memory
	// session has no id, and the case would then pass for the wrong reason.
	tracker := newSessionTracker(meter, nil, TransportPipe)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatalf("connecting the server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	// Before the handshake the session exists and has carried no initialize,
	// which is exactly the shape of a discover probe.
	if serverSession.InitializeParams() != nil {
		t.Fatal("the fixture is wrong: the session has already handshaken")
	}
	tracker.observe(sessionRequest(serverSession), "")
	if observing := tracker.observingCount(); observing != 0 {
		t.Errorf("%d sessions observed before any handshake, want none", observing)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatalf("connecting the client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	if serverSession.InitializeParams() == nil {
		t.Fatal("the session did not record an initialize, so this asserts nothing")
	}
	tracker.observe(sessionRequest(serverSession), "")
	if observing := tracker.observingCount(); observing != 1 {
		t.Errorf("%d sessions observed after the handshake, want one", observing)
	}
}

// observingCount reports how many sessions the tracker is measuring, for a test
// that has no other way to see the decision.
func (t *sessionTracker) observingCount() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.observing)
}

// sessionRequest is a real request carrying one session, which is all the
// tracker reads.
//
// A real SDK type rather than a stand-in, because mcp.Request has an unexported
// method and cannot be implemented from outside — which is the SDK saying that a
// request is whatever it hands a handler, and not something a test gets to
// invent.
func sessionRequest(session *mcp.ServerSession) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{Session: session, Params: &mcp.CallToolParamsRaw{Name: "search"}}
}
