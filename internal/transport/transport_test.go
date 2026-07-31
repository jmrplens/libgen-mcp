package transport

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestStreamableHTTPMapsFlags verifies the flag-to-SDK-options mapping:
// stateless defaults on (protocol 2026-07-28 requires it over HTTP), JSON
// response and the body cap follow their flags, and client aborts always
// propagate into handler contexts so in-flight mirror fetches are canceled.
func TestStreamableHTTPMapsFlags(t *testing.T) {
	tests := []struct {
		name string
		in   Options
		want mcp.StreamableHTTPOptions
	}{
		{"defaults", DefaultOptions(), mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true}},
		{"stateful_opt_out", Options{Stateless: false}, mcp.StreamableHTTPOptions{Stateless: false, PropagateRequestCancellation: true}},
		{"json_response", Options{Stateless: true, JSONResponse: true}, mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, PropagateRequestCancellation: true}},
		{"body_limit", Options{Stateless: true, MaxRequestBodyBytes: 1024}, mcp.StreamableHTTPOptions{Stateless: true, MaxRequestBodyBytes: 1024, PropagateRequestCancellation: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StreamableHTTP(tt.in)
			if *got != tt.want {
				t.Errorf("StreamableHTTP(%+v) = %+v, want %+v", tt.in, *got, tt.want)
			}
		})
	}
}
