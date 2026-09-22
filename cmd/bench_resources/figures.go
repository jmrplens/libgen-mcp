// figures.go decides which pictures the record is worth drawing as, and where
// they are written.
//
// Three, and each one answers a question somebody asked out loud while sizing a
// deployment: what does it weigh, what does another caller add, and what does it
// feel like to use when there are a lot of them. A fourth figure nobody can name
// the question for is a picture that will be scrolled past.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jmrplens/libgen-mcp/v2/cmd/internal/docgen"
)

// Where the rendered pairs are written. The site copy is under public/ because
// the page references it as an ordinary asset rather than importing it, which
// is what lets the picture element switch schemes without JavaScript.
const (
	docChartsDir  = "docs/benchmarks"
	siteChartsDir = "site/public/benchmarks"
)

// figure is one chart under the name its files are written as.
type figure struct {
	Name  string
	Chart chart
}

// figuresFor is every picture a record supports. A run with no series gets the
// first one only, which is what a matrix-only run should publish.
func figuresFor(run *Run) []figure {
	out := []figure{memoryByScenario(run)}
	if len(run.Series) > 0 && len(run.Series[0].Steps) > 1 {
		out = append(out, memoryByClients(&run.Series[0]), latencyByClients(&run.Series[0]))
	}
	return out
}

// memoryByScenario is what the process weighs, per point of the matrix.
func memoryByScenario(run *Run) figure {
	labels := make([]string, 0, len(run.Scenarios))
	idle := make([]float64, 0, len(run.Scenarios))
	peak := make([]float64, 0, len(run.Scenarios))
	for _, s := range run.Scenarios {
		labels = append(labels, s.ID)
		idle = append(idle, s.Memory.IdleMiB)
		peak = append(peak, s.Memory.PeakMiB)
	}
	return figure{
		Name: "memory-by-scenario",
		Chart: chart{
			Title:    "What the process weighs",
			Subtitle: "Resident set, read from the kernel. A container limit is set against the peak.",
			XLabels:  labels,
			YTitle:   "MiB",
			Bars:     true,
			Series: []series{
				{Label: "idle", Values: idle},
				{Label: "peak under load", Values: peak},
			},
		},
	}
}

// memoryByClients is what another caller adds, over the series.
func memoryByClients(s *SeriesScenario) figure {
	labels, mean, peak, held := seriesAxes(s)
	lines := []series{
		{Label: "mean under load", Values: mean},
		{Label: "peak under load", Values: peak},
	}
	// Only when every step has one. A table can print an absence; a line cannot,
	// and a step whose heap reading failed would be plotted at zero as a dip
	// nothing measured.
	if s.hasHeldHeap() {
		lines = append(lines, series{Label: "held, load stopped", Values: held})
	}
	return figure{
		Name: "memory-by-clients",
		Chart: chart{
			Title:    "What each extra caller costs",
			Subtitle: "One process, more client addresses at each step. Held is the live heap with a collection forced.",
			XLabels:  labels,
			// The steps are evenly spaced rather than placed to scale: the
			// ladder doubles, and a linear axis would put everything below a
			// hundred in the first inch. The published per-caller figure is the
			// fit through the real counts, so the slope a reader needs is a
			// number rather than something to measure off this picture.
			XTitle: "client addresses (steps evenly spaced)",
			YTitle: "MiB",
			Series: lines,
		},
	}
}

// latencyByClients is what the server feels like as the callers pile up.
func latencyByClients(s *SeriesScenario) figure {
	labels := make([]string, 0, len(s.Steps))
	p50 := make([]float64, 0, len(s.Steps))
	p99 := make([]float64, 0, len(s.Steps))
	for _, step := range s.Steps {
		labels = append(labels, strconv.Itoa(step.Clients))
		p50 = append(p50, step.P50Ms)
		p99 = append(p99, step.P99Ms)
	}
	return figure{
		Name: "latency-by-clients",
		Chart: chart{
			Title:    "What it feels like as the callers pile up",
			Subtitle: s.Method + ", nearest-rank percentiles over each step's steady phase.",
			XLabels:  labels,
			XTitle:   "client addresses (steps evenly spaced)",
			YTitle:   "ms",
			Series: []series{
				{Label: "p50", Values: p50},
				{Label: "p99", Values: p99},
			},
		},
	}
}

// seriesAxes pulls the three memory readings and the labels out of a series.
func seriesAxes(s *SeriesScenario) (labels []string, mean, peak, held []float64) {
	for _, step := range s.Steps {
		labels = append(labels, strconv.Itoa(step.Clients))
		mean = append(mean, step.RSSMeanMiB)
		peak = append(peak, step.RSSPeakMiB)
		held = append(held, step.SettledHeapMiB)
	}
	return labels, mean, peak, held
}

// writeFigures draws every figure in both schemes and writes the pair to both
// directories, or reports which file differs when check is set.
func writeFigures(run *Run, dest destinations, check bool) error {
	schemes, err := palettesFrom(dest.ThemeSheet)
	if err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, fig := range figuresFor(run) {
		for _, p := range schemes {
			body := []byte(fig.Chart.svg(p))
			name := fig.Name + "-" + p.Scheme + ".svg"
			expected[name] = true
			for _, dir := range []string{dest.DocCharts, dest.SiteCharts} {
				if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
					return fmt.Errorf("create %s: %w", dir, mkErr)
				}
				path := filepath.Join(dir, name)
				if wErr := docgen.WriteOrCheck(path, body, check, "`make bench-resources-render`"); wErr != nil {
					return wErr
				}
			}
		}
	}
	for _, dir := range []string{dest.DocCharts, dest.SiteCharts} {
		if pErr := pruneFigures(dir, expected, check); pErr != nil {
			return pErr
		}
	}
	return nil
}

// pruneFigures removes a figure the current record does not produce, or reports
// it when check is set.
//
// Without this a record that loses its series keeps publishing the series
// figures its last run drew, and the check passes: every file it knows about
// matches, and the ones it no longer knows about are never looked at. A stale
// picture is worse than a missing one, because nothing about it says it is
// stale.
func pruneFigures(dir string, expected map[string]bool, check bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", dir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".svg") || expected[name] {
			continue
		}
		path := filepath.Join(dir, name)
		if check {
			return fmt.Errorf("%s is not drawn from the current record; run `make bench-resources-render`", path)
		}
		if rErr := os.Remove(path); rErr != nil {
			return fmt.Errorf("remove %s: %w", path, rErr)
		}
	}
	return nil
}
