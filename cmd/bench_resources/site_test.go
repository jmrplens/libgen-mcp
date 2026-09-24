package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tempDestinations points every artifact at a tree of this test's own, seeded
// with the real stylesheet and the real page templates.
//
// The real files rather than fixtures, because what is under test is the
// drawing of the pages this repository actually publishes: a fixture page with
// tidy markers would pass while the committed one had lost a region, which is
// the only way this can go wrong without anybody noticing.
func tempDestinations(t *testing.T) destinations {
	t.Helper()
	root := t.TempDir()
	dest := destinations{
		Page:       filepath.Join(root, "page.md"),
		DocCharts:  filepath.Join(root, "docs-charts"),
		SiteCharts: filepath.Join(root, "site-charts"),
		SitePageEN: filepath.Join(root, "benchmarks.mdx"),
		SitePageES: filepath.Join(root, "es-benchmarks.mdx"),
		ThemeSheet: filepath.Join(root, "theme.css"),
	}
	// The command's own paths are relative to the module root, and a test runs
	// in its package directory.
	const moduleRoot = "../.."
	for _, copy := range []struct{ from, to string }{
		{filepath.Join(moduleRoot, themeSheet), dest.ThemeSheet},
		{filepath.Join(moduleRoot, sitePageEN), dest.SitePageEN},
		{filepath.Join(moduleRoot, sitePageES), dest.SitePageES},
	} {
		body, err := os.ReadFile(copy.from) // #nosec G304 -- paths this command owns
		if err != nil {
			t.Fatalf("seed %s: %v", copy.from, err)
		}
		if wErr := os.WriteFile(copy.to, body, 0o600); wErr != nil {
			t.Fatalf("seed %s: %v", copy.to, wErr)
		}
	}
	return dest
}

// benchDest is tempDestinations with the Markdown page pointed somewhere the
// caller already chose, for a test about the page rather than about the tree.
func benchDest(t *testing.T, page string) destinations {
	t.Helper()
	dest := tempDestinations(t)
	dest.Page = page
	return dest
}

// TestWriteSitePages_FillsEveryRegionInBothLanguages verifies the generated
// blocks reach both published pages, translated, and that nothing outside a
// region is touched.
func TestWriteSitePages_FillsEveryRegionInBothLanguages(t *testing.T) {
	dest := tempDestinations(t)
	run := fixtureRun()
	run.Series = []SeriesScenario{fixtureSeries()}

	if err := writeSitePages(run, dest, false); err != nil {
		t.Fatalf("writeSitePages: %v", err)
	}

	english := readPage(t, dest.SitePageEN)
	spanish := readPage(t, dest.SitePageES)

	for _, want := range []string{
		"Measured on Test CPU", "per caller while every caller is working",
		"At 100 client addresses", "benchmarks/memory-by-scenario-dark.svg", "| Scenario ",
	} {
		t.Run("english/"+want, func(t *testing.T) {
			if !strings.Contains(english, want) {
				t.Errorf("the English page does not carry %q", want)
			}
		})
	}
	for _, want := range []string{
		"Medido en Test CPU", "por cliente mientras todos llaman",
		"Con 100 direcciones de cliente", "| Escenario ",
	} {
		t.Run("spanish/"+want, func(t *testing.T) {
			if !strings.Contains(spanish, want) {
				t.Errorf("the Spanish page does not carry %q", want)
			}
		})
	}

	t.Run("the hand-written prose is left alone", func(t *testing.T) {
		if !strings.Contains(english, "## Reading these numbers") {
			t.Error("the English page lost a section the generator does not own")
		}
		if !strings.Contains(spanish, "## Cómo leer estos números") {
			t.Error("the Spanish page lost a section the generator does not own")
		}
	})

	t.Run("writing twice changes nothing", func(t *testing.T) {
		if err := writeSitePages(run, dest, true); err != nil {
			t.Errorf("a second pass reported a difference: %v", err)
		}
	})
}

// TestWriteSitePages_ReplacesAStaleBlockWithAnAbsence verifies a run that
// measured no series says so rather than leaving the last run's numbers.
//
// Leaving them was the first arrangement, on the reasoning that a matrix-only
// run should not erase a section. What it actually leaves is an older record's
// measurement on a page the gate then declares current, which is the one thing a
// generated document must not do.
func TestWriteSitePages_ReplacesAStaleBlockWithAnAbsence(t *testing.T) {
	dest := tempDestinations(t)
	full := fixtureRun()
	full.Series = []SeriesScenario{fixtureSeries()}
	if err := writeSitePages(full, dest, false); err != nil {
		t.Fatalf("writeSitePages: %v", err)
	}

	if err := writeSitePages(fixtureRun(), dest, false); err != nil {
		t.Fatalf("writeSitePages without a series: %v", err)
	}
	english := readPage(t, dest.SitePageEN)
	if strings.Contains(english, "per caller while every caller is working") {
		t.Error("the page still carries a measurement the current record does not have")
	}
	if !strings.Contains(english, enLabels.NotMeasured) {
		t.Error("the page does not say the run measured nothing for that section")
	}
	if !strings.Contains(readPage(t, dest.SitePageES), esLabels.NotMeasured) {
		t.Error("the Spanish page does not say it either")
	}
}

// TestApplySiteRegions_ReportsAPageItCannotFill verifies the two ways a page can
// stop being fillable, which is what happens when somebody edits a marker.
func TestApplySiteRegions_ReportsAPageItCannotFill(t *testing.T) {
	testCases := []struct {
		name, page, want string
	}{
		{name: "no marker", page: "# A page\n", want: "missing region marker"},
		{
			name: "a marker with no end",
			page: "{/* generated:host — run `make bench-resources-render`, do not edit by hand */}\n",
			want: "never closed",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "page.mdx")
			if err := os.WriteFile(path, []byte(tc.page), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			err := applySiteRegions(path, map[string]string{"host": "a block"}, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("applySiteRegions() error = %v, want it to say %q", err, tc.want)
			}
		})
	}

	t.Run("a page that is not there", func(t *testing.T) {
		err := applySiteRegions(filepath.Join(t.TempDir(), "absent.mdx"), map[string]string{"host": "x"}, false)
		if err == nil {
			t.Error("expected a read error")
		}
	})
}

// TestFigureBlock_SwitchesSchemesWithoutJavaScript verifies the picture element
// the pages carry: the dark file behind the media query, the light one as the
// img's own source, so a browser that ignores the query still gets a figure.
func TestFigureBlock_SwitchesSchemesWithoutJavaScript(t *testing.T) {
	got := figureBlock("memory-by-clients", `a "quoted" label`)
	for _, want := range []string{
		"memory-by-clients-dark.svg", "memory-by-clients-light.svg",
		"(prefers-color-scheme: dark)", "&quot;quoted&quot;", `loading="lazy"`,
		// Sized from the chart's own geometry, so the page reserves the box.
		`width="720" height="380"`,
	} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(got, want) {
				t.Errorf("figureBlock() does not carry %q:\n%s", want, got)
			}
		})
	}
}

// TestSeriesSummary_WithholdsAFigureItCouldNotMeasure verifies both languages
// say so rather than publishing a negative per-caller cost.
func TestSeriesSummary_WithholdsAFigureItCouldNotMeasure(t *testing.T) {
	noisy := fixtureSeries()
	noisy.Steps = []SeriesStep{
		{Clients: 1, RSSPeakMiB: 40, SettledHeapMiB: 11},
		{Clients: 10, RSSPeakMiB: 30, SettledHeapMiB: 10},
	}
	if got := seriesSummary(&noisy, enLabels); !strings.Contains(got, "at or below zero") {
		t.Errorf("the English summary = %q, want it withheld", got)
	}
	if got := seriesSummary(&noisy, esLabels); !strings.Contains(got, "en cero o por debajo") {
		t.Errorf("the Spanish summary = %q, want it withheld", got)
	}
}

// readPage reads a generated page back.
func readPage(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path) // #nosec G304 -- a path this test wrote
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}
