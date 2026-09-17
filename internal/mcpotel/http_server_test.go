package mcpotel

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// serveOnce drives one request through the server middleware with a real SDK
// behind it, and returns what was recorded.
func serveOnce(t *testing.T, req *http.Request, handler http.Handler, skip ...string) recorded {
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

	ServerMiddleware(handler, skip...).ServeHTTP(httptest.NewRecorder(), req)

	if err := tracerProvider.ForceFlush(t.Context()); err != nil {
		t.Fatalf("flushing spans: %v", err)
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	return recorded{spans: spanRecorder.Ended(), metrics: metrics}
}

// statusHandler answers with a fixed status.
func statusHandler(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	})
}

// TestServerSpanCoversARefusalTheMCPSpanNeverSees is what this layer is for.
//
// A request the host guard answers never reaches the MCP middleware, so without
// this an operator of a published endpoint cannot see the traffic they most need
// to watch: how much is being refused, and how long the refusal takes.
func TestServerSpanCoversARefusalTheMCPSpanNeverSees(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
	got := serveOnce(t, req, statusHandler(http.StatusForbidden))

	span := got.spanNamed(t, http.MethodPost)
	if span.SpanKind() != trace.SpanKindServer {
		t.Errorf("span kind = %v, want server", span.SpanKind())
	}
	if attrsOf(span)[string(attrHTTPResponseStatus)] != "403" {
		t.Errorf("status = %q, want 403", attrsOf(span)[string(attrHTTPResponseStatus)])
	}
	// A 4xx leaves the span status unset, and the convention is explicit that
	// this is a MUST for a server span: a refused Host is the server working
	// correctly, not failing.
	if span.Status().Code == codes.Error {
		t.Error("a 403 was marked as a span error")
	}
	if points := got.histogramAttrs(t, "http.server.request.duration"); len(points) != 1 {
		t.Errorf("%d duration points, want one", len(points))
	}
}

// TestServerSpanMarksAServerFailure is the other half: a 5xx is ours.
func TestServerSpanMarksAServerFailure(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	got := serveOnce(t, req, statusHandler(http.StatusInternalServerError))

	span := got.spanNamed(t, http.MethodGet)
	if span.Status().Code != codes.Error {
		t.Error("a 500 is not marked as a span error")
	}
	// No description: any text would come from a handler this middleware cannot
	// see, which is where a mirror's own response body could leak in.
	if span.Status().Description != "" {
		t.Errorf("the status carries a description: %q", span.Status().Description)
	}
}

// TestHealthIsNotInstrumented keeps a balancer's probe out of the traces.
//
// It polls at a fixed interval forever, so a span per probe would bury every
// real request — and the route answers a two-line handler that can only fail by
// the process being gone, which the probe itself already reports.
func TestHealthIsNotInstrumented(t *testing.T) {
	served := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
	got := serveOnce(t, req, handler, "/health")

	if !served {
		t.Fatal("the request was not forwarded, so the exclusion drops traffic rather than instrumentation")
	}
	if len(got.spans) != 0 {
		t.Errorf("the health probe produced %d spans: %s", len(got.spans), got.spanNames())
	}
	if points := got.histogramAttrs(t, "http.server.request.duration"); len(points) != 0 {
		t.Errorf("the health probe produced %d measurements", len(points))
	}
}

// TestOnlyTheExactHealthPathIsExcluded keeps the exclusion narrow.
//
// A path that merely starts with the excluded one is a different route, and on a
// published endpoint it is as likely to be a scanner as anything else — so it is
// instrumented like everything else.
func TestOnlyTheExactHealthPathIsExcluded(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health/../admin", nil)
	got := serveOnce(t, req, statusHandler(http.StatusNotFound), "/health")

	if len(got.spans) != 1 {
		t.Errorf("%d spans for a path near the excluded one, want one: %s", len(got.spans), got.spanNames())
	}
}

// TestServerSpanRecordsNoPath is the cardinality rule for a published endpoint.
//
// The path is whatever a scanner sends, so /wp-admin.php and ten thousand
// friends would each mint a series. Method and status answer what an HTTP-level
// view is for; what was called is on the MCP span.
func TestServerSpanRecordsNoPath(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/wp-admin.php?a=b", nil)
	got := serveOnce(t, req, statusHandler(http.StatusNotFound))

	for key, value := range attrsOf(got.spanNamed(t, http.MethodGet)) {
		if strings.Contains(value, "wp-admin") {
			t.Errorf("%s = %q, which carries the path a caller chose", key, value)
		}
	}
}

// TestServerSpanBoundsAMethodTheCallerInvented is the same rule on the one
// instrument every request touches.
//
// net/http accepts any token as a method, so r.Method is a string the caller
// chooses: on a metric that is one time series per invented verb.
func TestServerSpanBoundsAMethodTheCallerInvented(t *testing.T) {
	invented := strings.Repeat("Z", 200)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Method = invented
	got := serveOnce(t, req, statusHandler(http.StatusMethodNotAllowed))

	// "HTTP" for the name rather than _OTHER, which the convention states
	// separately: a name is a label a backend groups by, and _OTHER there would
	// read as a method rather than as the absence of one.
	span := got.spanNamed(t, "HTTP")
	attrs := attrsOf(span)
	if attrs[string(AttrHTTPRequestMethod)] != "_OTHER" {
		t.Errorf("%s = %q, want _OTHER", AttrHTTPRequestMethod, attrs[string(AttrHTTPRequestMethod)])
	}
	// The original goes on the span, truncated: this runs before every guard and
	// the header budget is a megabyte, so an unbounded copy would relay whatever
	// an anonymous caller sent to the operator's collector.
	original := attrs[string(attrHTTPRequestMethodOriginal)]
	if len(original) != maxOriginalMethod {
		t.Errorf("the original method is %d characters, want it bounded to %d", len(original), maxOriginalMethod)
	}
	// And never on the metric, which is the label space a caller must not choose.
	points := got.histogramAttrs(t, "http.server.request.duration")
	if len(points) != 1 {
		t.Fatalf("%d points, want one", len(points))
	}
	if _, present := points[0][string(attrHTTPRequestMethodOriginal)]; present {
		t.Errorf("the invented method reached a metric dimension: %v", points[0])
	}
	if points[0][string(AttrHTTPRequestMethod)] != "_OTHER" {
		t.Errorf("the metric recorded %q", points[0][string(AttrHTTPRequestMethod)])
	}
}

// TestKnownMethodIsCaseSensitive follows the convention, which says HTTP methods
// are: "get" is not GET, and recording it as one would report a request this
// server did not route that way.
func TestKnownMethodIsCaseSensitive(t *testing.T) {
	t.Parallel()

	if recorded, original := knownMethod("GET"); recorded != "GET" || original != "" {
		t.Errorf("knownMethod(GET) = %q, %q", recorded, original)
	}
	if recorded, original := knownMethod("get"); recorded != "_OTHER" || original != "get" {
		t.Errorf("knownMethod(get) = %q, %q, want it substituted", recorded, original)
	}
}

// TestCallerCannotSwitchOffTheRecordingOfItsOwnRefusal is the sampling
// decision, and it matters more here than anywhere else.
//
// Every caller of a published endpoint is anonymous. The default sampler is
// ParentBased(AlwaysOn), so a caller sending flags 00 would make this span
// non-recording — including the span for the refusal it is about to receive.
// An operator who set OTEL_TRACES_SAMPLER took that decision deliberately and
// keeps it; one who never asked gets a front door a caller cannot switch off.
func TestCallerCannotSwitchOffTheRecordingOfItsOwnRefusal(t *testing.T) {
	// An unsampled remote context, which is what flags 00 produces.
	const unsampled = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"

	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })

	for _, name := range []string{"unset", "set but blank"} {
		t.Run("ignored when the operator configured no sampler: "+name, func(t *testing.T) {
			// t.Setenv first so the cleanup is registered, then removed for the
			// unset case: a blank value and an absent one must reach the same
			// answer, because an orchestrator injects the first for the second.
			t.Setenv("OTEL_TRACES_SAMPLER", "")
			if name == "unset" {
				if err := os.Unsetenv("OTEL_TRACES_SAMPLER"); err != nil {
					t.Fatalf("unsetting the sampler: %v", err)
				}
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
			req.Header.Set("traceparent", unsampled)

			got := serveOnce(t, req, statusHandler(http.StatusForbidden))
			if len(got.spans) != 1 {
				t.Errorf("%d spans, want the refusal recorded despite the caller's cleared flag", len(got.spans))
			}
		})
	}

	t.Run("honored when the operator configured one", func(t *testing.T) {
		t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_traceidratio")
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
		req.Header.Set("traceparent", unsampled)

		got := serveOnce(t, req, statusHandler(http.StatusForbidden))
		if len(got.spans) != 0 {
			t.Errorf("%d spans, want the operator's sampler decision respected", len(got.spans))
		}
	})
}

// TestRequestSchemeReadsTheConnectionAndNotAHeader keeps a cosmetic attribute
// from being a caller's to choose.
//
// X-Forwarded-Proto is attacker-controlled on any deployment reachable without a
// proxy, and nothing here branches on the scheme — so a wrong one misinforms a
// dashboard rather than changing behavior, which is exactly the kind of value
// not worth trusting a header for.
func TestRequestSchemeReadsTheConnectionAndNotAHeader(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	if got := requestScheme(req); got != "http" {
		t.Errorf("requestScheme = %q for a plaintext connection, want http", got)
	}
}

// TestStatusRecorderForwardsFlush is not optional on this server.
//
// The default HTTP mode answers with text/event-stream, and an SSE response that
// is never flushed is a response the client never sees — so wrapping the
// ResponseWriter without forwarding Flush would turn every streaming response
// into a hang.
func TestStatusRecorderForwardsFlush(t *testing.T) {
	t.Parallel()

	inner := httptest.NewRecorder()
	recorder := &statusRecorder{ResponseWriter: inner, status: http.StatusOK}
	recorder.Flush()

	if !inner.Flushed {
		t.Error("Flush was not forwarded; an SSE response would hang")
	}
	// A flush commits the implicit 200: after bytes are on the wire a later
	// WriteHeader changes nothing the client sees, so recording its status
	// would label the measurement with a code that was never sent.
	recorder.WriteHeader(http.StatusInternalServerError)
	if recorder.status != http.StatusOK {
		t.Errorf("status = %d after a flush, want the committed 200", recorder.status)
	}
}
