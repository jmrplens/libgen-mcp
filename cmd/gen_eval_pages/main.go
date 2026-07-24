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
	regionEnd      = "{/* end generated */}"
)

// scenarioRow is one row of the scenario table: the id and what it checks.
type scenarioRow struct{ ID, What string }

// resultRow is one row of a run's results.
type resultRow struct{ ID, Mode, Status, Detail string }

// tableRow matches a Markdown table row whose first cell is a scenario id.
var tableRow = regexp.MustCompile(`^\|\s*(S[0-9b]+)\s*\|\s*(.*?)\s*\|$`)

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

	for _, page := range []struct{ path, scenarios, results string }{
		{pageEN, renderScenariosEN(scenarios), renderResultsEN(results)},
		{pageES, renderScenariosES(scenarios), renderResultsES(results)},
	} {
		if aerr := applyPage(page.path, page.scenarios, page.results, check); aerr != nil {
			return aerr
		}
	}
	return nil
}

// applyPage replaces the generated regions of one page, or reports a difference.
func applyPage(path, scenarios, results string, check bool) error {
	original, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	updated, err := replaceRegion(string(original), scenariosBegin, scenarios)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if results != "" {
		if updated, err = replaceRegion(updated, resultsBegin, results); err != nil {
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
		if len(cells) < 3 {
			return resultRow{}, false
		}
		return resultRow{
			ID:     m[1],
			Mode:   strings.TrimSpace(cells[0]),
			Status: strings.TrimSpace(cells[1]),
			Detail: strings.TrimSpace(strings.Join(cells[2:], "|")),
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

// renderResultsEN renders the English results table.
func renderResultsEN(rows []resultRow) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("| ID  | Mode   | Result  | Evidence |\n| --- | ------ | ------- | --- |\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", r.ID, r.Mode, statusIcon(r.Status), r.Detail)
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
	b.WriteString("| ID  | Modo   | Resultado | Evidencia |\n| --- | ------ | ------- | --- |\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", r.ID, r.Mode, statusIcon(r.Status), r.Detail)
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
