// libgen-mcp es un servidor MCP para buscar y descargar de Library Genesis.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
	"github.com/jmrplens/libgen-mcp/internal/tools"
)

const version = "0.1.0"

func main() {
	httpAddr := flag.String("http", "", "serve streamable HTTP on this address (e.g. :8080) instead of stdio")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if err := run(*httpAddr); err != nil {
		log.Fatal(err)
	}
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
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
		log.Printf("libgen-mcp %s listening on %s (streamable HTTP)", version, httpAddr)
		return http.ListenAndServe(httpAddr, handler)
	}
	fmt.Fprintf(os.Stderr, "libgen-mcp %s serving on stdio\n", version)
	return server.Run(context.Background(), &mcp.StdioTransport{})
}
