// main_test.go covers icon-asset generation: extracting svg<Name> constants
// from icons.go's AST, naming their WebP files, and the check/generate
// pipeline that reads or writes them. Every test here runs without
// rsvg-convert/cwebp on PATH by injecting a fake rasterizer — production
// code's real rasterize (the only function that shells out) is exercised by
// a single test gated on those tools actually being present, since CI does
// not install them (this generator is a maintainer-only, occasional step;
// its output is committed).
package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRasterizer returns deterministic, cheap-to-compare bytes for (svg,
// color) without touching rsvg-convert/cwebp, so checkAll/generateAll/runIn
// can be tested in pure Go.
func fakeRasterizer(svg, color string) ([]byte, error) {
	return []byte(svg + "|" + color), nil
}

// failingRasterizer always errors, for exercising error propagation.
func failingRasterizer(_, _ string) ([]byte, error) {
	return nil, errors.New("boom")
}

const fixtureIconsGo = `package toolutil

const (
	svgBranch = ` + "`<svg>branch</svg>`" + `
	svgMR     = ` + "`<svg>mr</svg>`" + `
)

// svgMIME is not SVG markup, so it must be skipped even though its name
// starts with "svg".
const svgMIME = "image/svg+xml"

// notPrefixed starts with "<svg" but its identifier does not start with
// "svg", so it must be skipped too.
const notPrefixed = ` + "`<svg>ignored</svg>`" + `

const (
	svgWeird = 42
)

const (
	svgInherited = ` + "`<svg>first</svg>`" + `
	svgInheritedTwo
)

var IconBranch = icon("branch", svgBranch)

func helper() string { return "not a const decl" }
`

const fixtureEmptyIconsGo = `package toolutil

const notAnIcon = "hello"
`

// writeFixture writes content to name inside a fresh temp directory and
// returns that directory.
func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return dir
}

// TestIconFileName covers the Go-identifier-to-asset-name mapping across the
// shapes an icon constant's suffix can take: already lowercase, an all-caps
// acronym, mixed case, a trailing digit, embedded separators, and empty.
func TestIconFileName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Branch", "branch"},
		{"MR", "mr"},
		{"MergeRequest", "mergerequest"},
		{"Vulnerability2", "vulnerability2"},
		{"Merge_Request-2", "mergerequest2"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := iconFileName(tt.in); got != tt.want {
				t.Errorf("iconFileName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestExtractIcons_FindsSVGConstantsInDeclarationOrder verifies the AST walk
// keeps declaration order and admits only constants that are BOTH named
// svg<Name> and hold SVG markup — the three near-misses in the fixture
// (wrong content, wrong name prefix, non-string value) and a grouped const
// that inherits its value must all be skipped.
func TestExtractIcons_FindsSVGConstantsInDeclarationOrder(t *testing.T) {
	dir := writeFixture(t, "icons.go", fixtureIconsGo)

	icons, err := extractIcons(filepath.Join(dir, "icons.go"))
	if err != nil {
		t.Fatalf("extractIcons() error: %v", err)
	}

	// svgMIME (wrong content), notPrefixed (wrong name), svgWeird (not a
	// string literal), and svgInheritedTwo (no value of its own) must all
	// be excluded; only svgBranch, svgMR, and svgInherited qualify.
	var names []string
	for _, ic := range icons {
		names = append(names, ic.name)
	}
	want := []string{"branch", "mr", "inherited"}
	if len(names) != len(want) {
		t.Fatalf("extractIcons() names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("extractIcons() names = %v, want %v", names, want)
		}
	}
	if icons[0].svg != "<svg>branch</svg>" {
		t.Errorf("icons[0].svg = %q, want %q", icons[0].svg, "<svg>branch</svg>")
	}
}

// TestExtractIcons_NoIconConstants verifies a source file with no qualifying
// constants yields an empty slice and no error, leaving the caller to decide
// whether that is a problem (runIn treats it as one).
func TestExtractIcons_NoIconConstants(t *testing.T) {
	dir := writeFixture(t, "icons.go", fixtureEmptyIconsGo)

	icons, err := extractIcons(filepath.Join(dir, "icons.go"))
	if err != nil {
		t.Fatalf("extractIcons() error: %v", err)
	}
	if len(icons) != 0 {
		t.Fatalf("extractIcons() = %v, want empty", icons)
	}
}

// TestExtractIcons_MalformedSource verifies unparseable Go source surfaces
// the parser's error rather than silently yielding no icons, which would look
// identical to a file that legitimately declares none.
func TestExtractIcons_MalformedSource(t *testing.T) {
	dir := writeFixture(t, "icons.go", "package toolutil\n\nconst svgBroken = `<svg>\n")

	if _, err := extractIcons(filepath.Join(dir, "icons.go")); err == nil {
		t.Fatal("extractIcons() error = nil, want a parse error for malformed Go source")
	}
}

// TestExtractIcons_MissingFile verifies a path that does not exist is an
// error, not an empty result.
func TestExtractIcons_MissingFile(t *testing.T) {
	if _, err := extractIcons(filepath.Join(t.TempDir(), "does-not-exist.go")); err == nil {
		t.Fatal("extractIcons() error = nil, want an error for a missing file")
	}
}

// TestRequireTools_AllPresent verifies the empty tool list is satisfied,
// pinning the vacuous-truth case so the loop cannot regress into rejecting it.
func TestRequireTools_AllPresent(t *testing.T) {
	if err := requireTools(); err != nil {
		t.Fatalf("requireTools() error = %v, want nil for an empty tool list", err)
	}
}

// TestRequireTools_ReportsMissingByName verifies a missing tool produces an
// error that names it, since the whole point of the check is telling the
// maintainer which package to install.
func TestRequireTools_ReportsMissingByName(t *testing.T) {
	err := requireTools("definitely-not-a-real-tool-libgen-mcp-xyz")
	if err == nil {
		t.Fatal("requireTools() error = nil, want an error naming the missing tool")
	}
	if !strings.Contains(err.Error(), "definitely-not-a-real-tool-libgen-mcp-xyz") {
		t.Errorf("requireTools() error = %v, want it to name the missing tool", err)
	}
}

// TestRepoRoot_FindsAncestorGoMod verifies the walk up finds the nearest
// ancestor holding go.mod, so the generator works when run from any
// subdirectory of the repository.
func TestRepoRoot_FindsAncestorGoMod(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	t.Chdir(nested)

	got, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot() error: %v", err)
	}
	// Resolve both sides through EvalSymlinks: on macOS, t.TempDir() lives
	// under /tmp, which is itself a symlink to /private/tmp.
	wantResolved, _ := filepath.EvalSymlinks(root)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("repoRoot() = %q, want %q", gotResolved, wantResolved)
	}
}

// TestRepoRoot_NoGoModAnywhereAbove verifies running outside any module is an
// error rather than a silent walk to the filesystem root.
func TestRepoRoot_NoGoModAnywhereAbove(t *testing.T) {
	// A fresh temp dir has no go.mod above it up to the filesystem root.
	t.Chdir(t.TempDir())

	if _, err := repoRoot(); err == nil {
		t.Fatal("repoRoot() error = nil, want an error when no go.mod is found")
	}
}

// TestGenerateAll_WritesEveryVariant verifies both themed files are written
// for every icon, with the light/dark color actually substituted into each —
// the substitution is what makes the two variants differ at all.
func TestGenerateAll_WritesEveryVariant(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "webp")
	icons := []iconSource{{name: "branch", svg: "<svg>branch</svg>"}, {name: "mr", svg: "<svg>mr</svg>"}}

	written, err := generateAll(dir, icons, fakeRasterizer)
	if err != nil {
		t.Fatalf("generateAll() error: %v", err)
	}
	if written != 4 {
		t.Fatalf("generateAll() written = %d, want 4 (2 icons x 2 variants)", written)
	}

	for _, name := range []string{"branch-light.webp", "branch-dark.webp", "mr-light.webp", "mr-dark.webp"} {
		data, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			t.Fatalf("expected %s to exist: %v", name, readErr)
		}
		if len(data) == 0 {
			t.Errorf("%s is empty", name)
		}
	}
	branchLight, _ := os.ReadFile(filepath.Join(dir, "branch-light.webp"))
	if string(branchLight) != "<svg>branch</svg>|"+colorLight {
		t.Errorf("branch-light.webp = %q, want the fake rasterizer's deterministic output", branchLight)
	}
}

// TestGenerateAll_PropagatesRasterizerError verifies a failing rasterizer
// aborts with the icon named, rather than writing a truncated or empty asset
// that would later embed as a corrupt image.
func TestGenerateAll_PropagatesRasterizerError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "webp")
	icons := []iconSource{{name: "branch", svg: "<svg>branch</svg>"}}

	_, err := generateAll(dir, icons, failingRasterizer)
	if err == nil {
		t.Fatal("generateAll() error = nil, want the rasterizer's error propagated")
	}
	if !strings.Contains(err.Error(), "branch") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("generateAll() error = %v, want it to name the icon and the underlying error", err)
	}
}

// TestGenerateAll_LeavesAssetsUntouchedWhenALaterIconFails verifies a failure
// part-way through does not leave a half-replaced asset set behind.
//
// The realistic trigger is a malformed SVG the maintainer just added: the
// icons before it in declaration order would rasterize fine. If generateAll
// wrote as it went, those would be replaced on disk while the rest kept their
// previous bytes, and the run would exit non-zero having silently mutated the
// working tree. Rendering everything before writing anything is what makes the
// failed run a no-op instead.
func TestGenerateAll_LeavesAssetsUntouchedWhenALaterIconFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "webp")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Seed the directory with recognizable "previous release" assets.
	const sentinel = "PREVIOUS-ASSET"
	seeded := []string{"good-light.webp", "good-dark.webp", "bad-light.webp", "bad-dark.webp"}
	for _, name := range seeded {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(sentinel), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	// "good" rasterizes; "bad" — declared after it — does not.
	icons := []iconSource{{name: "good", svg: "<svg>good</svg>"}, {name: "bad", svg: "<svg>bad</svg>"}}
	failOnBad := func(svg, color string) ([]byte, error) {
		if strings.Contains(svg, "bad") {
			return nil, errors.New("boom")
		}
		return fakeRasterizer(svg, color)
	}

	written, err := generateAll(dir, icons, failOnBad)
	if err == nil {
		t.Fatal("generateAll() error = nil, want the rasterizer's failure")
	}
	if written != 0 {
		t.Errorf("generateAll() wrote %d files before failing, want 0", written)
	}
	for _, name := range seeded {
		got, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			t.Errorf("%s: %v", name, readErr)
			continue
		}
		if string(got) != sentinel {
			t.Errorf("%s was replaced despite the run failing; got %q", name, got)
		}
	}
}

// TestGenerateAll_MkdirFailsWhenParentIsAFile verifies the output directory
// being unusable is reported, not ignored.
func TestGenerateAll_MkdirFailsWhenParentIsAFile(t *testing.T) {
	// A regular file where a directory component is expected makes
	// MkdirAll fail deterministically, regardless of OS/permissions.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	dir := filepath.Join(blocker, "webp")
	icons := []iconSource{{name: "branch", svg: "<svg>branch</svg>"}}

	if _, err := generateAll(dir, icons, fakeRasterizer); err == nil {
		t.Fatal("generateAll() error = nil, want an error when the output directory cannot be created")
	}
}

// TestGenerateAll_WriteFileFailsWhenTargetIsADirectory verifies a per-file
// write failure is reported, covering the branch after the directory itself
// was created successfully.
func TestGenerateAll_WriteFileFailsWhenTargetIsADirectory(t *testing.T) {
	dir := t.TempDir()
	// Pre-create a directory at the exact path generateAll needs to write
	// a file to, so os.WriteFile fails.
	if err := os.MkdirAll(filepath.Join(dir, "branch-light.webp"), 0o750); err != nil {
		t.Fatalf("mkdir collision path: %v", err)
	}
	icons := []iconSource{{name: "branch", svg: "<svg>branch</svg>"}}

	written, err := generateAll(dir, icons, fakeRasterizer)
	if err == nil {
		t.Fatal("generateAll() error = nil, want an error when a target path is a directory")
	}
	if written != 0 {
		t.Errorf("generateAll() written = %d, want 0 since the first write failed", written)
	}
}

// TestCheckAll_MatchesUpToDateAssets verifies check mode accepts assets that
// are byte-identical to what the rasterizer would produce now.
func TestCheckAll_MatchesUpToDateAssets(t *testing.T) {
	dir := t.TempDir()
	icons := []iconSource{{name: "branch", svg: "<svg>branch</svg>"}}
	if _, err := generateAll(dir, icons, fakeRasterizer); err != nil {
		t.Fatalf("seed generateAll() error: %v", err)
	}

	if err := checkAll(dir, icons, fakeRasterizer); err != nil {
		t.Fatalf("checkAll() error = %v, want nil for freshly generated assets", err)
	}
}

// TestCheckAll_ReportsMissingAsset verifies an icon with no generated file is
// reported by name, which is the case a newly added svg<Name> constant hits.
func TestCheckAll_ReportsMissingAsset(t *testing.T) {
	dir := t.TempDir()
	icons := []iconSource{{name: "branch", svg: "<svg>branch</svg>"}}

	err := checkAll(dir, icons, fakeRasterizer)
	if err == nil {
		t.Fatal("checkAll() error = nil, want an error for missing assets")
	}
	if !strings.Contains(err.Error(), "branch-dark.webp") {
		t.Errorf("checkAll() error = %v, want it to name the missing file", err)
	}
}

// TestCheckAll_ReportsStaleAsset verifies an asset whose bytes no longer match
// its source SVG is reported — the case an edited icon hits, and the one a
// mere existence check would miss.
func TestCheckAll_ReportsStaleAsset(t *testing.T) {
	dir := t.TempDir()
	icons := []iconSource{{name: "branch", svg: "<svg>branch</svg>"}}
	if _, err := generateAll(dir, icons, fakeRasterizer); err != nil {
		t.Fatalf("seed generateAll() error: %v", err)
	}
	// Edit the source SVG so the committed asset no longer matches.
	icons[0].svg = "<svg>changed</svg>"

	err := checkAll(dir, icons, fakeRasterizer)
	if err == nil {
		t.Fatal("checkAll() error = nil, want an error for a stale asset")
	}
	if !strings.Contains(err.Error(), "branch-light.webp") {
		t.Errorf("checkAll() error = %v, want it to name the stale file", err)
	}
}

// TestCheckAll_PropagatesRasterizerError verifies check mode distinguishes a
// broken rasterizer from a stale asset: it returns the rasterizer's error
// instead of reporting every file as out of date.
func TestCheckAll_PropagatesRasterizerError(t *testing.T) {
	dir := t.TempDir()
	icons := []iconSource{{name: "branch", svg: "<svg>branch</svg>"}}

	err := checkAll(dir, icons, failingRasterizer)
	if err == nil {
		t.Fatal("checkAll() error = nil, want the rasterizer's error propagated")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("checkAll() error = %v, want the underlying rasterizer error", err)
	}
}

// TestRunIn_GenerateThenCheckRoundTrips verifies the two modes agree: what
// generate writes, check immediately accepts. A disagreement here would make
// the --check gate unusable.
func TestRunIn_GenerateThenCheckRoundTrips(t *testing.T) {
	root := writeFixture(t, "icons.go", fixtureIconsGo)

	if err := runIn(root, "icons.go", "webp", false, fakeRasterizer); err != nil {
		t.Fatalf("runIn(generate) error: %v", err)
	}
	if err := runIn(root, "icons.go", "webp", true, fakeRasterizer); err != nil {
		t.Fatalf("runIn(check) error = %v, want nil right after generating", err)
	}
}

// TestRunIn_PropagatesExtractIconsError verifies an unreadable icons.go fails
// the run rather than being treated as a file with no icons.
func TestRunIn_PropagatesExtractIconsError(t *testing.T) {
	root := writeFixture(t, "icons.go", "package toolutil\n\nconst svgBroken = `<svg>\n")

	if err := runIn(root, "icons.go", "webp", true, fakeRasterizer); err == nil {
		t.Fatal("runIn() error = nil, want extractIcons()'s parse error propagated")
	}
}

// TestRunIn_NoIconsFoundIsAnError verifies finding zero icons is refused: it
// almost always means the extraction broke, and silently writing nothing
// would leave the committed assets stale without any signal.
func TestRunIn_NoIconsFoundIsAnError(t *testing.T) {
	root := writeFixture(t, "icons.go", fixtureEmptyIconsGo)

	err := runIn(root, "icons.go", "webp", true, fakeRasterizer)
	if err == nil {
		t.Fatal("runIn() error = nil, want an error when icons.go declares no svg<Name> constants")
	}
	if !strings.Contains(err.Error(), "icons.go") {
		t.Errorf("runIn() error = %v, want it to name the source file", err)
	}
}

// TestRunIn_CheckFailsBeforeGenerate verifies check mode reports missing
// assets instead of quietly creating them, so a CI-style invocation cannot
// mutate the working tree.
func TestRunIn_CheckFailsBeforeGenerate(t *testing.T) {
	root := writeFixture(t, "icons.go", fixtureIconsGo)

	if err := runIn(root, "icons.go", "webp", true, fakeRasterizer); err == nil {
		t.Fatal("runIn(check) error = nil, want an error when no assets have been generated yet")
	}
}

// TestRunIn_GeneratePropagatesRasterizerError verifies a rasterizer failure
// reaches the caller through runIn's generate path.
func TestRunIn_GeneratePropagatesRasterizerError(t *testing.T) {
	root := writeFixture(t, "icons.go", fixtureIconsGo)

	if err := runIn(root, "icons.go", "webp", false, failingRasterizer); err == nil {
		t.Fatal("runIn(generate) error = nil, want the rasterizer's error propagated")
	}
}

// TestRun_PropagatesRepoRootError verifies run surfaces the repo-root lookup
// failure, the one error it can hit before reaching runIn.
func TestRun_PropagatesRepoRootError(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := run(true, fakeRasterizer); err == nil {
		t.Fatal("run() error = nil, want repoRoot()'s error propagated when no go.mod is found")
	}
}

// TestRasterize_ToolNotOnPath exercises rasterize's exec.LookPath failure
// branch deterministically, without needing rsvg-convert/cwebp installed:
// an empty PATH can never resolve either one.
func TestRasterize_ToolNotOnPath(t *testing.T) {
	t.Setenv("PATH", "")

	if _, err := rasterize("<svg></svg>", colorLight); err == nil {
		t.Fatal("rasterize() error = nil, want an error when rsvg-convert cannot be resolved on PATH")
	}
}

// TestRasterize_InvalidSVGFailsAtRsvgConvert exercises rasterize's
// rsvg-convert error branch. Skipped without the tool for the same reason
// as TestRun_CheckModeAcceptsCommittedAssets.
func TestRasterize_InvalidSVGFailsAtRsvgConvert(t *testing.T) {
	if err := requireTools("rsvg-convert"); err != nil {
		t.Skip("skipping: " + err.Error())
	}

	if _, err := rasterize("not valid currentColor svg markup", colorLight); err == nil {
		t.Fatal("rasterize() error = nil, want rsvg-convert to reject non-SVG input")
	}
}

// TestRun_CheckModeAcceptsCommittedAssets verifies the real, committed WebP
// assets under internal/toolutil/icons/webp/ still match icons.go, using the
// real rasterize (rsvg-convert + cwebp) rather than a fake. This is the same
// gate `make check-icon-webp` runs; it is skipped when the machine cannot
// reproduce the committed bytes, which covers both a missing tool (the
// maintainer-only, non-CI dependency described in the package doc) and a
// librsvg older than minLibrsvg.
//
// The version half of that guard matters as much as the missing-tool half:
// Debian 12 ships librsvg 2.54.7, which renders three of the nine icons
// differently, and without the guard this test reports the committed assets
// as stale on every such machine — a failure about the renderer disguised as
// a failure about the assets.
func TestRun_CheckModeAcceptsCommittedAssets(t *testing.T) {
	if err := requireRenderer(); err != nil {
		t.Skip("skipping: " + err.Error())
	}

	if err := run(true, rasterize); err != nil {
		t.Fatalf("run(true) error: %v", err)
	}
}

// TestParseLibrsvgVersion covers the banner rsvg-convert actually prints, the
// major.minor-only form some distribution builds print, and output carrying
// no version at all.
func TestParseLibrsvgVersion(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want [3]int
		bad  bool
	}{
		{name: "debian 12 banner", out: "rsvg-convert version 2.54.7\n", want: [3]int{2, 54, 7}},
		{name: "debian 13 banner", out: "rsvg-convert version 2.60.0\n", want: [3]int{2, 60, 0}},
		{name: "major minor only", out: "rsvg-convert version 2.58\n", want: [3]int{2, 58, 0}},
		{name: "no version", out: "rsvg-convert: command not understood\n", bad: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLibrsvgVersion(tc.out)
			if tc.bad {
				if err == nil {
					t.Fatalf("parseLibrsvgVersion(%q) error = nil, want an error", tc.out)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLibrsvgVersion(%q) error: %v", tc.out, err)
			}
			if got != tc.want {
				t.Errorf("parseLibrsvgVersion(%q) = %v, want %v", tc.out, got, tc.want)
			}
		})
	}
}

// TestOlderThan pins the comparison against the versions that decided
// minLibrsvg, including that the floor itself is not older than itself.
func TestOlderThan(t *testing.T) {
	cases := []struct {
		name string
		a, b [3]int
		want bool
	}{
		{name: "ubuntu 22.04 below floor", a: [3]int{2, 52, 5}, b: minLibrsvg, want: true},
		{name: "debian 12 below floor", a: [3]int{2, 54, 7}, b: minLibrsvg, want: true},
		{name: "floor itself", a: minLibrsvg, b: minLibrsvg, want: false},
		{name: "ubuntu 24.04 at floor", a: [3]int{2, 58, 0}, b: minLibrsvg, want: false},
		{name: "debian 13 above floor", a: [3]int{2, 60, 0}, b: minLibrsvg, want: false},
		{name: "patch decides", a: [3]int{2, 58, 0}, b: [3]int{2, 58, 1}, want: true},
		{name: "major outranks minor", a: [3]int{3, 0, 0}, b: [3]int{2, 99, 99}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := olderThan(tc.a, tc.b); got != tc.want {
				t.Errorf("olderThan(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestFormatVersion checks the rendering used in requireRenderer's message,
// which is the only place a user sees these triples.
func TestFormatVersion(t *testing.T) {
	if got, want := formatVersion([3]int{2, 58, 0}), "2.58.0"; got != want {
		t.Errorf("formatVersion = %q, want %q", got, want)
	}
}

// TestRequireRenderer_AgreesWithTheRealToolchain asserts requireRenderer's
// verdict matches what the machine can actually do, rather than trusting it:
// when it reports the toolchain usable, the committed assets must verify, and
// when it refuses, the reason must name either a missing tool or the version.
func TestRequireRenderer_AgreesWithTheRealToolchain(t *testing.T) {
	err := requireRenderer()
	if err == nil {
		if runErr := run(true, rasterize); runErr != nil {
			t.Fatalf("requireRenderer() = nil but run(true) failed: %v", runErr)
		}
		return
	}
	msg := err.Error()
	if !strings.Contains(msg, "not found on PATH") && !strings.Contains(msg, "librsvg") {
		t.Errorf("requireRenderer() error %q names neither a missing tool nor librsvg", msg)
	}
}
