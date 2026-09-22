package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFiguresFor_DrawsWhatTheRecordSupports verifies a matrix-only run gets the
// one picture it has numbers for, and a run with a series gets all three.
//
// The alternative is a figure drawn from nothing, which is an empty panel with a
// title on it — a picture that says a measurement was taken when none was.
func TestFiguresFor_DrawsWhatTheRecordSupports(t *testing.T) {
	t.Run("a matrix-only run", func(t *testing.T) {
		got := figuresFor(fixtureRun())
		if len(got) != 1 || got[0].Name != "memory-by-scenario" {
			t.Errorf("figuresFor() = %v, want the memory figure alone", names(got))
		}
	})

	t.Run("a run with a series", func(t *testing.T) {
		run := fixtureRun()
		run.Series = []SeriesScenario{fixtureSeries()}
		if got := names(figuresFor(run)); len(got) != 3 {
			t.Errorf("figuresFor() = %v, want three figures", got)
		}
	})

	t.Run("a series of one step fixes no line", func(t *testing.T) {
		run := fixtureRun()
		short := fixtureSeries()
		short.Steps = short.Steps[:1]
		run.Series = []SeriesScenario{short}
		if got := names(figuresFor(run)); len(got) != 1 {
			t.Errorf("figuresFor() = %v, want no series figures from a single step", got)
		}
	})
}

// names lists the figures a record produced.
func names(figures []figure) []string {
	out := make([]string, len(figures))
	for i, f := range figures {
		out[i] = f.Name
	}
	return out
}

// TestMemoryByClients_DropsTheHeldLineWhenNobodyTookIt verifies the third line
// is absent rather than flat at zero, which would read as a measurement of zero.
func TestMemoryByClients_DropsTheHeldLineWhenNobodyTookIt(t *testing.T) {
	full := fixtureSeries()
	with := memoryByClients(&full)
	if len(with.Chart.Series) != 3 {
		t.Errorf("a series with settled readings drew %d lines, want three", len(with.Chart.Series))
	}

	bare := fixtureSeries()
	for i := range bare.Steps {
		bare.Steps[i].SettledHeapMiB, bare.Steps[i].SettledRSSMiB = 0, 0
	}
	if got := memoryByClients(&bare); len(got.Chart.Series) != 2 {
		t.Errorf("a series with no settled readings drew %d lines, want two", len(got.Chart.Series))
	}
}

// TestWriteFigures_WritesThePairToBothTrees verifies every figure lands twice
// per scheme: once for the repository page and once for the site.
func TestWriteFigures_WritesThePairToBothTrees(t *testing.T) {
	dest := tempDestinations(t)
	run := fixtureRun()
	run.Series = []SeriesScenario{fixtureSeries()}

	if err := writeFigures(run, dest, false); err != nil {
		t.Fatalf("writeFigures: %v", err)
	}
	for _, dir := range []string{dest.DocCharts, dest.SiteCharts} {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("read %s: %v", dir, err)
			}
			if len(entries) != 6 {
				t.Errorf("wrote %d files, want three figures in two schemes", len(entries))
			}
			body, err := os.ReadFile(filepath.Join(dir, "memory-by-clients-dark.svg")) // #nosec G304 -- just written
			if err != nil {
				t.Fatalf("read a figure: %v", err)
			}
			if !strings.HasPrefix(string(body), "<svg ") {
				t.Error("the file written is not an SVG document")
			}
		})
	}

	t.Run("drawing twice changes nothing", func(t *testing.T) {
		if err := writeFigures(run, dest, true); err != nil {
			t.Errorf("a second pass reported a difference: %v", err)
		}
	})

	t.Run("a stylesheet it cannot read", func(t *testing.T) {
		broken := dest
		broken.ThemeSheet = filepath.Join(t.TempDir(), "absent.css")
		if err := writeFigures(run, broken, false); err == nil {
			t.Error("expected an error with no palette to paint from")
		}
	})
}
