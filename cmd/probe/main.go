// probe verifica contra los mirrors reales que todas las rutas que usa
// libgen-mcp siguen funcionando (búsqueda por topic, API JSON, cadena de
// descarga). Uso: go run ./cmd/probe [-mirror https://libgen.li]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
)

// checker acumula el estado de las comprobaciones e imprime su resultado.
type checker struct{ failed bool }

func (c *checker) report(name string, err error, okMsg string) {
	if err != nil {
		c.failed = true
		fmt.Printf("[FAIL] %s: %v\n", name, err)
		return
	}
	fmt.Printf("[OK]   %s: %s\n", name, okMsg)
}

func main() {
	os.Exit(run())
}

func run() int {
	mirror := flag.String("mirror", "", "force a specific mirror base URL")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		fmt.Println("[FAIL] config:", err)
		return 1
	}
	if *mirror != "" {
		cfg.Mirror = *mirror
	}
	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		fmt.Println("[FAIL] mirrors manager:", err)
		return 1
	}
	client := libgen.New(mgr, cfg.Timeout)

	c := &checker{}
	list := mgr.Mirrors(ctx)
	if len(list) == 0 {
		c.report("mirrors", errors.New("no mirrors discovered"), "")
		return 1
	}
	c.report("mirrors", nil, fmt.Sprintf("%d discovered, preferred %s", len(list), list[0]))

	sampleMD5 := c.runSearches(ctx, client)
	if sampleMD5 == "" {
		fmt.Println("[FAIL] no sample md5 available, skipping details/download checks")
		return 1
	}

	file, edition, err := client.DetailsByMD5(ctx, sampleMD5)
	msg := ""
	if err == nil {
		msg = fmt.Sprintf("file fields=%d, edition present=%v", len(file), edition != nil)
	}
	c.report("json.php details", err, msg)

	getURL, base, err := client.ResolveGetURL(ctx, sampleMD5)
	c.report("ads.php key", err, "resolved via "+base)
	if err == nil {
		dlMsg, dlErr := checkDownload(ctx, getURL)
		c.report("CDN download", dlErr, dlMsg)
	}

	if c.failed {
		return 1
	}
	return 0
}

// runSearches ejecuta una búsqueda por cada topic y devuelve el primer md5 útil.
func (c *checker) runSearches(ctx context.Context, client *libgen.Client) string {
	searches := []struct{ topic, query string }{
		{"nonfiction", "golang"},
		{"fiction", "dune"},
		{"articles", "neural network"},
		{"magazines", "science"},
		{"comics", "batman"},
		{"standards", "safety"},
		{"fiction_rus", "мастер"},
	}
	var sampleMD5 string
	for _, s := range searches {
		page, mirrorUsed, serr := client.Search(ctx, libgen.SearchParams{Query: s.query, Topics: []string{s.topic}})
		msg := ""
		if serr == nil {
			msg = fmt.Sprintf("%d results (total %s) via %s", len(page.Results), page.TotalFiles, mirrorUsed)
			if sampleMD5 == "" && len(page.Results) > 0 {
				sampleMD5 = page.Results[0].MD5
			}
			if len(page.Results) == 0 {
				serr = fmt.Errorf("0 results for %q (query too narrow or parser broken)", s.query)
			}
		}
		c.report("search "+s.topic, serr, msg)
	}
	return sampleMD5
}

// checkDownload pide el primer byte del CDN para confirmar que la key resuelve.
func checkDownload(ctx context.Context, getURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, http.NoBody)
	if err != nil {
		return "", err
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	return fmt.Sprintf("status %d, content-disposition present=%v",
		resp.StatusCode, resp.Header.Get("Content-Disposition") != ""), nil
}
