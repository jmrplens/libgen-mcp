package main

import (
	"sort"
	"strings"
)

// Finding is one value that reaches a Markdown construct the audit judges,
// with the verdict it reached and the reason for it.
type Finding struct {
	Package    string `json:"package"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Function   string `json:"function"`
	Context    string `json:"context"`
	Verb       string `json:"verb"`
	Expression string `json:"expression"`
	Verdict    string `json:"verdict"`
	Reason     string `json:"reason,omitempty"`
	Wants      string `json:"wants,omitempty"`
}

// Report is the JSON work list one run produces.
type Report struct {
	Contexts   string      `json:"contexts"`
	Findings   []Finding   `json:"findings"`
	Unresolved []Finding   `json:"unresolved"`
	Excused    []Finding   `json:"excused"`
	Stale      []Directive `json:"stale"`
	Missing    []string    `json:"missing"`
	Summary    Summary     `json:"summary"`
}

// Summary aggregates what the sweep tracks.
type Summary struct {
	Holes      int `json:"holes"`
	Judged     int `json:"judged"`
	Safe       int `json:"safe"`
	Unescaped  int `json:"unescaped"`
	Unresolved int `json:"unresolved"`
	Excused    int `json:"excused"`
	Stale      int `json:"stale"`
	Missing    int `json:"missing"`
	Packages   int `json:"packages"`
}

// markdownEntryPoints are the functions that render Markdown for a client to
// read, named rather than discovered.
//
// The sweep walks every function in the swept packages, so a renderer not
// listed here is still audited; what the list adds is the other direction. A
// renderer that is renamed, split or deleted drops out of the sweep silently,
// and a gate that reports nothing because it found nothing to look at reads
// exactly like a gate that passed. A name here with no declaration behind it
// fails the run instead.
var markdownEntryPoints = map[string][]string{
	"internal/tools": {
		"renderSearchMarkdown", "renderDetailsMarkdown", "renderReadMarkdown",
		"renderDownloadMarkdown", "renderResolvedMarkdown",
		"writeNextSteps", "writeOpenAccess", "writeCitation", "writeEnrichment",
		"renderMatches", "renderOutline",
	},
	"internal/prompts": {
		"noCandidatesText", "requestedLine", "candidateText",
		"renderTable", "renderCandidates", "researchTopicText", "writeSection",
	},
	toolutilDir: {
		"MdTitleLink", "MdAutolink", "MdCodeSpan", "MarkdownFencedBlock",
	},
}

// auditPass is one sweep in progress.
type auditPass struct {
	prog       *program
	sel        selection
	root       string
	classifier *classifier
	directives map[directiveKey]Directive
	used       map[directiveKey]bool
	report     Report
}

// audit performs one sweep and returns what it found.
func audit(prog *program, sel selection, root string) Report {
	pass := &auditPass{
		prog:       prog,
		sel:        sel,
		root:       root,
		classifier: newClassifier(prog),
		directives: collectDirectives(prog, root),
		used:       map[directiveKey]bool{},
	}
	pass.report.Contexts = sel.label
	pass.report.Summary.Packages = len(prog.order)
	for _, hole := range collectHoles(prog) {
		pass.judge(hole)
	}
	pass.finish()
	return pass.report
}

// judge classifies one hole and files it where its verdict belongs.
func (p *auditPass) judge(hole sinkHole) {
	p.report.Summary.Holes++
	if !p.sel.judges(hole.ctx) || !hole.escapable() {
		return
	}
	p.report.Summary.Judged++
	where := scope{pkg: hole.fn.pkg, fn: hole.fn}
	result, why := p.classifier.classify(hole.expr, where, 0)
	if result == safe {
		p.report.Summary.Safe++
		return
	}
	finding := p.newFinding(hole, result, why)
	key := directiveKey{pkg: hole.fn.pkg.dir, expression: finding.Expression}
	if _, excused := p.directives[key]; excused {
		p.used[key] = true
		p.report.Excused = append(p.report.Excused, finding)
		return
	}
	if result == unescaped {
		p.report.Findings = append(p.report.Findings, finding)
		return
	}
	p.report.Unresolved = append(p.report.Unresolved, finding)
}

// newFinding renders one hole's verdict for a report.
func (p *auditPass) newFinding(hole sinkHole, result verdict, why string) Finding {
	at := p.prog.position(hole.pos)
	return Finding{
		Package:    hole.fn.pkg.dir,
		File:       relativePath(at.Filename, p.root),
		Line:       at.Line,
		Function:   hole.fn.name,
		Context:    hole.ctx.String(),
		Verb:       hole.verb,
		Expression: exprText(p.prog.fset, hole.expr),
		Verdict:    result.String(),
		Reason:     why,
		Wants:      hole.ctx.wants(),
	}
}

// finish sorts the report, records the exemptions nothing used, and checks
// that every named entry point is still there to audit.
func (p *auditPass) finish() {
	sortFindings(p.report.Findings)
	sortFindings(p.report.Unresolved)
	sortFindings(p.report.Excused)
	for key, directive := range p.directives {
		if !p.used[key] {
			p.report.Stale = append(p.report.Stale, directive)
		}
	}
	sort.Slice(p.report.Stale, func(i, j int) bool {
		return staleLess(p.report.Stale[i], p.report.Stale[j])
	})
	p.report.Missing = missingEntryPoints(p.prog)
	p.report.Summary.Unescaped = len(p.report.Findings)
	p.report.Summary.Unresolved = len(p.report.Unresolved)
	p.report.Summary.Excused = len(p.report.Excused)
	p.report.Summary.Stale = len(p.report.Stale)
	p.report.Summary.Missing = len(p.report.Missing)
}

// missingEntryPoints lists the named renderers the sweep did not find, each as
// the package and name it was expected under.
func missingEntryPoints(prog *program) []string {
	var missing []string
	for _, dir := range sortedKeys(markdownEntryPoints) {
		pkg, loaded := prog.byDir[dir]
		if !loaded {
			continue
		}
		for _, name := range markdownEntryPoints[dir] {
			if _, ok := pkg.funcs[name]; !ok {
				missing = append(missing, dir+"."+name)
			}
		}
	}
	return missing
}

// sortedKeys lists a map's keys in a fixed order, so two runs over the same
// tree produce the same report.
func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// sortFindings orders findings by where they are, so a report reads in the
// order a person would open the files.
func sortFindings(findings []Finding) {
	sort.Slice(findings, func(i, j int) bool {
		return findingLess(findings[i], findings[j])
	})
}

// findingLess orders two findings by file, then line, then expression.
func findingLess(a, b Finding) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Expression < b.Expression
}

// staleLess orders two unused exemptions by where they were declared.
func staleLess(a, b Directive) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	return a.Line < b.Line
}

// failing reports whether this run found something a gate must refuse, and
// counts the unresolved values that count as failures under the -fail
// settings.
func failing(report Report, failUnresolved bool, failIn []string) int {
	count := len(report.Findings) + len(report.Stale) + len(report.Missing)
	for _, finding := range report.Unresolved {
		if failUnresolved || matchesPrefix(finding.Package, failIn) {
			count++
		}
	}
	return count
}

// matchesPrefix reports whether a package is covered by one of the
// repository-relative prefixes a stricter rule was asked for.
func matchesPrefix(pkg string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if prefix != "" && strings.HasPrefix(pkg, prefix) {
			return true
		}
	}
	return false
}
