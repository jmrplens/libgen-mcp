package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingWriter is a ResponseWriter that reports what was done to the real
// connection: the deadlines that were set, and the bytes that were written.
//
// httptest.ResponseRecorder cannot stand in here. It implements neither
// SetWriteDeadline nor Flush, so http.NewResponseController reports
// ErrNotSupported and a writer that never called them would look identical to
// one that did.
type recordingWriter struct {
	header http.Header
	status int

	mu        sync.Mutex
	body      bytes.Buffer
	deadlines []time.Time
	flushes   int
	writeErr  error
}

func newRecordingWriter() *recordingWriter {
	return &recordingWriter{header: http.Header{}}
}

func (w *recordingWriter) Header() http.Header { return w.header }

func (w *recordingWriter) WriteHeader(code int) { w.status = code }

func (w *recordingWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.body.Write(b)
}

// SetWriteDeadline is what http.NewResponseController reaches through Unwrap.
func (w *recordingWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadlines = append(w.deadlines, t)
	return nil
}

func (w *recordingWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushes++
}

// written returns the bytes the handler and the heartbeat put on the wire.
func (w *recordingWriter) written() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

// clearedDeadline reports whether the zero time — which clears it — was set.
func (w *recordingWriter) clearedDeadline() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, d := range w.deadlines {
		if d.IsZero() {
			return true
		}
	}
	return false
}

// withKeepAliveInterval shortens the heartbeat for one test and puts it back.
func withKeepAliveInterval(t *testing.T, d time.Duration) {
	t.Helper()
	previous := sseKeepAliveInterval
	t.Cleanup(func() { sseKeepAliveInterval = previous })
	sseKeepAliveInterval = d
}

// serveSSE drives handler through the middleware against a recording writer.
func serveSSE(t *testing.T, handler http.HandlerFunc) *recordingWriter {
	t.Helper()
	rec := newRecordingWriter()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader("{}"))
	sseAware(handler).ServeHTTP(rec, req)
	return rec
}

// TestSSEHeaderFollowsTheResponseNotTheRequest is the correction this file is.
//
// The old test was on the request's Accept header, and the SDK answers
// text/event-stream to `*/*` and `text/*` as well — curl's default and several
// HTTP libraries'. Those clients got a real stream with no anti-buffering
// header, so a proxy held every progress notification until the transfer
// finished. Each case below sends an Accept that does not name the media type
// and has the handler commit to it anyway.
func TestSSEHeaderFollowsTheResponseNotTheRequest(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		want        bool
	}{
		{name: "a stream", contentType: "text/event-stream", want: true},
		{name: "a stream with parameters", contentType: "text/event-stream; charset=utf-8", want: true},
		{name: "a stream in odd case", contentType: "Text/Event-Stream", want: true},
		{name: "json", contentType: "application/json", want: false},
		{name: "nothing committed", contentType: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveSSE(t, func(w http.ResponseWriter, _ *http.Request) {
				if tc.contentType != "" {
					w.Header().Set(headerContentType, tc.contentType)
				}
				w.WriteHeader(http.StatusOK)
			})

			got := rec.Header().Get("X-Accel-Buffering") == "no"
			if got != tc.want {
				t.Errorf("X-Accel-Buffering set = %v, want %v for Content-Type %q", got, tc.want, tc.contentType)
			}
		})
	}
}

// TestSSEWriteDeadlineIsClearedForEveryResponseOfTheEndpoint covers the half
// that is not about streams.
//
// The listener carries a WriteTimeout, which bounds the whole handler rather
// than the write, so it would sever a tool call that is doing exactly what it
// was asked to do — a download under --json-response answers once, at the end.
// The endpoint clears it in both modes; the ordinary routes are not wrapped and
// keep it.
func TestSSEWriteDeadlineIsClearedForEveryResponseOfTheEndpoint(t *testing.T) {
	for _, contentType := range []string{"text/event-stream", "application/json"} {
		t.Run(contentType, func(t *testing.T) {
			rec := serveSSE(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set(headerContentType, contentType)
				w.WriteHeader(http.StatusOK)
			})
			if !rec.clearedDeadline() {
				t.Errorf("the write deadline was not cleared for a %s response", contentType)
			}
		})
	}
}

// TestSSEWriteCommitsTheResponseOnItsOwn covers the streamed POST, which never
// calls WriteHeader — so Write is the only hook that fires for a long tool call,
// and a wrapper that acted only in WriteHeader would do nothing at all there.
func TestSSEWriteCommitsTheResponseOnItsOwn(t *testing.T) {
	rec := serveSSE(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headerContentType, "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: {}\n\n"))
	})

	if rec.Header().Get("X-Accel-Buffering") != "no" {
		t.Error("a response committed by Write alone did not get the anti-buffering header")
	}
	if !rec.clearedDeadline() {
		t.Error("a response committed by Write alone kept the write deadline")
	}
}

// TestSSEKeepAliveFillsASilentStream is what the heartbeat exists for: nginx
// closes an idle upstream response at proxy_read_timeout, 60 seconds by
// default, and these streams are legitimately silent for that long while a
// resolve or a transfer is under way.
func TestSSEKeepAliveFillsASilentStream(t *testing.T) {
	withKeepAliveInterval(t, 10*time.Millisecond)

	rec := serveSSE(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headerContentType, "text/event-stream")
		w.WriteHeader(http.StatusOK)
		time.Sleep(120 * time.Millisecond)
	})

	if !strings.Contains(rec.written(), string(sseKeepAliveFrame)) {
		t.Fatalf("a silent stream carried no keep-alive: %q", rec.written())
	}
	// A comment frame, so a conforming reader discards it rather than
	// delivering an event nothing sent.
	if !strings.HasPrefix(string(sseKeepAliveFrame), ":") {
		t.Errorf("the keep-alive frame %q is not an SSE comment", sseKeepAliveFrame)
	}
}

// TestSSEKeepAliveDoesNotTouchAJSONResponse is the negative: a non-stream gets
// no heartbeat, because those bytes would be garbage in the middle of a JSON
// body.
func TestSSEKeepAliveDoesNotTouchAJSONResponse(t *testing.T) {
	withKeepAliveInterval(t, 10*time.Millisecond)

	rec := serveSSE(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headerContentType, "application/json")
		w.WriteHeader(http.StatusOK)
		time.Sleep(60 * time.Millisecond)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	})

	if got := rec.written(); got != `{"jsonrpc":"2.0","id":1,"result":{}}` {
		t.Errorf("the JSON body was interleaved with something: %q", got)
	}
}

// TestSSEKeepAliveNeverSplitsAnEvent is the whole reason there is a mutex.
//
// An http.ResponseWriter is not safe for concurrent use, and the SDK emits one
// event per Write, so a heartbeat that raced the handler could land inside an
// event and hand the client a frame that parses as neither. The interval is far
// shorter than the writes here, so the heartbeat is trying to fire throughout.
func TestSSEKeepAliveNeverSplitsAnEvent(t *testing.T) {
	withKeepAliveInterval(t, time.Millisecond)

	const event = "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n"
	const events = 50

	rec := serveSSE(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headerContentType, "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for range events {
			_, _ = w.Write([]byte(event))
			time.Sleep(2 * time.Millisecond)
		}
	})

	// Every frame is separated by a blank line, so removing the keep-alive
	// comments must leave exactly the events that were written, intact.
	body := rec.written()
	rebuilt := strings.ReplaceAll(body, string(sseKeepAliveFrame), "")
	if want := strings.Repeat(event, events); rebuilt != want {
		t.Errorf("the events came back altered once the keep-alives were removed.\n got %q\nwant %q",
			truncateForTest(rebuilt), truncateForTest(want))
	}
}

// truncateForTest keeps a failure message readable.
func truncateForTest(s string) string {
	const limit = 240
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

// TestSSEKeepAliveSkipsAStreamThatIsNotIdle keeps the heartbeat from padding a
// busy stream: a client watching progress events should see the events, not a
// comment between each pair.
//
// The tick is driven directly rather than waited for. Letting the ticker fire
// against a handler writing on a sleep loop makes the assertion a race with the
// scheduler — a runner that stalls the handler past the interval produces a
// keep-alive that is *correct*, and the test then fails for being on a busy
// machine. Widening the gap would only hide it the other way: a ticker that
// never fires at all passes whether or not the idle check exists, which is the
// one thing this case is for. Calling writeKeepAlive at a known idleness asserts
// the decision itself, in both directions.
func TestSSEKeepAliveSkipsAStreamThatIsNotIdle(t *testing.T) {
	const interval = time.Minute
	withKeepAliveInterval(t, interval)

	rec := newRecordingWriter()
	writer := &sseAwareWriter{ResponseWriter: rec, disconnected: t.Context().Done()}
	writer.Header().Set(headerContentType, "text/event-stream")
	writer.WriteHeader(http.StatusOK)
	t.Cleanup(writer.stopKeepAlive)

	event := "event: message\ndata: {}\n\n"
	if _, err := writer.Write([]byte(event)); err != nil {
		t.Fatalf("writing an event: %v", err)
	}

	// A stream that wrote a moment ago is not idle, whatever the ticker says.
	if !writer.writeKeepAlive() {
		t.Fatal("writeKeepAlive() = false, want the heartbeat still running")
	}
	if got := rec.written(); got != event {
		t.Errorf("a stream that had just written was given a keep-alive: %q", truncateForTest(got))
	}

	// Past the interval it is, and the frame goes out — so the case above is the
	// idle check working rather than the heartbeat being off altogether.
	writer.mu.Lock()
	writer.lastWrite = time.Now().Add(-2 * interval)
	writer.mu.Unlock()

	if !writer.writeKeepAlive() {
		t.Fatal("writeKeepAlive() = false for an idle stream, want the frame written")
	}
	if want := event + string(sseKeepAliveFrame); rec.written() != want {
		t.Errorf("an idle stream got %q, want %q", truncateForTest(rec.written()), want)
	}
}

// TestSSEKeepAliveStopsWithTheHandler is what keeps a write from reaching a
// ResponseWriter the server has already finished with.
//
// It asserts the heartbeat stops, which is the deterministic half. The other
// half — that the goroutine has actually exited before the handler's caller
// recycles the writer — is a race rather than a behavior, and the detector is
// what catches a regression in it; see stopKeepAlive.
func TestSSEKeepAliveStopsWithTheHandler(t *testing.T) {
	withKeepAliveInterval(t, 5*time.Millisecond)

	rec := serveSSE(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headerContentType, "text/event-stream")
		w.WriteHeader(http.StatusOK)
		time.Sleep(20 * time.Millisecond)
	})

	settled := rec.written()
	time.Sleep(40 * time.Millisecond)
	if after := rec.written(); after != settled {
		t.Errorf("the heartbeat wrote %d more bytes after the handler returned", len(after)-len(settled))
	}
}

// TestSSEKeepAliveEndsWhenTheClientGoesAway covers the other exit: a POST whose
// caller hung up mid-stream must not leave a goroutine writing to a dead
// connection for the life of the handler.
func TestSSEKeepAliveEndsWhenTheClientGoesAway(t *testing.T) {
	withKeepAliveInterval(t, 5*time.Millisecond)

	rec := newRecordingWriter()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader("{}"))

	sseAware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headerContentType, "text/event-stream")
		w.WriteHeader(http.StatusOK)
		cancel()
		time.Sleep(40 * time.Millisecond)
	})).ServeHTTP(rec, req)

	// The stream is silent throughout, so every byte here would be a keep-alive
	// written after the client was gone.
	if got := rec.written(); got != "" {
		t.Errorf("the heartbeat kept writing after the client disconnected: %q", got)
	}
}

// TestSSEUnwrapReachesTheConnectionButFlushDoesNot pins the pair that a
// reimplementation gets wrong.
//
// Unwrap must expose the real writer, or http.NewResponseController cannot set
// a deadline on it. Flush must NOT be left to the unwrapped writer, because the
// controller would then find the connection's own and flush outside the lock,
// racing the heartbeat on a writer that is not safe for concurrent use.
func TestSSEUnwrapReachesTheConnectionButFlushDoesNot(t *testing.T) {
	inner := newRecordingWriter()
	w := &sseAwareWriter{ResponseWriter: inner}

	if w.Unwrap() != http.ResponseWriter(inner) {
		t.Error("Unwrap does not expose the underlying writer, so no deadline can be set on it")
	}

	// The controller must find this type's own Flush, which takes the lock.
	// Flushing while the lock is held would deadlock if it did not.
	w.mu.Lock()
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		_ = http.NewResponseController(w).Flush()
	}()
	select {
	case <-flushed:
		w.mu.Unlock()
		t.Fatal("a flush went through while the write lock was held, so it bypassed this type's Flush")
	case <-time.After(50 * time.Millisecond):
	}
	w.mu.Unlock()
	<-flushed

	if inner.flushes != 1 {
		t.Errorf("the underlying writer was flushed %d times, want 1", inner.flushes)
	}
}
