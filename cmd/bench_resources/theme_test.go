package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPalettesFrom_ReadsTheSitesOwnTokens verifies the charts are painted from
// the stylesheet the pages beside them are.
//
// A palette written twice drifts, and a chart that has drifted reads as a
// foreign object on the page — which is worse than a chart that is plain. This
// reads the real sheet rather than a fixture, so a token renamed there fails
// here rather than on the day somebody looks at a figure.
func TestPalettesFrom_ReadsTheSitesOwnTokens(t *testing.T) {
	schemes, err := palettesFrom(filepath.Join("../..", themeSheet))
	if err != nil {
		t.Fatalf("palettesFrom: %v", err)
	}
	if len(schemes) != 2 {
		t.Fatalf("read %d schemes, want the light and dark pair", len(schemes))
	}

	seen := map[string]bool{}
	for _, p := range schemes {
		t.Run(p.Scheme, func(t *testing.T) {
			seen[p.Scheme] = true
			for name, value := range map[string]string{
				"Page": p.Page, "Plot": p.Plot, "Grid": p.Grid, "Text": p.Text, "Muted": p.Muted,
			} {
				t.Run(name, func(t *testing.T) {
					if !strings.HasPrefix(value, "#") {
						t.Errorf("%s = %q, which is not a color", name, value)
					}
				})
			}
			if len(p.Series) != 3 {
				t.Errorf("Series = %v, want three hues a chart can tell apart", p.Series)
			}
			if p.Page == p.Plot {
				t.Error("the panel is the same color as the page, so the plot has no edge")
			}
		})
	}
	if !seen[schemeLight] || !seen[schemeDark] {
		t.Errorf("read %v, want both schemes", seen)
	}
}

// TestPalettesFrom_RefusesASheetItCannotRead verifies every way the stylesheet
// can stop answering is an error rather than a color it guessed. A chart painted
// in a fallback is a chart nobody notices is wrong.
func TestPalettesFrom_RefusesASheetItCannotRead(t *testing.T) {
	testCases := []struct {
		name, sheet, want string
	}{
		{name: "no dark block", sheet: "body { color: red; }\n", want: ":root,"},
		{
			name:  "a block that is never closed",
			sheet: ":root,\n::backdrop {\n\t--sl-color-black: #000;",
			want:  "never closed",
		},
		{
			name: "a missing token",
			sheet: ":root,\n::backdrop {\n\t--sl-color-black: #000;\n}\n" +
				":root[data-theme=\"light\"],\n[data-theme=\"light\"] ::backdrop {\n\t--sl-color-black: #fff;\n}\n",
			want: "no --lgm-surface",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "theme.css")
			if err := os.WriteFile(path, []byte(tc.sheet), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			_, err := palettesFrom(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("palettesFrom() error = %v, want it to say %q", err, tc.want)
			}
		})
	}

	t.Run("a sheet that is not there", func(t *testing.T) {
		if _, err := palettesFrom(filepath.Join(t.TempDir(), "absent.css")); err == nil {
			t.Error("expected a read error")
		}
	})
}

// TestResolve_FollowsOneVarAndNoFurther verifies the reference this sheet
// actually uses is followed, and that anything it cannot resolve is an error.
func TestResolve_FollowsOneVarAndNoFurther(t *testing.T) {
	tokens := map[string]string{
		"--plain":    "#123456",
		"--indirect": "var(--plain)",
		"--dangling": "var(--nothing)",
		"--nested":   "var(--indirect)",
		"--words":    "inherit",
	}

	testCases := []struct {
		name, token, want, wantErr string
	}{
		{name: "a plain color", token: "--plain", want: "#123456"},
		{name: "one reference", token: "--indirect", want: "#123456"},
		{name: "a reference to nothing", token: "--dangling", wantErr: "--nothing"},
		{name: "two references deep", token: "--nested", wantErr: "not a color"},
		{name: "a token that is not there", token: "--absent", wantErr: "no --absent"},
		{name: "a keyword rather than a color", token: "--words", wantErr: "not a color"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolve(tokens, tc.token)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("resolve() = %q, %v; want an error saying %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got != tc.want {
				t.Errorf("resolve() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTokenValue_IgnoresAPropertyInsideAComment verifies the reader is anchored
// to a declaration rather than matching the many custom properties this sheet
// mentions in prose.
func TestTokenValue_IgnoresAPropertyInsideAComment(t *testing.T) {
	const sheet = "/* --sl-color-bg IS the ground; do not use --lgm-surface here */\n\t--lgm-surface: #161b22;\n"
	matches := tokenValue.FindAllStringSubmatch(sheet, -1)
	if len(matches) != 1 || matches[0][1] != "--lgm-surface" {
		t.Errorf("read %v, want only the declaration", matches)
	}
}
