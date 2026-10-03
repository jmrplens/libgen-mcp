//go:build httpe2e

package httpe2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// shadowedVariableLine is the message cmd/server logs for a variable a typed
// flag overrode, spelled here because package main cannot be imported.
const shadowedVariableLine = "a flag on the command line overrides an environment variable, which has no effect"

// TestATypedFlagThatOverridesAVariableIsNamed is the container image's case:
// its command carries --http, the deployment sets LIBGEN_MCP_HTTP_ADDR, and the
// flag wins. The precedence is by design and stays, so what is asserted is
// both halves — the server listens where the flag says and not where the
// variable says, and the log says the variable was read and overruled.
func TestATypedFlagThatOverridesAVariableIsNamed(t *testing.T) {
	flagAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	envAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))

	s := launchServer(t, "http://"+flagAddr, nil,
		map[string]string{"LIBGEN_MCP_HTTP_ADDR": envAddr},
		[]string{"--http", flagAddr})

	// launchServer returned once /health answered at flagAddr, which is the
	// first half. The variable's address must be closed.
	dialer := net.Dialer{Timeout: time.Second}
	if conn, err := dialer.DialContext(context.Background(), "tcp", envAddr); err == nil {
		_ = conn.Close()
		t.Fatalf("something answers at %s, the address the overridden variable named", envAddr)
	}

	record := awaitShadowRecord(t, s, 5*time.Second)
	for key, want := range map[string]string{
		"level":          "WARN",
		"variable":       "LIBGEN_MCP_HTTP_ADDR",
		"variable_value": envAddr,
		"flag":           "--http",
		"flag_value":     flagAddr,
	} {
		t.Run(key, func(t *testing.T) {
			if got, _ := record[key].(string); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		})
	}
	if n := strings.Count(s.logs(), shadowedVariableLine); n != 1 {
		t.Errorf("the warning was written %d times, want once:\n%s", n, s.logs())
	}
}

// TestAVariableTheFlagAgreesWithIsNotNamed is the quiet half: the same address
// in both places is not a conflict, and a warning there would teach the reader
// to skip it.
func TestAVariableTheFlagAgreesWithIsNotNamed(t *testing.T) {
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))

	s := launchServer(t, "http://"+addr, nil,
		map[string]string{"LIBGEN_MCP_HTTP_ADDR": addr},
		[]string{"--http", addr})

	if strings.Contains(s.logs(), shadowedVariableLine) {
		t.Errorf("a variable that agrees with its flag was reported as overridden:\n%s", s.logs())
	}
}

// awaitShadowRecord returns the decoded warning once it appears in the
// server's output. It is polled because the output is copied on a goroutine
// with no ordering against the health answer the harness waited for.
func awaitShadowRecord(t *testing.T, s *server, within time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		for line := range strings.SplitSeq(s.logs(), "\n") {
			if !strings.Contains(line, shadowedVariableLine) {
				continue
			}
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatalf("decoding %q: %v", line, err)
			}
			return record
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %q record within %s. Output:\n%s", shadowedVariableLine, within, s.logs())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
