package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDecodeFrame_ReadsBothShapesThisServerAnswersWith verifies the two bodies a
// plain POST can come back as.
//
// Both are correct and the default is the one that surprises: this server
// answers with an event stream unless it was started with --json-response, so a
// driver that only read bare JSON would measure the path a deployment does not
// take.
func TestDecodeFrame_ReadsBothShapesThisServerAnswersWith(t *testing.T) {
	testCases := []struct {
		name, body string
		wantErr    bool
		wantRPCErr bool
	}{
		{name: "a bare document", body: `{"jsonrpc":"2.0","id":1,"result":{}}`},
		{
			name: "one server-sent event",
			body: "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n",
		},
		{
			name:       "an event carrying an error",
			body:       "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-32601,\"message\":\"no such method\"}}\n",
			wantRPCErr: true,
		},
		{name: "a body with no document in it", body: "event: ping\n\n", wantErr: true},
		{name: "an empty body", body: "", wantErr: true},
		{name: "a document that is not JSON", body: "{oh no", wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeFrame([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeFrame: %v", err)
			}
			if (got.Error != nil) != tc.wantRPCErr {
				t.Errorf("error member = %+v, want present %t", got.Error, tc.wantRPCErr)
			}
		})
	}
}

// TestToolFailed_TellsAToolFailureFromAFrameThatArrived verifies the check that
// keeps this command from measuring an error path.
//
// A search that cannot reach its catalog answers in under a millisecond with a
// perfectly good JSON-RPC frame carrying isError, and a driver that only looked
// at the envelope would publish that as the cost of a search. This one did,
// until the guard was added and immediately stopped a run.
func TestToolFailed_TellsAToolFailureFromAFrameThatArrived(t *testing.T) {
	testCases := []struct {
		name, result string
		wantFailed   bool
		wantSaid     string
	}{
		{name: "a result that worked", result: `{"content":[{"type":"text","text":"25 results"}]}`},
		{
			name:       "a tool that reported a failure",
			result:     `{"isError":true,"content":[{"type":"text","text":"mirror unreachable"}]}`,
			wantFailed: true, wantSaid: "mirror unreachable",
		},
		{
			name:       "a failure with two blocks",
			result:     `{"isError":true,"content":[{"type":"text","text":"one"},{"type":"text","text":"two"}]}`,
			wantFailed: true, wantSaid: "one two",
		},
		{name: "a failure with nothing to say", result: `{"isError":true}`, wantFailed: true},
		{name: "no result at all", result: ""},
		{name: "a result that is not an object", result: `"a string"`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			failed, said := toolFailed(json.RawMessage(tc.result))
			if failed != tc.wantFailed {
				t.Errorf("toolFailed() = %t, want %t", failed, tc.wantFailed)
			}
			if said != tc.wantSaid {
				t.Errorf("toolFailed() said %q, want %q", said, tc.wantSaid)
			}
		})
	}
}

// TestTruncate_ShortensWithoutHidingWhatItIs verifies a quoted fragment names
// what came back without reproducing a whole results page in the terminal.
func TestTruncate_ShortensWithoutHidingWhatItIs(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate() = %q, want the whole string", got)
	}
	got := truncate(strings.Repeat("x", 50), 10)
	if len(got) != 13 || !strings.HasSuffix(got, "...") {
		t.Errorf("truncate() = %q, want ten characters and an ellipsis", got)
	}
}

// TestHTTPCaller_PresentsItsAddressAndTimesTheRoundTrip verifies the header that
// makes a concurrency series mean anything.
//
// This server charges a caller by the address its request arrives from, and
// every request here arrives from loopback, so without X-Real-IP every client
// would be one caller and the series would measure nothing.
func TestHTTPCaller_PresentsItsAddressAndTimesTheRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("X-Real-IP")+" "+r.Header.Get("MCP-Protocol-Version"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)
	}))
	t.Cleanup(srv.Close)

	c := newHTTPCaller(srv.URL, "198.51.100.7")
	t.Cleanup(c.close)

	got, err := c.call(t.Context(), methodToolsList, map[string]any{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got.Bytes == 0 || got.Duration <= 0 {
		t.Errorf("call() = %+v, want bytes and a duration", got)
	}
	if got.Err != nil || got.ToolError || got.Throttled {
		t.Errorf("call() reported a failure on a good response: %+v", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "198.51.100.7 "+protocolVersion {
		t.Errorf("the server saw %v, want the caller's address and the protocol version", seen)
	}
}

// TestHTTPCaller_TellsARefusalFromAService verifies a throttled call is recorded
// as a refusal rather than as a fast success, because a sizing number that
// counted refusals as service would say the server is quicker the busier it is.
func TestHTTPCaller_TellsARefusalFromAService(t *testing.T) {
	testCases := []struct {
		name          string
		status        int
		body          string
		wantThrottled bool
		wantErr       bool
	}{
		{name: "too many requests", status: http.StatusTooManyRequests, body: "slow down", wantThrottled: true},
		{name: "a server error", status: http.StatusInternalServerError, body: "boom", wantErr: true},
		{name: "a body that is not a frame", status: http.StatusOK, body: "not a frame", wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(srv.Close)

			c := newHTTPCaller(srv.URL, "198.51.100.1")
			t.Cleanup(c.close)

			got, err := c.call(t.Context(), methodToolsList, map[string]any{})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if got.Throttled != tc.wantThrottled {
				t.Errorf("Throttled = %t, want %t", got.Throttled, tc.wantThrottled)
			}
		})
	}
}

// TestHTTPCaller_ReportsAServerItCannotReach verifies a dead endpoint is an
// error rather than a zero-millisecond call.
func TestHTTPCaller_ReportsAServerItCannotReach(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	c := newHTTPCaller(url, "198.51.100.1")
	t.Cleanup(c.close)
	if _, err := c.call(t.Context(), methodToolsList, map[string]any{}); err == nil {
		t.Error("expected an error against a closed server")
	}
}

// fakeTarget wires a target to a pipe pair a test writes the replies on, so the
// stdio driver can be exercised without starting a process.
func fakeTarget(t *testing.T) (*target, *bufio.Reader, io.WriteCloser) {
	t.Helper()
	toServer, fromClient := io.Pipe()
	toClient, fromServer := io.Pipe()
	t.Cleanup(func() {
		_ = toServer.Close()
		_ = fromClient.Close()
		_ = toClient.Close()
		_ = fromServer.Close()
	})
	tgt := &target{stdin: fromClient, stdout: bufio.NewReader(toClient), stderr: &strings.Builder{}}
	return tgt, bufio.NewReader(toServer), fromServer
}

// TestStdioCaller_AnswersTheHandshakeAndSkipsNotifications verifies the two
// things the pipe driver has to get right: the binding's handshake is completed
// before anything is timed, and a notification arriving between a request and
// its reply is skipped rather than timed as the answer.
func TestStdioCaller_AnswersTheHandshakeAndSkipsNotifications(t *testing.T) {
	tgt, serverIn, serverOut := fakeTarget(t)

	// sequential: the server side answers each frame the caller writes, in the
	// order it writes them, so it cannot be a table of independent cases.
	go func() {
		for {
			line, err := serverIn.ReadBytes('\n')
			if err != nil {
				return
			}
			if strings.Contains(string(line), `"notifications/initialized"`) {
				continue
			}
			if strings.Contains(string(line), `"tools/list"`) {
				// A notification first, which the reader must skip.
				_, _ = serverOut.Write([]byte("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n"))
			}
			_, _ = serverOut.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n"))
		}
	}()

	c, err := newStdioCaller(t.Context(), tgt)
	if err != nil {
		t.Fatalf("newStdioCaller: %v", err)
	}
	got, err := c.call(t.Context(), methodToolsList, map[string]any{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got.Bytes == 0 || got.Err != nil {
		t.Errorf("call() = %+v, want a clean reply", got)
	}
}

// TestReadReply_StopsWhenTheContextDoes verifies a driver waiting on a server
// that never answers gives up with the run rather than blocking it forever.
func TestReadReply_StopsWhenTheContextDoes(t *testing.T) {
	_, _, _ = fakeTarget(t)
	silent, _ := io.Pipe()
	t.Cleanup(func() { _ = silent.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := readReply(ctx, bufio.NewReader(silent)); err == nil {
		t.Error("expected the context error from a server that never answers")
	}
}

// TestSearchArguments_MatchTheToolsSchema verifies the one tool call the matrix
// makes uses the field names the search tool declares.
//
// The first version of this command sent topic and limit, which the schema
// refuses, so every tools/call came back with isError and the timings were an
// error path. The names are asserted here because nothing else would notice:
// the call still completes, quickly, and looks like a measurement.
func TestSearchArguments_MatchTheToolsSchema(t *testing.T) {
	for _, field := range []string{"query", "topics", "results_per_page", "extra_sources"} {
		t.Run(field, func(t *testing.T) {
			if _, ok := searchArguments[field]; !ok {
				t.Errorf("searchArguments has no %q", field)
			}
		})
	}
	if searchArguments["extra_sources"] != "never" {
		t.Errorf("extra_sources = %v, want never so the call stays inside the stand-in catalog",
			searchArguments["extra_sources"])
	}
}
