package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
)

// unpaywallModelLocalPart is the local part of the address the test's client
// answers the elicitation prompt with. It appears nowhere else in the tree, and it
// is the local part rather than the whole address because the endpoint builds its
// query with url.QueryEscape: the "@" arrives as "%40", so a search for the
// address as written would find nothing whether or not the address leaked.
const (
	unpaywallModelLocalPart = "m41lCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	unpaywallModelSentinel  = unpaywallModelLocalPart + "@example.test"
)

// TestDownloadFailureDocumentDoesNotLeakTheElicitedSecret drives the whole tool
// call and reads what the model is handed, which is the sink the source-level tests
// in internal/libgen cannot see.
//
// Nothing shorter discriminates. downloadFailure performs no redaction of its own —
// it fences the error text and writes it — so a test that hands it a hand-built
// error passes before the fix and after it whichever error it hands over. What has
// to be exercised is the whole path: a caller answers the prompt, the secret
// reaches the endpoint's query string, the endpoint is unreachable, and net/http
// reports the failure as a *url.Error carrying the whole URL.
//
// The secret arrives the way a real caller's does, through the elicitation prompt,
// which is the door that stays open to anyone on the hosted endpoint. Unpaywall is
// the per-call secret this can be driven with offline: its base URL is a
// constructor option, so the request can be aimed at a closed local port. The
// Anna's key takes the identical path through the identical helper, and its own
// source and log legs are pinned in internal/libgen.
func TestDownloadFailureDocumentDoesNotLeakTheElicitedSecret(t *testing.T) {
	cfg := &config.Config{
		DownloadDir:   t.TempDir(),
		Timeout:       2 * time.Second,
		RateRPS:       1000,
		RateBurst:     100,
		RetryAttempts: 1,
		// The chain is narrowed to the one source this drives, so no other source
		// reaches for a real host. The tool call itself must leave `source` unset:
		// elicitUnpaywallEmail refuses to ask a dead-end question when a source is
		// pinned, because the per-call email only takes effect for an unnamed one.
		Sources: []string{"unpaywall"},
	}
	// Port 1 is reserved and closed, so the dial is refused immediately and the
	// resulting *net.OpError names a host and a port but no query string.
	client := libgen.New(staticMirrors{"http://127.0.0.1:1"}, cfg,
		libgen.WithUnpaywallBaseURL("http://127.0.0.1:1"))

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	Register(server, client, cfg)

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"},
		// "email" is the schema field elicitUnpaywallEmail asks under;
		// "unpaywall_email" is the request's own id, not what the result is keyed by.
		&mcp.ClientOptions{ElicitationHandler: acceptHandler(map[string]any{"email": unpaywallModelSentinel})})
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "download",
		Arguments: map[string]any{
			"doi":          "10.1000/redaction",
			"resolve_only": true,
		},
	})
	if err != nil {
		t.Fatalf("the tool call itself failed, so no document was produced: %v", err)
	}
	if !res.IsError {
		t.Fatal("the only source points at a closed port, so the download must fail and render a failure document")
	}

	document := renderedText(t, res)
	if !strings.Contains(document, "Download failed") {
		t.Fatalf("this is not the failure document the test means to read:\n%s", document)
	}
	// The premise: without this the test would pass against any tree, because a
	// document that never reached Unpaywall carries no secret to leak.
	if !strings.Contains(document, "unpaywall") {
		t.Fatalf("the elicited email never reached the Unpaywall request, so this pins nothing:\n%s", document)
	}
	if strings.Contains(document, unpaywallModelLocalPart) {
		t.Errorf("the document handed to the model carries the caller's contact address:\n%s", document)
	}
}

// renderedText joins every text block of a tool result, which is what a client
// shows and therefore what the model reads.
func renderedText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}
