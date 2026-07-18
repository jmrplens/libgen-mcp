// libgen-mcp es un servidor MCP para buscar y descargar de Library Genesis.
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
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
	"github.com/jmrplens/libgen-mcp/internal/tools"
)

// version y commit se inyectan en release con
// -ldflags "-X main.version=<v> -X main.commit=<sha>".
var (
	version = "0.1.0"
	commit  = "none"
)

func main() {
	httpAddr := flag.String("http", "", "serve streamable HTTP on this address (e.g. :8080) instead of stdio")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("libgen-mcp %s (commit %s)\n", version, commit)
		return
	}
	if err := run(*httpAddr); err != nil && !isCleanShutdown(err) {
		log.Fatal(err)
	}
}

// isCleanShutdown reporta si err representa un cierre normal del cliente MCP:
// nil, io.EOF (stdin cerrado) o context.Canceled.
func isCleanShutdown(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled)
}

// newHTTPHandler monta el handler MCP en / y expone GET /health.
func newHTTPHandler(mcpHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
	})
	mux.Handle("/", mcpHandler)
	return mux
}

func run(httpAddr string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		return err
	}
	client := libgen.New(mgr, cfg.Timeout)
	server := mcp.NewServer(&mcp.Implementation{Name: "libgen-mcp", Version: version}, nil)
	tools.Register(server, client, cfg)

	if httpAddr != "" {
		mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
		log.Printf("libgen-mcp %s (commit %s) listening on %s (streamable HTTP)", version, commit, httpAddr)
		// ReadHeaderTimeout guards against Slowloris; body/write timeouts stay
		// unset so long-lived streamable HTTP (SSE) sessions are not cut short.
		srv := &http.Server{
			Addr:              httpAddr,
			Handler:           newHTTPHandler(mcpHandler),
			ReadHeaderTimeout: 10 * time.Second,
		}
		return srv.ListenAndServe()
	}
	fmt.Fprintf(os.Stderr, "libgen-mcp %s (commit %s) serving on stdio\n", version, commit)
	return server.Run(context.Background(), &mcp.StdioTransport{})
}
