package tools

import (
	"context"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// elicitationSupported reports whether the connected client advertised the
// elicitation capability. When false, callers must fall back to their
// deterministic default behavior and MUST NOT call elicit*.
func elicitationSupported(req *mcp.CallToolRequest) bool {
	if req == nil || req.Session == nil {
		return false
	}
	ip := req.Session.InitializeParams()
	return ip != nil && ip.Capabilities != nil && ip.Capabilities.Elicitation != nil
}

// runFormElicit performs a single "form" elicitation with the given message and
// JSON schema, then returns the raw content value for fieldName. ok is true only
// when the capability is present, the round-trip succeeded, the user accepted,
// and Content carries fieldName. It never returns an error: elicitation is
// advisory and any failure collapses to ok=false so the caller falls back.
func runFormElicit(ctx context.Context, req *mcp.CallToolRequest, message string, schema any, fieldName string) (any, bool) {
	if !elicitationSupported(req) {
		return nil, false
	}
	res, err := req.Session.Elicit(ctx, &mcp.ElicitParams{
		Mode:            "form",
		Message:         message,
		RequestedSchema: schema,
	})
	if err != nil || res == nil || res.Action != "accept" || res.Content == nil {
		return nil, false
	}
	v, present := res.Content[fieldName]
	if !present {
		return nil, false
	}
	return v, true
}

// stringSchema builds a 2020-12 JSON-schema object with a single required
// top-level string property named fieldName.
func stringSchema(fieldName, fieldDescription string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			fieldName: map[string]any{
				"type":        "string",
				"description": fieldDescription,
			},
		},
		"required": []string{fieldName},
	}
}

// elicitText asks the user a single free-text question via form elicitation and
// returns the answer and ok=true only when the user accepted with a non-empty
// value. ok=false means: capability absent, user declined/canceled, an error,
// or an empty answer — in every such case the caller falls back. It NEVER returns
// an error to the caller; elicitation is advisory.
func elicitText(ctx context.Context, req *mcp.CallToolRequest, message, fieldName, fieldDescription string) (value string, ok bool) {
	raw, got := runFormElicit(ctx, req, message, stringSchema(fieldName, fieldDescription), fieldName)
	if !got {
		return "", false
	}
	s, isString := raw.(string)
	if !isString || s == "" {
		return "", false
	}
	return s, true
}

// elicitConfirm asks the user a yes/no confirmation via form elicitation (a
// single boolean field). It returns confirmed=true only when the user accepted
// AND the boolean is true. ok reports whether elicitation actually ran and
// yielded a usable boolean answer (capability present, the round-trip succeeded,
// the user accepted, and Content carried a boolean field). Callers that must
// default to "proceed" when elicitation is unavailable check ok (fall back to
// their default when ok is false); callers that must default to "do not proceed"
// can treat !confirmed as stop regardless of ok.
func elicitConfirm(ctx context.Context, req *mcp.CallToolRequest, message, fieldName, fieldDescription string) (confirmed, ok bool) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			fieldName: map[string]any{
				"type":        "boolean",
				"description": fieldDescription,
			},
		},
		"required": []string{fieldName},
	}
	raw, got := runFormElicit(ctx, req, message, schema, fieldName)
	if !got {
		return false, false
	}
	b, isBool := raw.(bool)
	if !isBool {
		return false, false
	}
	return b, true
}

// elicitChoice asks the user to pick one of options via form elicitation (an
// enum field) and returns the chosen value and ok=true only on accept with a
// value that is one of options. Any other outcome (capability absent, decline,
// cancel, error, or a value not in options) yields ("", false) so the caller
// falls back. Used for edition disambiguation.
func elicitChoice(ctx context.Context, req *mcp.CallToolRequest, message, fieldName, fieldDescription string, options []string) (choice string, ok bool) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			fieldName: map[string]any{
				"type":        "string",
				"description": fieldDescription,
				"enum":        options,
			},
		},
		"required": []string{fieldName},
	}
	raw, got := runFormElicit(ctx, req, message, schema, fieldName)
	if !got {
		return "", false
	}
	s, isString := raw.(string)
	if !isString || !slices.Contains(options, s) {
		return "", false
	}
	return s, true
}
