package tools

import (
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// elicitationSupported reports whether the connected client advertised the
// elicitation capability. When false, callers must fall back to their
// deterministic default behavior and MUST NOT ask anything.
func elicitationSupported(req *mcp.CallToolRequest) bool {
	if req == nil || req.Session == nil {
		return false
	}
	ip := req.Session.InitializeParams()
	return ip != nil && ip.Capabilities != nil && ip.Capabilities.Elicitation != nil
}

// inputRound collects the questions one tool call needs to put to the user, and
// reads back the answers when the call is a retry that carries them.
//
// Protocol version 2026-07-28 forbids a server from opening an elicitation while
// it is serving a request: a tool that needs input returns it on the result as an
// input request, the client fulfills it and calls the tool again with the answer
// (SEP-2322). A handler therefore runs twice — once to ask, once to act — so
// every ask* method has the same shape: it returns the answer when this call
// carries one, and otherwise records the question and returns the caller's
// fall-back value. The handler asks for everything it needs, then hands
// needsInput's result straight back when anything is outstanding, so several
// questions cost one round trip rather than one each.
//
// Clients too old for multi-round-trip are not a special case here: the SDK's
// server middleware answers their input requests with a plain elicitation call
// and re-enters the handler, so the code below is the only path.
type inputRound struct {
	// supported is false when the client cannot be asked at all, which turns
	// every ask into its fall-back answer without recording a question.
	supported bool
	// answers holds what the client sent back, empty on the first call.
	answers mcp.InputResponseMap
	// retry marks a call that already carries answers. A question with no answer
	// on a retry is treated as unanswered rather than asked again, so a client
	// that drops one request cannot bounce the call back and forth.
	retry bool
	// asked holds the questions this call still needs answered.
	asked mcp.InputRequestMap
}

// newInputRound starts a round of questions for one tool call, reading any
// answers the client sent back with it.
func newInputRound(req *mcp.CallToolRequest) *inputRound {
	r := &inputRound{supported: elicitationSupported(req)}
	if req != nil && req.Params != nil && len(req.Params.InputResponses) > 0 {
		r.answers = req.Params.InputResponses
		r.retry = true
	}
	return r
}

// needsInput returns the tool result that puts the recorded questions to the
// client, or nil when nothing is outstanding. A handler must return it as-is,
// with a zero output value: an input-required result carries no content, because
// the tool has not run yet.
func (r *inputRound) needsInput() *mcp.CallToolResult {
	if len(r.asked) == 0 {
		return nil
	}
	return &mcp.CallToolResult{InputRequests: r.asked}
}

// answer returns the client's answer to the question with this id. ok is false
// when the question has not been put yet (the caller should record it), when the
// client declined to answer it on a retry, or when the answer is not an
// elicitation result.
func (r *inputRound) answer(id string) (*mcp.ElicitResult, bool) {
	res, present := r.answers[id]
	if !present {
		return nil, false
	}
	elicited, isElicit := res.(*mcp.ElicitResult)
	return elicited, isElicit
}

// ask records a question under id, unless this call is a retry that simply came
// back without it — asking again there would loop.
func (r *inputRound) ask(id string, params *mcp.ElicitParams) {
	if r.retry {
		return
	}
	if r.asked == nil {
		r.asked = mcp.InputRequestMap{}
	}
	r.asked[id] = params
}

// askText puts a single free-text question to the user and returns the answer
// with ok=true only once the client has accepted with a non-empty value. Every
// other outcome — no capability, a question still to be put, a decline, an empty
// answer — yields ("", false) so the caller falls back.
func (r *inputRound) askText(id, message, fieldName, fieldDescription string) (string, bool) {
	if !r.supported {
		return "", false
	}
	res, answered := r.answer(id)
	if !answered {
		r.ask(id, &mcp.ElicitParams{
			Mode:            "form",
			Message:         message,
			RequestedSchema: stringSchema(fieldName, fieldDescription),
		})
		return "", false
	}
	return textAnswer(res, fieldName)
}

// textAnswer reads a free-text answer out of an elicitation result. It is the
// server's own guard on what came back: an SDK client validates the content
// against the schema we sent, but nothing obliges a client to, and a value of the
// wrong type or an empty string must fall back rather than be taken as an answer.
func textAnswer(res *mcp.ElicitResult, fieldName string) (string, bool) {
	if res == nil || res.Action != "accept" || res.Content == nil {
		return "", false
	}
	s, isString := res.Content[fieldName].(string)
	if !isString || s == "" {
		return "", false
	}
	return s, true
}

// askChoice puts a one-of-these question to the user and returns the chosen
// value with ok=true only once the client has accepted with one of the offered
// options. Any other outcome yields ("", false) so the caller falls back.
func (r *inputRound) askChoice(id, message, fieldName, fieldDescription string, options []string) (string, bool) {
	if !r.supported {
		return "", false
	}
	res, answered := r.answer(id)
	if !answered {
		r.ask(id, &mcp.ElicitParams{
			Mode:    "form",
			Message: message,
			RequestedSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					fieldName: map[string]any{
						"type":        "string",
						"description": fieldDescription,
						"enum":        options,
					},
				},
				"required": []string{fieldName},
			},
		})
		return "", false
	}
	return choiceAnswer(res, fieldName, options)
}

// choiceAnswer reads a one-of-these answer out of an elicitation result, holding
// it to the offered options: a value outside them is not a choice this server
// made available, whoever sent it.
func choiceAnswer(res *mcp.ElicitResult, fieldName string, options []string) (string, bool) {
	if res == nil || res.Action != "accept" || res.Content == nil {
		return "", false
	}
	s, isString := res.Content[fieldName].(string)
	if !isString || !slices.Contains(options, s) {
		return "", false
	}
	return s, true
}

// confirmDecision is the outcome of a confirmation question, distinguishing an
// explicit user "no" (which callers should honor by NOT proceeding) from an
// inability to ask (no capability, or the question still outstanding), where
// callers must fall back to their deterministic default.
type confirmDecision int

const (
	// confirmUnavailable means the question could not be answered (capability
	// absent, or it has only just been put): the caller must fall back to its
	// default behavior.
	confirmUnavailable confirmDecision = iota
	// confirmProceed means the user explicitly accepted.
	confirmProceed
	// confirmDeclined means the user explicitly declined or canceled: the caller
	// must NOT proceed with the side-effecting action.
	confirmDeclined
)

// rememberFieldDescription labels the opt-out checkbox that turns the
// confirmation off for the rest of the session.
const rememberFieldDescription = "Stop asking for the rest of this session and save future downloads without confirming"

// askConfirm puts a yes/no question to the user. confirmed is true only once the
// client accepted AND set the boolean; ok reports whether an answer was actually
// received (so a caller that defaults to "proceed" can tell "the user said no"
// from "nobody could be asked").
func (r *inputRound) askConfirm(id, message, fieldName, fieldDescription string) (confirmed, ok bool) {
	decision, _ := r.askConfirmRemember(id, message, fieldName, fieldDescription, "")
	switch decision {
	case confirmProceed:
		return true, true
	case confirmDeclined:
		return false, r.answeredAccept(id)
	case confirmUnavailable:
		return false, false
	}
	return false, false
}

// answeredAccept reports whether the answer to id was an accept, which is what
// separates "the user answered no" from "the user declined to answer".
func (r *inputRound) answeredAccept(id string) bool {
	res, answered := r.answer(id)
	return answered && res != nil && res.Action == "accept"
}

// askConfirmDecision puts a yes/no question to the user and returns the
// tri-state decision, without the opt-out checkbox.
func (r *inputRound) askConfirmDecision(id, message, fieldName, fieldDescription string) confirmDecision {
	decision, _ := r.askConfirmRemember(id, message, fieldName, fieldDescription, "")
	return decision
}

// askConfirmRemember is askConfirmDecision plus an optional second checkbox.
// When rememberField is non-empty the form carries it as an OPTIONAL boolean —
// optional because a required one would force the user to touch a field they may
// not care about, and because "I did not tick it" is a meaningful answer in its
// own right. remember is reported true only alongside confirmProceed: a declined
// download must not also silence the prompt that let the user decline.
func (r *inputRound) askConfirmRemember(id, message, fieldName, fieldDescription, rememberField string) (decision confirmDecision, remember bool) {
	if !r.supported {
		return confirmUnavailable, false
	}
	res, answered := r.answer(id)
	if !answered {
		r.ask(id, &mcp.ElicitParams{
			Mode:            "form",
			Message:         message,
			RequestedSchema: confirmSchema(fieldName, fieldDescription, rememberField),
		})
		return confirmUnavailable, false
	}
	return confirmAnswer(res, fieldName, rememberField)
}

// confirmAnswer reads a confirmation out of an elicitation result. An explicit
// decline or cancel is the user saying no; an accept turns on the boolean, and a
// missing or false one is also a no. Anything else is not an answer at all, so
// the caller falls back rather than aborting an action nobody refused.
func confirmAnswer(res *mcp.ElicitResult, fieldName, rememberField string) (decision confirmDecision, remember bool) {
	if res == nil {
		return confirmUnavailable, false
	}
	if res.Action == "decline" || res.Action == "cancel" {
		return confirmDeclined, false
	}
	if res.Action != "accept" {
		return confirmUnavailable, false
	}
	if b, ok := res.Content[fieldName].(bool); !ok || !b {
		return confirmDeclined, false
	}
	if rememberField != "" {
		if b, ok := res.Content[rememberField].(bool); ok && b {
			return confirmProceed, true
		}
	}
	return confirmProceed, false
}

// confirmSchema builds the schema of a confirmation form: a required boolean,
// plus the optional opt-out checkbox when rememberField is named.
func confirmSchema(fieldName, fieldDescription, rememberField string) map[string]any {
	properties := map[string]any{
		fieldName: map[string]any{"type": "boolean", "description": fieldDescription},
	}
	if rememberField != "" {
		properties[rememberField] = map[string]any{
			"type":        "boolean",
			"description": rememberFieldDescription,
		}
	}
	return map[string]any{
		"type":       "object",
		"properties": properties,
		// Only the confirmation itself is required; the opt-out is not.
		"required": []string{fieldName},
	}
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
