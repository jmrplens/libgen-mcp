package cachehints

import (
	"context"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// handlerReturning builds a stub method handler that answers with res and err,
// standing in for the SDK's base handler.
func handlerReturning(res mcp.Result, err error) mcp.MethodHandler {
	return func(context.Context, string, mcp.Request) (mcp.Result, error) { return res, err }
}

// TestMiddlewareStampsCatalogTTL asserts every cacheable catalog result comes
// back with the catalog TTL and keeps the SDK's public scope, which is correct
// here because this server has no auth: every client sees the same catalog.
func TestMiddlewareStampsCatalogTTL(t *testing.T) {
	cases := []struct {
		name   string
		method string
		result mcp.CacheableResult
	}{
		{"tools", "tools/list", &mcp.ListToolsResult{Cacheable: mcp.Cacheable{CacheScope: "public"}}},
		{"prompts", "prompts/list", &mcp.ListPromptsResult{Cacheable: mcp.Cacheable{CacheScope: "public"}}},
		{"discover", "server/discover", &mcp.DiscoverResult{Cacheable: mcp.Cacheable{CacheScope: "public"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Middleware()(handlerReturning(tc.result, nil))(context.Background(), tc.method, nil)
			if err != nil {
				t.Fatalf("middleware returned error %v, want nil", err)
			}
			cacheable, ok := got.(mcp.CacheableResult)
			if !ok {
				t.Fatalf("result %T does not carry cache hints", got)
			}
			if cacheable.GetTTLMs() != CatalogTTLMs {
				t.Errorf("ttlMs = %d, want %d", cacheable.GetTTLMs(), CatalogTTLMs)
			}
			if cacheable.GetCacheScope() != "public" {
				t.Errorf("cacheScope = %q, want %q", cacheable.GetCacheScope(), "public")
			}
		})
	}
}

// TestMiddlewarePassesThroughOtherResults asserts a non-catalog result is
// returned untouched: tool calls carry live data that must not be cached.
func TestMiddlewarePassesThroughOtherResults(t *testing.T) {
	want := &mcp.CallToolResult{}
	got, err := Middleware()(handlerReturning(want, nil))(context.Background(), "tools/call", nil)
	if err != nil {
		t.Fatalf("middleware returned error %v, want nil", err)
	}
	if got != mcp.Result(want) {
		t.Errorf("result = %#v, want the handler's own result %#v", got, want)
	}
}

// TestMiddlewarePassesThroughErrors asserts a failing handler is not second-guessed.
func TestMiddlewarePassesThroughErrors(t *testing.T) {
	want := errors.New("boom")
	got, err := Middleware()(handlerReturning(nil, want))(context.Background(), "tools/list", nil)
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
	if got != nil {
		t.Errorf("result = %#v, want nil alongside the error", got)
	}
}
