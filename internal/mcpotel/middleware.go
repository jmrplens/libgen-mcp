package mcpotel

import (
	"context"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// scopeName is the instrumentation scope every span and measurement carries.
//
// It names the code doing the instrumenting, not the code being instrumented,
// which is what lets an operator tell our spans apart from those of a library
// that also instruments this process. The specification asks for "the
// instrumentation scope, such as the instrumentation library name", and a Go
// import path is the unambiguous form of that.
const scopeName = "github.com/jmrplens/libgen-mcp/internal/mcpotel"

// Options configure the middleware.
type Options struct {
	// Callers turns a caller into attributes, subject to the deployment's
	// identity policy. Nil means nothing about who made a call is ever
	// recorded, which is also what the default policy does.
	Callers CallerAttributer

	// Transport is "pipe" for stdio and "tcp" for HTTP, which is what the
	// convention's note prescribes rather than a name of our choosing. Use
	// [TransportPipe] and [TransportTCP].
	Transport string

	// ProtocolVersions are the MCP revisions this server admits. Only a version
	// in this list is ever recorded, because the value arrives from the caller
	// and lands on a metric dimension; see protocolVersionFor. Empty means the
	// attribute is never recorded, which is the safe default for a caller that
	// has not thought about it.
	ProtocolVersions []string
}

// Middleware instruments every MCP request with a span and a duration
// measurement.
//
// # Shape
//
// One span per MCP request, SERVER kind, parented from the trace context in
// params._meta rather than from the transport. The convention gives the reason:
// one MCP request can be served by several HTTP requests when a client retries,
// and one streamable HTTP request can carry more than one MCP request, so
// parenting to the transport would attach an operation to whichever round trip
// happened to carry it.
//
// # No enabled flag
//
// There is none, deliberately. Without an installed SDK, otel.Tracer and
// otel.Meter return working no-ops that still propagate span context, so a flag
// would only add a branch that can disagree with whether telemetry is actually
// running. The cost of instrumenting unconditionally is the attribute
// construction below, which is a handful of constant-keyed strings.
func Middleware(opts Options) mcp.Middleware {
	callers := opts.Callers
	if callers == nil {
		callers = noCallerAttributes{}
	}
	tracer := otel.Tracer(scopeName)
	meter := otel.Meter(scopeName)
	duration := newDurationHistogram(meter)
	allowed := allowedVersions(opts.ProtocolVersions)

	// Built once: this is the same for every request this process serves, and
	// rebuilding it per call would allocate on the hot path for no reason.
	constant := make([]attribute.KeyValue, 0, 1)
	if opts.Transport != "" {
		constant = append(constant, AttrNetworkTransport.String(opts.Transport))
	}

	sessions := newSessionTracker(meter, constant, opts.Transport)

	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			current := describe(method, req)

			// Extract before Start, so the incoming trace context is the parent
			// rather than a link. A malformed or absent value leaves ctx
			// untouched, which is the propagator's specified behavior and the
			// reason no error is checked here.
			//
			// The ambient context is read first because Extract replaces it. On
			// HTTP that ambient span is this server's own HTTP span, and the
			// convention asks for both relationships: parent on the MCP context,
			// "and SHOULD link current ambient context, if it's present".
			// Without the link, a trace that arrives through _meta loses every
			// trace of which HTTP request carried it.
			//
			// The extracted context is bounded before it is used, for the
			// tracestate reason in [sanitizeRemoteContext]. The sampling half of
			// that helper is deliberately off here: this layer runs after the
			// inbound guards, so the caller is one the deployment admitted, and
			// honoring their sampled flag is what the convention asks for.
			ambient := trace.SpanContextFromContext(ctx)
			extracted := otel.GetTextMapPropagator().Extract(ctx, carrierFor(req))
			ctx = sanitizeRemoteContext(extracted, true)
			parent := trace.SpanContextFromContext(ctx)

			// The version is bounded by the allow-list, so it is cheap enough to
			// carry on the metric too. The session id is not: it is one value
			// per connected client, and the convention's own instrument table
			// omits it for exactly that reason.
			version := protocolVersionFor(req, allowed)

			// Identity is resolved before the span starts, like everything else
			// on it, because a sampler can only see what was present at
			// creation. A deployment sampling by caller needs it there.
			//
			// The context returned by Start is the one passed onward. Passing
			// the original would compile, run, and silently produce a flat trace
			// with every outbound fetch as a root — which here destroys the one
			// thing the tree is for, since a federated search fans out to
			// several providers concurrently and the shape of that fan-out is
			// the answer to "why was this search slow".
			ctx, span := tracer.Start(ctx, current.spanName, spanStartOptions(spanStart{
				constant: constant,
				call:     current,
				version:  version,
				session:  sessionIDOf(req),
				identity: callers.CallerAttributes(ctx, req),
				ambient:  ambient,
				parent:   parent,
			})...)

			// The handler reports its refusal and its source chain through here,
			// since the middleware can learn neither from the result.
			ctx, holder := withCallHolder(ctx)
			// Deferred so a panic still ends the span, which is what makes the
			// SDK record the panic as an exception event before re-panicking. A
			// panic also skips the recording below, so the source chain would be
			// lost for the one call whose trace is read most: the chain had
			// already reported three failures when the handler blew up.
			reported := false
			defer func() {
				if !reported {
					span.SetAttributes(holder.snapshot().spanAttributes()...)
				}
				span.End()
			}()

			// The tracker deliberately outlives this request: it parks a
			// goroutine on the session, which ends long after the call returns.
			sessions.observe(req, version) //nolint:contextcheck // the session outlives the request, so a request context would cancel the measurement

			started := time.Now()
			res, err := next(ctx, method, req)
			result := classify(res, err)

			// What the chain reported, read once. Which source served and how
			// much of the chain it cost is the question no layer above the chain
			// can answer, and it is the whole reason this instrumentation is
			// worth its cost on this server.
			chain := holder.snapshot()
			span.SetAttributes(chain.spanAttributes()...)
			reported = true

			// Before End, not inside it. After End, SetStatus and SetAttributes
			// are silent no-ops guarded by isRecording, so an outcome recorded
			// afterwards leaves the span green with nothing to say why.
			result.record(span)

			duration.Record(ctx, time.Since(started).Seconds(),
				metric.WithAttributes(durationAttributes(constant, current, result, chain, version)...))

			return res, err
		}
	}
}

// spanStart is everything the request span is created with, gathered so the
// middleware body reads as a sequence rather than as an assembly.
type spanStart struct {
	constant []attribute.KeyValue
	call     call
	version  string
	session  string
	identity []attribute.KeyValue
	// ambient is the span that was current before the incoming trace context
	// replaced it, and parent is what replaced it.
	ambient trace.SpanContext
	parent  trace.SpanContext
}

// spanStartOptions turns that into the options tracer.Start takes.
//
// Everything is fixed here rather than set afterwards because a sampler can only
// see what was present at creation: an attribute added after Start has already
// missed the decision it would have informed.
func spanStartOptions(s spanStart) []trace.SpanStartOption {
	// The optional pair is gathered first so the list is sized by the slices it
	// joins, in one allocation, rather than by a count kept beside them.
	optional := make([]attribute.KeyValue, 0, 2)
	if s.version != "" {
		optional = append(optional, AttrMCPProtocolVersion.String(s.version))
	}
	if s.session != "" {
		optional = append(optional, AttrMCPSessionID.String(s.session))
	}

	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(slices.Concat(s.constant, s.call.attributes, optional, s.identity)...),
	}
	// Only when Extract actually changed the parent. With no incoming context
	// the ambient span is already the parent, and linking a span to its own
	// parent says nothing.
	if s.ambient.IsValid() && !s.parent.Equal(s.ambient) {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: s.ambient}))
	}
	return opts
}

// durationAttributes are the dimensions one measurement carries.
//
// It deliberately omits the identity attributes the span carries. Every distinct
// label combination is a time series that has to be stored and paid for, and a
// per-caller dimension is unbounded by construction: it grows with the number of
// people using the deployment, which on a public endpoint is the internet. The
// Go SDK would drop the overflow into a bucket marked otel.metric.overflow
// rather than refuse it, so the failure would be silent data destruction rather
// than an error.
//
// Assembled from scratch rather than filtered from the span's list, because a
// filter is a list of what to remove and this is a list of what to keep — and
// only the second kind stays correct when somebody adds an attribute.
func durationAttributes(constant []attribute.KeyValue, current call, result outcome, chain holderSnapshot, version string) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, len(constant)+len(current.attributes)+5)
	attrs = append(attrs, constant...)
	attrs = append(attrs, metricAttributesFor(current, result)...)
	if version != "" {
		attrs = append(attrs, AttrMCPProtocolVersion.String(version))
	}
	attrs = append(attrs, result.metricAttributes()...)
	return append(attrs, chain.metricAttributes()...)
}

// call is what one request turned out to be, resolved before the span starts.
type call struct {
	// callerNameKey names the attribute whose value the caller chose, or is
	// empty when the request carries no such attribute. It exists so the metric
	// can bound a value the span records verbatim; see metricAttributesFor.
	callerNameKey attribute.Key

	spanName   string
	attributes []attribute.KeyValue
}

// describe works out the span name and creation-time attributes for a request.
//
// The naming rule is the convention's: "{mcp.method.name} {target}" where the
// target is the tool or prompt name when there is one, and the bare method name
// otherwise.
//
// There is no resource branch, and that is not an omission: this server declares
// no resources and answers the resource methods -32601 through capguard, so a
// resources/read span would describe a call that cannot happen. The disclosure
// those branches guarded — which item a call was about — arrives here through
// the item redactor instead, on the download and read paths where it is real.
func describe(method string, req mcp.Request) call {
	attrs := []attribute.KeyValue{AttrMCPMethodName.String(method)}

	switch params := paramsOf(req).(type) {
	case *mcp.CallToolParamsRaw:
		return describeToolCall(method, params.Name, attrs)
	case *mcp.CallToolParams:
		return describeToolCall(method, params.Name, attrs)

	case *mcp.GetPromptParams:
		// Four names, compiled in, so this is unambiguously low cardinality —
		// but the name still arrives from the caller, and metricAttributesFor
		// bounds it for that reason rather than for this one.
		if params.Name != "" {
			attrs = append(attrs, AttrGenAIPromptName.String(params.Name))
			return call{
				spanName:      method + " " + params.Name,
				attributes:    attrs,
				callerNameKey: AttrGenAIPromptName,
			}
		}
	}

	return call{spanName: method, attributes: attrs}
}

// describeToolCall names a tool call.
//
// gen_ai.tool.name is what the client asked for and is always recorded, because
// the convention makes it Conditionally Required whenever the operation relates
// to a specific tool. Four plain names is the whole surface here, so unlike a
// server with a dispatching tool there is nothing further to resolve: the tool
// name is what was done.
func describeToolCall(method, toolName string, attrs []attribute.KeyValue) call {
	if toolName == "" {
		return call{spanName: method, attributes: attrs}
	}

	attrs = append(attrs,
		AttrGenAIToolName.String(toolName),
		// "SHOULD be set to execute_tool when the operation describes a tool
		// call and SHOULD NOT be set otherwise", which is why it appears here
		// and in no other branch.
		AttrGenAIOperationName.String("execute_tool"),
	)

	return call{
		spanName:      method + " " + toolName,
		attributes:    attrs,
		callerNameKey: AttrGenAIToolName,
	}
}

// newDurationHistogram builds the convention's server duration instrument.
//
// The bucket boundaries are the convention's, passed explicitly because the Go
// SDK's default set is wrong for this metric: "This metric SHOULD be specified
// with ExplicitBucketBoundaries of [0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5,
// 10, 30, 60, 120, 300]". Seconds, not milliseconds, which the convention also
// fixes and which is the opposite of the duration this server logs.
//
// The error is checked rather than discarded. Go's Meter ends every creation
// method with "return i, validateInstrumentName(name)", handing back a fully
// working instrument alongside a non-nil error, so the constructor-shaped call
// invites ignoring it and an invalid name would then record and export nothing
// while looking entirely healthy. A failure here is not worth refusing to
// serve, so it degrades to a no-op instrument and says so once.
func newDurationHistogram(meter metric.Meter) metric.Float64Histogram {
	histogram, err := meter.Float64Histogram(
		"mcp.server.operation.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of an MCP request, measured on the receiver from arrival to response."),
		metric.WithExplicitBucketBoundaries(
			0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 30, 60, 120, 300,
		),
	)
	if err != nil {
		otel.Handle(err)
	}
	return histogram
}

// metricAttributesFor returns the call's attributes as a metric may carry them,
// with a caller-supplied name bounded.
//
// # The hole this closes
//
// gen_ai.tool.name and gen_ai.prompt.name are copied off the request, and the
// convention puts both on the duration metric. Nothing checks that the name
// refers to anything: a prompts/get for a prompt that does not exist would
// record the invented name as a metric dimension value. Any client could then
// mint one time series per string it cared to type, and the SDK's answer to an
// exhausted series budget is not an error but an otel.metric.overflow bucket
// that swallows everything after the limit, first-come-wins under cumulative
// temporality. Silent destruction of the real data, caused by a caller.
//
// # Why the outcome and not a registry
//
// Membership would mean handing this package the set of registered tool and
// prompt names, rebuilt wherever registration happens and drifting the first
// time somebody adds one somewhere new.
//
// The outcome answers the same question without a second copy of the truth: a
// name that names nothing cannot succeed. The SDK answers it with
// invalid-params or method-not-found, both already classified here as caller
// faults, so the substitution keys off a fact this function already has.
//
// The trade is deliberate and small: a real name whose call failed validation is
// bucketed too, so the metric under-reports it while the span still carries it
// exactly. Losing one label on a failed call is worth not letting a caller
// choose how many time series this process stores.
func metricAttributesFor(c call, result outcome) []attribute.KeyValue {
	bound := c.callerNameKey != "" && result.nameIsUnverified()

	out := make([]attribute.KeyValue, 0, len(c.attributes))
	for _, kv := range c.attributes {
		if bound && kv.Key == c.callerNameKey {
			out = append(out, kv.Key.String(ErrorTypeOther))
			continue
		}
		out = append(out, kv)
	}
	return out
}
