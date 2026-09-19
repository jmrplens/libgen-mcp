package main

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/logging"
)

// sdkStream points the process logger at a buffer and returns the SDK's logger
// beside a reader of what was written.
//
// The records come back parsed rather than as text, and that is the whole point
// of the harness: the defect this file exists for is two members named `level`
// in one JSON object, which a substring assertion cannot see at all — both
// spellings are present either way.
func sdkStream(t *testing.T, level slog.Level) (*slog.Logger, func() []map[string]any) {
	t.Helper()

	stream := &syncBuffer{}
	t.Cleanup(logging.SetDestination(stream))
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	logging.Setup(level)

	return sdkLogger(), func() []map[string]any {
		var records []map[string]any
		for line := range strings.SplitSeq(strings.TrimSpace(stream.String()), "\n") {
			if line == "" {
				continue
			}
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatalf("the log stream is not JSON, which every client depends on: %q: %v", line, err)
			}
			records = append(records, record)
		}
		return records
	}
}

// sdkRecord returns the first record with this message.
func sdkRecord(records []map[string]any, message string) (map[string]any, bool) {
	for _, record := range records {
		if record["msg"] == message {
			return record, true
		}
	}
	return nil, false
}

// sdkGroup returns a record's nested SDK attributes.
func sdkGroup(t *testing.T, record map[string]any) map[string]any {
	t.Helper()

	group, ok := record[sdkLogGroup].(map[string]any)
	if !ok {
		t.Fatalf("the record carries no %q group, so the SDK's attributes are at the top level: %v", sdkLogGroup, record)
	}
	return group
}

// TestTheSDKsPerSessionChatterIsDebugOnly drives the real SDK, because the only
// thing that makes the demotion work is the message text matching what the SDK
// actually writes.
//
// A hand-written record would pass against any list, including one copied from a
// different version of the SDK. Connecting a session emits the real messages, so
// a rename upstream fails here rather than quietly restoring the flood: on a
// stateless deployment every POST is a session, and these lines are otherwise the
// whole of the default stream.
func TestTheSDKsPerSessionChatterIsDebugOnly(t *testing.T) {
	t.Run("silent at info", func(t *testing.T) {
		_, records := sdkStream(t, slog.LevelInfo)
		runOneSession(t)

		for _, record := range records() {
			if sdkSessionChatter[record["msg"].(string)] {
				t.Errorf("per-session chatter reached an operator's default stream: %v", record)
			}
		}
	})

	t.Run("present at debug", func(t *testing.T) {
		_, records := sdkStream(t, slog.LevelDebug)
		runOneSession(t)

		got := records()
		// The three a session produces on this transport, plus the one that
		// carries the colliding attribute. The rest of [sdkSessionChatter]
		// belongs to flows driven elsewhere in this file or to the stdio Run
		// loop; what matters here is that the names are checked against the SDK
		// rather than against a list copied from another version of it.
		for _, message := range []string{
			"server connecting",
			"server session connected",
			"server session disconnected",
			"client log level set",
		} {
			t.Run(message, func(t *testing.T) {
				record, found := sdkRecord(got, message)
				if !found {
					t.Errorf("%q was not written at debug, so the SDK's session records are lost rather than demoted", message)
					return
				}
				if record["level"] != "DEBUG" {
					t.Errorf("%q is at %v, want DEBUG", message, record["level"])
				}
			})
		}
	})
}

// runOneSession connects a client to this server's real MCP server and closes
// it, which is what produces the SDK's per-session records.
func runOneSession(t *testing.T) {
	t.Helper()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := newCardTestServer().Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "sdk-log-probe", Version: "0"}, nil)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	// The request that produces "client log level set", which is the record
	// carrying an attribute named `level` — the collision this wrapper exists
	// for, driven here rather than hand-written so the message and the attribute
	// name are the SDK's own.
	//nolint:staticcheck // SA1019: the method is deprecated (SEP-2577) and still
	// served during the deprecation window, which is the point: a client on an
	// older revision still sends it, so the record it writes — the one carrying
	// an attribute named `level` — still arrives here.
	if levelErr := session.SetLoggingLevel(t.Context(), &mcp.SetLoggingLevelParams{Level: "warning"}); levelErr != nil {
		t.Fatalf("set logging level: %v", levelErr)
	}
	if closeErr := session.Close(); closeErr != nil {
		t.Fatalf("client close: %v", closeErr)
	}
	if closeErr := serverSession.Close(); closeErr != nil {
		t.Fatalf("server close: %v", closeErr)
	}
	// Waited rather than assumed. The SDK handles a session's messages on its
	// own goroutines, so a test that read the stream straight after Close would
	// be racing them — and the race is invisible in the direction that matters,
	// since a case asserting that a record is *absent* passes while it is merely
	// late.
	if waitErr := serverSession.Wait(); waitErr != nil {
		t.Fatalf("server session ended with %v", waitErr)
	}
}

// TestAnSDKAttributeCannotOverwriteTheSeverity is the collision this wrapper
// exists for.
//
// "client log level set" carries an attribute named `level`, which is also what
// the JSON handler calls the record's own severity. Neither side deduplicates,
// so the line goes out with two `level` members and a parser keeping the last
// one reads the severity as the client's chosen string — or, when that is empty,
// as no severity at all.
func TestAnSDKAttributeCannotOverwriteTheSeverity(t *testing.T) {
	t.Run("the SDK's own record, driven through a session", func(t *testing.T) {
		_, records := sdkStream(t, slog.LevelDebug)
		runOneSession(t)

		record, found := sdkRecord(records(), "client log level set")
		if !found {
			t.Fatalf("the record was not written: %v", records())
		}
		// One `level` member, and it is the severity. Before the group, the
		// object carried two and a parser keeping the last read the severity as
		// "warning" — a value that is not a severity at all.
		if record["level"] != "DEBUG" {
			t.Errorf("severity = %v, want DEBUG: the client's chosen level overwrote the record's own", record["level"])
		}
		if got := sdkGroup(t, record)["level"]; got != "warning" {
			t.Errorf("the client's level = %v, want it kept under the group", got)
		}
	})

	t.Run("a record that is not demoted", func(t *testing.T) {
		logger, records := sdkStream(t, slog.LevelDebug)

		logger.Warn("keepalive ping failed; closing session", "level", "notice")

		record, found := sdkRecord(records(), "keepalive ping failed; closing session")
		if !found {
			t.Fatalf("the record was not written: %v", records())
		}
		// The demotion path rebuilds the record, so it would nest the
		// attributes even without the group. This one takes the other branch.
		if record["level"] != "WARN" {
			t.Errorf("severity = %v, want WARN: an attribute overwrote the record's own level", record["level"])
		}
		if got := sdkGroup(t, record)["level"]; got != "notice" {
			t.Errorf("the SDK's own level attribute = %v, want it kept under the group", got)
		}
	})
}

// TestANonChatterRecordKeepsItsLevel is the other half of the demotion, and the
// one that keeps it worth having.
//
// The wrapper exists to quiet a flood, not to quiet the SDK. A keepalive failure
// is the protocol layer reporting something an operator has to act on, and it
// arrives at the level the SDK chose.
func TestANonChatterRecordKeepsItsLevel(t *testing.T) {
	logger, records := sdkStream(t, slog.LevelInfo)

	logger.Warn("keepalive ping failed; closing session")
	logger.Error("jsonrpc2 internal error")

	got := records()
	for _, tc := range []struct{ message, level string }{
		{"keepalive ping failed; closing session", "WARN"},
		{"jsonrpc2 internal error", "ERROR"},
	} {
		t.Run(tc.message, func(t *testing.T) {
			record, found := sdkRecord(got, tc.message)
			if !found {
				t.Errorf("%q was demoted out of an operator's default stream", tc.message)
				return
			}
			if record["level"] != tc.level {
				t.Errorf("%q is at %v, want %s", tc.message, record["level"], tc.level)
			}
		})
	}
}

// TestTheSDKsOrdinaryStopIsNotAnError covers the line every clean shutdown
// produces.
//
// The SDK reports a canceled run at ERROR, and the only context this process
// cancels is the one a signal builds — so every ordinary stop would end with an
// error record describing the stop somebody asked for. It is driven through the
// real Run for the same reason as the chatter: the message text is the whole
// mechanism, and this repository cannot even spell it the way the SDK does.
func TestTheSDKsOrdinaryStopIsNotAnError(t *testing.T) {
	_, records := sdkStream(t, slog.LevelDebug)

	serverTransport, _ := mcp.NewInMemoryTransports()
	if err := newCardTestServer().Run(canceledContext(), serverTransport); err == nil {
		t.Fatal("Run returned no error for a canceled context, so this test drove nothing")
	}

	got := records()
	for _, record := range got {
		if record["level"] == "ERROR" {
			t.Errorf("an ordinary stop produced an error record: %v", record)
		}
	}
	if record, found := sdkRecord(got, sdkRunCanceled); !found {
		t.Errorf("the SDK no longer writes %q; the demotion now matches nothing: %v", sdkRunCanceled, got)
	} else if record["level"] != "DEBUG" {
		t.Errorf("%q is at %v, want DEBUG", sdkRunCanceled, record["level"])
	}
}

// TestADerivedSDKLoggerKeepsItsAttributesAndItsGroup covers what a component
// logger built with slog.With produces.
//
// The SDK builds them, and a handler that dropped either would lose exactly the
// context that makes a record worth reading — which session, which request.
func TestADerivedSDKLoggerKeepsItsAttributesAndItsGroup(t *testing.T) {
	logger, records := sdkStream(t, slog.LevelDebug)

	logger.With("session_id", "abc").WithGroup("request").Info("jsonrpc2 internal error", "method", "tools/call")

	record, found := sdkRecord(records(), "jsonrpc2 internal error")
	if !found {
		t.Fatalf("the record was not written: %v", records())
	}
	group := sdkGroup(t, record)
	if group["session_id"] != "abc" {
		t.Errorf("an attached attribute was lost: %v", group)
	}
	nested, ok := group["request"].(map[string]any)
	if !ok || nested["method"] != "tools/call" {
		t.Errorf("a group the SDK opened was lost: %v", group)
	}
}

// TestTheSDKLoggerFollowsTheProcessLoggerInstalledLater is what keeps the wiring
// order from being load-bearing.
//
// The telemetry bridge replaces the default logger at startup. A handler
// captured when the server was built would carry whatever was installed at that
// moment, so moving the server construction one line above the telemetry wiring
// would silently stop the SDK's records reaching the collector while everything
// else kept going — a defect with no symptom on the machine that made it.
func TestTheSDKLoggerFollowsTheProcessLoggerInstalledLater(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	// The logger is built against one handler and then the default is replaced,
	// which is the shape of the startup: logging.Setup, then the server, then —
	// if this moved — the telemetry bridge.
	//
	// Both halves are real handlers. A test that left the standard library's
	// own default in place could not fail: slog.SetDefault also redirects the
	// log package, which that handler writes through, so a captured copy of it
	// still reaches whatever was installed later.
	atBuildTime := &syncBuffer{}
	t.Cleanup(logging.SetDestination(atBuildTime))
	logging.Setup(slog.LevelDebug)
	logger := sdkLogger()

	afterwards := &syncBuffer{}
	t.Cleanup(logging.SetDestination(afterwards))
	logging.Setup(slog.LevelDebug)

	logger.Info("jsonrpc2 internal error")

	if !strings.Contains(afterwards.String(), "jsonrpc2 internal error") {
		t.Errorf("stream = %q, want the record: the SDK's logger is pinned to the handler it was built with", afterwards.String())
	}
	if strings.Contains(atBuildTime.String(), "jsonrpc2 internal error") {
		t.Error("the record went to the handler installed when the logger was built")
	}
}
