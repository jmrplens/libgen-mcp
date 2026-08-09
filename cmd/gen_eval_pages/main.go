// Command gen_eval_pages generates the tables on the evaluator results pages, in
// both languages, so they cannot drift from the code they describe.
//
// Two tables must match something outside the page: the scenario list, whose
// authority is cmd/eval/README.md, and the latest run, whose authority is the
// results doc a run writes with --results-doc. Both were maintained by hand and
// both drifted — a stale scenario count, malformed rows, an evidence string quoting
// a message the code no longer emitted, and a live download key published in a
// results row.
//
// Only the regions between the generated-region markers are rewritten. The prose
// around them is written by hand and left alone.
//
// Usage:
//
//	go run ./cmd/gen_eval_pages/ --results-doc eval-results.md
//	go run ./cmd/gen_eval_pages/ --check
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	scenarioSource = "cmd/eval/README.md"
	// resultsSource is the run whose table the pages publish. It is versioned so the
	// pages can be regenerated — and checked — without a live run: a hand edit to a
	// published result is caught by CI, which is the whole point of generating them.
	resultsSource = "cmd/eval/testdata/latest-run.md"
	pageEN        = "site/src/content/docs/eval-results.mdx"
	pageES        = "site/src/content/docs/es/eval-results.mdx"
)

// Region markers. MDX comments, so they render as nothing.
const (
	scenariosBegin = "{/* generated:scenarios — run `make eval-pages`, do not edit by hand */}"
	resultsBegin   = "{/* generated:results — run `make eval-pages`, do not edit by hand */}"
	// The counts the prose quotes. They were written by hand and drifted: the
	// pages claimed 45 scenarios long after the suite reached 61. Generating the
	// sentences that carry a number keeps the prose honest by construction.
	scenarioSummaryBegin = "{/* generated:scenario-summary — run `make eval-pages`, do not edit by hand */}"
	resultsSummaryBegin  = "{/* generated:results-summary — run `make eval-pages`, do not edit by hand */}"
	regionEnd            = "{/* end generated */}"
)

// scenarioRow is one row of the scenario table: the id and what it checks.
type scenarioRow struct{ ID, What string }

// resultRow is one row of a run's results.
type resultRow struct{ ID, Mode, Status, Measured, Detail string }

// tableRow matches a Markdown table row whose first cell is a scenario id.
var tableRow = regexp.MustCompile(`^\|\s*(S[0-9b]+)\s*\|\s*(.*?)\s*\|$`)

// modelLine matches the model banner a run writes above its results table.
var modelLine = regexp.MustCompile("^Model:\\s*`([^`]+)`")

// runSummary is what a results table adds up to: the tallies the prose quotes.
type runSummary struct {
	Model                   string
	Total, Pass, Fail, Skip int
	Remote                  int
	// First and Last are the earliest and latest measurement dates among the rows.
	// They differ when the table was assembled from more than one run — a partial
	// run refreshes only the scenarios it executed — and the prose says so rather
	// than claiming a single sweep it cannot show.
	First, Last string
}

// summarize counts a run's rows so no page has to state a total by hand.
func summarize(model string, rows []resultRow) runSummary {
	s := runSummary{Model: model, Total: len(rows)}
	for _, r := range rows {
		switch strings.ToUpper(r.Status) {
		case "PASS":
			s.Pass++
		case "FAIL":
			s.Fail++
		case "SKIP":
			s.Skip++
		}
		if strings.EqualFold(r.Mode, "remote") {
			s.Remote++
		}
		if d := strings.TrimSpace(r.Measured); d != "" {
			if s.First == "" || d < s.First {
				s.First = d
			}
			if d > s.Last {
				s.Last = d
			}
		}
	}
	return s
}

// measuredSpanEN describes when the table's rows were measured, for the English
// prose: one date when every row came from the same run, a range when a partial
// run has refreshed part of the table since.
func measuredSpanEN(sum runSummary) string {
	if sum.First == "" {
		return "a single live run of the full suite"
	}
	if sum.First == sum.Last {
		return "a single live run of the full suite on " + sum.First
	}
	return "assembled from live runs between " + sum.First + " and " + sum.Last
}

// measuredTailEN is the sentence appended when the table spans more than one run,
// so a reader is told why the dates differ instead of inferring it.
func measuredTailEN(sum runSummary) string {
	if sum.First == "" || sum.First == sum.Last {
		return ""
	}
	return " Each row carries the date it was last measured: a partial run refreshes only the scenarios it executed."
}

// idRange describes a scenario list the way the prose does: the numeric span,
// plus any lettered variants called out by name.
func idRange(rows []scenarioRow) (span string, variants []string) {
	low, high := 0, 0
	for _, r := range rows {
		n, err := strconv.Atoi(strings.TrimPrefix(r.ID, "S"))
		if err != nil {
			variants = append(variants, r.ID)
			continue
		}
		if low == 0 || n < low {
			low = n
		}
		if n > high {
			high = n
		}
	}
	return fmt.Sprintf("S%d–S%d", low, high), variants
}

// variantSuffix renders the "plus the S6b variant" tail. The singular and plural
// templates each take the joined ids, so a language that puts the noun before the
// id ("más la variante S6b") reads correctly too.
func variantSuffix(variants []string, singular, plural string) string {
	if len(variants) == 0 {
		return ""
	}
	tmpl := singular
	if len(variants) > 1 {
		tmpl = plural
	}
	return fmt.Sprintf(tmpl, strings.Join(variants, ", "))
}

func main() {
	resultsDoc := flag.String("results-doc", resultsSource, "the run whose results the pages publish; a fresh one replaces the versioned copy")
	check := flag.Bool("check", false, "exit non-zero when a page differs from what would be generated, without writing")
	flag.Parse()

	if err := run(*resultsDoc, *check); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *check {
		fmt.Println("evaluator results pages are up to date")
		return
	}
	fmt.Println("regenerated the evaluator results pages")
}

// run rewrites (or checks) both pages.
func run(resultsDoc string, check bool) error {
	scenarios, err := readScenarios(scenarioSource)
	if err != nil {
		return err
	}
	if terr := assertTranslated(scenarios); terr != nil {
		return terr
	}
	results, rerr := readResults(resultsDoc)
	if rerr != nil {
		return rerr
	}
	// A run given on the command line becomes the published one, so regenerating
	// from a fresh run also updates the copy the check compares against.
	if resultsDoc != resultsSource && !check {
		if cerr := copyFile(resultsDoc, resultsSource); cerr != nil {
			return cerr
		}
	}

	model, merr := readModel(resultsDoc)
	if merr != nil {
		return merr
	}
	sum := summarize(model, results)

	for _, page := range []struct {
		path    string
		regions []region
	}{
		{pageEN, []region{
			{scenariosBegin, renderScenariosEN(scenarios)},
			{scenarioSummaryBegin, renderScenarioSummaryEN(scenarios, sum)},
			{resultsBegin, renderResultsEN(results)},
			{resultsSummaryBegin, renderResultsSummaryEN(sum, len(scenarios))},
		}},
		{pageES, []region{
			{scenariosBegin, renderScenariosES(scenarios)},
			{scenarioSummaryBegin, renderScenarioSummaryES(scenarios, sum)},
			{resultsBegin, renderResultsES(results)},
			{resultsSummaryBegin, renderResultsSummaryES(sum, len(scenarios))},
		}},
	} {
		if aerr := applyPage(page.path, page.regions, check); aerr != nil {
			return aerr
		}
	}
	return nil
}

// region is one generated block: the marker that opens it and the body to put
// between that marker and the next end marker.
type region struct{ begin, body string }

// applyPage replaces the generated regions of one page, or reports a difference.
func applyPage(path string, regions []region, check bool) error {
	original, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	updated := string(original)
	for _, r := range regions {
		// An empty body means there is nothing authoritative to write (no run),
		// so the region is left as it stands rather than blanked.
		if r.body == "" {
			continue
		}
		if updated, err = replaceRegion(updated, r.begin, r.body); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	if updated == string(original) {
		return nil
	}
	if check {
		return fmt.Errorf("%s is out of date; run `make eval-pages`", path)
	}
	return os.WriteFile(path, []byte(updated), 0o600)
}

// readModel reads the model banner a run writes above its results table.
func readModel(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	for line := range strings.SplitSeq(string(body), "\n") {
		if m := modelLine.FindStringSubmatch(line); m != nil {
			return m[1], nil
		}
	}
	return "", fmt.Errorf("%s has no `Model:` line", path)
}

// replaceRegion swaps the content between a begin marker and the next end marker.
func replaceRegion(page, begin, body string) (string, error) {
	start := strings.Index(page, begin)
	if start < 0 {
		return "", fmt.Errorf("missing region marker %q", begin)
	}
	rest := page[start+len(begin):]
	end := strings.Index(rest, regionEnd)
	if end < 0 {
		return "", fmt.Errorf("region %q is never closed by %q", begin, regionEnd)
	}
	// Prettier also formats these files and pads table cells to align them, which
	// would leave the two tools rewriting each other forever. The generated table is
	// the generator's to own, so Prettier is told to leave it alone.
	return page[:start+len(begin)] + "\n\n{/* prettier-ignore */}\n" + body + "\n\n" + rest[end:], nil
}

// readScenarios reads the canonical scenario list out of the evaluator's README,
// which is where the descriptions are written and reviewed.
func readScenarios(path string) ([]scenarioRow, error) {
	rows, err := readTable(path, func(m []string) (scenarioRow, bool) {
		// The results table in the same file also starts with an id; scenario rows
		// are the ones with exactly two cells.
		if strings.Contains(m[2], "|") {
			return scenarioRow{}, false
		}
		return scenarioRow{ID: m[1], What: m[2]}, true
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%s lists no scenarios", path)
	}
	return rows, nil
}

// readResults reads a run's results table as written by cmd/eval --results-doc.
func readResults(path string) ([]resultRow, error) {
	rows, err := readTable(path, func(m []string) (resultRow, bool) {
		cells := strings.Split(m[2], "|")
		if len(cells) < 4 {
			return resultRow{}, false
		}
		return resultRow{
			ID:       m[1],
			Mode:     strings.TrimSpace(cells[0]),
			Status:   strings.TrimSpace(cells[1]),
			Measured: strings.TrimSpace(cells[2]),
			Detail:   strings.TrimSpace(strings.Join(cells[3:], "|")),
		}, true
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%s holds no results", path)
	}
	return rows, nil
}

// readTable scans a Markdown file for id-keyed table rows and maps each one.
func readTable[T any](path string, keep func([]string) (T, bool)) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		m := tableRow.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		if row, ok := keep(m); ok {
			out = append(out, row)
		}
	}
	if err = sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}

// statusIcon renders a run status the way the pages present it.
func statusIcon(status string) string {
	switch strings.ToUpper(status) {
	case "PASS":
		return "✅ PASS"
	case "FAIL":
		return "❌ FAIL"
	case "SKIP":
		return "⏭️ SKIP"
	default:
		return "⚠️ " + strings.ToUpper(status)
	}
}

// renderScenariosEN renders the English scenario table.
func renderScenariosEN(rows []scenarioRow) string {
	var b strings.Builder
	b.WriteString("| ID  | What it checks |\n| --- | --- |\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s |\n", r.ID, r.What)
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderScenariosES renders the Spanish scenario table from the translations.
func renderScenariosES(rows []scenarioRow) string {
	var b strings.Builder
	b.WriteString("| ID  | Qué comprueba |\n| --- | --- |\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s |\n", r.ID, scenariosES[r.ID])
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderScenarioSummaryEN renders the English scenario tally.
func renderScenarioSummaryEN(rows []scenarioRow, sum runSummary) string {
	span, variants := idRange(rows)
	return fmt.Sprintf(
		"The suite is **%d scenarios** (%s%s). %d of them drive a server in remote (`--http`) mode; the rest run it over stdio.",
		len(rows), span, variantSuffix(variants, " plus the %s variant", " plus the %s variants"), sum.Remote)
}

// renderScenarioSummaryES renders the Spanish scenario tally.
func renderScenarioSummaryES(rows []scenarioRow, sum runSummary) string {
	span, variants := idRange(rows)
	return fmt.Sprintf(scenarioSummaryES,
		len(rows), span, variantSuffix(variants, variantSuffixES, variantSuffixPluralES), sum.Remote)
}

// measuredScopeEN says what the tally is a tally OF: the whole suite, or the part
// of it that has been measured.
//
// The sentence used to claim "every scenario" unconditionally, which held only
// while a scenario could not exist without a row. It can: a scenario added since
// the last live run has no result to publish, and the results table drops what it
// has no row for. Saying "every scenario" then overstates the coverage by exactly
// the scenarios nobody has run yet — the ones a reader would most want flagged.
func measuredScopeEN(sum runSummary, scenarios int) string {
	remote := fmt.Sprintf("including the %d that run against a server in remote (`--http`) mode.", sum.Remote)
	unmeasured := scenarios - sum.Total
	if unmeasured <= 0 {
		return fmt.Sprintf("out of %d — every scenario, %s", sum.Total, remote)
	}
	return fmt.Sprintf("out of the %d measured so far, %s %s", sum.Total, remote, unmeasuredNoteEN(unmeasured))
}

// unmeasuredNoteEN names the scenarios listed above the table that have no row in
// it yet.
func unmeasuredNoteEN(n int) string {
	if n == 1 {
		return "One scenario in the list above has no row here yet: it was added after the last live run, " +
			"and a result is published only once it has been measured."
	}
	return fmt.Sprintf("%d scenarios in the list above have no row here yet: they were added after the last "+
		"live run, and a result is published only once it has been measured.", n)
}

// renderResultsSummaryEN renders the English run tally.
func renderResultsSummaryEN(sum runSummary, scenarios int) string {
	if sum.Total == 0 {
		return ""
	}
	return fmt.Sprintf(
		"The table below is %s against `%s` (real Anthropic API, real mirrors, real downloads): **%d passed, %d failed, %d skipped** %s%s",
		measuredSpanEN(sum), sum.Model, sum.Pass, sum.Fail, sum.Skip, measuredScopeEN(sum, scenarios), measuredTailEN(sum))
}

// measuredSpanES is measuredSpanEN's Spanish counterpart.
func measuredSpanES(sum runSummary) string {
	if sum.First == "" {
		return measuredSpanNoneES
	}
	if sum.First == sum.Last {
		return fmt.Sprintf(measuredSpanOneES, sum.First)
	}
	return fmt.Sprintf(measuredSpanRangeES, sum.First, sum.Last)
}

// measuredTailES is measuredTailEN's Spanish counterpart.
func measuredTailES(sum runSummary) string {
	if sum.First == "" || sum.First == sum.Last {
		return ""
	}
	return measuredTailRangeES
}

// measuredScopeES is measuredScopeEN's Spanish counterpart.
func measuredScopeES(sum runSummary, scenarios int) string {
	remote := fmt.Sprintf(remoteShareES, sum.Remote)
	unmeasured := scenarios - sum.Total
	if unmeasured <= 0 {
		return fmt.Sprintf(scopeAllES, sum.Total, remote)
	}
	return fmt.Sprintf(scopeMeasuredES, sum.Total, remote, unmeasuredNoteES(unmeasured))
}

// unmeasuredNoteES is unmeasuredNoteEN's Spanish counterpart.
func unmeasuredNoteES(n int) string {
	if n == 1 {
		return unmeasuredOneES
	}
	return fmt.Sprintf(unmeasuredManyES, n)
}

// renderResultsSummaryES renders the Spanish run tally.
func renderResultsSummaryES(sum runSummary, scenarios int) string {
	if sum.Total == 0 {
		return ""
	}
	return fmt.Sprintf(resultsSummaryES,
		measuredSpanES(sum), sum.Model, sum.Pass, sum.Fail, sum.Skip, measuredScopeES(sum, scenarios), measuredTailES(sum))
}

// mdxEvidence makes a harness message safe to paste into an MDX table cell. The
// messages quote tool output verbatim, so one can carry a brace — a failure that
// reproduces a JSON payload, say — and MDX reads a bare "{" as the start of a
// JavaScript expression and fails the whole site build. Escaping both braces is
// enough: MDX renders "\{" as a literal brace, and the pages are generated, so a
// hand-fixed page would only be regenerated broken.
func mdxEvidence(s string) string {
	return strings.NewReplacer("{", `\{`, "}", `\}`).Replace(s)
}

// renderResultsEN renders the English results table.
func renderResultsEN(rows []resultRow) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("| ID  | Mode   | Result  | Measured | Evidence |\n| --- | ------ | ------- | --- | --- |\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", r.ID, r.Mode, statusIcon(r.Status), r.Measured, mdxEvidence(r.Detail))
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderResultsES renders the Spanish results table. The evidence strings are the
// harness's own messages and are left in English on purpose: they are quoted
// output, and translating them would put words in the harness's mouth.
func renderResultsES(rows []resultRow) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("| ID  | Modo   | Resultado | Medido | Evidencia |\n| --- | ------ | ------- | --- | --- |\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", r.ID, r.Mode, statusIcon(r.Status), r.Measured, mdxEvidence(r.Detail))
	}
	return strings.TrimRight(b.String(), "\n")
}

// assertTranslated fails when a scenario has no Spanish description, so a new one
// stops the build rather than appearing on the Spanish page in English.
func assertTranslated(rows []scenarioRow) error {
	var missing []string
	for _, r := range rows {
		if strings.TrimSpace(scenariosES[r.ID]) == "" {
			missing = append(missing, r.ID)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("no Spanish description for %s: add it to scenariosES in %s",
		strings.Join(missing, ", "), "cmd/gen_eval_pages/translations.go")
}

// copyFile replaces dst with the contents of src.
func copyFile(src, dst string) error {
	body, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err = os.WriteFile(dst, body, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}
