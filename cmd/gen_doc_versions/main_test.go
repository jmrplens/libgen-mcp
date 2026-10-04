// Tests for the docs/ release-value generator.

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// defaultCitation is the CITATION.cff a fixture gets unless it names its own.
const defaultCitation = "cff-version: 1.2.0\nversion: 2.2.0\ndate-released: 2027-03-15\n"

// fixture writes a minimal repository: VERSION, a CITATION.cff unless files
// names one ("" leaves it out), the files map relative to the root, and
// returns the root.
func fixture(t *testing.T, version string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if version != "" {
		files["VERSION"] = version + "\n"
	}
	if _, ok := files["CITATION.cff"]; !ok {
		files["CITATION.cff"] = defaultCitation
	}
	for rel, body := range files {
		if rel == "CITATION.cff" && body == "" {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// readFile returns a fixture file's contents.
func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const (
	twinPage = "Up to 2.0.1 it differed.\n\n```bash\nnpx -y pkg@%%VERSION%%\n```\n\nFrom 2.1.0, `go install x@v%%VERSION%%`.\n"
	docPage  = "Up to 2.0.1 it differed.\n\n```bash\nnpx -y pkg@2.1.0\n```\n\nFrom 2.1.0, `go install x@v2.1.0`.\n"

	citeTwin = "---\ntitle: Citations\ndatePublished: \"2026-09-24\"\n---\n\nFirst released in 2026, may it last.\n\n" +
		"```bibtex\n@software{x_%%RELEASE_YEAR%%,\nmonth = %%RELEASE_MONTH%%,\nversion = {%%VERSION%%},\nyear = {%%RELEASE_YEAR%%}\n}\n```\n\n" +
		"A paper from 1998, month = jan, stays.\n"
	citeDoc = "First released in 2026, may it last.\n\n" +
		"```bibtex\n@software{x_2026,\nmonth = oct,\nversion = {2.1.0},\nyear = {2026}\n}\n```\n\n" +
		"A paper from 1998, month = jan, stays.\n"
)

// TestRun_RewritesOnlyTheCurrentReleaseSpots drives a version bump end to end:
// the token positions move to the new release, the historical mentions stay,
// the Spanish twin is ignored and a page without a token is left alone.
func TestRun_RewritesOnlyTheCurrentReleaseSpots(t *testing.T) {
	root := fixture(t, "2.2.0", map[string]string{
		"site/src/content/docs/install/npm.mdx":    twinPage,
		"site/src/content/docs/es/install/npm.mdx": "Desde la 9.9.9, `pkg@%%VERSION%%`.\n",
		"site/src/content/docs/other.mdx":          "Only 1.0.0 here.\n",
		"site/src/content/docs/notes.txt":          "%%VERSION%%",
		"docs/install/npm.md":                      docPage,
		"docs/other.md":                            "Only 1.0.0 there, 5.5.5 too.\n",
	})
	var out bytes.Buffer
	if err := run([]string{"-root", root}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := "Up to 2.0.1 it differed.\n\n```bash\nnpx -y pkg@2.2.0\n```\n\nFrom 2.1.0, `go install x@v2.2.0`.\n"
	if got := readFile(t, root, "docs/install/npm.md"); got != want {
		t.Errorf("docs page =\n%s\nwant\n%s", got, want)
	}
	if got := readFile(t, root, "docs/other.md"); got != "Only 1.0.0 there, 5.5.5 too.\n" {
		t.Errorf("a page with no token was rewritten: %q", got)
	}
	if !strings.Contains(out.String(), "docs/install/npm.md: 2 mention(s) brought up to date") {
		t.Errorf("stdout = %q", out.String())
	}

	out.Reset()
	if err := run([]string{"-root", root, "-check"}, &out); err != nil {
		t.Errorf("check after a rewrite: %v", err)
	}
	out.Reset()
	if err := run([]string{"-root", root}, &out); err != nil || out.Len() != 0 {
		t.Errorf("a second rewrite: err=%v stdout=%q, want nothing to do", err, out.String())
	}
}

// TestRun_CitationDateFollowsCitationFile asserts the year and month tokens
// take CITATION.cff's date-released, that the twin's frontmatter is not read
// as content, and that a year or a month outside a token position stays.
func TestRun_CitationDateFollowsCitationFile(t *testing.T) {
	root := fixture(t, "2.2.0", map[string]string{
		"site/src/content/docs/citations.mdx": citeTwin,
		"docs/citations.md":                   citeDoc,
	})
	if err := run([]string{"-root", root}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := "First released in 2026, may it last.\n\n" +
		"```bibtex\n@software{x_2027,\nmonth = mar,\nversion = {2.2.0},\nyear = {2027}\n}\n```\n\n" +
		"A paper from 1998, month = jan, stays.\n"
	if got := readFile(t, root, "docs/citations.md"); got != want {
		t.Errorf("docs page =\n%s\nwant\n%s", got, want)
	}
}

// TestRun_CheckReportsEveryStaleSpot asserts check mode names each docs line
// that still carries the previous release and writes nothing.
func TestRun_CheckReportsEveryStaleSpot(t *testing.T) {
	root := fixture(t, "2.2.0", map[string]string{
		"site/src/content/docs/install/npm.mdx": twinPage,
		"site/src/content/docs/citations.mdx":   citeTwin,
		"docs/install/npm.md":                   docPage,
		"docs/citations.md":                     citeDoc,
	})
	err := run([]string{"-root", root, "-check"}, &bytes.Buffer{})
	if !errors.Is(err, errStale) {
		t.Fatalf("err = %v, want errStale", err)
	}
	for _, want := range []string{
		"docs/install/npm.md:4 names 2.1.0",
		"docs/install/npm.md:7 names 2.1.0",
		"docs/citations.md:5 names oct where site/src/content/docs/citations.mdx writes %%RELEASE_MONTH%%",
		"docs/citations.md:4 names 2026",
		"VERSION 2.2.0, CITATION.cff date-released mar 2027",
	} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		})
	}
	if got := readFile(t, root, "docs/install/npm.md"); got != docPage {
		t.Error("check mode wrote the page")
	}
}

// TestRun_Refusals covers every way a run stops before touching a page.
func TestRun_Refusals(t *testing.T) {
	cases := []struct {
		name    string
		version string
		files   map[string]string
		args    []string
		want    string
	}{
		{
			name:  "no VERSION file",
			files: map[string]string{"docs/a.md": ""},
			want:  "read VERSION",
		},
		{
			name:    "VERSION is not a release",
			version: "next",
			files:   map[string]string{},
			want:    `VERSION holds "next"`,
		},
		{
			name:    "VERSION has an empty prerelease field",
			version: "2.2.0-rc..1",
			files:   map[string]string{},
			want:    `VERSION holds "2.2.0-rc..1"`,
		},
		{
			name:    "no CITATION.cff",
			version: "1.0.0",
			files:   map[string]string{"CITATION.cff": ""},
			want:    "read CITATION.cff",
		},
		{
			name:    "no date-released",
			version: "1.0.0",
			files:   map[string]string{"CITATION.cff": "version: 1.0.0\n"},
			want:    "no date-released",
		},
		{
			name:    "a month that does not exist",
			version: "1.0.0",
			files:   map[string]string{"CITATION.cff": "date-released: 2026-13-01\n"},
			want:    "no date-released",
		},
		{
			name:    "no Starlight pages",
			version: "1.0.0",
			files:   map[string]string{},
			want:    "walk site/src/content/docs",
		},
		{
			name:    "a token page with no docs twin",
			version: "1.0.0",
			files:   map[string]string{"site/src/content/docs/lonely.mdx": "%%RELEASE_YEAR%%"},
			want:    "has no docs/ twin at docs/lonely.md",
		},
		{
			name:    "a historical mention that differs",
			version: "1.0.0",
			files: map[string]string{
				"site/src/content/docs/a.mdx": "Up to 2.0.1, then %%VERSION%%.",
				"docs/a.md":                   "Up to 2.0.0, then 1.0.0.",
			},
			want: `the twin has "2.0.1" at line 1, docs/ has "2.0.0" at line 1`,
		},
		{
			name:    "docs has fewer mentions",
			version: "1.0.0",
			files: map[string]string{
				"site/src/content/docs/a.mdx": "Pin %%VERSION%%.\n\nOr %%VERSION%%.",
				"docs/a.md":                   "Pin 1.0.0.",
			},
			want: "docs/ has nothing (the page has no more of them)",
		},
		{
			name:    "an unknown flag",
			version: "1.0.0",
			files:   map[string]string{},
			args:    []string{"-nope"},
			want:    "flag provided but not defined",
		},
		{
			name:    "a stray argument",
			version: "1.0.0",
			files:   map[string]string{},
			args:    []string{"docs"},
			want:    "unexpected arguments: docs",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := fixture(t, tc.version, tc.files)
			args := append([]string{"-root", root}, tc.args...)
			err := run(args, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestMentions_Boundaries pins what counts as a value of each kind: a leading
// v is allowed on a version, a longer number or identifier is not, and a
// sentence's full stop is not part of it.
func TestMentions_Boundaries(t *testing.T) {
	version, year, month := kinds[0], kinds[1], kinds[2]
	cases := []struct {
		name string
		k    kind
		src  string
		want []string
	}{
		{name: "plain and v-prefixed", k: version, src: "2.1.0 and v2.1.0", want: []string{"2.1.0", "2.1.0"}},
		{name: "end of sentence", k: version, src: "for 2.1.0. Next", want: []string{"2.1.0"}},
		{name: "prerelease", k: version, src: "tag 2.2.0-rc.1 now", want: []string{"2.2.0-rc.1"}},
		{name: "an address", k: version, src: "127.0.0.1 and 10.0.0.0/8", want: nil},
		{name: "a toolchain", k: version, src: "go1.27.0", want: nil},
		{name: "four numeric fields", k: version, src: "1.2.3.4", want: nil},
		{name: "after a dot", k: version, src: "x.2.1.0", want: nil},
		{name: "end of input", k: version, src: "v2.1.0", want: []string{"2.1.0"}},
		{name: "years", k: year, src: "x_2026, (2026) 2026-10-03", want: []string{"2026", "2026", "2026"}},
		{name: "not a year", k: year, src: "12026 a2026 20260", want: nil},
		{name: "months only in BibTeX form", k: month, src: "you may, month = oct, month = octo", want: []string{"oct"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, m := range mentions(tc.k, tc.src, false) {
				got = append(got, m.text)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("mentions(%q) = %v, want %v", tc.src, got, tc.want)
			}
		})
	}
}

// TestMentions_TokensInterleave asserts tokens and literals come back in page
// order, which is what the alignment depends on.
func TestMentions_TokensInterleave(t *testing.T) {
	got := mentions(kinds[0], "a %%VERSION%% b 1.0.0 c %%VERSION%%", true)
	var texts []string
	for _, m := range got {
		texts = append(texts, m.text)
	}
	if want := "%%VERSION%%,1.0.0,%%VERSION%%"; strings.Join(texts, ",") != want {
		t.Errorf("mentions = %v, want %s", texts, want)
	}
}

// TestRun_WriteFailureIsReported makes the docs page unwritable and expects the
// error rather than a silent skip.
func TestRun_WriteFailureIsReported(t *testing.T) {
	root := fixture(t, "2.2.0", map[string]string{
		"site/src/content/docs/a.mdx": "Pin %%VERSION%%.",
		"docs/a.md":                   "Pin 2.1.0.",
	})
	path := filepath.Join(root, "docs", "a.md")
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		_ = f.Close()
		t.Skip("the file stays writable here (running as root)")
	}
	if err := run([]string{"-root", root}, &bytes.Buffer{}); err == nil {
		t.Error("run succeeded writing a read-only page")
	}
}
