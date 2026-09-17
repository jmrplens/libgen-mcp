// recover.go keeps one request's bug from taking the process with it.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// recoverPanics turns a panic in a request handler into a JSON-RPC internal
// error instead of letting it take the process down.
//
// The SDK dispatches each request on its own goroutine and does not recover, so
// a nil dereference anywhere below this point is fatal to the whole binary. That
// is survivable for a stdio server owned by one person; it is not for a hosted
// endpoint, where every caller sharing the process loses their session because
// one request found a bug.
//
// internal/tools already wraps the four tool handlers in withRecovery, and this
// does not replace it: that layer meters every call and answers with an IsError
// tool result, which is the shape a model can read and act on. What it does not
// cover is everything else the SDK dispatches — the four prompt handlers, the
// completion path, the server card — and the elicitation round `download` runs
// to ask before saving, which is precisely the shape that took the sibling
// project's process down on an ordinary client call.
//
// This is a backstop, not a fix. A panic that lands here is a defect: it is
// logged at error level with its stack so it can be found and repaired, and
// recovering only decides that the other callers keep working in the meantime.
//
// The stack goes to the log and never into the response, which carries the
// generic internal-error text. A panic message can name a path, a mirror URL or
// a query fragment, and after the redaction work in internal/netguard that rule
// has a second reason: a per-call secret rides in some of those query strings.
//
// Go cannot recover a runtime throw — out of memory, a concurrent map write, a
// deadlock detected by the runtime — so this narrows the fatal set rather than
// emptying it.
func recoverPanics(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (result mcp.Result, err error) {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			slog.Error("recovered a panic while handling a request",
				"method", method,
				"panic", fmt.Sprint(r),
				"stack", string(debug.Stack()),
			)
			result = nil
			err = &jsonrpc.Error{
				Code:    jsonrpc.CodeInternalError,
				Message: "internal error",
			}
		}()
		return next(ctx, method, req)
	}
}
