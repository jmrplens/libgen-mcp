// vex_test.go holds the OpenVEX document to the declaration table: the
// committed copy in the repository, every way a copy can drift from the
// table, and the copy a release stamps.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// committedVEXPath is the repository's committed document, relative to this
// package.
const committedVEXPath = "../../.vex/libgen-mcp.openvex.json"

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

// fixtureDocument is the committed document the fixture table implies.
func fixtureDocument() vexDocument {
	return vexDocument{
		Context:    vexContext,
		ID:         vexCommittedID,
		Author:     vexAuthor,
		Timestamp:  "2026-10-04T00:00:00Z",
		Version:    1,
		Tooling:    vexTooling,
		Statements: vexStatements(fixtureDeclarations, nil),
	}
}

// TestCommittedVEX_MatchesTheDeclarations is the tie between the two: the
// document the repository publishes states exactly the real table's not-linked
// declarations, so removing a declaration (an advisory fixed) fails here until
// the statement goes too, and no statement can exist without one.
func TestCommittedVEX_MatchesTheDeclarations(t *testing.T) {
	t.Parallel()

	doc, err := readVEX(committedVEXPath)
	if err != nil {
		t.Fatal(err)
	}
	if drift := vexDrift(doc, acceptedAdvisories); len(drift) != 0 {
		t.Fatalf("%s disagrees with declarations.go (run make gen-vex):\n%s", committedVEXPath, strings.Join(drift, "\n"))
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

	statements := vexStatements(fixtureDeclarations, nil)
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
// identifiers to the ones measured: Trivy's root module and OCI purls (the
// docker.io one under index.docker.io) and Docker Scout's pkg:docker form.
func TestVEXProducts_AreTheIdentifiersScannersCompute(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		rel  *vexRelease
		want []string
	}{
		{
			name: "committed",
			want: []string{
				"pkg:golang/github.com/jmrplens/libgen-mcp/v2",
				"pkg:oci/libgen-mcp?repository_url=ghcr.io%2Fjmrplens%2Flibgen-mcp",
				"pkg:oci/libgen-mcp?repository_url=index.docker.io%2Fjmrplens%2Flibgen-mcp",
				"pkg:docker/jmrplens/libgen-mcp",
				"pkg:docker/jmrplens/libgen-mcp?repository_url=ghcr.io",
			},
		},
		{
			name: "release",
			rel:  &vexRelease{version: "2.2.0", indexDigest: "sha256:" + strings.Repeat("ab", 32)},
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
// can disagree with the table, and the agreeing one.
func TestVEXDrift_EveryWayACopyDisagrees(t *testing.T) {
	t.Parallel()

	extra := vexStatements(map[string]declaration{"GO-2026-0003 example.com/other": {category: categoryNotLinked, reason: "x"}}, nil)[0]
	adoptable := vexStatements(map[string]declaration{"GO-2026-0002 example.com/upgraded": {category: categoryNotLinked, reason: "x"}}, nil)[0]
	twoModules := extra
	twoModules.Products = append(slices.Clone(extra.Products), vexProduct{ID: "pkg:golang/x", Subcomponents: []vexComponent{{ID: "pkg:golang/example.com/second@v1.0.0"}}})
	noModule := extra
	noModule.Products = []vexProduct{{ID: "pkg:golang/x", Subcomponents: []vexComponent{{ID: "pkg:npm/left-pad"}}}}

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
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			doc := fixtureDocument()
			doc.Statements = vexStatements(fixtureDeclarations, nil)
			tc.edit(&doc)
			drift := vexDrift(doc, fixtureDeclarations)
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

	unchanged := writeVEXFixture(t, fixtureDocument())
	doc, err := committedVEX(unchanged, fixtureDeclarations)
	if err != nil || doc.Timestamp != "2026-10-04T00:00:00Z" || doc.Version != 1 {
		t.Errorf("unchanged: timestamp %q version %d err %v, want the old ones kept", doc.Timestamp, doc.Version, err)
	}

	changed := fixtureDocument()
	changed.Version = 3
	changed.Statements[0].ImpactStatement = "older words"
	doc, err = committedVEX(writeVEXFixture(t, changed), fixtureDeclarations)
	if err != nil || doc.Timestamp != at.Format(time.RFC3339) || doc.Version != 4 {
		t.Errorf("changed: timestamp %q version %d err %v, want now and 4", doc.Timestamp, doc.Version, err)
	}

	doc, err = committedVEX(filepath.Join(t.TempDir(), "new.json"), fixtureDeclarations)
	if err != nil || doc.Version != 1 || doc.Timestamp != at.Format(time.RFC3339) {
		t.Errorf("first: timestamp %q version %d err %v", doc.Timestamp, doc.Version, err)
	}

	broken := filepath.Join(t.TempDir(), "broken.json")
	if err = os.WriteFile(broken, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = committedVEX(broken, fixtureDeclarations); err == nil {
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

// TestRunMain_VEXFlags drives the VEX modes through the command line: the
// committed copy checked, rewritten and stamped for a release, and every
// combination of flags that says two things at once refused.
func TestRunMain_VEXFlags(t *testing.T) {
	fixedClock(t, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))

	committed, err := filepath.Abs(committedVEXPath)
	if err != nil {
		t.Fatal(err)
	}
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
	drifted := writeVEXFixture(t, fixtureDocument())

	// sequential: the release case writes the file the next case reads back
	for _, tc := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{name: "check", args: []string{"-vex-check", committed}, want: "states exactly the 1 not-linked declarations"},
		{name: "drift", args: []string{"-vex-check", drifted}, code: 1, want: "VEX DRIFT"},
		{name: "release", args: []string{"-vex-check", committed, "-vex-out", out, "-vex-release", "2.2.0", "-vex-index-digest", digest}, want: "wrote " + out},
		{name: "rewrite", args: []string{"-vex-write", rewritten}, want: "(1 statements, version 1)"},
		{name: "write and check", args: []string{"-vex-write", rewritten, "-vex-check", committed}, code: 2},
		{name: "out without check", args: []string{"-vex-out", out}, code: 2},
		{name: "pin without out", args: []string{"-vex-check", committed, "-vex-release", "2.2.0"}, code: 2},
		{name: "bad version", args: []string{"-vex-check", committed, "-vex-out", out, "-vex-release", "v2.2.0", "-vex-index-digest", digest}, code: 2},
		{name: "bad digest", args: []string{"-vex-check", committed, "-vex-out", out, "-vex-release", "2.2.0", "-vex-index-digest", "latest"}, code: 2},
		{name: "missing", args: []string{"-vex-check", filepath.Join(t.TempDir(), "absent.json")}, code: 2},
		{name: "unwritable", args: []string{"-vex-check", committed, "-vex-out", filepath.Join(t.TempDir(), "no", "such", "dir.json"), "-vex-release", "2.2.0", "-vex-index-digest", digest}, code: 2},
		{name: "unreadable rewrite", args: []string{"-vex-write", t.TempDir()}, code: 2},
	} {
		var stdout, stderr bytes.Buffer
		if code := runMain(context.Background(), tc.args, &stdout, &stderr); code != tc.code {
			t.Errorf("%s: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", tc.name, code, tc.code, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), tc.want) {
			t.Errorf("%s: stdout lacks %q:\n%s", tc.name, tc.want, stdout.String())
		}
	}

	var release vexDocument
	data, err = os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &release); err != nil {
		t.Fatal(err)
	}
	if release.ID != "https://github.com/jmrplens/libgen-mcp/releases/download/v2.2.0/libgen-mcp.openvex.json" || release.Timestamp != "2026-10-05T12:00:00Z" {
		t.Errorf("release copy header = %q %q", release.ID, release.Timestamp)
	}
	if got := release.Statements[0].Products[0].ID; got != "pkg:golang/github.com/jmrplens/libgen-mcp/v2@2.2.0" {
		t.Errorf("release copy's first product = %q, want it pinned to the release", got)
	}
}
