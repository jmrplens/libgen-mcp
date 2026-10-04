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

// twinDir is where the English Starlight pages live, relative to the root.
const twinDir = "site/src/content/docs"

// versionPattern matches a version-shaped string: three numeric fields and an
// optional prerelease suffix. Boundaries are checked by [versionBoundary],
// since Go regular expressions have no lookbehind.
var versionPattern = regexp.MustCompile(`\d+\.\d+\.\d+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?`)

// releasePattern is what the VERSION file must hold.
// It is versionPattern anchored, so every value it accepts is one the scan
// finds whole again: an empty prerelease field (`2.2.0-rc..1`) would be
// written by a rewrite and then read back by --check as `2.2.0-rc`.
var releasePattern = regexp.MustCompile(`^` + versionPattern.String() + `$`)

// datePattern finds CITATION.cff's `date-released`, quoted or not.
var datePattern = regexp.MustCompile(`(?m)^date-released:\s*["']?(\d{4})-(\d{2})-(\d{2})["']?\s*$`)

// frontmatterPattern matches a page's leading YAML block, which the site never
// substitutes into and docs/ has no counterpart of.
var frontmatterPattern = regexp.MustCompile(`\A---\n[\s\S]*?\n---\n`)

// bibtexMonths are BibTeX's month macros, which GitHub's CITATION.cff
// converter writes.
var bibtexMonths = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}

// errStale reports that check mode found docs/ naming another release.
var errStale = errors.New("docs/ disagrees with the current release")

// kind is one token: what a page writes, what its value looks like written
// out, and where such a value stands on its own.
type kind struct {
	placeholder string
	pattern     *regexp.Regexp
	boundary    func(src string, start, end int) bool
}

// kinds lists every token the site replaces, in the order a page is rewritten.
// The month is matched only in BibTeX's `month = …` form, the one place it is
// written, because the abbreviations are also English words (`may`).
var kinds = []kind{
	{placeholder: "%%VERSION%%", pattern: versionPattern, boundary: versionBoundary},
	{placeholder: "%%RELEASE_YEAR%%", pattern: regexp.MustCompile(`\d{4}`), boundary: wordBoundary},
	{placeholder: "%%RELEASE_MONTH%%", pattern: regexp.MustCompile(`month = ([a-z]{3}|%%RELEASE_MONTH%%)`), boundary: wordBoundary},
}

// mention is one value-shaped string, or one token, at a byte range of a page.
type mention struct {
	start, end int
	token      bool
	text       string
}

// page pairs an English Starlight page that writes a token with its docs/
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

// run parses args, reads the release values and syncs or checks every page pair.
func run(args []string, stdout io.Writer) error {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}
	values, err := readValues(opts.root)
	if err != nil {
		return err
	}
	pages, err := findPages(opts.root)
	if err != nil {
		return err
	}
	var stale []string
	for _, p := range pages {
		found, pageErr := processPage(opts, p, values, stdout)
		if pageErr != nil {
			return pageErr
		}
		stale = append(stale, found...)
	}
	if len(stale) > 0 {
		return fmt.Errorf("%w (VERSION %s, CITATION.cff date-released %s %s), run `make gen-doc-versions`:\n  %s",
			errStale, values["%%VERSION%%"], values["%%RELEASE_MONTH%%"], values["%%RELEASE_YEAR%%"],
			strings.Join(stale, "\n  "))
	}
	return nil
}

// parseOptions reads the command line.
func parseOptions(args []string) (options, error) {
	fs := flag.NewFlagSet("gen_doc_versions", flag.ContinueOnError)
	var opts options
	fs.StringVar(&opts.root, "root", ".", "repository root")
	fs.BoolVar(&opts.check, "check", false, "fail when docs/ disagrees with the current release instead of rewriting it")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return opts, nil
}

// readValues returns every token's value: the VERSION file, and the year and
// BibTeX month of CITATION.cff's date-released. Both are read strictly, as the
// site build reads them.
func readValues(root string) (map[string]string, error) {
	raw, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return nil, fmt.Errorf("read VERSION: %w", err)
	}
	version := strings.TrimSpace(string(raw))
	if !releasePattern.MatchString(version) {
		return nil, fmt.Errorf("VERSION holds %q, which is not a release number", version)
	}
	cff, err := os.ReadFile(filepath.Join(root, "CITATION.cff"))
	if err != nil {
		return nil, fmt.Errorf("read CITATION.cff: %w", err)
	}
	m := datePattern.FindStringSubmatch(string(cff))
	month := 0
	if m != nil {
		_, _ = fmt.Sscanf(m[2], "%d", &month)
	}
	if month < 1 || month > 12 {
		return nil, errors.New("CITATION.cff has no date-released of the form YYYY-MM-DD")
	}
	return map[string]string{
		"%%VERSION%%":       version,
		"%%RELEASE_YEAR%%":  m[1],
		"%%RELEASE_MONTH%%": bibtexMonths[month-1],
	}, nil
}

// findPages lists every English Starlight page that writes a token, paired
// with its docs/ twin. A page that writes one and has no twin is an error: the
// token's positions are the only record of which docs/ mentions are current,
// so a twin renamed away would silently stop being kept.
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

// holdsToken reports whether src writes any token.
func holdsToken(src string) bool {
	for _, k := range kinds {
		if strings.Contains(src, k.placeholder) {
			return true
		}
	}
	return false
}

// addPage appends the pair for one Starlight file when it writes a token.
func addPage(root, base, path string, pages *[]page) error {
	ext := filepath.Ext(path)
	if ext != ".mdx" && ext != ".md" {
		return nil
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !holdsToken(string(src)) {
		return nil
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return err
	}
	doc := filepath.ToSlash(filepath.Join("docs", strings.TrimSuffix(rel, ext)+".md"))
	twin := filepath.ToSlash(filepath.Join(twinDir, rel))
	if _, statErr := os.Stat(filepath.Join(root, doc)); statErr != nil {
		return fmt.Errorf("%s writes a release token but has no docs/ twin at %s", twin, doc)
	}
	*pages = append(*pages, page{twin: twin, doc: doc})
	return nil
}

// processPage aligns one pair for every token its twin writes and, outside
// check mode, writes the docs/ page when it changed. It returns one line per
// stale mention in check mode.
func processPage(opts options, p page, values map[string]string, stdout io.Writer) ([]string, error) {
	twinRaw, err := os.ReadFile(filepath.Join(opts.root, p.twin))
	if err != nil {
		return nil, err
	}
	docPath := filepath.Join(opts.root, p.doc)
	docRaw, err := os.ReadFile(docPath)
	if err != nil {
		return nil, err
	}
	// The frontmatter is blanked rather than cut, so a line number reported
	// against the twin is still the file's.
	twinSrc := frontmatterPattern.ReplaceAllStringFunc(string(twinRaw), func(fm string) string {
		return strings.Repeat("\n", strings.Count(fm, "\n"))
	})
	docSrc := string(docRaw)
	var lines []string
	changed := 0
	for _, k := range kinds {
		if !strings.Contains(twinSrc, k.placeholder) {
			continue
		}
		out, stale, syncErr := syncPage(k, twinSrc, docSrc, values[k.placeholder])
		if syncErr != nil {
			return nil, fmt.Errorf("%s against %s: %w", p.doc, p.twin, syncErr)
		}
		for _, m := range stale {
			lines = append(lines, fmt.Sprintf("%s:%d names %s where %s writes %s",
				p.doc, lineOf(docSrc, m.start), m.text, p.twin, k.placeholder))
		}
		changed += len(stale)
		docSrc = out
	}
	if opts.check {
		return lines, nil
	}
	if changed == 0 {
		return nil, nil
	}
	if writeErr := os.WriteFile(docPath, []byte(docSrc), 0o644); writeErr != nil { //nolint:gosec // a tracked docs page, world-readable like its siblings
		return nil, writeErr
	}
	_, err = fmt.Fprintf(stdout, "%s: %d mention(s) brought up to date\n", p.doc, changed)
	return nil, err
}

// syncPage lines up the docs page's mentions of one kind with the twin's and
// returns the page with every current-release mention set to value, plus the
// mentions that did not already say it.
func syncPage(k kind, twinSrc, docSrc, value string) (string, []mention, error) {
	twin := mentions(k, twinSrc, true)
	doc := mentions(k, docSrc, false)
	if i := firstDisagreement(twin, doc); i >= 0 {
		return "", nil, misaligned(k, twinSrc, docSrc, twin, doc, i)
	}
	var b strings.Builder
	var stale []mention
	last := 0
	for i, m := range doc {
		if !twin[i].token {
			continue
		}
		b.WriteString(docSrc[last:m.start])
		b.WriteString(value)
		last = m.end
		if m.text != value {
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
func misaligned(k kind, twinSrc, docSrc string, twin, doc []mention, i int) error {
	describe := func(src string, ms []mention) string {
		if i >= len(ms) {
			return "nothing (the page has no more of them)"
		}
		return fmt.Sprintf("%q at line %d", ms[i].text, lineOf(src, ms[i].start))
	}
	return fmt.Errorf("mention %d of the kind %s writes differs: the twin has %s, docs/ has %s. "+
		"The two copies must name the same values in the same order, with %s where the twin means the current release",
		i+1, k.placeholder, describe(twinSrc, twin), describe(docSrc, doc), k.placeholder)
}

// mentions returns every string of src shaped like a value of k, in order,
// counting a token in place of a value when withToken is set. A pattern with a
// capture group names the value by the group.
func mentions(k kind, src string, withToken bool) []mention {
	var out []mention
	for _, loc := range k.pattern.FindAllStringSubmatchIndex(src, -1) {
		start, end := loc[0], loc[1]
		if len(loc) > 2 {
			start, end = loc[2], loc[3]
		}
		if src[start:end] == k.placeholder || !k.boundary(src, start, end) {
			continue
		}
		out = append(out, mention{start: start, end: end, text: src[start:end]})
	}
	if withToken {
		for offset := 0; ; {
			i := strings.Index(src[offset:], k.placeholder)
			if i < 0 {
				break
			}
			start := offset + i
			offset = start + len(k.placeholder)
			out = append(out, mention{start: start, end: offset, token: true, text: k.placeholder})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].start < out[b].start })
	return out
}

// versionBoundary reports whether src[start:end] stands on its own as a
// version: not the tail of a longer number or identifier (`go1.27.0`,
// `10.0.0.1`), though a leading `v` is allowed, and not the head of a
// four-part address.
func versionBoundary(src string, start, end int) bool {
	if start > 0 {
		c := src[start-1]
		if isDigit(c) || c == '.' || (isLetter(c) && c != 'v') {
			return false
		}
	}
	return end+1 >= len(src) || src[end] != '.' || !isDigit(src[end+1])
}

// wordBoundary reports whether src[start:end] is not part of a longer run of
// letters or digits.
func wordBoundary(src string, start, end int) bool {
	if start > 0 && (isDigit(src[start-1]) || isLetter(src[start-1])) {
		return false
	}
	return end >= len(src) || (!isDigit(src[end]) && !isLetter(src[end]))
}

// isDigit reports whether c is an ASCII digit.
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isLetter reports whether c is an ASCII letter.
func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// lineOf returns the 1-based line holding byte offset.
func lineOf(src string, offset int) int {
	return strings.Count(src[:offset], "\n") + 1
}
