//go:build eval

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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

// skipPrefix marks an assertion message as a SKIP, not a pass or a fail. It is
// reserved for the two cases where there is genuinely nothing to grade: an unmet
// precondition (a capability the deployment has not configured) and a model that
// ran out of turns before answering.
//
// A live mirror or source that fails is NOT one of them. The model's behavior is
// still fully observable there, and the only wrong move left to it is claiming a
// result it never received — so those are graded by gradeDegraded rather than
// skipped. A scenario that skips routinely is not testing anything.
const skipPrefix = "SKIP:"

// Shared detail-string fragments, so an assertion's phrasing stays consistent and
// is defined once (SonarCloud go:S1192).
const (
	// functionalPrefix marks a detail as a functional/correctness failure (as
	// opposed to a SURFACE GAP), so a reader can tell "our bug" from "the model
	// didn't discover the capability".
	functionalPrefix = "FUNCTIONAL: "
	// notExtractableDetail opens the reason a fetched file yielded no
	// extractable text (scanned/unsupported); the concrete reason is appended.
	notExtractableDetail = "the file was not extractable ("
	// badDownloadMD5Detail is the failure detail when a download call's md5 is not
	// a 32-char hex string.
	badDownloadMD5Detail = "download md5 is not 32-hex"
)

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
// annasKeyFromEnv is the Anna's Archive membership key as configured when the
// harness started. It is captured at package initialization because a scenario
// clears LIBGEN_MCP_ANNAS_KEY from the environment to force the elicitation path:
// reading it later would find the cleared value. Empty when none is configured,
// in which case the host declines that prompt — which is how an assertion learns
// there was no key, without reading the environment itself. The key is a paid
// credential and is never checked into the repository.
var annasKeyFromEnv = strings.TrimSpace(os.Getenv("LIBGEN_MCP_ANNAS_KEY"))

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
	// The first call that did not error, not simply the first call. A model that
	// gets an argument wrong, is told so, and retries has made one effective
	// choice — the one that worked — and grading the abandoned attempt instead
	// reports a success as a failure. A run where every attempt errored still
	// returns the first, so a genuine failure is still surfaced.
	var first toolCall
	var found bool
	for _, c := range tr.Calls {
		if c.Name != name {
			continue
		}
		if !found {
			first, found = c, true
		}
		if c.Result == nil || !c.Result.IsError {
			return c, true
		}
	}
	return first, found
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

// Prompt-eval scope: the four MCP prompts (acquire_book, research_topic,
// get_paper, download_troubleshoot) are NOT covered by eval scenarios BY DESIGN.
// This eval drives a model over the TOOLS to check it can discover and use each
// capability from the tool/field descriptions alone. A model never autonomously
// issues a prompts/get call: MCP prompts are surfaced by the HOST as
// slash-commands / quick actions for a human to pick, not something the model
// invokes mid-conversation. Grading them here would test the harness, not the
// model. The prompts are instead covered end to end by the e2e suite
// (test/e2e/capabilities_test.go: ListPrompts advertises all four, plus GetPrompt
// cases for each).

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
			// the prompt never names extra_sources, so the model must discover the
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
		// S30-S31 are the REMOTE variants of the enrichment (S22) and citations (S21)
		// scenarios. get_details is read-only and behaves identically in remote mode
		// (remote mode only changes download, which returns a link instead of writing
		// to disk), so these confirm the model's enrich=true / get_details behavior is
		// unchanged under --http. assertEnrichment/assertCitations grade get_details
		// alone (no download-to-disk), so they apply unchanged in remote mode.
		{
			ID: "S30",
			Prompt: fmt.Sprintf("Find the research article with DOI %s (Hallmarks of Cancer) "+
				"and tell me which journal it was published in and how many times it's been cited.", scihubDOI),
			Remote:   true,
			SetupEnv: map[string]string{"LIBGEN_MCP_UNPAYWALL_EMAIL": unpaywallEmail()},
			Assert:   assertEnrichment,
		},
		{
			ID: "S31",
			Prompt: `Find the book "Clean Code" by Robert C. Martin and give me a BibTeX ` +
				`citation for it.`,
			Remote: true,
			Assert: assertCitations,
		},
		// S32-S35 cover the extra-sources escalation: the model searches for a title
		// the Library Genesis catalog does not carry, and must still find it because
		// the search escalates to Anna's Archive automatically. The pinned fixture
		// (test/e2e/testdata/escalation_item.json) defines the query and md5.
		{
			ID:     "S32",
			Prompt: `Find the book "Sejarah Indonesia Masa Persebaran Islam sampai Zaman VOC" and tell me whether you found it.`,
			Assert: assertSearchEscalation,
		},
		{
			ID:     "S33",
			Prompt: `Find the book "Sejarah Indonesia Masa Persebaran Islam sampai Zaman VOC" and tell me whether you found it.`,
			Remote: true,
			Assert: assertSearchEscalation,
		},
		{
			ID:     "S34",
			Prompt: `Find and download the book "Sejarah Indonesia Masa Persebaran Islam sampai Zaman VOC".`,
			Assert: assertSearchThenDownloadEscalated,
		},
		{
			ID:     "S35",
			Prompt: `Find and download the book "Sejarah Indonesia Masa Persebaran Islam sampai Zaman VOC".`,
			Remote: true,
			Assert: assertSearchThenDownloadEscalated,
		},
		// S36-S37 grade the follow-up an escalated search invites. The catalog has no
		// record for an Anna's-only md5, so get_details can only answer by falling back
		// to Anna's — an earlier run of this harness caught a model walking into that
		// miss, which is why the case is graded rather than assumed.
		{
			ID: "S36",
			Prompt: `Find the book "Sejarah Indonesia Masa Persebaran Islam sampai Zaman VOC" and ` +
				`then look up its full record details; tell me what collection it comes from.`,
			Assert: assertEscalatedDetails,
		},
		{
			ID: "S37",
			Prompt: `Find the book "Sejarah Indonesia Masa Persebaran Islam sampai Zaman VOC" and ` +
				`then look up its full record details; tell me what collection it comes from.`,
			Remote: true,
			Assert: assertEscalatedDetails,
		},
		// S38-S39 grade the two deployment defaults the per-call argument falls back
		// to. Neither prompt mentions extra sources, so what is under test is the
		// server honoring its own configuration — and, for never, the model staying
		// honest about a miss instead of inventing a result.
		{
			ID:       "S38",
			Prompt:   `Find the book "Sejarah Indonesia Masa Persebaran Islam sampai Zaman VOC" and tell me whether you found it.`,
			SetupEnv: map[string]string{"LIBGEN_MCP_EXTRA_SOURCES": "never"},
			Assert:   assertNoEscalationAndHonest,
		},
		{
			ID:       "S39",
			Prompt:   `Find books about the Go programming language.`,
			SetupEnv: map[string]string{"LIBGEN_MCP_EXTRA_SOURCES": "always"},
			Assert:   assertForcedExtras,
		},
		// S40 completes the trio on an escalated item: search finds it, get_details
		// (S36) describes it, and read must be able to open it — which exercises the
		// whole keyless Anna's path end to end, download included.
		{
			ID: "S40",
			Prompt: `Find the book "Sejarah Indonesia Masa Persebaran Islam sampai Zaman VOC" and ` +
				`show me a passage of its text.`,
			Assert: assertReadEscalated,
		},
		// S41 covers the Anna's membership opt-in: the prompt says the user has an
		// account but never names the argument, so the model must discover
		// annas_member itself. The key is supplied through elicitation, never stored.
		{
			ID: "S41",
			Prompt: `Download the book "Sejarah Indonesia Masa Persebaran Islam sampai Zaman VOC". ` +
				`I have an Anna's Archive membership, so use the faster member download if you can.`,
			SetupEnv: map[string]string{"LIBGEN_MCP_ANNAS_KEY": ""},
			Assert:   assertAnnasMemberDownload,
		},
		// S42-S44 come from the classes of bug this suite has actually produced:
		// a model filling a gap it should have reported, a deployment policy a
		// caller could lift, and a capability nothing had ever exercised.
		{
			ID: "S42",
			Prompt: `Find and download the book "Zxqvintor Blemmerhaus and the Quantum Marmalade of Vresk" ` +
				`by Pellworth Quintaglio-Vandermeersch, and tell me its ISBN and page count.`,
			// Nothing by that name exists anywhere. Everything the model can do here
			// is wrong except saying so, which makes it the cleanest possible probe
			// of whether the tool surface invites invention.
			Assert: assertReportsNothingFound,
		},
		{
			ID:     "S43",
			Prompt: fmt.Sprintf("Download the open-access article with DOI %s.", elicitOADOI),
			// The deployment allows the catalog and nothing else, so no article source
			// is available at all. What is under test is that the restriction holds and
			// the model reports the refusal rather than claiming a file.
			SetupEnv: map[string]string{"LIBGEN_MCP_SOURCES": "libgen"},
			Assert:   assertRestrictedSourcesHonored,
		},
		{
			ID: "S44",
			Prompt: `Search for books about "linux" and then show me the SECOND page of results — ` +
				`I have already seen the first.`,
			// Pagination had no coverage at all. The model must discover the page
			// argument rather than re-running the same search or inventing more rows.
			Assert: assertPagination,
		},
	}
}

// fabricatedISBN matches an ISBN-13 in prose. Nothing returned one for an invented
// book, so an answer carrying one made it up.
var fabricatedISBN = regexp.MustCompile(`97[89][- 0-9]{10,}`)

// assertReportsNothingFound verifies a model asked for something that does not
// exist says so. Every tool call it can make will come up empty, so the only wrong
// move left is describing a book nobody has — which is precisely the failure a
// skipping scenario hid for as long as this suite skipped.
func assertReportsNothingFound(tr transcript) (pass bool, detail string) {
	if _, ok := findCall(tr, "search"); !ok {
		return false, "model answered without searching at all"
	}
	_, out, err := searchOutput(tr)
	if err != nil {
		return false, err.Error()
	}
	for _, r := range out.Results {
		if strings.Contains(strings.ToLower(r.Title), "blemmerhaus") {
			return true, skipPrefix + " something actually matched the invented title, so there is nothing to probe"
		}
	}
	if !admitsMiss(tr.FinalText) {
		return false, "model did not report the miss; it answered: " + firstChars(tr.FinalText, 200)
	}
	// An ISBN or a page count in the answer would be fabricated: nothing returned one.
	if fabricatedISBN.MatchString(tr.FinalText) {
		return false, "model reported the miss but still produced an ISBN: " + firstChars(tr.FinalText, 200)
	}
	return true, "nothing exists by that name and the model said so, inventing no metadata"
}

// doiDownloadOutcome reports whether the model tried a DOI download and whether
// every such attempt was refused.
func doiDownloadOutcome(tr transcript) (attempted, refused bool) {
	for _, c := range tr.Calls {
		if c.Name != "download" || stringField(c.Input, "doi") == "" {
			continue
		}
		attempted = true
		if c.Result != nil && c.Result.IsError {
			refused = true
		}
	}
	return attempted, refused
}

// sourceOutsideAllowlist returns the name of a source that served a download while
// not being permitted, or "" when every successful download came from an allowed
// one.
func sourceOutsideAllowlist(tr transcript, allowed ...string) string {
	for _, c := range tr.Calls {
		if c.Name != "download" || c.Result == nil || c.Result.IsError {
			continue
		}
		var out tools.DownloadOutput
		if derr := decodeStructured(c.Structured, &out); derr != nil {
			continue
		}
		if out.Source == "" || slices.ContainsFunc(allowed, func(a string) bool {
			return strings.EqualFold(a, out.Source)
		}) {
			continue
		}
		return out.Source
	}
	return ""
}

// assertRestrictedSourcesHonored verifies LIBGEN_MCP_SOURCES holds. The deployment
// permits the catalog only, so a DOI download has no source that can serve it: the
// tool must refuse, and the model must pass that on rather than claim a file.
func assertRestrictedSourcesHonored(tr transcript) (pass bool, detail string) {
	sawDOIAttempt, doiRefused := doiDownloadOutcome(tr)
	if sawDOIAttempt && !doiRefused {
		return false, functionalPrefix + "a DOI download was served by a deployment whose only source is the catalog"
	}
	// The real question is which source served anything that did succeed. A model
	// that finds a legitimate route through the permitted source has done well; a
	// source outside the list appearing here is the restriction leaking.
	if used := sourceOutsideAllowlist(tr, "libgen"); used != "" {
		return false, functionalPrefix + "download was served by " + used +
			", which this deployment does not permit"
	}
	if !sawDOIAttempt {
		return false, "SURFACE GAP: model never tried the DOI it was given"
	}
	if admitsMiss(tr.FinalText) {
		return true, "restriction held and the model reported the refusal instead of claiming a file"
	}
	return true, "restriction held; the model routed through the permitted source instead of the refused one"
}

// assertPagination verifies the model reaches the second page rather than
// re-running the same search or continuing the list from its own head.
func assertPagination(tr transcript) (pass bool, detail string) {
	for _, c := range tr.Calls {
		if c.Name != "search" {
			continue
		}
		if page, okNum := c.Input["page"].(float64); okNum && page >= 2 {
			var out tools.SearchOutput
			if derr := decodeStructured(c.Structured, &out); derr != nil {
				return false, derr.Error()
			}
			if out.Page < 2 {
				return false, functionalPrefix + fmt.Sprintf("asked for page %v, got page %d back", page, out.Page)
			}
			if len(out.Results) == 0 {
				return gradeDegraded(tr, "the mirror served no second page for this query")
			}
			return true, fmt.Sprintf("model set page=%v and received page %d with %d results", page, out.Page, len(out.Results))
		}
	}
	return false, "SURFACE GAP: model never set the page argument — the pagination field's description may not convey it"
}

// assertNoEscalationAndHonest verifies the never mode is honored and the model does
// not paper over the resulting miss. A catalog-only search for a title the catalog
// lacks must return no extra-origin hits at all, and the model must say so rather
// than describe a file it never saw.
func assertNoEscalationAndHonest(tr transcript) (pass bool, detail string) {
	_, out, err := searchOutput(tr)
	if err != nil {
		return false, err.Error()
	}
	for _, r := range out.Results {
		if r.Origin == "annas" {
			return false, functionalPrefix + "never mode still returned Anna's-origin results"
		}
	}
	if len(out.OpenAccess) > 0 {
		return false, functionalPrefix + "never mode still returned open-access hits"
	}
	if admitsMiss(tr.FinalText) {
		return true, "never mode honored and the model reported the miss honestly"
	}
	// The answer is quoted so a maintainer can tell a fabricated result from a
	// phrasing this list simply does not recognize.
	return false, "model did not report the catalog miss; it answered: " + firstChars(tr.FinalText, 200)
}

// missAdmissions are the ways a model says it found nothing, or could not read
// what it found. The list is broad, because a false failure here would accuse a
// model of fabricating when it was merely being polite about it — but every entry
// is a phrase, never a bare word. "error" or "nothing" on their own appear in
// plenty of successful answers, and matching those would hand out honesty credit
// to exactly the fabrications this is meant to catch.
var missAdmissions = []string{
	"not found", "no results", "no result", "couldn't find", "could not find",
	"unable to find", "not able to find", "no matches", "no match", "wasn't able",
	"was not able", "didn't find", "did not find", "doesn't appear", "does not appear",
	"found nothing", "no books", "no record", "not available", "isn't available",
	"is not available", "no luck", "came up empty",
	// Reading a file that turned out to be unreadable.
	"not extractable", "no text", "couldn't extract", "could not extract",
	"unable to extract", "couldn't read", "could not read", "unable to read",
	"scanned", "image-only", "image only", "no table of contents", "no outline",
	"could not be extracted", "cannot be extracted", "could not be read",
	"cannot be read", "download failed", "could not download", "unable to download",
}

// admitsMiss reports whether an answer acknowledges coming up empty — no result,
// or a result it could not read. It is how a degraded live run is still graded:
// the model cannot control whether a PDF is a scan, but it can control whether it
// says so instead of inventing the contents.
func admitsMiss(answer string) bool {
	lower := strings.ToLower(answer)
	for _, admission := range missAdmissions {
		if strings.Contains(lower, admission) {
			return true
		}
	}
	return false
}

// gradeDegraded grades a scenario whose live payload did not arrive. The model's
// behavior is still fully observable, and the only wrong move left is to claim a
// result it never received — so this asserts honesty rather than skipping.
//
// It exists because a scenario that routinely skips is not testing anything: the
// live world varies, but what the model does about it should not.
func gradeDegraded(tr transcript, what string) (pass bool, detail string) {
	// No answer at all is the one case with nothing to judge: a model that ran out
	// of turns has not fabricated anything, so this stays a skip.
	if strings.TrimSpace(tr.FinalText) == "" {
		return true, skipPrefix + " " + what + "; the model produced no final answer (turn budget)"
	}
	if admitsMiss(tr.FinalText) {
		return true, what + "; the model reported that plainly instead of inventing a result"
	}
	return false, what + "; the model did not say so, it answered: " + firstChars(tr.FinalText, 160)
}

// firstChars returns up to n characters of s with newlines flattened, for
// embedding an answer in a one-line assertion message.
func firstChars(s string, n int) string {
	flat := strings.Join(strings.Fields(s), " ")
	if len(flat) <= n {
		return flat
	}
	return flat[:n] + "…"
}

// assertForcedExtras verifies the always mode consults the extra searchers even when
// the catalog answers. The query is an ordinary one the catalog has plenty of, so
// extra-origin hits can only be there because the mode forced them.
func assertForcedExtras(tr transcript) (pass bool, detail string) {
	_, out, err := searchOutput(tr)
	if err != nil {
		return false, err.Error()
	}
	if len(out.Results) == 0 {
		return gradeDegraded(tr, "the catalog returned nothing for an ordinary query (live mirror)")
	}
	var fromAnnas int
	for _, r := range out.Results {
		if r.Origin == "annas" {
			fromAnnas++
		}
	}
	if fromAnnas == 0 && len(out.OpenAccess) == 0 {
		return gradeDegraded(tr, "always mode ran but no extra searcher returned a hit (live network)")
	}
	return true, fmt.Sprintf("always mode consulted the extras alongside a %d-result catalog page (annas=%d, open access=%d)",
		len(out.Results), fromAnnas, len(out.OpenAccess))
}

// readTracesToEscalation reports whether a read call is reading the escalated item,
// by either route the model may take: keyed directly by the md5, or by the path of
// a file it downloaded with that md5 first.
//
// Requiring the md5 alone would fail a model that did the arguably better thing —
// download the escalated item, then read the local file — which is what the search
// guidance now steers it toward by naming the source to pin.
func readTracesToEscalation(tr transcript, call toolCall, annasMD5 map[string]bool) bool {
	if annasMD5[strings.ToLower(stringField(call.Input, "md5"))] {
		return true
	}
	if stringField(call.Input, "path") == "" {
		return false
	}
	// A path only counts when the transcript shows it was produced by downloading
	// one of the escalated md5s, so an unrelated local file cannot pass.
	for _, c := range tr.Calls {
		if c.Name != "download" || c.Result == nil || c.Result.IsError {
			continue
		}
		if annasMD5[strings.ToLower(stringField(c.Input, "md5"))] {
			return true
		}
	}
	return false
}

// assertReadEscalated verifies read works on an md5 only Anna's indexes. It is the
// strictest of the escalation scenarios: reading requires the file itself, so a
// pass means search, the Anna's download path and text extraction all worked.
func assertReadEscalated(tr transcript) (pass bool, detail string) {
	_, out, err := searchOutput(tr)
	if err != nil {
		return false, err.Error()
	}
	annasMD5 := map[string]bool{}
	for _, r := range out.Results {
		if r.Origin == "annas" && r.MD5 != "" {
			annasMD5[strings.ToLower(r.MD5)] = true
		}
	}
	if len(annasMD5) == 0 {
		return gradeDegraded(tr, "search returned no Anna's-origin results today (live network)")
	}
	call, ok := findCall(tr, "read")
	if !ok {
		return false, "SURFACE GAP: model never called read on the escalated result"
	}
	if !readTracesToEscalation(tr, call, annasMD5) {
		return false, functionalPrefix + "read was called on something that did not come from the escalated results"
	}
	if call.Result == nil || call.Result.IsError {
		return gradeDegraded(tr, "read of the escalated item failed live (Anna's/IPFS unavailable)")
	}
	var read tools.ReadOutput
	if derr := decodeStructured(call.Structured, &read); derr != nil {
		return false, derr.Error()
	}
	if !read.Extractable {
		return gradeDegraded(tr, "the escalated item was not extractable ("+read.Reason+")")
	}
	return true, fmt.Sprintf("read opened an Anna's-only item (%d chars extracted)", len(read.Text))
}

// assertAnnasMemberDownload verifies the membership opt-in is discoverable: the
// prompt says the user has an account without naming annas_member, so the model
// must set it. The key itself arrives through elicitation, so this also proves that
// prompt is answerable end to end.
func assertAnnasMemberDownload(tr transcript) (pass bool, detail string) {
	// Read from the transcript, not from the environment: this scenario clears the
	// configured key so the elicitation always fires, and the host answers it with
	// the key it has — accepting when one exists, declining when none does. Deriving
	// it this way keeps the assertion a pure function of the transcript, which is
	// what lets a recorded run be re-graded later.
	for _, e := range tr.Elicitations {
		if strings.Contains(strings.ToLower(e.Field), "key") && e.Action != "accept" {
			return true, skipPrefix + " no Anna's membership key was available, so the member tier cannot be exercised"
		}
	}
	call, ok := findCall(tr, "download")
	if !ok {
		return false, noDownloadCall
	}
	if member, _ := call.Input["annas_member"].(bool); !member {
		return false, "SURFACE GAP: model never set annas_member despite the user offering a membership"
	}
	// The scenario is about discovering the argument, which the check above already
	// settled. A spent allowance or an unreachable file host is the account's
	// business, not the tool surface's — so it is graded on honesty, not skipped.
	if downloadFailed(call) {
		return gradeDegraded(tr, "member download failed live (quota, mirror or gateway)")
	}
	var out tools.DownloadOutput
	if derr := decodeStructured(call.Structured, &out); derr != nil {
		return false, derr.Error()
	}
	if out.Account == nil {
		return true, "model set annas_member; the download went over the keyless path, so no allowance was reported"
	}
	return true, fmt.Sprintf("member download reported the account allowance (%d of %d left)",
		out.Account.DownloadsLeft, out.Account.DownloadsPerDay)
}

// assertEscalatedDetails verifies get_details answered for an md5 only Anna's
// indexes. A FAIL here is FUNCTIONAL: the search told the model to call
// get_details, so the tool must serve the md5 the search returned. A model that
// never reached get_details is a SURFACE GAP and also fails.
func assertEscalatedDetails(tr transcript) (pass bool, detail string) {
	_, out, err := searchOutput(tr)
	if err != nil {
		return false, err.Error()
	}
	var annas bool
	for _, r := range out.Results {
		if r.Origin == "annas" {
			annas = true
			break
		}
	}
	if !annas {
		return gradeDegraded(tr, "search returned no Anna's-origin results today (live network)")
	}
	call, ok := findCall(tr, "get_details")
	if !ok {
		return false, "model never called get_details on the escalated result"
	}
	var details tools.DetailsOutput
	if derr := decodeStructured(call.Structured, &details); derr != nil {
		return false, "get_details returned no usable record: " + derr.Error()
	}
	origin, _ := details.File["origin"].(string)
	if origin != "annas" {
		return false, fmt.Sprintf("get_details record origin = %q, want annas (the catalog has no record for this md5)", origin)
	}
	return true, fmt.Sprintf("get_details fell back to Anna's for the escalated md5 (collection=%v)", details.File["collection"])
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
	// What is under test is the remote contract: the model calls download and the
	// server hands back a link instead of a file. Whether the publisher then serves
	// the harness is the publisher's decision, not this project's behavior, so it is
	// evidence rather than the gate.
	if downloadFailed(call) {
		return gradeDegraded(tr, "remote resolve failed live (mirror/network)")
	}
	for _, f := range tr.Fetched {
		if f.Err == "" && f.Size > 0 {
			return true, fmt.Sprintf("remote: model got a link, harness fetched %d bytes to local disk", f.Size)
		}
	}
	for _, f := range tr.Fetched {
		if f.Err != "" {
			return true, "remote: model got a link and the server returned it; the harness's own fetch was refused upstream (" + f.Err + ")"
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
		return gradeDegraded(tr, "model set resolve_only correctly but the live resolve failed (mirror/network)")
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
	return true, fmt.Sprintf("resolved a URL via %s without downloading: %s", out.Resolved.Source, redactURL(out.Resolved.URL))
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
		return gradeDegraded(tr, fmt.Sprintf("ordered search returned only %d results from the mirror", len(out.Results)))
	}
	if !resultsCarryLinks(out.Results) {
		return gradeDegraded(tr, "results carried no download links from the mirror")
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
		return gradeDegraded(tr, "download did not complete live, so no progress could be emitted (mirror/network)")
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
			return gradeDegraded(tr, "search well-formed but the mirror returned 0 results")
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
		return gradeDegraded(tr, "model discovered the md5 download flow but the live fetch failed (mirror/network)")
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
		return gradeDegraded(tr, fmt.Sprintf("model chose a valid doi (%s) but the live fetch failed (mirror/network)", via))
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
		return gradeDegraded(tr, "standards search returned 0 results from the mirror")
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
		return gradeDegraded(tr, "details lookup failed against the live mirror")
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

// readTextGrounded reports whether any read call actually returned text or
// matches, so an answer describing contents has a source in the transcript rather
// than being invented. It is what separates a model that compiled an answer from
// what it read from one that made it up.
func readTextGrounded(tr transcript) bool {
	for _, c := range tr.Calls {
		if c.Name != "read" || c.Result == nil || c.Result.IsError {
			continue
		}
		var out tools.ReadOutput
		if err := decodeStructured(c.Structured, &out); err != nil {
			continue
		}
		if strings.TrimSpace(out.Text) != "" || len(out.Matches) > 0 {
			return true
		}
	}
	return false
}

// findOutlineCall returns the read call to grade an outline scenario against: the
// one that actually produced a table of contents, if any, else the first read.
//
// A model handed a copy with no embedded outline may legitimately try another
// copy, and grading its first attempt would call a correct recovery a fabrication
// — which is exactly what a live run reported before this existed.
func findOutlineCall(tr transcript) (toolCall, bool) {
	var firstOutline toolCall
	var haveOutline bool
	for _, c := range tr.Calls {
		if c.Name != "read" {
			continue
		}
		// Only a call that asked for an outline can be graded as one. Falling back
		// to any read at all would report a model that did set outline=true, then
		// read sequentially, as never having discovered the capability.
		if asked, _ := c.Input["outline"].(bool); !asked {
			continue
		}
		if !haveOutline {
			firstOutline, haveOutline = c, true
		}
		if c.Result == nil || c.Result.IsError {
			continue
		}
		var out tools.ReadOutput
		if err := decodeStructured(c.Structured, &out); err != nil {
			continue
		}
		if len(out.Outline) > 0 {
			return c, true
		}
	}
	if haveOutline {
		return firstOutline, true
	}
	// No outline call at all: hand back any read so the assertion can report the
	// missing argument rather than the missing call.
	return findCall(tr, "read")
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
	// A DOI needs no grounding in a search result: it is an identifier the user
	// supplies directly, and looking it up is the whole point of accepting it.
	if stringField(call.Input, "doi") != "" {
		return true, ""
	}
	return false, "get_details call set none of md5, id or doi"
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
		return gradeDegraded(tr, "read failed against the live mirror/source chain")
	}
	var out tools.ReadOutput
	if err := decodeStructured(call.Structured, &out); err != nil {
		return false, err.Error()
	}
	// Whether today's copy is a scan is live luck; whether the model then invents a
	// summary of text it never saw is exactly what this scenario should catch.
	if !out.Extractable {
		return gradeDegraded(tr, notExtractableDetail+out.Reason+")")
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

// redactURL strips a resolved link's query string, keeping the host and path.
//
// A libgen download URL carries a short-lived access key in its query, and these
// messages are published verbatim in the results tables — a static analyzer flagged
// one as a leaked credential, correctly. What the message is for is showing which
// host answered, which survives redaction intact.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparseable URL)"
	}
	if u.RawQuery == "" {
		return u.String()
	}
	u.RawQuery = ""
	return u.String() + "?(query redacted)"
}

// assertOpenAccessDiscovery checks the S20 open-access discovery flow: the model
// must set extra_sources itself (the prompt only asks it to "also check the
// open-access literature", it never names the field) and then surface one of the
// federated arXiv/Crossref/OpenLibrary hits in its final answer. An empty
// open_access list is a SKIP — the keyless providers are best-effort third-party
// APIs, so a live outage there is not a model failure.
func assertOpenAccessDiscovery(tr transcript) (pass bool, detail string) {
	call, out, err := searchOutput(tr)
	if err != nil {
		return false, err.Error()
	}
	extra, _ := call.Input["extra_sources"].(string)
	if extra != "always" {
		return false, "model did not set extra_sources to \"always\" on the search call"
	}
	if len(out.OpenAccess) == 0 {
		return gradeDegraded(tr, "extra_sources was set but no provider returned a hit (live network)")
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
		return false, functionalPrefix + why
	}
	if call.Result == nil || call.Result.IsError {
		return gradeDegraded(tr, "get_details failed against the live mirror")
	}
	var out tools.DetailsOutput
	if err := decodeStructured(call.Structured, &out); err != nil {
		return false, err.Error()
	}
	if out.Citations == nil || !strings.HasPrefix(strings.TrimSpace(out.Citations.BibTeX), "@") {
		return gradeDegraded(tr, "get_details returned no BibTeX (record metadata too sparse to build one)")
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
		return false, functionalPrefix + why
	}
	if call.Result == nil || call.Result.IsError {
		return gradeDegraded(tr, "get_details(enrich) failed against the live mirror/Crossref")
	}
	var out tools.DetailsOutput
	if err := decodeStructured(call.Structured, &out); err != nil {
		return false, err.Error()
	}
	if out.Enrichment == nil || out.Enrichment.Crossref == nil {
		return gradeDegraded(tr, "enrich=true but Crossref returned no metadata (best-effort external API)")
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
		return false, functionalPrefix + why
	}
	if call.Result == nil || call.Result.IsError {
		return gradeDegraded(tr, "read(find) failed against the live mirror/source chain")
	}
	var out tools.ReadOutput
	if err := decodeStructured(call.Structured, &out); err != nil {
		return false, err.Error()
	}
	if !out.Extractable {
		return gradeDegraded(tr, notExtractableDetail+out.Reason+")")
	}
	// Whether this particular copy contains the term is live luck; claiming a
	// passage that the search did not return is not.
	if out.MatchCount == 0 {
		return gradeDegraded(tr, "read(find) ran but found no matches for the term in this copy")
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
	call, ok := findOutlineCall(tr)
	if !ok {
		return false, "SURFACE GAP: model never called read — the outline capability lives on read and was not discovered"
	}
	if outline, _ := call.Input["outline"].(bool); !outline {
		return false, "SURFACE GAP: model called read without outline=true — read's outline field description may not convey table-of-contents mode"
	}
	// Provenance: the read identifier must trace to a prior search result, so a
	// hallucinated md5/doi that then hits a live error cannot pass as a benign skip.
	if keyed, why := readIdentifierOK(tr, call); !keyed {
		return false, functionalPrefix + why
	}
	if call.Result == nil || call.Result.IsError {
		return gradeDegraded(tr, "read(outline) failed against the live mirror/source chain")
	}
	var out tools.ReadOutput
	if err := decodeStructured(call.Structured, &out); err != nil {
		return false, err.Error()
	}
	if !out.Extractable {
		return gradeDegraded(tr, notExtractableDetail+out.Reason+")")
	}
	// A PDF with no embedded table of contents is common and legitimate, and a model
	// that then reads the book's own contents page and compiles one from it has done
	// nothing wrong — the text is right there in the transcript. Only an answer with
	// no source behind it is a fabrication.
	if len(out.Outline) == 0 {
		if readTextGrounded(tr) {
			return true, "no embedded table of contents; the model read the document and compiled one from its text"
		}
		return gradeDegraded(tr, "read(outline) ran cleanly but this file has no embedded table of contents")
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
		return gradeDegraded(tr, "model downloaded by DOI (host answered the email elicitation) but the live OA chain failed")
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
		return false, badDownloadMD5Detail
	}
	if !md5InSearchResults(tr, md5) {
		return false, "FUNCTIONAL: download md5 did not come from a prior search result (model may have hallucinated it)"
	}
	if downloadFailed(call) {
		if tr.ConfirmElicits == 0 {
			return gradeDegraded(tr, "live fetch failed before any save-confirmation elicitation fired (mirror/network)")
		}
		return gradeDegraded(tr, "confirmation elicitation fired but the live fetch failed (mirror/network)")
	}
	if tr.ConfirmElicits == 0 {
		return false, "FUNCTIONAL: download completed but no save-confirmation elicitation fired — the confirmation surface did not run"
	}
	fileOK, msg := checkDownloadedFile(call, "")
	if !fileOK {
		return false, functionalPrefix + msg
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
		return false, badDownloadMD5Detail
	}
	if downloadFailed(call) {
		return gradeDegraded(tr, "valid md5 download but the live fetch failed (mirror/network)")
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
		return false, badDownloadMD5Detail
	}
	if downloadFailed(call) {
		return gradeDegraded(tr, "model set source="+want+" correctly but the live download failed (mirror/network)")
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
		return gradeDegraded(tr, "valid DOI download but the live fetch failed (mirror/network)")
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

// assertSearchEscalation verifies the search was called and returned at least one
// Anna's-origin result (evidence the auto escalation fired), and that the model
// did not give up with "not found". A live provider outage is a SKIP.
func assertSearchEscalation(tr transcript) (pass bool, detail string) {
	call, out, err := searchOutput(tr)
	if err != nil {
		return false, err.Error()
	}
	_ = call
	var fromAnnas int
	for _, r := range out.Results {
		if r.Origin == "annas" {
			fromAnnas++
		}
	}
	if fromAnnas == 0 && len(out.OpenAccess) == 0 {
		return gradeDegraded(tr, "escalation produced no extra-origin results (live network)")
	}
	if fromAnnas == 0 {
		return gradeDegraded(tr, "only open-access hits, no Anna's-origin results today")
	}
	lower := strings.ToLower(tr.FinalText)
	if strings.Contains(lower, "not found") || strings.Contains(lower, "no results") || strings.Contains(lower, "couldn't find") {
		return false, "model reported not-found despite escalation returning Anna's results"
	}
	return true, fmt.Sprintf("escalation surfaced %d Anna's-origin result(s); model did not report not-found", fromAnnas)
}

// assertSearchThenDownloadEscalated verifies the model searched, then downloaded
// an item found via escalation (Anna's origin). A live download failure is a SKIP.
func assertSearchThenDownloadEscalated(tr transcript) (pass bool, detail string) {
	_, out, err := searchOutput(tr)
	if err != nil {
		return false, err.Error()
	}
	var annasMD5s []string
	for _, r := range out.Results {
		if r.Origin == "annas" && r.MD5 != "" {
			annasMD5s = append(annasMD5s, strings.ToLower(r.MD5))
		}
	}
	if len(annasMD5s) == 0 {
		return gradeDegraded(tr, "no Anna's-origin result to download (live network)")
	}
	dlCall, ok := findCall(tr, "download")
	if !ok {
		return false, "model searched but did not call download"
	}
	dlMD5, _ := dlCall.Input["md5"].(string)
	dlMD5 = strings.ToLower(strings.TrimSpace(dlMD5))
	if dlMD5 == "" {
		return false, "download call has no md5"
	}
	// Not a live-network skip: the escalation did surface Anna's results, so
	// downloading something else is the model failing the flow under test.
	if !slices.Contains(annasMD5s, dlMD5) {
		return false, "model downloaded an md5 not from an Anna's-origin result"
	}
	if dlCall.Result != nil && dlCall.Result.IsError {
		return gradeDegraded(tr, "download call returned a tool error (live network)")
	}
	return true, fmt.Sprintf("model searched, found an Anna's-origin item, and downloaded it (md5=%s)", dlMD5)
}
