// Command gen_icon_webp rasterizes the SVG icon constants declared in
// internal/toolutil/icons.go into lossless WebP fallbacks for MCP clients
// that reject image/svg+xml (VS Code Copilot's mcpIcons.ts MIME allowlist,
// for example, admits image/webp but not SVG).
//
// For every svg<Name> = `<svg ...>` constant it emits two 16x16 lossless
// WebP files under internal/toolutil/icons/webp/:
//
//	<name>-light.webp — near-black glyph (#1A1A1A), for Icon.Theme "light"
//	<name>-dark.webp  — near-white glyph (#FAFAFA), for Icon.Theme "dark"
//
// It requires cwebp (libwebp) and a librsvg at least [minLibrsvg] on PATH,
// because the assets are compared byte for byte and older renderers draw
// them differently. This is a
// maintainer-only, occasional regeneration step: the generated .webp files
// are committed to the repository, so ordinary builds and CI never invoke
// this tool. Run it after adding or editing an icon in icons.go.
//
// Usage:
//
//	go run ./cmd/gen_icon_webp/
//	go run ./cmd/gen_icon_webp/ --check
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	sourceFile = "internal/toolutil/icons.go"
	outDir     = "internal/toolutil/icons/webp"
	rasterSize = "16"

	colorLight = "#1A1A1A" // near-black, for Icon.Theme "light" (light background)
	colorDark  = "#FAFAFA" // near-white, for Icon.Theme "dark" (dark background)

	// The two external tools, named once. Each is looked up three times over
	// (the PATH check, the version probe and the render itself), and they are
	// resolved by exec.LookPath rather than invoked by bare name.
	toolRsvg  = "rsvg-convert"
	toolCwebp = "cwebp"
)

// iconSource is one svg<Name> constant extracted from icons.go.
type iconSource struct {
	name string // lowercase icon name, e.g. "branch"
	svg  string
}

// variant is one of the two themed WebP files generated per icon.
type variant struct {
	suffix string
	color  string
}

func variants() []variant {
	return []variant{
		{suffix: "-light", color: colorLight},
		{suffix: "-dark", color: colorDark},
	}
}

// rasterizer renders svg (with "currentColor" replaced by color) into an
// encoded image and returns its bytes. Production code uses [rasterize]
// (rsvg-convert + cwebp); tests inject a fake so checkAll/generateAll are
// exercised without requiring those external tools on PATH.
type rasterizer func(svg, color string) ([]byte, error)

func main() {
	check := flag.Bool("check", false, "verify the committed WebP assets match icons.go without writing them")
	probe := flag.Bool("probe", false, "report whether the local toolchain can reproduce the committed assets, then exit")
	flag.Parse()

	if err := requireRenderer(); err != nil {
		fatal(err)
	}
	if *probe {
		return
	}
	if err := run(*check, rasterize); err != nil {
		fatal(err)
	}
}

// run locates the repository root and delegates to runIn with this
// command's real source/output paths.
func run(check bool, raster rasterizer) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	return runIn(root, sourceFile, outDir, check, raster)
}

// runIn is gen_icon_webp's testable core: given an explicit repo root, an
// icons.go path and a WebP output directory (both relative to root), it
// extracts every svg<Name> constant and either verifies (check) or
// (re)writes the WebP assets for all of them using raster.
func runIn(root, source, out string, check bool, raster rasterizer) error {
	icons, err := extractIcons(filepath.Join(root, source))
	if err != nil {
		return err
	}
	if len(icons) == 0 {
		return fmt.Errorf("no svg<Name> constants found in %s", source)
	}

	dir := filepath.Join(root, out)
	if check {
		if checkErr := checkAll(dir, icons, raster); checkErr != nil {
			return checkErr
		}
		fmt.Printf("icon webp assets are up to date (%d icons, %d files)\n", len(icons), len(icons)*2)
		return nil
	}

	written, genErr := generateAll(dir, icons, raster)
	if genErr != nil {
		return genErr
	}
	fmt.Printf("wrote %d webp files for %d icons into %s\n", written, len(icons), out)
	return nil
}

// generateAll (re)writes every icon's light/dark WebP file under dir,
// returning how many files it wrote.
//
// Rasterization happens for ALL variants before the first file is written, so
// a failure part-way through — a malformed SVG the maintainer just added is
// the realistic case — leaves the committed assets untouched rather than
// half-replaced. Regenerating an unchanged SVG is byte-identical (that is what
// makes checkAll work at all), so a partial run would only corrupt the icons
// actually being edited, which are exactly the ones the maintainer would then
// have to notice were stale. Holding every icon's bytes in memory first costs
// a few kilobytes and removes the failure mode outright.
func generateAll(dir string, icons []iconSource, raster rasterizer) (int, error) {
	type pending struct {
		path string
		data []byte
	}
	rendered := make([]pending, 0, len(icons)*len(variants()))
	for _, ic := range icons {
		for _, v := range variants() {
			data, rasterErr := raster(ic.svg, v.color)
			if rasterErr != nil {
				return 0, fmt.Errorf("%s: %w", ic.name, rasterErr)
			}
			rendered = append(rendered, pending{filepath.Join(dir, ic.name+v.suffix+".webp"), data})
		}
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, err
	}
	written := 0
	for _, p := range rendered {
		if writeErr := os.WriteFile(p.path, p.data, 0o644); writeErr != nil { //nolint:gosec // generated asset, not sensitive
			return written, writeErr
		}
		written++
	}
	return written, nil
}

// checkAll reports whether every icon's light/dark WebP file under dir is
// present and byte-identical to what raster would produce.
func checkAll(dir string, icons []iconSource, raster rasterizer) error {
	var stale []string
	for _, ic := range icons {
		for _, v := range variants() {
			path := filepath.Join(dir, ic.name+v.suffix+".webp")
			want, err := raster(ic.svg, v.color)
			if err != nil {
				return fmt.Errorf("%s: %w", ic.name, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, want) {
				stale = append(stale, filepath.Base(path))
			}
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		return fmt.Errorf("icon webp assets are stale or missing, run `go run ./cmd/gen_icon_webp/`: %s", strings.Join(stale, ", "))
	}
	return nil
}

// rasterize renders svg (with every "currentColor" replaced by color) to a
// 16x16 PNG via rsvg-convert, then re-encodes it as lossless WebP via cwebp.
// This is the only function that shells out to external tools; production
// code reaches it through the [rasterizer] indirection.
//
// Both tools are invoked by their exec.LookPath-resolved absolute path,
// not by bare name: passing a bare name lets exec.Command re-resolve it
// against PATH at run time, which a writable-PATH-entry attack could hijack
// (Sonar go:S4036 / CWE-427 uncontrolled search path).
//
// Coverage note: the rsvg-convert LookPath and Run failure branches are
// each tested directly; the cwebp LookPath and Run failures are not, since
// forcing "rsvg-convert present, cwebp absent" needs test-machine-specific
// PATH surgery, and forcing cwebp's Run to fail needs a PNG rsvg-convert
// itself refuses to ever emit — not worth it for a maintainer-only tool.
func rasterize(svg, color string) ([]byte, error) {
	colored := strings.ReplaceAll(svg, "currentColor", color)
	ctx := context.Background()

	rsvgPath, err := exec.LookPath(toolRsvg)
	if err != nil {
		return nil, err
	}
	rsvg := exec.CommandContext(ctx, rsvgPath, "-w", rasterSize, "-h", rasterSize, "--format=png")
	rsvg.Stdin = strings.NewReader(colored)
	var png bytes.Buffer
	rsvg.Stdout = &png
	var rsvgErr bytes.Buffer
	rsvg.Stderr = &rsvgErr
	if runErr := rsvg.Run(); runErr != nil {
		return nil, fmt.Errorf("rsvg-convert: %w: %s", runErr, rsvgErr.String())
	}

	cwebpPath, err := exec.LookPath(toolCwebp)
	if err != nil {
		return nil, err
	}
	cwebp := exec.CommandContext(ctx, cwebpPath, "-lossless", "-z", "9", "-quiet", "-o", "-", "--", "-")
	cwebp.Stdin = bytes.NewReader(png.Bytes())
	var webp bytes.Buffer
	cwebp.Stdout = &webp
	var cwebpErr bytes.Buffer
	cwebp.Stderr = &cwebpErr
	if runErr := cwebp.Run(); runErr != nil {
		return nil, fmt.Errorf("cwebp: %w: %s", runErr, cwebpErr.String())
	}
	return webp.Bytes(), nil
}

// extractIcons parses sourceFile's AST and returns every constant declared
// as svg<Name> = `<svg ...>`, in declaration order.
func extractIcons(path string) ([]iconSource, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	var icons []iconSource
	for _, decl := range file.Decls {
		icons = append(icons, constDeclIcons(decl)...)
	}
	return icons, nil
}

// constDeclIcons returns every svg<Name> = `<svg ...>` constant declared by
// decl, or nil if decl is not a const block.
func constDeclIcons(decl ast.Decl) []iconSource {
	gen, ok := decl.(*ast.GenDecl)
	if !ok || gen.Tok != token.CONST {
		return nil
	}
	var icons []iconSource
	for _, spec := range gen.Specs {
		icons = append(icons, valueSpecIcons(spec)...)
	}
	return icons
}

// valueSpecIcons returns every svg<Name> = `<svg ...>` constant declared by
// a single `name = value` spec (a spec can declare several names at once,
// and a grouped const block can also declare a name with no value of its
// own, inheriting the previous spec's — that case is skipped here since
// Names and Values no longer line up).
func valueSpecIcons(spec ast.Spec) []iconSource {
	vs, ok := spec.(*ast.ValueSpec)
	if !ok || len(vs.Names) != len(vs.Values) {
		return nil
	}
	var icons []iconSource
	for i, name := range vs.Names {
		if ic, found := svgConstIcon(name.Name, vs.Values[i]); found {
			icons = append(icons, ic)
		}
	}
	return icons
}

// svgConstIcon reports whether expr is a string literal holding SVG markup
// assigned to a svg<Name> identifier, returning the resolved iconSource.
func svgConstIcon(name string, expr ast.Expr) (iconSource, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return iconSource{}, false
	}
	// lit.Value is a lexically valid Go string literal (single-quoted,
	// double-quoted, or backquoted) because the parser already accepted it,
	// so Unquote only fails here for a literal this function doesn't want
	// anyway (its content isn't SVG markup).
	value, err := strconv.Unquote(lit.Value)
	if err != nil || !strings.HasPrefix(name, "svg") || !strings.HasPrefix(value, "<svg") {
		return iconSource{}, false
	}
	return iconSource{name: iconFileName(strings.TrimPrefix(name, "svg")), svg: value}, true
}

// iconFileName converts a Go identifier suffix like "MR" or "Branch" into a
// lowercase, filesystem-safe asset name ("mr", "branch").
func iconFileName(s string) string {
	return strings.ToLower(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, s))
}

// minLibrsvg is the oldest librsvg release measured to rasterize icons.go
// into the committed WebP bytes exactly.
//
// The renderer's stroke antialiasing changed between 2.54 and 2.58. Debian
// 12's 2.54.7 and Ubuntu 22.04's 2.52.5 each disagree with the committed
// assets on three of the nine icons — the three drawn with rounded caps and
// joins on diagonals — while Ubuntu 24.04's 2.58.0 and Debian 13's 2.60.0
// reproduce all eighteen files byte for byte, across two different cwebp
// releases. So 2.58.0 is the lowest version measured to agree, not the
// release the behavior changed in: 2.55 through 2.57 were never tested and
// are refused rather than assumed.
var minLibrsvg = [3]int{2, 58, 0}

// librsvgVersionRE matches the release number in rsvg-convert's --version
// banner ("rsvg-convert version 2.54.7"). The patch component is optional
// because some distribution builds print only major.minor.
var librsvgVersionRE = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

// parseLibrsvgVersion extracts the release triple from rsvg-convert's
// --version output. An absent patch component reads as zero.
func parseLibrsvgVersion(out string) ([3]int, error) {
	m := librsvgVersionRE.FindStringSubmatch(out)
	if m == nil {
		return [3]int{}, fmt.Errorf("no version number in rsvg-convert output %q", strings.TrimSpace(out))
	}
	var v [3]int
	for i := range v {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, fmt.Errorf("parse librsvg version %q: %w", m[0], err)
		}
		v[i] = n
	}
	return v, nil
}

// formatVersion renders a release triple as "major.minor.patch".
func formatVersion(v [3]int) string {
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}

// olderThan reports whether release a precedes release b.
func olderThan(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// librsvgVersion reports the release of the rsvg-convert found on PATH.
func librsvgVersion() ([3]int, error) {
	path, err := exec.LookPath(toolRsvg)
	if err != nil {
		return [3]int{}, err
	}
	out, err := exec.CommandContext(context.Background(), path, "--version").Output()
	if err != nil {
		return [3]int{}, fmt.Errorf("rsvg-convert --version: %w", err)
	}
	return parseLibrsvgVersion(string(out))
}

// requireRenderer reports whether this machine can reproduce the committed
// assets: both tools on PATH, and librsvg at least [minLibrsvg].
//
// The version is part of the requirement because the assets are compared
// byte for byte. An older librsvg does not merely fail --check with a
// misleading "stale assets" report; run without --check it would rewrite all
// eighteen files in its own dialect and the divergence would be committed.
// So generating is guarded as well as verifying.
func requireRenderer() error {
	if err := requireTools(toolRsvg, toolCwebp); err != nil {
		return err
	}
	v, err := librsvgVersion()
	if err != nil {
		return err
	}
	if olderThan(v, minLibrsvg) {
		return fmt.Errorf("librsvg %s renders these icons differently and cannot reproduce the committed assets (need >= %s); `make gen-icon-webp` and `make check-icon-webp` fall back to a pinned container when it is too old",
			formatVersion(v), formatVersion(minLibrsvg))
	}
	return nil
}

// requireTools reports an actionable error naming every tool in names that
// is missing from PATH, or nil if all are present.
func requireTools(names ...string) error {
	var missing []string
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("required tool(s) not found on PATH: %s (install librsvg >= %s and libwebp — `apt install librsvg2-bin webp` or `brew install librsvg webp` — or run `make check-icon-webp`, which falls back to a pinned container)", strings.Join(missing, ", "), formatVersion(minLibrsvg))
	}
	return nil
}

// repoRoot walks up from the working directory to the nearest ancestor
// containing a go.mod file.
//
// Coverage note: os.Getwd()'s own error path is not tested — reliably
// forcing it requires deleting the process's current directory out from
// under it, which is racy and OS-dependent.
func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", wd)
		}
		dir = parent
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gen_icon_webp:", err)
	os.Exit(1)
}
