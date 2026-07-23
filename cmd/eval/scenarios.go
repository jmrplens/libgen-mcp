//go:build eval

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/discovery"
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
// unpaywall source (disabled by default without an email) is exercised. It is a
// safe fallback: live runs prefer LIBGEN_MCP_UNPAYWALL_EMAIL (see unpaywallEmail).
const evalUnpaywallEmail = "mail@jmrp.io"

// elicitOADOI is a reliably open-access article DOI (Ioannidis 2005, PLOS
// Medicine) used by the on-demand Unpaywall-email elicitation scenario: with no
// email configured, only the elicited contact email can bring Unpaywall into the
// download chain for this DOI.
const elicitOADOI = "10.1371/journal.pmed.0020124"

// unpaywallEmail returns the Unpaywall contact email the eval injects: the
// LIBGEN_MCP_UNPAYWALL_EMAIL environment value when set (the Makefile eval target
// loads it from .env, so live runs use the real address), otherwise the committed
// evalUnpaywallEmail fallback so an env-less run still exercises the open-access
// path.
func unpaywallEmail() string {
	if v := strings.TrimSpace(os.Getenv("LIBGEN_MCP_UNPAYWALL_EMAIL")); v != "" {
		return v
	}
	return evalUnpaywallEmail
}

// scenario is one live end-to-end check: a natural-language prompt, an optional
// per-scenario environment, and an assertion over the resulting transcript.
// Assertions grade the tool name, the argument JSON shape, and whether the real
// MCP response is non-empty / well-formed — never exact catalog content.
type scenario struct {
	ID         string
	Prompt     string
	ToolChoice string
	SetupEnv   map[string]string
	// Remote runs the scenario against the server in remote mode (download returns
	// a link instead of writing to disk); the harness then fetches the link to the
	// sandbox dir, so the file still ends up local.
	Remote bool
	Assert func(transcript) (bool, string)
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
			SetupEnv: map[string]string{"LIBGEN_MCP_UNPAYWALL_EMAIL": unpaywallEmail()},
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
		{
			ID: "S16",
			Prompt: `Find "The C Programming Language" by Kernighan and Ritchie, then ` +
				`give me the direct download URL — do NOT download the file, I just want the link.`,
			// resolve_only path: the model must discover it can set resolve_only=true to
			// get a link back instead of downloading, and the tool must return a
			// resolved URL. A live resolve failure is a SKIP.
			Assert: assertResolveOnlyLink,
		},
		// S17–S18 are the REMOTE block: the same "download this" requests, but the
		// server runs in remote mode (download returns a link, never a saved file).
		// The model just calls download as usual; the harness then fetches the link
		// to the sandbox dir (as an agent's own fetch tool would), so the file still
		// ends up local. The local block is the ordinary download scenarios (S5/S12/
		// S13), which write to disk directly. Together they verify: same LLM behavior,
		// different server behavior, file local in both.
		{
			ID:     "S17",
			Prompt: `Find "The C Programming Language" by Kernighan and Ritchie and download it.`,
			Remote: true,
			Assert: assertRemoteDownloadLandsLocal,
		},
		{
			ID:     "S18",
			Prompt: fmt.Sprintf("Download the article with DOI %s.", scihubDOI),
			Remote: true,
			Assert: assertRemoteDownloadLandsLocal,
		},
		{
			ID: "S19",
			Prompt: `Search Library Genesis articles for the paper "Hallmarks of Cancer: The Next Generation" ` +
				`by Hanahan and Weinberg, then read the first page of the PDF (do NOT download the whole file) ` +
				`and give me a two- or three-sentence summary of what it covers.`,
			// Exercises the search -> read -> summarize flow: the model must find the
			// paper's DOI via search, call read (not download) with that DOI to extract
			// the first page's text, then write its own summary of the UNTRUSTED
			// extracted text rather than dumping it verbatim.
			Assert: assertReadSummary,
		},
		{
			ID: "S20",
			Prompt: `Search for the paper "Attention Is All You Need" and also check the open-access ` +
				`literature (arXiv, Crossref) for a freely available copy; tell me what you found, ` +
				`including its DOI or arXiv link.`,
			// Open-access discovery: like S10-S13, this is deliberately under-specified —
			// the prompt never names include_open_access, so the model must discover the
			// search field itself and then surface one of the federated open-access hits
			// (arxiv/crossref) in its answer. A live provider outage is a SKIP, not a
			// failure, since the flag/plumbing already did its job.
			Assert: assertOpenAccessDiscovery,
		},
		// S21-S26 cover the capabilities added since v1.2.0, one per capability.
		// Each is deliberately phrased like a real user request that never names the
		// tool argument under test, so a pass means the model discovered the
		// capability from the tool/field descriptions alone. Each assertion's detail
		// string is explicit about whether a FAIL is a SURFACE GAP (the MCP surface
		// under-exposed the capability to the model) or FUNCTIONAL (our own bug).
		{
			ID: "S21",
			Prompt: `Find the book "Clean Code" by Robert C. Martin and give me a BibTeX ` +
				`citation for it.`,
			// Citations: the model must search then get_details (which builds the
			// BibTeX) and surface the citation, rather than fabricate one. A citation in
			// the answer with no get_details call is the surface gap under test.
			Assert: assertCitations,
		},
		{
			ID: "S22",
			Prompt: fmt.Sprintf("Find the research article with DOI %s (Hallmarks of Cancer) "+
				"and tell me which journal it was published in and how many times it's been cited.", scihubDOI),
			// Enrichment: the model must set enrich=true on get_details to pull the
			// Crossref journal/citation metadata. The email lets OpenLibrary/Crossref
			// use the polite pool; enrichment itself is keyless.
			SetupEnv: map[string]string{"LIBGEN_MCP_UNPAYWALL_EMAIL": unpaywallEmail()},
			Assert:   assertEnrichment,
		},
		{
			ID: "S23",
			Prompt: `Find the book "The C Programming Language" by Kernighan and Ritchie, then ` +
				`search inside it for the word "pointer" and show me a passage.`,
			// read find mode: the model must call read with a find argument (in-document
			// search) rather than downloading the whole file or reading sequentially.
			Assert: assertReadFind,
		},
		{
			ID: "S24",
			Prompt: `Find a PDF of "The C Programming Language" by Kernighan and Ritchie and ` +
				`show me its table of contents / chapter list.`,
			// read outline mode: the model must call read with outline=true to get the
			// document's table of contents instead of reading its text.
			Assert: assertReadOutline,
		},
		{
			ID:     "S25",
			Prompt: fmt.Sprintf("Download the open-access article with DOI %s.", elicitOADOI),
			// Elicitation (on-demand Unpaywall email): the email is forced empty for this
			// scenario, so the only way Unpaywall can serve this DOI is the per-call email
			// the host's elicitation handler supplies. Setting it empty here overrides any
			// email the Makefile loaded from .env, guaranteeing the on-demand path fires.
			SetupEnv: map[string]string{"LIBGEN_MCP_UNPAYWALL_EMAIL": ""},
			Assert:   assertElicitedEmailDownload,
		},
		{
			ID:     "S26",
			Prompt: `Find "The Pragmatic Programmer" by Andrew Hunt and David Thomas and download it.`,
			// Elicitation (download confirmation): with the host advertising elicitation,
			// a disk-writing download now raises a save-confirmation prompt. Uses a book
			// DISTINCT from S5/S14 so it is not a verbatim duplicate of the progress
			// scenario. The host's elicitation handler bumps a per-scenario counter each
			// time it answers a confirmation, which the transcript exposes — so this
			// scenario HARD-asserts the confirmation elicitation actually fired AND the
			// download still completed, rather than only inferring it from a saved file.
			Assert: assertConfirmedDownload,
		},
		{
			ID:     "S27",
			Prompt: `Find "The C Programming Language" by Kernighan and Ritchie, then search inside it for the word "pointer" and show me a passage.`,
			Remote: true,
			Assert: assertReadFind,
		},
		{
			ID:     "S28",
			Prompt: `Find a PDF of "Structure and Interpretation of Computer Programs" and show me its table of contents.`,
			Remote: true,
			Assert: assertReadOutline,
		},
		{
			ID:     "S29",
			Prompt: `I'm researching transformer neural networks. Find me some open-access papers on the topic.`,
			Remote: true,
			Assert: assertOpenAccessDiscovery,
		},
	}
}

// assertRemoteDownloadLandsLocal checks the remote block: the model calls
// download (which, in remote mode, returns a link), and the harness — acting as
// the agent's own fetch tool — pulls that link to local disk. A live resolve or
// fetch failure is a SKIP, since the model behavior under test was still correct.
func assertRemoteDownloadLandsLocal(tr transcript) (pass bool, detail string) {
	call, ok := findCall(tr, "download")
	if !ok {
		return false, noDownloadCall
	}
	if downloadFailed(call) {
		return true, skipPrefix + " remote resolve failed live (mirror/network)"
	}
	for _, f := range tr.Fetched {
		if f.Err == "" && f.Size > 0 {
			return true, fmt.Sprintf("remote: model got a link, harness fetched %d bytes to local disk", f.Size)
		}
	}
	for _, f := range tr.Fetched {
		if f.Err != "" {
			return true, skipPrefix + " resolved a link but the live fetch failed: " + f.Err
		}
	}
	return false, "remote download returned no fetchable link that landed locally"
}

// assertResolveOnlyLink checks the resolve-only path: the model sets
// resolve_only=true on a valid md5/doi download call, and the tool returns a
// resolved URL without downloading. A live resolve failure is a SKIP.
func assertResolveOnlyLink(tr transcript) (pass bool, detail string) {
	call, ok := findCall(tr, "download")
	if !ok {
		return false, noDownloadCall
	}
	if ro, _ := call.Input["resolve_only"].(bool); !ro {
		return false, "model did not set resolve_only=true"
	}
	if !isMD5(stringField(call.Input, "md5")) && !isDOI(stringField(call.Input, "doi")) {
		return false, "resolve call carried neither a valid md5 nor doi"
	}
	if downloadFailed(call) {
		return true, skipPrefix + " model set resolve_only correctly but the live resolve failed (mirror/network)"
	}
	var out struct {
		Resolved *struct {
			URL    string `json:"url"`
			Source string `json:"source"`
		} `json:"resolved"`
	}
	if err := decodeStructured(call.Structured, &out); err != nil {
		return false, err.Error()
	}
	if out.Resolved == nil || !strings.HasPrefix(out.Resolved.URL, "http") {
		return false, "resolve_only returned no resolved URL"
	}
	return true, fmt.Sprintf("resolved a URL via %s without downloading: %s", out.Resolved.Source, out.Resolved.URL)
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

// assertReadSummary checks the search -> read -> summarize flow: the model must
// resolve the file via read (keyed by a DOI or md5 from a prior search result)
// rather than downloading the whole file, then summarize the extracted text in
// its own words rather than dumping it verbatim. It enforces the "read, don't
// download" intent by requiring a read call and asserting NO download call
// occurred in the transcript. A not-extractable file or a live fetch failure is
// a SKIP, since the model's tool-use was still correct.
// readIdentifierOK verifies the read call was keyed by an identifier that came
// from a prior search result: a valid DOI traced back to search, or a 32-hex md5
// traced back to search. Both are provenance-checked so a model that hallucinates
// an identifier and then hits a live error cannot pass as a benign skip.
func readIdentifierOK(tr transcript, call toolCall) (ok bool, detail string) {
	doi := stringField(call.Input, "doi")
	md5 := stringField(call.Input, "md5")
	switch {
	case doi != "":
		if !isDOI(doi) {
			return false, "read doi is not a valid DOI"
		}
		if !doiInSearchResults(tr, doi) {
			return false, "read doi did not come from a prior search result (model may have hallucinated it)"
		}
	case md5 != "":
		if !isMD5(md5) {
			return false, "read md5 is not 32-hex"
		}
		if !md5InSearchResults(tr, md5) {
			return false, "read md5 did not come from a prior search result (model may have hallucinated it)"
		}
	default:
		return false, "read call set neither doi nor md5"
	}
	return true, ""
}

// idInSearchResults reports whether id matches an edition_id or file_id in any
// prior search result of the transcript.
func idInSearchResults(tr transcript, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, c := range tr.Calls {
		if c.Name != "search" {
			continue
		}
		var out tools.SearchOutput
		if decodeStructured(c.Structured, &out) != nil {
			continue
		}
		for _, r := range out.Results {
			if r.EditionID == id || r.FileID == id {
				return true
			}
		}
	}
	return false
}

// detailsIdentifierGrounded verifies a get_details call was keyed by an identifier
// from a prior search result: a 32-hex md5 traced to search, or an edition/file id
// traced to search. It guards enrichment/citation provenance (S22) so a model that
// hallucinates an identifier and then hits a live error cannot pass as a benign
// skip.
func detailsIdentifierGrounded(tr transcript, call toolCall) (ok bool, why string) {
	if md5 := stringField(call.Input, "md5"); md5 != "" {
		if !isMD5(md5) {
			return false, "get_details md5 is not 32-hex"
		}
		if !md5InSearchResults(tr, md5) {
			return false, "get_details md5 did not come from a prior search result (model may have hallucinated it)"
		}
		return true, ""
	}
	if id := stringField(call.Input, "id"); id != "" {
		if !idInSearchResults(tr, id) {
			return false, "get_details id did not come from a prior search result (model may have hallucinated it)"
		}
		return true, ""
	}
	return false, "get_details call set neither md5 nor id"
}

func assertReadSummary(tr transcript) (pass bool, detail string) {
	if _, ok := findCall(tr, "search"); !ok {
		return false, "no search call"
	}
	call, ok := findCall(tr, "read")
	if !ok {
		return false, "no read call"
	}
	// The intended flow reads the first page instead of fetching the whole file;
	// a download call means the model took the wrong path.
	if _, downloaded := findCall(tr, "download"); downloaded {
		return false, "model downloaded the file instead of reading it"
	}
	// read must be keyed by an identifier from a prior search result.
	if keyed, why := readIdentifierOK(tr, call); !keyed {
		return false, why
	}
	if call.Result == nil || call.Result.IsError {
		return true, skipPrefix + " read failed against the live mirror/source chain"
	}
	var out tools.ReadOutput
	if err := decodeStructured(call.Structured, &out); err != nil {
		return false, err.Error()
	}
	if !out.Extractable {
		return true, skipPrefix + " file was not extractable (" + out.Reason + ")"
	}
	if strings.TrimSpace(out.Text) == "" {
		return false, "extractable read returned no text"
	}
	if strings.TrimSpace(tr.FinalText) == "" {
		return false, "model produced no final summary"
	}
	if strings.Contains(tr.FinalText, out.Text) {
		return false, "model dumped the extracted text verbatim instead of summarizing it"
	}
	return true, fmt.Sprintf("read %s (%d chars); model summarized it in %d chars", out.Format, len(out.Text), len(tr.FinalText))
}

// assertOpenAccessDiscovery checks the S20 open-access discovery flow: the model
// must set include_open_access itself (the prompt only asks it to "also check the
// open-access literature", it never names the field) and then surface one of the
// federated arXiv/Crossref/OpenLibrary hits in its final answer. An empty
// open_access list is a SKIP — the keyless providers are best-effort third-party
// APIs, so a live outage there is not a model failure.
func assertOpenAccessDiscovery(tr transcript) (pass bool, detail string) {
	call, out, err := searchOutput(tr)
	if err != nil {
		return false, err.Error()
	}
	include, _ := call.Input["include_open_access"].(bool)
	if !include {
		return false, "model did not set include_open_access on the search call"
	}
	if len(out.OpenAccess) == 0 {
		return true, skipPrefix + " include_open_access was set but no provider returned a hit (live network)"
	}
	if !finalTextMentionsOpenAccess(tr.FinalText, out.OpenAccess) {
		return false, "model did not reference any open-access hit (origin, doi, or pdf_url) in its answer"
	}
	return true, fmt.Sprintf("open-access discovery surfaced %d hit(s); model referenced one in its answer", len(out.OpenAccess))
}

// finalTextMentionsOpenAccess reports whether the model's final prose references
// one of the federated open-access hits, by DOI, arXiv PDF URL, or origin label —
// evidence it actually used the open_access results rather than ignoring them.
func finalTextMentionsOpenAccess(text string, hits []discovery.DiscoveryResult) bool {
	lower := strings.ToLower(text)
	for _, h := range hits {
		if h.DOI != "" && strings.Contains(lower, strings.ToLower(h.DOI)) {
			return true
		}
		if h.PDFURL != "" && strings.Contains(text, h.PDFURL) {
			return true
		}
		if h.Origin != "" && strings.Contains(lower, h.Origin) {
			return true
		}
	}
	return false
}

// finalTextHasCitation reports whether the model's final prose contains a formal
// citation, by looking for BibTeX/RIS markers a real citation carries. Used to
// distinguish a model that surfaced get_details's citation from one that answered
// without one — or, when no get_details call was made, that fabricated one.
func finalTextHasCitation(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "@book") || strings.Contains(lower, "@article") ||
		strings.Contains(lower, "author =") || strings.Contains(lower, "author=")
}

// assertCitations checks the citations flow (S21): the model must search then call
// get_details — the tool that actually builds BibTeX — and surface the citation it
// returns. A citation in the answer with NO get_details call is a SURFACE GAP (the
// model fabricated it because get_details's description did not convey it provides
// citations). Sparse metadata that yields no BibTeX, or a live details failure, is
// a SKIP.
func assertCitations(tr transcript) (pass bool, detail string) {
	call, ok := findCall(tr, "get_details")
	if !ok {
		if finalTextHasCitation(tr.FinalText) {
			return false, "SURFACE GAP: model returned a citation without calling get_details — get_details's description may not convey that it provides BibTeX/RIS"
		}
		return false, "SURFACE GAP: model produced no citation and never called get_details, where citations live"
	}
	// get_details is legitimately keyed by md5 OR an edition/file id; grade both,
	// matching assertEnrichment, so an id-keyed lookup is not a spurious failure.
	if grounded, why := detailsIdentifierGrounded(tr, call); !grounded {
		return false, "FUNCTIONAL: " + why
	}
	if call.Result == nil || call.Result.IsError {
		return true, skipPrefix + " get_details failed against the live mirror"
	}
	var out tools.DetailsOutput
	if err := decodeStructured(call.Structured, &out); err != nil {
		return false, err.Error()
	}
	if out.Citations == nil || !strings.HasPrefix(strings.TrimSpace(out.Citations.BibTeX), "@") {
		return true, skipPrefix + " get_details returned no BibTeX (record metadata too sparse to build one)"
	}
	if !finalTextHasCitation(tr.FinalText) {
		return false, "FUNCTIONAL: get_details returned BibTeX but the model did not surface a citation in its answer"
	}
	return true, "model searched, called get_details, and surfaced the returned BibTeX citation"
}

// citationWords are the citation-specific tokens that count as the model engaging
// with the "how many times has it been cited" ask. Bare "cit" is deliberately
// excluded — it also matches "explicit", "solicit", "exciting", etc., which would
// make the check spuriously pass on unrelated prose.
var citationWords = []string{"citation", "cited", "citing", "cites"}

// answerMentionsEnrichment reports whether the model's final prose engaged with the
// journal/citation ask using the enriched Crossref record: it names the journal
// (Crossref ContainerTitle, when distinctive), states the citation count, or uses a
// citation-specific word. It is a soft signal — evidence the model used the
// enrichment rather than an exact-string requirement a paraphrase would fail. It is
// trustworthy: it fails only when the answer carries none of these signals, so a
// FAIL means the model genuinely omitted the enrichment it was shown.
func answerMentionsEnrichment(answer string, cr *libgen.CrossrefWork) bool {
	lower := strings.ToLower(answer)
	if cr != nil {
		if journal := strings.ToLower(strings.TrimSpace(cr.ContainerTitle)); len(journal) > 2 && strings.Contains(lower, journal) {
			return true
		}
		if cr.CitationCount > 0 && strings.Contains(lower, strconv.Itoa(cr.CitationCount)) {
			return true
		}
	}
	return containsAny(lower, citationWords...)
}

// assertEnrichment checks the opt-in enrichment flow (S22): the model must set
// enrich=true on get_details to pull Crossref journal/citation metadata, then
// answer the journal/citation question. A get_details call WITHOUT enrich=true is a
// SURFACE GAP (the enrich flag's description did not convey it fetches external
// metadata). Crossref returning nothing, or a live details failure, is a SKIP.
func assertEnrichment(tr transcript) (pass bool, detail string) {
	call, ok := findCall(tr, "get_details")
	if !ok {
		return false, "SURFACE GAP: model never called get_details — the enrich/external-metadata capability lives there and was not discovered"
	}
	if enrich, _ := call.Input["enrich"].(bool); !enrich {
		return false, "SURFACE GAP: model called get_details but did not set enrich=true — the enrich field's description may not convey it fetches journal/citation metadata"
	}
	// Provenance: the get_details identifier must trace to a prior search result, so
	// a hallucinated md5/id that then hits a live error cannot pass as a benign skip.
	if grounded, why := detailsIdentifierGrounded(tr, call); !grounded {
		return false, "FUNCTIONAL: " + why
	}
	if call.Result == nil || call.Result.IsError {
		return true, skipPrefix + " get_details(enrich) failed against the live mirror/Crossref"
	}
	var out tools.DetailsOutput
	if err := decodeStructured(call.Structured, &out); err != nil {
		return false, err.Error()
	}
	if out.Enrichment == nil || out.Enrichment.Crossref == nil {
		return true, skipPrefix + " enrich=true but Crossref returned no metadata (best-effort external API)"
	}
	if !answerMentionsEnrichment(tr.FinalText, out.Enrichment.Crossref) {
		if strings.TrimSpace(tr.FinalText) == "" {
			return true, skipPrefix + " model exhausted its turn budget before answering (enrich data was fetched correctly)"
		}
		return false, "FUNCTIONAL: Crossref data was present but the model's answer referenced neither the journal name, the citation count, nor any citation-specific term"
	}
	return true, fmt.Sprintf("model set enrich=true; Crossref journal=%q citations=%d; model answered the ask",
		out.Enrichment.Crossref.ContainerTitle, out.Enrichment.Crossref.CitationCount)
}

// assertReadFind checks the read find mode (S23): the model must call read with a
// non-empty find argument (in-document search), not download the whole file or read
// sequentially. Downloading, or reading with no find argument, is a SURFACE GAP (the
// read tool/find field description did not convey in-document search). A
// not-extractable file, no matches, or a live fetch failure is a SKIP.
func assertReadFind(tr transcript) (pass bool, detail string) {
	if _, ok := findCall(tr, "download"); ok {
		return false, "SURFACE GAP: model downloaded the file instead of using read's find mode — the read tool description may not convey in-document search"
	}
	call, ok := findCall(tr, "read")
	if !ok {
		return false, "SURFACE GAP: model never called read — the find capability lives on read and was not discovered"
	}
	if stringField(call.Input, "find") == "" {
		return false, "SURFACE GAP: model called read sequentially with no find argument — read's find field description may not convey in-document search"
	}
	// Provenance: the read identifier must trace to a prior search result, so a
	// hallucinated md5/doi that then hits a live error cannot pass as a benign skip.
	if keyed, why := readIdentifierOK(tr, call); !keyed {
		return false, "FUNCTIONAL: " + why
	}
	if call.Result == nil || call.Result.IsError {
		return true, skipPrefix + " read(find) failed against the live mirror/source chain"
	}
	var out tools.ReadOutput
	if err := decodeStructured(call.Structured, &out); err != nil {
		return false, err.Error()
	}
	if !out.Extractable {
		return true, skipPrefix + " file was not extractable (" + out.Reason + ")"
	}
	if out.MatchCount == 0 {
		return true, skipPrefix + " read(find) ran but found no matches for the term in this copy"
	}
	if strings.TrimSpace(tr.FinalText) == "" {
		return false, "FUNCTIONAL: read(find) returned matches but the model showed no passage"
	}
	return true, fmt.Sprintf("model used read find=%q; %d match(es); model surfaced a passage",
		stringField(call.Input, "find"), out.MatchCount)
}

// assertReadOutline checks the read outline mode (S24): the model must call read
// with outline=true to get the table of contents instead of the text. A read call
// without outline=true is a SURFACE GAP (read's outline field description did not
// convey table-of-contents mode). A PDF with no embedded outline, or a live fetch
// failure, is a SKIP — the mode still ran correctly.
func assertReadOutline(tr transcript) (pass bool, detail string) {
	call, ok := findCall(tr, "read")
	if !ok {
		return false, "SURFACE GAP: model never called read — the outline capability lives on read and was not discovered"
	}
	if outline, _ := call.Input["outline"].(bool); !outline {
		return false, "SURFACE GAP: model called read without outline=true — read's outline field description may not convey table-of-contents mode"
	}
	// Provenance: the read identifier must trace to a prior search result, so a
	// hallucinated md5/doi that then hits a live error cannot pass as a benign skip.
	if keyed, why := readIdentifierOK(tr, call); !keyed {
		return false, "FUNCTIONAL: " + why
	}
	if call.Result == nil || call.Result.IsError {
		return true, skipPrefix + " read(outline) failed against the live mirror/source chain"
	}
	var out tools.ReadOutput
	if err := decodeStructured(call.Structured, &out); err != nil {
		return false, err.Error()
	}
	if !out.Extractable {
		return true, skipPrefix + " file was not extractable (" + out.Reason + ")"
	}
	if len(out.Outline) == 0 {
		return true, skipPrefix + " read(outline) ran cleanly but this PDF has no embedded table of contents"
	}
	if strings.TrimSpace(tr.FinalText) == "" {
		return false, "FUNCTIONAL: outline returned entries but the model produced no answer"
	}
	return true, fmt.Sprintf("model used read outline=true; %d table-of-contents entr(ies) returned", len(out.Outline))
}

// assertElicitedEmailDownload checks the on-demand Unpaywall-email elicitation
// (S25): the scenario configures NO email, so the only way Unpaywall can serve the
// open-access DOI is the per-call email the host's elicitation handler supplies. The
// model just has to download by the DOI; the host answers the email prompt behind
// the scenes. A source of "unpaywall" is proof the elicited email threaded through
// (the config had none). A live OA-chain failure is a SKIP — the model's tool use
// was still correct, and the elicitation surface still fired.
func assertElicitedEmailDownload(tr transcript) (pass bool, detail string) {
	call, ok := findCall(tr, "download")
	if !ok {
		return false, noDownloadCall
	}
	if !isDOI(stringField(call.Input, "doi")) {
		return false, notAValidDOI
	}
	if downloadFailed(call) {
		return true, skipPrefix + " model downloaded by DOI (host answered the email elicitation) but the live OA chain failed"
	}
	var res libgen.DownloadResult
	if err := decodeStructured(call.Structured, &res); err != nil {
		return false, err.Error()
	}
	if res.Path == "" || res.SizeBytes <= 0 {
		return false, "FUNCTIONAL: download result had an empty path or zero size"
	}
	if res.Source == "unpaywall" {
		return true, fmt.Sprintf("elicitation fired: no email was configured yet Unpaywall served %d bytes — the elicited per-call email threaded through", res.SizeBytes)
	}
	return true, fmt.Sprintf("DOI download succeeded via %s (%d bytes); the host answered any email elicitation the server raised", res.Source, res.SizeBytes)
}

// assertConfirmedDownload checks the download-confirmation elicitation (S26): with
// the host advertising elicitation, a disk-writing download raises a save
// confirmation, which the host accepts. The host's elicitation handler bumps a
// per-scenario counter (tr.ConfirmElicits) each time it answers one, so this
// scenario HARD-asserts the confirmation elicitation actually fired AND the download
// completed — not merely that a file appeared. The model downloads a book by an md5
// from a prior search result; a live fetch failure (after a confirmation fired) is a
// SKIP.
func assertConfirmedDownload(tr transcript) (pass bool, detail string) {
	call, ok := findCall(tr, "download")
	if !ok {
		return false, noDownloadCall
	}
	md5 := stringField(call.Input, "md5")
	if !isMD5(md5) {
		return false, "download md5 is not 32-hex"
	}
	if !md5InSearchResults(tr, md5) {
		return false, "FUNCTIONAL: download md5 did not come from a prior search result (model may have hallucinated it)"
	}
	if downloadFailed(call) {
		if tr.ConfirmElicits == 0 {
			return true, skipPrefix + " live fetch failed before any save-confirmation elicitation fired (mirror/network)"
		}
		return true, skipPrefix + " confirmation elicitation fired but the live fetch failed (mirror/network)"
	}
	if tr.ConfirmElicits == 0 {
		return false, "FUNCTIONAL: download completed but no save-confirmation elicitation fired — the confirmation surface did not run"
	}
	fileOK, msg := checkDownloadedFile(call, "")
	if !fileOK {
		return false, "FUNCTIONAL: " + msg
	}
	return true, fmt.Sprintf("save-confirmation elicitation fired %dx and the host accepted it; %s — confirmation did not block the flow",
		tr.ConfirmElicits, msg)
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
