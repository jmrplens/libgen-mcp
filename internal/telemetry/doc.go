// Package telemetry is the only place in this server that knows about
// OpenTelemetry.
//
// Everything else reaches observability through the small API here, or through
// the OTel global providers this package installs. That is deliberate: an
// instrumentation library spread across every package is one nobody can remove,
// reconfigure or reason about, and the seams that matter here are few enough to
// be listed. Nothing outside this package imports go.opentelemetry.io.
//
// # Off by default, and why
//
// Telemetry stays off unless an operator turns it on, for privacy rather than
// for cost. Instrumenting a deployment is the operator's decision to make about
// their own users, and a server that traced by default would be making it for
// them. PRIVACY.md's promise is unaffected either way: it is scoped to what the
// maintainer receives, and an exporter an operator points at their own
// collector sends nothing to anyone else.
//
// # Configuration
//
// One switch is ours and the rest is the specification's. LIBGEN_MCP_TELEMETRY
// turns it on; endpoint, headers, timeouts, sampling and resource attributes
// come from the standard OTEL_* environment variables the exporters read
// themselves. Reinventing that surface would mean maintaining a second, worse
// copy of a configuration an operator already knows.
//
// **It is a variable and not a flag**, which is this server's rule rather than
// an omission: flags here are the transport's — where to listen, who may reach
// it, what one caller may spend — and everything else a deployment configures
// is a LIBGEN_MCP_* variable that config.Getenv reads. A --telemetry flag would
// be the first setting with two spellings and a precedence to explain.
//
// The name is deliberate in both halves. It is not in the OTEL_ namespace:
// nothing forbids that (the OTEL_{LANGUAGE}_{FEATURE} convention carries no RFC
// 2119 keyword and addresses SDKs), but the namespace belongs to the
// specification and to the language SDKs, it is actively occupied
// (OTEL_GO_X_RESOURCE, OTEL_GO_X_OBSERVABILITY, OTEL_GO_X_CARDINALITY_LIMIT), a
// future release could claim a plain name like OTEL_ENABLED and change its
// meaning underneath us, and an operator seeing an OTEL_ prefix will reasonably
// assume the SDK is what reads it. And it carries the LIBGEN_MCP_ prefix for
// the reason every variable here does: a stdio server runs in whatever shell
// its client was started from, beside every other tool that person uses, and a
// bare TELEMETRY or OBSERVABILITY is the kind of name two programs on one
// machine will both want. Once shipped, the name and its false default cannot
// move without a major version, so it is decided here or not at all.
//
// The standard OTEL_* variables keep their names and must never be given a
// prefix of ours. They are not read by this code at all: the exporters read
// them. Shadowing them under LIBGEN_MCP_ would mean passing the value as a
// programmatic option, which in Go is applied after the environment and so
// silently kills the variable it was meant to mirror, and it would break the
// ordinary case of a host that exports OTEL_EXPORTER_OTLP_ENDPOINT once for
// every service running on it.
//
// OTEL_SDK_DISABLED is honored as a veto on top, which is a different thing
// from an off switch; [SDKDisabledByEnv] says why the two cannot be collapsed.
// It is also the one variable on this surface parsed under the specification's
// grammar rather than this server's: the specification accepts "true" alone,
// and a variable it governs is not ours to loosen.
//
// # What an attribute may carry
//
// The same discipline the logs already keep, stated here because a span makes
// it easy to break: record what was called and how it ended, never what was
// passed. Tool names, outcome, duration and the mirror or source that served a
// call are in; search queries, titles, authors, DOIs, md5 hashes, file paths and
// credentials are out.
//
// The existing code is the precedent. A search logs how many results came back
// and not what was asked for; netguard redacts a query string out of a transport
// error before it reaches either sink; the download pipeline names the source it
// tried and not the URL it resolved, because on the member path that URL is
// itself a working credential. A span that recorded any of those would be
// publishing, to a collector, exactly what those three rules exist to keep out
// of a log.
package telemetry
