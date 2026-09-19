// Command audit_md_escaping finds catalog text that reaches a Markdown
// construct without an escaper between it and the page.
//
// Every tool result and every prompt message this server produces is Markdown
// built from third-party metadata: a title somebody uploaded, an author a
// catalog transcribed, a mirror's URL. A value that carries a pipe ends the
// table cell it sits in, one that carries a newline ends the row, one that
// opens with '#' becomes a heading of its own, and one that carries a
// backtick run can close the fence it was put inside. The rule this repository
// states in CLAUDE.md § Escaping untrusted content is that such a value goes
// through internal/toolutil; this command is what checks that it did.
//
// It reads the source of internal/tools, internal/prompts and
// internal/toolutil, walks each function that writes Markdown in the order it
// writes it, and for every runtime value asks two questions: which Markdown
// construct the value lands in, given everything written before it, and
// whether an escaper stands between the value and that construct. A value
// landing in prose is not judged — a paragraph holds a pipe and an angle
// bracket without the document changing shape.
//
// The walk is over syntax, not types: this module does not depend on
// golang.org/x/tools, and the three sibling audits read the tree the same way.
// What that costs is named where it is paid — a value is judged by the verb
// that prints it and by the names in the call chain, and a chain the walk
// cannot follow is reported as unresolved rather than assumed safe.
//
// A value that needs no escaping is declared in the source, beside the
// formatter that writes it:
//
//	//libgen:allow-unescaped <expression>: <reason>
//
// The expression is the one the report prints, so an exemption is written by
// copying what the finding named. Both halves are required: a declaration with
// no reason is not read as one at all, because the reason is what lets the next
// reader tell a value that needs no escaping from one somebody decided not to
// escape. A declaration that excuses nothing is itself reported, so one left
// behind by a later change cannot quietly widen the gate.
//
// Usage:
//
//	go run ./cmd/audit_md_escaping [-json out.json] [-check] [-v]
//	  [-contexts all] [-fail-unresolved-in internal/toolutil] [dirs...]
//
// It exits 0 when the run is clean, 1 when -check found something, and 2 when
// the audit could not do its job: a gate that cannot run must not read as a
// gate that passed.
package main
