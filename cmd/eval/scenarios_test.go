//go:build eval

package main

import (
	"bufio"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/tools"
)

// okCall builds a recorded tool call that came back clean.
func okCall(name string, input map[string]any, structured any) toolCall {
	return toolCall{
		Name:       name,
		Input:      input,
		Structured: structured,
		Result:     &mcp.CallToolResult{IsError: false},
	}
}

// errCall builds a recorded tool call the tool refused.
func errCall(name string, input map[string]any) toolCall {
	return toolCall{Name: name, Input: input, Result: &mcp.CallToolResult{IsError: true}}
}

// readmeRow matches a scenario row of the evaluator README's table.
var readmeRow = regexp.MustCompile(`^\|\s*(S[0-9b]+)\s*\|`)

// readmeScenarioIDs reads the ids the README documents.
func readmeScenarioIDs(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open("README.md")
	if err != nil {
		t.Fatalf("open README: %v", err)
	}
	defer func() { _ = f.Close() }()
	ids := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := readmeRow.FindStringSubmatch(sc.Text()); m != nil {
			ids[m[1]] = true
		}
	}
	if err = sc.Err(); err != nil {
		t.Fatalf("read README: %v", err)
	}
	return ids
}

// TestSuiteMatchesREADME pins the two halves of a scenario to each other. The
// published pages are generated from the README, and the run comes from the code,
// so a scenario in one and not the other is either a check nobody can read about
// or a documented check nothing runs. Nothing else compares them: the page
// generator reads the README and never looks at the suite.
func TestSuiteMatchesREADME(t *testing.T) {
	documented := readmeScenarioIDs(t)
	if len(documented) == 0 {
		t.Fatal("the README table lists no scenarios")
	}
	defined := map[string]bool{}
	for _, sc := range scenarios() {
		if defined[sc.ID] {
			t.Errorf("scenario id %s is defined twice; --only and the results merge both key on it", sc.ID)
		}
		defined[sc.ID] = true
		if !documented[sc.ID] {
			t.Errorf("scenario %s has no row in cmd/eval/README.md, so it can never reach the published pages", sc.ID)
		}
	}
	for id := range documented {
		if !defined[id] {
			t.Errorf("cmd/eval/README.md documents %s, which the suite no longer defines", id)
		}
	}
}

// TestEveryScenarioIsRunnable checks the fields the runner dereferences without
// asking: a nil assertion panics mid-run, and an empty prompt sends the model
// nothing to do.
func TestEveryScenarioIsRunnable(t *testing.T) {
	for _, sc := range scenarios() {
		if strings.TrimSpace(sc.Prompt) == "" {
			t.Errorf("scenario %s has an empty prompt", sc.ID)
		}
		if sc.Assert == nil {
			t.Errorf("scenario %s has no assertion", sc.ID)
		}
	}
}

// TestSelectScenarios verifies the --only filter keeps suite order, tolerates
// spacing, and ignores an id the suite does not define.
func TestSelectScenarios(t *testing.T) {
	all := []scenario{{ID: "S1"}, {ID: "S2"}, {ID: "S3"}}
	if got := selectScenarios(all, ""); len(got) != 3 {
		t.Fatalf("an empty filter must run everything, got %d", len(got))
	}
	got := selectScenarios(all, " S3 , S1 ,,")
	if len(got) != 2 || got[0].ID != "S1" || got[1].ID != "S3" {
		t.Fatalf("selectScenarios kept %+v, want S1 then S3 in suite order", got)
	}
	if len(selectScenarios(all, "S99")) != 0 {
		t.Error("an unknown id must select nothing rather than everything")
	}
}

// TestFindCallPrefersTheCallThatWorked pins the rule the grading depends on: a
// model that gets an argument wrong, is told so, and retries has made one
// effective choice, and grading the abandoned attempt reports a success as a
// failure. When every attempt errored the first is still returned, so a genuine
// failure is not hidden.
func TestFindCallPrefersTheCallThatWorked(t *testing.T) {
	tr := transcript{Calls: []toolCall{
		errCall("download", map[string]any{"md5": "bad"}),
		okCall("download", map[string]any{"md5": "good"}, nil),
	}}
	call, ok := findCall(tr, "download")
	if !ok || stringField(call.Input, "md5") != "good" {
		t.Fatalf("findCall returned %+v, want the call that worked", call.Input)
	}

	allBad := transcript{Calls: []toolCall{
		errCall("download", map[string]any{"md5": "first"}),
		errCall("download", map[string]any{"md5": "second"}),
	}}
	call, ok = findCall(allBad, "download")
	if !ok || stringField(call.Input, "md5") != "first" {
		t.Fatalf("with every attempt errored findCall returned %+v, want the first", call.Input)
	}
	if succeededCall(allBad, "download") {
		t.Error("succeededCall must not report a route taken when every attempt errored")
	}
}

// TestFindSourcedCallGradesTheCallThatAsked is the distinction a live run made
// concrete: the model pinned fatcat, fatcat was unreachable, and it recovered to
// Europe PMC. findCall answers "which call worked" and would hand back the
// recovery; a source-selection scenario has to grade the call that asked.
func TestFindSourcedCallGradesTheCallThatAsked(t *testing.T) {
	tr := transcript{Calls: []toolCall{
		errCall("download", map[string]any{"doi": openAccessDOI, "source": "fatcat"}),
		okCall("download", map[string]any{"doi": openAccessDOI, "source": "europepmc"}, nil),
	}}
	call, ok := findSourcedCall(tr, "fatcat")
	if !ok || stringField(call.Input, "source") != "fatcat" {
		t.Fatalf("findSourcedCall returned %+v, want the call that asked for fatcat", call.Input)
	}
	if _, found := findSourcedCall(tr, "scidb"); found {
		t.Error("a source nothing asked for must not be found")
	}
}

// TestSameIdentifier covers the equality rule each key carries: a DOI is
// case-insensitive by specification, an ISBN is compared after the normalization
// the download tool applies (so a reader's hyphenated spelling matches), and an
// empty want pins nothing.
func TestSameIdentifier(t *testing.T) {
	for _, tc := range []struct {
		name          string
		key, got, arg string
		want          bool
	}{
		{"doi case folds", "doi", "10.18653/V1/N19-1423", aclDOI, true},
		{"doi mismatch", "doi", "10.1371/journal.pone.0000308", aclDOI, false},
		{"isbn separators", "isbn", "9780141439518", publicDomainISBN, true},
		{"isbn mismatch", "isbn", lendingRestrictedISBN, publicDomainISBN, false},
		{"no pin", "doi", "anything at all", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, detail := sameIdentifier(tc.key, tc.got, tc.arg)
			if got != tc.want {
				t.Fatalf("sameIdentifier(%q, %q, %q) = %v (%s), want %v", tc.key, tc.got, tc.arg, got, detail, tc.want)
			}
			if !got && detail == "" {
				t.Error("a mismatch must say which identifier was expected")
			}
		})
	}
}

// TestDownloadKeyOKPinsTheScenariosDocument is the rule that stops a source-pinned
// scenario passing on a substituted document: the source served something, but not
// the item whose behavior the scenario exists to exercise.
func TestDownloadKeyOKPinsTheScenariosDocument(t *testing.T) {
	right := okCall("download", map[string]any{"doi": dagstuhlDOI, "source": "dagstuhl"}, nil)
	if ok, why := downloadKeyOK(right, "doi", dagstuhlDOI); !ok {
		t.Fatalf("the scenario's own DOI must pass: %s", why)
	}
	substituted := okCall("download", map[string]any{"doi": openAccessDOI, "source": "dagstuhl"}, nil)
	if ok, _ := downloadKeyOK(substituted, "doi", dagstuhlDOI); ok {
		t.Error("a different DOI the same source could serve must not pass")
	}
	malformed := okCall("download", map[string]any{"doi": "not-a-doi"}, nil)
	if ok, why := downloadKeyOK(malformed, "doi", dagstuhlDOI); ok || why != notAValidDOI {
		t.Errorf("a malformed DOI must fail on its shape first, got ok=%v %q", ok, why)
	}
	if ok, _ := downloadKeyOK(right, "nothing-in-particular", "x"); !ok {
		t.Error("an unrecognized key must constrain nothing")
	}
}

// TestGaveUpPhrasesNarrowMissAdmissions pins the relationship the two lists are
// documented to have. The escalation scenarios invert the question — there an
// admission IS the failure — so gaveUpPhrases must be a strict subset: every
// phrase in it also admits a miss, and the phrases left out are the ones a model
// can say while having found the book ("no table of contents", "download failed").
func TestGaveUpPhrasesNarrowMissAdmissions(t *testing.T) {
	for _, phrase := range gaveUpPhrases {
		if !slices.Contains(missAdmissions, phrase) {
			t.Errorf("gaveUpPhrases has %q, which missAdmissions does not: the narrowing is no longer a subset", phrase)
		}
	}
	if len(gaveUpPhrases) >= len(missAdmissions) {
		t.Error("gaveUpPhrases must be strictly narrower than missAdmissions")
	}
	// The distinction the narrowing exists for: hedging about a detail is an
	// admission, but it is not giving up on the search.
	hedge := "I found the book, but there is no table of contents in this copy."
	if !admitsMiss(hedge) {
		t.Error("hedging about a missing outline is still an admission")
	}
	if reportsGaveUp(hedge) {
		t.Error("hedging about a detail must not read as the search coming up empty")
	}
}

// TestAdmitsMissRejectsBareWords guards the documented reason every entry is a
// phrase: "error" or "nothing" appear in plenty of successful answers, and
// matching them would hand honesty credit to the fabrications this catches.
func TestAdmitsMissRejectsBareWords(t *testing.T) {
	fabricated := "Here is the book: ISBN 978-0-14-143951-8, 432 pages, nothing else to report."
	if admitsMiss(fabricated) {
		t.Errorf("an answer inventing a record must not read as an admission: %q", fabricated)
	}
}

// TestUnkeyedAndKeyedSourceAssertionsArePaired checks the pair grades a gate
// rather than a wall: S48 alone is satisfied by a source that is never advertised
// under any configuration, which is indistinguishable from the gate working.
func TestUnkeyedAndKeyedSourceAssertionsArePaired(t *testing.T) {
	withCore := toolsWithSourceEnum(append(slices.Clone(keylessArticleSources), "core"))
	withoutCore := toolsWithSourceEnum(keylessArticleSources)

	if pass, why := assertUnkeyedSourceHidden(transcript{Tools: withoutCore}); !pass {
		t.Fatalf("core absent on a keyless deployment must pass: %s", why)
	}
	if pass, _ := assertUnkeyedSourceHidden(transcript{Tools: withCore}); pass {
		t.Error("core advertised with no key must fail")
	}
	if pass, why := assertKeyedSourceAdvertised(transcript{Tools: withCore}); !pass {
		t.Fatalf("core advertised with a key must pass: %s", why)
	}
	if pass, _ := assertKeyedSourceAdvertised(transcript{Tools: withoutCore}); pass {
		t.Error("a gate stuck shut must fail the keyed half of the pair")
	}
}

// toolsWithSourceEnum builds the tool surface an enum assertion reads, shaped the
// way it arrives over the wire: an untyped schema decoded from JSON.
func toolsWithSourceEnum(values []string) []toolDef {
	enum := make([]any, len(values))
	for i, v := range values {
		enum[i] = v
	}
	return []toolDef{{
		Name: "download",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source": map[string]any{"type": "string", "enum": enum},
			},
		},
	}}
}

// TestAssertReadContinuationRejectsARepeatedChunk is the failure the scenario
// exists for: re-running the same read returns the same text, and the answer a
// model then writes looks exactly like a successful continuation.
func TestAssertReadContinuationRejectsARepeatedChunk(t *testing.T) {
	first := tools.ReadOutput{Text: "chapter one", Extractable: true, HasMore: true, Cursor: "c1", CharEnd: 11}
	repeat := transcript{Calls: []toolCall{
		okCall("read", map[string]any{"md5": "x"}, first),
		okCall("read", map[string]any{"md5": "x", "cursor": "c1"}, first),
	}}
	if pass, why := assertReadContinuation(repeat); pass {
		t.Fatalf("a continuation returning the first chunk again must fail, got: %s", why)
	}

	second := tools.ReadOutput{Text: "chapter two", Extractable: true, HasMore: false, CharStart: 11}
	moved := transcript{Calls: []toolCall{
		okCall("read", map[string]any{"md5": "x"}, first),
		okCall("read", map[string]any{"md5": "x", "cursor": "c1"}, second),
	}}
	if pass, why := assertReadContinuation(moved); !pass {
		t.Fatalf("a genuine continuation must pass: %s", why)
	}

	stopped := transcript{Calls: []toolCall{okCall("read", map[string]any{"md5": "x"}, first)}}
	pass, why := assertReadContinuation(stopped)
	if pass || !strings.Contains(why, "SURFACE GAP") {
		t.Fatalf("reading one chunk and stopping is the surface gap under test, got pass=%v %q", pass, why)
	}
}

// TestAssertSkipConfirmationNeedsBothHalves checks the opt-out is graded on the
// argument AND on the confirmation not firing: either alone is satisfied by the
// other half being broken.
func TestAssertSkipConfirmationNeedsBothHalves(t *testing.T) {
	const md5 = "0123456789abcdef0123456789abcdef"
	search := okCall("search", map[string]any{"query": "sicp"},
		tools.SearchOutput{Results: []libgen.Result{{MD5: md5}}})
	saved := libgen.DownloadResult{Path: "/tmp/sicp.pdf", SizeBytes: 42, Source: "libgen"}

	waived := transcript{Calls: []toolCall{
		search,
		okCall("download", map[string]any{"md5": md5, "skip_confirmation": true}, saved),
	}}
	if pass, why := assertSkipConfirmation(waived); !pass {
		t.Fatalf("the opt-out taking effect must pass: %s", why)
	}

	ignored := waived
	ignored.ConfirmElicits = 1
	pass, why := assertSkipConfirmation(ignored)
	if pass || !strings.Contains(why, functionalPrefix) {
		t.Fatalf("a confirmation raised anyway is our bug, got pass=%v %q", pass, why)
	}

	never := transcript{Calls: []toolCall{search, okCall("download", map[string]any{"md5": md5}, saved)}}
	pass, why = assertSkipConfirmation(never)
	if pass || !strings.Contains(why, "SURFACE GAP") {
		t.Fatalf("never setting the argument is a surface gap, got pass=%v %q", pass, why)
	}
}
