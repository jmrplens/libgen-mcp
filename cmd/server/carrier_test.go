package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// carriedRequest is an MCP request as the SDK hands it to a receiving
// middleware: the header of the POST it arrived on, or none at all on a
// transport that stamps none.
func carriedRequest(token string) mcp.Request {
	req := &mcp.CallToolRequest{}
	if token == "" {
		return req
	}
	req.Extra = &mcp.RequestExtra{Header: http.Header{carrierHeader: []string{token}}}
	return req
}

// stampedToken drives the HTTP half and returns the token it minted, along with
// the cancel that ends the POST.
func stampedToken(t *testing.T, c *requestCarriers) (token string, endPOST func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	var minted string
	handler := c.middleware(chargePolicy{}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		minted = r.Header.Get(carrierHeader)
	}))
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader("{}"))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if minted == "" {
		cancel()
		t.Fatal("the middleware minted no carrier token for a POST")
	}
	return minted, cancel
}

// TestCarrierCancelsACallWhenThePOSTGoesAway is the whole point: an abandoned
// download must stop rather than run to completion for an answer that has
// nowhere left to go.
func TestCarrierCancelsACallWhenThePOSTGoesAway(t *testing.T) {
	var c requestCarriers
	token, endPOST := stampedToken(t, &c)

	handlerCtx := make(chan context.Context, 1)
	released := make(chan struct{})
	next := func(ctx context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
		handlerCtx <- ctx
		<-released
		return &mcp.CallToolResult{}, nil
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.bind(next)(t.Context(), "tools/call", carriedRequest(token))
	}()

	ctx := <-handlerCtx
	if ctx.Err() != nil {
		t.Fatalf("the handler started already canceled: %v", ctx.Err())
	}

	endPOST()

	select {
	case <-ctx.Done():
		if cause := context.Cause(ctx); !errors.Is(cause, errCallerGone) {
			t.Errorf("cause = %v, want errCallerGone so a handler can tell this from a deadline", cause)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler's context outlived the POST that carried it")
	}
	close(released)
	<-done
}

// TestCarrierCancelsAtOnceWhenThePOSTIsAlreadyOver covers the token with no
// entry.
//
// The POST that carried a call blocks until its response is written, so the
// carrier is live when the call is dispatched. A token nothing is registered
// under therefore means the POST has already ended — and the call is canceled
// straight away rather than left running for an answer that can no longer be
// delivered.
func TestCarrierCancelsAtOnceWhenThePOSTIsAlreadyOver(t *testing.T) {
	var c requestCarriers
	token, endPOST := stampedToken(t, &c)
	endPOST()
	// The entry is removed by a context.AfterFunc, so wait for it rather than
	// assuming the goroutine has run.
	waitUntil(t, func() bool { return c.lookup(token) == nil })

	seen := contextSeenBy(t, &c, "tools/call", token)
	if seen.Err() == nil {
		t.Error("a call carrying a token nobody registered ran under a live context")
	}
	if cause := context.Cause(seen); !errors.Is(cause, errCallerGone) {
		t.Errorf("cause = %v, want errCallerGone", cause)
	}
}

// contextSeenBy runs one request through bind and returns the context the
// handler was given.
//
// The context travels back on a channel rather than through a captured
// variable, which is what keeps the closure from being a nested-context
// assignment the linter objects to — and it also means a handler that never ran
// fails here rather than leaving a nil to dereference later.
func contextSeenBy(t *testing.T, c *requestCarriers, method, token string) context.Context {
	t.Helper()

	seen := make(chan context.Context, 1)
	next := func(ctx context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
		seen <- ctx
		return &mcp.CallToolResult{}, nil
	}
	if _, err := c.bind(next)(t.Context(), method, carriedRequest(token)); err != nil {
		t.Fatalf("bind returned an error: %v", err)
	}
	select {
	case ctx := <-seen:
		return ctx
	default:
		t.Fatal("the handler never ran")
		return nil
	}
}

// waitUntil polls a condition rather than sleeping a fixed time.
func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the condition never held")
}

// TestCarrierLeavesANotificationAlone is the case binding would break.
//
// A notification has no response, so the POST does not wait for it to be
// handled before returning — its handler can legitimately still be running when
// the carrier is already done. notifications/initialized on a stateful session
// is the everyday example, and binding it would cancel work nobody abandoned.
func TestCarrierLeavesANotificationAlone(t *testing.T) {
	var c requestCarriers
	token, endPOST := stampedToken(t, &c)
	endPOST()
	waitUntil(t, func() bool { return c.lookup(token) == nil })

	if seen := contextSeenBy(t, &c, "notifications/initialized", token); seen.Err() != nil {
		t.Errorf("the notification ran under a canceled context (%v); nothing was waiting for it", seen.Err())
	}
}

// TestCarrierLeavesAnUncarriedRequestAlone covers stdio and the in-memory
// transport the server card is built over: neither passes through the HTTP
// middleware, so neither carries a token, and neither must be canceled for it.
func TestCarrierLeavesAnUncarriedRequestAlone(t *testing.T) {
	var c requestCarriers

	if seen := contextSeenBy(t, &c, "tools/call", ""); seen.Err() != nil {
		t.Errorf("a request with no carrier token was canceled: %v", seen.Err())
	}
}

// TestCarrierRefusesASmuggledToken is the reason the header is stamped rather
// than merely read.
//
// A client that could set the carrier header would be naming somebody else's
// POST: bind would attach their call to a context that is not theirs, and ending
// that POST would cancel it. One who could set the client-address header would
// be choosing whose budget their traffic counts against. Both are
// server-controlled, so both are overwritten on a POST and deleted on anything
// else.
func TestCarrierRefusesASmuggledToken(t *testing.T) {
	var c requestCarriers
	// A policy that vouches for nobody, so the address charged is the peer's —
	// which is what the test asserts arrives, rather than the caller's value.
	charge := chargePolicy{}

	reachedWith := func(t *testing.T, method string) (token, address string) {
		t.Helper()
		handler := c.middleware(charge, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			token = r.Header.Get(carrierHeader)
			address = r.Header.Get(clientAddressHeader)
		}))
		var body io.Reader
		if method == http.MethodPost {
			body = strings.NewReader("{}")
		}
		req := httptest.NewRequestWithContext(t.Context(), method, "/", body)
		req.RemoteAddr = "198.51.100.23:41100"
		req.Header.Set(carrierHeader, "smuggled")
		req.Header.Set(clientAddressHeader, "203.0.113.7")
		handler.ServeHTTP(httptest.NewRecorder(), req)
		return token, address
	}

	t.Run("a POST gets both values overwritten", func(t *testing.T) {
		token, address := reachedWith(t, http.MethodPost)
		if token == "smuggled" {
			t.Error("the caller's own carrier token reached the SDK")
		}
		if token == "" {
			t.Error("no token was minted for the POST")
		}
		if address == "203.0.113.7" {
			t.Error("the caller chose the address their traffic is charged to")
		}
		if address != "198.51.100.23" {
			t.Errorf("charged address = %q, want the peer this policy vouches for nobody else than", address)
		}
	})

	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodOptions} {
		t.Run("a "+method+" gets both deleted", func(t *testing.T) {
			token, address := reachedWith(t, method)
			if token != "" {
				t.Errorf("a %s carried the token %q through; this method mints none, so none may pass", method, token)
			}
			if address != "" {
				t.Errorf("a %s carried the charged address %q through; this method stamps none", method, address)
			}
		})
	}
}

// TestCarrierForgetsAPOSTThatEnded keeps the registry from growing for the life
// of the process. One entry per request that is never removed is a leak with a
// client-controlled rate.
func TestCarrierForgetsAPOSTThatEnded(t *testing.T) {
	var c requestCarriers
	token, endPOST := stampedToken(t, &c)

	if c.lookup(token) == nil {
		t.Fatal("the POST's context was not registered while it was live")
	}
	endPOST()
	waitUntil(t, func() bool { return c.lookup(token) == nil })
}

// TestCarrierTokensAreDistinct pins that each POST gets its own name. One shared
// token would tie every concurrent call to whichever POST ended first.
func TestCarrierTokensAreDistinct(t *testing.T) {
	var c requestCarriers
	seen := map[string]bool{}
	for range 16 {
		token, endPOST := stampedToken(t, &c)
		defer endPOST()
		if seen[token] {
			t.Fatalf("token %q was minted twice", token)
		}
		seen[token] = true
	}
}

// TestCarrierCancelsACallWhoseCarrierIsDoneButStillRegistered closes the window
// between the lookup and the callback.
//
// The entry is removed by a context.AfterFunc, and AfterFunc runs its callback
// on another goroutine — so a POST that ended a moment ago leaves a carrier that
// is already canceled and still in the map. Registering a second AfterFunc and
// going straight on would hand that call a live context and start work for an
// answer that can never be delivered; the check has to be synchronous.
func TestCarrierCancelsACallWhoseCarrierIsDoneButStillRegistered(t *testing.T) {
	var c requestCarriers

	// A carrier that is done and stays in the map, which is what the window
	// looks like from inside bind.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	const token = "a-token-whose-post-has-ended"
	c.contexts.Store(token, ctx)

	seen := contextSeenBy(t, &c, "tools/call", token)

	if seen.Err() == nil {
		t.Error("a call whose POST had already ended ran under a live context")
	}
	if cause := context.Cause(seen); !errors.Is(cause, errCallerGone) {
		t.Errorf("cause = %v, want errCallerGone", cause)
	}
}
