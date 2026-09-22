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

// TestWriteFigures_RemovesAFigureTheRecordNoLongerDraws verifies the pruning
// that keeps a stale picture off the page.
//
// Without it a record that loses its series keeps publishing the series figures
// its last run drew, and the check passes: every file it knows about matches,
// and the ones it no longer knows about are never looked at. A stale picture is
// worse than a missing one, because nothing about it says it is stale.
func TestWriteFigures_RemovesAFigureTheRecordNoLongerDraws(t *testing.T) {
	dest := tempDestinations(t)
	full := fixtureRun()
	full.Series = []SeriesScenario{fixtureSeries()}
	if err := writeFigures(full, dest, false); err != nil {
		t.Fatalf("writeFigures: %v", err)
	}

	if err := writeFigures(fixtureRun(), dest, false); err != nil {
		t.Fatalf("writeFigures without a series: %v", err)
	}
	for _, dir := range []string{dest.DocCharts, dest.SiteCharts} {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("read %s: %v", dir, err)
			}
			if len(entries) != 2 {
				t.Errorf("%d files remain, want the one figure a matrix-only record draws, in two schemes", len(entries))
			}
			for _, entry := range entries {
				if strings.Contains(entry.Name(), "by-clients") {
					t.Errorf("%s is still published although the record no longer draws it", entry.Name())
				}
			}
		})
	}
}

// TestPruneFigures_ReportsALeftoverRatherThanRemovingItUnderCheck verifies the
// gate says what is stale instead of quietly tidying the working tree.
func TestPruneFigures_ReportsALeftoverRatherThanRemovingItUnderCheck(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "memory-by-clients-dark.svg")
	if err := os.WriteFile(stale, []byte("<svg/>"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	keep := filepath.Join(dir, "keep-light.svg")
	if err := os.WriteFile(keep, []byte("<svg/>"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// A file that is not a figure at all, which must be left alone either way.
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	expected := map[string]bool{"keep-light.svg": true}

	t.Run("check reports it", func(t *testing.T) {
		err := pruneFigures(dir, expected, true)
		if err == nil || !strings.Contains(err.Error(), "memory-by-clients-dark.svg") {
			t.Errorf("pruneFigures() error = %v, want it to name the stale figure", err)
		}
		if _, sErr := os.Stat(stale); sErr != nil {
			t.Error("the check removed the file rather than reporting it")
		}
	})

	t.Run("a render removes it", func(t *testing.T) {
		if err := pruneFigures(dir, expected, false); err != nil {
			t.Fatalf("pruneFigures: %v", err)
		}
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Error("the stale figure is still there")
		}
		for _, kept := range []string{keep, other} {
			t.Run(filepath.Base(kept), func(t *testing.T) {
				if _, err := os.Stat(kept); err != nil {
					t.Errorf("%s was removed and should not have been", filepath.Base(kept))
				}
			})
		}
	})

	t.Run("a directory that is not there", func(t *testing.T) {
		if err := pruneFigures(filepath.Join(t.TempDir(), "absent"), expected, false); err != nil {
			t.Errorf("pruneFigures() = %v, want nothing to do", err)
		}
	})
}

// TestHasHeldHeap_AsksWhetherALineCanBeDrawn verifies the check the held series
// is gated on: every step, not any.
//
// A table can print an absence; a line cannot, and a step whose heap reading
// failed would be plotted at zero as a dip nothing measured.
func TestHasHeldHeap_AsksWhetherALineCanBeDrawn(t *testing.T) {
	testCases := []struct {
		name  string
		steps []SeriesStep
		want  bool
	}{
		{
			name: "every step has one",
			steps: []SeriesStep{
				{Clients: 1, SettledHeapMiB: 2}, {Clients: 2, SettledHeapMiB: 3},
			},
			want: true,
		},
		{
			name: "one step's heap read failed",
			steps: []SeriesStep{
				{Clients: 1, SettledHeapMiB: 2}, {Clients: 2, SettledRSSMiB: 30},
			},
		},
		{name: "no steps at all"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := &SeriesScenario{Steps: tc.steps}
			if got := s.hasHeldHeap(); got != tc.want {
				t.Errorf("hasHeldHeap() = %t, want %t", got, tc.want)
			}
		})
	}

	t.Run("the figure drops the line rather than plotting a zero", func(t *testing.T) {
		partial := fixtureSeries()
		partial.Steps[1].SettledHeapMiB = 0
		if got := memoryByClients(&partial); len(got.Chart.Series) != 2 {
			t.Errorf("drew %d lines, want the held line dropped", len(got.Chart.Series))
		}
	})
}
