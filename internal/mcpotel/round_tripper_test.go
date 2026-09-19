package mcpotel

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// roundTripFunc adapts a function to a RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip calls the wrapped function.
func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// driveOutbound sends one request through the instrumented transport with a
// real SDK behind it, and returns what was recorded plus the request the base
// transport actually saw.
func driveOutbound(t *testing.T, target string, base roundTripFunc) (recorded, *http.Request) {
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

	var seen *http.Request
	transport := NewTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req
		return base(req)
	}))

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	req.Header.Set("tracestate", "vendor=state")
	req.Header.Set("baggage", "key=value")

	resp, _ := transport.RoundTrip(req)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	if flushErr := tracerProvider.ForceFlush(t.Context()); flushErr != nil {
		t.Fatalf("flushing spans: %v", flushErr)
	}
	var metrics metricdata.ResourceMetrics
	if collectErr := reader.Collect(t.Context(), &metrics); collectErr != nil {
		t.Fatalf("collecting metrics: %v", collectErr)
	}
	return recorded{spans: spanRecorder.Ended(), metrics: metrics}, seen
}

// okResponse is a stub 200.
func okResponse(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
}

// TestOutboundSpanRecordsTheEndpointAndNeverTheURL is the disclosure this
// instrumentation exists to decline.
//
// A download URL frequently carries the md5 or the DOI in its path, and a
// Sci-Hub or Anna's URL names the item outright — so url.full would export the
// reading list the identity policy protects, and on the member path it would
// export a working credential.
func TestOutboundSpanRecordsTheEndpointAndNeverTheURL(t *testing.T) {
	const target = "https://libgen.li:8443/get.php?md5=9f2b7c1e4a5d6083f1c2b3a4d5e6f708&key=secret"

	got, _ := driveOutbound(t, target, okResponse)

	span := got.spanNamed(t, http.MethodGet)
	if span.SpanKind() != trace.SpanKindClient {
		t.Errorf("span kind = %v, want client", span.SpanKind())
	}
	attrs := attrsOf(span)
	for key, want := range map[string]string{
		string(AttrHTTPRequestMethod):   http.MethodGet,
		string(attrServerAddress):       "libgen.li",
		string(attrServerPort):          "8443",
		string(attrURLScheme):           "https",
		string(attrHTTPResponseStatus):  "200",
		string(attrNetworkProtocolName): "http",
	} {
		t.Run(key, func(t *testing.T) {
			if attrs[key] != want {
				t.Errorf("%s = %q, want %q", key, attrs[key], want)
			}
		})
	}

	// Nothing on the span may carry the path, the query or the whole URL —
	// asserted over every value rather than by checking for one key, since the
	// disclosure is the string and not the name it arrived under.
	for key, value := range attrs {
		for _, forbidden := range []string{"9f2b7c1e4a5d6083f1c2b3a4d5e6f708", "get.php", "key=secret"} {
			t.Run(forbidden, func(t *testing.T) {
				if strings.Contains(value, forbidden) {
					t.Errorf("%s = %q, which carries %q", key, value, forbidden)
				}
			})
		}
	}
	if _, present := attrs["url.full"]; present {
		t.Error("url.full is recorded")
	}
	// The span name is the method alone: a name built from the path would mint
	// a distinct span name per book.
	if span.Name() != http.MethodGet {
		t.Errorf("span name = %q, want the bare method", span.Name())
	}
}

// TestOutboundRequestCarriesNoTraceContext is the rule this server does not
// share with its sibling.
//
// Its downstreams are dozens of third parties, several named by another third
// party. A traceparent injected toward a mirror or a publisher hands a stable
// correlation handle to somebody the operator did not choose and cannot audit.
func TestOutboundRequestCarriesNoTraceContext(t *testing.T) {
	_, seen := driveOutbound(t, "https://example.org/thing", okResponse)

	if seen == nil {
		t.Fatal("the base transport was never called")
	}
	for _, header := range propagationHeaders {
		if value := seen.Header.Get(header); value != "" {
			t.Errorf("the outbound request carries %s: %q", header, value)
		}
	}
}

// TestStripOutboundLeavesTheOriginalRequestAlone keeps a retry able to carry
// what it was built with.
//
// A RoundTripper "should not modify the request", and the download pipeline
// reuses request objects across its own retry schedule — a header deleted in
// place would stay deleted for an attempt that was meant to have it.
func TestStripOutboundLeavesTheOriginalRequestAlone(t *testing.T) {
	t.Parallel()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.org/", http.NoBody)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	req.Header.Set("Authorization", "Bearer not-a-credential-only-a-test-fixture")

	stripped := StripOutbound(req)

	if req.Header.Get("traceparent") == "" {
		t.Error("the original request was mutated")
	}
	if stripped.Header.Get("traceparent") != "" {
		t.Error("the clone still carries the trace context")
	}
	// Only the propagation headers go: everything else the caller set is the
	// request it asked for.
	if stripped.Header.Get("Authorization") == "" {
		t.Error("a header that is not propagation was removed")
	}
	if StripOutbound(nil) != nil {
		t.Error("StripOutbound(nil) returned something")
	}
}

// TestStripOutboundClearsTheSpanFromTheClonedContext closes the door the header
// deletion leaves open.
//
// Deleting the headers is enough for the transport this server installs, which
// injects nothing — but NewTransport wraps whatever RoundTripper it is given,
// and a lower one that propagates from the context would put the trace back on
// the wire behind this function's own promise. The boundary is what is asserted,
// not the one implementation that happens to be underneath it.
func TestStripOutboundClearsTheSpanFromTheClonedContext(t *testing.T) {
	t.Parallel()

	provider := sdktrace.NewTracerProvider()
	ctx, span := provider.Tracer("test").Start(t.Context(), "download")
	defer span.End()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.org/", http.NoBody)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}

	stripped := StripOutbound(req)

	if trace.SpanContextFromContext(stripped.Context()).IsValid() {
		t.Error("the clone still carries a span context, so a lower transport can re-inject the trace")
	}
	// The original keeps it: the outbound child span is recorded under the
	// caller's, and that decision is the caller's rather than this clone's.
	if !trace.SpanContextFromContext(req.Context()).IsValid() {
		t.Error("the original request lost its span, so the outbound span has no parent")
	}
}

// TestOutboundMetricBoundsAHostSomebodyElseChose is the series-budget rule for
// this server's shape.
//
// The mirror list is discovered and rotates, and an open-access index hands back
// a publisher's own URL that this server then fetches — so the host on an
// outbound request is frequently named by a third party. The span carries the
// real one; the metric carries it only when it is a host this deployment is
// configured to reach.
func TestOutboundMetricBoundsAHostSomebodyElseChose(t *testing.T) {
	SetMetricServerAddresses([]string{"libgen.li"})
	t.Cleanup(func() { SetMetricServerAddresses(nil) })

	t.Run("a configured host is named", func(t *testing.T) {
		got, _ := driveOutbound(t, "https://libgen.li/search", okResponse)
		points := got.histogramAttrs(t, "http.client.request.duration")
		if len(points) != 1 {
			t.Fatalf("%d points, want one", len(points))
		}
		if points[0][string(attrServerAddress)] != "libgen.li" {
			t.Errorf("server.address = %q, want the configured host", points[0][string(attrServerAddress)])
		}
	})

	t.Run("a host nobody configured is one bucket", func(t *testing.T) {
		got, _ := driveOutbound(t, "https://a-publisher-an-index-named.example/paper.pdf", okResponse)
		points := got.histogramAttrs(t, "http.client.request.duration")
		if len(points) != 1 {
			t.Fatalf("%d points, want one", len(points))
		}
		if got := points[0][string(attrServerAddress)]; got != OtherServerAddress {
			t.Errorf("server.address = %q, want %q", got, OtherServerAddress)
		}
		// The port rides with the host: a bounded address beside an unbounded
		// port would move the same cardinality one column over.
		if _, present := points[0][string(attrServerPort)]; present {
			t.Errorf("server.port survived beside a bounded address: %v", points[0])
		}
		// And the span still has the truth, because a span has no series budget.
		if attrsOf(got.spanNamed(t, http.MethodGet))[string(attrServerAddress)] != "a-publisher-an-index-named.example" {
			t.Error("the span lost the real host")
		}
	})
}

// TestOutboundStatusCodeIsNotASpanError is deliberate: the chain treats a 404 as
// an answer.
//
// The convention says a 4xx SHOULD be an error on a CLIENT span, and following
// it here would paint every failover attempt red — a source that does not have
// an item answers 404, and the next one serves it. A transport error is the only
// failure this layer counts, because then nothing answered at all.
func TestOutboundStatusCodeIsNotASpanError(t *testing.T) {
	notFound := func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Request: req}, nil
	}
	got, _ := driveOutbound(t, "https://libgen.li/missing", notFound)

	span := got.spanNamed(t, http.MethodGet)
	if span.Status().Code == codes.Error {
		t.Error("a 404 was marked as a span error; the chain treats it as an answer")
	}
	if attrsOf(span)[string(attrHTTPResponseStatus)] != "404" {
		t.Error("the status code was not recorded")
	}
	if _, present := attrsOf(span)[string(AttrErrorType)]; present {
		t.Error("error.type is set for a 404")
	}
}

// TestOutboundTransportErrorIsAFailureWithoutItsText covers the one failure this
// layer does count, and what it refuses to record about it.
//
// The error text carries addresses — net/http prints the whole request URL in a
// *url.Error — so recording it would make error.type unbounded and would put the
// URL back on the span by another route.
func TestOutboundTransportErrorIsAFailureWithoutItsText(t *testing.T) {
	failing := func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: connecting to https://libgen.li/get.php?key=secret failed")
	}
	got, _ := driveOutbound(t, "https://libgen.li/get.php?key=secret", failing)

	span := got.spanNamed(t, http.MethodGet)
	if span.Status().Code != codes.Error {
		t.Error("a transport error is not marked as a span error")
	}
	if attrsOf(span)[string(AttrErrorType)] != ErrorTypeOther {
		t.Errorf("error.type = %q, want %q", attrsOf(span)[string(AttrErrorType)], ErrorTypeOther)
	}
	if got := span.Status().Description; strings.Contains(got, "secret") || strings.Contains(got, "get.php") {
		t.Errorf("the status description carries the URL: %q", got)
	}
}

// TestOutboundSpanIsAChildOfTheCallThatCausedIt is what makes a federated search
// legible: eight concurrent children, one of them the whole duration.
func TestOutboundSpanIsAChildOfTheCallThatCausedIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tracerProvider)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	ctx, parent := tracerProvider.Tracer("test").Start(t.Context(), "tools/call search")
	client := &http.Client{Transport: NewTransport(nil)}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, http.NoBody)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("the request failed: %v", err)
	}
	_ = resp.Body.Close()
	parent.End()

	if flushErr := tracerProvider.ForceFlush(t.Context()); flushErr != nil {
		t.Fatalf("flushing: %v", flushErr)
	}
	got := recorded{spans: spanRecorder.Ended()}
	child := got.spanNamed(t, http.MethodGet)
	if child.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Error("the outbound span is not a child of the call that caused it")
	}
}

// TestNewTransportDefaultsToTheStandardTransport keeps a nil base usable, which
// is what a caller that only wants instrumentation passes.
func TestNewTransportDefaultsToTheStandardTransport(t *testing.T) {
	t.Parallel()

	if NewTransport(nil) == nil {
		t.Fatal("NewTransport(nil) returned nothing")
	}
}
