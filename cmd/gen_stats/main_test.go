package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmrplens/libgen-mcp/cmd/internal/docgen"
	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/freshness"
)

// repoRoot locates the repository from the package directory the test runs in.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}
	return root
}

// TestRun_TheCommittedRegionIsCurrent is the parity test: the region in
// README.md is what this command would write now.
//
// It defers when the harness says the artifacts are refreshed elsewhere. In a
// stack of pull requests they are refreshed once, at the top, so every layer
// below carries them stale on purpose — and a unit suite that failed on that
// would be red on every layer nobody merges.
func TestRun_TheCommittedRegionIsCurrent(t *testing.T) {
	freshness.SkipIfDeferred(t)

	var out, errOut bytes.Buffer
	if status := run([]string{"--check", "--dir", repoRoot(t)}, &out, &errOut); status != 0 {
		t.Fatalf("run(--check) = %d, want 0\nstdout: %s\nstderr: %s", status, out.String(), errOut.String())
	}
}

// TestRun_ExitStatusSaysWhichOfTheThreeHappened pins the split every gate in
// this repository uses: 0 clean, 1 refused, 2 could not run.
func TestRun_ExitStatusSaysWhichOfTheThreeHappened(t *testing.T) {
	stale := fixtureRoot(t, "# Title\n\n"+startMark+"\nstale\n"+endMark+"\n")
	testCases := []struct {
		name string
		args []string
		want int
	}{
		{name: "help", args: []string{"-h"}, want: 0},
		{name: "an unknown flag", args: []string{"--nonsense"}, want: 2},
		{name: "a directory with no README", args: []string{"--dir", t.TempDir()}, want: 2},
		{name: "a README with no region", args: []string{"--dir", fixtureRoot(t, "# Title\n\nNo region.\n")}, want: 2},
		{name: "a stale region", args: []string{"--check", "--dir", stale}, want: 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if got := run(tc.args, &out, &errOut); got != tc.want {
				t.Errorf("run() = %d, want %d\nstdout: %s\nstderr: %s", got, tc.want, out.String(), errOut.String())
			}
		})
	}
}

// fixtureRoot writes a README with the given content into a temporary tree and
// returns the tree, so a case can drive the command over a file it controls.
func fixtureRoot(t *testing.T, readme string) string {
	t.Helper()
	return writeTree(t, map[string]string{statsFile: readme})
}

// writeTree writes a file tree under a temporary directory and returns it.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

// TestRun_WritesTheRegionAndThenAgreesWithItself verifies the two modes are
// two readings of one rendering: a write followed by a check passes, which is
// what make gen-stats followed by make check-stats has to do.
func TestRun_WritesTheRegionAndThenAgreesWithItself(t *testing.T) {
	root := fixtureRoot(t, "# Title\n\n"+startMark+"\n"+endMark+"\n")

	var out, errOut bytes.Buffer
	if status := run([]string{"--dir", root}, &out, &errOut); status != 0 {
		t.Fatalf("the write run = %d, want 0: %s", status, errOut.String())
	}
	written, err := os.ReadFile(filepath.Join(root, statsFile))
	if err != nil {
		t.Fatalf("read the written README: %v", err)
	}
	if !strings.Contains(string(written), "| Tools") {
		t.Errorf("README = %q, want the surface table in it", written)
	}

	out.Reset()
	errOut.Reset()
	if status := run([]string{"--check", "--dir", root}, &out, &errOut); status != 0 {
		t.Errorf("the check run after the write = %d, want 0: %s", status, errOut.String())
	}
}

// TestRender_IsWhatTheTableGateAccepts is the reason the rendering goes
// through the shared renderer: two gates that disagree about one file would
// each undo the other, and nothing would ever be green.
func TestRender_IsWhatTheTableGateAccepts(t *testing.T) {
	rendered := render(Stats{
		Tools: 4, Prompts: 4, Sources: 21, Providers: 8, EnvVars: 55,
		Packages: 46, TestFiles: 216,
		ByLayer: map[string]int{"unit (internal)": 101, "unit (cmd)": 63},
	})
	formatted, changed := docgen.FormatMarkdownTables(rendered)

	if changed || formatted != rendered {
		t.Errorf("the table gate would rewrite this rendering:\ngot:\n%s\nwant:\n%s", rendered, formatted)
	}
}

// TestRender_OmitsALayerWithNoFiles verifies a layer that holds nothing writes
// no row: a table saying zero for a surface that does not exist reads as a
// surface with no tests.
func TestRender_OmitsALayerWithNoFiles(t *testing.T) {
	rendered := render(Stats{ByLayer: map[string]int{"unit (cmd)": 3}})

	if !strings.Contains(rendered, "unit (cmd)") {
		t.Errorf("rendering = %q, want the layer that has files", rendered)
	}
	if strings.Contains(rendered, "collector acceptance") {
		t.Errorf("rendering = %q, want no row for a layer with no files", rendered)
	}
}

// TestLayerOf_TakesTheLongestPrefix pins the ordering the layer table depends
// on: test/e2e is a prefix of every module nested under it, so the shortest
// match would put every HTTP and stdio file in the live suite.
func TestLayerOf_TakesTheLongestPrefix(t *testing.T) {
	testCases := []struct {
		path string
		want string
	}{
		{path: "internal/tools/markdown_test.go", want: "unit (internal)"},
		{path: "cmd/gen_stats/main_test.go", want: "unit (cmd)"},
		{path: "test/e2e/live_test.go", want: "live end-to-end"},
		{path: "test/e2e/http/routing_test.go", want: "HTTP end-to-end"},
		{path: "test/e2e/stdio/transport_test.go", want: "stdio end-to-end"},
		{path: "test/e2e/collector/spans_test.go", want: "collector acceptance"},
		{path: "somewhere/else/x_test.go", want: "other"},
	}

	for _, tc := range testCases {
		t.Run(tc.path, func(t *testing.T) {
			if got := layerOf(tc.path); got != tc.want {
				t.Errorf("layerOf(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestRender_ListsAnUnknownLayerRatherThanHidingIt verifies a test tree the
// layer table does not describe is counted and shown. Hiding it would make the
// testing reference look complete when it had stopped being so.
func TestRender_ListsAnUnknownLayerRatherThanHidingIt(t *testing.T) {
	rendered := render(Stats{ByLayer: map[string]int{"other": 2, "unit (cmd)": 1}})

	if !strings.Contains(rendered, "| other") {
		t.Errorf("rendering = %q, want the unknown layer listed", rendered)
	}
	if strings.Index(rendered, "unit (cmd)") > strings.Index(rendered, "| other") {
		t.Errorf("rendering = %q, want the declared layers before the rest", rendered)
	}
}

// TestCollect_ReadsEachCountFromWhereTheServerReadsIt verifies the counts come
// from the source of truth rather than from a list this command keeps: a
// number typed here would drift exactly the way the prose it replaced did.
func TestCollect_ReadsEachCountFromWhereTheServerReadsIt(t *testing.T) {
	stats, err := collect(repoRoot(t))
	if err != nil {
		t.Fatalf("collect() error = %v", err)
	}

	if stats.Sources != len(config.KnownSources) {
		t.Errorf("Sources = %d, want len(config.KnownSources) = %d", stats.Sources, len(config.KnownSources))
	}
	if stats.EnvVars != len(config.KnownEnvNames()) {
		t.Errorf("EnvVars = %d, want len(config.KnownEnvNames()) = %d", stats.EnvVars, len(config.KnownEnvNames()))
	}
	positive := []struct {
		name  string
		count int
	}{
		{name: "Tools", count: stats.Tools},
		{name: "Prompts", count: stats.Prompts},
		{name: "Providers", count: stats.Providers},
		{name: "Packages", count: stats.Packages},
		{name: "TestFiles", count: stats.TestFiles},
	}
	for _, tc := range positive {
		t.Run(tc.name, func(t *testing.T) {
			if tc.count <= 0 {
				t.Errorf("%s = %d, want a count this repository plainly has", tc.name, tc.count)
			}
		})
	}
}

// TestWalkTree_CountsPackagesAndTestFilesAndSkipsWhatItShould verifies the
// walk over a tree small enough to count by hand, testdata included: a
// fixtures directory holds Go files that are nobody's package.
func TestWalkTree_CountsPackagesAndTestFilesAndSkipsWhatItShould(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a/a.go":                  "package a\n",
		"a/a_test.go":             "package a\n",
		"b/c/c.go":                "package c\n",
		"b/c/testdata/skipped.go": "package c\n",
		"node_modules/x/y.go":     "package y\n",
		"README.md":               "not Go\n",
	})

	var stats Stats
	stats.ByLayer = map[string]int{}
	if err := walkTree(root, &stats); err != nil {
		t.Fatalf("walkTree() error = %v", err)
	}
	if stats.Packages != 2 {
		t.Errorf("Packages = %d, want 2: a/ and b/c/, with testdata and node_modules skipped", stats.Packages)
	}
	if stats.TestFiles != 1 {
		t.Errorf("TestFiles = %d, want 1", stats.TestFiles)
	}
}

// TestWalkTree_ReportsATreeItCannotRead verifies the walk's error reaches the
// caller rather than producing a count that is quietly short.
func TestWalkTree_ReportsATreeItCannotRead(t *testing.T) {
	var stats Stats
	stats.ByLayer = map[string]int{}
	if err := walkTree(filepath.Join(t.TempDir(), "absent"), &stats); err == nil {
		t.Error("walkTree() over a directory that is not there returned no error")
	}
}
