//go:build e2e

package e2e

import (
	"context"
	cryptomd5 "crypto/md5" //nolint:gosec // MD5 is the digest LibGen keys files by; used only for building the fixture chain.
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/prompts"
	"github.com/jmrplens/libgen-mcp/internal/tools"
)

// This file extends the gated e2e suite to cover every capability added since
// v1.2.0: the four prompts, get_details citations, opt-in enrichment, the read
// tool's sequential/find/outline modes, open-access discovery in search, and
// elicitation. Network-dependent assertions gate on requireLive and SKIP (never
// fail) when the live site or the open-access providers are unreachable. The
// elicitation and the offline-prompt cases are DETERMINISTIC (httptest fixtures
// and in-memory transports, no live network) so they run and pass unconditionally.

// staticMirrors is a fixed MirrorLister for building an offline libgen client
// (no live network) in the deterministic prompt and elicitation cases.
type staticMirrors []string

// Mirrors returns the fixed mirror list, ignoring the context.
func (s staticMirrors) Mirrors(context.Context) []string { return s }

// connectInMemory connects an in-memory MCP client to server and returns the
// session, wiring the client's elicitation capability from handler (nil = the
// client advertises no elicitation capability, exercising the fallback path). The
// session is closed on test cleanup.
func connectInMemory(t *testing.T, ctx context.Context, server *mcp.Server, handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)) *mcp.ClientSession {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "e2e-client", Version: "test"},
		&mcp.ClientOptions{ElicitationHandler: handler})
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// newPromptSession registers the prompts on an in-memory server backed by client
// and returns a connected client session for GetPrompt/ListPrompts.
func newPromptSession(t *testing.T, ctx context.Context, client *libgen.Client, cfg *config.Config) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "libgen-mcp-e2e", Version: "test"}, nil)
	prompts.Register(server, client, cfg)
	return connectInMemory(t, ctx, server, nil)
}

// newToolSession registers the tools on an in-memory server backed by client and
// returns a connected client session, wiring the elicitation handler (nil = none)
// and any register options (e.g. remote downloads).
func newToolSession(t *testing.T, ctx context.Context, client *libgen.Client, cfg *config.Config, handler func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error), opts ...tools.RegisterOption) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "libgen-mcp-e2e", Version: "test"}, nil)
	tools.Register(server, client, cfg, opts...)
	return connectInMemory(t, ctx, server, handler)
}

// offlineConfig returns a plain local config rooted at a fresh temp dir with a
// small, email-free source set, for the deterministic prompt cases that perform
// no network I/O.
func offlineConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		DownloadDir:   t.TempDir(),
		Timeout:       5 * time.Second,
		RateRPS:       1000,
		RateBurst:     100,
		RetryAttempts: 1,
		Sources:       []string{"libgen", "scihub"},
	}
}

// offlineClient builds a libgen client with no reachable mirrors, for the
// deterministic prompt cases whose exercised path performs no search.
func offlineClient(t *testing.T, cfg *config.Config) *libgen.Client {
	t.Helper()
	return libgen.New(staticMirrors{}, cfg)
}

// promptUserText asserts the prompt returned at least one message, that the first
// message is a non-empty user-role text message, and returns its text.
func promptUserText(t *testing.T, res *mcp.GetPromptResult) string {
	t.Helper()
	if res == nil || len(res.Messages) == 0 {
		t.Fatal("prompt returned no messages")
	}
	msg := res.Messages[0]
	if msg.Role != "user" {
		t.Errorf("first prompt message role = %q, want user", msg.Role)
	}
	tc, ok := msg.Content.(*mcp.TextContent)
	if !ok {
		t.Fatalf("first prompt message content is not text: %T", msg.Content)
	}
	if strings.TrimSpace(tc.Text) == "" {
		t.Error("prompt message text is empty")
	}
	return tc.Text
}

// wantPromptNames lists the four prompts the server must advertise since v1.2.0.
var wantPromptNames = []string{"acquire_book", "research_topic", "get_paper", "download_troubleshoot"}

// TestE2EPromptsAdvertised proves all four prompts are advertised via
// ListPrompts. It is DETERMINISTIC: prompt discovery performs no search, so it
// runs against an offline client with no live network.
func TestE2EPromptsAdvertised(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cfg := offlineConfig(t)
	session := newPromptSession(t, ctx, offlineClient(t, cfg), cfg)

	res, err := session.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("ListPrompts error: %v", err)
	}
	got := make(map[string]bool, len(res.Prompts))
	for _, p := range res.Prompts {
		got[p.Name] = true
	}
	for _, name := range wantPromptNames {
		if !got[name] {
			t.Errorf("prompt %q not advertised; got %v", name, got)
		}
	}
}

// TestE2EPromptGetPaperByDOI proves the get_paper prompt returns download
// guidance for a bare DOI. It is DETERMINISTIC: the DOI path performs no search,
// so it runs against an offline client.
func TestE2EPromptGetPaperByDOI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cfg := offlineConfig(t)
	session := newPromptSession(t, ctx, offlineClient(t, cfg), cfg)

	const doi = "10.1371/journal.pmed.0020124"
	res, err := session.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "get_paper",
		Arguments: map[string]string{"doi": doi},
	})
	if err != nil {
		t.Fatalf("GetPrompt(get_paper) error: %v", err)
	}
	txt := promptUserText(t, res)
	if !strings.Contains(txt, "download") {
		t.Errorf("get_paper DOI guidance should mention download:\n%s", txt)
	}
	if !strings.Contains(txt, doi) {
		t.Errorf("get_paper DOI guidance should echo the DOI:\n%s", txt)
	}
}

// TestE2EPromptDownloadTroubleshoot proves the download_troubleshoot prompt
// returns a recovery plan for a failed book download with an error message. It is
// DETERMINISTIC: the prompt performs no search, so it runs against an offline
// client.
func TestE2EPromptDownloadTroubleshoot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cfg := offlineConfig(t)
	session := newPromptSession(t, ctx, offlineClient(t, cfg), cfg)

	res, err := session.GetPrompt(ctx, &mcp.GetPromptParams{
		Name: "download_troubleshoot",
		Arguments: map[string]string{
			"md5":   "d41d8cd98f00b204e9800998ecf8427e",
			"error": "all libgen mirrors unreachable (network block?)",
		},
	})
	if err != nil {
		t.Fatalf("GetPrompt(download_troubleshoot) error: %v", err)
	}
	txt := promptUserText(t, res)
	if !strings.Contains(strings.ToLower(txt), "retry") {
		t.Errorf("troubleshoot plan should offer retry guidance:\n%s", txt)
	}
}

// TestE2EPromptAcquireBook drives the acquire_book prompt against the LIVE site:
// it searches for a real title and asserts the returned plan tells the model to
// call get_details and download. It gates on requireLive and inherits the suite's
// SKIP-when-unreachable discipline.
func TestE2EPromptAcquireBook(t *testing.T) {
	env := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	session := newPromptSession(t, ctx, env.client, env.cfg)

	res, err := session.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "acquire_book",
		Arguments: map[string]string{"title": "The Linux Programming Interface"},
	})
	if err != nil {
		t.Fatalf("GetPrompt(acquire_book) error: %v", err)
	}
	txt := promptUserText(t, res)
	if !strings.Contains(txt, "get_details") {
		t.Errorf("acquire_book plan should reference get_details:\n%s", txt)
	}
	if !strings.Contains(txt, "download") {
		t.Errorf("acquire_book plan should reference download:\n%s", txt)
	}
}

// TestE2EPromptResearchTopic drives the research_topic prompt against the LIVE
// site: it searches a topic and asserts a non-empty user-role reading-list
// message with a Next actions block comes back. It gates on requireLive.
func TestE2EPromptResearchTopic(t *testing.T) {
	env := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session := newPromptSession(t, ctx, env.client, env.cfg)

	res, err := session.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "research_topic",
		Arguments: map[string]string{"topic": "reinforcement learning", "kind": "both"},
	})
	if err != nil {
		t.Fatalf("GetPrompt(research_topic) error: %v", err)
	}
	txt := promptUserText(t, res)
	// The message is either a reading list (Next actions) or the documented
	// no-results recovery guidance — both are valid, non-empty user messages.
	if !strings.Contains(txt, "Next actions") && !strings.Contains(txt, "No results") {
		t.Errorf("research_topic message should be a reading list or recovery guidance:\n%s", txt)
	}
}

// TestE2EGetDetailsCitations drives the get_details tool against the LIVE site for
// a known md5 and asserts the structured DetailsOutput carries BibTeX/RIS
// citations built from the record. It gates on requireLive and SKIPS if the
// record lacks the title needed to build a citation.
func TestE2EGetDetailsCitations(t *testing.T) {
	env := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	md5 := firstMD5(t, ctx, env.client, "linux")
	pace()
	session := newToolSession(t, ctx, env.client, env.cfg, nil)

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_details",
		Arguments: map[string]any{"md5": md5},
	})
	if err != nil {
		t.Fatalf("CallTool(get_details) error: %v", err)
	}
	if res.IsError {
		t.Skipf("get_details returned an error result live: %+v", res.Content)
	}
	var out tools.DetailsOutput
	decodeStructured(t, res, &out)
	if out.Citations == nil {
		t.Skipf("record %s carried no title; no citations to assert", md5)
	}
	if !strings.HasPrefix(strings.TrimSpace(out.Citations.BibTeX), "@") {
		t.Errorf("BibTeX should start with @, got:\n%s", out.Citations.BibTeX)
	}
	if !strings.Contains(out.Citations.RIS, "TY") || !strings.Contains(out.Citations.RIS, "ER") {
		t.Errorf("RIS should carry TY and ER tags, got:\n%s", out.Citations.RIS)
	}
	t.Logf("citations md5=%s bibtex=%d bytes ris=%d bytes", md5, len(out.Citations.BibTeX), len(out.Citations.RIS))
}

// firstArticleWithDOI runs a live articles search and returns the md5 of the
// first result that carries both a canonical md5 and a DOI (so its details record
// can drive DOI-based enrichment). It SKIPS the calling test when none qualifies.
func firstArticleWithDOI(t *testing.T, ctx context.Context, c *libgen.Client, query string) string {
	t.Helper()
	page, _, err := c.Search(ctx, libgen.SearchParams{Query: query, Topics: []string{"articles"}})
	if err != nil {
		t.Fatalf("Search(%q) error: %v", query, err)
	}
	for i := range page.Results {
		r := page.Results[i]
		if md5Re.MatchString(r.MD5) && strings.TrimSpace(r.DOI) != "" {
			return r.MD5
		}
	}
	t.Skipf("no article with both an md5 and a DOI for query %q; cannot exercise enrichment", query)
	return ""
}

// TestE2EGetDetailsEnrichment drives get_details with enrich=true against the LIVE
// site for an article record that carries a DOI. Enrichment is best-effort: the
// call must NOT error, and when Crossref responds the Enrichment field is present.
// It gates on requireLive and on a configured contact email (loadLiveConfig always
// supplies one), and never fails on a best-effort miss.
func TestE2EGetDetailsEnrichment(t *testing.T) {
	env := requireLive(t)
	if strings.TrimSpace(env.cfg.UnpaywallEmail) == "" {
		t.Skip("no contact email configured; skipping enrichment")
	}
	if !env.cfg.EnrichEnabled {
		t.Skip("enrichment disabled on this deployment; skipping")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	md5 := firstArticleWithDOI(t, ctx, env.client, "cancer")
	pace()
	session := newToolSession(t, ctx, env.client, env.cfg, nil)

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_details",
		Arguments: map[string]any{"md5": md5, "enrich": true},
	})
	if err != nil {
		t.Fatalf("CallTool(get_details enrich) transport error: %v", err)
	}
	if res.IsError {
		t.Skipf("get_details enrich returned an error result live: %+v", res.Content)
	}
	var out tools.DetailsOutput
	decodeStructured(t, res, &out)
	// Enrichment is best-effort: a nil result is a legitimate miss, not a failure.
	if out.Enrichment == nil {
		t.Skipf("enrichment found nothing for md5=%s (Crossref unreachable or DOI unindexed)", md5)
	}
	t.Logf("enrichment md5=%s crossref=%v openlibrary=%v", md5, out.Enrichment.Crossref != nil, out.Enrichment.OpenLibrary != nil)
}

// newReadSession builds a size-capped live client (so a parsing mistake can never
// pull a large file) and an in-memory local tool session exposing it, for the
// read-mode e2e cases.
func newReadSession(t *testing.T, ctx context.Context) (*libgen.Client, *mcp.ClientSession) {
	t.Helper()
	cfg := loadLiveConfig(t)
	cfg.MaxDownloadBytes = maxE2EDownloadBytes
	client := buildClient(t, cfg)
	return client, newToolSession(t, ctx, client, cfg, nil)
}

// smallestTargetIn searches one topic ordered by ascending size and returns the
// smallest downloadable result (canonical md5, parseable non-zero size within the
// polite cap). It SKIPS the calling test when no such target is available.
func smallestTargetIn(t *testing.T, ctx context.Context, client *libgen.Client, topic, query string) libgen.Result {
	t.Helper()
	page, _, err := client.Search(ctx, libgen.SearchParams{
		Query: query, Topics: []string{topic}, Order: "size", OrderMode: "asc",
	})
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	target := smallestDownloadable(page.Results)
	if target.MD5 == "" {
		t.Skipf("no small downloadable %s target for %q; skipping to stay polite", topic, query)
	}
	return target
}

// callRead invokes the read tool and decodes its structured ReadOutput. It SKIPS
// the calling test (never fails) on a transport error or a tool-error result,
// since a live fetch/resolve hiccup is not a suite failure. A not-extractable file
// is a normal (non-error) result and is returned for the caller to assert.
func callRead(t *testing.T, ctx context.Context, session *mcp.ClientSession, args map[string]any) tools.ReadOutput {
	t.Helper()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read", Arguments: args})
	if err != nil {
		t.Skipf("read tool call failed live: %v", err)
	}
	if res.IsError {
		t.Skipf("read tool returned an error result (live fetch unavailable): %+v", res.Content)
	}
	var out tools.ReadOutput
	decodeStructured(t, res, &out)
	return out
}

// assertUntrustedFirst asserts a read result leads its next_steps with the
// UNTRUSTED-content warning, the invariant every read mode must preserve.
func assertUntrustedFirst(t *testing.T, out tools.ReadOutput) {
	t.Helper()
	if len(out.NextSteps) == 0 {
		t.Fatal("read output carried no next_steps")
	}
	if !strings.Contains(out.NextSteps[0], "UNTRUSTED") {
		t.Errorf("read next_steps[0] should carry the UNTRUSTED warning, got: %q", out.NextSteps[0])
	}
}

// TestE2EReadModes drives the read tool's three modes against a real small file
// from the LIVE site: sequential text (with a cursor when more remains), find (a
// plausible common word), and outline (entries or a clean no-outline result). Each
// mode must lead with the UNTRUSTED warning. It gates on requireLive; find and
// outline are best-effort (zero matches / no TOC are valid, not failures), and the
// whole test SKIPS if the target is not extractable.
func TestE2EReadModes(t *testing.T) {
	requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	client, session := newReadSession(t, ctx)
	target := smallestTargetIn(t, ctx, client, "nonfiction", "python")
	t.Logf("read target md5=%s size=%q ext=%q title=%q", target.MD5, target.Size, target.Extension, target.Title)
	pace()

	seq := callRead(t, ctx, session, map[string]any{"md5": target.MD5})
	assertUntrustedFirst(t, seq)
	if !seq.Extractable {
		t.Skipf("target %s is not extractable (%s); read modes need extractable text", target.MD5, seq.Reason)
	}
	if strings.TrimSpace(seq.Text) == "" {
		t.Error("sequential read returned no text for an extractable file")
	}
	if seq.HasMore && seq.Cursor == "" {
		t.Error("sequential read reported has_more but returned no cursor")
	}
	pace()

	find := callRead(t, ctx, session, map[string]any{"md5": target.MD5, "find": "the", "max_matches": 5})
	assertUntrustedFirst(t, find)
	for i := range find.Matches {
		if strings.TrimSpace(find.Matches[i].Snippet) == "" {
			t.Errorf("find match %d carried an empty snippet", i)
		}
	}
	t.Logf("find matches=%d total=%d", len(find.Matches), find.MatchCount)
	pace()

	outline := callRead(t, ctx, session, map[string]any{"md5": target.MD5, "outline": true})
	assertUntrustedFirst(t, outline)
	t.Logf("outline entries=%d extractable=%v", len(outline.Outline), outline.Extractable)
}

// TestE2EReadNotExtractable exercises the not-extractable path via an unsupported
// format: a small comic (cbr/cbz) has no extractable text layer, so read must
// report extractable=false with a reason (and still lead with the UNTRUSTED
// warning) instead of returning text. It gates on requireLive and SKIPS when no
// small comic target is available or the sample turns out to be extractable.
func TestE2EReadNotExtractable(t *testing.T) {
	requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	client, session := newReadSession(t, ctx)
	target := smallestTargetIn(t, ctx, client, "comics", "batman")
	t.Logf("not-extractable target md5=%s size=%q ext=%q", target.MD5, target.Size, target.Extension)
	pace()

	out := callRead(t, ctx, session, map[string]any{"md5": target.MD5})
	assertUntrustedFirst(t, out)
	if out.Extractable {
		t.Skipf("comic target %s was extractable; no not-extractable sample this run", target.MD5)
	}
	if strings.TrimSpace(out.Reason) == "" {
		t.Error("a not-extractable result should carry a reason")
	}
	t.Logf("not-extractable reason=%q", out.Reason)
}

// validOrigin reports whether an open-access hit's origin label is one of the
// three keyless discovery providers.
func validOrigin(origin string) bool {
	switch origin {
	case "arxiv", "crossref", "openlibrary":
		return true
	default:
		return false
	}
}

// TestE2ESearchOpenAccessIncluded drives the search tool with
// include_open_access=true for a research-y query against the LIVE site (which
// also hits arXiv/Crossref/OpenLibrary). It asserts the OpenAccess list is
// populated, each hit is labeled by a known origin, and no DOI is duplicated. It
// gates on requireLive and SKIPS when the open-access providers return nothing.
func TestE2ESearchOpenAccessIncluded(t *testing.T) {
	env := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session := newToolSession(t, ctx, env.client, env.cfg, nil)

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "graphene", "topics": []string{"articles"}, "include_open_access": true},
	})
	if err != nil {
		t.Fatalf("CallTool(search) error: %v", err)
	}
	if res.IsError {
		t.Fatalf("search tool returned an error result: %+v", res.Content)
	}
	var out tools.SearchOutput
	decodeStructured(t, res, &out)
	if len(out.OpenAccess) == 0 {
		t.Skip("no open-access hits (providers unreachable or no matches); skipping")
	}
	seenDOI := map[string]bool{}
	for i := range out.OpenAccess {
		h := out.OpenAccess[i]
		if !validOrigin(h.Origin) {
			t.Errorf("open-access hit %d has an unexpected origin %q", i, h.Origin)
		}
		doi := strings.ToLower(strings.TrimSpace(h.DOI))
		if doi == "" {
			continue
		}
		if seenDOI[doi] {
			t.Errorf("duplicate DOI %q across open-access hits (dedup failed)", doi)
		}
		seenDOI[doi] = true
	}
	t.Logf("open_access hits=%d unique_dois=%d", len(out.OpenAccess), len(seenDOI))
}

// TestE2ESearchOpenAccessOmittedDefault proves that with include_open_access
// omitted and the deployment default OFF, a core search still works and returns no
// open-access hits. It gates on requireLive; the config's OpenAccessEnabled is
// forced off so an environment default cannot perturb the assertion.
func TestE2ESearchOpenAccessOmittedDefault(t *testing.T) {
	env := requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cfg := *env.cfg // shallow copy so the forced flag is local to this test
	cfg.OpenAccessEnabled = false
	session := newToolSession(t, ctx, env.client, &cfg, nil)

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "linux", "topics": []string{"nonfiction"}},
	})
	if err != nil {
		t.Fatalf("CallTool(search) error: %v", err)
	}
	if res.IsError {
		t.Fatalf("search tool returned an error result: %+v", res.Content)
	}
	var out tools.SearchOutput
	decodeStructured(t, res, &out)
	if len(out.OpenAccess) != 0 {
		t.Errorf("open_access should be empty when the flag is omitted and default off, got %d", len(out.OpenAccess))
	}
	if len(out.Results) == 0 && (out.TotalFiles == "" || out.TotalFiles == "0") {
		t.Error("core search returned no results and no total_files with open-access omitted")
	}
}

// unpaywallStub serves the Unpaywall lookup for the email-on-demand elicitation
// case: it records how many lookups it received and the last email query param and
// always replies with an open-access record. resolve_only never fetches the PDF,
// so url_for_pdf is a static placeholder.
func unpaywallStub(t *testing.T) (base string, lookups *atomic.Int32, lastEmail *string) {
	t.Helper()
	var hits atomic.Int32
	var email string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		email = r.URL.Query().Get("email")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"is_oa":true,"best_oa_location":{"url_for_pdf":"https://cdn.example/oa.pdf"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &hits, &email
}

// bookChainMirror serves the full book download chain (ads.php -> get.php -> CDN)
// for a payload whose md5 it advertises, counting CDN body GETs so the
// confirmation cases can prove whether the file was fetched. It returns the
// server and the GET counter.
func bookChainMirror(t *testing.T, payload []byte) (srv *httptest.Server, cdnGET *atomic.Int32) {
	t.Helper()
	sum := cryptomd5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])
	var getHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<html><a href="get.php?md5=%s&key=TESTKEY123">GET</a></html>`, wantMD5)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/cdn/file", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/cdn/file", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			return
		}
		getHits.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="book.pdf"`)
		_, _ = w.Write(payload)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &getHits
}

// elicitEmailConfig is a config with NO Unpaywall email and only "unpaywall"
// enabled, so a Unpaywall resolution can only come from the on-demand per-call
// email path — never a live Sci-Hub call.
func elicitEmailConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		DownloadDir:   t.TempDir(),
		Timeout:       5 * time.Second,
		RateRPS:       1000,
		RateBurst:     100,
		RetryAttempts: 1,
		Sources:       []string{"unpaywall"},
	}
}

// TestE2EElicitEmailOnDemand proves the on-demand Unpaywall-email elicitation
// wiring end-to-end and DETERMINISTICALLY (httptest, no live network): with no
// configured email and a client that accepts the elicitation with an email, a
// resolve_only DOI download consults Unpaywall using the elicited email and
// resolves the link via the unpaywall source. Asserts the handler was invoked
// (one lookup) and the elicited email reached Unpaywall.
func TestE2EElicitEmailOnDemand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	base, lookups, lastEmail := unpaywallStub(t)
	client := libgen.New(staticMirrors{}, elicitEmailConfig(t), libgen.WithUnpaywallBaseURL(base))
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"email": "asked@example.com"}}, nil
	}
	session := newToolSession(t, ctx, client, elicitEmailConfig(t), handler)

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"doi": "10.1/x", "resolve_only": true},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("download with an accepted email should not be a tool error: %+v", res.Content)
	}
	var out tools.DownloadOutput
	decodeStructured(t, res, &out)
	if out.Resolved == nil || out.Resolved.Source != "unpaywall" {
		t.Fatalf("expected a resolved link from unpaywall, got %+v", out.Resolved)
	}
	if lookups.Load() != 1 {
		t.Errorf("Unpaywall lookups = %d, want 1 (handler-elicited email path)", lookups.Load())
	}
	if *lastEmail != "asked@example.com" {
		t.Errorf("Unpaywall received email = %q, want the elicited %q", *lastEmail, "asked@example.com")
	}
}

// TestE2EElicitDownloadConfirmAccept proves the download-confirmation accept
// wiring DETERMINISTICALLY: with an elicitation-capable client that confirms, a
// local md5 download is prompted and the file is saved to disk.
func TestE2EElicitDownloadConfirmAccept(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	payload := []byte("%PDF-1.4 confirm-accept e2e payload")
	srv, cdnGET := bookChainMirror(t, payload)
	sum := cryptomd5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])

	cfg := offlineConfig(t)
	var elicits atomic.Int32
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		elicits.Add(1)
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
	}
	client := libgen.New(staticMirrors{srv.URL}, cfg)
	session := newToolSession(t, ctx, client, cfg, handler)

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": wantMD5},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("an accepted download should not be a tool error: %+v", res.Content)
	}
	if elicits.Load() != 1 {
		t.Errorf("elicitation handler invoked %d times, want 1", elicits.Load())
	}
	var out tools.DownloadOutput
	decodeStructured(t, res, &out)
	if out.Path == "" {
		t.Fatalf("accepted download should report a saved path; got %+v", out)
	}
	if _, statErr := os.Stat(out.Path); statErr != nil {
		t.Errorf("accepted download did not write the file: %v", statErr)
	}
	if cdnGET.Load() == 0 {
		t.Error("accepted download never fetched the file body (0 CDN GETs)")
	}
}

// TestE2EElicitDownloadConfirmDecline proves the download-confirmation decline
// wiring DETERMINISTICALLY: with an elicitation-capable client that declines, no
// file is written (0 CDN GETs, empty download dir), the result is a non-error, and
// it still surfaces the resolved link.
func TestE2EElicitDownloadConfirmDecline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	payload := []byte("%PDF-1.4 confirm-decline e2e payload")
	srv, cdnGET := bookChainMirror(t, payload)
	sum := cryptomd5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])

	cfg := offlineConfig(t)
	var elicits atomic.Int32
	handler := func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		elicits.Add(1)
		return &mcp.ElicitResult{Action: "decline"}, nil
	}
	client := libgen.New(staticMirrors{srv.URL}, cfg)
	session := newToolSession(t, ctx, client, cfg, handler)

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": wantMD5},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("a declined download should be a non-error result; got %+v", res.Content)
	}
	if elicits.Load() != 1 {
		t.Errorf("elicitation handler invoked %d times, want 1", elicits.Load())
	}
	if cdnGET.Load() != 0 {
		t.Errorf("a declined download fetched the file body %d time(s), want 0", cdnGET.Load())
	}
	var out tools.DownloadOutput
	decodeStructured(t, res, &out)
	if out.Path != "" {
		t.Errorf("a declined download must not save a file, but Path=%q", out.Path)
	}
	if out.Resolved == nil {
		t.Errorf("a declined download should still surface the resolved link; got %+v", out)
	}
	if entries, _ := os.ReadDir(cfg.DownloadDir); len(entries) != 0 {
		t.Errorf("a declined download wrote %d file(s) to disk, want 0", len(entries))
	}
}

// TestE2EElicitNoHandlerFallback proves default preservation DETERMINISTICALLY: a
// client that did NOT advertise elicitation is never prompted, and the local md5
// download proceeds and saves the file exactly as it does today.
func TestE2EElicitNoHandlerFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	payload := []byte("%PDF-1.4 no-handler fallback e2e payload")
	srv, cdnGET := bookChainMirror(t, payload)
	sum := cryptomd5.Sum(payload) //nolint:gosec // integrity digest, not a security primitive.
	wantMD5 := hex.EncodeToString(sum[:])

	cfg := offlineConfig(t)
	client := libgen.New(staticMirrors{srv.URL}, cfg)
	// nil handler -> the client advertises no elicitation capability.
	session := newToolSession(t, ctx, client, cfg, nil)

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": wantMD5},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("a no-capability download should not be a tool error: %+v", res.Content)
	}
	var out tools.DownloadOutput
	decodeStructured(t, res, &out)
	if out.Path == "" {
		t.Fatalf("fallback download should report a saved path; got %+v", out)
	}
	if _, statErr := os.Stat(out.Path); statErr != nil {
		t.Errorf("fallback download did not write the file: %v", statErr)
	}
	if cdnGET.Load() == 0 {
		t.Error("fallback download never fetched the file body (0 CDN GETs)")
	}
}
