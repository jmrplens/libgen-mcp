// Tests for start.go: a start that does not happen ends with its reason at ERROR.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jmrplens/libgen-mcp/v2/internal/logging"
)

// captureRecords sends every log record to a buffer for the rest of the test,
// and puts the default logger and the stream back afterwards.
func captureRecords(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	restore := logging.SetDestination(&buf)
	logging.Setup(slog.LevelInfo)
	t.Cleanup(func() {
		restore()
		slog.SetDefault(previous)
	})
	return &buf
}

// lastRecord parses every line of buf as a JSON record and returns the last.
func lastRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	var record map[string]any
	for _, line := range lines {
		record = nil
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("a log line is not a JSON record: %q (%v)\nall records:\n%s", line, err, buf.String())
		}
	}
	if record == nil {
		t.Fatalf("no record was written")
	}
	return record
}

// TestRefuseStart pins the one place a refused start is reported: the status it
// is given comes back unchanged, and the reason is a single JSON record at ERROR
// carrying the error's own text.
func TestRefuseStart(t *testing.T) {
	cases := []struct {
		name string
		code int
		err  error
	}{
		{name: "a refused configuration", code: exitRefused, err: errors.New(`--transport "bogus" is not one of stdio, http, auto`)},
		{name: "a refused command line", code: exitUsage, err: errors.New("flag provided but not defined: -bogus")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureRecords(t)
			if got := refuseStart(tc.code, tc.err); got != tc.code {
				t.Errorf("refuseStart(%d, …) = %d, want %d", tc.code, got, tc.code)
			}
			if n := strings.Count(strings.TrimSpace(buf.String()), "\n") + 1; n != 1 {
				t.Errorf("refuseStart wrote %d records, want 1:\n%s", n, buf.String())
			}
			record := lastRecord(t, buf)
			if record["level"] != "ERROR" {
				t.Errorf("level = %v, want ERROR", record["level"])
			}
			if record["msg"] != tc.err.Error() {
				t.Errorf("msg = %v, want the error's own text %q", record["msg"], tc.err.Error())
			}
		})
	}
}

// TestRefuseStart_InstallsTheJSONHandler covers a refusal made before anything
// else installed the handler, which is where a flag the parser refuses is
// reported: the default logger then writes plain text through the log package,
// and the record must still be JSON at ERROR.
func TestRefuseStart_InstallsTheJSONHandler(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	restore := logging.SetDestination(&buf)
	t.Cleanup(func() {
		restore()
		slog.SetDefault(previous)
	})
	var text bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&text, nil)))

	refuseStart(exitUsage, errors.New("flag provided but not defined: -bogus"))

	if text.Len() != 0 {
		t.Errorf("the refusal went through the handler it found: %q", text.String())
	}
	if record := lastRecord(t, &buf); record["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", record["level"])
	}
}

// TestParseServerFlags pins the statuses Go's flag package would exit with, and
// that only a refused command line is reported: -h is a request, not a failure.
func TestParseServerFlags(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantCode    int
		wantProceed bool
		wantRecord  string
	}{
		{name: "a command line it reads", args: []string{"--known=x"}, wantCode: 0, wantProceed: true},
		{name: "a request for the usage", args: []string{"-h"}, wantCode: 0, wantProceed: false},
		{name: "a flag nobody defined", args: []string{"--bogus"}, wantCode: exitUsage, wantProceed: false, wantRecord: "flag provided but not defined: -bogus"},
		{name: "a value the flag cannot hold", args: []string{"--count=many"}, wantCode: exitUsage, wantProceed: false, wantRecord: `invalid value "many" for flag -count`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldArgs, oldFlags := os.Args, flag.CommandLine
			t.Cleanup(func() { os.Args, flag.CommandLine = oldArgs, oldFlags })
			os.Args = append([]string{"libgen-mcp"}, tc.args...)
			flag.CommandLine = flag.NewFlagSet("libgen-mcp", flag.ContinueOnError)
			flag.CommandLine.SetOutput(io.Discard)
			flag.String("known", "", "a flag that exists")
			flag.Int("count", 0, "a flag holding a number")
			buf := captureRecords(t)

			code, proceed := parseServerFlags()
			if code != tc.wantCode || proceed != tc.wantProceed {
				t.Errorf("parseServerFlags() = (%d, %v), want (%d, %v)", code, proceed, tc.wantCode, tc.wantProceed)
			}
			if tc.wantRecord == "" {
				if buf.Len() != 0 {
					t.Errorf("logged %q, want nothing", buf.String())
				}
				return
			}
			record := lastRecord(t, buf)
			if record["level"] != "ERROR" || !strings.HasPrefix(record["msg"].(string), tc.wantRecord) {
				t.Errorf("last record = %v, want ERROR starting %q", record, tc.wantRecord)
			}
		})
	}
}

// TestMainWithExit_ARefusedStartEndsWithItsReasonAtError drives the refusals
// planStart makes through mainWithExit, one per kind of check, and holds each
// to the exit status it always had and to a last record at ERROR naming it.
//
// Before every refusal went through refuseStart, each of these was written with
// log.Print after the JSON handler was installed, so the reason a server would
// not start was the last record and was at INFO.
func TestMainWithExit_ARefusedStartEndsWithItsReasonAtError(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		env      map[string]string
		wantCode int
		wantMsg  string
	}{
		{name: "an unknown transport", args: []string{"--transport", "bogus"}, wantCode: 1, wantMsg: `--transport "bogus"`},
		{name: "a negative body cap", args: []string{"--max-request-body-bytes", "-1"}, wantCode: 1, wantMsg: "--max-request-body-bytes must be >= 0"},
		{name: "half a TLS pair", args: []string{"--tls-cert", "cert.pem"}, wantCode: 1, wantMsg: "--tls-cert was given without --tls-key"},
		{name: "a socket mode on an address", args: []string{"--http", "127.0.0.1:0", "--http-socket-mode", "0600"}, wantCode: 1, wantMsg: "--http-socket-mode"},
		{name: "a header nobody is trusted to set", args: []string{"--http", "127.0.0.1:0", "--trusted-proxy-header", realIP}, wantCode: 1, wantMsg: "--trusted-proxy-header"},
		{name: "a public URL with no host", args: []string{"--http", "127.0.0.1:0", "--public-url", "ftp://x"}, wantCode: 1, wantMsg: "--public-url"},
		{name: "a rate limit on loopback", args: []string{"--http", "127.0.0.1:0", "--rate-limit-rps", "10"}, wantCode: 1, wantMsg: "--rate-limit-rps"},
		{name: "a drain delay past the cap", args: []string{"--drain-delay", "1h"}, wantCode: 1, wantMsg: "--drain-delay 1h0m0s"},
		{name: "a session timeout without sessions", args: []string{"--session-timeout", "1m"}, wantCode: 1, wantMsg: "--session-timeout"},
		{name: "a path with a query", args: []string{"--http-path", "/a?b"}, wantCode: 1, wantMsg: `--http-path "/a?b"`},
		{name: "a malformed origin", args: []string{"--trusted-origins", "not an origin"}, wantCode: 1, wantMsg: "not an origin"},
		{name: "an HTTP variable that does not parse", env: map[string]string{"LIBGEN_MCP_RATE_LIMIT_RPS": "abc"}, wantCode: 1, wantMsg: "LIBGEN_MCP_RATE_LIMIT_RPS"},
		{name: "a flag nobody defined", args: []string{"--bogus"}, wantCode: 2, wantMsg: "flag provided but not defined: -bogus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			for name, value := range tc.env {
				t.Setenv(name, value)
			}
			buf := captureRecords(t)

			var code int
			awaitReturn(t, func() {
				code = callMainWithExitQuiet(t, append([]string{"libgen-mcp"}, tc.args...)...)
			})
			if code != tc.wantCode {
				t.Errorf("mainWithExit(%q) = %d, want %d", tc.args, code, tc.wantCode)
			}
			record := lastRecord(t, buf)
			if record["level"] != "ERROR" {
				t.Errorf("the last record is at %v, want ERROR: %v", record["level"], record)
			}
			if msg, _ := record["msg"].(string); !strings.Contains(msg, tc.wantMsg) {
				t.Errorf("the last record says %q, want the reason, containing %q", msg, tc.wantMsg)
			}
		})
	}
}

// callMainWithExitQuiet is callMainWithExit with the flag parser's usage
// output discarded, so a refused command line does not print it into the test
// log.
func callMainWithExitQuiet(t *testing.T, args ...string) int {
	t.Helper()
	oldArgs := os.Args
	oldFlags := flag.CommandLine
	t.Cleanup(func() {
		os.Args = oldArgs
		flag.CommandLine = oldFlags
	})
	os.Args = args
	flag.CommandLine = flag.NewFlagSet(args[0], flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	return mainWithExit()
}
