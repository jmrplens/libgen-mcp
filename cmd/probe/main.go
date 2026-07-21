// Command probe verifies against the real mirrors that every route libgen-mcp
// uses still works (search by topic, JSON API, download chain).
// Usage: go run ./cmd/probe [-mirror https://libgen.li]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
)

// checker accumulates the state of the checks and prints their results to w.
type checker struct {
	w      io.Writer
	failed bool
}

func (c *checker) report(name string, err error, okMsg string) {
	if err != nil {
		c.failed = true
		fmt.Fprintf(c.w, "[FAIL] %s: %v\n", name, err)
		return
	}
	fmt.Fprintf(c.w, "[OK]   %s: %s\n", name, okMsg)
}

func main() {
	os.Exit(run(os.Stdout, os.Args[1:]))
}

// newManager builds the mirror manager. It is a test seam (overridden in tests
// so the probe runs against a local server instead of the live discovery page).
var newManager = mirrors.NewManager

// run parses flags, builds the client stack from configuration and drives the
// probe, returning the process exit code. All output goes to w.
func run(w io.Writer, args []string) int {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(w)
	mirror := fs.String("mirror", "", "force a specific mirror base URL")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(w, "[FAIL] config:", err)
		return 1
	}
	if *mirror != "" {
		cfg.Mirror = *mirror
	}
	mgr, err := newManager(cfg)
	if err != nil {
		fmt.Fprintln(w, "[FAIL] mirrors manager:", err)
		return 1
	}
	client := libgen.New(mgr, cfg)
	return probe(ctx, w, mgr, client)
}

// mirrorLister is the subset of the mirror manager the probe depends on.
type mirrorLister interface {
	Mirrors(ctx context.Context) []string
}

// probe runs the full smoke check (mirrors → search → details → download) against
// the supplied mirror lister and client, returning the process exit code.
func probe(ctx context.Context, w io.Writer, mgr mirrorLister, client *libgen.Client) int {
	c := &checker{w: w}
	list := mgr.Mirrors(ctx)
	if len(list) == 0 {
		c.report("mirrors", errors.New("no mirrors discovered"), "")
		return 1
	}
	c.report("mirrors", nil, fmt.Sprintf("%d discovered, preferred %s", len(list), list[0]))

	sampleMD5 := c.runSearches(ctx, client)
	if sampleMD5 == "" {
		fmt.Fprintln(w, "[FAIL] no sample md5 available, skipping details/download checks")
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

// runSearches runs one search per topic and returns the first useful md5.
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

// checkDownload requests the first byte from the CDN to confirm the key resolves.
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
