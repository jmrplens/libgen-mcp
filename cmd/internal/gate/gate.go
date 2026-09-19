// Package gate holds the command line every gate in this repository takes,
// and the exit statuses they answer with.
//
// A gate here reports in one of three ways, and the split is the same one
// every time: 0 when the run is clean, 1 when -check found something, and 2
// when the command could not do its job at all. The third is the one worth
// having a package for — a gate that cannot run must not read as a gate that
// passed, and that is a rule about every command rather than about any one of
// them.
//
// What is shared is the plumbing, not the decision: [Parse] reads the flags
// and says whether they were accepted, and each command writes its own
// statuses at its own call site, where a reader can see them.
package gate

import (
	"errors"
	"flag"
	"io"
)

// Flags are the two every gate takes: where to look, and whether to refuse.
type Flags struct {
	// Dir is the repository root to read.
	Dir string
	// Check is whether the command exits non-zero on a finding instead of
	// printing a report.
	Check bool
}

// Parse reads the shared command line.
//
// checkUsage is the one sentence that describes what -check refuses, which is
// different for each gate and is the part a reader of `-h` needs. register, if
// given, adds the flags a command has of its own before parsing.
//
// The error is [flag.ErrHelp] when usage was asked for, which is a clean exit
// rather than a failure, and any other error means the command line was not
// accepted. Both are returned rather than acted on: the exit status belongs at
// the call site, where the three of them read together.
func Parse(name string, args []string, errOut io.Writer, checkUsage string, register func(*flag.FlagSet)) (Flags, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(errOut)
	var parsed Flags
	flags.StringVar(&parsed.Dir, "dir", ".", "repository root to read")
	flags.BoolVar(&parsed.Check, "check", false, checkUsage)
	if register != nil {
		register(flags)
	}
	if err := flags.Parse(args); err != nil {
		return Flags{}, err
	}
	return parsed, nil
}

// Helped reports whether an error from [Parse] is the request for usage, which
// every gate answers with a clean exit.
func Helped(err error) bool {
	return errors.Is(err, flag.ErrHelp)
}
