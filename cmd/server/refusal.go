// refusal.go writes the transport-level refusals of the MCP endpoint.
//
// A refusal on this route is answered as JSON-RPC, carrying the id of the
// request it refuses, because a client handed a 4xx whose body is not a
// recognizable JSON-RPC error is told by the transport specification to
// conclude it is talking to an initialization-era server and downgrade to the
// withdrawn HTTP+SSE transport. It then issues a GET, which a stateless
// deployment answers 405, and ends with no transport at all — instead of the
// single retry a readable error would have told it to make.
//
// So an opaque refusal does not merely read badly: it turns a configuration
// mistake into a false protocol diagnosis. Every gate in front of the endpoint
// — the Host guard, the cross-origin protection, the protocol-version check —
// writes through here, so a client sees one shape whichever layer refused.
//
// Off the endpoint nothing changes. The 404 answers a scanner rather than a
// client and already names the endpoint, and the server card is a public
// document with no JSON-RPC session behind it.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
)

// JSON-RPC error codes for the refusals this server writes.
//
// -32768 to -32000 is reserved by JSON-RPC and by the MCP specification, so a
// code for a condition the specification does not define has to be allocated
// outside it. These mirror their HTTP status multiplied by -100, which lands
// well below the reserved range and reads back as the status it came from.
//
// codeUnsupportedProtocolVersion is the exception, and deliberately so: the MCP
// specification defines that one, and it sits where the specification put it.
const (
	codeUnsupportedProtocolVersion = -32022
	codeForbidden                  = -40300 // mirrors HTTP 403
	codeBadRequest                 = -40000 // mirrors HTTP 400
)

// maxIDProbeBytes bounds how much of a request body is read to recover its id.
//
// The id is a top-level member and conventionally the second one, so this is
// generous rather than tight. It exists because the probe runs on requests that
// are being refused, from callers nothing has vouched for: without a bound,
// anyone could make the server buffer a body of any size.
const maxIDProbeBytes = 1 << 20

// refusal is a rejection of an MCP request, ready to be written as a response.
type refusal struct {
	status  int
	code    int
	message string
	// header carries response headers the status requires, such as Retry-After
	// on a 429. Nil when the status needs none.
	header http.Header
	// data is the error object's optional `data` member, already in its wire
	// shape, or nil to omit it.
	data any
}

// jsonRPCError is the wire shape of a transport-level rejection.
//
// The id is recovered from the body and the member is omitted when there is
// none, rather than sent as null: under 2026-07-28 a RequestId is a string or
// an integer, so null is not a legal value and the schema marks the member
// optional precisely so it can be left out. A client that recognizes this body
// by validating it against the published schema — rather than by reading
// error.code — is the case that makes the shape matter, because a null id fails
// that validation and the failure it produces is the very downgrade this body
// exists to prevent.
type jsonRPCError struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id,omitempty"`
	Error   jsonRPCErrorBody `json:"error"`
}

// jsonRPCErrorBody is the error object of that response.
type jsonRPCErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// write emits the refusal as a JSON-RPC error response with its status.
//
// The request is taken so the response can carry the id it refuses. Every
// caller returns immediately afterwards, which is what makes reading the body
// here safe — see [requestIDFromBody].
func (f refusal) write(w http.ResponseWriter, r *http.Request) {
	for name, values := range f.header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.status)
	body := jsonRPCError{
		JSONRPC: "2.0",
		ID:      requestIDFromBody(r),
		Error:   jsonRPCErrorBody{Code: f.code, Message: f.message, Data: f.data},
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.ErrorContext(r.Context(), "failed to write the refusal response", "error", err)
	}
}

// newHeader builds a canonical single-value header set.
//
// Constructing an http.Header from a map literal skips canonicalization, so a
// key spelled the way its RFC spells it never answers Header.Get, whose lookup
// key is canonicalized. The wire output stays correct either way because
// [refusal.write] uses Add, but anything reading a refusal back sees an empty
// header. Pairs arrive as alternating name and value; an odd trailing name is
// ignored.
func newHeader(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}

// requestIDFromBody recovers the JSON-RPC id of a request that is about to be
// refused, so the error response correlates with it.
//
// "Error responses MUST include the same ID as the request they correspond to
// (except in error cases where the ID could not be read due a malformed
// request)." The exception is narrow, and none of the refusals this serves fall
// under it: an unsupported protocol version, an untrusted Origin and a wrong
// Host are all decided from headers, on a body that is perfectly well formed
// and whose id is right there.
//
// It returns nil when there is no id to be had: a GET, a notification, a body
// that is not a JSON-RPC message, or one larger than the probe reads. Callers
// then omit the member entirely rather than sending null.
//
// **Reading the body is safe only because every caller is refusing the request
// and returning**; nothing downstream will read it again. Do not call this on a
// path that forwards.
func requestIDFromBody(r *http.Request) json.RawMessage {
	if r == nil || r.Body == nil || r.Method != http.MethodPost {
		return nil
	}

	var probe struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxIDProbeBytes))
	if err := decoder.Decode(&probe); err != nil {
		return nil
	}
	// Decode stops at the end of the first JSON value, so a body like
	// `{"id":1} trailing` decodes without complaint. That body is not a
	// request, and an id lifted out of it is not the id of anything: require
	// the value to be the whole of what was sent.
	//
	// A second Decode rather than decoder.More(), which is not the check it
	// looks like. More reports whether another element exists in the array or
	// object currently being parsed, and is implemented as "the next byte is
	// not ] or }" — so it answers true for `{...} trailing` and for two
	// concatenated messages, and false for `{...}}`. Only end of input means
	// the value was the whole body.
	var rest json.RawMessage
	if err := decoder.Decode(&rest); !errors.Is(err, io.EOF) {
		return nil
	}
	// An object that does not announce itself as JSON-RPC 2.0 is not a message,
	// so there is no request to correlate a refusal with. Stopping here rather
	// than also requiring "method" is deliberate: a body carrying jsonrpc and an
	// id but no method is a malformed request, which the specification's own
	// exception covers, and refusing to echo would withhold an id the sender can
	// still match.
	if probe.JSONRPC != "2.0" {
		return nil
	}
	if !isRequestID(probe.ID) {
		return nil
	}
	return probe.ID
}

// isRequestID reports whether a raw JSON value can stand as a JSON-RPC id.
//
// A string or a number is echoed back exactly as it arrived, which is what
// correlation means: the client matches on the value it sent, not on our
// reading of it. Everything else — null, an object, an array, an absent member
// — leaves the response with no id.
func isRequestID(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	switch {
	case trimmed[0] == '"':
		var s string
		return json.Unmarshal(trimmed, &s) == nil
	case trimmed[0] == '-' || (trimmed[0] >= '0' && trimmed[0] <= '9'):
		var n json.Number
		return json.Unmarshal(trimmed, &n) == nil
	}
	return false
}
