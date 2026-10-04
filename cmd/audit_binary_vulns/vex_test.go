// vex_test.go holds the OpenVEX document to the declaration table: the
// committed copy in the repository, every way a copy can drift from the
// table, and the copy a release stamps once the gate has passed.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// committedVEXPath and versionPath are the repository's committed document
// and the VERSION file it describes, relative to this package.
const (
	committedVEXPath = "../../.vex/libgen-mcp.openvex.json"
	versionPath      = "../../VERSION"
)

// fixtureVersion is the version the fixture documents describe.
const fixtureVersion = "2.2.0"

// fixtureDeclarations is a table with one declaration of each category.
var fixtureDeclarations = map[string]declaration{
	"GO-2026-0001 example.com/linked":   {category: categoryNotLinked, reason: "only example.com/linked/safe is linked"},
	"GO-2026-0002 example.com/upgraded": {category: categoryFixNotYetAdoptable, reason: "v1.2.3 is in its cooldown"},
}

// fixedClock pins the clock a written document is stamped with for the
// duration of one test.
func fixedClock(t *testing.T, at time.Time) {
	t.Helper()

	previous := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = previous })
}

// writeVEXFixture writes a document into a temporary directory and returns
// its path.
func writeVEXFixture(t *testing.T, doc vexDocument) string {
	t.Helper()

	data, err := encodeVEX(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "doc.openvex.json")
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// committedDocument is the committed document a table implies for the
// fixture version.
func committedDocument(declared map[string]declaration) vexDocument {
	return vexDocument{
		Context:    vexContext,
		ID:         vexCommittedID,
		Author:     vexAuthor,
		Timestamp:  "2026-10-04T00:00:00Z",
		Version:    1,
		Tooling:    vexTooling,
		Statements: vexStatements(declared, vexRelease{version: fixtureVersion}),
	}
}

// repositoryVersion is the version the VERSION file names.
func repositoryVersion(t *testing.T) string {
	t.Helper()

	data, err := os.ReadFile(versionPath)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// TestCommittedVEX_MatchesTheDeclarations is the tie between the two: the
// document the repository publishes states exactly the real table's not-linked
// declarations for the version in VERSION, so removing a declaration (an
// advisory fixed) fails here until the statement goes too, no statement can
// exist without one, and a version bump fails until the document names it.
func TestCommittedVEX_MatchesTheDeclarations(t *testing.T) {
	t.Parallel()

	doc, err := readVEX(committedVEXPath)
	if err != nil {
		t.Fatal(err)
	}
	if drift := vexDrift(doc, acceptedAdvisories, repositoryVersion(t)); len(drift) != 0 {
		t.Fatalf("%s disagrees with declarations.go and VERSION (run make gen-vex):\n%s", committedVEXPath, strings.Join(drift, "\n"))
	}
	data, err := os.ReadFile(committedVEXPath)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeVEX(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, encoded) {
		t.Errorf("%s is not formatted the way this command writes it (run make gen-vex)", committedVEXPath)
	}
}

// TestVEXStatements_OnlyNotLinkedDeclarationsAreStated: a
// fix-not-yet-adoptable declaration excuses code that is in the binary, so it
// must never become a not_affected statement.
func TestVEXStatements_OnlyNotLinkedDeclarationsAreStated(t *testing.T) {
	t.Parallel()

	statements := vexStatements(fixtureDeclarations, vexRelease{version: fixtureVersion})
	if len(statements) != 1 {
		t.Fatalf("got %d statements, want the one not-linked declaration", len(statements))
	}
	s := statements[0]
	if s.Vulnerability.Name != "GO-2026-0001" || s.Vulnerability.ID != "https://pkg.go.dev/vuln/GO-2026-0001" {
		t.Errorf("vulnerability = %+v", s.Vulnerability)
	}
	if s.Status != "not_affected" || s.Justification != "vulnerable_code_not_present" || s.ImpactStatement != "only example.com/linked/safe is linked" {
		t.Errorf("verdict = %q %q %q", s.Status, s.Justification, s.ImpactStatement)
	}
	for _, p := range s.Products {
		if len(p.Subcomponents) != 1 || p.Subcomponents[0].ID != "pkg:golang/example.com/linked" {
			t.Errorf("product %s names subcomponents %+v, want the module the advisory is filed against", p.ID, p.Subcomponents)
		}
	}
}

// TestVEXProducts_AreTheIdentifiersScannersCompute pins the product
// identifiers to the ones measured: Trivy's root module purl, its OCI purls
// (the docker.io one under index.docker.io), only ever with a digest, and
// Docker Scout's pkg:docker form. Every one carries the version, so no
// document matches a release it was not checked against.
func TestVEXProducts_AreTheIdentifiersScannersCompute(t *testing.T) {
	t.Parallel()

	digest := "sha256:" + strings.Repeat("ab", 32)
	for _, tc := range []struct {
		name string
		rel  vexRelease
		want []string
	}{
		{
			name: "committed",
			rel:  vexRelease{version: "2.2.0"},
			want: []string{
				"pkg:golang/github.com/jmrplens/libgen-mcp/v2@2.2.0",
				"pkg:golang/github.com/jmrplens/libgen-mcp/v2@v2.2.0",
				"pkg:docker/jmrplens/libgen-mcp@2.2.0",
				"pkg:docker/jmrplens/libgen-mcp@2.2.0?repository_url=ghcr.io",
			},
		},
		{
			name: "release",
			rel:  vexRelease{version: "2.2.0", indexDigest: digest},
			want: []string{
				"pkg:golang/github.com/jmrplens/libgen-mcp/v2@2.2.0",
				"pkg:golang/github.com/jmrplens/libgen-mcp/v2@v2.2.0",
				"pkg:oci/libgen-mcp@sha256%3A" + strings.Repeat("ab", 32) + "?repository_url=ghcr.io%2Fjmrplens%2Flibgen-mcp",
				"pkg:oci/libgen-mcp@sha256%3A" + strings.Repeat("ab", 32) + "?repository_url=index.docker.io%2Fjmrplens%2Flibgen-mcp",
				"pkg:docker/jmrplens/libgen-mcp@2.2.0",
				"pkg:docker/jmrplens/libgen-mcp@2.2.0?repository_url=ghcr.io",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := vexProducts(tc.rel); !slices.Equal(got, tc.want) {
				t.Errorf("products =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

// TestVEXDrift_EveryWayACopyDisagrees walks each way the committed document
// can disagree with the table and its version, and the agreeing one.
func TestVEXDrift_EveryWayACopyDisagrees(t *testing.T) {
	t.Parallel()

	rel := vexRelease{version: fixtureVersion}
	extra := vexStatements(map[string]declaration{"GO-2026-0003 example.com/other": {category: categoryNotLinked, reason: "x"}}, rel)[0]
	adoptable := vexStatements(map[string]declaration{"GO-2026-0002 example.com/upgraded": {category: categoryNotLinked, reason: "x"}}, rel)[0]
	twoModules := extra
	twoModules.Products = append(slices.Clone(extra.Products), vexProduct{ID: "pkg:golang/x", Subcomponents: []vexComponent{{ID: "pkg:golang/example.com/second@v1.0.0"}}})
	noModule := extra
	noModule.Products = []vexProduct{{ID: "pkg:golang/x", Subcomponents: []vexComponent{{ID: "pkg:npm/left-pad"}}}}
	otherVersion := vexStatements(fixtureDeclarations, vexRelease{version: "2.1.0"})

	for _, tc := range []struct {
		name string
		edit func(*vexDocument)
		want string
	}{
		{name: "agrees", edit: func(*vexDocument) {}},
		{name: "header", edit: func(d *vexDocument) { d.Author = "somebody" }, want: "document header"},
		{name: "timestamp", edit: func(d *vexDocument) { d.Timestamp = "yesterday" }, want: "RFC 3339 timestamp"},
		{name: "version", edit: func(d *vexDocument) { d.Version = 0 }, want: "RFC 3339 timestamp"},
		{name: "missing", edit: func(d *vexDocument) { d.Statements = nil }, want: `"GO-2026-0001 example.com/linked" is declared not-linked and has no statement`},
		{name: "undeclared", edit: func(d *vexDocument) { d.Statements = append(d.Statements, extra) }, want: `"GO-2026-0003 example.com/other" has a statement and no not-linked declaration behind it`},
		{name: "adoptable", edit: func(d *vexDocument) { d.Statements = append(d.Statements, adoptable) }, want: "is declared fix-not-yet-adoptable"},
		{name: "duplicate", edit: func(d *vexDocument) { d.Statements = append(d.Statements, d.Statements[0]) }, want: "has more than one statement"},
		{name: "two modules", edit: func(d *vexDocument) { d.Statements = append(d.Statements, twoModules) }, want: "names 2 advisory and module pairs"},
		{name: "no module", edit: func(d *vexDocument) { d.Statements = append(d.Statements, noModule) }, want: "names 0 advisory and module pairs"},
		{name: "text", edit: func(d *vexDocument) { d.Statements[0].Justification = "inline_mitigations_already_exist" }, want: "is not the one the declaration writes"},
		{name: "products", edit: func(d *vexDocument) { d.Statements[0].Products = d.Statements[0].Products[:1] }, want: "is not the one the declaration writes"},
		{name: "another release", edit: func(d *vexDocument) { d.Statements = otherVersion }, want: "is not the one the declaration writes for this version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			doc := committedDocument(fixtureDeclarations)
			tc.edit(&doc)
			drift := vexDrift(doc, fixtureDeclarations, fixtureVersion)
			if tc.want == "" {
				if len(drift) != 0 {
					t.Fatalf("drift = %v, want none", drift)
				}
				return
			}
			if !strings.Contains(strings.Join(drift, "\n"), tc.want) {
				t.Errorf("drift = %v, want one saying %q", drift, tc.want)
			}
		})
	}
}

// TestCommittedVEXDocument_VersionsTheStatementsNotTheFile: rewriting with
// nothing changed keeps the timestamp and version, a changed statement
// advances both, and a first write starts at version 1.
func TestCommittedVEXDocument_VersionsTheStatementsNotTheFile(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	fixedClock(t, at)

	unchanged := writeVEXFixture(t, committedDocument(fixtureDeclarations))
	doc, err := committedVEX(unchanged, fixtureDeclarations, fixtureVersion)
	if err != nil || doc.Timestamp != "2026-10-04T00:00:00Z" || doc.Version != 1 {
		t.Errorf("unchanged: timestamp %q version %d err %v, want the old ones kept", doc.Timestamp, doc.Version, err)
	}

	changed := committedDocument(fixtureDeclarations)
	changed.Version = 3
	changed.Statements[0].ImpactStatement = "older words"
	doc, err = committedVEX(writeVEXFixture(t, changed), fixtureDeclarations, fixtureVersion)
	if err != nil || doc.Timestamp != at.Format(time.RFC3339) || doc.Version != 4 {
		t.Errorf("changed: timestamp %q version %d err %v, want now and 4", doc.Timestamp, doc.Version, err)
	}

	doc, err = committedVEX(unchanged, fixtureDeclarations, "2.3.0")
	if err != nil || doc.Version != 2 || !strings.HasSuffix(doc.Statements[0].Products[0].ID, "@2.3.0") {
		t.Errorf("bumped: version %d err %v products %+v, want a new revision naming 2.3.0", doc.Version, err, doc.Statements[0].Products)
	}

	doc, err = committedVEX(filepath.Join(t.TempDir(), "new.json"), fixtureDeclarations, fixtureVersion)
	if err != nil || doc.Version != 1 || doc.Timestamp != at.Format(time.RFC3339) {
		t.Errorf("first: timestamp %q version %d err %v", doc.Timestamp, doc.Version, err)
	}

	broken := filepath.Join(t.TempDir(), "broken.json")
	if err = os.WriteFile(broken, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = committedVEX(broken, fixtureDeclarations, fixtureVersion); err == nil {
		t.Error("an unreadable committed document was overwritten instead of reported")
	}
}

// TestReadVEX_IsStrict: a field this command does not write, trailing data
// and a missing file are each an error rather than a document read half
// right.
func TestReadVEX_IsStrict(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, tc := range []struct{ name, body string }{
		{name: "unknown field", body: `{"@context":"x","role":"vendor"}`},
		{name: "trailing", body: `{"@context":"x"} {}`},
		{name: "not json", body: `nope`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readVEX(path); err == nil {
				t.Errorf("readVEX(%s) accepted it", tc.body)
			}
		})
	}
	if _, err := readVEX(filepath.Join(dir, "absent.json")); err == nil {
		t.Error("a missing document was read")
	}
}

// TestRunVEX_TheReleaseCopyIsWrittenOnlyAfterTheGatePasses drives the release
// mode over the fixture module: with the not-linked declaration true of the
// build graph the copy is written, pinned to the release, and with it false
// the gate fails and nothing is written at all.
func TestRunVEX_TheReleaseCopyIsWrittenOnlyAfterTheGatePasses(t *testing.T) {
	fixedClock(t, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))

	dir, config := fixtureRelease(t)
	declared := map[string]declaration{declarationKey(everyStdlib.id, "stdlib"): {category: categoryNotLinked, reason: "fixture"}}
	committed := writeVEXFixture(t, committedDocument(declared))
	digest := "sha256:" + strings.Repeat("0f", 32)

	// sequential: the cases share the fixture clock, which is a package variable
	for _, tc := range []struct {
		name    string
		imports []string
		code    int
		want    string
	}{
		{name: "not linked", imports: []string{"net/smtp"}, want: "verified not-linked"},
		{name: "linked", imports: []string{"runtime"}, code: 1, want: "wrote no statement"},
	} {
		advisory := everyStdlib
		advisory.imports = tc.imports
		out := filepath.Join(t.TempDir(), vexReleaseAsset)
		cfg := vexConfig{
			check: committed, out: out, version: fixtureVersion,
			release:  vexRelease{version: "2.2.0", indexDigest: digest},
			declared: declared,
			audit:    auditConfig{dir: dir, config: config, db: writeVulnDB(t, advisory)},
		}
		var stdout, stderr bytes.Buffer
		if code := runVEX(context.Background(), cfg, &stdout, &stderr); code != tc.code || !strings.Contains(stdout.String(), tc.want) {
			t.Errorf("%s: exit %d, want %d and %q\nstdout:\n%s\nstderr:\n%s", tc.name, code, tc.code, tc.want, stdout.String(), stderr.String())
			continue
		}
		_, statErr := os.Stat(out)
		if tc.code != 0 {
			if !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("%s: a copy was written although the gate failed", tc.name)
			}
			continue
		}
		var release vexDocument
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, &release); err != nil {
			t.Fatal(err)
		}
		if release.ID != "https://github.com/jmrplens/libgen-mcp/releases/download/v2.2.0/libgen-mcp.openvex.json" || release.Timestamp != "2026-10-05T12:00:00Z" {
			t.Errorf("release copy header = %q %q", release.ID, release.Timestamp)
		}
		if got := release.Statements[0].Products[2].ID; !strings.Contains(got, "@sha256%3A"+strings.Repeat("0f", 32)) {
			t.Errorf("release copy's OCI product = %q, want it pinned to the index digest", got)
		}
	}
}

// TestRunMain_VEXFlags drives the VEX modes through the command line: the
// committed copy checked and rewritten, and every combination of flags that
// says two things at once, or half of one, refused.
func TestRunMain_VEXFlags(t *testing.T) {
	fixedClock(t, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))

	committed, err := filepath.Abs(committedVEXPath)
	if err != nil {
		t.Fatal(err)
	}
	version := repositoryVersion(t)
	digest := "sha256:" + strings.Repeat("0f", 32)
	out := filepath.Join(t.TempDir(), vexReleaseAsset)
	rewritten := filepath.Join(t.TempDir(), "rewritten.json")
	data, err := os.ReadFile(committed)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(rewritten, data, 0o600); err != nil { //#nosec G703 -- a path under the test's own temporary directory
		t.Fatal(err)
	}
	drifted := writeVEXFixture(t, committedDocument(fixtureDeclarations))

	for _, tc := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{name: "check", args: []string{"-vex-check", committed, "-vex-version", version}, want: "states exactly the 1 not-linked declarations, for " + version},
		{name: "drift", args: []string{"-vex-check", drifted, "-vex-version", version}, code: 1, want: "VEX DRIFT"},
		{name: "rewrite", args: []string{"-vex-write", rewritten, "-vex-version", version}, want: "(1 statements, version "},
		{name: "no version", args: []string{"-vex-check", committed}, code: 2},
		{name: "bad committed version", args: []string{"-vex-check", committed, "-vex-version", "v" + version}, code: 2},
		{name: "write and check", args: []string{"-vex-write", rewritten, "-vex-check", committed, "-vex-version", version}, code: 2},
		{name: "out without check", args: []string{"-vex-out", out, "-vex-version", version}, code: 2},
		{name: "pin without out", args: []string{"-vex-check", committed, "-vex-version", version, "-vex-release", "2.2.0"}, code: 2},
		{name: "bad release", args: []string{"-vex-check", committed, "-vex-version", version, "-vex-out", out, "-vex-release", "v2.2.0", "-vex-index-digest", digest}, code: 2},
		{name: "bad digest", args: []string{"-vex-check", committed, "-vex-version", version, "-vex-out", out, "-vex-release", "2.2.0", "-vex-index-digest", "latest"}, code: 2},
		{name: "missing", args: []string{"-vex-check", filepath.Join(t.TempDir(), "absent.json"), "-vex-version", version}, code: 2},
		{name: "unreadable rewrite", args: []string{"-vex-write", t.TempDir(), "-vex-version", version}, code: 2},
		{name: "unwritable release copy", args: []string{"-vex-check", committed, "-vex-version", version, "-vex-out", out, "-vex-release", "2.2.0", "-vex-index-digest", digest, "-config", filepath.Join(t.TempDir(), "absent.yml")}, code: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runMain(context.Background(), tc.args, &stdout, &stderr); code != tc.code {
				t.Errorf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tc.code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.want) {
				t.Errorf("stdout lacks %q:\n%s", tc.want, stdout.String())
			}
		})
	}
	if _, statErr := os.Stat(out); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("a release copy was written by a run whose gate could not be made")
	}
}

// TestWriteVEX_AnUnwritablePathFails covers the write itself failing.
func TestWriteVEX_AnUnwritablePathFails(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := writeVEX(filepath.Join(t.TempDir(), "no", "such", "dir.json"), vexDocument{}, &stdout, &stderr); code != 2 || stderr.Len() == 0 {
		t.Errorf("exit %d, stderr %q, want 2 and the reason", code, stderr.String())
	}
}
