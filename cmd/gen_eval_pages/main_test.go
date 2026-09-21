package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeResultsFixture writes a results doc body to a temp file and returns its path.
func writeResultsFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// TestReadResultsParsesMeasured verifies the results parser reads the Measured
// column and keeps a detail containing an escaped pipe in one piece.
func TestReadResultsParsesMeasured(t *testing.T) {
	path := writeResultsFixture(t, `# run

Model: `+"`m`"+`

| Scenario | Mode | Status | Measured | Detail |
| --- | --- | --- | --- | --- |
| S1 | local | PASS | 2026-01-02 | plain |
| S2 | remote | SKIP | 2026-03-04 | has a \| pipe |
`)
	rows, err := readResults(path)
	if err != nil {
		t.Fatalf("readResults: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("read %d rows, want 2", len(rows))
	}
	if rows[0].Measured != "2026-01-02" || rows[0].Detail != "plain" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].Mode != "remote" || rows[1].Measured != "2026-03-04" {
		t.Errorf("row 1 = %+v", rows[1])
	}
	if !strings.Contains(rows[1].Detail, "pipe") {
		t.Errorf("an escaped pipe split the detail: %q", rows[1].Detail)
	}
}

// TestSummarizeSpansMeasurementDates verifies the summary reports the earliest and
// latest dates in the table, which is what tells the prose whether it may still call
// itself a single run.
func TestSummarizeSpansMeasurementDates(t *testing.T) {
	sum := summarize("m", []resultRow{
		{ID: "S1", Mode: "local", Status: "PASS", Measured: "2026-03-04"},
		{ID: "S2", Mode: "remote", Status: "PASS", Measured: "2026-01-02"},
		{ID: "S3", Mode: "local", Status: "FAIL", Measured: "2026-02-03"},
	})
	if sum.First != "2026-01-02" || sum.Last != "2026-03-04" {
		t.Errorf("span = %s..%s, want 2026-01-02..2026-03-04", sum.First, sum.Last)
	}
	if sum.Pass != 2 || sum.Fail != 1 || sum.Remote != 1 {
		t.Errorf("tallies = %+v", sum)
	}
}

// TestMeasuredSpanWording verifies the prose stops claiming a single sweep once the
// table holds rows from more than one run, and says so in both languages.
func TestMeasuredSpanWording(t *testing.T) {
	one := runSummary{First: "2026-01-02", Last: "2026-01-02"}
	if got := measuredSpanEN(one); !strings.Contains(got, "a single live run") {
		t.Errorf("single-date EN wording = %q", got)
	}
	if got := measuredTailEN(one); got != "" {
		t.Errorf("a single-run table needs no explanation, got %q", got)
	}

	many := runSummary{First: "2026-01-02", Last: "2026-03-04"}
	gotEN := measuredSpanEN(many)
	if strings.Contains(gotEN, "single") || !strings.Contains(gotEN, "2026-03-04") {
		t.Errorf("multi-date EN wording = %q", gotEN)
	}
	if measuredTailEN(many) == "" {
		t.Error("a multi-run table must explain why the dates differ")
	}
	gotES := measuredSpanES(many)
	if strings.Contains(gotES, "única") || !strings.Contains(gotES, "2026-03-04") {
		t.Errorf("multi-date ES wording = %q", gotES)
	}
	if measuredTailES(many) == "" {
		t.Error("the Spanish page must carry the same explanation")
	}
}

// TestResultsSummaryFlagsUnmeasuredScenarios verifies the prose stops claiming
// "every scenario" once the suite holds one the last run never measured. The
// results table drops what it has no row for, so without this the page overstates
// its coverage by exactly the scenarios nobody has run — the ones a reader would
// most want flagged.
func TestResultsSummaryFlagsUnmeasuredScenarios(t *testing.T) {
	sum := runSummary{Model: "m", Total: 3, Pass: 3, Remote: 1, First: "2026-01-02", Last: "2026-01-02"}

	complete := renderResultsSummaryEN(sum, 3, 0)
	if !strings.Contains(complete, "every scenario") {
		t.Errorf("a fully measured suite should still say so; got %q", complete)
	}
	if strings.Contains(complete, "no row here yet") {
		t.Errorf("a fully measured suite must not warn about unmeasured rows; got %q", complete)
	}

	partial := renderResultsSummaryEN(sum, 5, 0)
	if strings.Contains(partial, "every scenario") {
		t.Errorf("an unmeasured scenario must stop the every-scenario claim; got %q", partial)
	}
	for _, want := range []string{"3 measured so far", "2 scenarios", "no row here yet"} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(partial, want) {
				t.Errorf("summary missing %q; got %q", want, partial)
			}
		})
	}
	if one := renderResultsSummaryEN(sum, 4, 0); !strings.Contains(one, "One scenario") {
		t.Errorf("a single unmeasured scenario needs the singular; got %q", one)
	}

	partialES := renderResultsSummaryES(sum, 5, 0)
	if !strings.Contains(partialES, "2 escenarios") || !strings.Contains(partialES, "medidos hasta ahora") {
		t.Errorf("the Spanish page must carry the same warning; got %q", partialES)
	}
	if oneES := renderResultsSummaryES(sum, 4, 0); !strings.Contains(oneES, "Un escenario") {
		t.Errorf("the Spanish singular is missing; got %q", oneES)
	}
}

// TestMeasuredSpanWithoutDates verifies a table whose rows carry no date (an older
// results doc) still produces a sentence rather than an empty claim.
func TestMeasuredSpanWithoutDates(t *testing.T) {
	none := runSummary{}
	if got := measuredSpanEN(none); got == "" || strings.Contains(got, "between") {
		t.Errorf("dateless EN wording = %q", got)
	}
	if got := measuredSpanES(none); got == "" {
		t.Error("dateless ES wording is empty")
	}
}

// TestIdRange_DescribesTheListTheWayTheProseDoes verifies the span and the
// lettered variants are told apart: a variant carries a letter, so it has no
// place in a numeric range, and the prose names it separately.
func TestIdRange_DescribesTheListTheWayTheProseDoes(t *testing.T) {
	testCases := []struct {
		name         string
		rows         []scenarioRow
		wantSpan     string
		wantVariants string
	}{
		{
			name:     "a plain run",
			rows:     []scenarioRow{{ID: "S3"}, {ID: "S1"}, {ID: "S12"}},
			wantSpan: "S1–S12",
		},
		{
			name:         "one variant",
			rows:         []scenarioRow{{ID: "S1"}, {ID: "S6b"}, {ID: "S9"}},
			wantSpan:     "S1–S9",
			wantVariants: "S6b",
		},
		{
			name:         "two variants",
			rows:         []scenarioRow{{ID: "S1"}, {ID: "S6b"}, {ID: "S7c"}},
			wantSpan:     "S1–S1",
			wantVariants: "S6b,S7c",
		},
		{name: "no rows at all", rows: nil, wantSpan: "S0–S0"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			span, variants := idRange(tc.rows)
			if span != tc.wantSpan {
				t.Errorf("idRange() span = %q, want %q", span, tc.wantSpan)
			}
			if got := strings.Join(variants, ","); got != tc.wantVariants {
				t.Errorf("idRange() variants = %q, want %q", got, tc.wantVariants)
			}
		})
	}
}

// TestVariantSuffix_PicksTheTemplateAndWritesNothingForNone verifies the tail
// agrees with itself in number, and that a run with no variants gets no tail
// rather than an empty phrase.
func TestVariantSuffix_PicksTheTemplateAndWritesNothingForNone(t *testing.T) {
	testCases := []struct {
		name     string
		variants []string
		want     string
	}{
		{name: "none", variants: nil, want: ""},
		{name: "one", variants: []string{"S6b"}, want: " plus the S6b variant"},
		{name: "two", variants: []string{"S6b", "S7c"}, want: " plus the S6b, S7c variants"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := variantSuffix(tc.variants, " plus the %s variant", " plus the %s variants")
			if got != tc.want {
				t.Errorf("variantSuffix(%v) = %q, want %q", tc.variants, got, tc.want)
			}
		})
	}
}

// TestStatusIcon_MarksAnUnknownStatusRatherThanHidingIt verifies a status the
// pages do not know is shown with a warning and its own name: a run that
// reported something new should say so, not render as blank.
func TestStatusIcon_MarksAnUnknownStatusRatherThanHidingIt(t *testing.T) {
	testCases := []struct{ status, want string }{
		{status: "PASS", want: "✅ PASS"},
		{status: "pass", want: "✅ PASS"},
		{status: "FAIL", want: "❌ FAIL"},
		{status: "SKIP", want: "⏭️ SKIP"},
		{status: "flaked", want: "⚠️ FLAKED"},
	}

	for _, tc := range testCases {
		t.Run(tc.status, func(t *testing.T) {
			if got := statusIcon(tc.status); got != tc.want {
				t.Errorf("statusIcon(%q) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

// TestReplaceRegion_RefusesAMarkerItCannotFindOrClose verifies the two ways a
// page can be wrong, because the alternative to an error is a page written
// with the generated block in the wrong place or not at all.
func TestReplaceRegion_RefusesAMarkerItCannotFindOrClose(t *testing.T) {
	const begin = "{/* BEGIN:table */}"

	t.Run("a region it replaces", func(t *testing.T) {
		page := "before\n" + begin + "\nold\n" + regionEnd + "\nafter\n"
		got, err := replaceRegion(page, begin, "new")
		if err != nil {
			t.Fatalf("replaceRegion() = %v", err)
		}
		if !strings.Contains(got, "new") || strings.Contains(got, "old") {
			t.Errorf("replaceRegion() = %q, want the body replaced", got)
		}
		if !strings.Contains(got, "{/* prettier-ignore */}") {
			t.Errorf("replaceRegion() = %q, want the generated table left to the generator", got)
		}
		if !strings.HasPrefix(got, "before\n") || !strings.HasSuffix(got, "after\n") {
			t.Errorf("replaceRegion() = %q, want everything outside the region kept", got)
		}
	})

	t.Run("a marker that is not there", func(t *testing.T) {
		if _, err := replaceRegion("nothing here\n", begin, "new"); err == nil {
			t.Error("replaceRegion() accepted a page with no begin marker")
		}
	})

	t.Run("a region nothing closes", func(t *testing.T) {
		if _, err := replaceRegion("before\n"+begin+"\nold\n", begin, "new"); err == nil {
			t.Error("replaceRegion() accepted a region that is never closed")
		}
	})
}

// TestReadModel_FindsTheBannerOrSaysWhyNot verifies the model a run was
// measured with is read from the run itself, and that a run with no banner is
// refused rather than published under no model at all.
func TestReadModel_FindsTheBannerOrSaysWhyNot(t *testing.T) {
	t.Run("a run with a banner", func(t *testing.T) {
		path := writeResultsFixture(t, "# Run\n\nModel: `claude-haiku-4-5`\n\n| S1 | stdio |\n")
		got, err := readModel(path)
		if err != nil {
			t.Fatalf("readModel() = %v", err)
		}
		if got != "claude-haiku-4-5" {
			t.Errorf("readModel() = %q, want the model from the banner", got)
		}
	})

	t.Run("a run with none", func(t *testing.T) {
		path := writeResultsFixture(t, "# Run\n\nno banner here\n")
		if _, err := readModel(path); err == nil {
			t.Error("readModel() accepted a run with no Model line")
		}
	})

	t.Run("a path that is not there", func(t *testing.T) {
		if _, err := readModel(filepath.Join(t.TempDir(), "absent.md")); err == nil {
			t.Error("readModel() accepted a path that is not there")
		}
	})
}

// TestRenderScenarios_BothLanguagesRenderEveryRow verifies the two tables stay
// the same length, which is what the i18n parity of these pages rests on.
func TestRenderScenarios_BothLanguagesRenderEveryRow(t *testing.T) {
	rows := []scenarioRow{{ID: "S1", What: "a first check"}, {ID: "S2", What: "a second"}}

	for _, tc := range []struct {
		name     string
		rendered string
	}{
		{name: "English", rendered: renderScenariosEN(rows)},
		{name: "Spanish", rendered: renderScenariosES(rows)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Count(tc.rendered, "\n| S"); got != len(rows) {
				t.Errorf("%s table has %d scenario rows, want %d:\n%s", tc.name, got, len(rows), tc.rendered)
			}
		})
	}
}

// TestRenderScenarioSummary_BothLanguagesQuoteTheSameCounts verifies the tally
// each page opens with is built from the rows rather than typed, so the two
// cannot drift from each other or from the table below them.
func TestRenderScenarioSummary_BothLanguagesQuoteTheSameCounts(t *testing.T) {
	rows := []scenarioRow{{ID: "S1"}, {ID: "S2"}, {ID: "S6b"}}
	sum := runSummary{Remote: 2}

	for _, tc := range []struct {
		name     string
		rendered string
	}{
		{name: "English", rendered: renderScenarioSummaryEN(rows, sum)},
		{name: "Spanish", rendered: renderScenarioSummaryES(rows, sum)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, want := range []string{"3", "S1–S2", "S6b", "2"} {
				if !strings.Contains(tc.rendered, want) {
					t.Errorf("%s summary = %q, want it to carry %q", tc.name, tc.rendered, want)
				}
			}
		})
	}
}

// writeScenarioFixture writes a catalog table to a temp file and returns its path.
func writeScenarioFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "README.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// TestReadScenarios_RefusesAnUnlabeledRow verifies the stimulus is required and
// closed to the two values the rule defines.
//
// It is the half of the column that cannot be left to review. A row that fell
// back to "uncoached" for being blank would claim the server described itself
// well enough to be used unaided, on a public page, in the one case where nobody
// had looked — so the generator stops instead, naming the row.
func TestReadScenarios_RefusesAnUnlabeledRow(t *testing.T) {
	for _, tc := range []struct {
		name, table, want string
	}{
		{
			name:  "a label outside the two",
			table: "| ID  | What it checks | Stimulus |\n| --- | --- | --- |\n| S1 | a check | partly |\n",
			want:  `S1 ("partly")`,
		},
		{
			name:  "an empty cell",
			table: "| ID  | What it checks | Stimulus |\n| --- | --- | --- |\n| S1 | a check |  |\n",
			want:  `S1 ("")`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readScenarios(writeScenarioFixture(t, tc.table))
			if err == nil {
				t.Fatal("expected a refusal, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to name %s", err, tc.want)
			}
		})
	}

	rows, err := readScenarios(writeScenarioFixture(t,
		"| ID  | What it checks | Stimulus |\n| --- | --- | --- |\n"+
			"| S1 | a check | coached |\n| S2 | another | uncoached |\n"))
	if err != nil {
		t.Fatalf("a labeled table must parse: %v", err)
	}
	if len(rows) != 2 || rows[0].Stimulus != coached || rows[1].Stimulus != uncoached {
		t.Errorf("parsed %+v, want S1 coached and S2 uncoached", rows)
	}
	if rows[0].What != "a check" {
		t.Errorf("What = %q, want the cell without its padding", rows[0].What)
	}
}

// TestCoachedAmong_CountsTheMeasuredOnesOnly verifies the number the tally
// publishes is the coached share of what was actually run, not of the suite: a
// scenario with no result row has not contributed to any pass rate, so counting
// it would overstate the qualification exactly where there is nothing to qualify.
func TestCoachedAmong_CountsTheMeasuredOnesOnly(t *testing.T) {
	scenarios := []scenarioRow{
		{ID: "S1", Stimulus: coached},
		{ID: "S2", Stimulus: uncoached},
		{ID: "S3", Stimulus: coached},
	}
	results := []resultRow{{ID: "S1"}, {ID: "S2"}}

	if got := coachedAmong(scenarios, results); got != 1 {
		t.Errorf("coachedAmong = %d, want 1 (S3 is coached but was never measured)", got)
	}
	if got := coachedCount(scenarios); got != 2 {
		t.Errorf("coachedCount = %d, want 2", got)
	}
}

// TestResultsSummary_SaysHowMuchOfTheTallyIsCoached verifies the published
// headline carries the qualification rather than leaving it to the table below.
// The sentence is generated, so a page that lost it differs from what would be
// generated and --check fails - which is what makes the refusal structural
// instead of a thing somebody has to remember.
func TestResultsSummary_SaysHowMuchOfTheTallyIsCoached(t *testing.T) {
	sum := runSummary{Model: "m", Total: 4, Pass: 4, First: "2026-01-02", Last: "2026-01-02"}

	for _, tc := range []struct {
		name, rendered, want string
	}{
		{name: "English", rendered: renderResultsSummaryEN(sum, 4, 3), want: "3 of the 4 measured are coached"},
		{name: "Spanish", rendered: renderResultsSummaryES(sum, 4, 3), want: "3 de los 4 medidos están guiados"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.rendered, tc.want) {
				t.Errorf("summary = %q, want it to carry %q", tc.rendered, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		name, rendered string
	}{
		{name: "English", rendered: renderResultsSummaryEN(sum, 4, 0)},
		{name: "Spanish", rendered: renderResultsSummaryES(sum, 4, 0)},
	} {
		t.Run("no coached scenario measured/"+tc.name, func(t *testing.T) {
			if strings.Contains(strings.ToLower(tc.rendered), "coached") ||
				strings.Contains(tc.rendered, "guiados") {
				t.Errorf("a run with no coached row must not carry the note; got %q", tc.rendered)
			}
		})
	}
}

// TestRenderScenarios_CarryTheStimulusInEachLanguage verifies the column reaches
// both pages, translated. The English label is what the README carries and what
// the rule is written in, so it is also the key the Spanish table is looked up
// by - a label the generator would refuse never reaches the map.
func TestRenderScenarios_CarryTheStimulusInEachLanguage(t *testing.T) {
	rows := []scenarioRow{{ID: "S1", What: "a check", Stimulus: coached}}

	if got := renderScenariosEN(rows); !strings.Contains(got, "| Stimulus |") || !strings.Contains(got, "| coached |") {
		t.Errorf("English table = %q, want a Stimulus column carrying the label", got)
	}
	if got := renderScenariosES(rows); !strings.Contains(got, "| Estímulo |") || !strings.Contains(got, "| guiado |") {
		t.Errorf("Spanish table = %q, want the translated column and label", got)
	}
}
