// theme.go reads the chart palette out of the documentation site's stylesheet.
//
// The charts are published beside the site's own prose, so they are painted
// with the site's own tokens rather than with colors restated here. A palette
// written twice drifts, and a chart that has drifted reads as a foreign object
// on the page — which is worse than a chart that is plain.
//
// Only one color is stated here rather than read, and it carries the reason it
// has to be: the palette has a single accent, and a chart with three lines on it
// needs two more hues that are not it.

package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Scheme names, which double as the file-name suffix of the rendered pair.
const (
	schemeLight = "light"
	schemeDark  = "dark"
)

// themeSheet is where the tokens are read from.
const themeSheet = "site/src/styles/theme.css"

// palette is what a chart paints with.
type palette struct {
	Scheme string
	// Page is the figure's ground and Plot the panel the data sits on, so a
	// chart dropped on the site's page looks like part of it.
	Page  string
	Plot  string
	Grid  string
	Text  string
	Muted string
	// Series are the line colors, in the order a chart always lists them.
	Series []string
}

// extraSeriesHues are the two line colors the site palette does not have.
//
// The palette carries one accent, and a chart with three lines needs three
// hues that can be told apart. These two are a blue and a green rather than
// another warm tone, because the accent is amber and a second amber line reads
// as the same measurement at a different opacity.
//
// Each is stated per scheme at the lightness its own ground needs: measured
// against the plot panels below, every one of them clears 4.5:1, which is the
// floor the site's own contrast check holds its CSS to.
var extraSeriesHues = map[string][]string{
	schemeDark:  {"#58a6ff", "#3fb950"},
	schemeLight: {"#0a58ca", "#1a7f37"},
}

// tokenValue matches one custom property's declaration.
//
// It is anchored to the start of a line so a property mentioned inside a
// comment, of which this stylesheet has many, is never read as a declaration.
var tokenValue = regexp.MustCompile(`(?m)^\s*(--[a-z0-9-]+):\s*([^;]+);`)

// palettesFrom reads both schemes out of the stylesheet.
//
// The two blocks are found by their selectors rather than by position: the dark
// one is the bare :root and the light one is the [data-theme="light"] override,
// which is the arrangement the sheet itself explains at the top.
func palettesFrom(path string) ([]palette, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- a path this command owns
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	text := string(body)

	dark, err := paletteIn(text, schemeDark, `:root,`)
	if err != nil {
		return nil, err
	}
	light, err := paletteIn(text, schemeLight, `:root[data-theme="light"],`)
	if err != nil {
		return nil, err
	}
	return []palette{light, dark}, nil
}

// paletteIn reads one scheme's block, which runs from its selector to the first
// closing brace after it.
func paletteIn(text, scheme, selector string) (palette, error) {
	start := strings.Index(text, selector)
	if start < 0 {
		return palette{}, fmt.Errorf("%s has no %s block", themeSheet, selector)
	}
	end := strings.Index(text[start:], "\n}")
	if end < 0 {
		return palette{}, fmt.Errorf("%s: the %s block is never closed", themeSheet, selector)
	}

	tokens := map[string]string{}
	for _, match := range tokenValue.FindAllStringSubmatch(text[start:start+end], -1) {
		tokens[match[1]] = strings.TrimSpace(match[2])
	}

	out := palette{Scheme: scheme}
	for _, want := range []struct {
		token string
		into  *string
	}{
		{"--sl-color-black", &out.Page},
		{"--lgm-surface", &out.Plot},
		{"--lgm-border", &out.Grid},
		{"--sl-color-gray-1", &out.Text},
		{"--sl-color-gray-3", &out.Muted},
	} {
		value, err := resolve(tokens, want.token)
		if err != nil {
			return palette{}, err
		}
		*want.into = value
	}
	accent, err := resolve(tokens, "--sl-color-accent")
	if err != nil {
		return palette{}, err
	}
	out.Series = append([]string{accent}, extraSeriesHues[scheme]...)
	return out, nil
}

// resolve reads one token, following a var() reference to the value behind it.
//
// One level is enough for this sheet and is deliberately not more: a chain the
// reader cannot follow is an error rather than a color it guessed, because a
// chart painted in a fallback would be a chart nobody notices is wrong.
func resolve(tokens map[string]string, name string) (string, error) {
	value, ok := tokens[name]
	if !ok {
		return "", fmt.Errorf("%s: no %s in this block", themeSheet, name)
	}
	if inner, found := strings.CutPrefix(value, "var("); found {
		referenced := strings.TrimSuffix(strings.TrimSpace(inner), ")")
		if value, ok = tokens[referenced]; !ok {
			return "", fmt.Errorf("%s: %s points at %s, which this block does not define", themeSheet, name, referenced)
		}
	}
	if !strings.HasPrefix(value, "#") {
		return "", fmt.Errorf("%s: %s is %q, which is not a color this can paint with", themeSheet, name, value)
	}
	return value, nil
}
