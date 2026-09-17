// sse.go keeps a streamed MCP response alive on its way through a proxy.
//
// Three things have to be true for a long tool call to survive the trip, and
// all three are decided from the **response** rather than from the request's
// Accept header. That is the correction this file is: the SDK answers
// text/event-stream to `*/*` and to `text/*` as well, which is curl's default
// and several HTTP libraries', so a test on the literal "text/event-stream" in
// Accept missed exactly the clients most likely to be used to try the endpoint
// out. They received a real SSE stream with no anti-buffering header at all.
//
// Widening the Accept test instead would have been the wrong fix, because the
// other two things this does must NOT apply to an ordinary route.

package main

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// sseKeepAliveInterval is how long a stream may stay silent before a comment
// frame is written.
//
// Clearing this end's write deadline keeps the server from timing the stream
// out; it does nothing about the hops in between. nginx closes an idle upstream
// response at proxy_read_timeout — 60 seconds by default — and load balancers
// and mobile carrier NATs are less generous still. The streams here are
// legitimately silent for that long: a resolve may spend LIBGEN_MCP_RESOLVE_BUDGET
// (30s by default) before the first progress notification, and a download that
// is transferring but not reporting may go LIBGEN_MCP_DOWNLOAD_STALL_TIMEOUT
// (60s) between them. 25 seconds leaves room for two frames inside the tightest
// of those windows.
//
// A var rather than a const only so tests can shorten it; nothing at runtime
// writes it.
var sseKeepAliveInterval = 25 * time.Second

// sseKeepAliveFrame is a comment: a line beginning with ':' carries no field, so
// a conforming SSE reader — the MCP go-sdk client's included — discards it
// without producing an event. It exists to put bytes on the wire.
var sseKeepAliveFrame = []byte(": keep-alive\n\n")

// sseAware wraps the MCP endpoint so a response that turns out to be a stream is
// treated as one.
//
// It is mounted on the endpoint alone, which is what makes clearing the write
// deadline safe: /health, the server card and the 404 keep the server's
// WriteTimeout as their slow-reader guard, and they are small bodies that have
// no business taking a minute to write.
func sseAware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writer := &sseAwareWriter{ResponseWriter: w, disconnected: r.Context().Done()}
		defer writer.stopKeepAlive()
		next.ServeHTTP(writer, r)
	})
}

// sseAwareWriter clears the write deadline when a response commits, and adds the
// anti-buffering header and a keep-alive when that response is a stream.
type sseAwareWriter struct {
	http.ResponseWriter
	// disconnected is the request context's Done channel rather than the
	// context itself: the heartbeat needs only the cancellation signal, and a
	// Context stored in a struct outlives the call it belongs to.
	disconnected <-chan struct{}
	wroteHeader  bool

	// mu serializes the handler's writes with the keep-alive goroutine's. An
	// http.ResponseWriter is not safe for concurrent use, and the SDK emits one
	// event per Write, so holding this around each write is what keeps a
	// keep-alive frame from landing in the middle of an event.
	mu        sync.Mutex
	lastWrite time.Time
	stopped   bool
	stop      chan struct{}
	done      chan struct{}
}

// Unwrap exposes the underlying writer so [http.NewResponseController] — which
// the SDK uses to set deadlines — reaches the real connection rather than
// stopping at this wrapper.
//
// **Flush deliberately does not unwrap.** This type implements it, so the
// controller finds it here and the flush takes the same lock the writes do; let
// the controller reach past this wrapper and a flush races the keep-alive
// goroutine on a writer that is not safe for concurrent use.
func (w *sseAwareWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush pushes buffered bytes under the write lock.
func (w *sseAwareWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// WriteHeader applies the treatment once, when the status and the content type
// are committed.
func (w *sseAwareWriter) WriteHeader(code int) {
	streaming := false
	if !w.wroteHeader {
		w.wroteHeader = true
		// The deadline goes for every response of this endpoint, not only for a
		// stream. A tool call is legitimately long in both modes — a download
		// under --json-response answers once, at the end, and the wall-clock cap
		// on one call defaults to an hour — so a WriteTimeout that survived here
		// would sever exactly the calls this server exists to make. It is the
		// ordinary routes that keep the guard.
		//
		// Zero time clears it. Best-effort: ignored on transports that manage
		// their own deadlines, HTTP/2 among them.
		_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Time{})
		if isEventStream(w.Header().Get(headerContentType)) {
			streaming = true
			// The transport spec makes this a SHOULD: an nginx-class proxy
			// otherwise accumulates events in a buffer instead of forwarding
			// them, and these streams carry notifications/progress during a
			// multi-megabyte transfer — so the events a caller is watching
			// would all arrive at once, when the transfer finished.
			w.Header().Set("X-Accel-Buffering", "no")
		}
	}
	w.ResponseWriter.WriteHeader(code)
	if streaming {
		w.startKeepAlive()
	}
}

// Write covers the streamed POST response, which never calls WriteHeader
// explicitly — so this is the only hook that fires for a long tool call.
func (w *sseAwareWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastWrite = time.Now()
	return w.ResponseWriter.Write(b)
}

// startKeepAlive runs the idle-stream heartbeat until the handler returns or the
// client goes away. It is called from the handler goroutine, once, after the
// header is on the wire.
func (w *sseAwareWriter) startKeepAlive() {
	w.stop = make(chan struct{})
	w.done = make(chan struct{})
	stop, done := w.stop, w.done
	go func() {
		defer close(done)
		ticker := time.NewTicker(sseKeepAliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-w.disconnected:
				return
			case <-ticker.C:
				if !w.writeKeepAlive() {
					return
				}
			}
		}
	}()
}

// writeKeepAlive emits one comment frame and reports whether the heartbeat
// should continue. A stream that has written recently is not idle, so it is
// skipped rather than padded.
func (w *sseAwareWriter) writeKeepAlive() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return false
	}
	if !w.lastWrite.IsZero() && time.Since(w.lastWrite) < sseKeepAliveInterval {
		return true
	}
	if _, err := w.ResponseWriter.Write(sseKeepAliveFrame); err != nil {
		return false
	}
	w.lastWrite = time.Now()
	_ = http.NewResponseController(w.ResponseWriter).Flush()
	return true
}

// stopKeepAlive ends the heartbeat and waits for the goroutine to exit.
//
// Closing stop is what ends it; the wait closes the last window, which the lock
// alone does not. Once the handler returns, net/http is free to recycle the
// ResponseWriter — and a heartbeat goroutine parked on its select could still
// take the freed mutex and write to it. That is a race rather than a behavior,
// so it is the detector that would catch a regression here, not an assertion:
// TestSSEKeepAliveStopsWithTheHandler pins only that the heartbeat stops.
func (w *sseAwareWriter) stopKeepAlive() {
	w.mu.Lock()
	if w.stopped || w.stop == nil {
		w.stopped = true
		w.mu.Unlock()
		return
	}
	w.stopped = true
	close(w.stop)
	done := w.done
	w.mu.Unlock()
	<-done
}

// isEventStream reports whether a Content-Type names the SSE media type,
// ignoring any parameters and case.
func isEventStream(contentType string) bool {
	base, _, _ := strings.Cut(contentType, ";")
	return strings.EqualFold(strings.TrimSpace(base), "text/event-stream")
}
