package mcpsurface

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestDocsConfig_ForcesFullSurface checks that the ambient environment cannot
// trim what the generators describe: sources are cleared and every
// credential-gated one gets a placeholder.
func TestDocsConfig_ForcesFullSurface(t *testing.T) {
	t.Setenv("LIBGEN_MCP_SOURCES", "libgen")
	t.Setenv("LIBGEN_MCP_UNPAYWALL_EMAIL", "")
	t.Setenv("LIBGEN_MCP_CORE_KEY", "")

	cfg := DocsConfig()
	if cfg.Sources != nil {
		t.Errorf("Sources should be cleared to advertise every source, got %v", cfg.Sources)
	}
	if cfg.UnpaywallEmail == "" {
		t.Error("UnpaywallEmail placeholder missing: the unpaywall source would drop out of the schema")
	}
	if cfg.CoreKey == "" {
		t.Error("CoreKey placeholder missing: the core source would drop out of the schema")
	}
}

// TestDocsConfig_KeepsRealCredentials verifies a configured credential is left
// alone, so the placeholder only fills a gap.
func TestDocsConfig_KeepsRealCredentials(t *testing.T) {
	t.Setenv("LIBGEN_MCP_UNPAYWALL_EMAIL", "real@example.org")

	if got := DocsConfig().UnpaywallEmail; got != "real@example.org" {
		t.Errorf("UnpaywallEmail = %q, want the configured value", got)
	}
}

// TestTools returns the four registered tools with their schemas attached.
func TestTools(t *testing.T) {
	list, err := Tools(DocsConfig())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("got %d tools, want 4", len(list))
	}
	for _, tool := range list {
		if tool.Name == "" || tool.Description == "" || tool.InputSchema == nil {
			t.Errorf("tool %q is missing name, description or inputSchema", tool.Name)
		}
	}
}

// TestPrompts returns the registered prompts over a real round-trip.
func TestPrompts(t *testing.T) {
	list, err := Prompts(DocsConfig())
	if err != nil {
		t.Fatalf("Prompts: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("got no prompts, want the registered set")
	}
	for _, p := range list {
		if p.Name == "" || p.Description == "" {
			t.Errorf("prompt %q is missing name or description", p.Name)
		}
	}
}

// TestSession_SetupError checks a failing setup aborts before any connection is
// made, rather than returning a session the caller would have to clean up.
func TestSession_SetupError(t *testing.T) {
	want := errors.New("setup failed")
	session, cleanup, err := Session(func(*mcp.Server) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if session != nil || cleanup != nil {
		t.Error("a failed setup must return no session and no cleanup")
	}
}

// TestProjectRoot finds the directory holding go.mod from the package directory.
func TestProjectRoot(t *testing.T) {
	root, err := ProjectRoot()
	if err != nil {
		t.Fatalf("ProjectRoot: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "go.mod")); statErr != nil {
		t.Errorf("ProjectRoot returned %q, which holds no go.mod: %v", root, statErr)
	}
}

// TestProjectRoot_NotFound covers the walk running out of parents.
func TestProjectRoot_NotFound(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if _, err := ProjectRoot(); err == nil {
		t.Fatal("expected an error in a tree without go.mod, got nil")
	}
}
