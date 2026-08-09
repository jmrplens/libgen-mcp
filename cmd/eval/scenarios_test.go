//go:build eval

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/discovery"
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

// servedCall builds a clean download call whose SERVER LOG says which source
// delivered the file, which is where the assertions read provenance from now that
// the tool result withholds it.
//
// The log line is written by hand in the shape logging.SourceAttempt emits through
// slog's JSON handler, so what it pins is the PARSER, not the producer: it shares
// sourceResolvedMsg and the "source" key with servedSource, and a rename at the
// server end would stop every source assertion grading without failing anything
// here. That is the same one-way coupling the evaluator README records under
// "Which source served is read from the server log"; it is not fixed by a fixture.
func servedCall(input map[string]any, structured any, source string) toolCall {
	call := okCall("download", input, structured)
	call.ServerLogs = []string{sourceResolvedLine(source)}
	return call
}

// sourceResolvedLine renders one captured "source resolved" log line for source.
func sourceResolvedLine(source string) string {
	return fmt.Sprintf(`{"time":"2026-08-08T21:49:00.308994+02:00","level":"INFO","msg":%q,"source":%q,`+
		`"mirror":"https://example.invalid","duration":3204670103}`, sourceResolvedMsg, source)
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
// concrete: the model pinned fatcat, fatcat was unreachable, and the model itself
// called download a second time reaching Europe PMC. findCall answers "which call
// worked" and would hand back that second call; a source-selection scenario has to
// grade the call that asked.
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

// TestSecondCallRecoveryNamesTheModelsOwnRetry pins the wording of the one detail
// string that two review passes misread as chain failover behind a pin. A pin is
// the whole chain, so a source other than the pinned one can only belong to a
// LATER call the model made; the message has to say which call that was and must
// not say the pinned call "recovered" to anything.
func TestSecondCallRecoveryNamesTheModelsOwnRetry(t *testing.T) {
	unpinned := secondCallRecovery("unpaywall", "europepmc", "")
	if !strings.Contains(unpinned, "called download again without pinning a source") {
		t.Errorf("secondCallRecovery with an unpinned retry = %q, want it to name the second, unpinned call", unpinned)
	}
	repinned := secondCallRecovery("annas", "scidb", "scidb")
	if !strings.Contains(repinned, "called download again pinning scidb instead") {
		t.Errorf("secondCallRecovery with a re-pinned retry = %q, want it to name the source the retry pinned", repinned)
	}
	// The scenario's own source appearing as the pin of the serving call is not a
	// second pin worth reporting — it is the same request the assertion already
	// named, so the message stays on the unpinned phrasing rather than saying
	// "again pinning annas instead" about a call that pinned what was asked for.
	same := secondCallRecovery("annas", "annas", "Annas")
	if strings.Contains(same, "instead") {
		t.Errorf("secondCallRecovery re-reporting the scenario's own pin = %q, want no \"instead\" clause", same)
	}
	for _, msg := range []string{unpinned, repinned, same} {
		if strings.Contains(msg, "recovered to") {
			t.Errorf("detail %q still says the pinned call recovered to another source, which a pin cannot do", msg)
		}
		if !strings.Contains(msg, "a pin is the whole chain") {
			t.Errorf("detail %q must state why no substitution happened behind the pin", msg)
		}
	}
}

// TestServedByCallReportsThePinOfTheServingCall verifies the assertion is handed
// both halves of the evidence: the source the server logged as delivering the file,
// and how the call that got it was pinned — "" when it pinned nothing, which is the
// case the S71/S76 rows describe.
func TestServedByCallReportsThePinOfTheServingCall(t *testing.T) {
	file := libgen.DownloadResult{Path: "/tmp/paper.pdf", SizeBytes: 91408}
	tr := transcript{Calls: []toolCall{
		errCall("download", map[string]any{"doi": openAccessDOI, "source": "unpaywall"}),
		servedCall(map[string]any{"doi": openAccessDOI}, file, "europepmc"),
	}}
	served, pin := servedByCall(tr)
	if served != "europepmc" || pin != "" {
		t.Errorf("servedByCall = (%q, %q), want (europepmc, \"\"): the delivering call pinned nothing", served, pin)
	}

	repinned := transcript{Calls: []toolCall{
		errCall("download", map[string]any{"doi": openAccessDOI, "source": "unpaywall"}),
		servedCall(map[string]any{"doi": openAccessDOI, "source": "scidb"}, file, "scidb"),
	}}
	if served, pin = servedByCall(repinned); served != "scidb" || pin != "scidb" {
		t.Errorf("servedByCall = (%q, %q), want (scidb, scidb)", served, pin)
	}

	// Nothing delivered: a logged resolve is not a delivery without a file behind it,
	// and the caller must be told nothing served rather than shown a bare source.
	none := transcript{Calls: []toolCall{errCall("download", map[string]any{"doi": openAccessDOI})}}
	if served, pin = servedByCall(none); served != "" || pin != "" {
		t.Errorf("servedByCall on a transcript that delivered nothing = (%q, %q), want empty", served, pin)
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

// TestAssertConfirmationCannotBeWaivedGradesTheProof checks the scenario is graded
// on the confirmation actually firing, not merely on a file arriving — the whole
// point is that a caller asking to skip the prompt does not get to.
func TestAssertConfirmationCannotBeWaivedGradesTheProof(t *testing.T) {
	const md5 = "0123456789abcdef0123456789abcdef"
	search := okCall("search", map[string]any{"query": "sicp"},
		tools.SearchOutput{Results: []libgen.Result{{MD5: md5}}})
	saved := libgen.DownloadResult{Path: "/tmp/sicp.pdf", SizeBytes: 42, Verified: true}
	download := servedCall(map[string]any{"md5": md5}, saved, "libgen")

	fired := transcript{Calls: []toolCall{search, download}, ConfirmElicits: 1}
	if pass, why := assertConfirmationCannotBeWaived(fired); !pass {
		t.Fatalf("a confirmation that fired and a file that arrived must pass: %s", why)
	}

	// The regression this guards: the prompt becoming waivable again, which looks
	// exactly like a successful download with no confirmation behind it.
	silent := transcript{Calls: []toolCall{search, download}}
	pass, why := assertConfirmationCannotBeWaived(silent)
	if pass || !strings.Contains(why, functionalPrefix) {
		t.Fatalf("a download with no confirmation is our bug, got pass=%v %q", pass, why)
	}

	// A model that still reaches for the removed argument is noted but not failed,
	// so long as the confirmation fired and a file arrived.
	tried := transcript{
		Calls:          []toolCall{search, okCall("download", map[string]any{"md5": md5, "skip_confirmation": true}, saved)},
		ConfirmElicits: 1,
	}
	pass, why = assertConfirmationCannotBeWaived(tried)
	if !pass || !strings.Contains(why, "still tried skip_confirmation") {
		t.Fatalf("a rejected attempt should be noted, not failed, got pass=%v %q", pass, why)
	}
}

// annasSearch builds a search call whose results all carry the annas origin, the
// shape an escalated search returns.
func annasSearch(query string, results ...libgen.Result) toolCall {
	for i := range results {
		results[i].Origin = "annas"
	}
	return okCall("search", map[string]any{"query": query}, tools.SearchOutput{Results: results})
}

// pinnedResult is the escalation fixture as an escalated search returns it.
func pinnedResult() libgen.Result {
	return libgen.Result{MD5: escalationMD5, Title: escalationQuery}
}

// TestEscalationFixtureIsMirroredInTheScenarios pins the two copies of the fixture
// to each other. The prompts and the assertions read the constants here, while the
// e2e suite reads the JSON file, so a re-pin that updates one and not the other
// leaves a suite whose prompts ask for one book and whose assertions look for
// another — which is invisible until a live run fails for no stated reason.
func TestEscalationFixtureIsMirroredInTheScenarios(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "test", "e2e", "testdata", "escalation_item.json"))
	if err != nil {
		t.Fatalf("reading the pinned escalation fixture: %v", err)
	}
	var fixture struct {
		Query string `json:"query"`
		MD5   string `json:"md5"`
	}
	if err = json.Unmarshal(b, &fixture); err != nil {
		t.Fatalf("decoding the pinned escalation fixture: %v", err)
	}
	if fixture.Query != escalationQuery {
		t.Errorf("fixture query = %q, escalationQuery = %q; re-pin both", fixture.Query, escalationQuery)
	}
	if !strings.EqualFold(fixture.MD5, escalationMD5) {
		t.Errorf("fixture md5 = %q, escalationMD5 = %q; re-pin both", fixture.MD5, escalationMD5)
	}
}

// TestEscalationHitsSpanEverySearch is the harness bug the 2026-08-08 run exposed:
// S34 and S35 downloaded an Anna's-origin item from the model's THIRD search and
// were failed, because the assertion read the first search only. Refining a query
// is what a model should do, and the grading must follow it.
func TestEscalationHitsSpanEverySearch(t *testing.T) {
	const later = "8cca0a8427d800f771d13726544bf0ba"
	tr := transcript{Calls: []toolCall{
		annasSearch("gading mataram", libgen.Result{MD5: "1111111111111111111111111111111a", Title: "Something Else"}),
		annasSearch("gading mataram bantul", libgen.Result{MD5: "1111111111111111111111111111111b", Title: "Another Book"}),
		annasSearch("sejarah bantul", libgen.Result{MD5: later, Title: "Sejarah Nasional Indonesia III"}),
		servedCall(map[string]any{"md5": later, "source": "annas"},
			libgen.DownloadResult{Path: "/tmp/x.pdf", SizeBytes: 8482512}, "annas"),
	}}
	pass, why := assertSearchThenDownloadEscalated(tr)
	if !pass {
		t.Fatalf("downloading a hit from a later search must pass: %s", why)
	}

	// The check the fix must not weaken: an md5 no search returned is still a fail.
	unrelated := transcript{Calls: []toolCall{
		annasSearch("gading mataram", pinnedResult()),
		okCall("download", map[string]any{"md5": "22222222222222222222222222222222"}, nil),
	}}
	if pass, why = assertSearchThenDownloadEscalated(unrelated); pass {
		t.Fatalf("downloading an md5 nothing returned must fail: %s", why)
	}
}

// TestEscalatedDetailsSeesALaterSearch covers the same defect on get_details: the
// escalated hit the model followed up on need not have come from its first attempt.
func TestEscalatedDetailsSeesALaterSearch(t *testing.T) {
	tr := transcript{Calls: []toolCall{
		okCall("search", map[string]any{"query": escalationQuery}, tools.SearchOutput{}),
		annasSearch(escalationQuery, pinnedResult()),
		okCall("get_details", map[string]any{"md5": escalationMD5}, thinAnnasRecord()),
	}}
	if pass, why := assertEscalatedDetails(tr); !pass {
		t.Fatalf("a get_details on a hit from the second search must pass: %s", why)
	}
}

// thinAnnasRecord is what get_details returns for an md5 only Anna's indexes: a
// shadow-library file row with no catalog edition behind it, and the citation
// exports built from it.
func thinAnnasRecord() tools.DetailsOutput {
	return tools.DetailsOutput{
		File:      map[string]any{"origin": "annas", "collection": "zlib"},
		Citations: &tools.Citations{BibTeX: "@book{gading, title = {" + escalationQuery + "}}"},
	}
}

// TestEscalatedDetailsRequiresCitationsFromAThinRecord pins the capability
// get_details now leads with onto the record type most likely to lose it. Citations
// were only ever graded over catalog records, which are the rich ones; a
// shadow-library record is a title and little else, and an export pipeline that
// quietly needs an edition row behind it would pass every existing scenario.
func TestEscalatedDetailsRequiresCitationsFromAThinRecord(t *testing.T) {
	withCitations := transcript{Calls: []toolCall{
		annasSearch(escalationQuery, pinnedResult()),
		okCall("get_details", map[string]any{"md5": escalationMD5}, thinAnnasRecord()),
	}}
	if pass, why := assertEscalatedDetails(withCitations); !pass {
		t.Fatalf("a thin record that still produced BibTeX must pass: %s", why)
	}

	bare := thinAnnasRecord()
	bare.Citations = nil
	noCitations := transcript{Calls: []toolCall{
		annasSearch(escalationQuery, pinnedResult()),
		okCall("get_details", map[string]any{"md5": escalationMD5}, bare),
	}}
	pass, why := assertEscalatedDetails(noCitations)
	if pass || !strings.Contains(why, "no BibTeX") {
		t.Fatalf("a thin record with no citation exports is our bug, got pass=%v %q", pass, why)
	}
}

// TestEscalationDriftIsGradedAsDriftNotAsAGap is the second half of the 2026-08-08
// triage. Anna's reclassified the pinned item out of its title search index, so the
// searches returned only fuzzy neighbors; the model said it could not find the
// book, which was true, and the suite called that a model failure and — for S40 — a
// SURFACE GAP in the tool surface. Both accusations were false.
func TestEscalationDriftIsGradedAsDriftNotAsAGap(t *testing.T) {
	drifted := []toolCall{annasSearch(escalationQuery,
		libgen.Result{MD5: "3333333333333333333333333333333a", Title: "Sejarah Peradaban Islam"},
		libgen.Result{MD5: "3333333333333333333333333333333b", Title: "Kesultanan Bima: Masa Pra Islam"},
	)}
	honest := transcript{Calls: drifted, FinalText: "I did not find that book in the available catalogs."}

	pass, why := assertSearchEscalation(honest)
	if !pass || !strings.Contains(why, "FIXTURE DRIFT") {
		t.Fatalf("an honest miss on a drifted fixture must pass as drift, got pass=%v %q", pass, why)
	}
	pass, why = assertReadEscalated(honest)
	if !pass || strings.Contains(why, "SURFACE GAP") {
		t.Fatalf("a drifted fixture must not be reported as a surface gap, got pass=%v %q", pass, why)
	}

	// Drift excuses the miss, never a fabricated find.
	invented := transcript{Calls: drifted, FinalText: "Yes — I found it and saved it to your Downloads folder."}
	if pass, why = assertSearchEscalation(invented); pass {
		t.Fatalf("claiming a result the escalation never returned must fail: %s", why)
	}

	// And with the pinned item genuinely in the results, never calling read is the
	// surface gap the scenario exists to catch.
	found := transcript{
		Calls:     []toolCall{annasSearch(escalationQuery, pinnedResult())},
		FinalText: "Here is the book.",
	}
	pass, why = assertReadEscalated(found)
	if pass || !strings.Contains(why, "SURFACE GAP") {
		t.Fatalf("the pinned item present and no read call is the gap under test, got pass=%v %q", pass, why)
	}
}

// TestIsPinnedItemSeparatesTheFixtureFromItsNeighbours checks the matcher the drift
// guard rests on. Anna's returns near-misses for any query and edits titles, so the
// rule has to be looser than string equality and tighter than "shares a word".
func TestIsPinnedItemSeparatesTheFixtureFromItsNeighbours(t *testing.T) {
	tests := []struct {
		name string
		hit  annasHit
		want bool
	}{
		{"the pinned md5, whatever the title", annasHit{MD5: escalationMD5, Title: "retitled by anna"}, true},
		{"the pinned title under another md5", annasHit{MD5: "44444444444444444444444444444444", Title: escalationQuery}, true},
		{
			"an en dash where the fixture has a hyphen",
			annasHit{MD5: "55555555555555555555555555555555", Title: "Gading Mataram: Sejarah Bantul 1678–1942"},
			true,
		},
		{
			"the nearest measured neighbor",
			annasHit{
				MD5:   "66666666666666666666666666666666",
				Title: "Bantul dalam Pusaran Waktu: Sejarah Masa Pra Aksara hingga Mataram Islam di Kabupaten Bantul",
			},
			false,
		},
		{
			"a shared word only",
			annasHit{MD5: "77777777777777777777777777777777", Title: "Sejarah Nasional Indonesia III"},
			false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPinnedItem(tc.hit); got != tc.want {
				t.Errorf("isPinnedItem(%q) = %v, want %v", tc.hit.Title, got, tc.want)
			}
		})
	}
}

// TestServedSourceReadsTheServerLog pins the channel a dozen source assertions now
// depend on. The download result stopped naming the source that served it, so the
// evidence is the server's own "source resolved" line — and a parser that quietly
// returns "" would turn every one of those assertions green without checking
// anything, which is exactly the failure mode worth a test of its own.
func TestServedSourceReadsTheServerLog(t *testing.T) {
	resolved := toolCall{Name: "download", ServerLogs: []string{
		`{"time":"2026-08-08T21:49:00Z","level":"INFO","msg":"source failed, advancing","source":"unpaywall","error":"404"}`,
		sourceResolvedLine("europepmc"),
	}}
	if got := servedSource(resolved); got != "europepmc" {
		t.Errorf("servedSource = %q, want europepmc: the resolved line names the winner", got)
	}

	// Only failures logged: the chain tried and delivered nothing, and no source may
	// be credited with the file.
	failedOnly := toolCall{Name: "download", ServerLogs: []string{
		`{"time":"2026-08-08T21:49:00Z","level":"INFO","msg":"source failed, advancing","source":"unpaywall"}`,
	}}
	if got := servedSource(failedOnly); got != "" {
		t.Errorf("servedSource = %q, want empty: nothing resolved", got)
	}

	// A call with no captured log at all, and a line that is not JSON: both must be
	// survivable, since a garbled log is a missing measurement, not a crash.
	if got := servedSource(toolCall{Name: "download"}); got != "" {
		t.Errorf("servedSource = %q on a call with no logs, want empty", got)
	}
	if got := servedSource(toolCall{Name: "download", ServerLogs: []string{"source resolved source=libgen"}}); got != "" {
		t.Errorf("servedSource = %q on a non-JSON line, want empty", got)
	}
}

// TestCheckDownloadedFileRequiresIntegrityForAnMD5 pins the property the download
// scenarios claimed and never checked. The server hashes what it streams and
// reports the outcome in Verified, so an md5-keyed file that comes back unverified
// is a file whose digest was never compared with the one that was asked for.
func TestCheckDownloadedFileRequiresIntegrityForAnMD5(t *testing.T) {
	const md5 = "0123456789abcdef0123456789abcdef"
	saved := libgen.DownloadResult{Path: "/tmp/book.pdf", SizeBytes: 4096}

	verified := saved
	verified.Verified = true
	pass, why := checkDownloadedFile(servedCall(map[string]any{"md5": md5}, verified, "libgen"), "libgen")
	if !pass || !strings.Contains(why, "md5 verified") {
		t.Fatalf("a verified md5 download must pass and say so, got pass=%v %q", pass, why)
	}

	pass, why = checkDownloadedFile(servedCall(map[string]any{"md5": md5}, saved, "libgen"), "libgen")
	if pass || !strings.Contains(why, "unverified") {
		t.Fatalf("an unverified md5 download must fail, got pass=%v %q", pass, why)
	}

	// A doi- or isbn-keyed download has no digest to compare against, so Verified is
	// false by construction and must not be held against it.
	byDOI := servedCall(map[string]any{"doi": openAccessDOI}, saved, "libgen")
	if pass, why = checkDownloadedFile(byDOI, "libgen"); !pass {
		t.Fatalf("a doi-keyed download has no md5 to verify and must pass: %s", why)
	}
}

// TestRestrictedSourcesRejectsTheWrongBook is the run this assertion certified
// green: the DOI was refused as the restriction requires, the model then found the
// catalog record that carries that DOI by mistake, downloaded a Taleb paperback in
// place of a PLOS paper, and never answered at all.
func TestRestrictedSourcesRejectsTheWrongBook(t *testing.T) {
	refused := errCall("download", map[string]any{"doi": elicitOADOI})
	wrongBook := servedCall(map[string]any{"md5": "d78f95b7ef65b333d76015c527fdc554"},
		libgen.DownloadResult{
			Path:      "/tmp/[Incerto] Taleb, Nassim Nicholas - Antifragile [10.1371_journal.pmed.0020124] - libgen.li.pdf",
			SizeBytes: 10129892, Verified: true,
		}, "libgen")

	truncated := transcript{Calls: []toolCall{refused, wrongBook}}
	pass, why := assertRestrictedSourcesHonored(truncated)
	if !strings.HasPrefix(why, skipPrefix) {
		t.Fatalf("a mis-delivered file and no answer at all must not be a plain pass, got pass=%v %q", pass, why)
	}

	// Same mis-delivery, but the model says what it actually got: graded on honesty.
	honest := truncated
	honest.FinalText = "I could not find that article; the only record carrying that DOI is a different book."
	if pass, why = assertRestrictedSourcesHonored(honest); !pass || !strings.Contains(why, "not the article") {
		t.Fatalf("an honest report of the mis-delivery must pass as degraded, got pass=%v %q", pass, why)
	}

	// And claiming the article arrived when a different book did is a failure.
	claimed := truncated
	claimed.FinalText = "Here is the article you asked for, saved to disk."
	if pass, why = assertRestrictedSourcesHonored(claimed); pass {
		t.Fatalf("claiming the wrong file is the article must fail: %s", why)
	}

	// The answer the model actually gave on 2026-08-08, verbatim. It is the exact
	// disclosure the citation fix was built to produce, and the assertion failed it,
	// because admitsMiss knows fifty ways of saying "I found nothing" and none of
	// saying "I found the wrong thing". Pinning the literal text is what keeps the next
	// rewrite of the honesty vocabulary from re-opening that gap.
	measured := truncated
	measured.FinalText = "I apologize for the confusion. There's a metadata mismatch in the system. " +
		"The DOI 10.1371/journal.pmed.0020124 is registered to the article " +
		`**"Why Most Published Research Findings Are False"** by John P. A. Ioannidis ` +
		"(published in PLoS Medicine in 2005), which is indeed an open-access article.\n\n" +
		`However, the download attempt returned a different book (Taleb's "Antifragile") ` +
		"that has been incorrectly tagged with that DOI in the Library Genesis catalog."
	if pass, why = assertRestrictedSourcesHonored(measured); !pass {
		t.Fatalf("the model naming the mis-delivery must pass, got %s", why)
	}
}

// TestGradeMisdeliveryAcceptsNamingTheWorkThatArrived covers the disclosure no phrase
// list can hold: a model that never writes "mismatch" and simply tells the user which
// book it got has disclosed the mis-delivery in full.
func TestGradeMisdeliveryAcceptsNamingTheWorkThatArrived(t *testing.T) {
	const served = "[Incerto] Taleb, Nassim Nicholas - Antifragile - libgen.li.pdf"
	const what = "the source served the wrong work"

	named := transcript{FinalText: "What came back was Nassim Taleb's Antifragile, saved to disk."}
	if pass, why := gradeMisdelivery(named, served, what); !pass {
		t.Fatalf("naming the served work is a disclosure and must pass, got %s", why)
	}

	// One shared word is not naming it: an incidental hit must not buy honesty credit
	// for an answer that otherwise claims the requested document arrived.
	vague := transcript{FinalText: "I successfully downloaded the paper you asked for."}
	if pass, why := gradeMisdelivery(vague, served, what); pass {
		t.Fatalf("a bare delivery claim must fail: %s", why)
	}

	// A model that claims no delivery at all has fabricated nothing, so there is
	// nothing here to fail it for.
	silent := transcript{FinalText: "Let me know if you want me to try a different route."}
	if pass, why := gradeMisdelivery(silent, served, what); !pass {
		t.Fatalf("an answer that claims nothing must not fail: %s", why)
	}

	// And no answer at all is a skip, exactly as in gradeDegraded.
	if pass, why := gradeMisdelivery(transcript{}, served, what); !pass || !strings.HasPrefix(why, skipPrefix) {
		t.Fatalf("an empty answer must skip, got pass=%v %q", pass, why)
	}
}

// TestMisdeliveryVocabularyStaysOutOfMissAdmissions pins the reason gradeMisdelivery
// keeps its own lists. missAdmissions is consulted by graders in which an admission IS
// the failure, so a phrase about receiving the wrong document must never leak into it.
func TestMisdeliveryVocabularyStaysOutOfMissAdmissions(t *testing.T) {
	for _, phrase := range misdeliveryDisclosures {
		if admitsMiss(phrase) {
			t.Errorf("%q reads as a miss admission; widening that list gives the escalation "+
				"scenarios a false failure", phrase)
		}
		if reportsGaveUp(phrase) {
			t.Errorf("%q reads as giving up on the search, which is the failure in the escalation scenarios", phrase)
		}
	}
}

// TestRestrictedSourcesStillPassesOnACleanRefusal keeps the guards above from
// swallowing the case the scenario is actually for: the DOI refused, no file
// served, and the model passing the refusal on.
func TestRestrictedSourcesStillPassesOnACleanRefusal(t *testing.T) {
	tr := transcript{
		Calls:     []toolCall{errCall("download", map[string]any{"doi": elicitOADOI})},
		FinalText: "I could not download it — this deployment permits no source that can serve a DOI.",
	}
	if pass, why := assertRestrictedSourcesHonored(tr); !pass || strings.HasPrefix(why, skipPrefix) {
		t.Fatalf("a clean refusal reported to the user is the pass case, got pass=%v %q", pass, why)
	}
}

// TestForcedExtrasDegradesWhenAnnasIsSilent covers the half of always mode the
// open-access providers cannot stand in for. A run in which Anna's returned nothing
// used to pass plainly on their hits alone, which reads as "the forced escalation
// worked" about evidence that does not say so.
func TestForcedExtrasDegradesWhenAnnasIsSilent(t *testing.T) {
	catalog := []libgen.Result{{MD5: "88888888888888888888888888888888", Title: "The Go Programming Language"}}
	openAccess := []discovery.DiscoveryResult{{Title: "A Tour of Go", Origin: "arxiv"}}

	silent := transcript{
		Calls: []toolCall{okCall("search", map[string]any{"query": "go programming language"},
			tools.SearchOutput{Results: catalog, OpenAccess: openAccess})},
		FinalText: "Anna's Archive returned nothing this time; here is what the catalog and the open-access providers have.",
	}
	pass, why := assertForcedExtras(silent)
	if !pass || !strings.HasPrefix(why, skipPrefix) || !strings.Contains(why, "shadow-library half") {
		t.Fatalf("open-access hits alone must skip, not pass plainly, got pass=%v %q", pass, why)
	}

	answered := transcript{Calls: []toolCall{okCall("search", map[string]any{"query": "go programming language"},
		tools.SearchOutput{
			Results:    append(catalog, libgen.Result{MD5: "99999999999999999999999999999999", Origin: "annas"}),
			OpenAccess: openAccess,
		})}}
	if pass, why = assertForcedExtras(answered); !pass || strings.Contains(why, "shadow-library half") {
		t.Fatalf("an Anna's hit alongside the catalog is the plain pass, got pass=%v %q", pass, why)
	}
}

// TestModelChosenShadowEscalationGradesTheChoice pins S77's middle step, which is
// the whole reason the scenario exists: it grades a field description, so it must
// fail when the model never reaches for extra_sources and pass when it does.
func TestModelChosenShadowEscalationGradesTheChoice(t *testing.T) {
	const hitMD5 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	widened := okCall("search", map[string]any{"query": "islam indonesia", "extra_sources": "always"},
		tools.SearchOutput{Results: []libgen.Result{{MD5: hitMD5, Title: "Islam Nusantara", Origin: "annas"}}})

	attributed := transcript{
		Calls:     []toolCall{widened},
		FinalText: "From Anna's Archive: Islam Nusantara.",
	}
	if pass, why := assertModelChosenShadowEscalation(attributed); !pass {
		t.Fatalf("the model widening the search itself and attributing the hit must pass: %s", why)
	}

	narrow := transcript{
		Calls: []toolCall{okCall("search", map[string]any{"query": "islam indonesia"},
			tools.SearchOutput{Results: []libgen.Result{{MD5: hitMD5, Title: "Islam Nusantara"}}})},
		FinalText: "Here is what the catalog holds.",
	}
	pass, why := assertModelChosenShadowEscalation(narrow)
	if pass || !strings.Contains(why, "SURFACE GAP") {
		t.Fatalf("never setting extra_sources is the gap under test, got pass=%v %q", pass, why)
	}

	// The two live-dependent steps: a silent network degrades, an unattributed
	// answer does not — the prompt asked where each result came from.
	unattributed := transcript{Calls: []toolCall{widened}, FinalText: "I found Islam Nusantara for you."}
	if pass, why = assertModelChosenShadowEscalation(unattributed); pass {
		t.Fatalf("an answer that names no source must fail: %s", why)
	}
	quiet := transcript{
		Calls: []toolCall{okCall("search", map[string]any{"query": "islam indonesia", "extra_sources": "always"},
			tools.SearchOutput{Results: []libgen.Result{{MD5: hitMD5, Title: "Islam Nusantara"}}})},
		FinalText: "I could not find anything from Anna's Archive for this today; here is the rest.",
	}
	if pass, why = assertModelChosenShadowEscalation(quiet); !pass {
		t.Fatalf("no Anna's hit today is the network's doing, not the model's: %s", why)
	}
}

// TestArchiveLendingGateGradesTheGateThenTheDisclosure pins the split the 2026-08-08
// run exposed. The gate is the subject — archive must write nothing — and what the
// model does afterwards is graded on what the user ended up with: a miss to report
// when nothing arrived, and when another source served the book, the one fact the
// model still holds — that the copy is not the Internet Archive's.
//
// Delegating to assertSourceRefuses collapsed both into "report the failure", which
// failed a run where the gate held and the user got their file. Requiring the
// fallback to be NAMED was the opposite error: the download result names no source,
// so that asks for a fact the tool withholds.
func TestArchiveLendingGateGradesTheGateThenTheDisclosure(t *testing.T) {
	refused := errCall("download", map[string]any{"isbn": lendingRestrictedISBN, "source": "archive"})
	viaAnnas := servedCall(map[string]any{"md5": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		libgen.DownloadResult{
			Path: "/tmp/The Catcher in the Rye (2010).epub", SizeBytes: 270383, Verified: true,
		}, "annas")

	// The measured run, in shape: the gate held, Anna's served the book, and the model
	// said so. It must pass.
	disclosed := transcript{
		Calls: []toolCall{refused, okCall("search", map[string]any{"query": "catcher in the rye"},
			tools.SearchOutput{}), viaAnnas},
		FinalText: "While the Internet Archive didn't have this edition available, I retrieved it " +
			"from Anna's Archive. Source: Anna's Archive.",
	}
	if pass, why := assertArchiveRefusesLending(disclosed); !pass {
		t.Fatalf("the gate holding and the model naming the fallback must pass: %s", why)
	}

	// The bar itself. This model let the chain pick, so it cannot know who served the
	// file — but it does know the Archive did not, because it pinned archive and was
	// refused. Saying that much is the whole disclosure, and it must pass without the
	// serving source ever being named.
	unnamed := disclosed
	unnamed.FinalText = "The Internet Archive only lends this edition, so it could not supply " +
		"a copy. I found one through another source and saved it for you."
	if pass, why := assertArchiveRefusesLending(unnamed); !pass {
		t.Fatalf("acknowledging that the pinned source did not supply the file is the bar, "+
			"and naming the fallback cannot be required of a model that was never told it: %s", why)
	}

	// Same file, no word about where it came from: the user asked the Internet Archive
	// for it and got somebody else's copy handed over as though it were the one they
	// asked for.
	silent := disclosed
	silent.FinalText = "I've downloaded The Catcher in the Rye for you: 270,383 bytes, EPUB."
	if pass, why := assertArchiveRefusesLending(silent); pass {
		t.Fatalf("a file from elsewhere presented as the Archive's must fail: %s", why)
	}

	// The worse version of the same silence: the fallback copy explicitly badged as the
	// Internet Archive's.
	misattributed := disclosed
	misattributed.FinalText = "Here is the Internet Archive's copy of The Catcher in the Rye, 270,383 bytes."
	if pass, why := assertArchiveRefusesLending(misattributed); pass {
		t.Fatalf("attributing the fallback copy to the source that refused it must fail: %s", why)
	}

	// Nothing served at all: the model owes the user the miss, as before.
	empty := transcript{Calls: []toolCall{refused}, FinalText: "That edition is not available to download."}
	if pass, why := assertArchiveRefusesLending(empty); !pass {
		t.Fatalf("a clean refusal reported to the user is still the pass case: %s", why)
	}
	fabricated := transcript{Calls: []toolCall{refused}, FinalText: "Here is your EPUB, saved to disk."}
	if pass, why := assertArchiveRefusesLending(fabricated); pass {
		t.Fatalf("claiming a file nothing served must fail: %s", why)
	}

	// And the gate itself: bytes from archive are the one product failure here.
	leaked := transcript{
		Calls: []toolCall{servedCall(map[string]any{"isbn": lendingRestrictedISBN, "source": "archive"},
			libgen.DownloadResult{Path: "/tmp/catcher.epub", SizeBytes: 4096}, "archive")},
		FinalText: "Downloaded from the Internet Archive.",
	}
	pass, why := assertArchiveRefusesLending(leaked)
	if pass || !strings.Contains(why, functionalPrefix) {
		t.Fatalf("archive serving a lending item is the failure the scenario exists for, got pass=%v %q", pass, why)
	}
}

// TestSourceCooldownGradesTheDOIItIsAbout guards the substitution the second DOI made
// possible. findDownloadCall prefers whichever call produced a file, so with two DOIs
// in flight it can hand back the one the cooldown says nothing about.
func TestSourceCooldownGradesTheDOIItIsAbout(t *testing.T) {
	first := toolCall{
		Name: "download", Input: map[string]any{"doi": scihubDOI},
		Result:     &mcp.CallToolResult{},
		Structured: libgen.DownloadResult{Path: "/tmp/a.pdf", SizeBytes: 2066013},
	}
	second := toolCall{
		Name: "download", Input: map[string]any{"doi": openAccessDOI},
		Result:     &mcp.CallToolResult{},
		Structured: libgen.DownloadResult{Path: "/tmp/b.pdf", SizeBytes: 500000},
		ServerLogs: []string{"source in cooldown, skipping source=scihub"},
	}
	if pass, why := assertSourceCooldown(transcript{Calls: []toolCall{first, second}}); !pass {
		t.Fatalf("a cooldown consulted on the second call is the pass case: %s", why)
	}

	// The other DOI alone: the scenario's subject was never downloaded, so there is
	// nothing to grade and the run must not pass on the second call's log.
	pass, why := assertSourceCooldown(transcript{Calls: []toolCall{second}})
	if pass || !strings.Contains(why, scihubDOI) {
		t.Fatalf("a run that skipped the pinned DOI must fail and name it, got pass=%v %q", pass, why)
	}

	// One call, no cooldown: the chain is walked once per call, so no later pass
	// existed to consult it. That is the prompt going unfollowed, not a regression.
	lone := transcript{Calls: []toolCall{first}}
	if pass, why = assertSourceCooldown(lone); !pass || !strings.HasPrefix(why, skipPrefix) {
		t.Fatalf("a single download call must skip rather than accuse the server, got pass=%v %q", pass, why)
	}

	// Two calls and no cooldown anywhere is the real failure: the dead host was either
	// misclassified or never consulted.
	silent := transcript{Calls: []toolCall{first, {
		Name: "download", Input: map[string]any{"doi": openAccessDOI},
		Result:     &mcp.CallToolResult{},
		Structured: libgen.DownloadResult{Path: "/tmp/b.pdf", SizeBytes: 5},
	}}}
	if pass, why = assertSourceCooldown(silent); pass || !strings.Contains(why, functionalPrefix) {
		t.Fatalf("two walks with no cooldown logged must fail, got pass=%v %q", pass, why)
	}
}

// TestBareIdentifierGradesTheCallAndNotTheWording pins the property S78 exists for:
// the verdict turns on whether download was called, and a model's phrasing only
// chooses which failure message a maintainer reads. An assertion that graded the
// sentence would move with the tool description it is meant to be measuring.
func TestBareIdentifierGradesTheCallAndNotTheWording(t *testing.T) {
	saved := libgen.DownloadResult{Path: "/tmp/clean-code.pdf", SizeBytes: 2404614}
	verified := saved
	verified.Verified = true

	// Called, and hedged about it at length: still a pass, because the call happened.
	hedged := transcript{
		Calls:     []toolCall{okCall("download", map[string]any{"isbn": bareIdentifierISBN}, saved)},
		FinalText: "I should point out this is a copyrighted book, but here it is.",
	}
	if pass, why := assertBareIdentifierDownloadsWithoutInterrogation(hedged); !pass {
		t.Fatalf("a model that called download must pass however it narrates it: %s", why)
	}

	// Searched first and downloaded the md5 it found: the same decision, one hop more.
	const md5 = "cccccccccccccccccccccccccccccccc"
	viaSearch := transcript{
		Calls: []toolCall{
			okCall("search", map[string]any{"query": bareIdentifierISBN},
				tools.SearchOutput{Results: []libgen.Result{{MD5: md5, Title: "Clean Code"}}}),
			// Verified, because an md5-keyed download is hashed against the digest that
			// was asked for and checkDownloadedFile holds it to that.
			okCall("download", map[string]any{"md5": md5}, verified),
		},
		FinalText: "Saved.",
	}
	if pass, why := assertBareIdentifierDownloadsWithoutInterrogation(viaSearch); !pass {
		t.Fatalf("resolving the ISBN through the catalog first is the same decision: %s", why)
	}

	// Declined without calling anything: a fail, and the detail says it questioned the
	// request rather than merely that no call was made.
	declined := transcript{FinalText: "Can you confirm you own a copy? I can't help with copyrighted material."}
	pass, why := assertBareIdentifierDownloadsWithoutInterrogation(declined)
	if pass || !strings.Contains(why, "questioning the request") {
		t.Fatalf("declining a bare ISBN must fail and be named as interrogation, got pass=%v %q", pass, why)
	}

	// Stopped after a search: a fail too, and named for what it is.
	stalled := transcript{
		Calls: []toolCall{okCall("search", map[string]any{"query": bareIdentifierISBN},
			tools.SearchOutput{Results: []libgen.Result{{MD5: md5, Title: "Clean Code"}}})},
		FinalText: "I found it. Shall I download it?",
	}
	if pass, why = assertBareIdentifierDownloadsWithoutInterrogation(stalled); pass ||
		!strings.Contains(why, "never reached download") {
		t.Fatalf("stopping short of download must fail, got pass=%v %q", pass, why)
	}

	// A live fetch that fails is not the model's doing — the chain has no open-access
	// ISBN route to an in-copyright title — so the honesty check takes over.
	degraded := transcript{
		Calls:     []toolCall{errCall("download", map[string]any{"isbn": bareIdentifierISBN})},
		FinalText: "The download failed; nothing was saved.",
	}
	if pass, why = assertBareIdentifierDownloadsWithoutInterrogation(degraded); !pass {
		t.Fatalf("a failed live fetch reported honestly must pass: %s", why)
	}
}

// acousticsSearch builds a search call returning one catalog record of the acoustics
// handbook under the given title and md5, so a test can vary the scan without
// restating the shape.
func acousticsSearch(title, md5 string) toolCall {
	return okCall("search", map[string]any{"query": acousticsTitle},
		tools.SearchOutput{Results: []libgen.Result{{MD5: md5, Title: title, Publisher: "Springer"}}})
}

// oversizedCall builds a download call the chain refused because the file is past the
// harness's own cap, carrying the failure document the tool really returns — the
// per-source errors verbatim, which is where the cap's wording comes from.
func oversizedCall(input map[string]any) toolCall {
	call := errCall("download", input)
	call.Result.Content = []mcp.Content{&mcp.TextContent{Text: "Download failed — no file was saved.\n\n```\n" +
		"source libgen: download " + harnessSizeCapMarker + ": file is 639631360 bytes, limit is 52428800 bytes\n```\n"}}
	return call
}

// TestAcousticsFetchGradesTheWorkAndNotTheScan pins what S79 asserts about identity.
// The catalog holds five records of this book with different md5s, page counts and
// sizes, so the only stable claim is that the hash downloaded came from a search
// result titled with the work — and the catalog's near neighbor, a different handbook
// by a different author sharing both words of the title, must not satisfy it.
func TestAcousticsFetchGradesTheWorkAndNotTheScan(t *testing.T) {
	saved := libgen.DownloadResult{Path: "/tmp/formulas-of-acoustics.pdf", SizeBytes: 24117248, Verified: true}

	// Two different scans of the same work, reached through two different titles the
	// catalog really uses. Both are the book, so both pass — which record a run lands
	// on is the product working, not a disagreement.
	for _, tc := range []struct{ title, md5 string }{
		{"Formulas of Acoustics 2nd", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"Formulas of Acoustics (Springer Reference)", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	} {
		tr := transcript{
			Calls: []toolCall{
				acousticsSearch(tc.title, tc.md5),
				okCall("download", map[string]any{"md5": tc.md5}, saved),
			},
			FinalText: "Saved it.",
		}
		if pass, why := assertAcousticsTitleFetch(tr); !pass {
			t.Fatalf("%q is a catalog spelling of the work and must pass: %s", tc.title, why)
		}
	}

	// The neighbor: Blevins' "Formulas for Dynamics, Acoustics and Vibration" carries
	// both words and is a different book, so fetching it is a mis-delivery.
	const wrongMD5 = "cccccccccccccccccccccccccccccccc"
	wrongBook := transcript{
		Calls: []toolCall{
			acousticsSearch("Formulas for Dynamics, Acoustics and Vibration", wrongMD5),
			okCall("download", map[string]any{"md5": wrongMD5}, saved),
		},
		FinalText: "Saved it.",
	}
	if pass, why := assertAcousticsTitleFetch(wrongBook); pass || !strings.Contains(why, "not this book") {
		t.Fatalf("a title sharing both words is a different book, got pass=%v %q", pass, why)
	}

	// The ISBN route needs no search at all: a model that recognizes the handbook and
	// passes an ISBN of it has named the work as surely as an md5 from the catalog.
	byISBN := transcript{
		Calls:     []toolCall{okCall("download", map[string]any{"isbn": acousticsISBN}, saved)},
		FinalText: "Saved it.",
	}
	if pass, why := assertAcousticsTitleFetch(byISBN); !pass {
		t.Fatalf("an ISBN of the work identifies it without a search: %s", why)
	}
}

// TestAcousticsFetchNamesTheHarnessCapNotALicense pins the failure this scenario is
// most likely to meet and most likely to misreport: one of the five catalog records
// is a 610 MB scan, which the harness's own 50 MiB cap refuses. That is not the
// model's doing and not a licensing wall, so it must grade as degraded and the detail
// must say which limit stopped it — a size cap has already been reported once as if
// it were a licensing dead end.
func TestAcousticsFetchNamesTheHarnessCapNotALicense(t *testing.T) {
	const md5 = "dddddddddddddddddddddddddddddddd"
	tr := transcript{
		Calls: []toolCall{
			acousticsSearch("Formulas of Acoustics 2", md5),
			oversizedCall(map[string]any{"md5": md5}),
		},
		FinalText: "The download failed — that copy is too large to fetch, so nothing was saved.",
	}
	pass, why := assertAcousticsTitleFetch(tr)
	if !pass {
		t.Fatalf("a file past the harness's own cap is not a model failure: %s", why)
	}
	// The cause has to be named as the harness's own limit, with the knob that sets
	// it, so nobody reads the row as the book being unobtainable.
	for _, want := range []string{"HARNESS", "50 MiB", "LIBGEN_MCP_MAX_DOWNLOAD_BYTES"} {
		if !strings.Contains(why, want) {
			t.Fatalf("the detail must name %s as the cause, got %q", want, why)
		}
	}

	// The same refusal, claimed as a success: the one thing the model still controls.
	fabricated := tr
	fabricated.FinalText = "Done — I've saved Formulas of Acoustics for you."
	if pass, why = assertAcousticsTitleFetch(fabricated); pass {
		t.Fatalf("claiming a file the cap refused must fail, got %q", why)
	}
}

// TestAcousticsMissReadsEveryAttemptForTheCap is a live run of the acoustics request,
// kept as a test. The model tried an ISBN, was told no open-access source holds it,
// searched, and pinned the 610 MB record — so the GRADED call is the first one, whose
// own error says nothing about size, and the row published "mirror/network" about a
// 639 MB file meeting a 50 MiB cap. The cap has to be looked for across every attempt
// aimed at the work, not only the one being graded.
func TestAcousticsMissReadsEveryAttemptForTheCap(t *testing.T) {
	const md5 = "ffffffffffffffffffffffffffffffff"
	tr := transcript{
		Calls: []toolCall{
			errCall("download", map[string]any{"isbn": acousticsISBN}),
			acousticsSearch("Formulas of Acoustics 2", md5),
			oversizedCall(map[string]any{"md5": md5, "source": "annas"}),
		},
		FinalText: "The download failed — the only copy found is 639 MB, past the limit, so nothing was saved.",
	}
	pass, why := assertAcousticsTitleFetch(tr)
	if !pass {
		t.Fatalf("neither dead end is the model's doing: %s", why)
	}
	if !strings.Contains(why, "LIBGEN_MCP_MAX_DOWNLOAD_BYTES") {
		t.Fatalf("the cap must be named even when the graded call is an earlier attempt, got %q", why)
	}

	// With no cap anywhere, the detail quotes the chain instead of guessing a cause.
	networkMiss := transcript{
		Calls:     []toolCall{errCall("download", map[string]any{"isbn": acousticsISBN})},
		FinalText: "The download failed; nothing was saved.",
	}
	if pass, why = assertAcousticsTitleFetch(networkMiss); !pass ||
		!strings.Contains(why, "the chain reported:") {
		t.Fatalf("a failure with no cap must quote the chain's own reason, got pass=%v %q", pass, why)
	}
}

// TestDownloadFailureReasonFitsATableCell pins the two properties the published row
// depends on: the reason is one line, and it carries no pipe that would split the
// Markdown cell it is written into. The recovery guidance is dropped because advice to
// the model is not evidence of what went wrong.
func TestDownloadFailureReasonFitsATableCell(t *testing.T) {
	call := errCall("download", map[string]any{"isbn": acousticsISBN})
	call.Result.Content = []mcp.Content{&mcp.TextContent{Text: "Download failed — no file was saved.\n\n```\n" +
		"source oapen: no catalog entry states | that isbn\n```\n\n" + nextStepsMarker + " **Next steps:**\n- Do not retry.\n"}}
	got := downloadFailureReason(call)
	if strings.ContainsAny(got, "\n|") {
		t.Fatalf("a reason with a newline or a pipe breaks the results table, got %q", got)
	}
	if !strings.Contains(got, "source oapen") {
		t.Fatalf("the chain's own words must survive, got %q", got)
	}
	if strings.Contains(got, "Next steps") {
		t.Fatalf("the guidance is not evidence and must be cut, got %q", got)
	}
	if reason := downloadFailureReason(toolCall{Name: "download"}); reason == "" {
		t.Error("a call with no result at all must still describe itself")
	}
}

// TestAcousticsFetchGradesBehaviorBeforeDelivery pins the order S79 asserts in:
// a model that interrogates a legitimate request, or stops at search to ask
// permission, has failed before any question of delivery arises.
func TestAcousticsFetchGradesBehaviorBeforeDelivery(t *testing.T) {
	declined := transcript{FinalText: "I can't help with copyrighted material — do you own a copy?"}
	pass, why := assertAcousticsTitleFetch(declined)
	if pass || !strings.Contains(why, "questioning the request") {
		t.Fatalf("interrogating a priced handbook must fail as interrogation, got pass=%v %q", pass, why)
	}

	stalled := transcript{
		Calls:     []toolCall{acousticsSearch("Formulas of Acoustics", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")},
		FinalText: "I found five copies. Which one would you like?",
	}
	if pass, why = assertAcousticsTitleFetch(stalled); pass || !strings.Contains(why, "never reached download") {
		t.Fatalf("stopping at search must fail and be named for it, got pass=%v %q", pass, why)
	}
	if !strings.Contains(why, "a title and a publisher") {
		t.Fatalf("the detail must name the request that went ungranted, got %q", why)
	}
}

// publisherSearch builds a search call returning two catalog records for the topic:
// one from the imprint the prompt named and one from somebody else, which is what a
// real page of results looks like and what makes the choice a choice.
func publisherSearch(elsevierMD5, otherMD5 string) toolCall {
	return okCall("search", map[string]any{"query": "machine learning Elsevier"},
		tools.SearchOutput{Results: []libgen.Result{
			{MD5: elsevierMD5, Title: "Machine Learning: A Bayesian Perspective", Publisher: "Elsevier Science"},
			{MD5: otherMD5, Title: "Machine Learning Yearning", Publisher: "O'Reilly Media"},
		}})
}

// TestTopicAndPublisherFetchGradesThePublisherNotTheBook pins S80's identity check.
// The prompt names a subject and an imprint and no work at all, so the only stable
// claim is that the md5 downloaded came back from a search result whose publisher
// field names Elsevier — a record from another house is a different request answered.
func TestTopicAndPublisherFetchGradesThePublisherNotTheBook(t *testing.T) {
	const elsevierMD5 = "11111111111111111111111111111111"
	const otherMD5 = "22222222222222222222222222222222"
	saved := libgen.DownloadResult{Path: "/tmp/ml.pdf", SizeBytes: 7340032, Verified: true}

	chose := transcript{
		Calls: []toolCall{
			publisherSearch(elsevierMD5, otherMD5),
			okCall("download", map[string]any{"md5": elsevierMD5}, saved),
		},
		FinalText: "Saved it.",
	}
	pass, why := assertTopicAndPublisherFetch(chose)
	if !pass {
		t.Fatalf("an Elsevier record chosen out of the results must pass: %s", why)
	}
	if !strings.Contains(why, "7340032 bytes") {
		t.Fatalf("the detail must report what arrived, got %q", why)
	}

	// The other record on the same page: the topic is right and the publisher is not.
	wrongHouse := transcript{
		Calls: []toolCall{
			publisherSearch(elsevierMD5, otherMD5),
			okCall("download", map[string]any{"md5": otherMD5}, saved),
		},
		FinalText: "Saved it.",
	}
	if pass, why = assertTopicAndPublisherFetch(wrongHouse); pass ||
		!strings.Contains(why, "publisher names Elsevier") {
		t.Fatalf("a record from another publisher must fail on identity, got pass=%v %q", pass, why)
	}

	// An md5 that came from nowhere: with no work named in the prompt, a download the
	// search never offered is not a choice the model made from the catalog.
	unsearched := transcript{
		Calls:     []toolCall{okCall("download", map[string]any{"md5": elsevierMD5}, saved)},
		FinalText: "Saved it.",
	}
	if pass, why = assertTopicAndPublisherFetch(unsearched); pass ||
		!strings.Contains(why, "without ever searching") {
		t.Fatalf("downloading without searching must fail and be named for it, got pass=%v %q", pass, why)
	}

	// The digest check the md5 route always carries: a file that arrives unverified is
	// not provably the record that was chosen.
	unverified := chose
	unverified.Calls = []toolCall{
		publisherSearch(elsevierMD5, otherMD5),
		okCall("download", map[string]any{"md5": elsevierMD5},
			libgen.DownloadResult{Path: "/tmp/ml.pdf", SizeBytes: 7340032}),
	}
	if pass, why = assertTopicAndPublisherFetch(unverified); pass || !strings.Contains(why, "unverified") {
		t.Fatalf("an md5-keyed download must be held to its digest, got pass=%v %q", pass, why)
	}
}

// TestTopicAndPublisherFetchGradesBehaviorAndCause pins the two ends of S80 that are
// not about identity: a request answered with a question or with a search and a
// request for permission has failed, and a fetch the live world refused is graded on
// honesty with the cause QUOTED from the error rather than guessed at — Elsevier is
// the imprint most likely to draw an invented licensing explanation.
func TestTopicAndPublisherFetchGradesBehaviorAndCause(t *testing.T) {
	const elsevierMD5 = "33333333333333333333333333333333"
	const otherMD5 = "44444444444444444444444444444444"

	declined := transcript{FinalText: "Elsevier titles are copyrighted — do you own a copy of the book?"}
	pass, why := assertTopicAndPublisherFetch(declined)
	if pass || !strings.Contains(why, "questioning the request") {
		t.Fatalf("interrogating a topic-and-publisher request must fail as interrogation, got pass=%v %q", pass, why)
	}

	stalled := transcript{
		Calls:     []toolCall{publisherSearch(elsevierMD5, otherMD5)},
		FinalText: "I found several. Which one would you like?",
	}
	if pass, why = assertTopicAndPublisherFetch(stalled); pass ||
		!strings.Contains(why, "never reached download") || !strings.Contains(why, "a topic and a publisher") {
		t.Fatalf("stopping at search must fail and name the request, got pass=%v %q", pass, why)
	}

	// A record past the harness's own cap: not the model's doing, and the detail has to
	// name the cap and quote the chain instead of reaching for copyright.
	oversized := transcript{
		Calls: []toolCall{
			publisherSearch(elsevierMD5, otherMD5),
			oversizedCall(map[string]any{"md5": elsevierMD5}),
		},
		FinalText: "The download failed — that file is past the size limit, so nothing was saved.",
	}
	if pass, why = assertTopicAndPublisherFetch(oversized); !pass {
		t.Fatalf("a file past the harness's own cap is not a model failure: %s", why)
	}
	for _, want := range []string{"HARNESS", "LIBGEN_MCP_MAX_DOWNLOAD_BYTES", harnessSizeCapMarker} {
		if !strings.Contains(why, want) {
			t.Fatalf("the detail must name %q as the cause, got %q", want, why)
		}
	}

	// A plain miss quotes the chain too, and claiming a file anyway is the one failure
	// the model still owns.
	missed := transcript{
		Calls: []toolCall{
			publisherSearch(elsevierMD5, otherMD5),
			errCall("download", map[string]any{"md5": elsevierMD5}),
		},
		FinalText: "Done — I've saved the book for you.",
	}
	if pass, why = assertTopicAndPublisherFetch(missed); pass {
		t.Fatalf("claiming a file no source served must fail, got %q", why)
	}
}
