package main

import (
	"regexp"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

// registeredSurface connects to the server newRegisteredServer builds for
// httpAddr and returns the names of the tools it advertises together with the
// handshake Instructions a connecting model receives.
func registeredSurface(t *testing.T, httpAddr string) (names []string, instructions string) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	server, err := newRegisteredServer(cfg, httpAddr)
	if err != nil {
		t.Fatalf("newRegisteredServer() error = %v", err)
	}
	st, ct := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	session, err := mcp.NewClient(&mcp.Implementation{Name: "fetch-test", Version: "0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	for tl, tErr := range session.Tools(t.Context(), nil) {
		if tErr != nil {
			t.Fatalf("list tools: %v", tErr)
		}
		names = append(names, tl.Name)
	}
	return names, session.InitializeResult().Instructions
}

// hasTool reports whether the surface advertises the named tool.
func hasTool(names []string, want string) bool { return slices.Contains(names, want) }

// instructionStep matches the numbered workflow lines of the handshake
// Instructions ("3. download — …"), whose leading word is a tool name the text
// is telling the model to call.
var instructionStep = regexp.MustCompile(`(?m)^\d+\. (\w+) —`)

// assertInstructionsMatchSurface checks the handshake text and the tool list
// agree: every tool the Instructions walk a model through must be one the
// server actually registered. The text goes straight into the model's system
// prompt, so naming a tool that is not there is worse than saying nothing —
// the model spends a turn discovering it, exactly the cost hiding the tool was
// meant to avoid.
func assertInstructionsMatchSurface(t *testing.T, names []string, instructions string) {
	t.Helper()
	if instructions == "" {
		t.Fatal("handshake Instructions is empty")
	}
	for _, m := range instructionStep.FindAllStringSubmatch(instructions, -1) {
		if !hasTool(names, m[1]) {
			t.Errorf("Instructions walk the model through %q, which this deployment does not register; tools = %v", m[1], names)
		}
	}
}

// TestRemoteDeploymentHidesRead is the default this change is about: a server
// started with --http does not fetch file bodies, so it neither registers read
// nor tells a model to call it.
func TestRemoteDeploymentHidesRead(t *testing.T) {
	t.Setenv("LIBGEN_MCP_SERVER_FETCH", "")
	names, instructions := registeredSurface(t, ":0")

	if hasTool(names, "read") {
		t.Errorf("a remote server advertises read by default; tools = %v", names)
	}
	for _, want := range []string{"search", "get_details", "download"} {
		if !hasTool(names, want) {
			t.Errorf("%s is missing from a remote surface; tools = %v", want, names)
		}
	}
	assertInstructionsMatchSurface(t, names, instructions)
}

// TestRemoteDeploymentServesReadWhenFetchAllowed covers the operator opt-in: a
// hosted deployment with the egress capacity to spare turns fetching back on and
// gets read back with it.
func TestRemoteDeploymentServesReadWhenFetchAllowed(t *testing.T) {
	t.Setenv("LIBGEN_MCP_SERVER_FETCH", "true")
	names, instructions := registeredSurface(t, ":0")

	if !hasTool(names, "read") {
		t.Errorf("read is missing though fetching was enabled; tools = %v", names)
	}
	assertInstructionsMatchSurface(t, names, instructions)
}

// TestStdioDeploymentServesRead pins the other default: on a local stdio server
// the connection and the disk are the user's own, so fetching stays on.
func TestStdioDeploymentServesRead(t *testing.T) {
	t.Setenv("LIBGEN_MCP_SERVER_FETCH", "")
	names, instructions := registeredSurface(t, "")

	if !hasTool(names, "read") {
		t.Errorf("read is missing from a local stdio surface; tools = %v", names)
	}
	assertInstructionsMatchSurface(t, names, instructions)
}

// TestStdioDeploymentHonorsFetchOff covers the opt-out on a local server: the
// operator's explicit false wins over the stdio default.
func TestStdioDeploymentHonorsFetchOff(t *testing.T) {
	t.Setenv("LIBGEN_MCP_SERVER_FETCH", "0")
	names, instructions := registeredSurface(t, "")

	if hasTool(names, "read") {
		t.Errorf("read is advertised though fetching was disabled; tools = %v", names)
	}
	assertInstructionsMatchSurface(t, names, instructions)
}

// TestRemoteDownloadsImpliesNoFetch covers the third remote trigger: a hosted
// stdio deployment declares itself remote with LIBGEN_MCP_REMOTE_DOWNLOADS, and
// that alone must move read's default with it — its disk and its egress are as
// shared as an HTTP deployment's.
func TestRemoteDownloadsImpliesNoFetch(t *testing.T) {
	t.Setenv("LIBGEN_MCP_SERVER_FETCH", "")
	t.Setenv("LIBGEN_MCP_REMOTE_DOWNLOADS", "1")
	names, instructions := registeredSurface(t, "")

	if hasTool(names, "read") {
		t.Errorf("a hosted stdio server advertises read by default; tools = %v", names)
	}
	assertInstructionsMatchSurface(t, names, instructions)
}
