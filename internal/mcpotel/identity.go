package mcpotel

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
)

// CallerAttributer turns the caller of a request into the attributes a span may
// carry for them, subject to the deployment's identity policy.
//
// # Why this is an interface rather than a call into internal/telemetry
//
// The policy itself, its three modes and its keyring live in internal/telemetry
// beside the rest of the configuration, and the charged address is resolved in
// cmd/server, which owns the trust rules that decide it. Neither belongs here,
// and importing either would tie instrumentation to a package it has no business
// knowing — internal/telemetry pulls the OpenTelemetry **SDK** in, and this
// package imports the API alone, which is what lets it be linked into a binary
// that exports nothing at zero cost. This is the seam between them.
//
// # Why an empty result is the common case
//
// The default policy exports nothing about who made a call, and a deployment
// that never thought about the question keeps that default. So the ordinary
// answer is nil, and every caller must treat it as ordinary rather than as a
// failure to look something up.
type CallerAttributer interface {
	// CallerAttributes returns what may be recorded about the caller of this
	// request, which is frequently nothing.
	CallerAttributes(ctx context.Context, req mcp.Request) []attribute.KeyValue
}

// CallerAttributerFunc wraps a function as a [CallerAttributer].
func CallerAttributerFunc(f func(ctx context.Context, req mcp.Request) []attribute.KeyValue) CallerAttributer {
	return callerAttributerFunc(f)
}

// callerAttributerFunc is the function adapter.
type callerAttributerFunc func(ctx context.Context, req mcp.Request) []attribute.KeyValue

// CallerAttributes calls the wrapped function.
func (f callerAttributerFunc) CallerAttributes(ctx context.Context, req mcp.Request) []attribute.KeyValue {
	return f(ctx, req)
}

// noCallerAttributes is the fallback when no attributer was configured, and it
// is the same answer the default policy gives: nothing.
type noCallerAttributes struct{}

// CallerAttributes records nothing.
func (noCallerAttributes) CallerAttributes(context.Context, mcp.Request) []attribute.KeyValue {
	return nil
}
