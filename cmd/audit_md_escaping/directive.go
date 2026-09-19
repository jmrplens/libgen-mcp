package main

import (
	"go/token"
	"strings"
)

// exemptionDirective is how a value that needs no escaping is declared: in the
// source, in the package that owns the formatter, never in a list this audit
// carries.
//
//	//libgen:allow-unescaped <expression>: <reason>
//
// The expression is the one a finding prints, so an exemption is written by
// copying the value the report named. It excuses that expression wherever the
// package interpolates it, which is what a value repeated across several
// formatters needs, and a directive that excuses nothing is itself reported,
// so one left behind by a later change cannot quietly widen the gate.
//
// Escaping a value that needs no escaping is not free, which is why this
// exists at all rather than a blanket instruction to wrap everything: a
// constant this server compiled in is not catalog text, and wrapping it
// teaches the next reader a rule that is not the rule.
const exemptionDirective = "//libgen:allow-unescaped"

// Directive is one declared exemption, kept with where it was declared so a
// stale one can be pointed at.
type Directive struct {
	Package    string `json:"package"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Expression string `json:"expression"`
	Reason     string `json:"reason"`
}

// directiveKey identifies the findings one directive excuses: an expression,
// in the package that writes it.
type directiveKey struct {
	pkg        string
	expression string
}

// collectDirectives reads every exemption declared in the swept packages.
func collectDirectives(prog *program, root string) map[directiveKey]Directive {
	found := map[directiveKey]Directive{}
	for _, pkg := range prog.order {
		for _, file := range pkg.files {
			for _, group := range file.Comments {
				for _, comment := range group.List {
					collectOne(prog, pkg, root, comment.Text, comment.Pos(), found)
				}
			}
		}
	}
	return found
}

// collectOne records one comment, when it is an exemption.
func collectOne(prog *program, pkg *auditPkg, root, text string, pos token.Pos, found map[directiveKey]Directive) {
	expression, reason, ok := parseDirective(text)
	if !ok {
		return
	}
	at := prog.position(pos)
	key := directiveKey{pkg: pkg.dir, expression: expression}
	found[key] = Directive{
		Package:    pkg.dir,
		File:       relativePath(at.Filename, root),
		Line:       at.Line,
		Expression: expression,
		Reason:     reason,
	}
}

// parseDirective splits one comment into the expression it excuses and the
// reason given for it.
//
// Both halves are required. A directive with no reason is not read as an
// exemption at all, because the reason is the whole value of the mechanism:
// the next reader has to be able to tell a value that needs no escaping from
// one somebody decided not to escape.
func parseDirective(text string) (expression, reason string, ok bool) {
	rest, isDirective := strings.CutPrefix(strings.TrimSpace(text), exemptionDirective)
	if !isDirective {
		return "", "", false
	}
	excused, why, hasReason := strings.Cut(strings.TrimSpace(rest), ":")
	excused = strings.TrimSpace(excused)
	why = strings.TrimSpace(why)
	if excused == "" || !hasReason || why == "" {
		return "", "", false
	}
	return excused, why, true
}
