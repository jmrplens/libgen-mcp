//go:build httpe2e

package httpe2e

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// A hosted server reaches the mirrors over an egress IP shared by everyone it
// serves, so it does not pull whole files: LIBGEN_MCP_SERVER_FETCH defaults to
// off under --http, and with it off the read tool — the one tool that cannot
// work without fetching the file — is not registered at all.
//
// These cases drive the REAL binary, because that default is decided in package
// main from the flags it was started with. A unit test would be asserting its own
// reassembly of that decision, and the surface a deployment actually advertises
// is precisely what this is about.

// listedToolNames returns the tool names in a tools/list reply.
func listedToolNames(t *testing.T, reply response) []string {
	t.Helper()

	var payload struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	frame := jsonFrame(reply.body)
	if err := json.Unmarshal([]byte(frame), &payload); err != nil {
		t.Fatalf("tools/list reply is not JSON-RPC: %v (%s)", err, truncate(reply.body))
	}
	names := make([]string, 0, len(payload.Result.Tools))
	for _, tool := range payload.Result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// TestHTTPHidesReadByDefault asserts the shipped default: an --http deployment
// advertises the three tools that move no file body, and not read.
func TestHTTPHidesReadByDefault(t *testing.T) {
	s := startServer(t, nil)

	names := listedToolNames(t, s.do(t, request{body: toolsListBody}))
	if slices.Contains(names, "read") {
		t.Errorf("an --http server advertises read by default; tools = %v", names)
	}
	for _, want := range []string{"search", "get_details", "download"} {
		if !slices.Contains(names, want) {
			t.Errorf("%s is missing; tools = %v", want, names)
		}
	}
}

// TestHTTPReadIsUnreachableWhenHidden asserts the tool is gone rather than
// merely unlisted: calling it is refused by the protocol, so no fetch can be
// started by a client that guesses the name.
func TestHTTPReadIsUnreachableWhenHidden(t *testing.T) {
	s := startServer(t, nil)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call",` +
		`"params":{"name":"read","arguments":{"md5":"87a4ebdaf21fa6cc70009a3dd63194ee"}}}`
	reply := s.do(t, request{body: body})
	frame := jsonFrame(reply.body)
	if !strings.Contains(frame, `"error"`) {
		t.Errorf("calling the hidden read tool was not refused: %s", truncate(reply.body))
	}
	if !s.healthy(t) {
		t.Error("the server stopped serving after a call to a hidden tool")
	}
}

// TestHTTPServesReadWhenFetchEnabled covers the operator opt-in: a deployment
// that can afford the egress turns fetching on and gets read back.
func TestHTTPServesReadWhenFetchEnabled(t *testing.T) {
	s := startServer(t, map[string]string{"LIBGEN_MCP_SERVER_FETCH": "1"})

	names := listedToolNames(t, s.do(t, request{body: toolsListBody}))
	if !slices.Contains(names, "read") {
		t.Errorf("read is missing though LIBGEN_MCP_SERVER_FETCH=1; tools = %v", names)
	}
}

// TestHTTPRejectsNonBooleanServerFetch asserts a typo fails the start rather
// than being read as one value or the other, the way every other boolean does.
// A setting that decides whether this deployment reaches the mirrors for whole
// files must not be able to default silently because it was misspelled.
func TestHTTPRejectsNonBooleanServerFetch(t *testing.T) {
	// runServerExpectingExit passes the parent environment through, so this
	// reaches the child. The address is never bound: configuration is loaded
	// before the listener, so the refusal happens first.
	t.Setenv("LIBGEN_MCP_SERVER_FETCH", "banana")

	out, err := runServerExpectingExit(t, "--http", "127.0.0.1:0")
	if err == nil {
		t.Fatalf("the server started with a non-boolean LIBGEN_MCP_SERVER_FETCH. Output:\n%s", out)
	}
	if !strings.Contains(out, "LIBGEN_MCP_SERVER_FETCH") {
		t.Errorf("the refusal does not name the variable that caused it:\n%s", out)
	}
}
