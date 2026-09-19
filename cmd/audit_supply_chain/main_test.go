package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

// contains reports whether any finding mentions want, so a case can assert what
// was found without pinning the whole sentence.
func contains(findings []string, want string) bool {
	for _, f := range findings {
		if strings.Contains(f, want) {
			return true
		}
	}
	return false
}

// TestAPinIsTheSHAAndTheCommentTogether covers the rule's two halves. The
// second is the one that looks optional and is not: an action pinned with no
// version comment is pinned *and frozen*, because Dependabot has nothing to
// read and will never propose the bump.
func TestAPinIsTheSHAAndTheCommentTogether(t *testing.T) {
	const text = `jobs:
  a:
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e
      - uses: actions/cache@55cc8345863c7cc4c66a329aec7e433d2d1c52a9 # v6
      - uses: ./.github/workflows/race.yml
`
	findings := checkPinnedUses("w.yml", text)
	if !contains(findings, "actions/checkout@v7 is not pinned") {
		t.Errorf("a floating tag was accepted: %v", findings)
	}
	if !contains(findings, "carries no version comment") {
		t.Errorf("a SHA with no version comment was accepted: %v", findings)
	}
	for _, unwanted := range []string{"actions/cache", "race.yml"} {
		t.Run(unwanted, func(t *testing.T) {
			if contains(findings, unwanted) {
				t.Errorf("%s should pass: %v", unwanted, findings)
			}
		})
	}
	if len(findings) != 2 {
		t.Errorf("want exactly 2 findings, got %d: %v", len(findings), findings)
	}
}

// TestACredentialedJobIsOneThatInheritedThePermissionToo is the half a
// job-level reading misses: this repository grants contents/id-token at the
// *workflow* level, so every job that declares no permissions of its own holds
// them. A check that only read job["permissions"] would audit none of them.
func TestACredentialedJobIsOneThatInheritedThePermissionToo(t *testing.T) {
	doc := map[string]any{"permissions": map[string]any{"contents": "write", "id-token": "write"}}

	if !jobIsCredentialed(map[string]any{}, doc) {
		t.Error("a job declaring no permissions inherits the workflow's and is credentialed")
	}
	readOnly := map[string]any{"permissions": map[string]any{"contents": "read"}}
	if jobIsCredentialed(readOnly, doc) {
		t.Error("a job that narrows itself to contents: read is not credentialed")
	}
	if !jobIsCredentialed(map[string]any{"permissions": "write-all"}, map[string]any{}) {
		t.Error("write-all is every permission at once")
	}
	if jobIsCredentialed(map[string]any{"permissions": map[string]any{}}, doc) {
		t.Error("permissions: {} grants nothing, whatever the workflow declares")
	}
}

// TestEveryWayToRunUnreviewedCodeIsCaught walks the four shapes, and the two
// lines that must NOT be flagged: a comment, and a pip install that pins its
// tree by hash.
func TestEveryWayToRunUnreviewedCodeIsCaught(t *testing.T) {
	const script = `npx --yes some-cli pack
go install example.com/tool@latest
curl -fsSL https://example.com/i.sh | sh
pip install twine
# npx --yes this-one-is-a-comment
pip install --require-hashes -r requirements.txt
`
	findings := runtimeResolvedCode("s", script)
	for _, want := range []string{"npx resolves", "@latest is whatever", "piped into a shell", "without --require-hashes"} {
		t.Run(want, func(t *testing.T) {
			if !contains(findings, want) {
				t.Errorf("missed %q: %v", want, findings)
			}
		})
	}
	if len(findings) != 4 {
		t.Errorf("want exactly 4 findings — the comment and the hashed install must pass — got %d: %v",
			len(findings), findings)
	}
}

// TestPinningTheActionDoesNotPinWhatItDownloads is the gap this rule exists
// for, and the one indirection it allows.
func TestPinningTheActionDoesNotPinWhatItDownloads(t *testing.T) {
	doc := map[string]any{"env": map[string]any{"GORELEASER_VERSION": "v2.18.2"}}

	cases := []struct {
		name string
		with map[string]any
		want string
	}{
		{"no version at all", map[string]any{}, "names no version"},
		{"latest", map[string]any{"version": "latest"}, `is "latest"`},
		{"a range", map[string]any{"version": "~> v2"}, `is "~> v2"`},
		{"an env that does not exist", map[string]any{"version": "${{ env.NOPE }}"}, "which the workflow does not define"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkStepAction("w", "goreleaser/goreleaser-action@"+strings.Repeat("a", 40), tc.with, doc)
			if !contains(got, tc.want) {
				t.Errorf("want a finding mentioning %q, got %v", tc.want, got)
			}
		})
	}

	for _, ok := range []map[string]any{
		{"version": "v2.18.2"},
		{"version": "${{ env.GORELEASER_VERSION }}"},
	} {
		t.Run(fmt.Sprint(ok["version"]), func(t *testing.T) {
			if got := checkStepAction("w", "goreleaser/goreleaser-action@x", ok, doc); len(got) != 0 {
				t.Errorf("%v should pass: %v", ok, got)
			}
		})
	}

	// An action that downloads nothing is not this rule's business.
	if got := checkStepAction("w", "actions/checkout@x", map[string]any{}, doc); len(got) != 0 {
		t.Errorf("actions/checkout is not a tool installer: %v", got)
	}
}

// TestTheCooldownMustBeStatedRatherThanInherited also pins the docker-only
// trap: Dependabot rejects a SemVer sub-key there outright, and a configuration
// file it refuses stops **every** ecosystem, not the one that carries it.
func TestTheCooldownMustBeStatedRatherThanInherited(t *testing.T) {
	const config = `version: 2
updates:
  - package-ecosystem: "gomod"
    directory: "/"
  - package-ecosystem: "npm"
    directory: "/site"
    cooldown:
      default-days: 1
  - package-ecosystem: "docker"
    directory: "/"
    cooldown:
      default-days: 3
      semver-major-days: 7
  - package-ecosystem: "github-actions"
    directory: "/"
    cooldown:
      default-days: 3
`
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(config), &doc); err != nil {
		t.Fatalf("fixture does not parse: %v", err)
	}
	findings := checkDependabot(doc)
	for _, want := range []string{"gomod (/) states no cooldown", "below the 3", "carries semver-major-days"} {
		t.Run(want, func(t *testing.T) {
			if !contains(findings, want) {
				t.Errorf("missed %q: %v", want, findings)
			}
		})
	}
	if contains(findings, "github-actions") {
		t.Errorf("a stated cooldown at the floor should pass: %v", findings)
	}
}

// TestTheSecurityTableCannotPromiseASupersededMajor is re-aimed from the
// source's rule: this project's table says "Latest release / Anything older"
// rather than listing majors, which is a stronger policy for a single static
// binary. What the rule holds is that the section exists and that no row
// promises fixes for a major the repository has left behind.
func TestTheSecurityTableCannotPromiseASupersededMajor(t *testing.T) {
	const current = "2.0.0\n"

	if got := checkSecurityPolicy(current, "# Security\n\nMail me.\n"); len(got) != 1 {
		t.Errorf("a policy with no supported-versions section must be a finding, got %v", got)
	}

	stale := "## Supported versions\n\n| Version | Supported |\n| --- | --- |\n| `1.x` | Yes |\n"
	if got := checkSecurityPolicy(current, stale); !contains(got, "marks 1.x supported while this repository ships 2.x") {
		t.Errorf("a superseded major marked supported must be a finding, got %v", got)
	}

	fine := "## Supported versions\n\n| Version | Supported |\n| --- | --- |\n| `2.x` | Yes |\n| `1.x` | No — upgrade |\n"
	if got := checkSecurityPolicy(current, fine); len(got) != 0 {
		t.Errorf("the current major supported and the old one not should pass: %v", got)
	}

	// The shape this repository actually ships: no major named at all.
	shipped := "## Supported versions\n\n| Version | Supported |\n| --- | --- |\n| Latest release | Yes |\n| Anything older | No — upgrade |\n"
	if got := checkSecurityPolicy(current, shipped); len(got) != 0 {
		t.Errorf("a table that names no major has nothing to contradict: %v", got)
	}
}

// TestThisRepositoryPassesItsOwnAudit is the regression test the whole command
// exists for: it runs against the real tree, not a fixture.
func TestThisRepositoryPassesItsOwnAudit(t *testing.T) {
	root, err := repositoryRoot(".")
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}
	findings, err := audit(root)
	if err != nil {
		t.Fatalf("auditing %s: %v", root, err)
	}
	if len(findings) != 0 {
		t.Errorf("the repository does not pass its own supply-chain audit:\n  %s", strings.Join(findings, "\n  "))
	}
}

// TestAnAmbiguousWorkflowIsRefusedRatherThanAudited: a duplicated mapping key
// is accepted by most YAML parsers, which keep one value and drop the other. A
// step written with two run: blocks would then be audited as though one of them
// did not exist, which is worse than not auditing it at all.
func TestAnAmbiguousWorkflowIsRefusedRatherThanAudited(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o750); err != nil {
		t.Fatal(err)
	}
	const dup = `jobs:
  a:
    steps:
      - run: echo one
        run: npx --yes something
`
	if err := os.WriteFile(filepath.Join(dir, ".github", "workflows", "w.yml"), []byte(dup), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := audit(dir); err == nil {
		t.Error("a workflow with a duplicated key was audited rather than refused")
	}
}

// TestRunReportsFindingsOnStdoutAndExitsNonZero pins the contract a CI step
// depends on: the findings are readable in the log and the exit code fails the
// job.
func TestRunReportsFindingsOnStdoutAndExitsNonZero(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o750); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(".github", "workflows", "w.yml"), "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v7\n")
	write(filepath.Join(".github", "dependabot.yml"), "version: 2\nupdates: []\n")
	write("VERSION", "1.0.0\n")
	write("SECURITY.md", "## Supported versions\n\n| Version | Supported |\n| --- | --- |\n| Latest release | Yes |\n")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--root", dir}, &stdout, &stderr); code != 1 {
		t.Errorf("exit code %d, want 1; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "is not pinned to a commit SHA") {
		t.Errorf("the finding is not on stdout: %q", stdout.String())
	}
}
