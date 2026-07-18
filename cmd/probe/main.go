// probe verifica contra los mirrors reales que todas las rutas que usa
// libgen-mcp siguen funcionando (búsqueda por topic, API JSON, cadena de
// descarga). Uso: go run ./cmd/probe [-mirror https://libgen.li]
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
)

func main() {
	mirror := flag.String("mirror", "", "force a specific mirror base URL")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		fmt.Println("[FAIL] config:", err)
		os.Exit(1)
	}
	if *mirror != "" {
		cfg.Mirror = *mirror
	}
	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		fmt.Println("[FAIL] mirrors manager:", err)
		os.Exit(1)
	}
	client := libgen.New(mgr, cfg.Timeout)

	failed := false
	report := func(name string, err error, okMsg string) {
		if err != nil {
			failed = true
			fmt.Printf("[FAIL] %s: %v\n", name, err)
			return
		}
		fmt.Printf("[OK]   %s: %s\n", name, okMsg)
	}

	list := mgr.Mirrors(ctx)
	if len(list) == 0 {
		report("mirrors", fmt.Errorf("no mirrors discovered"), "")
		os.Exit(1)
	}
	report("mirrors", nil, fmt.Sprintf("%d discovered, preferred %s", len(list), list[0]))

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
		page, mirrorUsed, err := client.Search(ctx, libgen.SearchParams{Query: s.query, Topics: []string{s.topic}})
		msg := ""
		if err == nil {
			msg = fmt.Sprintf("%d results (total %s) via %s", len(page.Results), page.TotalFiles, mirrorUsed)
			if sampleMD5 == "" && len(page.Results) > 0 {
				sampleMD5 = page.Results[0].MD5
			}
			if len(page.Results) == 0 {
				err = fmt.Errorf("0 results for %q (query too narrow or parser broken)", s.query)
			}
		}
		report("search "+s.topic, err, msg)
	}

	if sampleMD5 == "" {
		fmt.Println("[FAIL] no sample md5 available, skipping details/download checks")
		os.Exit(1)
	}

	file, edition, err := client.DetailsByMD5(ctx, sampleMD5)
	msg := ""
	if err == nil {
		msg = fmt.Sprintf("file fields=%d, edition present=%v", len(file), edition != nil)
	}
	report("json.php details", err, msg)

	getURL, base, err := client.ResolveGetURL(ctx, sampleMD5)
	report("ads.php key", err, fmt.Sprintf("resolved via %s", base))

	if err == nil {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
		req.Header.Set("Range", "bytes=0-0")
		resp, err := http.DefaultClient.Do(req)
		msg := ""
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("status %d", resp.StatusCode)
			} else {
				msg = fmt.Sprintf("status %d, content-disposition present=%v",
					resp.StatusCode, resp.Header.Get("Content-Disposition") != "")
			}
		}
		report("CDN download", err, msg)
	}

	if failed {
		os.Exit(1)
	}
}
