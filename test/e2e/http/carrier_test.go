//go:build httpe2e

package httpe2e

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// searchCallBody is a tools/call that reaches an upstream mirror, which is what
// makes an abandoned call cost something here rather than merely finish
// unnoticed.
const searchCallBody = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"query":"anything"}}}`

// TestCarrier_AbandonedCallStopsWorkingUpstream is the behavior, observed where
// it is visible: at the mirror.
//
// Without the carrier an abandoned POST leaves its handler running. The SDK
// deliberately ignores the HTTP request's cancellation — it may already have
// published a response — and expects the protocol's cancellation notification
// instead, which no client older than protocol 2026-07-28 can send. So the transfer keeps going
// against the mirror, holding a download slot and spending the shared outbound
// bucket, for a result that has nowhere left to go: this server configures no
// EventStore, so the answer can only be written to the stream that carried the
// request.
//
// The mirror blocks until its own request context ends and reports when that
// happened, so what is asserted is the server closing the upstream request —
// not a log line, and not a timing coincidence. LIBGEN_MCP_TIMEOUT and the
// wall-clock cap are both widened past the assertion window, so the carrier is
// the only thing that can end it.
func TestCarrier_AbandonedCallStopsWorkingUpstream(t *testing.T) {
	reached := make(chan struct{}, 1)
	released := make(chan struct{}, 1)
	m := startMirror(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case reached <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		select {
		case released <- struct{}{}:
		default:
		}
	})

	env := mirrorEnv(m)
	// Both of these would end the call on their own, which is the whole point
	// of widening them: with the defaults a test like this passes on a build
	// that has no carrier at all, it just takes longer.
	env["LIBGEN_MCP_TIMEOUT"] = "120s"
	env["LIBGEN_MCP_ACTION_TIMEOUT"] = "0"
	s := startServer(t, env)

	ctx, abandon := context.WithCancel(t.Context())
	defer abandon()

	callDone := make(chan struct{})
	go func() {
		defer close(callDone)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/", strings.NewReader(searchCallBody))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", acceptHeader)
		req.Header.Set("MCP-Protocol-Version", protocolVersion)
		resp, err := s.httpClient().Do(req)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	select {
	case <-reached:
	case <-time.After(60 * time.Second):
		t.Fatalf("the call never reached the mirror. Output:\n%s", s.logs())
	}

	abandon()

	select {
	case <-released:
	case <-time.After(20 * time.Second):
		t.Fatalf("the upstream request outlived the POST that asked for it. Output:\n%s", s.logs())
	}
	<-callDone

	// And the process is unharmed by having a call pulled out from under it.
	if !s.healthy(t) {
		t.Fatalf("the server stopped serving after a call was abandoned. Output:\n%s", s.logs())
	}
	if got := s.do(t, mcpPOST(nil)).status; got != http.StatusOK {
		t.Errorf("tools/list after an abandoned call = %d, want %d", got, http.StatusOK)
	}
}

// TestCarrier_ACallThatIsNotAbandonedRunsToCompletion is the negative half, and
// the one that would catch a carrier that cancels everything.
//
// The bar is deliberately low — the mirror answers nothing useful, so the tool
// call comes back as an error — because what is being asserted is that the call
// completed on its own terms rather than being cut short by the layer that is
// supposed to notice only abandoned ones.
func TestCarrier_ACallThatIsNotAbandonedRunsToCompletion(t *testing.T) {
	m := startMirror(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>no results</body></html>"))
	})
	s := startServer(t, mirrorEnv(m))

	reply := s.do(t, request{body: searchCallBody})
	if reply.status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", reply.status, http.StatusOK, truncate(reply.body))
	}
	if m.calls() == 0 {
		t.Error("the call never reached the mirror, so nothing about cancellation was measured")
	}
	// The carrier's cause must not leak into a result the client is still
	// waiting for.
	if strings.Contains(reply.body, "abandoned the request") {
		t.Errorf("a live call was answered as abandoned: %s", truncate(reply.body))
	}
}
