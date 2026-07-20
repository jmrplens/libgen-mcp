//go:build eval

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/tools"
)

// skipPrefix marks an assertion message as a SKIP (an unmet precondition or a
// flaky live mirror), not a pass or a fail.
const skipPrefix = "SKIP:"

// noDownloadCall is the failure detail when a download scenario produced no
// download tool call.
const noDownloadCall = "no download call"

// notAValidDOI is the failure detail when a download call's doi argument is not
// a syntactically valid DOI.
const notAValidDOI = "download doi is not a valid DOI"

// openAccessDOI is a stable open-access article DOI used by the DOI download
// scenario (PLoS ONE, freely available via Unpaywall / Sci-Hub).
const openAccessDOI = "10.1371/journal.pone.0000308"

// scihubDOI is a heavily-cited paywalled article DOI (Hanahan & Weinberg,
// "Hallmarks of Cancer: The Next Generation", Cell 2011) used to exercise the
// Sci-Hub source: unlike an arXiv DOI, a paywalled paper is what Sci-Hub actually
// mirrors, so the download has a real chance to complete instead of always
// skipping.
const scihubDOI = "10.1016/j.cell.2011.02.013"

// evalUnpaywallEmail is the contact email the article scenario sets so the
// unpaywall source (disabled by default without an email) is exercised.
const evalUnpaywallEmail = "mail@jmrp.io"

// scenario is one live end-to-end check: a natural-language prompt, an optional
// per-scenario environment, and an assertion over the resulting transcript.
// Assertions grade the tool name, the argument JSON shape, and whether the real
// MCP response is non-empty / well-formed — never exact catalog content.
type scenario struct {
	ID         string
	Prompt     string
	ToolChoice string
	SetupEnv   map[string]string
	Assert     func(transcript) (bool, string)
}

var (
	md5Pattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	doiPattern = regexp.MustCompile(`^10\.\d{4,9}/`)
)

// isMD5 reports whether s is a 32-character hex md5.
func isMD5(s string) bool { return md5Pattern.MatchString(strings.TrimSpace(s)) }

// isDOI reports whether s looks like a DOI (10.<registrant>/...).
func isDOI(s string) bool { return doiPattern.MatchString(strings.TrimSpace(s)) }

// hasTopic reports whether the tool input's topics array contains topic.
func hasTopic(input map[string]any, topic string) bool {
	return slices.Contains(stringSlice(input, "topics"), topic)
}

// subsetOf reports whether every value in got is one of the allowed values. An
// empty got is trivially a subset.
func subsetOf(got []string, allowed ...string) bool {
	for _, g := range got {
		if !slices.Contains(allowed, g) {
			return false
		}
	}
	return true
}

// stringSlice extracts a string slice from a JSON-decoded tool input value.
func stringSlice(input map[string]any, key string) []string {
	raw, ok := input[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, isStr := item.(string); isStr {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	default:
		return nil
	}
}

// stringField extracts a string field from a JSON-decoded tool input value.
func stringField(input map[string]any, key string) string {
	if s, ok := input[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// findCall returns the first tool call with the given name.
func findCall(tr transcript, name string) (toolCall, bool) {
	for _, c := range tr.Calls {
		if c.Name == name {
			return c, true
		}
	}
	return toolCall{}, false
}

// decodeStructured re-marshals a JSON-decoded structured content value into a
// typed target.
func decodeStructured(v, target any) error {
	if v == nil {
		return errors.New("no structured content")
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal structured: %w", err)
	}
	if err = json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode structured: %w", err)
	}
	return nil
}

// searchOutput finds the first search call and decodes its structured output.
func searchOutput(tr transcript) (toolCall, tools.SearchOutput, error) {
	call, ok := findCall(tr, "search")
	if !ok {
		return toolCall{}, tools.SearchOutput{}, errors.New("no search call")
	}
	var out tools.SearchOutput
	if err := decodeStructured(call.Structured, &out); err != nil {
		return call, tools.SearchOutput{}, err
	}
	return call, out, nil
}

// downloadFailed reports whether a download tool call did not produce a file
// (protocol error or an IsError result), which for a live mirror is treated as
// a SKIP rather than a model failure.
func downloadFailed(call toolCall) bool {
	return call.Result == nil || call.Result.IsError
}

// md5InSearchResults reports whether md5 appears in any prior search result of
// the transcript.
func md5InSearchResults(tr transcript, md5 string) bool {
	for _, c := range tr.Calls {
		if c.Name != "search" {
			continue
		}
		var out tools.SearchOutput
		if decodeStructured(c.Structured, &out) != nil {
			continue
		}
		for _, r := range out.Results {
			if strings.EqualFold(r.MD5, md5) {
				return true
			}
		}
	}
	return false
}

// doiInSearchResults reports whether doi appears in any prior search result of
// the transcript.
func doiInSearchResults(tr transcript, doi string) bool {
	for _, c := range tr.Calls {
		if c.Name != "search" {
			continue
		}
		var out tools.SearchOutput
		if decodeStructured(c.Structured, &out) != nil {
			continue
		}
		for _, r := range out.Results {
			if strings.EqualFold(r.DOI, doi) {
				return true
			}
		}
	}
	return false
}

// scenarios returns the ordered list of live scenarios.
func scenarios() []scenario {
	return []scenario{
		{
			ID:     "S1",
			Prompt: `Find the book "Introduction to Algorithms" by Cormen. It is a non-fiction textbook: search the nonfiction collection and match on the title and author fields.`,
			Assert: assertS1,
		},
		{
			ID:     "S2",
			Prompt: `Search for the research article "Attention Is All You Need" in the articles collection.`,
			Assert: assertS2,
		},
		{
			ID:     "S3",
			Prompt: `Search Library Genesis for the standard "ISO 9001" in the standards collection.`,
			Assert: assertS3,
		},
		{
			ID:     "S4",
			Prompt: `Find the book "Introduction to Algorithms" by Cormen in the nonfiction collection, then fetch the full details of the first result.`,
			Assert: assertS4,
		},
		{
			ID:     "S5",
			Prompt: `Find "The C Programming Language" by Kernighan and Ritchie and download it for me.`,
			Assert: assertS5,
		},
		{
			ID:     "S6",
			Prompt: fmt.Sprintf("Download the article with DOI %s from sci-hub.", scihubDOI),
			Assert: assertS6Scihub,
		},
		{
			ID:     "S6b",
			Prompt: `Find the book "The C Programming Language" by Kernighan and Ritchie, then download it from the randombook source.`,
			Assert: assertS6Randombook,
		},
		{
			ID:     "S7",
			Prompt: fmt.Sprintf("Download the open-access article with DOI %s.", openAccessDOI),
			// Unpaywall is disabled unless a contact email is configured; set one so
			// this scenario exercises the open-access (unpaywall) path functionally.
			SetupEnv: map[string]string{"LIBGEN_MCP_UNPAYWALL_EMAIL": evalUnpaywallEmail},
			Assert:   assertS7,
		},
		{
			ID:     "S8",
			Prompt: `Can you find me a good book?`,
			Assert: assertS8,
		},
		{
			ID:     "S9",
			Prompt: fmt.Sprintf("Download the open-access article with DOI %s from sci-hub.", openAccessDOI),
			// Force a fast, deterministic start-failure: sci-hub is the only
			// enabled source and its sole host is a dead local address, so every
			// resolve/connect attempt is refused instantly. The 1ms retry waits
			// keep the whole staged schedule sub-second while still exercising it
			// end to end, so the tool must surface the actionable could-not-start
			// error and the model must react without fabricating success.
			SetupEnv: map[string]string{
				"LIBGEN_MCP_SOURCES":                    "scihub",
				"LIBGEN_MCP_SCIHUB_HOSTS":               "127.0.0.1",
				"LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS": "1ms,1ms",
				"LIBGEN_MCP_TIMEOUT":                    "2s",
			},
			Assert: assertS9Retry,
		},
		// S10–S13 are deliberately under-specified: the prompts read like a real
		// user request and give NO guidance on which collection, search fields, or
		// download source to use. They test that the model can discover the right
		// tool arguments from the tool/field descriptions alone — a proxy for how
		// well the server self-describes to an unguided LLM.
		{
			ID:     "S10",
			Prompt: `I want to read the novel "Dune" by Frank Herbert — can you find it in the library for me?`,
			Assert: assertNaturalSearch("dune"),
		},
		{
			ID:     "S11",
			Prompt: `Find the graphic novel "Watchmen" by Alan Moore.`,
			Assert: assertNaturalSearch("watchmen"),
		},
		{
			ID:     "S12",
			Prompt: `Can you download the book "Clean Code" by Robert C. Martin for me?`,
			Assert: assertNaturalBookDownload,
		},
		{
			ID: "S13",
			Prompt: `Get me a PDF of the research paper "Hallmarks of Cancer: The Next Generation" ` +
				`by Hanahan and Weinberg.`,
			// A paywalled paper Sci-Hub actually mirrors (unlike an arXiv-only paper),
			// so the article download path can complete. The model must discover on
			// its own that articles are fetched by DOI, not md5. Unpaywall is left
			// disabled (this paper is not open access); Sci-Hub serves it.
			Assert: assertNaturalArticleDownload,
		},
		{
			ID:     "S14",
			Prompt: `Find "The C Programming Language" by Kernighan and Ritchie and download it.`,
			// Progress path: the harness attaches a progress token to every download
			// call, so a successful download must surface progress notifications to
			// the client. Asserts the notifications actually arrived end to end.
			Assert: assertDownloadProgress,
		},
		{
			ID: "S15",
			Prompt: `List 50 science fiction books sorted by year, newest first, ` +
				`as a table, and include each book's download links.`,
			// Surface test: the model must set a large page size and an ordering, and
			// — because the tool tells it to via next_steps — actually include the
			// download links in its written answer.
			Assert: assertOrderedTableWithLinks,
		},
	}
}

// assertOrderedTableWithLinks checks a large, ordered results request that asks
// for download links: the model must set a big page size and an ordering, get a
// sizable page whose results carry links, and then include those links in its
// final answer (the tool's next_steps instructs it to). A thin mirror page is a
// SKIP.
func assertOrderedTableWithLinks(tr transcript) (pass bool, detail string) {
	call, out, err := searchOutput(tr)
	if err != nil {
		return false, err.Error()
	}
	if per, _ := call.Input["results_per_page"].(float64); per < 50 {
		return false, fmt.Sprintf("results_per_page should be large (>=50) for a big list; got %v", call.Input["results_per_page"])
	}
	if stringField(call.Input, "order") == "" {
		return false, "model did not set an order for a sorted list"
	}
	if len(out.Results) < 25 {
		return true, fmt.Sprintf("%s ordered search returned only %d results from the mirror", skipPrefix, len(out.Results))
	}
	if !resultsCarryLinks(out.Results) {
		return true, skipPrefix + " results carried no download links from the mirror"
	}
	if !finalTextHasLink(tr.FinalText) {
		return false, "model did not include any download link in its answer despite the results carrying links"
	}
	return true, fmt.Sprintf("ordered page of %d with links; model surfaced links in its answer", len(out.Results))
}

// resultsCarryLinks reports whether any search result exposes a download link.
func resultsCarryLinks(results []libgen.Result) bool {
	for _, r := range results {
		for _, d := range r.Downloads {
			if d.URL != "" {
				return true
			}
		}
	}
	return false
}

// finalTextHasLink reports whether the model's final prose contains a URL or a
// Markdown link (evidence it surfaced the download links to the user).
func finalTextHasLink(s string) bool {
	return strings.Contains(s, "http://") || strings.Contains(s, "https://") || strings.Contains(s, "](")
}

// assertDownloadProgress checks that a successful download emitted progress
// notifications that reached the client (the harness attaches a progress token to
// download calls). A live fetch failure is a SKIP, since no progress can flow when
// the download never starts.
func assertDownloadProgress(tr transcript) (pass bool, detail string) {
	call, ok := findCall(tr, "download")
	if !ok {
		return false, noDownloadCall
	}
	if downloadFailed(call) {
		return true, skipPrefix + " download did not complete live, so no progress could be emitted (mirror/network)"
	}
	var last *mcp.ProgressNotificationParams
	n := 0
	for i := range tr.Progress {
		if fmt.Sprint(tr.Progress[i].ProgressToken) == downloadProgressToken {
			last = &tr.Progress[i]
			n++
		}
	}
	if n == 0 {
		return false, "download succeeded but no progress notifications reached the client"
	}
	if last.Progress <= 0 {
		return false, fmt.Sprintf("final progress notification reported no bytes (progress=%v)", last.Progress)
	}
	detail = fmt.Sprintf("received %d progress notification(s); final progress=%v total=%v", n, last.Progress, last.Total)
	return true, detail
}

// assertNaturalSearch builds an assertion for an under-specified search prompt:
// the model must translate the request into a single search call whose query
// carries the distinctive title token, with no guidance on topic or search
// fields. A mirror that returns nothing is a SKIP, not a failure.
func assertNaturalSearch(titleToken string) func(transcript) (bool, string) {
	return func(tr transcript) (pass bool, detail string) {
		call, out, err := searchOutput(tr)
		if err != nil {
			return false, err.Error()
		}
		query := strings.ToLower(stringField(call.Input, "query"))
		if query == "" {
			return false, "empty query"
		}
		if !strings.Contains(query, titleToken) {
			return false, fmt.Sprintf("query %q does not mention %q", query, titleToken)
		}
		if len(out.Results) == 0 {
			return true, skipPrefix + " search well-formed but the mirror returned 0 results"
		}
		topics := stringSlice(call.Input, "topics")
		return true, fmt.Sprintf("unguided search; %d results; topics=%v", len(out.Results), topics)
	}
}

// assertNaturalBookDownload checks an under-specified "download this book" prompt:
// the model must search, then download by an md5 it discovered — without being
// told to use md5 or which source. A live fetch failure is a SKIP.
func assertNaturalBookDownload(tr transcript) (pass bool, detail string) {
	call, ok := findCall(tr, "download")
	if !ok {
		return false, noDownloadCall
	}
	md5 := stringField(call.Input, "md5")
	if !isMD5(md5) {
		return false, "model did not download by a valid md5 (books are md5-keyed)"
	}
	if !md5InSearchResults(tr, md5) {
		return false, "download md5 did not come from a prior search result"
	}
	if downloadFailed(call) {
		return true, skipPrefix + " model discovered the md5 download flow but the live fetch failed (mirror/network)"
	}
	return checkDownloadedFile(call, "")
}

// assertNaturalArticleDownload checks an under-specified "get me the PDF of this
// paper" prompt: the model must discover that articles are keyed by DOI (not
// md5) and download by a valid DOI — no source named. Downloading by a valid DOI
// is the discovery signal under test; whether the DOI came from a prior search or
// the model already knew it is not graded (a wrong DOI would simply fail to
// resolve → SKIP, never a false pass). A live fetch failure is a SKIP.
func assertNaturalArticleDownload(tr transcript) (pass bool, detail string) {
	call, ok := findCall(tr, "download")
	if !ok {
		return false, noDownloadCall
	}
	doi := stringField(call.Input, "doi")
	if !isDOI(doi) {
		if isMD5(stringField(call.Input, "md5")) {
			return false, "model downloaded by md5; articles must be keyed by doi"
		}
		return false, "model did not download by a valid doi"
	}
	via := "known"
	if doiInSearchResults(tr, doi) {
		via = "search"
	}
	if downloadFailed(call) {
		return true, fmt.Sprintf("%s model chose a valid doi (%s) but the live fetch failed (mirror/network)", skipPrefix, via)
	}
	ok2, msg := checkDownloadedFile(call, "")
	if !ok2 {
		return ok2, msg
	}
	return true, msg + " (doi via " + via + ")"
}

// assertS1 checks a nonfiction title+author search with a valid first md5.
func assertS1(tr transcript) (pass bool, detail string) {
	call, out, err := searchOutput(tr)
	if err != nil {
		return false, err.Error()
	}
	if stringField(call.Input, "query") == "" {
		return false, "empty query"
	}
	if !hasTopic(call.Input, "nonfiction") {
		return false, "topics missing nonfiction"
	}
	if !subsetOf(stringSlice(call.Input, "search_in"), "title", "author") {
		return false, "search_in not a subset of {title, author}"
	}
	if len(out.Results) == 0 {
		return false, "search returned no results"
	}
	if !isMD5(out.Results[0].MD5) {
		return false, "first result md5 is not 32-hex"
	}
	return true, fmt.Sprintf("nonfiction search; %d results; first md5 ok", len(out.Results))
}

// assertS2 checks an articles search that yields at least one DOI.
func assertS2(tr transcript) (pass bool, detail string) {
	call, out, err := searchOutput(tr)
	if err != nil {
		return false, err.Error()
	}
	if !hasTopic(call.Input, "articles") {
		return false, "topics missing articles"
	}
	for _, r := range out.Results {
		if isDOI(r.DOI) {
			return true, "articles search; found a result with a valid DOI"
		}
	}
	return false, "no result carried a valid DOI"
}

// assertS3 checks a standards search, skipping when the mirror returns nothing.
func assertS3(tr transcript) (pass bool, detail string) {
	call, out, err := searchOutput(tr)
	if err != nil {
		return false, err.Error()
	}
	if !hasTopic(call.Input, "standards") {
		return false, "topics missing standards"
	}
	if len(out.Results) == 0 {
		return true, skipPrefix + " standards search returned 0 results from the mirror"
	}
	return true, fmt.Sprintf("standards search; %d results", len(out.Results))
}

// assertS4 checks a get_details call keyed by an md5 from a prior search result.
func assertS4(tr transcript) (pass bool, detail string) {
	call, ok := findCall(tr, "get_details")
	if !ok {
		return false, "no get_details call"
	}
	md5 := stringField(call.Input, "md5")
	if !isMD5(md5) {
		return false, "get_details md5 is not 32-hex"
	}
	if !md5InSearchResults(tr, md5) {
		return false, "get_details md5 did not come from a prior search result"
	}
	if call.Result == nil || call.Result.IsError {
		return true, skipPrefix + " details lookup failed against the live mirror"
	}
	var out tools.DetailsOutput
	if err := decodeStructured(call.Structured, &out); err != nil {
		return false, err.Error()
	}
	if len(out.File) == 0 && len(out.Edition) == 0 {
		return false, "details had neither File nor Edition"
	}
	return true, "get_details returned a File or Edition record"
}

// assertS5 checks a book download by md5 produces a saved, non-empty file.
func assertS5(tr transcript) (pass bool, detail string) {
	call, ok := findCall(tr, "download")
	if !ok {
		return false, noDownloadCall
	}
	if !isMD5(stringField(call.Input, "md5")) {
		return false, "download md5 is not 32-hex"
	}
	if downloadFailed(call) {
		return true, skipPrefix + " valid md5 download but the live fetch failed (mirror/network)"
	}
	return checkDownloadedFile(call, "")
}

// assertS6Scihub checks a source-restricted article download from sci-hub.
func assertS6Scihub(tr transcript) (pass bool, detail string) {
	return assertSourcedDownload(tr, "scihub", "doi")
}

// assertS6Randombook checks a source-restricted book download from randombook.
func assertS6Randombook(tr transcript) (pass bool, detail string) {
	return assertSourcedDownload(tr, "randombook", "md5")
}

// assertSourcedDownload checks that the model set the source arg to want and
// keyed the download by the expected identifier (doi or md5). When the live
// fetch succeeds it also confirms DownloadResult.Source == want; a live fetch
// failure is a SKIP since the model behavior under test (source selection) was
// still correct.
func assertSourcedDownload(tr transcript, want, key string) (pass bool, detail string) {
	call, ok := findCall(tr, "download")
	if !ok {
		return false, noDownloadCall
	}
	if stringField(call.Input, "source") != want {
		return false, "download source arg is not " + want
	}
	if key == "doi" && !isDOI(stringField(call.Input, "doi")) {
		return false, notAValidDOI
	}
	if key == "md5" && !isMD5(stringField(call.Input, "md5")) {
		return false, "download md5 is not 32-hex"
	}
	if downloadFailed(call) {
		return true, skipPrefix + " model set source=" + want + " correctly but the live download failed (mirror/network)"
	}
	return checkDownloadedFile(call, want)
}

// assertS7 checks an open-access DOI download served by unpaywall or sci-hub.
func assertS7(tr transcript) (pass bool, detail string) {
	call, ok := findCall(tr, "download")
	if !ok {
		return false, noDownloadCall
	}
	if !isDOI(stringField(call.Input, "doi")) {
		return false, notAValidDOI
	}
	if downloadFailed(call) {
		return true, skipPrefix + " valid DOI download but the live fetch failed (mirror/network)"
	}
	fileOK, msg := checkDownloadedFile(call, "")
	if !fileOK {
		return fileOK, msg
	}
	var res libgen.DownloadResult
	if err := decodeStructured(call.Structured, &res); err != nil {
		return false, err.Error()
	}
	if res.Source != "unpaywall" && res.Source != "scihub" {
		return false, "unexpected article source " + res.Source
	}
	return true, "downloaded DOI via " + res.Source
}

// assertS9Retry checks the staged start-retry schedule end to end: with sci-hub
// pinned to a dead host, the download must exhaust its retries and surface the
// actionable "could not start" error (naming retry-now / retry-later / ask-the-
// user recovery), and the model must react to that error rather than fabricate a
// successful download. A live success here is impossible (the host is dead), so
// an unexpected non-error result is a genuine failure, not a SKIP.
func assertS9Retry(tr transcript) (pass bool, detail string) {
	call, ok := findCall(tr, "download")
	if !ok {
		return false, noDownloadCall
	}
	if stringField(call.Input, "source") != "scihub" {
		return false, "download source arg is not scihub"
	}
	if !isDOI(stringField(call.Input, "doi")) {
		return false, notAValidDOI
	}
	if !downloadFailed(call) {
		return false, "expected the download to fail to start against the dead host, but it succeeded"
	}
	errText := strings.ToLower(resultText(call.Result))
	if !strings.Contains(errText, "retry") || !strings.Contains(errText, "ask") {
		return false, "tool error is not the actionable could-not-start message: " + errText
	}
	// Valid recovery is either relaying the failure/options to the user or
	// actively retrying the download itself; fabricating success is the failure.
	recovered := containsAny(strings.ToLower(tr.FinalText),
		"retry", "later", "again", "unable", "couldn't", "could not", "failed", "wasn't able", "ask") ||
		countCalls(tr, "download") >= 2
	if !recovered {
		return false, "model neither retried nor surfaced the start-failure to the user"
	}
	return true, "start-retries exhausted; actionable error surfaced and the model did not fabricate success"
}

// containsAny reports whether s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// countCalls counts the tool calls with the given name in the transcript.
func countCalls(tr transcript, name string) int {
	n := 0
	for _, c := range tr.Calls {
		if c.Name == name {
			n++
		}
	}
	return n
}

// assertS8 passes when the model asks to clarify (no tool call) or the search
// tool's own validation rejects the missing query.
func assertS8(tr transcript) (pass bool, detail string) {
	if len(tr.Calls) == 0 {
		return true, "model asked to clarify instead of guessing (no tool call)"
	}
	for _, c := range tr.Calls {
		if c.Name == "search" && c.Result != nil && c.Result.IsError {
			return true, "search validation rejected the underspecified query"
		}
	}
	return false, "model called a tool without clarifying the ambiguous request"
}

// checkDownloadedFile decodes a download result and confirms a non-empty saved
// path and size, plus (when want is non-empty) the serving source.
func checkDownloadedFile(call toolCall, want string) (pass bool, detail string) {
	var res libgen.DownloadResult
	if err := decodeStructured(call.Structured, &res); err != nil {
		return false, err.Error()
	}
	if res.Path == "" || res.SizeBytes <= 0 {
		return false, "download result had an empty path or zero size"
	}
	if want != "" && res.Source != want {
		return false, "DownloadResult.Source is " + res.Source + ", want " + want
	}
	return true, fmt.Sprintf("downloaded %d bytes via %s", res.SizeBytes, res.Source)
}

// selectScenarios filters scenarios by a comma-separated --only list (empty
// runs all).
func selectScenarios(all []scenario, only string) []scenario {
	only = strings.TrimSpace(only)
	if only == "" {
		return all
	}
	wanted := map[string]bool{}
	for id := range strings.SplitSeq(only, ",") {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = true
		}
	}
	var out []scenario
	for _, sc := range all {
		if wanted[sc.ID] {
			out = append(out, sc)
		}
	}
	return out
}
