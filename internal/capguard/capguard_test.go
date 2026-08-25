package capguard

import (
	"context"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// handlerRecording builds a stub method handler that records whether it ran and
// answers with res, standing in for the SDK's base handler.
func handlerRecording(ran *bool, res mcp.Result) mcp.MethodHandler {
	return func(context.Context, string, mcp.Request) (mcp.Result, error) {
		*ran = true
		return res, nil
	}
}

// TestNoResourcesRejectsResourceMethods asserts every resource method is
// answered with -32601 and never reaches the SDK handler beneath.
func TestNoResourcesRejectsResourceMethods(t *testing.T) {
	for _, method := range []string{"resources/list", "resources/templates/list", "resources/read"} {
		t.Run(method, func(t *testing.T) {
			ran := false
			got, err := NoResources()(handlerRecording(&ran, &mcp.ListResourcesResult{}))(context.Background(), method, nil)
			if ran {
				t.Error("the SDK handler ran; the guard must short-circuit it")
			}
			if got != nil {
				t.Errorf("result = %#v, want nil alongside the error", got)
			}
			var wire *jsonrpc.Error
			if !errors.As(err, &wire) {
				t.Fatalf("err = %v (%T), want a *jsonrpc.Error", err, err)
			}
			if wire.Code != jsonrpc.CodeMethodNotFound {
				t.Errorf("code = %d, want %d", wire.Code, jsonrpc.CodeMethodNotFound)
			}
		})
	}
}

// TestNoResourcesPassesThroughOtherMethods asserts the guard is narrow: the
// methods this server does implement are handed to the SDK unchanged.
func TestNoResourcesPassesThroughOtherMethods(t *testing.T) {
	for _, method := range []string{"tools/list", "tools/call", "prompts/list", "prompts/get", "server/discover"} {
		t.Run(method, func(t *testing.T) {
			ran := false
			want := &mcp.CallToolResult{}
			got, err := NoResources()(handlerRecording(&ran, want))(context.Background(), method, nil)
			if err != nil {
				t.Fatalf("middleware returned error %v, want nil", err)
			}
			if !ran {
				t.Error("the SDK handler did not run")
			}
			if got != mcp.Result(want) {
				t.Errorf("result = %#v, want the handler's own result %#v", got, want)
			}
		})
	}
}
