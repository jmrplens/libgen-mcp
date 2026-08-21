// Command libgen-mcp is an MCP server for searching and downloading from Library Genesis.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/cachehints"
	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/logging"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
	"github.com/jmrplens/libgen-mcp/internal/prompts"
	"github.com/jmrplens/libgen-mcp/internal/tools"
	"github.com/jmrplens/libgen-mcp/internal/transport"
	buildversion "github.com/jmrplens/libgen-mcp/internal/version"
)

// httpShutdownTimeout bounds how long a graceful HTTP shutdown may take before
// in-flight connections are forcibly closed.
const httpShutdownTimeout = 5 * time.Second

// version and commit are injected at release time with
// -ldflags "-X main.version=<v> -X main.commit=<sha>".
//
// version is empty rather than a literal, and deliberately not initialized from
// libgenmcp.Version: -X sets a variable's initial value, but a variable with a
// runtime initializer is overwritten by package init afterwards, which would
// silently discard the tag goreleaser stamps. Empty means "nobody stamped one",
// and buildversion.Set leaves the number compiled in from VERSION in place.
var (
	version = ""
	commit  = "none"
)

func main() {
	// Before anything else, and before any request can be made: the release
	// ldflags stamp this package's version, and internal/version is what builds
	// the User-Agent every outbound request carries.
	buildversion.Set(version)
	// Wrap the real logic so deferred cleanup (signal reset) runs before exit;
	// this avoids log.Fatal skipping defers on the error path.
	os.Exit(mainWithExit())
}

// mainWithExit parses flags, wires the signal context and runs the server,
// returning the process exit code.
func mainWithExit() int {
	httpAddr := flag.String("http", "", "serve streamable HTTP on this address (e.g. :8080) instead of stdio")
	showVersion := flag.Bool("version", false, "print version and exit")
	stateless := flag.Bool("stateless", true, "stateless streamable HTTP (default; required for MCP protocol 2026-07-28): no Mcp-Session-Id, each POST self-contained, GET/DELETE return 405; use -stateless=false for legacy stateful sessions")
	jsonResponse := flag.Bool("json-response", false, "return application/json responses instead of text/event-stream (SSE)")
	maxBody := flag.Int64("max-request-body-bytes", 0, "maximum streamable HTTP request body size in bytes; 0 uses the SDK default (4 MiB)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("libgen-mcp %s (commit %s)\n", buildversion.Current(), commit)
		return 0
	}

	// A negative cap disables the SDK limit outright, which must not be reachable
	// from a flag: this server is meant to face untrusted clients.
	if *maxBody < 0 {
		log.Printf("--max-request-body-bytes must be >= 0, got %d", *maxBody)
		return 1
	}

	// Cancel the root context on the first SIGINT/SIGTERM so both transports can
	// shut down gracefully; a second signal restores the default behavior.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := transport.Options{Stateless: *stateless, JSONResponse: *jsonResponse, MaxRequestBodyBytes: *maxBody}
	if err := run(ctx, *httpAddr, opts); err != nil && !isCleanShutdown(err) {
		log.Print(err)
		return 1
	}
	return 0
}

// isCleanShutdown reports whether err represents a normal shutdown of the MCP
// client: nil, io.EOF (stdin closed) or context.Canceled.
func isCleanShutdown(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled)
}

// newMCPServer builds the bare MCP server with its receiving middleware in
// place; the caller registers the tools and prompts on top.
func newMCPServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "libgen-mcp", Version: buildversion.Current()}, nil)
	// The catalog is identical for every client and only changes with a release,
	// so tell clients how long they may hold on to it (SEP-2549).
	server.AddReceivingMiddleware(cachehints.Middleware())
	return server
}

// newHTTPHandler mounts the MCP handler at / and exposes GET /health.
func newHTTPHandler(mcpHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
	})
	mux.Handle("/", mcpHandler)
	return mux
}

func run(ctx context.Context, httpAddr string, opts transport.Options) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if vErr := cfg.Validate(); vErr != nil {
		return vErr
	}
	// Install the global slog logger before serving so every log line goes to
	// stderr (stdout is reserved for the stdio MCP transport).
	logging.Setup(cfg.LogLevel)

	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		return err
	}
	client := libgen.New(mgr, cfg)
	server := newMCPServer()
	// When the server can't write to the client's disk, the download tool returns a
	// link to fetch instead of saving a file. That's the case in HTTP mode, and also
	// for a hosted stdio deployment (e.g. behind mcp-proxy) that opts in via
	// LIBGEN_MCP_REMOTE_DOWNLOADS, since its filesystem is unreachable/ephemeral too.
	var regOpts []tools.RegisterOption
	if httpAddr != "" || cfg.RemoteDownloads {
		regOpts = append(regOpts, tools.WithRemoteDownloads())
	}
	tools.Register(server, client, cfg, regOpts...)
	prompts.Register(server, client, cfg)

	if httpAddr != "" {
		return serveHTTP(ctx, server, httpAddr, opts)
	}
	fmt.Fprintf(os.Stderr, "libgen-mcp %s (commit %s) serving on stdio\n", buildversion.Current(), commit)
	return server.Run(ctx, &mcp.StdioTransport{})
}

// serveHTTP runs the streamable HTTP transport and shuts it down gracefully when
// ctx is canceled, tolerating the expected http.ErrServerClosed. Connections
// still streaming after httpShutdownTimeout are closed outright rather than
// waited on.
func serveHTTP(ctx context.Context, server *mcp.Server, httpAddr string, opts transport.Options) error {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, transport.StreamableHTTP(opts))
	log.Printf("libgen-mcp %s (commit %s) listening on %s (streamable HTTP, stateless=%t, json-response=%t)",
		buildversion.Current(), commit, httpAddr, opts.Stateless, opts.JSONResponse)
	if !opts.Stateless {
		log.Print("stateless mode is off: legacy compatibility transport, clients negotiate MCP protocol 2025-11-25 or older")
	}
	// ReadHeaderTimeout guards against Slowloris; body/write timeouts stay
	// unset so long-lived streamable HTTP (SSE) sessions are not cut short.
	srv := &http.Server{
		Addr:              httpAddr,
		Handler:           newHTTPHandler(mcpHandler),
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		// ctx is already canceled here, so derive the shutdown deadline from a
		// cancellation-stripped copy of ctx (preserving its values) rather than
		// the dead parent, keeping graceful shutdown bounded by httpShutdownTimeout.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), httpShutdownTimeout)
		defer cancel()
		err := srv.Shutdown(shutdownCtx)
		if err == nil {
			return nil
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		// Shutdown waits for every connection to go idle, and a streamable HTTP
		// (SSE) stream never does on its own: a client attached when the signal
		// arrives holds the graceful phase open until the deadline. That is an
		// expected shutdown, not a failure — cut the remaining connections so the
		// listener is released instead of leaking it and reporting an error.
		log.Printf("graceful shutdown exceeded %s with streams still open; closing remaining connections", httpShutdownTimeout)
		return srv.Close()
	}
}
