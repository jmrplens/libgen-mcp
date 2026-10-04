// The command's implementation: page discovery, the mention alignment, and the
// rewrite or check.

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// versionToken is what a Starlight page writes where the current release
// belongs. site/src/lib/release-version.mjs replaces it at build time.
const versionToken = "%%VERSION%%"

// twinDir is where the English Starlight pages live, relative to the root.
const twinDir = "site/src/content/docs"

// versionPattern matches a version-shaped string: three numeric fields and an
// optional prerelease suffix. Boundaries are checked by [atBoundary], since Go
// regular expressions have no lookbehind.
var versionPattern = regexp.MustCompile(`\d+\.\d+\.\d+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?`)

// releasePattern is what the VERSION file must hold.
// It is versionPattern anchored, so every value it accepts is one the scan
// finds whole again: an empty prerelease field (`2.2.0-rc..1`) would be
// written by a rewrite and then read back by --check as `2.2.0-rc`.
var releasePattern = regexp.MustCompile(`^` + versionPattern.String() + `$`)

// errStale reports that check mode found docs/ naming another release.
var errStale = errors.New("docs/ disagrees with VERSION")

// mention is one version-shaped string, or one token, at a byte range of a page.
type mention struct {
	start, end int
	token      bool
	text       string
}

// page pairs an English Starlight page that writes the token with its docs/
// twin, both relative to the root.
type page struct {
	twin, doc string
}

// options holds the parsed flags.
type options struct {
	root  string
	check bool
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run parses args, reads VERSION and syncs or checks every page pair.
func run(args []string, stdout io.Writer) error {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}
	version, err := readVersion(opts.root)
	if err != nil {
		return err
	}
	pages, err := findPages(opts.root)
	if err != nil {
		return err
	}
	var stale []string
	for _, p := range pages {
		found, pageErr := processPage(opts, p, version, stdout)
		if pageErr != nil {
			return pageErr
		}
		stale = append(stale, found...)
	}
	if len(stale) > 0 {
		return fmt.Errorf("%w (VERSION is %s), run `make gen-doc-versions`:\n  %s",
			errStale, version, strings.Join(stale, "\n  "))
	}
	return nil
}

// parseOptions reads the command line.
func parseOptions(args []string) (options, error) {
	fs := flag.NewFlagSet("gen_doc_versions", flag.ContinueOnError)
	var opts options
	fs.StringVar(&opts.root, "root", ".", "repository root")
	fs.BoolVar(&opts.check, "check", false, "fail when docs/ disagrees with VERSION instead of rewriting it")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return opts, nil
}

// readVersion returns the trimmed VERSION file, refusing anything that is not a
// release number.
func readVersion(root string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return "", fmt.Errorf("read VERSION: %w", err)
	}
	version := strings.TrimSpace(string(raw))
	if !releasePattern.MatchString(version) {
		return "", fmt.Errorf("VERSION holds %q, which is not a release number", version)
	}
	return version, nil
}

// findPages lists every English Starlight page that writes the token, paired
// with its docs/ twin. A page that writes the token and has no twin is an
// error: the token's positions are the only record of which docs/ mentions are
// current, so a twin renamed away would silently stop being kept.
func findPages(root string) ([]page, error) {
	base := filepath.Join(root, twinDir)
	var pages []page
	err := filepath.WalkDir(base, func(path string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != base && d.Name() == "es" {
				return filepath.SkipDir
			}
			return nil
		}
		return addPage(root, base, path, &pages)
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", twinDir, err)
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].doc < pages[j].doc })
	return pages, nil
}

// addPage appends the pair for one Starlight file when it writes the token.
func addPage(root, base, path string, pages *[]page) error {
	ext := filepath.Ext(path)
	if ext != ".mdx" && ext != ".md" {
		return nil
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !strings.Contains(string(src), versionToken) {
		return nil
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return err
	}
	doc := filepath.ToSlash(filepath.Join("docs", strings.TrimSuffix(rel, ext)+".md"))
	twin := filepath.ToSlash(filepath.Join(twinDir, rel))
	if _, statErr := os.Stat(filepath.Join(root, doc)); statErr != nil {
		return fmt.Errorf("%s writes %s but has no docs/ twin at %s", twin, versionToken, doc)
	}
	*pages = append(*pages, page{twin: twin, doc: doc})
	return nil
}

// processPage aligns one pair and, outside check mode, writes the docs/ page
// when it changed. It returns one line per stale mention in check mode.
func processPage(opts options, p page, version string, stdout io.Writer) ([]string, error) {
	twinSrc, err := os.ReadFile(filepath.Join(opts.root, p.twin))
	if err != nil {
		return nil, err
	}
	docPath := filepath.Join(opts.root, p.doc)
	docSrc, err := os.ReadFile(docPath)
	if err != nil {
		return nil, err
	}
	out, stale, err := syncPage(string(twinSrc), string(docSrc), version)
	if err != nil {
		return nil, fmt.Errorf("%s against %s: %w", p.doc, p.twin, err)
	}
	if opts.check {
		lines := make([]string, 0, len(stale))
		for _, m := range stale {
			lines = append(lines, fmt.Sprintf("%s:%d names %s where %s writes the current release",
				p.doc, lineOf(string(docSrc), m.start), m.text, p.twin))
		}
		return lines, nil
	}
	if len(stale) == 0 {
		return nil, nil
	}
	if writeErr := os.WriteFile(docPath, []byte(out), 0o644); writeErr != nil { //nolint:gosec // a tracked docs page, world-readable like its siblings
		return nil, writeErr
	}
	_, err = fmt.Fprintf(stdout, "%s: %d mention(s) set to %s\n", p.doc, len(stale), version)
	return nil, err
}

// syncPage lines up the docs page's mentions with the twin's and returns the
// page with every current-release mention set to version, plus the mentions
// that did not already say it.
func syncPage(twinSrc, docSrc, version string) (string, []mention, error) {
	twin := mentions(twinSrc, true)
	doc := mentions(docSrc, false)
	if i := firstDisagreement(twin, doc); i >= 0 {
		return "", nil, misaligned(twinSrc, docSrc, twin, doc, i)
	}
	var b strings.Builder
	var stale []mention
	last := 0
	for i, m := range doc {
		if !twin[i].token {
			continue
		}
		b.WriteString(docSrc[last:m.start])
		b.WriteString(version)
		last = m.end
		if m.text != version {
			stale = append(stale, m)
		}
	}
	b.WriteString(docSrc[last:])
	return b.String(), stale, nil
}

// firstDisagreement returns the first index at which the two sequences cannot
// be the same page, or -1 when they line up: the same length, and every
// literal mention of the twin repeated exactly in the docs page.
func firstDisagreement(twin, doc []mention) int {
	n := min(len(twin), len(doc))
	for i := range n {
		if !twin[i].token && twin[i].text != doc[i].text {
			return i
		}
	}
	if len(twin) != len(doc) {
		return n
	}
	return -1
}

// misaligned describes where the two copies of a page stopped agreeing.
func misaligned(twinSrc, docSrc string, twin, doc []mention, i int) error {
	describe := func(src string, ms []mention) string {
		if i >= len(ms) {
			return "nothing (the page has no more versions)"
		}
		return fmt.Sprintf("%q at line %d", ms[i].text, lineOf(src, ms[i].start))
	}
	return fmt.Errorf("version mention %d differs: the twin has %s, docs/ has %s. "+
		"The two copies must name the same versions in the same order, with %s where the twin means the current release",
		i+1, describe(twinSrc, twin), describe(docSrc, doc), versionToken)
}

// mentions returns every version-shaped string of src in order and, when
// withToken is set, every token as well.
func mentions(src string, withToken bool) []mention {
	var out []mention
	for _, loc := range versionPattern.FindAllStringIndex(src, -1) {
		if atBoundary(src, loc[0], loc[1]) {
			out = append(out, mention{start: loc[0], end: loc[1], text: src[loc[0]:loc[1]]})
		}
	}
	if withToken {
		for offset := 0; ; {
			i := strings.Index(src[offset:], versionToken)
			if i < 0 {
				break
			}
			start := offset + i
			offset = start + len(versionToken)
			out = append(out, mention{start: start, end: offset, token: true, text: versionToken})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].start < out[b].start })
	return out
}

// atBoundary reports whether src[start:end] stands on its own as a version: not
// the tail of a longer number or identifier (`go1.27.0`, `10.0.0.1`), though a
// leading `v` is allowed, and not the head of a four-part address.
func atBoundary(src string, start, end int) bool {
	if start > 0 {
		c := src[start-1]
		if isDigit(c) || c == '.' || (isLetter(c) && c != 'v') {
			return false
		}
	}
	return end+1 >= len(src) || src[end] != '.' || !isDigit(src[end+1])
}

// isDigit reports whether c is an ASCII digit.
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isLetter reports whether c is an ASCII letter.
func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// lineOf returns the 1-based line holding byte offset.
func lineOf(src string, offset int) int {
	return strings.Count(src[:offset], "\n") + 1
}
