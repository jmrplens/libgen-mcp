// Package collectore2e proves that a real OTLP receiver accepts what this
// server exports.
//
// The in-process stub in test/e2e/http answers 200 to anything. That is the
// right shape for asking what a payload does *not* contain — it keeps the bytes
// and never decodes — and it is exactly the wrong shape for asking whether the
// bytes mean anything. A stub answers 200 to a malformed protobuf, to a resource
// missing an attribute a pipeline requires, to a metric whose unit contradicts
// its name and to a span kind out of range, every one of which ships telemetry
// no backend can read behind a green suite.
//
// So this module starts a genuine OpenTelemetry Collector, exports into it, and
// reads back what it decoded: the pipeline ends in a file exporter, and the
// collector writes OTLP JSON only after parsing the protobuf, routing it and
// re-encoding it. A document appearing in that file is evidence that a real
// implementation understood what was sent.
//
// It needs Docker and is therefore **run by hand**, through
// make test-e2e-collector, following this repository's own precedent for
// validate-http-stateless. Nothing in CI installs a daemon; if that changes, the
// race workflow is where this would hang.
//
// The tests carry the collectore2e build tag; this file is what a plain build
// sees of the package.
package collectore2e
