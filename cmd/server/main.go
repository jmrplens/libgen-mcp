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

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/logging"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
	"github.com/jmrplens/libgen-mcp/internal/prompts"
	"github.com/jmrplens/libgen-mcp/internal/tools"
)

// httpShutdownTimeout bounds how long a graceful HTTP shutdown may take before
// in-flight connections are forcibly closed.
const httpShutdownTimeout = 5 * time.Second

// version and commit are injected at release time with
// -ldflags "-X main.version=<v> -X main.commit=<sha>".
var (
	version = "1.0.0"
	commit  = "none"
)

func main() {
	// Wrap the real logic so deferred cleanup (signal reset) runs before exit;
	// this avoids log.Fatal skipping defers on the error path.
	os.Exit(mainWithExit())
}

// mainWithExit parses flags, wires the signal context and runs the server,
// returning the process exit code.
func mainWithExit() int {
	httpAddr := flag.String("http", "", "serve streamable HTTP on this address (e.g. :8080) instead of stdio")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("libgen-mcp %s (commit %s)\n", version, commit)
		return 0
	}

	// Cancel the root context on the first SIGINT/SIGTERM so both transports can
	// shut down gracefully; a second signal restores the default behavior.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, *httpAddr); err != nil && !isCleanShutdown(err) {
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

func run(ctx context.Context, httpAddr string) error {
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
	server := mcp.NewServer(&mcp.Implementation{Name: "libgen-mcp", Version: version}, nil)
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
		return serveHTTP(ctx, server, httpAddr)
	}
	fmt.Fprintf(os.Stderr, "libgen-mcp %s (commit %s) serving on stdio\n", version, commit)
	return server.Run(ctx, &mcp.StdioTransport{})
}

// serveHTTP runs the streamable HTTP transport and shuts it down gracefully when
// ctx is canceled, tolerating the expected http.ErrServerClosed.
func serveHTTP(ctx context.Context, server *mcp.Server, httpAddr string) error {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	log.Printf("libgen-mcp %s (commit %s) listening on %s (streamable HTTP)", version, commit, httpAddr)
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
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return nil
	}
}
