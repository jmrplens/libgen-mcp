// stdio_test.go covers the input filter that keeps one malformed line from
// ending a stdio session.
//
// The end-to-end module drives the same behavior against a real process and is
// where the claim is finally made; these cases are the cheap half, and they
// reach the shapes a process is awkward to produce — a line of exactly the
// ceiling, a writer under the race detector, a reader that splits a line across
// several Read calls.

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

// testLineCeiling is the ceiling these cases run at.
//
// Small on purpose: the real default is 4 MiB, and a case that had to build a
// 4 MiB line to test the boundary would spend its time on allocation rather than
// on the boundary. What the filter does at the edge does not depend on where the
// edge is.
const testLineCeiling = 64

// drive feeds input through the filter and returns what the SDK would have read
// and what was written back to the client.
func drive(t *testing.T, input string, ceiling int) (passedThrough, answered string) {
	t.Helper()

	var out bytes.Buffer
	reader, _ := resilientStdio(strings.NewReader(input), &out, ceiling)
	read, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading through the filter: %v", err)
	}
	return string(read), out.String()
}

// TestFilterPassesValidMessagesThrough pins the filter's most important
// property: it does as little as it can.
//
// Anything that is a JSON object carrying "jsonrpc":"2.0" reaches the SDK byte
// for byte, whatever else is wrong with it, because deciding what a valid
// message MEANS is the SDK's job. A filter that second-guessed it would be a
// second, divergent implementation of the protocol, and the divergence would
// show up as a message the server refuses and the specification allows.
func TestFilterPassesValidMessagesThrough(t *testing.T) {
	lines := []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
		// A notification: no id, and still a valid message.
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		// Nonsense as a request, valid as a frame. The SDK answers it, not this.
		`{"jsonrpc":"2.0","id":2,"method":"no/such/method","params":{"a":[1,2,{"b":null}]}}`,
	}
	input := strings.Join(lines, "\n") + "\n"

	passed, answered := drive(t, input, 1<<20)
	if passed != input {
		t.Errorf("a valid message was altered on the way through:\ngot  %q\nwant %q", passed, input)
	}
	if answered != "" {
		t.Errorf("the filter answered a message it should have passed through: %q", answered)
	}
}

// TestFilterAnswersUnreadableLines covers each shape the filter refuses, and the
// code it must use for it.
//
// The codes are the point, not just the refusal. -32700 means the server could
// not parse JSON at all; telling a client that about an array or a number sends
// it looking for a syntax problem it does not have, which is a worse answer than
// silence.
func TestFilterAnswersUnreadableLines(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantCode float64
		// wantID is the id the refusal must echo, or "" for null.
		wantID string
	}{
		{name: "not JSON at all", line: `hello`, wantCode: codeParseError},
		{name: "truncated JSON", line: `{"jsonrpc":"2.0",`, wantCode: codeParseError},
		{name: "a bare JSON number", line: `42`, wantCode: codeInvalidRequest},
		{name: "a bare JSON string", line: `"hello"`, wantCode: codeInvalidRequest},
		{name: "an object without jsonrpc", line: `{"id":7,"method":"x"}`, wantCode: codeInvalidRequest, wantID: "7"},
		{name: "an object with the wrong version", line: `{"jsonrpc":"1.0","id":"a"}`, wantCode: codeInvalidRequest, wantID: `"a"`},
		// An id JSON-RPC does not allow must not be echoed: a refusal carrying
		// an object id would itself be an invalid response.
		{name: "an object id is not echoed", line: `{"id":{"k":1},"method":"x"}`, wantCode: codeInvalidRequest},
		{name: "an array id is not echoed", line: `{"id":[1],"method":"x"}`, wantCode: codeInvalidRequest},
		// A boolean is not one of the three types the specification allows
		// there either, and echoing it makes the refusal an invalid response —
		// so a client that sent something malformed cannot parse the reply
		// telling it so.
		{name: "a boolean id is not echoed", line: `{"jsonrpc":"1.0","id":true}`, wantCode: codeInvalidRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			passed, answered := drive(t, tt.line+"\n", 1<<20)
			if passed != "" {
				t.Errorf("an unreadable line reached the SDK: %q", passed)
			}
			code, id := decodeRefusal(t, answered)
			if code != tt.wantCode {
				t.Errorf("error code = %v, want %v: %s", code, tt.wantCode, answered)
			}
			want := tt.wantID
			if want == "" {
				want = "null"
			}
			if id != want {
				t.Errorf("refusal id = %s, want %s: %s", id, want, answered)
			}
		})
	}
}

// TestFilterDropsBlankLinesSilently pins the one refusal that says nothing.
//
// A blank line is framing, not a message. Answering one would be noise on a
// stream that is otherwise well behaved, and a client that writes "\n\n" between
// messages would get a refusal for every gap.
func TestFilterDropsBlankLinesSilently(t *testing.T) {
	valid := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

	passed, answered := drive(t, "\n   \n\t\n"+valid+"\n", 1<<20)
	if passed != valid+"\n" {
		t.Errorf("the valid message did not come through intact: %q", passed)
	}
	if answered != "" {
		t.Errorf("a blank line was answered: %q", answered)
	}
}

// TestFilterCeilingIsOnTheMessageNotTheFraming is the off-by-one that decides
// whether the two transports agree.
//
// bufio.ReadSlice yields the newline with the line it terminates, so counting
// what it returns charges the framing to the message and refuses a message of
// exactly the ceiling — a byte narrower than the HTTP body cap this number is
// chosen to match. The two rows here are one byte apart and that is the whole
// case.
func TestFilterCeilingIsOnTheMessageNotTheFraming(t *testing.T) {
	// A well-formed message padded to an exact length, so the boundary is the
	// only thing that decides the outcome.
	atCeiling := paddedMessage(t, testLineCeiling)
	overCeiling := paddedMessage(t, testLineCeiling+1)

	t.Run("exactly the ceiling is accepted", func(t *testing.T) {
		passed, answered := drive(t, atCeiling+"\n", testLineCeiling)
		if passed != atCeiling+"\n" {
			t.Errorf("a message of exactly the ceiling was refused: %q (answered %q)", passed, answered)
		}
	})

	t.Run("one byte over is refused", func(t *testing.T) {
		passed, answered := drive(t, overCeiling+"\n", testLineCeiling)
		if passed != "" {
			t.Errorf("an over-long message reached the SDK: %q", passed)
		}
		code, _ := decodeRefusal(t, answered)
		if code != codeInvalidRequest {
			t.Errorf("error code = %v, want %v: %s", code, float64(codeInvalidRequest), answered)
		}
		// The refusal has to name the variable that raises the ceiling, or the
		// deployment whose client sends larger messages has nothing to act on.
		if !strings.Contains(answered, config.EnvName("STDIO_MAX_LINE_BYTES")) {
			t.Errorf("the refusal does not name the variable that raises the ceiling: %s", answered)
		}
	})
}

// TestFilterResynchronizesAfterAnOverLongLine is what makes the ceiling a
// refusal rather than a break.
//
// The remainder of an over-long message is discarded to the newline rather than
// read as a message of its own: otherwise one refusal becomes a stream of them,
// and a fragment that happens to parse would reach the SDK as a message nobody
// sent.
func TestFilterResynchronizesAfterAnOverLongLine(t *testing.T) {
	valid := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	oversize := `{"jsonrpc":"2.0","id":9,"method":"x","params":{"pad":"` + strings.Repeat("A", 4*testLineCeiling) + `"}}`

	passed, answered := drive(t, oversize+"\n"+valid+"\n", testLineCeiling)
	if passed != valid+"\n" {
		t.Errorf("the line after an over-long one was not served intact: %q", passed)
	}
	if got := strings.Count(strings.TrimSpace(answered), "\n") + 1; got != 1 {
		t.Errorf("one over-long line produced %d refusals, want 1: %s", got, answered)
	}
}

// TestFilterAssemblesALineLongerThanTheReadBuffer pins why this is not a
// bufio.Scanner.
//
// A Scanner caps a token at its buffer size and silently truncates past it,
// which would turn a legitimate large request into a corrupted one — the exact
// failure this file exists to prevent, arriving as a parse error the client
// cannot explain. ReadSlice reports a full buffer instead, and the assembly
// loop keeps going.
func TestFilterAssemblesALineLongerThanTheReadBuffer(t *testing.T) {
	// bufio's default buffer is 4096, so a message comfortably past it takes
	// several ReadSlice calls to assemble.
	big := `{"jsonrpc":"2.0","id":1,"method":"x","params":{"pad":"` + strings.Repeat("B", 40_000) + `"}}`

	passed, answered := drive(t, big+"\n", 1<<20)
	if passed != big+"\n" {
		t.Errorf("a message longer than the read buffer did not come through intact (got %d bytes, want %d; answered %q)",
			len(passed), len(big)+1, answered)
	}
}

// TestFilterServesALastLineWithNoTrailingNewline pins the shape a client leaves
// behind when it writes a message and exits without terminating it.
//
// The assembled line and the end of the stream arrive on the same read, so the
// filter has to hand over what it has before reporting the end — returning the
// error first would silently drop a message the client did send, which is worse
// than the truncation this file exists to prevent because nothing anywhere would
// say so.
func TestFilterServesALastLineWithNoTrailingNewline(t *testing.T) {
	valid := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

	passed, answered := drive(t, valid, 1<<20)
	if passed != valid {
		t.Errorf("the last line was not served: %q (answered %q)", passed, answered)
	}
	if answered != "" {
		t.Errorf("a well-formed last line was answered with an error: %q", answered)
	}
}

// TestLockedWriterSerializesWriters is the case the race detector is for.
//
// The SDK writes responses and this filter writes refusals, both to the same
// stdout, and progressNotifier adds a third writer for as long as a download
// runs. A refusal interleaved into the middle of a response would corrupt the
// very stream this exists to protect.
func TestLockedWriterSerializesWriters(t *testing.T) {
	var out bytes.Buffer
	w := &lockedWriter{w: &out}

	const writers, each = 8, 50
	var wg sync.WaitGroup
	for i := range writers {
		mark := "abcdefgh"[i : i+1]
		wg.Go(func() {
			line := []byte(strings.Repeat(mark, 64) + "\n")
			for range each {
				if _, err := w.Write(line); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()

	// Every line is one writer's byte repeated, so a frame cut in half by
	// another writer shows up as a line with two of them in it.
	for line := range strings.SplitSeq(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if strings.Count(line, string(line[0])) != len(line) {
			t.Fatalf("a write was interleaved with another: %q", line)
		}
	}
}

// TestLockedWriterCloseLeavesStdoutOpen pins that the filter does not close a
// stream it does not own. Closing stdout would take the process's own output
// with it, and the SDK calls Close when a session ends.
func TestLockedWriterCloseLeavesStdoutOpen(t *testing.T) {
	var out bytes.Buffer
	w := &lockedWriter{w: &out}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := w.Write([]byte("still open\n")); err != nil {
		t.Errorf("the writer stopped working after Close: %v", err)
	}
}

// paddedMessage builds a valid JSON-RPC line of exactly n bytes.
func paddedMessage(t *testing.T, n int) string {
	t.Helper()
	const prefix = `{"jsonrpc":"2.0","id":1,"method":"x","params":{"pad":"`
	const suffix = `"}}`
	padding := n - len(prefix) - len(suffix)
	if padding < 0 {
		t.Fatalf("a %d byte message cannot hold the envelope (%d bytes)", n, len(prefix)+len(suffix))
	}
	line := prefix + strings.Repeat("A", padding) + suffix
	if len(line) != n {
		t.Fatalf("built a %d byte message, want %d", len(line), n)
	}
	if !json.Valid([]byte(line)) {
		t.Fatalf("the padded message is not valid JSON: %s", line)
	}
	return line
}

// decodeRefusal pulls the error code and the raw id out of a refusal line.
func decodeRefusal(t *testing.T, answered string) (code float64, id string) {
	t.Helper()
	if answered == "" {
		t.Fatal("nothing was written back to the client")
	}
	if !strings.HasSuffix(answered, "\n") {
		t.Errorf("the refusal is not newline-terminated, so it runs into the next message: %q", answered)
	}
	var decoded struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   *struct {
			Code    float64 `json:"code"`
			Message string  `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(answered)), &decoded); err != nil {
		t.Fatalf("the refusal is not JSON: %v: %q", err, answered)
	}
	if decoded.JSONRPC != "2.0" {
		t.Errorf("the refusal is not JSON-RPC 2.0: %q", answered)
	}
	if decoded.Error == nil {
		t.Fatalf("the refusal carries no error member: %q", answered)
	}
	if decoded.Error.Message == "" {
		t.Errorf("the refusal carries no message, so the client is told a number and nothing else: %q", answered)
	}
	return decoded.Error.Code, string(decoded.ID)
}

// TestFilterPassesABatchToTheSDK keeps a client this server still supports from
// being refused by the filter in front of it.
//
// A top-level array is a JSON-RPC batch, and whether one is allowed depends on
// the negotiated protocol version: the SDK accepts batches for every version
// before 2025-06-18. Refusing them here takes that decision away from the one
// place that knows the version, and turns a legacy client's valid request into
// an Invalid Request it cannot act on.
func TestFilterPassesABatchToTheSDK(t *testing.T) {
	const batch = `[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`

	passed, answered := drive(t, batch+"\n", 1<<20)

	if passed != batch+"\n" {
		t.Errorf("the batch did not reach the SDK: passed %q", passed)
	}
	if answered != "" {
		t.Errorf("the filter answered a batch itself: %s", answered)
	}
}
