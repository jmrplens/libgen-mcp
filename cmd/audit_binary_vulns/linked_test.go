// linked_test.go holds the not-linked check to the build graph: a real fixture
// module listed by the real go command, and a stand-in for the go command
// where the case is about the bookkeeping rather than the graph.

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// TestRun_NotLinkedIsCheckedAgainstTheBuildGraph drives the whole gate over
// the fixture module with the standard library advisory declared not-linked:
// the declaration passes when the package it names is not linked, fails when
// it is, and fails when the advisory names no package at all.
func TestRun_NotLinkedIsCheckedAgainstTheBuildGraph(t *testing.T) {
	t.Parallel()

	dir, config := fixtureRelease(t)
	declared := map[string]declaration{declarationKey(everyStdlib.id, "stdlib"): {category: categoryNotLinked, reason: "fixture"}}
	for _, tc := range []struct {
		name    string
		imports []string
		code    int
		want    string
	}{
		{name: "not linked", imports: []string{"net/smtp"}, want: "verified not-linked " + everyStdlib.id + " stdlib: none of the 1 packages the advisory names (net/smtp)"},
		{name: "linked", imports: []string{"net/smtp", "runtime"}, code: 1, want: "LINKED " + everyStdlib.id + " stdlib: runtime is linked on " + hostTarget.String()},
		{name: "whole module", imports: []string{}, code: 1, want: "UNCHECKED " + everyStdlib.id + " stdlib: the advisory names no packages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			advisory := everyStdlib
			advisory.imports = tc.imports
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), auditConfig{dir: dir, config: config, db: writeVulnDB(t, advisory), declared: declared}, &stdout, &stderr)
			if code != tc.code || !strings.Contains(stdout.String(), tc.want) {
				t.Fatalf("exit %d, want %d and %q\nstdout:\n%s\nstderr:\n%s", code, tc.code, tc.want, stdout.String(), stderr.String())
			}
		})
	}
}

// TestCheckNotLinked_EveryTargetIsListedOnce covers the bookkeeping with the
// go command stood in for: every target of every build is listed, once however
// many declarations ask, only not-linked declarations are checked, and a
// listing that fails stops the run.
func TestCheckNotLinked_EveryTargetIsListedOnce(t *testing.T) {
	previous := listDeps
	t.Cleanup(func() { listDeps = previous })

	builds := []build{{id: "server", main: "./cmd/server", targets: []target{linuxAMD64, windowsARM64}}}
	notLinked := &declaration{category: categoryNotLinked, reason: "x"}
	adoptable := &declaration{category: categoryFixNotYetAdoptable, reason: "y"}
	rep := report{findings: []judged{
		{osv: "GO-2099-0010", module: "example.com/a", imports: []string{"example.com/a/bad"}, declaration: notLinked},
		{osv: "GO-2099-0011", module: "example.com/b", imports: []string{"example.com/b/bad"}, declaration: notLinked},
		{osv: "GO-2099-0012", module: "example.com/c", imports: []string{"example.com/c/bad"}, declaration: adoptable},
		{osv: "GO-2099-0013", module: "example.com/d"},
	}}
	var listed []string
	listDeps = func(_ context.Context, _ string, b build, tg target) ([]string, error) {
		listed = append(listed, b.id+" "+tg.String())
		if tg == windowsARM64 {
			return []string{"example.com/b/bad"}, nil
		}
		return []string{"example.com/a/good"}, nil
	}
	if err := checkNotLinked(context.Background(), "", builds, &rep); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Errorf("listed %v, want each of the two targets once", listed)
	}
	if len(rep.verified) != 1 || !strings.Contains(rep.verified[0], "GO-2099-0010 example.com/a") || !strings.Contains(rep.verified[0], "2 targets") {
		t.Errorf("verified = %v", rep.verified)
	}
	if len(rep.linked) != 1 || rep.linked[0] != "GO-2099-0011 example.com/b: example.com/b/bad is linked on windows/arm64" {
		t.Errorf("linked = %v", rep.linked)
	}

	listDeps = func(context.Context, string, build, target) ([]string, error) { return nil, errors.New("no go") }
	if err := checkNotLinked(context.Background(), "", builds, &report{findings: rep.findings[:1]}); err == nil {
		t.Error("a listing that failed did not stop the run")
	}
}

// TestListDeps_AnUnbuildableMainFails runs the real go command on a package
// that does not exist, which must be an error naming the target rather than an
// empty list read as "nothing is linked".
func TestListDeps_AnUnbuildableMainFails(t *testing.T) {
	t.Parallel()

	_, err := listDeps(context.Background(), writeFixtureModule(t), build{main: "./absent"}, hostTarget)
	if err == nil || !strings.Contains(err.Error(), hostTarget.String()) {
		t.Errorf("err = %v, want a failure naming %s", err, hostTarget)
	}
}
