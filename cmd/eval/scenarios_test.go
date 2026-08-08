//go:build eval

package main

import (
	"bufio"
	"encoding/json"
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

// TestAssertConfirmationCannotBeWaivedGradesTheProof checks the scenario is graded
// on the confirmation actually firing, not merely on a file arriving — the whole
// point is that a caller asking to skip the prompt does not get to.
func TestAssertConfirmationCannotBeWaivedGradesTheProof(t *testing.T) {
	const md5 = "0123456789abcdef0123456789abcdef"
	search := okCall("search", map[string]any{"query": "sicp"},
		tools.SearchOutput{Results: []libgen.Result{{MD5: md5}}})
	saved := libgen.DownloadResult{Path: "/tmp/sicp.pdf", SizeBytes: 42, Source: "libgen", Verified: true}
	download := okCall("download", map[string]any{"md5": md5}, saved)

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
		okCall("download", map[string]any{"md5": later, "source": "annas"},
			libgen.DownloadResult{Path: "/tmp/x.pdf", SizeBytes: 8482512, Source: "annas"}),
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

// TestCheckDownloadedFileRequiresIntegrityForAnMD5 pins the property the download
// scenarios claimed and never checked. The server hashes what it streams and
// reports the outcome in Verified, so an md5-keyed file that comes back unverified
// is a file whose digest was never compared with the one that was asked for.
func TestCheckDownloadedFileRequiresIntegrityForAnMD5(t *testing.T) {
	const md5 = "0123456789abcdef0123456789abcdef"
	saved := libgen.DownloadResult{Path: "/tmp/book.pdf", SizeBytes: 4096, Source: "libgen"}

	verified := saved
	verified.Verified = true
	pass, why := checkDownloadedFile(okCall("download", map[string]any{"md5": md5}, verified), "libgen")
	if !pass || !strings.Contains(why, "md5 verified") {
		t.Fatalf("a verified md5 download must pass and say so, got pass=%v %q", pass, why)
	}

	pass, why = checkDownloadedFile(okCall("download", map[string]any{"md5": md5}, saved), "libgen")
	if pass || !strings.Contains(why, "unverified") {
		t.Fatalf("an unverified md5 download must fail, got pass=%v %q", pass, why)
	}

	// A doi- or isbn-keyed download has no digest to compare against, so Verified is
	// false by construction and must not be held against it.
	byDOI := okCall("download", map[string]any{"doi": openAccessDOI}, saved)
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
	wrongBook := okCall("download", map[string]any{"md5": "d78f95b7ef65b333d76015c527fdc554"},
		libgen.DownloadResult{
			Path:      "/tmp/[Incerto] Taleb, Nassim Nicholas - Antifragile [10.1371_journal.pmed.0020124] - libgen.li.pdf",
			SizeBytes: 10129892, Source: "libgen", Verified: true,
		})

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
