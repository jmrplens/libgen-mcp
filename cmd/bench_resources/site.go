// site.go fills the generated regions of the two published pages.
//
// The pages themselves are written by hand — the prose around a number is an
// editorial decision and belongs to whoever writes it — and everything that
// carries a measurement is generated into a marked region. That is the same
// arrangement the evaluator pages use, for the same reason: a number typed into
// prose stops being true on the next run and says nothing about it.

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/jmrplens/libgen-mcp/cmd/internal/docgen"
)

// The two pages and the markers that open each generated region.
const (
	sitePageEN = "site/src/content/docs/benchmarks.mdx"
	sitePageES = "site/src/content/docs/es/benchmarks.mdx"
	regionEnd  = "{/* end generated */}"
)

// regionNames are the blocks each page carries, in the order they appear.
var regionNames = []string{
	"host", "memory-figure", "memory-table",
	"series-figure", "series-summary",
	"latency-figure", "latency-summary",
}

// writeSitePages rewrites both pages' generated regions, or reports the first
// one that differs.
func writeSitePages(run *Run, dest destinations, check bool) error {
	for _, page := range []struct {
		path   string
		blocks map[string]string
	}{
		{dest.SitePageEN, siteBlocksEN(run)},
		{dest.SitePageES, siteBlocksES(run)},
	} {
		if err := applySiteRegions(page.path, page.blocks, check); err != nil {
			return err
		}
	}
	return nil
}

// applySiteRegions replaces every generated region of one page.
func applySiteRegions(path string, blocks map[string]string, check bool) error {
	original, err := os.ReadFile(path) // #nosec G304 -- a path this command owns
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	updated := string(original)
	for _, name := range regionNames {
		body, ok := blocks[name]
		// A region with nothing to put in it is left as it stands rather than
		// blanked: a run that measured no series should not erase the section
		// its last run filled, it should leave the page saying what it said.
		if !ok || body == "" {
			continue
		}
		if updated, err = replaceRegion(updated, name, body); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	if updated == string(original) {
		return nil
	}
	return docgen.WriteOrCheck(path, []byte(updated), check, "`make bench-resources-render`")
}

// replaceRegion swaps what sits between a region's marker and the next end
// marker.
func replaceRegion(page, name, body string) (string, error) {
	begin := fmt.Sprintf("{/* generated:%s — run `make bench-resources-render`, do not edit by hand */}", name)
	start := strings.Index(page, begin)
	if start < 0 {
		return "", fmt.Errorf("missing region marker %q", name)
	}
	rest := page[start+len(begin):]
	end := strings.Index(rest, regionEnd)
	if end < 0 {
		return "", fmt.Errorf("region %q is never closed by %q", name, regionEnd)
	}
	// Prettier formats these files and pads a table's cells to align them, which
	// would leave the two tools rewriting each other forever. The generated
	// block is the generator's to own, so Prettier is told to leave it alone.
	//
	// The body's own trailing newline is trimmed before the separator is added.
	// It is not cosmetic: a table renderer ends its output with one, and the two
	// together made a second blank line that Prettier collapsed on sight — which
	// is the two tools rewriting each other by a different route.
	return page[:start+len(begin)] + "\n\n{/* prettier-ignore */}\n" +
		strings.TrimRight(body, "\n") + "\n\n" + rest[end:], nil
}

// figureBlock is a picture with its light and dark pair, as the picture element
// that switches between them without JavaScript.
//
// The dark file is the source guarded by the media query and the light one is
// the img's own src, so a reader with no preference and a browser that ignores
// the query still gets a figure rather than nothing.
func figureBlock(name, alt string) string {
	return fmt.Sprintf(`<figure>
  <picture>
    <source srcset="/libgen-mcp/benchmarks/%s-dark.svg" media="(prefers-color-scheme: dark)" />
    <img src="/libgen-mcp/benchmarks/%s-light.svg" alt="%s" loading="lazy" />
  </picture>
</figure>`, name, name, escapeXML(alt))
}

// siteBlocksEN is every generated region of the English page.
func siteBlocksEN(run *Run) map[string]string {
	return siteBlocks(run, enLabels, renderMemoryTableEN(run))
}

// siteBlocksES is every generated region of the Spanish page.
func siteBlocksES(run *Run) map[string]string {
	return siteBlocks(run, esLabels, renderMemoryTableES(run))
}

// siteBlocks is the two pages' shared shape.
//
// One function rather than two, because the pages carry the same blocks and
// differ only in the language of their prose and the headers of their one table.
// Two copies is how a block added to one page goes missing from the other.
func siteBlocks(run *Run, l labels, memoryTable string) map[string]string {
	blocks := map[string]string{
		"host":          hostBlock(run, l),
		"memory-figure": figureBlock("memory-by-scenario", l.MemoryAlt),
		"memory-table":  memoryTable,
	}
	if len(run.Series) > 0 && len(run.Series[0].Steps) > 1 {
		s := &run.Series[0]
		blocks["series-figure"] = figureBlock("memory-by-clients", l.SeriesAlt)
		blocks["series-summary"] = seriesSummary(s, l)
		blocks["latency-figure"] = figureBlock("latency-by-clients", l.LatencyAlt)
		blocks["latency-summary"] = latencySummary(s, l)
	}
	return blocks
}

// hostBlock names the machine, the build and the day, which is what makes a
// number on this page comparable with a later one rather than just a number.
func hostBlock(run *Run, l labels) string {
	var parts []string
	parts = append(parts, fmt.Sprintf(l.Host, run.Host.describe()))
	if run.Server.Version != "" {
		parts = append(parts, fmt.Sprintf(l.Build, buildLabel(run.Server)))
	}
	if run.Server.BytesOnDisk > 0 {
		parts = append(parts, fmt.Sprintf(l.Binary, float64(run.Server.BytesOnDisk)/(1024*1024)))
	}
	parts = append(parts, fmt.Sprintf(l.MeasuredOn, run.GeneratedAt))
	return strings.Join(parts, ", ") + "."
}

// seriesSummary is the sentence under the series figure.
func seriesSummary(s *SeriesScenario, l labels) string {
	load, hasLoad := s.loadSlopeMiB()
	held, hasHeld := s.tenancySlopeKiB()
	if !hasLoad || !hasHeld {
		return l.NoSlope
	}
	return fmt.Sprintf(l.LoadSlope, load, held)
}

// latencySummary is the sentence under the latency figure.
func latencySummary(s *SeriesScenario, l labels) string {
	last := s.Steps[len(s.Steps)-1]
	return fmt.Sprintf(l.LatencyAt, last.Clients, last.P50Ms, last.P99Ms, s.Method, s.PerClientRPS)
}
