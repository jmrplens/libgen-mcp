package transport

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestStreamableHTTPMapsFlags verifies the flag-to-SDK-options mapping:
// stateless defaults on (protocol 2026-07-28 requires it over HTTP), JSON
// response and the body cap follow their flags, client aborts always propagate
// into handler contexts so in-flight mirror fetches are canceled, and the SDK's
// own DNS-rebinding check is always off because this server answers it itself.
func TestStreamableHTTPMapsFlags(t *testing.T) {
	const always = true // DisableLocalhostProtection and PropagateRequestCancellation
	tests := []struct {
		name string
		in   Options
		want mcp.StreamableHTTPOptions
	}{
		{"defaults", DefaultOptions(), mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: always, DisableLocalhostProtection: always}},
		{"stateful_opt_out", Options{Stateless: false}, mcp.StreamableHTTPOptions{Stateless: false, PropagateRequestCancellation: always, DisableLocalhostProtection: always}},
		{"json_response", Options{Stateless: true, JSONResponse: true}, mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, PropagateRequestCancellation: always, DisableLocalhostProtection: always}},
		{"body_limit", Options{Stateless: true, MaxRequestBodyBytes: 1024}, mcp.StreamableHTTPOptions{Stateless: true, MaxRequestBodyBytes: 1024, PropagateRequestCancellation: always, DisableLocalhostProtection: always}},
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

// TestLocalhostProtectionIsOffOnEveryShape is the half of the pair that must not
// drift back, stated on its own rather than only inside the table above.
//
// The SDK refuses any non-loopback Host on a connection accepted on a loopback
// address, which is every reverse-proxy recipe this project documents, and it
// refuses it with plain text on a route a Streamable HTTP client reads as JSON.
// cmd/server's host guard answers the same question with the deployment's own
// flags in hand. Turning this back on would put the SDK's answer back in front
// of it, one layer further in, where no flag can reach it.
func TestLocalhostProtectionIsOffOnEveryShape(t *testing.T) {
	for name, opts := range map[string]Options{
		"the defaults":   DefaultOptions(),
		"the zero value": {},
		"every knob turned the other way": {
			Stateless: false, JSONResponse: true, MaxRequestBodyBytes: 1 << 20, ServesTLS: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if !StreamableHTTP(opts).DisableLocalhostProtection {
				t.Errorf("StreamableHTTP(%+v) left the SDK's localhost protection on", opts)
			}
		})
	}
}
