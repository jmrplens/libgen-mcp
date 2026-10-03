// main_test.go drives the command end to end: the release configuration read,
// every target built or found, every binary scanned against a database the
// test wrote, and the exit code the gate turns on.

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureRelease writes a module and a GoReleaser configuration that builds
// it for the host, and returns the module directory and the configuration.
func fixtureRelease(t *testing.T) (dir, config string) {
	t.Helper()

	dir = writeFixtureModule(t)
	config = writeConfig(t, "builds:\n  - id: fixture\n    main: .\n    env: [CGO_ENABLED=0]\n    flags: [-trimpath]\n    goos: ["+
		hostTarget.goos+"]\n    goarch: ["+hostTarget.goarch+"]\n")
	return dir, config
}

// TestRunMain_AnUndeclaredAdvisoryFailsTheGate is the gate doing its job: a
// binary linking a module an advisory names fails the run, and the report
// says which advisory, which module and which target.
func TestRunMain_AnUndeclaredAdvisoryFailsTheGate(t *testing.T) {
	t.Parallel()

	dir, config := fixtureRelease(t)
	var stdout, stderr bytes.Buffer
	code := runMain(context.Background(), []string{"-dir", dir, "-config", config, "-db", writeVulnDB(t, everyStdlib, neverLinked)}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	want := "FINDING " + everyStdlib.id + " stdlib@"
	if !strings.Contains(stdout.String(), want) || !strings.Contains(stdout.String(), "in "+hostTarget.String()+": "+everyStdlib.summary) {
		t.Errorf("the report lacks the finding %q for %s:\n%s", want, hostTarget, stdout.String())
	}
	if strings.Contains(stdout.String(), neverLinked.id) {
		t.Errorf("the report names an advisory against a module the binary does not link:\n%s", stdout.String())
	}
}

// TestRun_ADeclaredAdvisoryPasses covers the other side: the same finding,
// accepted by a declaration, passes, a database with nothing that applies
// passes with the clean verdict, and a declaration that no finding needs fails
// the run although nothing is undeclared.
func TestRun_ADeclaredAdvisoryPasses(t *testing.T) {
	t.Parallel()

	dir, config := fixtureRelease(t)
	accepted := map[string]declaration{declarationKey(everyStdlib.id, "stdlib"): {category: categoryFixNotYetAdoptable, reason: "fixture"}}
	for _, tc := range []struct {
		name     string
		db       []fixtureAdvisory
		declared map[string]declaration
		code     int
		want     string
	}{
		{
			name:     "declared",
			db:       []fixtureAdvisory{everyStdlib},
			declared: accepted,
			want:     "1 findings in the 1 binaries, each accepted by a declaration",
		},
		{
			name: "clean",
			db:   []fixtureAdvisory{neverLinked},
			want: "no advisory affects a module the 1 binaries link",
		},
		{
			name:     "stale",
			db:       []fixtureAdvisory{neverLinked},
			declared: accepted,
			code:     1,
			want:     `STALE declaration "` + everyStdlib.id + ` stdlib"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			code := run(context.Background(), auditConfig{dir: dir, config: config, db: writeVulnDB(t, tc.db...), declared: tc.declared}, &stdout, &stderr)
			if code != tc.code || !strings.Contains(stdout.String(), tc.want) {
				t.Fatalf("exit %d, want %d and %q\nstdout:\n%s\nstderr:\n%s", code, tc.code, tc.want, stdout.String(), stderr.String())
			}
		})
	}
}

// TestRun_ScansPrebuiltBinariesAgainstTheSameTable covers -binaries: the
// binaries somebody else built are scanned in place, named by the platform
// they record, and held to the same table.
func TestRun_ScansPrebuiltBinariesAgainstTheSameTable(t *testing.T) {
	t.Parallel()

	_, config := fixtureRelease(t)
	bin := buildFixture(t)
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), auditConfig{config: config, binaries: bin, db: writeVulnDB(t, everyStdlib)}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if want := "FINDING " + everyStdlib.id + " stdlib@"; !strings.Contains(stdout.String(), want) {
		t.Errorf("the report lacks %q:\n%s", want, stdout.String())
	}
	if want := "fixture " + hostTarget.String() + " built with"; !strings.Contains(stdout.String(), want) {
		t.Errorf("the report does not name the binary by its build and recorded target %q:\n%s", want, stdout.String())
	}
}

// TestRun_ARunThatCannotBeMade_FailsOnStderr covers each step failing: the
// configuration, the build, finding the prebuilt binaries and the scan. A gate
// that could not check the release must not read as one that passed.
func TestRun_ARunThatCannotBeMade_FailsOnStderr(t *testing.T) {
	t.Parallel()

	dir, config := fixtureRelease(t)
	for _, tc := range []struct {
		name string
		cfg  auditConfig
		want string
	}{
		{name: "configuration", cfg: auditConfig{dir: dir, config: filepath.Join(dir, "absent.yml")}, want: "absent.yml"},
		{name: "build", cfg: auditConfig{dir: t.TempDir(), config: config}, want: "building fixture"},
		{name: "prebuilt", cfg: auditConfig{config: config, binaries: filepath.Join(dir, "absent-*")}, want: "matches no file"},
		{name: "scan", cfg: auditConfig{dir: dir, config: config, db: "file://" + filepath.ToSlash(filepath.Join(t.TempDir(), "absent"))}, want: "govulncheck on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), tc.cfg, &stdout, &stderr); code != 2 {
				t.Fatalf("exit %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), tc.want) || stdout.Len() != 0 {
				t.Errorf("stderr %q lacks %q, or stdout carried a report: %q", stderr.String(), tc.want, stdout.String())
			}
		})
	}
}

// TestRun_NoDirectoryForTheBinaries_FailsOnStderr covers the temporary
// directory the binaries are built into not being creatable, which is set up
// by pointing every variable the platforms read it from at nothing.
func TestRun_NoDirectoryForTheBinaries_FailsOnStderr(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent")
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, absent)
	}

	dir, config := fixtureRelease(t)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), auditConfig{dir: dir, config: config}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), absent) {
		t.Errorf("stderr %q does not name the directory it could not create in", stderr.String())
	}
}

// TestRunMain_ArgumentsItCannotRead covers the command line: -h is not an
// error, and an unknown flag or a positional argument is a usage error rather
// than a run.
func TestRunMain_ArgumentsItCannotRead(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{name: "help", args: []string{"-h"}, want: 0},
		{name: "unknown flag", args: []string{"-nope"}, want: 2},
		{name: "positional argument", args: []string{"./cmd/server"}, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			if code := runMain(context.Background(), tc.args, &stdout, &stderr); code != tc.want {
				t.Errorf("runMain(%q) = %d, want %d", tc.args, code, tc.want)
			}
		})
	}
}

// TestMain_HandsTheExitCodeToTheProcess covers main itself through the exit
// seam, with a configuration that does not exist so it stops before building.
func TestMain_HandsTheExitCodeToTheProcess(t *testing.T) {
	previousArgs, previousExit := os.Args, exitProcess
	t.Cleanup(func() { os.Args, exitProcess = previousArgs, previousExit })

	os.Args = []string{toolName, "-config", filepath.Join(t.TempDir(), "absent.yml")}
	code := -1
	exitProcess = func(c int) { code = c }
	main()
	if code != 2 {
		t.Fatalf("main exited %d, want 2 for a configuration that does not exist", code)
	}
}
