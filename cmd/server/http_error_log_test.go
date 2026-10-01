package main

import (
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/v2/internal/telemetry"
)

// TestHTTPServerErrorLogMovesTheLineOffTheMessage is the rewrite itself: the
// line net/http composed becomes a field on the strip list, and the message a
// constant the export leg may carry.
//
// Both halves are asserted, because the export leg does not rewrite messages:
// a line left as the message reaches the collector whatever the strip list
// says, and a line dropped altogether would take from the operator the one
// thing they read to debug a certificate.
func TestHTTPServerErrorLogMovesTheLineOffTheMessage(t *testing.T) {
	_, records := sdkStream(t, slog.LevelInfo)
	const line = "http: TLS handshake error from 198.51.100.23:40211: EOF"

	httpServerErrorLog().Print(line)

	record, found := sdkRecord(records(), httpServerErrorMessage)
	if !found {
		t.Fatalf("no record carries the constant message: %v", records())
	}
	if got := record[telemetry.LogFieldHTTPServerError]; got != line {
		t.Errorf("record[%q] = %v, want net/http's line whole on stderr", telemetry.LogFieldHTTPServerError, got)
	}
	if got := record["level"]; got != httpServerErrorLevel.String() {
		t.Errorf("level = %v, want %s: net/http calls this its error log", got, httpServerErrorLevel)
	}
	for _, other := range records() {
		if msg, _ := other["msg"].(string); strings.Contains(msg, "198.51.100.23") {
			t.Errorf("the peer's address is in a record's message, which the export leg carries verbatim: %v", other)
		}
	}
	if !telemetry.ExportStrippedFields[telemetry.LogFieldHTTPServerError] {
		t.Errorf("%q is not on the export strip list", telemetry.LogFieldHTTPServerError)
	}
}

// TestHTTPServerErrorLogKeepsWhatWasAttached checks the derived handlers carry
// what With and WithGroup attached, rather than dropping it on the way to the
// process logger.
func TestHTTPServerErrorLogKeepsWhatWasAttached(t *testing.T) {
	_, records := sdkStream(t, slog.LevelInfo)

	slog.New(newHTTPErrorLogHandler()).With("listener", "mcp").WithGroup("net").
		Warn("ignored", "detail", "kept")

	record, found := sdkRecord(records(), httpServerErrorMessage)
	if !found {
		t.Fatalf("no record carries the constant message: %v", records())
	}
	if record["listener"] != "mcp" {
		t.Errorf("an attribute attached with With was lost: %v", record)
	}
	group, ok := record["net"].(map[string]any)
	if !ok {
		t.Fatalf("the group attached with WithGroup was lost: %v", record)
	}
	if group[telemetry.LogFieldHTTPServerError] != "ignored" || group["detail"] != "kept" {
		t.Errorf("the grouped attributes = %v, want the original message and the record's own attribute", group)
	}
}

// TestAFailedHandshakeReachesTheLogUnderTheStrippedField drives net/http
// itself, because the line under test is one the standard library composes and
// a hand-written string proves nothing about what it actually writes.
//
// A peer that opens a connection to a TLS listener and sends something that is
// neither TLS nor HTTP is what every internet scanner does, and net/http logs
// it "from" the peer's address and port.
func TestAFailedHandshakeReachesTheLogUnderTheStrippedField(t *testing.T) {
	_, records := sdkStream(t, slog.LevelInfo)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.Config.ErrorLog = httpServerErrorLog()
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(t.Context(), "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	peer := conn.LocalAddr().String()
	// Not a TLS record, and not one of the five prefixes net/http recognizes
	// as plain HTTP and answers with a 400 instead of logging.
	if _, err = conn.Write([]byte("hello, neither TLS nor HTTP\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Read(make([]byte, 64))
	_ = conn.Close()

	var record map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for record == nil && time.Now().Before(deadline) {
		record, _ = sdkRecord(records(), httpServerErrorMessage)
		if record == nil {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if record == nil {
		t.Fatalf("net/http logged no handshake failure through the server's error log: %v", records())
	}
	line, _ := record[telemetry.LogFieldHTTPServerError].(string)
	if !strings.Contains(line, "TLS handshake error") || !strings.Contains(line, peer) {
		t.Errorf("record[%q] = %q, want net/http's handshake line naming the peer %s", telemetry.LogFieldHTTPServerError, line, peer)
	}
	if msg, _ := record["msg"].(string); strings.Contains(msg, peer) {
		t.Errorf("the peer %s is in the message, which the export leg carries verbatim: %q", peer, msg)
	}
}
