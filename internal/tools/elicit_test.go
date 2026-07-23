package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// elicitProbeInput selects which elicit helper the probe tool exercises and,
// for the choice helper, the options it offers.
type elicitProbeInput struct {
	Kind    string   `json:"kind" jsonschema:"which helper to call: text, confirm or choice,required"`
	Options []string `json:"options,omitempty" jsonschema:"options offered to elicitChoice"`
}

// elicitProbeOutput reports back what the elicit helper returned so the test can
// assert on the (value, ok/confirmed) pair through a real MCP round-trip.
type elicitProbeOutput struct {
	Value     string `json:"value"`
	OK        bool   `json:"ok"`
	Confirmed bool   `json:"confirmed"`
	Supported bool   `json:"supported"`
}

// newElicitSession wires an in-memory MCP server exposing a single "probe" tool
// that calls the elicit* helpers, connected to a client whose ElicitationHandler
// is the supplied function. A nil handler means the client advertises no
// elicitation capability, letting tests exercise the fallback path. It returns a
// live client session ready for CallTool.
func newElicitSession(t *testing.T, handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "probe", Description: "exercises the elicit helpers for tests"},
		func(ctx context.Context, req *mcp.CallToolRequest, in elicitProbeInput) (*mcp.CallToolResult, elicitProbeOutput, error) {
			out := elicitProbeOutput{Supported: elicitationSupported(req)}
			switch in.Kind {
			case "text":
				out.Value, out.OK = elicitText(ctx, req, "your name?", "name", "the user's name")
			case "confirm":
				out.Confirmed, out.OK = elicitConfirm(ctx, req, "proceed?", "proceed", "confirm the action")
			case "choice":
				out.Value, out.OK = elicitChoice(ctx, req, "pick one", "edition", "the chosen edition", in.Options)
			}
			return nil, out, nil
		})

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"},
		&mcp.ClientOptions{ElicitationHandler: handler})
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// callProbe drives the probe tool once and decodes its structured output.
func callProbe(t *testing.T, session *mcp.ClientSession, in elicitProbeInput) elicitProbeOutput {
	t.Helper()
	args, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshaling probe input: %v", err)
	}
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "probe", Arguments: json.RawMessage(args),
	})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if res.IsError {
		t.Fatalf("probe returned an error result: %+v", res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshaling probe output: %v", err)
	}
	var out elicitProbeOutput
	if uerr := json.Unmarshal(raw, &out); uerr != nil {
		t.Fatalf("decoding probe output: %v", uerr)
	}
	return out
}

// acceptHandler returns an ElicitationHandler that always accepts with the given
// content map, so tests can simulate a user filling the form.
func acceptHandler(content map[string]any) func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	return func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: content}, nil
	}
}

// TestElicit_NotSupported verifies that when the client advertises no
// elicitation capability, elicitationSupported is false and every helper returns
// ok=false immediately (fallback path) without hanging on a round-trip.
func TestElicit_NotSupported(t *testing.T) {
	session := newElicitSession(t, nil)
	out := callProbe(t, session, elicitProbeInput{Kind: "text"})
	if out.Supported {
		t.Fatal("elicitationSupported should be false without an ElicitationHandler")
	}
	if out.OK || out.Value != "" {
		t.Fatalf("elicitText should fall back to (\"\", false); got (%q, %v)", out.Value, out.OK)
	}
	confirm := callProbe(t, session, elicitProbeInput{Kind: "confirm"})
	if confirm.OK || confirm.Confirmed {
		t.Fatalf("elicitConfirm should fall back to (false, false); got (%v, %v)", confirm.Confirmed, confirm.OK)
	}
	choice := callProbe(t, session, elicitProbeInput{Kind: "choice", Options: []string{"a", "b"}})
	if choice.OK || choice.Value != "" {
		t.Fatalf("elicitChoice should fall back to (\"\", false); got (%q, %v)", choice.Value, choice.OK)
	}
}

// TestElicitText_Accept verifies elicitText returns the submitted value with
// ok=true when the client accepts with a non-empty field.
func TestElicitText_Accept(t *testing.T) {
	session := newElicitSession(t, acceptHandler(map[string]any{"name": "Ada"}))
	out := callProbe(t, session, elicitProbeInput{Kind: "text"})
	if !out.Supported {
		t.Fatal("elicitationSupported should be true with an ElicitationHandler")
	}
	if !out.OK || out.Value != "Ada" {
		t.Fatalf("want (\"Ada\", true); got (%q, %v)", out.Value, out.OK)
	}
}

// TestElicitText_Decline verifies elicitText falls back to ("", false) when the
// user declines the elicitation.
func TestElicitText_Decline(t *testing.T) {
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	session := newElicitSession(t, handler)
	out := callProbe(t, session, elicitProbeInput{Kind: "text"})
	if out.OK || out.Value != "" {
		t.Fatalf("decline should yield (\"\", false); got (%q, %v)", out.Value, out.OK)
	}
}

// TestElicitText_AcceptEmpty verifies an accept with an empty string is treated
// as no answer so the caller falls back.
func TestElicitText_AcceptEmpty(t *testing.T) {
	session := newElicitSession(t, acceptHandler(map[string]any{"name": ""}))
	out := callProbe(t, session, elicitProbeInput{Kind: "text"})
	if out.OK || out.Value != "" {
		t.Fatalf("empty accept should yield (\"\", false); got (%q, %v)", out.Value, out.OK)
	}
}

// TestElicitConfirm_AcceptTrue verifies elicitConfirm reports (true, true) when
// the user accepts with the boolean field set to true.
func TestElicitConfirm_AcceptTrue(t *testing.T) {
	session := newElicitSession(t, acceptHandler(map[string]any{"proceed": true}))
	out := callProbe(t, session, elicitProbeInput{Kind: "confirm"})
	if !out.OK || !out.Confirmed {
		t.Fatalf("want (confirmed=true, ok=true); got (%v, %v)", out.Confirmed, out.OK)
	}
}

// TestElicitConfirm_AcceptFalse verifies elicitConfirm reports (false, true)
// when the user accepts but sets the boolean to false: elicitation ran (ok) yet
// the user did not confirm.
func TestElicitConfirm_AcceptFalse(t *testing.T) {
	session := newElicitSession(t, acceptHandler(map[string]any{"proceed": false}))
	out := callProbe(t, session, elicitProbeInput{Kind: "confirm"})
	if !out.OK || out.Confirmed {
		t.Fatalf("want (confirmed=false, ok=true); got (%v, %v)", out.Confirmed, out.OK)
	}
}

// TestElicitChoice_Accept verifies elicitChoice returns the chosen option with
// ok=true when the accepted value is one of the offered options.
func TestElicitChoice_Accept(t *testing.T) {
	session := newElicitSession(t, acceptHandler(map[string]any{"edition": "b"}))
	out := callProbe(t, session, elicitProbeInput{Kind: "choice", Options: []string{"a", "b", "c"}})
	if !out.OK || out.Value != "b" {
		t.Fatalf("want (\"b\", true); got (%q, %v)", out.Value, out.OK)
	}
}

// TestElicitChoice_NotAnOption verifies elicitChoice falls back to ("", false)
// when the accepted value is not among the offered options.
func TestElicitChoice_NotAnOption(t *testing.T) {
	session := newElicitSession(t, acceptHandler(map[string]any{"edition": "z"}))
	out := callProbe(t, session, elicitProbeInput{Kind: "choice", Options: []string{"a", "b", "c"}})
	if out.OK || out.Value != "" {
		t.Fatalf("out-of-set value should yield (\"\", false); got (%q, %v)", out.Value, out.OK)
	}
}
