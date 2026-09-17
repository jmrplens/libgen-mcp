//go:build stdioe2e

// transport_test.go covers the two streams themselves: what may appear on
// stdout, what belongs on stderr, and what the process does when nobody says
// anything for a while.

package stdioe2e

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

// startupLine is what the server writes to stderr once it is serving stdio. It
// is the anchor every stderr assertion waits on, since the harness copies that
// stream on a goroutine of its own.
const startupLine = "serving on stdio"

// searchCall is the tools/call parameters these tests use when they need the
// server to do real work.
//
// The arguments are spelled the way the tool's own schema declares them, which
// matters more than it looks: the SDK validates arguments before the handler is
// reached, so a call with a field the schema does not have comes back as an
// IsError result having run none of the code the test is about — and having
// logged nothing either, since the metering wrapper is inside the handler. A
// test written against an invented "limit" field passed its stdout assertion
// and silently measured nothing.
//
// extra_sources is "never" so the call stays on the fixture. Left at its
// default, an empty catalog page escalates the search to the open-access
// providers, which are real hosts on the public internet — and a transport
// module that reaches them reports their outages as this server's regressions.
const searchCall = `{"name":"search","arguments":{"query":"kolmogorov","results_per_page":25,"extra_sources":"never"}}`

// TestStdout_CarriesNothingButJSONRPC pins the property every stdio client
// depends on and no unit test can observe.
//
// On this transport stdout is the protocol. One stray fmt.Println, one library
// that logs to the wrong stream, one banner behind a flag, and every client
// fails to parse the stream — usually with an error that names the client
// rather than this server. Nothing in the type system stops it, and until this
// module existed nothing in the repository noticed: the live suite drives tool
// behavior and test/e2e/http drives a socket, neither of which has a stdout to
// keep clean.
//
// scripts/validate-npm.mjs checks the same property for the launcher package,
// which is the tell that it matters and that it was being checked in the wrong
// place: a defect found while publishing is found after the code is tagged.
func TestStdout_CarriesNothingButJSONRPC(t *testing.T) {
	s := startSession(t, baseEnv(t, startMirror(t)))

	// The handshake, the catalog, and a call that actually reaches the mirror —
	// the three places a stray write is most likely, since startup, registration
	// and tool execution are all on that path.
	for _, tc := range []struct {
		name    string
		request string
	}{
		{name: "initialize", request: initializeRequest(1)},
		{name: "tools/list", request: request(2, "tools/list", "")},
		{name: "prompts/list", request: request(3, "prompts/list", "")},
		{name: "tools/call", request: request(4, "tools/call", searchCall)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// readMessage already fails on anything that is not JSON. What is
			// left to check is that it is JSON-RPC and answers what was asked.
			got := s.call(t, tc.request)
			if got["jsonrpc"] != "2.0" {
				t.Errorf("%s was not answered with JSON-RPC 2.0: %v", tc.name, got)
			}
			if got["id"] == nil {
				t.Errorf("%s came back with no id, so a client cannot match it to a request: %v", tc.name, got)
			}
		})
	}

	// The startup line is written before the server answers anything, so its
	// absence would mean the process took a different path entirely.
	s.waitForStderr(t, startupLine, 10*time.Second)
}

// TestStderr_TakesTheLogsAndStdoutDoesNot checks the other half of the same
// rule, at a log level noisy enough to catch a misrouted writer.
//
// Debug logging produces a lot, and none of it may appear on stdout — which
// readMessage would have failed on. What this adds is that it appeared at all:
// a server that logged nowhere would pass the test above for the wrong reason.
func TestStderr_TakesTheLogsAndStdoutDoesNot(t *testing.T) {
	env := baseEnv(t, startMirror(t))
	env["LIBGEN_MCP_LOG_LEVEL"] = "debug"
	s := startSession(t, env)

	if got := s.call(t, initializeRequest(1)); got["error"] != nil {
		t.Fatalf("initialize failed: %v", got["error"])
	}
	if got := s.call(t, request(2, "tools/list", "")); got["error"] != nil {
		t.Fatalf("tools/list failed: %v", got["error"])
	}

	s.waitForStderr(t, startupLine, 10*time.Second)
}

// TestStderr_LogLinesKeepTheirSeverity pins that a record's severity survives
// the attributes traveling with it.
//
// slog's JSON handler names the record's own severity "level", and an attribute
// added anywhere on the way out under the same key is a second member with the
// same name. Neither side deduplicates, so the line goes out with two and any
// parser keeping the last reads the severity as the empty string. The raw text
// looks correct either way, which is why this is asserted by parsing.
//
// Only the JSON lines are examined. The first thing this server writes to
// stderr is a plain "serving on stdio" banner, which is not a log record and is
// not meant to be parsed as one — stderr is not the protocol, so a human line
// there is fine and a record with no severity is not.
//
// It anchors on a record the tool call itself produces rather than on the
// startup banner. The banner is written before the call is even made, so
// waiting for it would return a buffer the call's own records had not reached
// yet — the harness copies stderr on a goroutine with no ordering against the
// stdout reply — and every assertion below would run on an empty stream and
// pass for the wrong reason. That is not hypothetical: it is what the first
// version of this test did.
func TestStderr_LogLinesKeepTheirSeverity(t *testing.T) {
	env := baseEnv(t, startMirror(t))
	env["LIBGEN_MCP_LOG_LEVEL"] = "debug"
	s := startSession(t, env)

	if got := s.call(t, initializeRequest(1)); got["error"] != nil {
		t.Fatalf("initialize failed: %v", got["error"])
	}
	if got := s.call(t, request(2, "tools/call", searchCall)); got["error"] != nil {
		t.Fatalf("the search call failed: %v", got["error"])
	}
	// withRecovery writes this for every call it meters, so it exists whether
	// the search found anything or not.
	text := s.waitForStderr(t, "tool call completed", 10*time.Second)

	records := 0
	for line := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Errorf("a log line begins like JSON and is not: %q (%v)", line, err)
			continue
		}
		records++
		if level, _ := record["level"].(string); level == "" {
			t.Errorf("a log line reached a parser with no severity: %s", line)
		}
	}
	// Without this the loop above passes on a stream that carried no records at
	// all, which is the same result a silenced logger produces.
	if records == 0 {
		t.Errorf("no log records were written at debug level, so nothing was checked:\n%s", text)
	}
}

// TestHandshake_DeclaresOnlyWhatThisServerServes drives the advertised
// capabilities over the wire.
//
// A nil capability is not neutral: the SDK fills one in with its own defaults,
// so a capability this server does not serve gets advertised purely by
// omission. cmd/server asserts the pin against the struct; this asserts what a
// client actually receives, which is the thing the promise is made to.
func TestHandshake_DeclaresOnlyWhatThisServerServes(t *testing.T) {
	s := startSession(t, baseEnv(t, startMirror(t)))

	got := s.call(t, initializeRequest(1))
	if got["error"] != nil {
		t.Fatalf("initialize failed: %v", got["error"])
	}
	result, ok := got["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize returned no result: %v", got)
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("the handshake declared no capabilities: %v", result)
	}

	for _, want := range []string{"tools", "prompts"} {
		t.Run(want, func(t *testing.T) {
			declared, present := caps[want].(map[string]any)
			if !present {
				t.Fatalf("the handshake does not declare %q, which this server registers", want)
			}
			// listChanged is false because the catalog is fixed at registration
			// and only changes with a release. It is not cosmetic: a client that
			// believes it opens a subscriptions/listen stream this server never
			// completes.
			if changed, _ := declared["listChanged"].(bool); changed {
				t.Errorf("%q declares listChanged, which this server never sends", want)
			}
		})
	}
	for _, unwanted := range []string{"logging", "resources", "completions"} {
		t.Run("no "+unwanted, func(t *testing.T) {
			if _, present := caps[unwanted]; present {
				t.Errorf("the handshake declares %q, which this server does not serve: %v", unwanted, caps)
			}
		})
	}
}

// TestIdleSession_IsNotClosedByTheServer is the regression pin for
// ServerOptions.KeepAlive staying zero.
//
// The SDK's keepalive is opt-in and closes the session on the first unanswered
// ping. ping is removed in the 2026-07-28 revision, so a conformant client of
// that revision cannot answer one, and a server that asked for a keepalive
// would kill the session of an idle client — a defect that shipped in the
// sibling project and was held in place there by a unit test asserting the ping
// ought to be there. This server is correct today by omission, which is exactly
// the kind of correctness that needs a test outside the code that omits it.
//
// Sixty seconds is deliberate: it has to outlast a ping interval and the
// failure threshold that would follow it, or the test passes by finishing
// early. That cost is why it lives in this module and not in the unit suite.
func TestIdleSession_IsNotClosedByTheServer(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out a keepalive interval")
	}

	s := startSession(t, baseEnv(t, startMirror(t)))
	if got := s.call(t, initializeRequest(1)); got["error"] != nil {
		t.Fatalf("initialize failed: %v", got["error"])
	}

	time.Sleep(60 * time.Second)

	if !s.alive() {
		t.Fatalf("the server exited while idle:\n%s", s.stderrText())
	}
	// Still serving, and nothing unsolicited arrived in between: the next
	// message must be this call's own answer, which is what call() checks.
	if got := s.call(t, request(2, "tools/list", "")); got["error"] != nil {
		t.Fatalf("the session stopped working while idle: %v", got["error"])
	}
}

// TestAliveReportsADeadProcess keeps the assertion above from being vacuous.
//
// alive() reads a channel the reaper closes, and a helper that always answered
// "yes" would make the idle test pass on a server that had died — which is the
// failure it exists to catch. So the helper is checked against a process that
// is definitely gone.
func TestAliveReportsADeadProcess(t *testing.T) {
	s := startSession(t, baseEnv(t, startMirror(t)))
	if got := s.call(t, initializeRequest(1)); got["error"] != nil {
		t.Fatalf("initialize failed: %v", got["error"])
	}
	if !s.alive() {
		t.Fatal("the server is reported dead while it is answering requests")
	}

	if _, exited := s.terminate(t, 20*time.Second); !exited {
		t.Fatalf("the server did not exit when asked:\n%s", s.stderrText())
	}
	if s.alive() {
		t.Error("alive() still reports the process as running after it exited, so every assertion resting on it is vacuous")
	}
}

// TestMalformedInput_IsAnsweredAndTheSessionSurvives is the defect the input
// filter exists for.
//
// The SDK's read loop ends its reader goroutine on any error from the decoder,
// and the session ends with it — which on stdio is the process. So a message
// that fails to parse is handled exactly like a closed pipe: the client is left
// with EOF on a stream it can still write to, and nothing is written explaining
// why. One malformed line from a buggy client takes the server down.
//
// Every row asserts both halves, because either alone would pass against a
// broken server: the sender gets the JSON-RPC error its input deserves, and the
// session still answers the next ordinary request. The framing here is one
// message per line, so resynchronizing costs nothing and there is no reason for
// a bad line to be fatal.
func TestMalformedInput_IsAnsweredAndTheSessionSurvives(t *testing.T) {
	tests := []struct {
		name string
		line string
		// wantCode is the JSON-RPC error code the sender must be told.
		wantCode float64
	}{
		{name: "not JSON at all", line: `this is not json`, wantCode: -32700},
		{name: "truncated JSON", line: `{"jsonrpc":"2.0","id":1,`, wantCode: -32700},
		{name: "valid JSON that is not an object", line: `[1,2,3]`, wantCode: -32600},
		{name: "a JSON object that is not JSON-RPC", line: `{"hello":"world","id":9}`, wantCode: -32600},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := startSession(t, baseEnv(t, startMirror(t)))
			if got := s.call(t, initializeRequest(1)); got["error"] != nil {
				t.Fatalf("initialize failed: %v", got["error"])
			}

			s.send(t, tt.line)
			answer := s.readMessage(t, 15*time.Second)
			failure, ok := answer["error"].(map[string]any)
			if !ok {
				t.Fatalf("the malformed line was not answered with a JSON-RPC error: %v", answer)
			}
			if code, _ := failure["code"].(float64); code != tt.wantCode {
				t.Errorf("error code = %v, want %v: %v", failure["code"], tt.wantCode, answer)
			}

			// The session, not just the answer. A server that replied and then
			// died would satisfy everything above.
			if got := s.call(t, request(3, "tools/list", "")); got["error"] != nil {
				t.Fatalf("the session did not survive the malformed line: %v", got["error"])
			}
			if !s.alive() {
				t.Errorf("the server exited after one malformed line:\n%s", s.stderrText())
			}
		})
	}
}

// TestTransportAuto_WithAPipeOnStdin_SpeaksStdio pins the half of the transport
// inference every MCP client depends on, against a real process.
//
// The shape under test is the shape that ships: an MCP client connects a pipe to
// file descriptor 0, which is exactly what exec.Cmd's StdinPipe gives this
// session, and `--transport auto` has to read that as "somebody is speaking to
// me". Getting it wrong is not a visible failure — it is an HTTP listener nobody
// asked for and a client that waits at initialize forever with no output at all.
//
// Both halves are asserted, because either alone would pass while the feature
// was broken: the log line says what was inferred, and the JSON-RPC answers
// coming back down stdout say the server went on to serve it. An HTTP listener
// answers nothing here whatever it logged.
func TestTransportAuto_WithAPipeOnStdin_SpeaksStdio(t *testing.T) {
	s := startSessionWithArgs(t, baseEnv(t, startMirror(t)), "--transport", "auto")

	for _, tc := range []struct {
		name    string
		request string
	}{
		{name: "initialize", request: initializeRequest(1)},
		{name: "tools/list", request: request(2, "tools/list", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := s.call(t, tc.request)
			if got["error"] != nil {
				t.Fatalf("%s failed: %v", tc.name, got["error"])
			}
			if got["jsonrpc"] != "2.0" {
				t.Errorf("%s was not answered with JSON-RPC 2.0: %v", tc.name, got)
			}
		})
	}

	logs := s.waitForStderr(t, "transport inferred from stdin", 10*time.Second)
	for _, want := range []string{`"transport":"stdio"`, "stdin is a pipe"} {
		if !strings.Contains(logs, want) {
			t.Errorf("the inference was not logged as %s:\n%s", want, logs)
		}
	}
}

// TestTerminalGuidance_IsNotPrintedToAClient is the other side of the message
// this step adds.
//
// A word of explanation for somebody who started the server from a shell is
// worth having; the same words arriving in a client's log on every session, or
// worse on its stdout, are not. A client connects a pipe, so it must see
// neither.
func TestTerminalGuidance_IsNotPrintedToAClient(t *testing.T) {
	s := startSession(t, baseEnv(t, startMirror(t)))
	if got := s.call(t, initializeRequest(1)); got["error"] != nil {
		t.Fatalf("initialize failed: %v", got["error"])
	}

	// Anchored on a line the server always writes, so this is an absence in what
	// was logged rather than in what the harness had copied so far.
	logs := s.waitForStderr(t, startupLine, 10*time.Second)
	if strings.Contains(logs, "Model Context Protocol server, not an interactive program") {
		t.Errorf("the terminal guidance was printed to a client with a pipe on stdin:\n%s", logs)
	}
}

// TestTransportStdio_WithAnAddressServesStdioAndListensNowhere is the other
// direction of the resolution --transport introduces.
//
// The selector decides the transport and --http supplies an address, so the two
// together can ask for something contradictory. What must not happen is the
// half-and-half: a stdio session served AND a port quietly opened, which would
// be a listener nobody knows is there on a deployment that believes it is
// speaking over pipes.
//
// The listener is checked by dialing rather than by reading a log line: a server
// that logged "serving stdio" and bound the port anyway would satisfy any
// message assertion, and the socket is the thing that matters.
func TestTransportStdio_WithAnAddressServesStdioAndListensNowhere(t *testing.T) {
	addr := freeLoopbackAddr(t)
	s := startSessionWithArgs(t, baseEnv(t, startMirror(t)), "--transport", "stdio", "--http", addr)

	if got := s.call(t, initializeRequest(1)); got["error"] != nil {
		t.Fatalf("the stdio session was not served: %v", got["error"])
	}

	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(t.Context(), "tcp", addr)
	if err == nil {
		_ = conn.Close()
		t.Errorf("%s accepted a connection, so --transport stdio still opened a listener", addr)
	}

	// And the operator is told, because an address that does nothing is the
	// hardest kind of thing to notice is missing.
	logs := s.waitForStderr(t, "was given but this process is serving stdio", 10*time.Second)
	if !strings.Contains(logs, addr) {
		t.Errorf("the warning does not name the address that was dropped:\n%s", logs)
	}
}

// freeLoopbackAddr returns a loopback address nothing is listening on.
//
// The port is taken and released, which leaves a window in which something else
// could claim it. That is acceptable here and nowhere else in this module: the
// assertion is that the SERVER did not bind it, and a foreign listener would
// make the case fail rather than pass — the direction a flake has to fall.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := l.Addr().String()
	if closeErr := l.Close(); closeErr != nil {
		t.Fatalf("releasing the reserved port: %v", closeErr)
	}
	return addr
}
