// stdio.go presents stdin to the SDK with lines it cannot read already
// answered, so one malformed message does not end the session.

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/jmrplens/libgen-mcp/v2/internal/config"
)

// resilientStdio builds the reader and writer the stdio transport runs on, with
// unreadable input answered rather than fatal.
//
// The SDK's read loop ends its reader goroutine on any error from the decoder,
// and the session ends with it — which on stdio is the process. A message that
// fails to parse is therefore handled exactly like a closed pipe: the client is
// left with EOF on a stream it can still write to, and nothing is written
// explaining why.
//
// JSON-RPC 2.0 defines error codes for both shapes of this, and the framing here
// is one message per line, so the next line is an independent message and
// resynchronizing costs nothing. This filter does that on the SDK's behalf: a
// line it would choke on is answered and dropped, and the SDK never sees it.
//
// It deliberately does the least it can. Anything that parses as a JSON object
// carrying "jsonrpc":"2.0" is passed through untouched, whatever else is wrong
// with it, because deciding what a valid message means is the SDK's job and a
// filter that second-guessed it would be a second, divergent implementation of
// the protocol.
func resilientStdio(in io.Reader, out io.Writer, maxLineBytes int) (*sanitizedInput, io.WriteCloser) {
	shared := &lockedWriter{w: out}
	return &sanitizedInput{in: bufio.NewReader(in), out: shared, maxLineBytes: maxLineBytes}, shared
}

// lockedWriter serializes writes from the SDK and from the input filter.
//
// Both write to the same stdout, and a refusal interleaved into the middle of a
// response would corrupt the very stream this exists to protect. It is not a
// theoretical collision here: progressNotifier writes notifications while a
// multi-megabyte fetch is in flight, so there is a writer on another goroutine
// for as long as a download runs.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// Write serializes one write against every other writer sharing this stdout.
func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// Close is a no-op: stdout is not ours to close.
func (l *lockedWriter) Close() error { return nil }

// sanitizedInput presents stdin to the SDK with unparseable lines removed.
type sanitizedInput struct {
	in  *bufio.Reader
	out io.Writer
	// maxLineBytes is the longest line that will be assembled. A longer one is
	// discarded to the newline and answered rather than accumulated.
	maxLineBytes int

	// pending holds the remainder of the line currently being handed over.
	// Lines are passed through whole, so the SDK's own framing is unchanged.
	pending []byte
}

// Read implements io.Reader, yielding only lines the SDK can parse.
//
// A line is assembled rather than scanned with a bufio.Scanner because a
// Scanner caps a token at its buffer size and silently truncates past it, which
// would turn a legitimate large request into the exact failure this is here to
// prevent. The assembly is bounded all the same: a line past the ceiling is
// dropped up to the next newline and answered, which is the resynchronization
// this file already performs for unparseable lines. Without that bound there is
// no ceiling at all, and a peer that never sends a newline grows the process by
// the size of what it writes.
func (s *sanitizedInput) Read(p []byte) (int, error) {
	for len(s.pending) == 0 {
		line, oversize, err := s.readLine()
		switch {
		case oversize:
			_, _ = s.out.Write(errorLine(nil, codeInvalidRequest, fmt.Sprintf(
				"Invalid Request: message exceeds the %d byte limit; send a smaller message or raise %s",
				s.maxLineBytes, config.EnvName("STDIO_MAX_LINE_BYTES"),
			)))
		case line != "":
			if refusal, refuse := refuseUnreadable(line); refuse {
				if refusal != nil {
					_, _ = s.out.Write(refusal)
				}
			} else {
				s.pending = []byte(line)
			}
		}
		if err != nil {
			// Handed back unchanged, EOF included: the SDK decides what a read
			// error means, and a filter that translated one would be making
			// that decision somewhere the caller cannot see.
			if len(s.pending) == 0 {
				return 0, err
			}
			break
		}
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

// readLine assembles the next line, reporting separately that it was longer
// than the ceiling and has been discarded.
//
// Discarding continues to the newline rather than stopping at the ceiling, so
// the remainder of an over-long message is not read as a message of its own —
// which would turn one refusal into a stream of them, and could hand the SDK a
// fragment that happens to parse.
func (s *sanitizedInput) readLine() (line string, oversize bool, err error) {
	var assembled []byte
	for {
		chunk, readErr := s.in.ReadSlice('\n')
		// ReadSlice yields the delimiter with the line it terminates. The
		// ceiling is on the message, not on the framing around it, so the
		// newline is not charged to it: counting it would refuse a message of
		// exactly the ceiling, which is a byte narrower than the HTTP body cap
		// this number is chosen to match.
		counted := len(chunk)
		if readErr == nil && counted > 0 {
			counted--
		}
		switch {
		case oversize:
			// Already over: keep reading to the newline, keep nothing.
		case len(assembled)+counted > s.maxLineBytes:
			oversize = true
			assembled = nil
		default:
			assembled = append(assembled, chunk...)
		}
		if readErr == nil {
			return string(assembled), oversize, nil
		}
		// A full buffer is not an error, it is how ReadSlice says "more of this
		// line follows".
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		return string(assembled), oversize, readErr
	}
}

// Close is a no-op: stdin is not ours to close.
func (s *sanitizedInput) Close() error { return nil }

// The two JSON-RPC 2.0 error codes this filter can produce. Anything else is a
// judgment about what a message means, which belongs to the SDK.
const (
	// codeParseError says the server could not parse JSON at all.
	codeParseError = -32700
	// codeInvalidRequest says the JSON parsed and is not a valid request.
	codeInvalidRequest = -32600
)

// refuseUnreadable reports whether a line must be kept from the SDK, and what to
// answer its sender.
//
// A nil refusal with refuse true means drop it silently: a blank line is
// framing, not a message, and answering one would be noise on a stream that is
// otherwise well behaved.
//
// The pre-parse is the whole of the filter's cost, and it is bounded by the line
// ceiling rather than by a depth check of its own. Go's decoder refuses nesting
// past 10000 levels and is linear in the bytes it scans, so the worst a peer can
// do inside a 4 MiB line is about two milliseconds — measured, not assumed — and
// the SDK's own decoder caps depth at 1000 behind that. A third limit here would
// be a third number to keep in step for no bound it adds.
func refuseUnreadable(line string) (refusal []byte, refuse bool) {
	trimmed := trimSpace(line)
	if trimmed == "" {
		return nil, true
	}

	var probe struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		// Unmarshal into a struct also fails for well-formed JSON that is not
		// an object — an array, a bare string, a number — and those are not
		// parse errors. -32700 means the server could not parse JSON at all;
		// telling a client that about valid JSON sends it looking for a syntax
		// problem it does not have.
		if json.Valid([]byte(trimmed)) {
			// A top-level array is refused here too, and that is deliberate
			// rather than an oversight about batches.
			//
			// It is true that the SDK accepts a batch for a protocol version
			// negotiated before 2025-06-18, and that refusing one here takes
			// that away from a client old enough to send it. Passing it through
			// was tried and measured instead: on a session that negotiated a
			// newer version — which is every ordinary client — the SDK treats
			// the array as a protocol violation, ends the session and the
			// process exits non-zero. That is exactly the failure this filter
			// exists to prevent, reintroduced for the common case to serve a
			// case nothing in this project has seen.
			//
			// The filter cannot tell the two apart: it sits under the SDK and
			// the negotiated version is the SDK's to know. Given one answer for
			// both, an Invalid Request a legacy client can read beats a dead
			// server for everyone else.
			return errorLine(nil, codeInvalidRequest, "Invalid Request"), true
		}
		// Genuinely not JSON. The id is unknowable, so it is null, which is
		// what JSON-RPC 2.0 prescribes for a parse error.
		return errorLine(nil, codeParseError, "Parse error"), true
	}
	if probe.JSONRPC != "2.0" {
		// Structurally JSON and not a JSON-RPC message. The id, if it carried
		// one, is worth echoing so the sender can match the refusal — but only
		// a scalar one: JSON-RPC allows string, number or null there, and
		// echoing an object or array id would make the refusal itself an
		// invalid response.
		return errorLine(scalarID(probe.ID), codeInvalidRequest, "Invalid Request"), true
	}
	return nil, false
}

// scalarID returns the id if JSON-RPC may echo it, or nothing.
//
// String, number and null are the whole of what the specification allows there,
// so an object, an array and a boolean are all ids this server must not echo:
// answering `{"id":true}` with `{"id":true}` makes the refusal itself an invalid
// response, which leaves a client that sent something malformed unable to parse
// the reply telling it so.
func scalarID(id json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(id)
	if len(trimmed) == 0 {
		return nil
	}
	switch trimmed[0] {
	case '{', '[', 't', 'f':
		return nil
	}
	return id
}

// errorLine builds one newline-terminated JSON-RPC error response.
func errorLine(id json.RawMessage, code int, message string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
	if err != nil {
		// Unreachable: every field here marshals. Staying silent is better than
		// writing a half-formed line onto the protocol stream.
		return nil
	}
	return append(body, '\n')
}

// trimSpace trims ASCII whitespace, which is all a JSON line can be padded with.
func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

// isSpace reports whether c is one of the four bytes JSON counts as whitespace.
func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}
