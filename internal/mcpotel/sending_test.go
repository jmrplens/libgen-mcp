package mcpotel

import (
	"context"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// driveSending runs one server-initiated request through the sending middleware
// with a real SDK behind it.
func driveSending(t *testing.T, opts Options, method string, handler mcp.MethodHandler) recorded {
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

	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "irrelevant"}}
	_, _ = SendingMiddleware(opts)(handler)(t.Context(), method, req)

	if err := tracerProvider.ForceFlush(t.Context()); err != nil {
		t.Fatalf("flushing spans: %v", err)
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	return recorded{spans: spanRecorder.Ended(), metrics: metrics}
}

// TestSendingMiddlewareRecordsAClientSpan covers the direction this server
// initiates.
//
// It records nothing in production today — this server's elicitation is the
// result-carried form and nothing calls session.Elicit — and it is here for the
// rule in its doc comment, which is what the first server-initiated request
// under a deadline will meet.
func TestSendingMiddlewareRecordsAClientSpan(t *testing.T) {
	handler := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return &mcp.CallToolResult{}, nil
	}
	got := driveSending(t, Options{Transport: TransportPipe}, "notifications/progress", handler)

	span := got.spanNamed(t, "notifications/progress")
	if span.SpanKind() != trace.SpanKindClient {
		t.Errorf("span kind = %v, want client: this server is the caller here", span.SpanKind())
	}
	attrs := attrsOf(span)
	if attrs[string(AttrMCPMethodName)] != "notifications/progress" {
		t.Errorf("%s = %q", AttrMCPMethodName, attrs[string(AttrMCPMethodName)])
	}
	if attrs[string(AttrNetworkTransport)] != TransportPipe {
		t.Errorf("%s = %q, want %q", AttrNetworkTransport, attrs[string(AttrNetworkTransport)], TransportPipe)
	}

	if points := got.histogramAttrs(t, "mcp.client.operation.duration"); len(points) != 1 {
		t.Errorf("%d client duration points, want one per request", len(points))
	}
}

// TestSendingMiddlewareCountsEveryErrorCode is the client-side rule, and it
// contradicts the server-side one on purpose.
//
// "All JSON-RPC error codes SHOULD be considered errors" when we are the caller:
// a caller-fault code such as method-not-found is not the receiver's failure,
// but a client that cannot serve what this server asked for is exactly the thing
// to notice.
func TestSendingMiddlewareCountsEveryErrorCode(t *testing.T) {
	handler := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return nil, &jsonrpc.Error{Code: -32601, Message: "method not found"}
	}
	got := driveSending(t, Options{}, "elicitation/create", handler)

	attrs := attrsOf(got.spanNamed(t, "elicitation/create"))
	if attrs[string(AttrErrorType)] != "-32601" {
		t.Errorf("%s = %q, want the code: every error code counts when this server is the caller",
			AttrErrorType, attrs[string(AttrErrorType)])
	}

	// The same code on the receiving side is the caller's fault and not a
	// failure, which is the contradiction stated as two functions.
	if serverSide := classify(nil, &jsonrpc.Error{Code: -32601}); serverSide.failed {
		t.Error("the server-side classification now counts a caller-fault code as a failure")
	}
}

// TestClassifyClientTreatsATransportFailureAsUnclassifiable pins the fallback.
func TestClassifyClientTreatsATransportFailureAsUnclassifiable(t *testing.T) {
	t.Parallel()

	got := classifyClient(errors.New("the pipe closed"))
	if !got.failed {
		t.Error("a transport failure is not counted as a failure")
	}
	if got.errorType != ErrorTypeOther {
		t.Errorf("errorType = %q, want %q", got.errorType, ErrorTypeOther)
	}
	// Nothing to classify means no status code invented for it.
	if got.statusCode != "" {
		t.Errorf("statusCode = %q, want none", got.statusCode)
	}
	// And a success is a success.
	if classifyClient(nil).failed {
		t.Error("a nil error was classified as a failure")
	}
}
