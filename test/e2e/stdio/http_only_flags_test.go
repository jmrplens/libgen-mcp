//go:build stdioe2e

// http_only_flags_test.go drives a stdio server given flags only the HTTP
// transport reads: it names each one on stderr, at the severity the way it was
// given earns, keeps stdout to JSON-RPC, and serves anyway.

package stdioe2e

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The two lines cmd/server writes about an ignored HTTP-only flag, spelled here
// because this module cannot import package main.
const (
	stdioIgnoredFlagLine     = "this flag is read by the HTTP transport only, so it has no effect on a stdio server"
	stdioIgnoredVariableLine = "this variable configures the HTTP transport only, so it has no effect on a stdio server"
)

// ignoredFlagRecords starts nothing and asks nothing of the server: it reads the
// records naming ignored flags out of stderr, keyed by the flag each names.
//
// It anchors on the "serving on stdio" banner, which serveStdio writes after the
// transport decision has been explained. Anchoring on a line written before the
// records under test would return a buffer they had not reached yet, and the
// absence assertions a caller makes would pass for the wrong reason.
func ignoredFlagRecords(t *testing.T, s *session) map[string]map[string]any {
	t.Helper()
	text := s.waitForStderr(t, startupLine, 10*time.Second)
	records := map[string]map[string]any{}
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
		if msg, _ := record["msg"].(string); msg != stdioIgnoredFlagLine && msg != stdioIgnoredVariableLine {
			continue
		}
		flagName, _ := record["flag"].(string)
		records[flagName] = record
	}
	return records
}

// servesOverCleanStdout drives the handshake and a catalog listing, which is
// what an MCP client does first. readMessage fails the test on any stdout line
// that is not JSON, so reaching the end is stdout having carried nothing else.
func servesOverCleanStdout(t *testing.T, s *session) {
	t.Helper()
	for _, tc := range []struct {
		name    string
		request string
	}{
		{name: "initialize", request: initializeRequest(1)},
		{name: "tools/list", request: request(2, "tools/list", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := s.call(t, tc.request)
			if got["jsonrpc"] != "2.0" || got["error"] != nil {
				t.Errorf("%s was not served as JSON-RPC 2.0: %v", tc.name, got)
			}
		})
	}
}

// expectIgnoredFlag checks one record against the fields it must carry.
func expectIgnoredFlag(t *testing.T, records map[string]map[string]any, flagName string, want map[string]string) {
	t.Helper()
	record, found := records[flagName]
	if !found {
		t.Errorf("no record names %s as ignored; records naming flags: %v", flagName, records)
		return
	}
	for key, value := range want {
		if got, _ := record[key].(string); got != value {
			t.Errorf("%s: %s = %q, want %q (record %v)", flagName, key, got, value, record)
		}
	}
}

// TestHTTPOnlyFlags_OnAChosenStdio_AreNamedAndIgnored is the case the change
// opened with: a stdio user who passes --rate-limit-rps expecting a limiter, and
// a dotenv file shared with an HTTP deployment that carries its origin list.
//
// The typed flags are named at WARN, since the operator chose stdio and asked
// for something it does not do. The variable is named at INFO, since the
// environment is ambient rather than typed for this run. The server serves
// either way, over a stdout that carries nothing but JSON-RPC.
func TestHTTPOnlyFlags_OnAChosenStdio_AreNamedAndIgnored(t *testing.T) {
	env := baseEnv(t, startMirror(t))
	env["LIBGEN_MCP_TRUSTED_ORIGINS"] = "https://claude.ai"
	s := startSessionWithArgs(t, env, "--transport", "stdio", "--rate-limit-rps=0.001", "--rate-limit-burst=1")

	servesOverCleanStdout(t, s)

	records := ignoredFlagRecords(t, s)
	expectIgnoredFlag(t, records, "--rate-limit-rps", map[string]string{
		"level": "WARN", "msg": stdioIgnoredFlagLine, "source": "command line", "variable": "LIBGEN_MCP_RATE_LIMIT_RPS",
	})
	expectIgnoredFlag(t, records, "--rate-limit-burst", map[string]string{
		"level": "WARN", "msg": stdioIgnoredFlagLine, "source": "command line", "variable": "LIBGEN_MCP_RATE_LIMIT_BURST",
	})
	expectIgnoredFlag(t, records, "--trusted-origins", map[string]string{
		"level": "INFO", "msg": stdioIgnoredVariableLine, "source": "environment", "variable": "LIBGEN_MCP_TRUSTED_ORIGINS",
	})
	if len(records) != 3 {
		t.Errorf("%d flags named as ignored, want the 3 given: %v", len(records), records)
	}
}

// TestHTTPOnlyFlags_UnderTransportAuto_AreNamedAtInfo starts the binary with the
// container image's own command plus a response-format flag, over the pipe every
// MCP client connects, which is what `docker run -i` of the image does.
//
// That command line was written for either transport, and stdio has no
// response format, so the flag is expected there rather than a mistake: it is
// named at INFO, never missing. --max-request-body-bytes is the one HTTP flag
// stdio has a counterpart for, a setting plausibly meant for both, so it stays
// at WARN and names the variable to set instead.
func TestHTTPOnlyFlags_UnderTransportAuto_AreNamedAtInfo(t *testing.T) {
	s := startSessionWithArgs(t, baseEnv(t, startMirror(t)),
		"--transport", "auto", "--http", "0.0.0.0:8080", "--json-response", "--max-request-body-bytes=1048576")

	servesOverCleanStdout(t, s)

	records := ignoredFlagRecords(t, s)
	expectIgnoredFlag(t, records, "--json-response", map[string]string{
		"level": "INFO", "msg": stdioIgnoredFlagLine, "source": "command line", "variable": "LIBGEN_MCP_JSON_RESPONSE",
	})
	expectIgnoredFlag(t, records, "--max-request-body-bytes", map[string]string{
		"level": "WARN", "msg": stdioIgnoredFlagLine, "on_stdio_set": "LIBGEN_MCP_STDIO_MAX_LINE_BYTES",
	})
	if len(records) != 2 {
		t.Errorf("%d flags named as ignored, want the 2 given: %v", len(records), records)
	}
}

// TestHTTPOnlyFlags_NoneGiven_NothingIsNamed keeps the ordinary stdio start
// quiet: a line naming nothing on every start is one an operator learns to skip.
func TestHTTPOnlyFlags_NoneGiven_NothingIsNamed(t *testing.T) {
	s := startSession(t, baseEnv(t, startMirror(t)))

	servesOverCleanStdout(t, s)

	if records := ignoredFlagRecords(t, s); len(records) != 0 {
		t.Errorf("a stdio start given no HTTP flag named some as ignored: %v", records)
	}
}
