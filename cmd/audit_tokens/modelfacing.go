// modelfacing.go narrows what the token footprint counts to what a model
// actually receives.
//
// The measurement used to marshal the whole tool definition, which meant it
// also counted `icons` — base64-encoded SVG data URIs that exist for client
// user interfaces (a tool picker, an approval dialog). No MCP client puts an
// icon into a model's context, so counting them overstated the figure this
// command exists to report.

package main

import (
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// marshalModelFacing marshals a catalog entry with its presentation-only
// fields removed, so the result reflects context cost rather than wire cost.
//
// *mcp.Tool and *mcp.Prompt are measured today; this command has no
// resources to weigh, but the switch mirrors the sibling gitlab-mcp-server's
// audit_tokens, whose own catalog also carries Icons on Resource and
// ResourceTemplate — extend the switch here if this command grows to
// measure either.
//
// The entry is copied before being cleared: it comes from a live session, and
// clearing the original would corrupt any later measurement of the same list.
func marshalModelFacing(entry any) ([]byte, error) {
	switch typed := entry.(type) {
	case *mcp.Tool:
		clone := *typed
		clone.Icons = nil
		return json.Marshal(&clone)
	case *mcp.Prompt:
		clone := *typed
		clone.Icons = nil
		return json.Marshal(&clone)
	default:
		return json.Marshal(entry)
	}
}
