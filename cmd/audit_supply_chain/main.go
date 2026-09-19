package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// A pinned reference is owner/repo[/subpath]@<40 hex>. The trailing comment
// naming the version is not part of the value the runner resolves, so it is not
// matched here; it is checked separately, because an action pinned without one
// is pinned and frozen — Dependabot has nothing to read.
var (
	pinnedUses  = regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)
	usesLine    = regexp.MustCompile(`^\s*(?:-\s*)?uses:\s*(\S+)(.*)$`)
	versionNote = regexp.MustCompile(`#\s*v?\d`)
)

// envExpression matches the one indirection a version pin is allowed to take: a
// ${{ env.NAME }} reference into the workflow's top-level env block, where the
// value itself is exact.
var envExpression = regexp.MustCompile(`\$\{\{\s*env\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// exactVersion is v<major>.<minor>.<patch>, optionally with a pre-release tail.
var exactVersion = regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// supportedMajor picks a `N.x` cell out of a SECURITY.md table row.
var supportedMajor = regexp.MustCompile("`(\\d+)\\.x`")

// scriptReference finds the scripts/<file> paths a run: block invokes, so their
// contents are audited alongside the block itself: a job that runs no
// unpinned code directly and then calls a script that does is not safer for it.
var scriptReference = regexp.MustCompile(`scripts/[A-Za-z0-9_.-]+\.(?:sh|mjs|py|ps1)`)

// pipInstall is the positive half of the pip rule. RE2 has no lookahead, so the
// "without --require-hashes" half is applied to the rest of the line separately.
var pipInstall = regexp.MustCompile(`\bpip\s+install\b`)

// credentialedPermissions are the permissions that make a job worth attacking:
// one mints this repository's publishing identity, the other can rewrite it.
var credentialedPermissions = []string{"contents", "id-token"}

// versionInputs are the action inputs that name a tool the action then
// downloads. Pinning the action by SHA says nothing about what it fetches.
var versionInputs = map[string]string{
	"goreleaser/goreleaser-action": "version",
	"sigstore/cosign-installer":    "cosign-release",
}

// cooldownEcosystems are the ecosystems where an explicit cooldown is
// meaningful — every one this repository configures.
var cooldownEcosystems = []string{"gomod", "npm", "github-actions", "docker"}

// minCooldownDays is the shortest release window this repository accepts before
// Dependabot may propose a dependency.
const minCooldownDays = 3

// The two paths this audit reads, named once so a finding and the file it
// opens cannot drift apart.
const (
	githubDir      = ".github"
	dependabotFile = githubDir + "/dependabot.yml"
)

// repositoryRoot walks up from start until it finds the directory holding
// go.mod, so the auditor can be run from anywhere in the tree.
func repositoryRoot(start string) (string, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(current, "go.mod")); statErr == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("go.mod not found from %s", start)
		}
		current = parent
	}
}

// checkPinnedUses reads the raw text rather than the parsed document on
// purpose: a uses: inside a block someone commented out is still a line a
// future edit will uncomment, and the parser would not show it.
func checkPinnedUses(pathLabel, text string) []string {
	var findings []string
	for i, line := range strings.Split(text, "\n") {
		m := usesLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ref, rest := m[1], m[2]
		// A local reusable workflow is this repository; there is nothing to pin.
		if strings.HasPrefix(ref, "./") {
			continue
		}
		if !pinnedUses.MatchString(ref) {
			findings = append(findings, fmt.Sprintf(
				"%s:%d: uses: %s is not pinned to a commit SHA; a tag is a pointer its owner can move",
				pathLabel, i+1, ref,
			))
			continue
		}
		if !versionNote.MatchString(rest) {
			findings = append(findings, fmt.Sprintf(
				"%s:%d: uses: %s carries no version comment; Dependabot cannot tell what the SHA stands for, so the action is pinned and frozen",
				pathLabel, i+1, ref,
			))
		}
	}
	return findings
}

// jobIsCredentialed reports whether the job can mint or spend this
// repository's identity, taking the workflow-level permissions as the default
// the job inherits when it declares none of its own.
func jobIsCredentialed(job, doc map[string]any) bool {
	perms, ok := job["permissions"]
	if !ok {
		perms = doc["permissions"]
	}
	switch p := perms.(type) {
	case string:
		// `permissions: write-all` is every permission at once.
		return p == "write-all"
	case map[string]any:
		for _, name := range credentialedPermissions {
			if v, has := p[name]; has && v == "write" {
				return true
			}
		}
	}
	return false
}

// runtimeResolvedCode names the ways a run: block can fetch and execute
// something chosen at run time rather than reviewed at merge time.
func runtimeResolvedCode(where, text string) []string {
	var findings []string
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "#") {
			continue
		}
		switch {
		case strings.Contains(line, "npx "):
			findings = append(findings, fmt.Sprintf(
				"%s:%d: npx resolves a package tree at run time inside a credentialed job", where, i+1,
			))
		case strings.Contains(line, "@latest"):
			findings = append(findings, fmt.Sprintf(
				"%s:%d: @latest is whatever the registry serves at that moment", where, i+1,
			))
		case regexp.MustCompile(`curl[^|]*\|\s*(ba)?sh`).MatchString(line):
			findings = append(findings, fmt.Sprintf(
				"%s:%d: a download piped into a shell runs code nobody reviewed", where, i+1,
			))
		case pipInstall.MatchString(line) && !strings.Contains(line, "--require-hashes"):
			findings = append(findings, fmt.Sprintf(
				"%s:%d: pip install without --require-hashes resolves the dependency tree at run time", where, i+1,
			))
		}
	}
	return findings
}

// checkStepAction catches the gap a SHA pin leaves: an action that downloads a
// tool is pinned, and the tool it downloads is not.
func checkStepAction(where, uses string, with, doc map[string]any) []string {
	action, _, _ := strings.Cut(uses, "@")
	input, watched := versionInputs[action]
	if !watched {
		return nil
	}
	value, _ := with[input].(string)
	if value == "" {
		return []string{fmt.Sprintf(
			"%s: %s names no %s, so the tool it downloads is whatever is current at run time",
			where, action, input,
		)}
	}
	// One indirection is allowed: ${{ env.NAME }} into the workflow's own env
	// block, where the value has to be exact. That keeps the version in one
	// place without leaving it unpinned.
	if m := envExpression.FindStringSubmatch(value); m != nil {
		env, _ := doc["env"].(map[string]any)
		resolved, _ := env[m[1]].(string)
		if resolved == "" {
			return []string{fmt.Sprintf(
				"%s: %s: %s points at env.%s, which the workflow does not define",
				where, action, input, m[1],
			)}
		}
		value = resolved
	}
	if !exactVersion.MatchString(value) {
		return []string{fmt.Sprintf(
			"%s: %s: %s is %q; pin an exact version, because SHA-pinning the action does not pin the binary it fetches",
			where, action, input, value,
		)}
	}
	return nil
}

// checkWorkflowJobs walks the parsed document. Everything here is about
// structure — which job holds what, and what its steps run — so it reads the
// document rather than the text.
func checkWorkflowJobs(pathLabel string, doc map[string]any, root string) []string {
	var findings []string
	jobs, _ := doc["jobs"].(map[string]any)
	ids := make([]string, 0, len(jobs))
	for id := range jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	seenScripts := map[string]bool{}
	for _, id := range ids {
		job, _ := jobs[id].(map[string]any)
		if job == nil || !jobIsCredentialed(job, doc) {
			continue
		}
		steps, _ := job["steps"].([]any)
		for i, raw := range steps {
			step, _ := raw.(map[string]any)
			if step == nil {
				continue
			}
			where := fmt.Sprintf("%s: job %q step %d", pathLabel, id, i+1)
			findings = append(findings, checkStep(where, step, doc, root, seenScripts)...)
		}
	}
	return findings
}

// checkStep audits one step of a credentialed job: what it downloads, and what
// it runs — its own shell and any repository script that shell invokes.
func checkStep(where string, step, doc map[string]any, root string, seenScripts map[string]bool) []string {
	var findings []string
	if uses, ok := step["uses"].(string); ok {
		with, _ := step["with"].(map[string]any)
		findings = append(findings, checkStepAction(where, uses, with, doc)...)
	}
	run, _ := step["run"].(string)
	if run == "" {
		return findings
	}
	findings = append(findings, runtimeResolvedCode(where, run)...)
	return append(findings, checkReferencedScripts(where, run, root, seenScripts)...)
}

// checkReferencedScripts audits the scripts/ files a run: block invokes. A job
// that runs nothing unpinned itself and then calls a script that does is not
// safer for the indirection.
func checkReferencedScripts(where, run, root string, seenScripts map[string]bool) []string {
	var findings []string
	for _, script := range scriptReference.FindAllString(run, -1) {
		if seenScripts[script] {
			continue
		}
		seenScripts[script] = true
		body, err := os.ReadFile(filepath.Join(root, script))
		if err != nil {
			continue
		}
		findings = append(findings,
			runtimeResolvedCode(fmt.Sprintf("%s (via %s)", where, script), string(body))...)
	}
	return findings
}

// checkDependabot holds every configured ecosystem to an explicit cooldown.
// Inheriting the platform default means the window is whatever GitHub decides
// it is next, which is not a decision this repository made.
func checkDependabot(doc map[string]any) []string {
	var findings []string
	updates, _ := doc["updates"].([]any)
	for _, raw := range updates {
		entry, _ := raw.(map[string]any)
		if entry == nil {
			continue
		}
		findings = append(findings, checkDependabotEntry(entry)...)
	}
	return findings
}

// checkDependabotEntry holds one ecosystem's entry to a stated cooldown at or
// above the floor, and to the sub-keys Dependabot will accept for it.
func checkDependabotEntry(entry map[string]any) []string {
	ecosystem, _ := entry["package-ecosystem"].(string)
	if !slices.Contains(cooldownEcosystems, ecosystem) {
		return nil
	}
	directory, _ := entry["directory"].(string)
	label := fmt.Sprintf("%s (%s)", ecosystem, directory)
	config := dependabotFile

	cooldown, _ := entry["cooldown"].(map[string]any)
	if cooldown == nil {
		return []string{fmt.Sprintf(
			"%s: %s states no cooldown, so the window is the platform default GitHub can change", config, label,
		)}
	}

	var findings []string
	days, ok := cooldown["default-days"].(int)
	if !ok || days < minCooldownDays {
		findings = append(findings, fmt.Sprintf(
			"%s: %s has default-days %v, below the %d this repository accepts",
			config, label, cooldown["default-days"], minCooldownDays,
		))
	}
	// Dependabot rejects the SemVer sub-keys for docker outright, and a
	// configuration file it refuses stops every update rather than one.
	if ecosystem != "docker" {
		return findings
	}
	for key := range cooldown {
		if strings.HasPrefix(key, "semver-") {
			findings = append(findings, fmt.Sprintf(
				"%s: %s carries %s, which Dependabot rejects for docker — the whole file is then ignored",
				config, label, key,
			))
		}
	}
	return findings
}

// checkSecurityPolicy holds SECURITY.md to the majors this repository ships.
// The table itself is the policy's; what is checked is that it exists and that
// no row promises fixes for a major that has been superseded.
func checkSecurityPolicy(version, securityMD string) []string {
	if !strings.Contains(securityMD, "## Supported versions") {
		return []string{"SECURITY.md: no \"## Supported versions\" section, so nothing states which releases get fixes"}
	}
	currentMajor, _, _ := strings.Cut(strings.TrimSpace(version), ".")
	var findings []string
	for line := range strings.SplitSeq(securityMD, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		m := supportedMajor.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		supported := strings.Contains(strings.ToLower(line), "yes")
		if supported && m[1] != currentMajor {
			findings = append(findings, fmt.Sprintf(
				"SECURITY.md: the table marks %s.x supported while this repository ships %s.x", m[1], currentMajor,
			))
		}
	}
	return findings
}

func audit(root string) ([]string, error) {
	var findings []string

	workflowDir := filepath.Join(root, githubDir, "workflows")
	workflows, err := filepath.Glob(filepath.Join(workflowDir, "*.yml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(workflows)
	if len(workflows) == 0 {
		return nil, fmt.Errorf("no workflows under %s", workflowDir)
	}
	for _, path := range workflows {
		text, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		label := filepath.ToSlash(filepath.Join(githubDir, "workflows", filepath.Base(path)))
		findings = append(findings, checkPinnedUses(label, string(text))...)

		var doc map[string]any
		// KnownFields is not enough: a duplicated mapping key is accepted by
		// most parsers, which silently keep one value. A workflow whose meaning
		// depends on that choice is refused rather than audited.
		decoder := yaml.NewDecoder(strings.NewReader(string(text)))
		if decodeErr := decoder.Decode(&doc); decodeErr != nil {
			return nil, fmt.Errorf("%s: %w", label, decodeErr)
		}
		findings = append(findings, checkWorkflowJobs(label, doc, root)...)
	}

	dependabot, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dependabotFile)))
	if err != nil {
		return nil, err
	}
	var dependabotDoc map[string]any
	if unmarshalErr := yaml.Unmarshal(dependabot, &dependabotDoc); unmarshalErr != nil {
		return nil, fmt.Errorf("%s: %w", dependabotFile, unmarshalErr)
	}
	findings = append(findings, checkDependabot(dependabotDoc)...)

	version, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return nil, err
	}
	securityMD, err := os.ReadFile(filepath.Join(root, "SECURITY.md"))
	if err != nil {
		return nil, err
	}
	findings = append(findings, checkSecurityPolicy(string(version), string(securityMD))...)

	return findings, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("audit_supply_chain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "repository root (default: the directory holding go.mod)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dir := *root
	if dir == "" {
		found, err := repositoryRoot(".")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		dir = found
	}

	findings, err := audit(dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(findings) > 0 {
		for _, f := range findings {
			fmt.Fprintln(stdout, f)
		}
		fmt.Fprintf(stdout, "\n%d supply-chain finding(s)\n", len(findings))
		return 1
	}
	fmt.Fprintln(stdout, "supply chain: every action pinned, every credentialed job free of run-time-resolved code, cooldowns stated, security policy current")
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
