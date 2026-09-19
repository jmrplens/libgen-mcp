package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/jmrplens/libgen-mcp/cmd/internal/mcpsurface"
	"github.com/jmrplens/libgen-mcp/internal/config"
)

// toolName prefixes every line this command writes about itself.
const toolName = "audit_doc_names"

// exit is os.Exit behind a variable, so the one line main carries is reachable
// from a test rather than only from a process.
var exit = os.Exit

// docRoots are the files and trees this audit reads, relative to the
// repository root. A directory is walked for .md and .mdx; a file is read as
// it is.
var docRoots = []string{
	"docs",
	"site/src/content/docs",
	"README.md",
	"llms-install.md",
	"CLAUDE.md",
	"npm/libgen-mcp/README.md",
}

// skipTrees are the directories under a root that are not read. See the
// command's doc comment for why the historical tree is one of them.
var skipTrees = []string{"docs/superpowers"}

func main() {
	exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses the command line and performs the sweep.
func run(args []string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet(toolName, flag.ContinueOnError)
	flags.SetOutput(errOut)
	var (
		dir   = flags.String("dir", ".", "repository root")
		check = flags.Bool("check", false, "exit non-zero when a name does not resolve")
	)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	surface, err := readSurface()
	if err != nil {
		fmt.Fprintf(errOut, "%s: %v\n", toolName, err)
		return 2
	}
	pages, err := readPages(*dir)
	if err != nil {
		fmt.Fprintf(errOut, "%s: %v\n", toolName, err)
		return 2
	}

	report := audit(pages, surface)
	writeReport(out, report)
	if *check && len(report.Findings) > 0 {
		fmt.Fprintf(errOut, "%s: %d name(s) in the documentation do not resolve\n", toolName, len(report.Findings))
		return 1
	}
	return 0
}

// Surface is what the server actually has, read from the registration rather
// than from a list this command keeps.
type Surface struct {
	EnvNames []string
	Sources  []string
	Tools    []string
	Prompts  []string
}

// readSurface registers the tools and prompts for real and reads the two
// lists the server validates against.
func readSurface() (Surface, error) {
	cfg := mcpsurface.DocsConfig()
	tools, err := mcpsurface.Tools(cfg)
	if err != nil {
		return Surface{}, fmt.Errorf("list the tools: %w", err)
	}
	prompts, err := mcpsurface.Prompts(cfg)
	if err != nil {
		return Surface{}, fmt.Errorf("list the prompts: %w", err)
	}
	surface := Surface{EnvNames: config.KnownEnvNames(), Sources: config.KnownSources}
	for _, tool := range tools {
		surface.Tools = append(surface.Tools, tool.Name)
	}
	for _, prompt := range prompts {
		surface.Prompts = append(surface.Prompts, prompt.Name)
	}
	return surface, nil
}

// Page is one documentation file, kept with its repository-relative path so a
// finding can name a line in it.
type Page struct {
	Path  string
	Lines []string
}

// readPages reads every documentation file under the roots.
func readPages(dir string) ([]Page, error) {
	var pages []Page
	for _, root := range docRoots {
		found, err := readRoot(dir, root)
		if err != nil {
			return nil, err
		}
		pages = append(pages, found...)
	}
	if len(pages) == 0 {
		return nil, errors.New("no documentation was read; the roots are wrong or the tree is not this repository")
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Path < pages[j].Path })
	return pages, nil
}

// readRoot reads one root: a file as it is, a directory walked for Markdown.
func readRoot(dir, root string) ([]Page, error) {
	full := filepath.Join(dir, filepath.FromSlash(root))
	info, err := os.Stat(full)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	if !info.IsDir() {
		page, readErr := readPage(dir, full)
		if readErr != nil {
			return nil, readErr
		}
		return []Page{page}, nil
	}

	var pages []Page
	walkErr := filepath.WalkDir(full, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !isMarkdown(entry.Name()) {
			return err
		}
		relative, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		if skipped(filepath.ToSlash(relative)) {
			return nil
		}
		page, readErr := readPage(dir, path)
		if readErr != nil {
			return readErr
		}
		pages = append(pages, page)
		return nil
	})
	return pages, walkErr
}

// isMarkdown reports whether a file name is one this audit reads.
func isMarkdown(name string) bool {
	return strings.HasSuffix(name, ".md") || strings.HasSuffix(name, ".mdx")
}

// skipped reports whether a repository-relative path is under a tree this
// audit leaves alone.
func skipped(relative string) bool {
	for _, tree := range skipTrees {
		if relative == tree || strings.HasPrefix(relative, tree+"/") {
			return true
		}
	}
	return false
}

// readPage reads one file into its lines.
func readPage(dir, path string) (Page, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Page{}, fmt.Errorf("read %s: %w", path, err)
	}
	relative, err := filepath.Rel(dir, path)
	if err != nil {
		relative = path
	}
	return Page{
		Path:  filepath.ToSlash(relative),
		Lines: strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n"),
	}, nil
}

// Finding is one name in the documentation that does not resolve, or one name
// on the surface that the documentation never mentions.
type Finding struct {
	Kind    string `json:"kind"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Name    string `json:"name"`
	Message string `json:"message"`
}

// Report is what one sweep found.
type Report struct {
	Findings []Finding `json:"findings"`
	Summary  Summary   `json:"summary"`
}

// Summary aggregates what the sweep tracks.
type Summary struct {
	Pages     int `json:"pages"`
	EnvTokens int `json:"env_tokens"`
	Sources   int `json:"sources"`
	Stale     int `json:"stale"`
	Undocs    int `json:"undocumented"`
}

// envToken matches a variable this server would define. The prefix makes it
// unmistakable, which is why this namespace is checked and the tool names are
// not.
var envToken = regexp.MustCompile(`LIBGEN_MCP_[A-Z0-9_]+`)

// nameDirective declares a name the page writes on purpose although the server
// does not have it:
//
//	<!-- libgen:allow-name LIBGEN_MCP_HTTP: the spelling this server deliberately does not use -->
//
// It exists because the first run of this audit found twelve findings and every
// one of them was a page naming a variable in order to say it is not read —
// "LIBGEN_MCP_OTEL_EXPORTER_OTLP_ENDPOINT would be a variable nothing reads" is
// a sentence that has to write the name to make its point. A counter-example is
// documentation doing its job, and a gate that cannot tell one from a mistake
// would be answered with a list of exemptions inside this command, which is the
// audit moving out of the tree and into the auditor.
//
// It excuses the name in the page that declares it, and nowhere else: the same
// counter-example in six pages is six sentences, each of which has to be true
// on its own. Both halves are required, and a declaration that excuses nothing
// is itself a finding, so one left behind by a later edit cannot quietly widen
// the gate.
// Both comment syntaxes are accepted because both are needed. MDX does not
// parse an HTML comment at all — it reads the "!" as the start of a name and
// fails the build — so a declaration in a .mdx page is written the MDX way,
// and one in a .md page the Markdown way. Neither renders.
// They are two expressions rather than one with the delimiters alternated,
// because RE2 has no backreference: an alternation accepts an opener closed by
// the other syntax, which is a malformed comment neither parser hides — so
// the name would be excused in text a reader sees.
var nameDirectives = []*regexp.Regexp{
	regexp.MustCompile(`<!--\s*libgen:allow-name\s+([A-Za-z0-9_]+)\s*:\s*(\S.*?)\s*-->`),
	regexp.MustCompile(`\{/\*\s*libgen:allow-name\s+([A-Za-z0-9_]+)\s*:\s*(\S.*?)\s*\*/\}`),
}

// declaredNames lists the names a line declares, in either syntax.
func declaredNames(line string) []string {
	var names []string
	for _, directive := range nameDirectives {
		for _, match := range directive.FindAllStringSubmatch(line, -1) {
			names = append(names, match[1])
		}
	}
	return names
}

// withoutDeclarations returns a line with every declaration taken out of it, so
// what a declaration has to write is not read as what the page says.
func withoutDeclarations(line string) string {
	for _, directive := range nameDirectives {
		line = directive.ReplaceAllString(line, "")
	}
	return line
}

// sourceValue matches a source named in a shape that can only mean a source:
// a JSON "source" field, or one of the pinning arguments spelled as JSON.
var sourceValue = regexp.MustCompile(`"source"\s*:\s*"([a-z0-9_]+)"`)

// sourcesList matches an assignment of the source allow list, in a shell line
// or in prose, up to the end of the value.
var sourcesList = regexp.MustCompile(`LIBGEN_MCP_SOURCES\s*[=:]\s*"?([a-z0-9_,\s]+)"?`)

// audit reads every page and returns what did not resolve.
func audit(pages []Page, surface Surface) Report {
	report := Report{Summary: Summary{Pages: len(pages)}}
	mentioned := map[string]bool{}
	for _, page := range pages {
		auditPage(page, surface, &report, mentioned)
	}
	undocs := undocumented(surface, mentioned)
	report.Summary.Undocs = len(undocs)
	report.Findings = append(report.Findings, undocs...)
	sortFindings(report.Findings)
	return report
}

// auditPage reads one page for the two token namespaces and records which
// surface names it mentions.
func auditPage(page Page, surface Surface, report *Report, mentioned map[string]bool) {
	declared, declaredAt := declarations(page)
	for number, raw := range page.Lines {
		// The declaration has to write the name it excuses, so it is taken out
		// of the line before anything is matched in it. Otherwise every
		// declaration would excuse itself and no stale one could ever be
		// reported, which is the half of the mechanism that keeps it honest.
		line := withoutDeclarations(raw)
		auditEnvTokens(line, page, number+1, surface, report, declared)
		auditSourceNames(line, page, number+1, surface, report)
		noteMentions(line, surface, mentioned)
	}
	for name, used := range declared {
		if used {
			continue
		}
		report.Summary.Stale++
		report.Findings = append(report.Findings, Finding{
			Kind: "stale", File: page.Path, Line: declaredAt[name], Name: name,
			Message: "this page declares the name and never writes it",
		})
	}
}

// auditEnvTokens reports the variables one line names that nothing reads, and
// marks the declaration that excuses one as used.
func auditEnvTokens(line string, page Page, number int, surface Surface, report *Report, declared map[string]bool) {
	for _, name := range envToken.FindAllString(line, -1) {
		if slices.Contains(surface.EnvNames, name) {
			continue
		}
		if _, excused := declared[name]; excused {
			declared[name] = true
			continue
		}
		report.Summary.EnvTokens++
		report.Findings = append(report.Findings, Finding{
			Kind: "env", File: page.Path, Line: number, Name: name,
			Message: "no LIBGEN_MCP_ variable of that name is read by internal/config",
		})
	}
}

// auditSourceNames reports the sources one line names, in a shape that can
// only mean a source, that nobody can select.
func auditSourceNames(line string, page Page, number int, surface Surface, report *Report) {
	for _, name := range namedSources(line) {
		if slices.Contains(surface.Sources, name) {
			continue
		}
		report.Summary.Sources++
		report.Findings = append(report.Findings, Finding{
			Kind: "source", File: page.Path, Line: number, Name: name,
			Message: "no download source of that name is in config.KnownSources",
		})
	}
}

// declarations reads the names a page declares on purpose, and the line each
// declaration is on so a stale one can be pointed at. The value is whether the
// declaration was used, which auditPage fills in.
func declarations(page Page) (used map[string]bool, at map[string]int) {
	used, at = map[string]bool{}, map[string]int{}
	for number, line := range page.Lines {
		// A directive inside a code span is being shown, not made: the gate
		// record documents this mechanism by printing its syntax, and reading
		// that as a declaration made the record's own row a stale one.
		for _, name := range declaredNames(codeSpans.ReplaceAllString(line, "")) {
			used[name] = false
			at[name] = number + 1
		}
	}
	return used, at
}

// codeSpans matches an inline code span, so what a page quotes can be told
// from what it declares.
var codeSpans = regexp.MustCompile("`[^`]*`")

// namedSources lists the sources a line names in a shape that can only mean a
// source.
func namedSources(line string) []string {
	var names []string
	for _, match := range sourceValue.FindAllStringSubmatch(line, -1) {
		names = append(names, match[1])
	}
	for _, match := range sourcesList.FindAllStringSubmatch(line, -1) {
		for name := range strings.SplitSeq(match[1], ",") {
			if name = strings.TrimSpace(name); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

// noteMentions records the registered names a line mentions, so the other
// direction of the audit can say which are documented nowhere.
//
// A tool or prompt name is matched as a whole word only, which is what keeps
// "search" in a sentence about searching from counting: the documentation
// names a tool in a code span, a heading or a list, and a word boundary is as
// much as this can ask without a list of exemptions.
func noteMentions(line string, surface Surface, mentioned map[string]bool) {
	for _, name := range slices.Concat(surface.Tools, surface.Prompts) {
		if mentioned[name] {
			continue
		}
		if wordAt(line, name) {
			mentioned[name] = true
		}
	}
}

// wordAt reports whether line contains name bounded by something other than a
// name character, so get_details does not match get_details_extra.
func wordAt(line, name string) bool {
	for index := 0; ; {
		found := strings.Index(line[index:], name)
		if found < 0 {
			return false
		}
		start := index + found
		end := start + len(name)
		if !nameByte(line, start-1) && !nameByte(line, end) {
			return true
		}
		index = start + 1
	}
}

// nameByte reports whether the byte at an offset could continue an identifier.
// An offset outside the line is a boundary.
func nameByte(line string, at int) bool {
	if at < 0 || at >= len(line) {
		return false
	}
	c := line[at]
	return c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}

// undocumented lists the registered names no page mentions.
func undocumented(surface Surface, mentioned map[string]bool) []Finding {
	var findings []Finding
	for _, kind := range []struct {
		label string
		names []string
	}{
		{label: "tool", names: surface.Tools},
		{label: "prompt", names: surface.Prompts},
	} {
		for _, name := range kind.names {
			if mentioned[name] {
				continue
			}
			findings = append(findings, Finding{
				Kind: kind.label, Name: name,
				Message: "the server registers this " + kind.label + " and no page read here mentions it",
			})
		}
	}
	return findings
}

// sortFindings orders findings by where they are, so a report reads in the
// order a person would open the files. The ones with no file — a name nothing
// documents — sort last, because they are a different kind of work.
func sortFindings(findings []Finding) {
	sort.Slice(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if (a.File == "") != (b.File == "") {
			return a.File != ""
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Name < b.Name
	})
}

// writeReport prints what the sweep found.
func writeReport(out io.Writer, report Report) {
	for _, finding := range report.Findings {
		if finding.File == "" {
			fmt.Fprintf(out, "  %-8s %s: %s\n", finding.Kind, finding.Name, finding.Message)
			continue
		}
		fmt.Fprintf(out, "  %s:%d  %s %s: %s\n", finding.File, finding.Line, finding.Kind, finding.Name, finding.Message)
	}
	fmt.Fprintf(out, "\n%s: read %d pages; %d unknown variable(s), %d unknown source(s), %d stale declaration(s), %d undocumented name(s)\n",
		toolName, report.Summary.Pages, report.Summary.EnvTokens, report.Summary.Sources,
		report.Summary.Stale, report.Summary.Undocs)
}
