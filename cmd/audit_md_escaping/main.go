package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// toolName prefixes every line this command writes about itself, so a failure
// in a suite of gates names the gate that failed.
const toolName = "audit_md_escaping"

// exit is os.Exit behind a variable, so the one line main carries is reachable
// from a test rather than only from a process.
var exit = os.Exit

// absRoot is filepath.Abs behind a variable for the same reason: it fails only
// when the working directory has been removed under the process, and a gate's
// error path is worth a test even when reaching it for real takes a deleted
// directory.
var absRoot = filepath.Abs

// auditRun is one configured run: where to look, what to judge, and what to
// write.
type auditRun struct {
	dir      string
	jsonPath string
	contexts string
	check    bool
	verbose  bool
	// failUnresolved counts every value the audit cannot follow as a failure.
	failUnresolved bool
	// failUnresolvedIn names the packages, by repository-relative prefix,
	// whose unresolved values fail the gate while the rest stay reported. It
	// is how the tree is held to the stricter rule one package at a time:
	// internal/toolutil first, since a blind spot there is a blind spot behind
	// every formatter that calls it.
	failUnresolvedIn string
	dirs             []string
}

func main() {
	exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses the command line and performs the sweep.
//
// It returns 0 when the run is clean, 1 when -check found something, and 2
// when the audit itself could not do its job, which is the split its sibling
// audits use: a gate that cannot run must not read as a gate that passed.
func run(args []string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet(toolName, flag.ContinueOnError)
	flags.SetOutput(errOut)
	cfg := auditRun{dirs: auditDirs}
	flags.StringVar(&cfg.dir, "dir", ".", "repository root to audit")
	flags.StringVar(&cfg.jsonPath, "json", "", "write the JSON work list to this path")
	flags.StringVar(&cfg.contexts, "contexts", allContexts,
		"Markdown contexts to judge: "+allContexts+", or a comma-separated list of "+contextNames())
	flags.BoolVar(&cfg.check, "check", false, "exit non-zero when a value still reaches a Markdown construct unescaped")
	flags.BoolVar(&cfg.verbose, "v", false, "list the excused and unresolved values as well as the failing ones")
	flags.BoolVar(&cfg.failUnresolved, "fail-unresolved", false, "count a value the audit cannot follow as a failure")
	flags.StringVar(&cfg.failUnresolvedIn, "fail-unresolved-in", "",
		"count a value the audit cannot follow as a failure in these packages only: a comma-separated list of "+
			"repository-relative prefixes, such as "+toolutilDir)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if dirs := flags.Args(); len(dirs) > 0 {
		cfg.dirs = dirs
	}
	return execute(cfg, out, errOut)
}

// execute performs one configured run, so a test can drive the whole audit
// without going through a command line.
func execute(cfg auditRun, out, errOut io.Writer) int {
	sel, err := parseContexts(cfg.contexts)
	if err != nil {
		return fail(errOut, err)
	}
	root, err := absRoot(cfg.dir)
	if err != nil {
		return fail(errOut, err)
	}
	prog, err := loadProgram(root, cfg.dirs)
	if err != nil {
		return fail(errOut, err)
	}

	report := audit(prog, sel, root)
	writeReport(out, report, cfg.verbose)
	if cfg.jsonPath != "" {
		if writeErr := writeJSON(cfg.jsonPath, report); writeErr != nil {
			return fail(errOut, writeErr)
		}
	}
	if !cfg.check {
		return 0
	}
	if count := failing(report, cfg.failUnresolved, splitPrefixes(cfg.failUnresolvedIn)); count > 0 {
		fmt.Fprintf(errOut, "%s: %s\n", toolName, summarize(report))
		return 1
	}
	return 0
}

// fail reports an error the audit could not work around and returns the exit
// status that says the gate did not run.
func fail(errOut io.Writer, err error) int {
	fmt.Fprintf(errOut, "%s: %v\n", toolName, err)
	return 2
}

// splitPrefixes reads the -fail-unresolved-in list.
func splitPrefixes(value string) []string {
	var prefixes []string
	for prefix := range strings.SplitSeq(value, ",") {
		if prefix = strings.TrimSpace(prefix); prefix != "" {
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes
}
