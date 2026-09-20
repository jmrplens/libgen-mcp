// Command godoc_tool is the consolidated Go documentation auditor and fixer.
// It combines the read-only audit (formerly audit_godocs — reports missing or
// malformed doc comments) and the in-place fixer (formerly add_docs — generates
// and inserts godoc-compliant comments), and moves a package comment into the
// one file the convention keeps it in.
//
// Usage:
//
//	godoc_tool audit [--format markdown|json] [--output path] [--fail-on-findings]
//	godoc_tool fix [--dry-run] <paths...>
//	godoc_tool move-package-doc [--dry-run] <paths...>
//
// The audit subcommand's flags match the former audit_godocs binary exactly.
// The fix subcommand gains a --dry-run flag (prints what would change without
// writing files) that the former add_docs binary lacked.
//
// # Where a package comment lives
//
// In doc.go, and the audit reports every other file that carries one. Go
// attaches the package comment above any file's package clause, so a comment
// in some other file is one edit away from being joined by a second — and two
// package comments are not an error, they are two package comments, with
// whichever the toolchain reads first becoming the package's documentation.
//
// move-package-doc is what moves an existing one. It copies the comment
// verbatim above a new doc.go's package clause and cuts it from the file it
// came from, and it carries the build constraint with it rather than leaving
// it behind: a constraint governs the file it is in, and doc.go is a new file
// of the same package.
//
// It declines three shapes on purpose. A package that already has a doc.go is
// left alone. A package with no comment has nothing to move. And a comment the
// convention refuses is left where it is, because moving it would enshrine as
// the package's documentation a comment the audit already reports.
package main
