// Package mcpsurface introspects the registered MCP surface for the command-line
// generators.
//
// Several commands under cmd/ need the same thing: the tools and prompts the
// server actually registers, read over a real MCP round-trip rather than
// described by hand, against a configuration that advertises the full capability
// set regardless of what the ambient environment enables. Keeping that in one
// place is what makes the generated artifacts identical on every machine — the
// credential placeholders in DocsConfig in particular, since a missing one
// silently trims the download tool's schema and makes the --check gates pass or
// fail by accident.
package mcpsurface

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
	"github.com/jmrplens/libgen-mcp/internal/prompts"
	"github.com/jmrplens/libgen-mcp/internal/tools"
)

// pageSize is high enough that the whole surface arrives in a single list
// response, so callers never have to paginate.
const pageSize = 2000

// DocsConfig returns the configuration the generated artifacts must describe:
// every source enabled, with a placeholder for each credential-gated one.
//
// The download tool advertises only the sources the ambient config enables
// (unpaywall is off without a contact email, core is off without an API key, and
// LIBGEN_MCP_SOURCES can trim the chain), but generated documentation describes
// the full capability set. A new credential-gated source needs its placeholder
// here, or the committed output differs between a machine that holds the
// credential and one that does not.
func DocsConfig() *config.Config {
	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{}
	}
	cfg.Sources = nil
	if cfg.UnpaywallEmail == "" {
		cfg.UnpaywallEmail = "docs@example.com"
	}
	if cfg.CoreKey == "" {
		cfg.CoreKey = "docs-placeholder-key"
	}
	return cfg
}

// Session creates an in-memory MCP server+client pair, applies setup to the
// server, and returns the connected client session together with a cleanup
// function the caller must invoke.
func Session(setup func(*mcp.Server) error) (session *mcp.ClientSession, cleanup func(), err error) {
	opts := &mcp.ServerOptions{PageSize: pageSize}
	server := mcp.NewServer(&mcp.Implementation{Name: "mcpsurface", Version: "0.0.1"}, opts)
	if setupErr := setup(server); setupErr != nil {
		return nil, nil, setupErr
	}

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()

	serverSession, err := server.Connect(ctx, st, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("server connect: %w", err)
	}

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "mcpsurface-client", Version: "0.0.1"}, nil)
	session, err = mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		_ = serverSession.Close()
		_ = serverSession.Wait()
		return nil, nil, fmt.Errorf("client connect: %w", err)
	}

	return session, func() {
		_ = session.Close()
		_ = serverSession.Wait()
	}, nil
}

// Tools returns the registered tools via a real tools/list round-trip, in
// registration order. Construction is offline-safe: config.Load and
// mirrors.NewManager perform no network I/O, and the client is never asked to
// make a request.
func Tools(cfg *config.Config) ([]*mcp.Tool, error) {
	session, cleanup, err := clientSession(cfg, func(s *mcp.Server, c *libgen.Client, cf *config.Config) {
		tools.Register(s, c, cf)
	})
	if err != nil {
		return nil, err
	}
	defer cleanup()

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	return result.Tools, nil
}

// Prompts returns the registered prompts via a real prompts/list round-trip.
// The prompts are half of what a client sees on connect, and describing them by
// hand drifts the same way a hand-written tool list would.
func Prompts(cfg *config.Config) ([]*mcp.Prompt, error) {
	session, cleanup, err := clientSession(cfg, prompts.Register)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	result, err := session.ListPrompts(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("list prompts: %w", err)
	}
	return result.Prompts, nil
}

// clientSession wires a libgen client onto an in-memory server using register,
// which is either tools.Register or prompts.Register.
func clientSession(cfg *config.Config, register func(*mcp.Server, *libgen.Client, *config.Config)) (*mcp.ClientSession, func(), error) {
	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("create mirror manager: %w", err)
	}
	client := libgen.New(mgr, cfg)

	return Session(func(server *mcp.Server) error {
		register(server, client, cfg)
		return nil
	})
}

// ProjectRoot walks up from the working directory to the directory holding
// go.mod, so a generator works from anywhere in the repository.
func ProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found in any parent directory")
		}
		dir = parent
	}
}
