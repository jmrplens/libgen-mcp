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

// rawDirective is the same declaration for the second verdict:
//
//	//libgen:allow-raw <expression>: <reason>
//
// It is a directive of its own rather than a second meaning of the first, so
// that a value excused for escaping is not thereby excused for shape. The two
// verdicts ask different questions — what the value can do to the line, and
// what the line is — and each is answered on its own.
const rawDirective = "//libgen:allow-raw"

// directiveKind names which verdict a directive answers.
type directiveKind string

const (
	// kindUnescaped excuses the escaping verdict.
	kindUnescaped directiveKind = "unescaped"
	// kindRaw excuses the shape verdict, which today is the card row.
	kindRaw directiveKind = "raw"
)

// directivePrefixes maps each directive's spelling to the verdict it answers.
var directivePrefixes = map[string]directiveKind{
	exemptionDirective: kindUnescaped,
	rawDirective:       kindRaw,
}

// kindFor is the directive a context's findings are excused by.
func kindFor(ctx mdContext) directiveKind {
	if ctx.shape() {
		return kindRaw
	}
	return kindUnescaped
}

// Directive is one declared exemption, kept with where it was declared so a
// stale one can be pointed at.
type Directive struct {
	Package    string        `json:"package"`
	File       string        `json:"file"`
	Line       int           `json:"line"`
	Kind       directiveKind `json:"kind"`
	Expression string        `json:"expression"`
	Reason     string        `json:"reason"`
}

// directiveKey identifies the findings one directive excuses: an expression,
// in the package that writes it, for one verdict.
type directiveKey struct {
	pkg        string
	kind       directiveKind
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
	kind, expression, reason, ok := parseDirective(text)
	if !ok {
		return
	}
	at := prog.position(pos)
	key := directiveKey{pkg: pkg.dir, kind: kind, expression: expression}
	found[key] = Directive{
		Package:    pkg.dir,
		File:       relativePath(at.Filename, root),
		Line:       at.Line,
		Kind:       kind,
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
func parseDirective(text string) (kind directiveKind, expression, reason string, ok bool) {
	text = strings.TrimSpace(text)
	for prefix, candidate := range directivePrefixes {
		rest, isDirective := strings.CutPrefix(text, prefix)
		if !isDirective {
			continue
		}
		excused, why, hasReason := strings.Cut(strings.TrimSpace(rest), ":")
		excused = strings.TrimSpace(excused)
		why = strings.TrimSpace(why)
		if excused == "" || !hasReason || why == "" {
			return "", "", "", false
		}
		return candidate, excused, why, true
	}
	return "", "", "", false
}
