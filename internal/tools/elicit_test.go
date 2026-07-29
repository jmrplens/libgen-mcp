package tools

import (
	"context"
	"encoding/json"
	"errors"
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
	Decision  int    `json:"decision"`
	Remember  bool   `json:"remember"`
}

// newElicitSession wires an in-memory MCP server exposing a single "probe" tool
// that calls the elicit* helpers, connected to a client whose ElicitationHandler
// is the supplied function. A nil handler means the client advertises no
// elicitation capability, letting tests exercise the fallback path. It returns a
// live client session ready for CallTool.
func newElicitSession(t *testing.T, handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientSession {
	t.Helper()
	return elicitSession(t, handler, false)
}

// newRawElicitSession is newElicitSession with the client's automatic
// round-trip middleware switched off, so a test can observe the input-required
// result itself and drive the retry by hand.
func newRawElicitSession(t *testing.T, handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientSession {
	t.Helper()
	return elicitSession(t, handler, true)
}

// elicitSession wires the in-memory server and client behind both constructors.
func elicitSession(t *testing.T, handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error), rawRoundTrips bool) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "probe", Description: "exercises the elicit helpers for tests"},
		func(_ context.Context, req *mcp.CallToolRequest, in elicitProbeInput) (*mcp.CallToolResult, elicitProbeOutput, error) {
			out := elicitProbeOutput{Supported: elicitationSupported(req)}
			round := newInputRound(req)
			switch in.Kind {
			case "text":
				out.Value, out.OK = round.askText("name", "your name?", "name", "the user's name")
			case "confirm":
				out.Confirmed, out.OK = round.askConfirm("proceed", "proceed?", "proceed", "confirm the action")
			case "choice":
				out.Value, out.OK = round.askChoice("edition", "pick one", "edition", "the chosen edition", in.Options)
			case "confirmdecision":
				out.Decision = int(round.askConfirmDecision("confirm", "proceed?", "confirm", "confirm the action"))
			case "textandconfirm":
				out.Value, out.OK = round.askText("name", "your name?", "name", "the user's name")
				out.Confirmed, _ = round.askConfirm("proceed", "proceed?", "proceed", "confirm the action")
			case "confirmremember":
				d, r := round.askConfirmRemember("confirm", "proceed?", "confirm", "confirm the action", "dont_ask_again")
				out.Decision, out.Remember = int(d), r
			}
			if pending := round.needsInput(); pending != nil {
				return pending, elicitProbeOutput{}, nil
			}
			return nil, out, nil
		})

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	opts := &mcp.ClientOptions{ElicitationHandler: handler}
	if rawRoundTrips {
		opts.MultiRoundTrip = &mcp.MultiRoundTripOptions{Disabled: true}
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, opts)
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
	return decodeProbe(t, res)
}

// decodeProbe decodes the probe tool's structured output from a tool result.
func decodeProbe(t *testing.T, res *mcp.CallToolResult) elicitProbeOutput {
	t.Helper()
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

// TestInputRound_AsksThroughTheResult verifies HOW a question now reaches the
// client. Protocol version 2026-07-28 forbids a server from opening an
// elicitation while it is serving a request; the question travels back on the
// tool result instead, and the client fulfills it and calls again (SEP-2322).
// The client here has the automatic round-trip middleware switched off, so the
// input-required result is observable rather than being answered behind the
// scenes.
func TestInputRound_AsksThroughTheResult(t *testing.T) {
	session := newRawElicitSession(t, acceptHandler(map[string]any{"name": "Ada"}))
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "probe", Arguments: map[string]any{"kind": "text"},
	})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if !res.NeedsInput() {
		t.Fatalf("result should ask for input, got %+v", res)
	}
	params, ok := res.InputRequests["name"].(*mcp.ElicitParams)
	if !ok {
		t.Fatalf("input request \"name\" should be an elicitation, got %T", res.InputRequests["name"])
	}
	if params.Message == "" {
		t.Error("the elicitation carries no message for the user")
	}
	if res.StructuredContent != nil || len(res.Content) > 0 {
		t.Error("an input-required result must carry no content: the tool has not run yet")
	}

	// Answering it and calling again completes the tool call.
	done, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "probe",
		Arguments: map[string]any{"kind": "text"},
		InputResponses: mcp.InputResponseMap{
			"name": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"name": "Ada"}},
		},
		RequestState: res.RequestState,
	})
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if done.NeedsInput() {
		t.Fatal("the answered call asked again instead of completing")
	}
	out := decodeProbe(t, done)
	if !out.OK || out.Value != "Ada" {
		t.Fatalf("want (\"Ada\", true); got (%q, %v)", out.Value, out.OK)
	}
}

// TestInputRound_UnansweredOnRetryFallsBack verifies the loop guard: a retry that
// comes back without an answer for a question is treated as unanswered, not as a
// reason to ask again. A client that drops one request would otherwise bounce the
// call between server and client until the SDK's retry cap stopped it.
func TestInputRound_UnansweredOnRetryFallsBack(t *testing.T) {
	session := newRawElicitSession(t, acceptHandler(map[string]any{"name": "Ada"}))
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "probe",
		Arguments: map[string]any{"kind": "text"},
		InputResponses: mcp.InputResponseMap{
			"unrelated": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"x": "y"}},
		},
	})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if res.NeedsInput() {
		t.Fatal("a retry missing its answer must not ask again")
	}
	out := decodeProbe(t, res)
	if out.OK || out.Value != "" {
		t.Fatalf("an unanswered question should fall back to (\"\", false); got (%q, %v)", out.Value, out.OK)
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

// TestElicitationSupported_NilCases exercises the guard arm of elicitationSupported
// directly (no session round-trip): both a nil request and a request with a nil
// Session must report the capability as absent, so callers take the fallback path.
func TestElicitationSupported_NilCases(t *testing.T) {
	if elicitationSupported(nil) {
		t.Error("elicitationSupported(nil) should be false")
	}
	if elicitationSupported(&mcp.CallToolRequest{}) {
		t.Error("elicitationSupported with a nil Session should be false")
	}
}

// TestAskConfirmDecision_UnavailableNilReq exercises the no-capability arm
// directly: with a nil request there is no session to ask, so the decision is
// confirmUnavailable, nothing is recorded as a question, and the caller falls
// back to its default behavior.
func TestAskConfirmDecision_UnavailableNilReq(t *testing.T) {
	round := newInputRound(nil)
	if got := round.askConfirmDecision("confirm", "proceed?", "confirm", "d"); got != confirmUnavailable {
		t.Errorf("nil-request decision = %v, want confirmUnavailable", got)
	}
	if pending := round.needsInput(); pending != nil {
		t.Errorf("a client that cannot be asked must produce no input request, got %+v", pending.InputRequests)
	}
}

// TestElicitConfirmDecision_Declined verifies that an explicit client decline is
// honored as a user "no": elicitConfirmDecision returns confirmDeclined so the
// caller must not proceed with the side-effecting action.
func TestElicitConfirmDecision_Declined(t *testing.T) {
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	session := newElicitSession(t, handler)
	out := callProbe(t, session, elicitProbeInput{Kind: "confirmdecision"})
	if out.Decision != int(confirmDeclined) {
		t.Errorf("declined decision = %d, want %d (confirmDeclined)", out.Decision, int(confirmDeclined))
	}
}

// TestAskConfirm_ClientHandlerErrorFailsTheCall pins a change the 2026-07-28
// protocol brings with it. The round trip that answers a question is now the
// client's to make, so a client whose elicitation handler fails takes the whole
// tool call down with it; the server never sees the failure and cannot fall back
// the way it did when it made the call itself. Worth pinning because it is the
// one behavior the migration could not preserve.
func TestAskConfirm_ClientHandlerErrorFailsTheCall(t *testing.T) {
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return nil, errors.New("boom")
	}
	session := newElicitSession(t, handler)
	_, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "probe", Arguments: map[string]any{"kind": "confirmdecision"},
	})
	if err == nil {
		t.Fatal("a failing client elicitation handler should fail the tool call")
	}
}

// TestAskConfirm_UnansweredIsUnavailable verifies the server-side half of the
// same story: when a question comes back unanswered, the decision is
// confirmUnavailable — never confirmDeclined, which would wrongly abort a
// side-effecting action nobody refused.
func TestAskConfirm_UnansweredIsUnavailable(t *testing.T) {
	session := newRawElicitSession(t, acceptHandler(map[string]any{"confirm": true}))
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "probe",
		Arguments: map[string]any{"kind": "confirmdecision"},
		InputResponses: mcp.InputResponseMap{
			"unrelated": &mcp.ElicitResult{Action: "accept"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if got := decodeProbe(t, res).Decision; got != int(confirmUnavailable) {
		t.Errorf("unanswered decision = %d, want %d (confirmUnavailable)", got, int(confirmUnavailable))
	}
}

// TestElicitConfirmDecision_UnexpectedAction verifies elicitConfirmDecision maps
// an action that is neither accept, decline nor cancel to confirmUnavailable, so
// the caller falls back to its default rather than treating it as a decision.
func TestElicitConfirmDecision_UnexpectedAction(t *testing.T) {
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "deferred"}, nil
	}
	session := newElicitSession(t, handler)
	out := callProbe(t, session, elicitProbeInput{Kind: "confirmdecision"})
	if out.Decision != int(confirmUnavailable) {
		t.Fatalf("an unexpected action should map to confirmUnavailable (%d); got %d", confirmUnavailable, out.Decision)
	}
}

// TestElicitConfirmRemember_AcceptWithOptOut verifies that ticking the opt-out
// box alongside the confirmation reports remember=true, which is what turns the
// prompt off for the rest of the session.
func TestElicitConfirmRemember_AcceptWithOptOut(t *testing.T) {
	session := newElicitSession(t, acceptHandler(map[string]any{
		"confirm": true, "dont_ask_again": true,
	}))
	out := callProbe(t, session, elicitProbeInput{Kind: "confirmremember"})
	if confirmDecision(out.Decision) != confirmProceed {
		t.Fatalf("decision = %v, want confirmProceed", confirmDecision(out.Decision))
	}
	if !out.Remember {
		t.Fatal("remember should be true when the opt-out box is ticked")
	}
}

// TestElicitConfirmRemember_AcceptWithoutOptOut is the ordinary case: the user
// approves this one download and is asked again next time.
func TestElicitConfirmRemember_AcceptWithoutOptOut(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content map[string]any
	}{
		{"box left unticked", map[string]any{"confirm": true, "dont_ask_again": false}},
		{"box absent entirely", map[string]any{"confirm": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := newElicitSession(t, acceptHandler(tc.content))
			out := callProbe(t, session, elicitProbeInput{Kind: "confirmremember"})
			if confirmDecision(out.Decision) != confirmProceed {
				t.Fatalf("decision = %v, want confirmProceed", confirmDecision(out.Decision))
			}
			if out.Remember {
				t.Fatal("remember should be false when the opt-out box is not ticked")
			}
		})
	}
}

// TestElicitConfirmRemember_DeclineNeverRemembers guards the combination that
// would be worst to get wrong: saying no to a download must not also silence the
// prompt that made saying no possible.
func TestElicitConfirmRemember_DeclineNeverRemembers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		result  *mcp.ElicitResult
		wantDec confirmDecision
	}{
		{"explicit decline", &mcp.ElicitResult{Action: "decline"}, confirmDeclined},
		{"cancel", &mcp.ElicitResult{Action: "cancel"}, confirmDeclined},
		{
			"accepted the form but unticked confirm, while asking not to be asked again",
			&mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": false, "dont_ask_again": true}},
			confirmDeclined,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := newElicitSession(t, func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				return tc.result, nil
			})
			out := callProbe(t, session, elicitProbeInput{Kind: "confirmremember"})
			if confirmDecision(out.Decision) != tc.wantDec {
				t.Fatalf("decision = %v, want %v", confirmDecision(out.Decision), tc.wantDec)
			}
			if out.Remember {
				t.Fatal("a declined download must never set remember")
			}
		})
	}
}

// TestElicitConfirmRemember_OptOutIsOptional checks the schema the client is
// shown: the confirmation is required, the opt-out is not. A required opt-out
// would make the user answer a question they did not ask for.
func TestElicitConfirmRemember_OptOutIsOptional(t *testing.T) {
	var got *mcp.ElicitParams
	session := newElicitSession(t, func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		got = req.Params
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
	})
	callProbe(t, session, elicitProbeInput{Kind: "confirmremember"})
	if got == nil {
		t.Fatal("the elicitation never reached the client")
	}
	schema, ok := got.RequestedSchema.(map[string]any)
	if !ok {
		t.Fatalf("RequestedSchema is %T, want map[string]any", got.RequestedSchema)
	}
	props, _ := schema["properties"].(map[string]any)
	if _, present := props["dont_ask_again"]; !present {
		t.Fatal("the opt-out field is missing from the form")
	}
	// Assert the positive case too: without it, a failed type assertion would
	// leave required empty and the loop below would pass by doing nothing.
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("required is %T, want []any", schema["required"])
	}
	if len(required) != 1 || required[0] != "confirm" {
		t.Fatalf("required = %v, want exactly [confirm]", required)
	}
	for _, r := range required {
		if r == "dont_ask_again" {
			t.Fatal("the opt-out must be optional, not required")
		}
	}
}

// TestAnswerGuards covers what the server does with an answer that does not fit
// the question it asked. An SDK client validates the content against the schema
// we sent and never forwards these, but nothing obliges a client to, so the
// guards are checked here directly rather than through a round-trip the SDK
// would refuse to make.
func TestAnswerGuards(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		cases := []struct {
			name string
			res  *mcp.ElicitResult
			want string
			ok   bool
		}{
			{"accepted", &mcp.ElicitResult{Action: "accept", Content: map[string]any{"name": "Ada"}}, "Ada", true},
			{"declined", &mcp.ElicitResult{Action: "decline"}, "", false},
			{"no result", nil, "", false},
			{"no content", &mcp.ElicitResult{Action: "accept"}, "", false},
			{"field missing", &mcp.ElicitResult{Action: "accept", Content: map[string]any{"other": "x"}}, "", false},
			{"empty value", &mcp.ElicitResult{Action: "accept", Content: map[string]any{"name": ""}}, "", false},
			{"wrong type", &mcp.ElicitResult{Action: "accept", Content: map[string]any{"name": 42}}, "", false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, ok := textAnswer(tc.res, "name")
				if got != tc.want || ok != tc.ok {
					t.Errorf("textAnswer = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
				}
			})
		}
	})

	t.Run("choice", func(t *testing.T) {
		options := []string{"first", "second"}
		cases := []struct {
			name string
			res  *mcp.ElicitResult
			want string
			ok   bool
		}{
			{"an offered option", &mcp.ElicitResult{Action: "accept", Content: map[string]any{"edition": "second"}}, "second", true},
			{"outside the options", &mcp.ElicitResult{Action: "accept", Content: map[string]any{"edition": "third"}}, "", false},
			{"wrong type", &mcp.ElicitResult{Action: "accept", Content: map[string]any{"edition": 42}}, "", false},
			{"declined", &mcp.ElicitResult{Action: "decline"}, "", false},
			{"no result", nil, "", false},
			{"no content", &mcp.ElicitResult{Action: "accept"}, "", false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, ok := choiceAnswer(tc.res, "edition", options)
				if got != tc.want || ok != tc.ok {
					t.Errorf("choiceAnswer = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
				}
			})
		}
	})

	t.Run("confirm", func(t *testing.T) {
		cases := []struct {
			name     string
			res      *mcp.ElicitResult
			decision confirmDecision
			remember bool
		}{
			{"confirmed", &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, confirmProceed, false},
			{"confirmed and remembered", &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true, "dont_ask_again": true}}, confirmProceed, true},
			{"accepted with false", &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": false}}, confirmDeclined, false},
			{"declined", &mcp.ElicitResult{Action: "decline"}, confirmDeclined, false},
			{"canceled", &mcp.ElicitResult{Action: "cancel"}, confirmDeclined, false},
			{"field missing", &mcp.ElicitResult{Action: "accept", Content: map[string]any{}}, confirmDeclined, false},
			{"wrong type", &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": "yes"}}, confirmDeclined, false},
			{"unknown action", &mcp.ElicitResult{Action: "sideways"}, confirmUnavailable, false},
			{"no result", nil, confirmUnavailable, false},
			// A declined download must not also silence the prompt that let the user decline.
			{"declined but remembered", &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": false, "dont_ask_again": true}}, confirmDeclined, false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				decision, remember := confirmAnswer(tc.res, "confirm", "dont_ask_again")
				if decision != tc.decision || remember != tc.remember {
					t.Errorf("confirmAnswer = (%v, %v), want (%v, %v)", decision, remember, tc.decision, tc.remember)
				}
			})
		}
	})
}

// TestInputRound_AsksEverythingInOneExchange verifies that a handler needing two
// answers asks for both at once. download relies on it: a call that needs a
// credential AND a confirmation costs the user one exchange, not one per
// question, which is what the old ask-mid-call shape gave.
func TestInputRound_AsksEverythingInOneExchange(t *testing.T) {
	session := newRawElicitSession(t, acceptHandler(nil))
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "probe", Arguments: map[string]any{"kind": "textandconfirm"},
	})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if !res.NeedsInput() {
		t.Fatalf("result should ask for input, got %+v", res)
	}
	if len(res.InputRequests) != 2 {
		t.Fatalf("want both questions in one result, got %d: %+v", len(res.InputRequests), res.InputRequests)
	}
	for _, id := range []string{"name", "proceed"} {
		if _, ok := res.InputRequests[id]; !ok {
			t.Errorf("question %q missing from the input requests", id)
		}
	}

	done, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "probe",
		Arguments: map[string]any{"kind": "textandconfirm"},
		InputResponses: mcp.InputResponseMap{
			"name":    &mcp.ElicitResult{Action: "accept", Content: map[string]any{"name": "Ada"}},
			"proceed": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"proceed": true}},
		},
		RequestState: res.RequestState,
	})
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	out := decodeProbe(t, done)
	if out.Value != "Ada" || !out.Confirmed {
		t.Errorf("both answers should reach the handler; got value=%q confirmed=%v", out.Value, out.Confirmed)
	}
}
