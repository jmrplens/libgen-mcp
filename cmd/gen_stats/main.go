package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jmrplens/libgen-mcp/cmd/internal/docgen"
	"github.com/jmrplens/libgen-mcp/cmd/internal/mcpsurface"
	"github.com/jmrplens/libgen-mcp/cmd/internal/testsource"
	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/discovery"
)

// toolName prefixes every line this command writes about itself.
const toolName = "gen_stats"

// The region README.md reserves for the counts, and the target that refreshes
// it. The marks are HTML comments, so a reader of the rendered page sees the
// table and nothing about how it got there.
const (
	startMark  = "<!-- START STATS -->"
	endMark    = "<!-- END STATS -->"
	statsFile  = "README.md"
	regenerate = "make gen-stats"
)

// exit is os.Exit behind a variable, so the one line main carries is reachable
// from a test rather than only from a process.
var exit = os.Exit

func main() {
	exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses the command line and writes or checks the region.
//
// It returns 0 when the run is clean, 1 when --check found the region stale,
// and 2 when the command could not do its job: a gate that cannot run must not
// read as a gate that passed.
func run(args []string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet(toolName, flag.ContinueOnError)
	flags.SetOutput(errOut)
	var (
		dir   = flags.String("dir", ".", "repository root")
		check = flags.Bool("check", false, "exit non-zero when the committed region is stale")
	)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	stats, err := collect(*dir)
	if err != nil {
		fmt.Fprintf(errOut, "%s: %v\n", toolName, err)
		return 2
	}
	return apply(*dir, stats, *check, out, errOut)
}

// apply writes the rendered region, or compares it and says which it was.
func apply(dir string, stats Stats, check bool, out, errOut io.Writer) int {
	path := filepath.Join(dir, statsFile)
	existing, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(errOut, "%s: %v\n", toolName, err)
		return 2
	}
	replaced, err := docgen.ComputeReplacedSection(string(existing), startMark, endMark, render(stats))
	if err != nil {
		fmt.Fprintf(errOut, "%s: %v\n", toolName, err)
		return 2
	}
	if writeErr := docgen.WriteOrCheck(path, []byte(replaced), check, regenerate); writeErr != nil {
		fmt.Fprintf(errOut, "%s: %v\n", toolName, writeErr)
		return 1
	}
	if check {
		fmt.Fprintf(out, "%s: %s is current\n", toolName, statsFile)
		return 0
	}
	fmt.Fprintf(out, "%s: wrote the counts into %s\n", toolName, statsFile)
	return 0
}

// Stats is what one run counted.
type Stats struct {
	Tools     int
	Prompts   int
	Sources   int
	Providers int
	EnvVars   int
	Packages  int
	TestFiles int
	// ByLayer counts test files per test surface, in the order layers lists.
	ByLayer map[string]int
}

// layers are the test surfaces, in the order docs/development/testing.md
// introduces them, each with the path prefix that identifies it.
//
// The prefixes are checked longest-first, so test/e2e's own files do not
// swallow the modules nested under it.
var layers = []struct{ name, prefix string }{
	{name: "unit (internal)", prefix: "internal/"},
	{name: "unit (cmd)", prefix: "cmd/"},
	{name: "HTTP end-to-end", prefix: "test/e2e/http/"},
	{name: "stdio end-to-end", prefix: "test/e2e/stdio/"},
	{name: "collector acceptance", prefix: "test/e2e/collector/"},
	{name: "live end-to-end", prefix: "test/e2e/"},
}

// collect counts the surface, reading each number from the place the server
// reads it rather than from a list this command keeps.
func collect(dir string) (Stats, error) {
	cfg := mcpsurface.DocsConfig()
	tools, err := mcpsurface.Tools(cfg)
	if err != nil {
		return Stats{}, fmt.Errorf("list the tools: %w", err)
	}
	prompts, err := mcpsurface.Prompts(cfg)
	if err != nil {
		return Stats{}, fmt.Errorf("list the prompts: %w", err)
	}
	stats := Stats{
		Tools:     len(tools),
		Prompts:   len(prompts),
		Sources:   len(config.KnownSources),
		Providers: len(discovery.ExtraProviders("", nil)),
		EnvVars:   len(config.KnownEnvNames()),
		ByLayer:   map[string]int{},
	}
	if walkErr := walkTree(dir, &stats); walkErr != nil {
		return Stats{}, walkErr
	}
	return stats, nil
}

// walkTree counts the packages and test files under dir.
//
// A package is a directory holding at least one Go file, which is what `go
// list` would say and what a reader counting them in a file tree would say.
// The walk skips what testsource skips — vendored trees, generated output, the
// site — so the numbers describe this module rather than its dependencies.
func walkTree(dir string, stats *Stats) error {
	withGo := map[string]bool{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if testsource.SkipDir(entry.Name()) && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		relative, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		withGo[filepath.Dir(relative)] = true
		if strings.HasSuffix(entry.Name(), testsource.FileSuffix) {
			stats.TestFiles++
			stats.ByLayer[layerOf(filepath.ToSlash(relative))]++
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk %s: %w", dir, err)
	}
	stats.Packages = len(withGo)
	return nil
}

// layerOf names the test surface a file belongs to, or "other" for one outside
// every layer — which is a count worth seeing rather than hiding, since it
// means a test tree exists that the testing reference does not describe.
func layerOf(relative string) string {
	best, bestLen := "other", -1
	for _, layer := range layers {
		if strings.HasPrefix(relative, layer.prefix) && len(layer.prefix) > bestLen {
			best, bestLen = layer.name, len(layer.prefix)
		}
	}
	return best
}

// render builds the region's Markdown: one table of the surface, one of the
// test files per layer.
//
// Both go through [docgen.RenderMarkdownTable], which is what make
// check-md-tables normalizes every other table in the repository to. Emitting
// an unpadded table here would leave two gates disagreeing about one file:
// this one would write it and that one would rewrite it, forever.
func render(stats Stats) string {
	surface := [][]string{
		{"Tools", strconv.Itoa(stats.Tools)},
		{"Prompts", strconv.Itoa(stats.Prompts)},
		{"Download sources", strconv.Itoa(stats.Sources)},
		{"Discovery providers", strconv.Itoa(stats.Providers)},
		{"`LIBGEN_MCP_*` variables", strconv.Itoa(stats.EnvVars)},
		{"Go packages", strconv.Itoa(stats.Packages)},
		{"Test files", strconv.Itoa(stats.TestFiles)},
	}
	var layerRows [][]string
	for _, name := range layerNames(stats.ByLayer) {
		layerRows = append(layerRows, []string{name, strconv.Itoa(stats.ByLayer[name])})
	}
	alignments := []docgen.Alignment{docgen.AlignLeft, docgen.AlignRight}
	return strings.TrimRight(
		docgen.RenderMarkdownTable([]string{"Surface", "Count"}, alignments, surface)+
			"\n"+docgen.RenderMarkdownTable([]string{"Test surface", "Files"}, alignments, layerRows),
		"\n",
	)
}

// layerNames lists the layers that have files, in the order layers declares
// them, with anything outside them last.
func layerNames(byLayer map[string]int) []string {
	var names []string
	for _, layer := range layers {
		if byLayer[layer.name] > 0 {
			names = append(names, layer.name)
		}
	}
	var extra []string
	for name := range byLayer {
		if !knownLayer(name) {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return append(names, extra...)
}

// knownLayer reports whether a name is one of the declared layers.
func knownLayer(name string) bool {
	for _, layer := range layers {
		if layer.name == name {
			return true
		}
	}
	return false
}
