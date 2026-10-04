// Tests for the docs/ release-number generator.

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture writes a minimal repository: VERSION, the files map relative to the
// root, and returns the root.
func fixture(t *testing.T, version string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if version != "" {
		files["VERSION"] = version + "\n"
	}
	for rel, body := range files {
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
)

// TestRun_RewritesOnlyTheCurrentReleaseSpots drives a version bump end to end:
// the token positions move to the new release, the historical mentions stay,
// the Spanish twin is ignored and a page without the token is left alone.
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
	if !strings.Contains(out.String(), "docs/install/npm.md: 2 mention(s) set to 2.2.0") {
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

// TestRun_CheckReportsEveryStaleSpot asserts check mode names each docs line
// that still carries the previous release and writes nothing.
func TestRun_CheckReportsEveryStaleSpot(t *testing.T) {
	root := fixture(t, "2.2.0", map[string]string{
		"site/src/content/docs/install/npm.mdx": twinPage,
		"docs/install/npm.md":                   docPage,
	})
	err := run([]string{"-root", root, "-check"}, &bytes.Buffer{})
	if !errors.Is(err, errStale) {
		t.Fatalf("err = %v, want errStale", err)
	}
	for _, want := range []string{"docs/install/npm.md:4 names 2.1.0", "docs/install/npm.md:7 names 2.1.0", "VERSION is 2.2.0"} {
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
			name:    "no Starlight pages",
			version: "1.0.0",
			files:   map[string]string{},
			want:    "walk site/src/content/docs",
		},
		{
			name:    "a token page with no docs twin",
			version: "1.0.0",
			files:   map[string]string{"site/src/content/docs/lonely.mdx": "%%VERSION%%"},
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
			want: "docs/ has nothing (the page has no more versions)",
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

// TestMentions_Boundaries pins what counts as a version: a leading v is
// allowed, a longer number or identifier is not, and a sentence's full stop is
// not part of it.
func TestMentions_Boundaries(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{name: "plain and v-prefixed", src: "2.1.0 and v2.1.0", want: []string{"2.1.0", "2.1.0"}},
		{name: "end of sentence", src: "for 2.1.0. Next", want: []string{"2.1.0"}},
		{name: "prerelease", src: "tag 2.2.0-rc.1 now", want: []string{"2.2.0-rc.1"}},
		{name: "an address", src: "127.0.0.1 and 10.0.0.0/8", want: nil},
		{name: "a toolchain", src: "go1.27.0", want: nil},
		{name: "four numeric fields", src: "1.2.3.4", want: nil},
		{name: "after a dot", src: "x.2.1.0", want: nil},
		{name: "end of input", src: "v2.1.0", want: []string{"2.1.0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, m := range mentions(tc.src, false) {
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
	got := mentions("a %%VERSION%% b 1.0.0 c %%VERSION%%", true)
	var kinds []string
	for _, m := range got {
		kinds = append(kinds, m.text)
	}
	if want := "%%VERSION%%,1.0.0,%%VERSION%%"; strings.Join(kinds, ",") != want {
		t.Errorf("mentions = %v, want %s", kinds, want)
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
