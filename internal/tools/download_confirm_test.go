package tools

import (
	"context"
	"crypto/md5" //nolint:gosec // tests compute the LibGen file digest for integrity assertions.
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
)

// confirmMirror serves the full book download chain (ads.php → get.php → CDN) for a
// payload whose md5 it advertises, and separately counts HEAD probes and GET
// body-fetches of the CDN endpoint. The counters let the confirmation tests prove
// which requests each path makes: a size probe issues a HEAD, the actual download
// issues a GET, and the default (no-capability) path must issue neither a probe.
func confirmMirror(t *testing.T, payload []byte) (srv *httptest.Server, cdnGET, cdnHEAD *atomic.Int32) {
	t.Helper()
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])
	var getHits, headHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<html><a href="get.php?md5=%s&key=TESTKEY123">GET</a></html>`, wantMD5)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/cdn/file", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/cdn/file", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			headHits.Add(1)
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			return
		}
		getHits.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="book.pdf"`)
		_, _ = w.Write(payload)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &getHits, &headHits
}

// newConfirmSession registers the tools on a client backed by mirrors and connects
// an in-memory MCP client whose elicitation capability is governed by handler
// (nil = no capability, exercising the default/headless path). It is the download
// confirmation counterpart of newDownloadSession.
func newConfirmSession(t *testing.T, cfg *config.Config, mirrors libgen.MirrorLister, handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientSession {
	t.Helper()
	client := libgen.New(mirrors, cfg)
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

// confirmConfig returns a plain local-download config rooted at a fresh temp dir.
func confirmConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
}

// TestDownloadTool_ConfirmAccepted verifies the confirm-and-save path: with an
// elicitation-capable client that accepts the confirmation, a local md5 download is
// prompted (the elicitation handler is invoked exactly once) and the file is then
// downloaded and saved to disk.
func TestDownloadTool_ConfirmAccepted(t *testing.T) {
	payload := []byte("%PDF-1.4 confirm-accepted book payload")
	srv, cdnGET, _ := confirmMirror(t, payload)
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])

	cfg := confirmConfig(t)
	var elicits atomic.Int32
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		elicits.Add(1)
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
	}
	session := newConfirmSession(t, cfg, staticMirrors{srv.URL}, handler)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": wantMD5},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if res.IsError {
		t.Fatalf("an accepted download should not be a tool error: %+v", res.Content)
	}
	if elicits.Load() != 1 {
		t.Errorf("elicitation handler invoked %d times, want 1", elicits.Load())
	}
	out := decodeDownloadOutput(t, res)
	if out.Path == "" {
		t.Fatalf("accepted download should report a saved path; got %+v", out)
	}
	if _, statErr := os.Stat(out.Path); statErr != nil {
		t.Errorf("accepted download did not write the file: %v", statErr)
	}
	if cdnGET.Load() == 0 {
		t.Error("accepted download never fetched the file body (0 CDN GETs)")
	}
}

// TestDownloadTool_ConfirmDeclined verifies the decline path: with an
// elicitation-capable client that declines the confirmation, NO file is written
// (the CDN body endpoint gets 0 GETs), the result is NOT a tool error, and it
// carries guidance plus the resolved direct link so the user can fetch it later.
func TestDownloadTool_ConfirmDeclined(t *testing.T) {
	payload := []byte("%PDF-1.4 confirm-declined book payload")
	srv, cdnGET, _ := confirmMirror(t, payload)
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])

	cfg := confirmConfig(t)
	var elicits atomic.Int32
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		elicits.Add(1)
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": false}}, nil
	}
	session := newConfirmSession(t, cfg, staticMirrors{srv.URL}, handler)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": wantMD5},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if res.IsError {
		t.Fatalf("a declined download should be a non-error result; got %+v", res.Content)
	}
	if elicits.Load() != 1 {
		t.Errorf("elicitation handler invoked %d times, want 1", elicits.Load())
	}
	if cdnGET.Load() != 0 {
		t.Errorf("a declined download fetched the file body %d time(s), want 0", cdnGET.Load())
	}
	out := decodeDownloadOutput(t, res)
	if out.Path != "" {
		t.Errorf("a declined download must not save a file, but Path=%q", out.Path)
	}
	if out.Resolved == nil {
		t.Errorf("a declined download should still surface the resolved link; got %+v", out)
	}
	if entries, _ := os.ReadDir(cfg.DownloadDir); len(entries) != 0 {
		t.Errorf("a declined download wrote %d file(s) to disk, want 0", len(entries))
	}
}

// TestDownloadTool_NoElicitationDownloadsNormally proves default preservation: a
// client that did NOT advertise elicitation is never prompted and never triggers a
// size probe — the download proceeds and saves the file exactly as today, and the
// CDN endpoint sees ZERO HEAD probes (only the body GET).
func TestDownloadTool_NoElicitationDownloadsNormally(t *testing.T) {
	payload := []byte("%PDF-1.4 no-elicitation book payload")
	srv, cdnGET, cdnHEAD := confirmMirror(t, payload)
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])

	cfg := confirmConfig(t)
	// nil handler → the client advertises no elicitation capability.
	session := newConfirmSession(t, cfg, staticMirrors{srv.URL}, nil)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": wantMD5},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if res.IsError {
		t.Fatalf("a no-capability download should not be a tool error: %+v", res.Content)
	}
	out := decodeDownloadOutput(t, res)
	if out.Path == "" {
		t.Fatalf("download should report a saved path; got %+v", out)
	}
	if _, statErr := os.Stat(out.Path); statErr != nil {
		t.Errorf("download did not write the file: %v", statErr)
	}
	if cdnHEAD.Load() != 0 {
		t.Errorf("the no-capability path issued %d HEAD probe(s), want 0 (no probe without elicitation)", cdnHEAD.Load())
	}
	if cdnGET.Load() == 0 {
		t.Error("the no-capability path never fetched the file body (0 CDN GETs)")
	}
}

// TestDownloadTool_ResolveOnlyNoConfirm verifies that resolve_only never prompts,
// even with an elicitation-capable client: resolve_only never writes to disk, so
// there is nothing to confirm and the elicitation handler is not invoked.
func TestDownloadTool_ResolveOnlyNoConfirm(t *testing.T) {
	payload := []byte("%PDF-1.4 resolve-only no-confirm payload")
	srv, _, cdnHEAD := confirmMirror(t, payload)
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])

	cfg := confirmConfig(t)
	var elicits atomic.Int32
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		elicits.Add(1)
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
	}
	session := newConfirmSession(t, cfg, staticMirrors{srv.URL}, handler)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": wantMD5, "resolve_only": true},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if res.IsError {
		t.Fatalf("resolve_only returned a tool error: %+v", res.Content)
	}
	if elicits.Load() != 0 {
		t.Errorf("resolve_only invoked the elicitation handler %d times, want 0", elicits.Load())
	}
	if cdnHEAD.Load() != 0 {
		t.Errorf("resolve_only issued %d HEAD probe(s), want 0", cdnHEAD.Load())
	}
	out := decodeDownloadOutput(t, res)
	if out.Resolved == nil {
		t.Errorf("resolve_only should return a resolved link; got %+v", out)
	}
}

// TestDownloadTool_ConfirmCanceled verifies that an explicit cancel of the
// download confirmation aborts the save (same as a decline): the file body is
// never fetched, nothing is written, and the result is a non-error with the link.
func TestDownloadTool_ConfirmCanceled(t *testing.T) {
	payload := []byte("%PDF-1.4 confirm-canceled book payload")
	srv, cdnGET, _ := confirmMirror(t, payload)
	sum := md5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])

	cfg := confirmConfig(t)
	var elicits atomic.Int32
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		elicits.Add(1)
		return &mcp.ElicitResult{Action: "cancel"}, nil
	}
	session := newConfirmSession(t, cfg, staticMirrors{srv.URL}, handler)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": wantMD5},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if res.IsError {
		t.Fatalf("a canceled download should be a non-error result; got %+v", res.Content)
	}
	if elicits.Load() != 1 {
		t.Errorf("elicitation handler invoked %d times, want 1", elicits.Load())
	}
	if cdnGET.Load() != 0 {
		t.Errorf("a canceled download fetched the file body %d time(s), want 0", cdnGET.Load())
	}
	out := decodeDownloadOutput(t, res)
	if out.Path != "" {
		t.Errorf("a canceled download must not save a file, but Path=%q", out.Path)
	}
	if entries, _ := os.ReadDir(cfg.DownloadDir); len(entries) != 0 {
		t.Errorf("a canceled download wrote %d file(s) to disk, want 0", len(entries))
	}
}
