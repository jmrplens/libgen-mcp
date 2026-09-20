package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDispatch_ExitStatusSaysWhichOfTheThreeHappened pins the split: 0 when a
// subcommand did its job, 1 when it reported something it could describe, and
// 2 when the command line itself was wrong.
//
// This command had no test of its own until now, which is the kind of gap a
// tool that audits documentation should not be the one to have.
func TestDispatch_ExitStatusSaysWhichOfTheThreeHappened(t *testing.T) {
	clean := writeFiles(t, map[string]string{
		"doc.go":    "// Package widget does a thing.\npackage widget\n",
		"widget.go": "package widget\n\n// Do does it.\nfunc Do() {}\n",
	})

	testCases := []struct {
		name string
		args []string
		want int
	}{
		{name: "no subcommand", args: nil, want: 2},
		{name: "an unknown subcommand", args: []string{"nonsense"}, want: 2},
		{name: "fix with no path", args: []string{"fix"}, want: 2},
		{name: "move-package-doc with no path", args: []string{"move-package-doc"}, want: 2},
		{name: "move-package-doc with an unknown flag", args: []string{"move-package-doc", "-nonsense", "."}, want: 2},
		{name: "move-package-doc on a path that is not there", args: []string{"move-package-doc", filepath.Join(clean, "absent")}, want: 1},
		{name: "move-package-doc on a package that has one", args: []string{"move-package-doc", clean}, want: 0},
		{name: "fix on a clean package", args: []string{"fix", clean}, want: 0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if got := dispatch(tc.args, &out, &errOut); got != tc.want {
				t.Errorf("dispatch(%v) = %d, want %d\nstdout: %s\nstderr: %s",
					tc.args, got, tc.want, out.String(), errOut.String())
			}
		})
	}
}

// TestDispatch_UsageNamesEverySubcommand verifies a run with no arguments says
// what there is to run, which is the only thing a reader of that message wants.
func TestDispatch_UsageNamesEverySubcommand(t *testing.T) {
	var out, errOut bytes.Buffer
	dispatch(nil, &out, &errOut)

	for _, want := range []string{"audit", "fix", "move-package-doc"} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(errOut.String(), want) {
				t.Errorf("usage = %q, want it to name %q", errOut.String(), want)
			}
		})
	}
}

// TestDispatch_AuditReportsAFindingAsStatusOne verifies the audit's own
// failure reaches the caller as a refusal rather than as a clean exit.
func TestDispatch_AuditReportsAFindingAsStatusOne(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"go.mod":    "module example.com/widget\n\ngo 1.27\n",
		"widget.go": "package widget\n\n// Do does it.\nfunc Do() {}\n",
	})

	var out, errOut bytes.Buffer
	status := dispatch([]string{"audit", "--fail-on-findings", "--root", dir}, &out, &errOut)
	if status != 1 {
		t.Fatalf("dispatch(audit) = %d, want 1: a package with no package comment is a finding\nstdout: %s\nstderr: %s",
			status, out.String(), errOut.String())
	}
}

// TestDispatch_MovesAPackageCommentEndToEnd drives the subcommand the way the
// command line does, so the wiring between the flag set and the mover is
// exercised rather than assumed.
func TestDispatch_MovesAPackageCommentEndToEnd(t *testing.T) {
	dir := writeFiles(t, map[string]string{"widget.go": "// Package widget does a thing.\npackage widget\n"})

	var out, errOut bytes.Buffer
	if status := dispatch([]string{"move-package-doc", dir}, &out, &errOut); status != 0 {
		t.Fatalf("dispatch(move-package-doc) = %d, want 0: %s", status, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "doc.go")); err != nil {
		t.Errorf("doc.go was not written: %v", err)
	}
}

// TestDispatch_DryRunIsNotRememberedByTheNextRun pins the one hazard of a
// package-level flag: it is shared by both writing subcommands, so a run that
// set it must not leave the next one printing instead of writing.
func TestDispatch_DryRunIsNotRememberedByTheNextRun(t *testing.T) {
	first := writeFiles(t, map[string]string{"widget.go": "// Package widget does a thing.\npackage widget\n"})
	second := writeFiles(t, map[string]string{"gadget.go": "// Package gadget does another.\npackage gadget\n"})

	var out, errOut bytes.Buffer
	dispatch([]string{"move-package-doc", "-dry-run", first}, &out, &errOut)
	if _, err := os.Stat(filepath.Join(first, "doc.go")); err == nil {
		t.Fatal("the dry run wrote doc.go")
	}

	dispatch([]string{"move-package-doc", second}, &out, &errOut)
	if _, err := os.Stat(filepath.Join(second, "doc.go")); err != nil {
		t.Errorf("the second run did not write doc.go, so -dry-run was remembered: %v", err)
	}
}
