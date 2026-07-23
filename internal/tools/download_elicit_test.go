package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
)

// unpaywallElicitServer serves the Unpaywall lookup for the download-tool
// elicitation tests: it records how many lookups it received and the last email
// query parameter, and always replies with an open-access record. resolve_only never
// fetches the PDF, so the url_for_pdf value is a static placeholder.
func unpaywallElicitServer(t *testing.T) (base string, lookups *atomic.Int32, lastEmail *string) {
	t.Helper()
	var hits atomic.Int32
	var email string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		email = r.URL.Query().Get("email")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"is_oa":true,"best_oa_location":{"url_for_pdf":"https://cdn.example/oa.pdf"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &hits, &email
}

// newElicitDownloadSession registers the tools on a client (built with the given
// config and options) whose MCP client advertises elicitation via handler (nil = no
// capability, exercising the fallback path). It returns a live session for CallTool.
func newElicitDownloadSession(t *testing.T, cfg *config.Config, handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error), opts ...libgen.Option) *mcp.ClientSession {
	t.Helper()
	client := libgen.New(staticMirrors{}, cfg, opts...)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	Register(server, client, cfg)

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"},
		&mcp.ClientOptions{ElicitationHandler: handler})
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// elicitDownloadConfig is a config with NO Unpaywall email and only "unpaywall"
// enabled, so the default chain is empty and any Unpaywall resolution can only come
// from the on-demand per-call email path (never a live Sci-Hub call).
func elicitDownloadConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		DownloadDir:   t.TempDir(),
		Timeout:       5 * time.Second,
		RateRPS:       1000,
		RateBurst:     100,
		RetryAttempts: 1,
		Sources:       []string{"unpaywall"},
	}
}

// decodeDownloadOutput unmarshals a download tool result's structured content into
// the full DownloadOutput (including the resolve_only Resolved link).
func decodeDownloadOutput(t *testing.T, res *mcp.CallToolResult) DownloadOutput {
	t.Helper()
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out DownloadOutput
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatal(uerr)
	}
	return out
}

// TestDownloadTool_ElicitsUnpaywallEmailOnAccept verifies the on-demand email flow:
// with no configured Unpaywall email and a client that accepts the elicitation with
// an email, a resolve_only DOI download consults Unpaywall using the elicited email
// (for this request only) and resolves the link via the "unpaywall" source.
func TestDownloadTool_ElicitsUnpaywallEmailOnAccept(t *testing.T) {
	base, lookups, lastEmail := unpaywallElicitServer(t)
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"email": "asked@example.com"}}, nil
	}
	session := newElicitDownloadSession(t, elicitDownloadConfig(t), handler, libgen.WithUnpaywallBaseURL(base))

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"doi": "10.1/x", "resolve_only": true},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if res.IsError {
		t.Fatalf("download with an accepted email should not be a tool error: %+v", res.Content)
	}
	out := decodeDownloadOutput(t, res)
	if out.Resolved == nil || out.Resolved.Source != "unpaywall" {
		t.Fatalf("expected a resolved link from unpaywall, got %+v", out.Resolved)
	}
	if lookups.Load() != 1 {
		t.Errorf("Unpaywall lookups = %d, want 1", lookups.Load())
	}
	if *lastEmail != "asked@example.com" {
		t.Errorf("Unpaywall received email = %q, want the elicited %q", *lastEmail, "asked@example.com")
	}
}

// TestDownloadTool_ElicitDeclineFallsBack verifies the deterministic fallback: when
// the client supports elicitation but the user declines, no email is used, Unpaywall
// is not consulted (0 lookups), and the DOI download fails with no usable source —
// exactly today's behavior for a server with no configured email.
func TestDownloadTool_ElicitDeclineFallsBack(t *testing.T) {
	base, lookups, _ := unpaywallElicitServer(t)
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	session := newElicitDownloadSession(t, elicitDownloadConfig(t), handler, libgen.WithUnpaywallBaseURL(base))

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"doi": "10.1/x", "resolve_only": true},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if !res.IsError {
		t.Fatal("a DOI download with a declined email and no configured email should be a tool error")
	}
	if lookups.Load() != 0 {
		t.Errorf("Unpaywall lookups = %d after a decline, want 0", lookups.Load())
	}
}

// TestDownloadTool_NoElicitCapabilityFallsBack verifies that a client which did NOT
// advertise elicitation is never prompted: no elicitation is attempted, Unpaywall is
// not consulted (0 lookups), and the DOI download fails just as it does today. This
// guards the headless/CI path stays byte-identical.
func TestDownloadTool_NoElicitCapabilityFallsBack(t *testing.T) {
	base, lookups, _ := unpaywallElicitServer(t)
	// nil handler → the client advertises no elicitation capability.
	session := newElicitDownloadSession(t, elicitDownloadConfig(t), nil, libgen.WithUnpaywallBaseURL(base))

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"doi": "10.1/x", "resolve_only": true},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if !res.IsError {
		t.Fatal("a DOI download with no elicitation capability and no configured email should be a tool error")
	}
	if lookups.Load() != 0 {
		t.Errorf("Unpaywall lookups = %d without elicitation, want 0", lookups.Load())
	}
}

// TestLooksLikeEmail verifies the light email sanity check accepts plausible
// addresses and rejects malformed ones without over-validating.
func TestLooksLikeEmail(t *testing.T) {
	valid := []string{"a@b.co", "you@example.com", "x.y@sub.domain.org"}
	for _, e := range valid {
		if !looksLikeEmail(e) {
			t.Errorf("looksLikeEmail(%q) = false, want true", e)
		}
	}
	invalid := []string{"", "nope", "@example.com", "a@b", "a@b.", "a@.com"}
	for _, e := range invalid {
		if looksLikeEmail(e) {
			t.Errorf("looksLikeEmail(%q) = true, want false", e)
		}
	}
	// A trimmed, plausible address must survive the handler's TrimSpace + check.
	if !looksLikeEmail(strings.TrimSpace("  ok@ok.io  ")) {
		t.Error("looksLikeEmail should accept a trimmed plausible address")
	}
}
