package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// dryRun controls whether a writing subcommand changes files (false) or only
// prints what it would do (true). Set by each one's --dry-run flag.
var dryRun bool

// exit is os.Exit behind a variable, so the one line main carries is reachable
// from a test rather than only from a process.
var exit = os.Exit

func main() {
	exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}

// dispatch runs one subcommand and returns the status to exit with: 0 when it
// did its job, 1 when it reported a finding or a failure it could describe,
// and 2 when the command line itself was wrong.
//
// It takes its writers and its arguments rather than reading the process, so
// those three statuses are something a test can observe. They were not before:
// this command had no test of its own, which is the kind of gap a tool that
// audits documentation should not be the one to have.
func dispatch(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "usage: godoc_tool <audit|fix|move-package-doc> [options]")
		fmt.Fprintln(errOut, "  audit             report missing or malformed Go doc comments")
		fmt.Fprintln(errOut, "  fix               generate and insert godoc-compliant comments")
		fmt.Fprintln(errOut, "  move-package-doc  move each package comment into a doc.go of its own")
		return 2
	}

	switch args[0] {
	case "audit":
		if err := run(args[1:], out); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		return 0
	case "fix":
		return overPaths("fix", "print what would change without writing files", args[1:], errOut, processPath)
	// A subcommand rather than a flag on fix: it does not fix anything, it
	// moves a comment that is already correct, and a flag that replaces what a
	// command does is a second command wearing the first one's name.
	case "move-package-doc":
		return overPaths("move-package-doc", "print what would move without writing files", args[1:], errOut, movePackageDocs)
	default:
		fmt.Fprintf(errOut, "unknown subcommand %q (valid: audit, fix, move-package-doc)\n", args[0])
		return 2
	}
}

// overPaths is the shape both writing subcommands have: a --dry-run flag, at
// least one path, and one action per path that reports its own failure and
// lets the rest run.
//
// Every path is attempted even after one fails, because they are independent:
// a tree where one package cannot be handled is still a tree where the others
// should be, and stopping early would make a person run the command once per
// package.
func overPaths(name, dryRunUsage string, args []string, errOut io.Writer, action func(string) error) int {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(errOut)
	flags.BoolVar(&dryRun, "dry-run", false, dryRunUsage)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() == 0 {
		fmt.Fprintf(errOut, "%s: at least one file or directory path is required\n", name)
		return 2
	}
	failed := false
	for _, path := range flags.Args() {
		if err := action(path); err != nil {
			fmt.Fprintln(errOut, err)
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}
