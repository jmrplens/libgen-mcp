package mcpotel

import (
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// AttrHTTPRequestMethod is the Stable HTTP convention's method key, and the one
// key of this group with a reader outside this package.
//
// A collector reads it to tell an outbound fetch apart from the MCP server span
// sharing its trace, and it reads this constant rather than spelling the string
// a second time. A second spelling is a record that empties itself the day the
// convention renames one, silently.
const AttrHTTPRequestMethod = attribute.Key("http.request.method")

// The other attribute keys for an outbound HTTP call, from the Stable HTTP
// conventions.
const (
	attrHTTPResponseStatus = attribute.Key("http.response.status_code")
	attrServerAddress      = attribute.Key("server.address")
	// attrServerPort pairs with server.address; two ports on one host are
	// two endpoints.
	attrServerPort            = attribute.Key("server.port")
	attrNetworkProtocolName   = attribute.Key("network.protocol.name")
	attrURLScheme             = attribute.Key("url.scheme")
	attrErrorTypeForTransport = AttrErrorType
)

// NewTransport wraps a RoundTripper so every outbound fetch becomes a child span
// of whatever MCP operation caused it.
//
// It is installed at the one chokepoint every source, probe, mirror lookup and
// redirect hop already inherits — netguard's client — so a source added later
// is instrumented without anybody remembering to instrument it.
//
// # Why not otelhttp
//
// The contrib instrumentation is the obvious choice and it records url.full,
// which on this server means a span carrying **what somebody read**: a download
// URL frequently has the md5 or the DOI in its path, and a Sci-Hub or Anna's URL
// names the item outright. That is the same value the identity policy protects
// and the same one D1 redacts out of an error string — one rule with two exits,
// and shipping it here through a third door would be worse than not
// instrumenting at all, because it would look like a considered privacy position
// while being none.
//
// A resolved member URL is worse still: it is a working credential, usable by
// whoever reads the trace.
//
// Redacting url.full afterwards is possible, with a SpanProcessor rewriting it
// at OnStart, and it is more machinery than the value justifies. So this records
// what an operator actually needs and nothing else.
//
// # What a trace shows without the path
//
// It is a smaller loss than it sounds, because the parent span already names the
// operation and the source: gen_ai.tool.name says download or search, and
// libgen_mcp.source says which entry of the chain served it, which is what an
// operator is actually asking. The child spans then answer the questions the
// parent cannot: how many round trips one tool call took, how long each took,
// which one failed, and whether a retry happened. A federated search showing up
// as eight concurrent children, seven of them fast and one of them the whole
// duration, is exactly the kind of thing that is invisible in a log and obvious
// in a trace.
//
// # Errors
//
// A transport error is a failure. A 4xx or 5xx is NOT marked as a span error
// here, deliberately: "For HTTP status codes in the 4xx range span status ...
// SHOULD be set to Error in case of SpanKind.CLIENT", which would make every
// expected 404 from a not-found probe a red span. This server treats a 404 as
// an answer rather than a failure in its own handlers, and the span should
// agree with the handler rather than with a rule written for a generic client.
// The status code is always recorded, so a dashboard can classify however it
// likes.
func NewTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &instrumentedTransport{
		base:     base,
		tracer:   otel.Tracer(scopeName),
		duration: newHTTPClientDurationHistogram(otel.Meter(scopeName)),
	}
}

// metricServerAddresses is the closed set of hosts the http.client metric may
// carry, set once at startup from what this process is configured to reach.
//
// The span always carries the real host. The metric is where the label must not
// be somebody else's to choose, and here it is chosen twice over: the mirror
// list is discovered and rotates, and an open-access provider hands back a
// publisher URL this server then fetches — so the host on an outbound request is
// frequently named by a third party. Every one of those would otherwise mint a
// series, and the SDK's cardinality cap fills with them until real measurements
// collapse into the overflow bucket.
//
// A View with an AttributeFilter would drop the label in the SDK instead, and it
// is deliberately not used: bounding the value here means it is never recorded
// at all, where a filter removes it from the aggregation and **still lets it
// reach a collector through exemplars**. A View is a cardinality mechanism, not
// a privacy one.
var metricServerAddresses atomic.Pointer[map[string]struct{}]

// SetMetricServerAddresses declares which hosts the http.client metric may name.
// Unset, or set empty, every host is recorded as [OtherServerAddress].
func SetMetricServerAddresses(hosts []string) {
	set := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		if host != "" {
			set[host] = struct{}{}
		}
	}
	metricServerAddresses.Store(&set)
}

// OtherServerAddress is what the metric carries for a host outside the declared
// set: the absence of a known name, not the name a caller sent.
const OtherServerAddress = "_OTHER"

// boundedServerAddress returns the host as the metric may carry it.
func boundedServerAddress(host string) string {
	set := metricServerAddresses.Load()
	if set == nil {
		return OtherServerAddress
	}
	if _, ok := (*set)[host]; ok {
		return host
	}
	return OtherServerAddress
}

type instrumentedTransport struct {
	base     http.RoundTripper
	tracer   trace.Tracer
	duration metric.Float64Histogram
}

func (t *instrumentedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// The convention's client span name is the method alone when there is no
	// low-cardinality route template, and there is none here: an outbound path
	// carries the item's identifier, so a name built from one would mint a
	// distinct span name per book.
	host := req.URL.Hostname()
	port := serverPort(req.URL)
	shared := []attribute.KeyValue{
		AttrHTTPRequestMethod.String(req.Method),
		attrURLScheme.String(req.URL.Scheme),
		attrNetworkProtocolName.String("http"),
	}

	// The span carries the real endpoint; the metric carries it only when the
	// host is one this process is configured to reach, and the port rides with
	// the host: both come off a URL a third party may have chosen, and a bounded
	// address next to an unbounded port would move the same cardinality one
	// column over. See metricServerAddresses.
	spanAttrs := append(append([]attribute.KeyValue(nil), shared...),
		attrServerAddress.String(host), attrServerPort.Int(port))

	ctx, span := t.tracer.Start(req.Context(), req.Method,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(spanAttrs...),
	)
	defer span.End()

	started := time.Now()
	// The request handed down is a clone, not this one: a RoundTripper "should
	// not modify the request", and the download pipeline reuses request objects
	// across its own retry schedule.
	//
	// StripOutbound is applied here rather than left to the caller, because this
	// is the boundary: everything above it is this server's own context and
	// everything below it is a third party's connection. It also clones, which
	// is what satisfies the rule above. See its doc for why leaving the trace
	// context in place is not an option on this server.
	resp, err := t.base.RoundTrip(StripOutbound(req.WithContext(ctx)))
	elapsed := time.Since(started).Seconds()

	metricAttrs := append([]attribute.KeyValue(nil), shared...)
	if bounded := boundedServerAddress(host); bounded == host {
		metricAttrs = append(metricAttrs, attrServerAddress.String(host), attrServerPort.Int(port))
	} else {
		metricAttrs = append(metricAttrs, attrServerAddress.String(bounded))
	}
	switch {
	case err != nil:
		// A transport error, which is the only failure this layer treats as
		// one: no response arrived at all. The error text is not recorded,
		// because it carries addresses and would make error.type unbounded.
		span.SetStatus(codes.Error, "")
		span.SetAttributes(attrErrorTypeForTransport.String(ErrorTypeOther))
		metricAttrs = append(metricAttrs, attrErrorTypeForTransport.String(ErrorTypeOther))
	case resp != nil:
		status := attrHTTPResponseStatus.Int(resp.StatusCode)
		span.SetAttributes(status)
		metricAttrs = append(metricAttrs, status)
	}

	t.duration.Record(ctx, elapsed, metric.WithAttributes(metricAttrs...))
	return resp, err
}

// newHTTPClientDurationHistogram builds the Stable HTTP client instrument.
//
// The boundaries are the convention's own for this metric, which are tighter
// than the MCP operation ones because a single API call is expected to be
// faster than the tool call containing it.
func newHTTPClientDurationHistogram(meter metric.Meter) metric.Float64Histogram {
	histogram, err := meter.Float64Histogram(
		"http.client.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of an outbound request to a mirror or an open-access source."),
		metric.WithExplicitBucketBoundaries(
			0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10,
		),
	)
	if err != nil {
		otel.Handle(err)
	}
	return histogram
}

// serverPort resolves the port the request will really use, scheme default
// included, because "no port written" and "port 443 written out" are the same
// endpoint and must not be two label values.
func serverPort(u *url.URL) int {
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			return n
		}
	}
	if u.Scheme == "http" {
		return 80
	}
	return 443
}
