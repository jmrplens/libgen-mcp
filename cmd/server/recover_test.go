// recover_test.go covers the panic backstop twice: the middleware on its own,
// and the whole chain as a client meets it.
//
// The second half is the one that matters. What this change claims is that a
// defect in one request does not end everyone else's session, and a middleware
// that recovers correctly but was wired in the wrong order proves nothing about
// that — so the session cases drive a real mcp.Server over a real transport.

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/mcpotel"
)

// panicSecret is the sort of thing a panic message carries and a caller must
// never receive: it stands for the path, mirror URL or query fragment a real
// panic here would name, and a query fragment on some of this server's outbound
// URLs is a credential.
const panicSecret = "k3y-in-the-panic-message"

// TestRecoverPanicsAnswersInsteadOfDying covers the middleware on its own.
func TestRecoverPanicsAnswersInsteadOfDying(t *testing.T) {
	tests := []struct {
		name    string
		handler mcp.MethodHandler
		wantErr bool
	}{
		{
			name: "a panicking handler becomes an internal error",
			handler: func(context.Context, string, mcp.Request) (mcp.Result, error) {
				panic("boom " + panicSecret)
			},
			wantErr: true,
		},
		{
			// The shape that took the sibling project's process down: a nil
			// capabilities pointer read as though it were present. It comes from
			// a function so the nilness analyzer cannot fold it away, which is
			// also how it reached production — through an accessor returning nil.
			name: "a nil dereference is recovered like any other panic",
			handler: func(context.Context, string, mcp.Request) (mcp.Result, error) {
				_ = noClientCapabilities().Elicitation
				return &mcp.CallToolResult{}, nil
			},
			wantErr: true,
		},
		{
			name: "a handler that returns normally is untouched",
			handler: func(context.Context, string, mcp.Request) (mcp.Result, error) {
				return &mcp.CallToolResult{}, nil
			},
		},
		{
			// A handler's own error is a considered answer, not a defect, and
			// must arrive as it was written rather than flattened to "internal
			// error" like a panic.
			name: "a handler's own error is passed through unchanged",
			handler: func(context.Context, string, mcp.Request) (mcp.Result, error) {
				return nil, errors.New("the handler's own failure")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := recoverPanics(tt.handler)(t.Context(), "tools/call", nil)

			if !tt.wantErr {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				if result == nil {
					t.Error("the result was dropped for a handler that returned one")
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), panicSecret) {
				t.Errorf("the panic message reached the caller: %v", err)
			}
		})
	}

	t.Run("the recovered error carries the internal-error code", func(t *testing.T) {
		_, err := recoverPanics(func(context.Context, string, mcp.Request) (mcp.Result, error) {
			panic("boom")
		})(t.Context(), "tools/call", nil)

		var rpcErr *jsonrpc.Error
		if !errors.As(err, &rpcErr) {
			t.Fatalf("error does not carry a JSON-RPC code: %v", err)
		}
		if rpcErr.Code != jsonrpc.CodeInternalError {
			t.Errorf("code = %d, want %d (internal error)", rpcErr.Code, jsonrpc.CodeInternalError)
		}
	})

	t.Run("a handler's own error keeps its text", func(t *testing.T) {
		_, err := recoverPanics(func(context.Context, string, mcp.Request) (mcp.Result, error) {
			return nil, errors.New("the handler's own failure")
		})(t.Context(), "tools/call", nil)

		if err == nil || !strings.Contains(err.Error(), "the handler's own failure") {
			t.Errorf("err = %v, want the handler's own message; only a PANIC is flattened", err)
		}
	})
}

// noClientCapabilities returns the nil the SDK returns for a client that
// declared no capabilities, opaquely enough that static analysis does not fold
// the dereference above into a compile-time error.
func noClientCapabilities() *mcp.ClientCapabilities { return nil }

// TestAPanickingPromptDoesNotEndTheSession is the claim this change exists for,
// asserted through the middleware chain newMCPServer actually builds.
//
// A prompt rather than a tool, because a prompt is what internal/tools'
// withRecovery does NOT cover: the four tool handlers have their own recovery
// and their own result shape, and everything else the SDK dispatches had none.
//
// Three things in one case, and all three are needed. The caller gets a
// well-formed error rather than a dropped connection; the panic's text is not in
// it; and — the point — the NEXT request on the same session is served, which is
// the difference between a request that failed and a process that died.
func TestAPanickingPromptDoesNotEndTheSession(t *testing.T) {
	session := connectToServerWithPanickingPrompt(t)

	_, err := session.GetPrompt(t.Context(), &mcp.GetPromptParams{Name: "boom"})
	if err == nil {
		t.Fatal("the panicking prompt returned no error")
	}
	if strings.Contains(err.Error(), panicSecret) {
		t.Errorf("the panic message reached the caller: %v", err)
	}
	if strings.Contains(err.Error(), "recover.go") || strings.Contains(err.Error(), "goroutine ") {
		t.Errorf("a stack trace reached the caller: %v", err)
	}

	// The session survived. Without the middleware the process is gone by now
	// and this call cannot be made at all.
	for _, listErr := range session.Tools(t.Context(), nil) {
		if listErr != nil {
			t.Fatalf("the session did not survive the panic: %v", listErr)
		}
		break
	}
}

// connectToServerWithPanickingPrompt builds a server through newMCPServer — so
// the real middleware chain is in place — registers one prompt that panics, and
// returns a connected client session.
func connectToServerWithPanickingPrompt(t *testing.T) *mcp.ClientSession {
	t.Helper()

	server := newMCPServer(serverInstructions(true, false), nil, heavyCeiling{}, mcpotel.Options{})
	// One tool, so tools/list below has something to answer with and the
	// survival check is about the session rather than an empty catalog.
	mcp.AddTool(server, &mcp.Tool{Name: "ping", Description: "returns nothing in particular"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
			return &mcp.CallToolResult{}, struct{}{}, nil
		})
	server.AddPrompt(&mcp.Prompt{Name: "boom", Description: "panics on purpose"},
		func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			panic("boom " + panicSecret)
		})

	st, ct := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "panic-probe", Version: "0"}, nil)
	session, err := client.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// TestAPanickingToolKeepsWithRecoverysResult pins the nesting, which is the part
// a reorder would break silently.
//
// internal/tools' withRecovery is the inner layer and has to win: it meters the
// call and answers with an IsError tool RESULT, which a model can read and act
// on, where this middleware answers a protocol-level error a model only sees as
// a failed call. Both layers recovering the same panic would be harmless; the
// wrong one winning would quietly downgrade every tool panic's answer.
func TestAPanickingToolKeepsWithRecoverysResult(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load(): %v", err)
	}
	server, err := newRegisteredServer(cfg, "", nil, inflightFlag{}, identityChoice{})
	if err != nil {
		t.Fatalf("newRegisteredServer(): %v", err)
	}

	st, ct := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "tool-panic-probe", Version: "0"}, nil)
	session, err := client.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	// A read with no identifier at all: the handler refuses it rather than
	// panicking, which is the point — this asserts that an ordinary tool failure
	// still comes back as a tool RESULT and not as a protocol error, so the
	// middleware is not swallowing the layer beneath it.
	got, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "read", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("the tool call came back as a protocol error, not a tool result: %v", err)
	}
	if !got.IsError {
		t.Error("a refused tool call did not come back as an IsError result")
	}
}

// TestAddingLastPutsTheBackstopOutermost pins the SDK semantic the wiring rests
// on, which is both counterintuitive and not visible from the chain itself.
//
// AddReceivingMiddleware wraps the handler built so far, so the LAST call is the
// OUTERMOST layer — which is what makes recoverPanics, added after cachehints and
// capguard, cover a panic in either of them as well as one in a handler. Nothing
// in this repository would notice if a future SDK reversed that, or if somebody
// moved the line up: the middleware would still recover handler panics and would
// silently stop covering the two beside it.
//
// So this asserts the ordering directly: a middleware added BEFORE the backstop
// panics, and the backstop has to catch it.
func TestAddingLastPutsTheBackstopOutermost(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "order-probe", Version: "0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "ping", Description: "returns nothing in particular"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
			return &mcp.CallToolResult{}, struct{}{}, nil
		})

	// Stands in for cachehints and capguard: added first, so it must end up
	// INSIDE the backstop added second.
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				panic("boom " + panicSecret)
			}
			return next(ctx, method, req)
		}
	})
	server.AddReceivingMiddleware(recoverPanics)

	st, ct := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "order-probe", Version: "0"}, nil)
	session, err := client.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	_, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "ping"})
	if err == nil {
		t.Fatal("the panicking middleware returned no error")
	}
	if strings.Contains(err.Error(), panicSecret) {
		t.Errorf("the panic message reached the caller: %v", err)
	}
	// The session survived, which it cannot have done if the panic escaped.
	for _, listErr := range session.Tools(t.Context(), nil) {
		if listErr != nil {
			t.Fatalf("the session did not survive a panic in a middleware beside the backstop: %v", listErr)
		}
		break
	}
}
