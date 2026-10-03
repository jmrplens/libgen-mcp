package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/v2/internal/discovery"
	"github.com/jmrplens/libgen-mcp/v2/internal/mcpotel"
	"github.com/jmrplens/libgen-mcp/v2/internal/transport"
)

// testCeilings builds a pair of process ceilings small enough for a test to
// fill, shared by nothing else.
func testCeilings(held, sessions int64) *processCeilings {
	return &processCeilings{
		held:        &processSlots{limit: held},
		sessions:    &processSlots{limit: sessions},
		descriptors: 64,
		measured:    true,
	}
}

// TestHeldCallsFor_SizesTheCeilingFromTheDescriptorLimit pins the arithmetic
// the startup line and the operator pages quote.
func TestHeldCallsFor_SizesTheCeilingFromTheDescriptorLimit(t *testing.T) {
	for _, tc := range []struct {
		name        string
		descriptors uint64
		want        int64
	}{
		{name: "a hard limit of 1024", descriptors: 1024, want: 83},
		{name: "a hard limit of 4096", descriptors: 4096, want: 352},
		{name: "a default systemd service", descriptors: 524288, want: 45868},
		{name: "a container's inherited limit", descriptors: 1048576, want: 91744},
		{name: "a limit smaller than the reservation", descriptors: 64, want: 1},
		{name: "no descriptors at all", descriptors: 0, want: 1},
		{name: "the largest limit there is", descriptors: math.MaxUint64, want: 1614090106449585760},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := heldCallsFor(tc.descriptors)
			if got != tc.want {
				t.Errorf("heldCallsFor(%d) = %d, want %d", tc.descriptors, got, tc.want)
			}
			if got <= 0 {
				t.Errorf("heldCallsFor(%d) = %d, want a positive ceiling", tc.descriptors, got)
			}
		})
	}
}

// TestNewProcessCeilings_FallsBackWhereThePlatformSaysNothing pins the Windows
// figure, and that the two ceilings are sized together.
func TestNewProcessCeilings_FallsBackWhereThePlatformSaysNothing(t *testing.T) {
	c := newProcessCeilings(func() (uint64, bool) { return 0, false })
	if c.measured || c.descriptors != fallbackDescriptorLimit {
		t.Errorf("descriptors = %d (measured %t), want the fallback %d", c.descriptors, c.measured, fallbackDescriptorLimit)
	}
	if c.held.limit != 83 || c.sessions.limit != 41 {
		t.Errorf("ceilings = %d held, %d sessions; want 83 and 41", c.held.limit, c.sessions.limit)
	}

	measured := newProcessCeilings(func() (uint64, bool) { return 4096, true })
	if !measured.measured || measured.held.limit != 352 || measured.sessions.limit != 176 {
		t.Errorf("under 4096: %d held, %d sessions (measured %t); want 352 and 176", measured.held.limit, measured.sessions.limit, measured.measured)
	}
}

// TestHeldCallDescriptors_IsTheWidestFanOut ties a held call's cost to the
// provider list: the caller's connection, the catalog request, and one per
// searcher an escalated search runs at once. The pinned 10 is what the figures
// in the tests above and the operator pages were computed with, so a provider
// added to the list fails here first, pointing at every figure to redo.
func TestHeldCallDescriptors_IsTheWidestFanOut(t *testing.T) {
	if want := uint64(2 + len(discovery.ExtraProviders("", nil))); heldCallDescriptors != want {
		t.Errorf("heldCallDescriptors = %d, want 2 + the %d extra searchers = %d", heldCallDescriptors, want-2, want)
	}
	if heldCallDescriptors != 10 {
		t.Errorf("heldCallDescriptors = %d; the documented figures assume 10, so recompute them", heldCallDescriptors)
	}
}

// TestProcessSlots_NeverTakesMoreThanTheLimit drives acquire's retry on purpose:
// another acquire takes the last slot between the read and the swap, and the
// retry must see the count full rather than take a slot past it.
func TestProcessSlots_NeverTakesMoreThanTheLimit(t *testing.T) {
	slots := &processSlots{limit: 1}
	var raced atomic.Bool
	slots.contend = func() {
		if raced.CompareAndSwap(false, true) {
			slots.open.Add(1)
		}
	}
	if slots.acquire() {
		t.Fatal("acquire took a slot another acquire had taken under it")
	}
	if got := slots.open.Load(); got != 1 {
		t.Errorf("count = %d, want 1", got)
	}

	slots.contend = nil
	slots.release()
	if !slots.acquire() {
		t.Error("a released slot could not be taken again")
	}
}

// TestBusyLog_WritesOncePerWindow keeps a flood of refusals from burying the
// log stream: the first line of a window is written, the rest are not.
func TestBusyLog_WritesOncePerWindow(t *testing.T) {
	var b busyLog
	b.log(t.Context(), "first")
	first := b.last.Load()
	if first == 0 {
		t.Fatal("the first refusal of a window was not logged")
	}
	b.log(t.Context(), "second")
	if b.last.Load() != first {
		t.Error("a second refusal inside the window was logged")
	}
	// Compared with the backdated stamp rather than with first: Windows' clock
	// is coarse enough that the third write can land on the first's instant.
	backdated := time.Now().Add(-2 * busyLogWindow).UnixNano()
	b.last.Store(backdated)
	b.log(t.Context(), "third")
	if b.last.Load() == backdated {
		t.Error("a refusal after the window was not logged")
	}
}

// holdingServer is the production MCP server carrying one tool, named like the
// real search, whose calls block until the test releases them.
type holdingServer struct {
	server  *mcp.Server
	entered chan struct{}
	release chan struct{}
}

// newHoldingServer builds it. The handler also ends when its call is canceled,
// so a test that fails half-way does not leave a call behind for shutdown to
// wait out.
func newHoldingServer(t *testing.T) *holdingServer {
	t.Helper()
	type in struct{}
	type out struct{}
	h := &holdingServer{
		server:  newMCPServer(serverInstructions(true, false), nil, heavyCeiling{}, mcpotel.Options{}),
		entered: make(chan struct{}, 16),
		release: make(chan struct{}),
	}
	mcp.AddTool(h.server, &mcp.Tool{Name: "search", Description: "stub"},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ in) (*mcp.CallToolResult, out, error) {
			h.entered <- struct{}{}
			select {
			case <-h.release:
			case <-ctx.Done():
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, out{}, nil
		})
	t.Cleanup(h.releaseAll)
	return h
}

// releaseAll lets every held call finish. Safe to call more than once.
func (h *holdingServer) releaseAll() {
	select {
	case <-h.release:
	default:
		close(h.release)
	}
}

// awaitEntered waits for n calls to have reached the handler.
func (h *holdingServer) awaitEntered(t *testing.T, n int) {
	t.Helper()
	// sequential: each receive waits for one more call to be in flight
	for range n {
		select {
		case <-h.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("a call never reached the handler")
		}
	}
}

// serveWithCeilings serves server over a real socket against ceilings, and
// returns its base URL. The server is shut down when the test ends.
func serveWithCeilings(t *testing.T, server *mcp.Server, opts transport.Options, ceilings *processCeilings) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serveHTTPOn(ctx, server, ln, opts, httpPolicy{
			guard:    newHostGuard(addr, "", trustedProxies{}),
			ceilings: ceilings,
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(httpShutdownTimeout + 5*time.Second):
			t.Error("serveHTTPOn did not return after cancel")
		}
	})
	base := "http://" + addr
	waitForHealth(t, base)
	return base
}

// The bodies and headers of the calls these tests make.
const (
	legacyVersion  = "2025-11-25"
	legacyCallBody = `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"search","arguments":{}}}`
	modernCallBody = `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"search","arguments":{},"_meta":{` +
		`"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
		`"io.modelcontextprotocol/clientCapabilities":{},` +
		`"io.modelcontextprotocol/clientInfo":{"name":"test","version":"0"}}}}`
)

// legacyHeaders are what a client on a revision before 2026-07-28 sends.
func legacyHeaders() map[string]string {
	return map[string]string{protocolVersionHeader: legacyVersion}
}

// modernHeaders are what a 2026-07-28 client sends for a call of search.
func modernHeaders() map[string]string {
	return map[string]string{
		protocolVersionHeader: protocolVersionStatelessOnly,
		"Mcp-Method":          methodToolsCall,
		"Mcp-Name":            "search",
	}
}

// reply is what a POST came back with.
type reply struct {
	status int
	header http.Header
	body   string
	// closed reports that the server closed the connection with the reply.
	closed bool
}

// sendMCP sends one POST to the MCP endpoint. It reports failure as an error
// rather than failing the test, so it can run off the test goroutine.
func sendMCP(ctx context.Context, base, body string, headers map[string]string) (reply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/", strings.NewReader(body))
	if err != nil {
		return reply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return reply{}, err
	}
	return reply{status: resp.StatusCode, header: resp.Header, body: string(raw), closed: resp.Close}, nil
}

// shortContext bounds a call that a working ceiling refuses at once, so a
// ceiling that let it through fails the test with a deadline rather than
// holding it until the whole suite times out.
func shortContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// awaitCall receives a held call's outcome, or fails the test once callWait
// passes. The call goes out on a background context, so a bare receive would
// wait for go test's own deadline if a broken ceiling never let it finish.
func awaitCall(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(callWait):
		t.Fatal("the held call did not finish after it was released")
		return nil
	}
}

// holdCall starts one call in the background and returns where its reply
// arrives.
func holdCall(base, body string, headers map[string]string) <-chan error {
	done := make(chan error, 1)
	go func() {
		r, err := sendMCP(context.Background(), base, body, headers)
		if err == nil && r.status != http.StatusOK {
			err = errors.New(r.body)
		}
		done <- err
	}()
	return done
}

// TestProcessCeiling_RefusesACallPastTheHeldCeiling is the gap the ceiling
// closes: search and get_details had no process-wide bound, so every call
// offered was held, and enough of them held every descriptor the process had.
func TestProcessCeiling_RefusesACallPastTheHeldCeiling(t *testing.T) {
	h := newHoldingServer(t)
	ceilings := testCeilings(2, 1)
	base := serveWithCeilings(t, h.server, transport.DefaultOptions(), ceilings)

	first := holdCall(base, legacyCallBody, legacyHeaders())
	second := holdCall(base, legacyCallBody, legacyHeaders())
	h.awaitEntered(t, 2)

	t.Run("an older revision's call is refused as a tool error, and its connection closed", func(t *testing.T) {
		r, err := sendMCP(shortContext(t), base, legacyCallBody, legacyHeaders())
		if err != nil {
			t.Fatalf("third call: %v", err)
		}
		if !strings.Contains(r.body, processBusyText) || !strings.Contains(r.body, `"isError":true`) {
			t.Errorf("the third call with two held and a ceiling of two was not refused: %d %s", r.status, r.body)
		}
		if !r.closed {
			t.Error("the refused call's connection was kept open, holding a descriptor of the limit that refused it")
		}
	})

	t.Run("a 2026-07-28 call is refused in the gate with 503", func(t *testing.T) {
		r, err := sendMCP(shortContext(t), base, modernCallBody, modernHeaders())
		if err != nil {
			t.Fatalf("modern call: %v", err)
		}
		assertBusyRefusal(t, r, `9`)
	})

	t.Run("health still answers", func(t *testing.T) {
		if !healthOK(base) {
			t.Error("/health did not answer with every held slot taken")
		}
	})

	h.releaseAll()
	if err := awaitCall(t, first); err != nil {
		t.Errorf("the first held call was not served once released: %v", err)
	}
	if err := awaitCall(t, second); err != nil {
		t.Errorf("the second held call was not served once released: %v", err)
	}
	waitUntil(t, func() bool { return ceilings.held.open.Load() == 0 })
}

// assertBusyRefusal checks the gate's refusal of a full process: 503, a fixed
// Retry-After, the connection closed, and a JSON-RPC error carrying the id.
func assertBusyRefusal(t *testing.T, r reply, wantID string) {
	t.Helper()
	if r.status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (body %s)", r.status, r.body)
	}
	if got := r.header.Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want 30", got)
	}
	if !r.closed {
		t.Error("the refusal left the connection open")
	}
	var body jsonRPCError
	if err := json.Unmarshal([]byte(r.body), &body); err != nil {
		t.Fatalf("the refusal is not a JSON-RPC error: %v (%s)", err, r.body)
	}
	if body.Error.Code != codeServiceUnavailable || body.Error.Message != processBusyText {
		t.Errorf("error = %d %q, want %d %q", body.Error.Code, body.Error.Message, codeServiceUnavailable, processBusyText)
	}
	if string(body.ID) != wantID {
		t.Errorf("id = %s, want %s", body.ID, wantID)
	}
}

// TestProcessCeiling_AModernCallCountsOnce pins the hand-over between the gate
// and the middleware: a 2026-07-28 call's slot is taken in the gate, and the
// call claims it rather than taking a second, so a ceiling of one serves it.
func TestProcessCeiling_AModernCallCountsOnce(t *testing.T) {
	h := newHoldingServer(t)
	ceilings := testCeilings(1, 1)
	base := serveWithCeilings(t, h.server, transport.DefaultOptions(), ceilings)

	done := holdCall(base, modernCallBody, modernHeaders())
	h.awaitEntered(t, 1)
	if got := ceilings.held.open.Load(); got != 1 {
		t.Errorf("one modern call holds %d slots, want 1", got)
	}
	h.releaseAll()
	if err := awaitCall(t, done); err != nil {
		t.Errorf("the modern call was not served: %v", err)
	}
	waitUntil(t, func() bool { return ceilings.held.open.Load() == 0 })
}

// TestProcessCeiling_ALegacyCallIsCountedWhereTheSDKDispatchesIt pins the other
// half: a call on a revision whose headers do not prove the method takes its
// slot in the middleware, and gives it back when it ends.
func TestProcessCeiling_ALegacyCallIsCountedWhereTheSDKDispatchesIt(t *testing.T) {
	h := newHoldingServer(t)
	ceilings := testCeilings(4, 1)
	base := serveWithCeilings(t, h.server, transport.DefaultOptions(), ceilings)

	done := holdCall(base, legacyCallBody, legacyHeaders())
	h.awaitEntered(t, 1)
	if got := ceilings.held.open.Load(); got != 1 {
		t.Errorf("one legacy call holds %d slots, want 1", got)
	}
	h.releaseAll()
	if err := awaitCall(t, done); err != nil {
		t.Errorf("the legacy call was not served: %v", err)
	}
	waitUntil(t, func() bool { return ceilings.held.open.Load() == 0 })
}

// claimedRequest registers a POST context carrying claim under a fresh carrier
// token, and returns a request of method carried by it.
func claimedRequest(t *testing.T, claim *postClaim, method string) mcp.Request {
	t.Helper()
	token := rand.Text()
	mcpCarriers.contexts.Store(token, context.WithValue(t.Context(), postClaimKey{}, claim))
	t.Cleanup(func() { mcpCarriers.contexts.Delete(token) })
	extra := &mcp.RequestExtra{Header: http.Header{carrierHeader: []string{token}}}
	if method == methodPromptsGet {
		return &mcp.GetPromptRequest{Params: &mcp.GetPromptParams{Name: "acquire_book"}, Extra: extra}
	}
	return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "search"}, Extra: extra}
}

// TestProcessCeilingsMiddleware_CountsOnlyCallsThatCanBeHeld walks every branch
// that lets a request past without taking a slot, and the two refusal shapes.
func TestProcessCeilingsMiddleware_CountsOnlyCallsThatCanBeHeld(t *testing.T) {
	var reached atomic.Int32
	handler := processCeilingsMiddleware(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		reached.Add(1)
		return &mcp.CallToolResult{}, nil
	})

	t.Run("a request on no POST is not counted", func(t *testing.T) {
		if _, err := handler(t.Context(), methodToolsCall, carriedRequest("")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a POST that is gone is not counted", func(t *testing.T) {
		if _, err := handler(t.Context(), methodToolsCall, carriedRequest("gone")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a POST the gate did not see is not counted", func(t *testing.T) {
		token := rand.Text()
		mcpCarriers.contexts.Store(token, t.Context())
		defer mcpCarriers.contexts.Delete(token)
		if _, err := handler(t.Context(), methodToolsCall, carriedRequest(token)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a listing is not counted at a full ceiling", func(t *testing.T) {
		full := testCeilings(0, 1)
		if _, err := handler(t.Context(), "tools/list", claimedRequest(t, &postClaim{ceilings: full}, "tools/list")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a call whose slot the gate took claims it", func(t *testing.T) {
		full := testCeilings(0, 1)
		claim := &postClaim{ceilings: full}
		claim.gateHeld.Store(true)
		res, err := handler(t.Context(), methodToolsCall, claimedRequest(t, claim, methodToolsCall))
		if err != nil || refusalText(t, res) != "" {
			t.Fatalf("the call the gate already counted was refused: %v %v", res, err)
		}
		if claim.gateHeld.Load() {
			t.Error("the gate's slot was not claimed")
		}
	})
	if got := reached.Load(); got != 5 {
		t.Errorf("the handler was reached %d times, want 5", got)
	}

	t.Run("a tools/call past the ceiling is a tool error", func(t *testing.T) {
		claim := &postClaim{ceilings: testCeilings(0, 1)}
		res, err := handler(t.Context(), methodToolsCall, claimedRequest(t, claim, methodToolsCall))
		if err != nil || refusalText(t, res) != processBusyText {
			t.Errorf("got %v %v, want the busy tool error", res, err)
		}
		if !claim.refused.Load() {
			t.Error("the refusal was not marked for the connection to close")
		}
	})
	t.Run("a prompts/get past the ceiling is a JSON-RPC error", func(t *testing.T) {
		claim := &postClaim{ceilings: testCeilings(0, 1)}
		_, err := handler(t.Context(), methodPromptsGet, claimedRequest(t, claim, methodPromptsGet))
		var rpcErr *jsonrpc.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != codeServiceUnavailable || rpcErr.Message != processBusyText {
			t.Errorf("err = %v, want code %d %q", err, codeServiceUnavailable, processBusyText)
		}
	})
	if got := reached.Load(); got != 5 {
		t.Errorf("a refused call reached the handler: %d calls, want 5", got)
	}
}

// TestBusyAwareWriter_ClosesWhicheverWriteSendsTheHeader pins that a refusal
// closes the connection however the SDK first touches the response, and that
// the writer underneath stays reachable to http.ResponseController.
func TestBusyAwareWriter_ClosesWhicheverWriteSendsTheHeader(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first func(w *busyAwareWriter)
	}{
		{name: "a flush", first: func(w *busyAwareWriter) { w.Flush() }},
		{name: "a write", first: func(w *busyAwareWriter) { _, _ = w.Write([]byte("x")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			claim := &postClaim{}
			claim.refused.Store(true)
			w := &busyAwareWriter{ResponseWriter: rec, claim: claim}
			tc.first(w)
			w.Flush()
			if got := rec.Header().Get("Connection"); got != "close" {
				t.Errorf("Connection = %q, want close", got)
			}
			if w.Unwrap() != rec {
				t.Error("Unwrap does not return the writer underneath")
			}
		})
	}
}

// TestAnnounceProcessCeilings_NamesWhatThisListenerHolds pins the startup line:
// the session figure only where sessions are kept, the source of the figures,
// and the warning a stateful listener that never closes a session earns.
func TestAnnounceProcessCeilings_NamesWhatThisListenerHolds(t *testing.T) {
	for _, tc := range []struct {
		name        string
		ceilings    *processCeilings
		opts        transport.Options
		want        []string
		unwanted    []string
		wantWarning bool
	}{
		{
			name:     "stateless names no session ceiling",
			ceilings: newProcessCeilings(func() (uint64, bool) { return 1024, true }),
			opts:     transport.DefaultOptions(),
			want:     []string{`"held_calls_per_process":83`, `"descriptor_limit":1024`, `"RLIMIT_NOFILE"`},
			unwanted: []string{"stateful_sessions_per_process"},
		},
		{
			name:     "stateful names the session ceiling",
			ceilings: newProcessCeilings(func() (uint64, bool) { return 0, false }),
			opts:     transport.Options{SessionTimeout: time.Minute},
			want:     []string{`"stateful_sessions_per_process":41`, "fallback"},
			unwanted: []string{"never closes an idle session"},
		},
		{
			name:        "stateful without a session timeout is warned",
			ceilings:    newProcessCeilings(func() (uint64, bool) { return 1024, true }),
			opts:        transport.Options{},
			want:        []string{"never closes an idle session"},
			wantWarning: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureTelemetryLog(t)
			announceProcessCeilings(tc.ceilings, tc.opts)
			out := logs()
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("the startup line does not carry %s:\n%s", want, out)
				}
			}
			for _, unwanted := range tc.unwanted {
				if strings.Contains(out, unwanted) {
					t.Errorf("the startup line carries %s:\n%s", unwanted, out)
				}
			}
			if got := strings.Contains(out, `"level":"WARN"`); got != tc.wantWarning {
				t.Errorf("warning written = %t, want %t:\n%s", got, tc.wantWarning, out)
			}
		})
	}
}
