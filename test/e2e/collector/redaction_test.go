//go:build collectore2e

package collectore2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRedaction_NothingSensitiveSurvivesIntoADecodedDocument asks the question
// the stub cannot.
//
// test/e2e/http searches raw payload bytes, which proves a value was not on the
// wire. This reads what a receiver actually decoded and re-encoded — the form a
// backend stores, indexes and shows — so a value that survived protobuf but
// arrived somewhere unexpected in the pipeline is visible here and nowhere else.
func TestRedaction_NothingSensitiveSurvivesIntoADecodedDocument(t *testing.T) {
	const (
		query  = "collector-planted-query-3e7b"
		secret = "collector-planted-key-not-a-credential-3e7b"
	)

	c := startCollector(t)
	s := startServer(t, c, map[string]string{
		"LIBGEN_MCP_ANNAS_KEY":          secret,
		"LIBGEN_MCP_TELEMETRY_IDENTITY": "full",
	})

	s.call(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"query":"`+query+`"}}}`)

	// Waited for, so the assertion is about documents that exist.
	if _, _, ok := c.awaitSpan(t, 60*time.Second, func(otlpResourceSpans, otlpSpan) bool { return true }); !ok {
		t.Fatalf("nothing was parsed, so nothing was checked:\n%s", c.containerLogs(t))
	}
	// A moment for the other two pipelines, which flush on their own schedule:
	// the point is to read everything the collector wrote, not only the first
	// file to appear.
	time.Sleep(2 * time.Second)

	for _, file := range []string{tracesFile, metricsFile, logsFile} {
		raw, err := os.ReadFile(filepath.Join(c.outDir, file)) //#nosec G304 -- a path this package built in its own temp dir
		if err != nil {
			continue
		}
		for _, forbidden := range []string{query, secret} {
			if strings.Contains(string(raw), forbidden) {
				t.Errorf("%q survived into %s, which is the form a backend stores", forbidden, file)
			}
		}
	}
}
