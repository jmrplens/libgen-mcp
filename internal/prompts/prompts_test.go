package prompts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
)

type staticMirrors []string

func (s staticMirrors) Mirrors(context.Context) []string { return s }

// newFixtureClient builds a libgen client backed by an httptest mirror that
// serves the libgen package's search fixture, mirroring the internal/tools
// test setup.
func newFixtureClient(t *testing.T) *libgen.Client {
	t.Helper()
	searchHTML, err := os.ReadFile("../libgen/testdata/search_books.html")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(searchHTML) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	return libgen.New(staticMirrors{srv.URL}, cfg)
}

func TestAcquireBook_RequiresTitle(t *testing.T) {
	client := newFixtureClient(t)
	_, err := handleAcquireBook(context.Background(), client, &mcp.GetPromptRequest{
		Params: &mcp.GetPromptParams{Arguments: map[string]string{}},
	})
	if err == nil {
		t.Fatal("expected error when title is missing")
	}
}

func TestAcquireBook_ReturnsUserInstruction(t *testing.T) {
	client := newFixtureClient(t)
	res, err := handleAcquireBook(context.Background(), client, &mcp.GetPromptRequest{
		Params: &mcp.GetPromptParams{Arguments: map[string]string{"title": "linux"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Role != "user" {
		t.Fatalf("want one user-role message, got %+v", res.Messages)
	}
	txt := res.Messages[0].Content.(*mcp.TextContent).Text
	if !strings.Contains(txt, "Next actions") {
		t.Errorf("missing Next actions block:\n%s", txt)
	}
}

// newNoSearchClient builds a libgen client whose /index.php handler fails the
// test if hit, proving a code path performs no search.
func newNoSearchClient(t *testing.T) *libgen.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/index.php", func(_ http.ResponseWriter, _ *http.Request) {
		t.Errorf("get_paper DOI path must not search")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	return libgen.New(staticMirrors{srv.URL}, cfg)
}

func TestGetPaper_DOINoSearch(t *testing.T) {
	client := newNoSearchClient(t)
	res, err := handleGetPaper(context.Background(), client, &mcp.GetPromptRequest{
		Params: &mcp.GetPromptParams{Arguments: map[string]string{"doi": "10.1/x"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Role != "user" {
		t.Fatalf("want one user-role message, got %+v", res.Messages)
	}
	txt := res.Messages[0].Content.(*mcp.TextContent).Text
	if !strings.Contains(txt, "download") {
		t.Errorf("missing download instruction:\n%s", txt)
	}
	if !strings.Contains(txt, "10.1/x") {
		t.Errorf("missing DOI in message:\n%s", txt)
	}
	if !strings.Contains(txt, "get_details") {
		t.Errorf("missing get_details caveat:\n%s", txt)
	}
}

func TestGetPaper_CitationSearches(t *testing.T) {
	client := newFixtureClient(t)
	res, err := handleGetPaper(context.Background(), client, &mcp.GetPromptRequest{
		Params: &mcp.GetPromptParams{Arguments: map[string]string{"citation": "hallmarks of cancer"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Role != "user" {
		t.Fatalf("want one user-role message, got %+v", res.Messages)
	}
	txt := res.Messages[0].Content.(*mcp.TextContent).Text
	if !strings.Contains(txt, "| # | Title | Authors | Year | Publisher | DOI |") {
		t.Errorf("missing candidate table:\n%s", txt)
	}
	if !strings.Contains(txt, "Next actions") {
		t.Errorf("missing Next actions block:\n%s", txt)
	}
}

// newCitationClient builds a libgen client whose /index.php handler serves a
// different fixture per libgen topic code carried in the outgoing request's
// topics[] query param (articles -> "a", nonfiction -> "l"). Fixtures are read
// in the test goroutine so t.Fatal is safe; the handler only writes bytes.
func newCitationClient(t *testing.T, byTopic map[string]string) *libgen.Client {
	t.Helper()
	bodies := make(map[string][]byte, len(byTopic))
	for topic, path := range byTopic {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		bodies[topic] = body
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Query().Get("topics[]")]
		if !ok {
			http.Error(w, "unexpected topic", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	return libgen.New(staticMirrors{srv.URL}, cfg)
}

const paperTableHeader = "| # | Title | Authors | Year | Publisher | DOI |"

// TestGetPaper_CitationRetriesNonfiction proves the citation path retries once
// against nonfiction when the articles search is empty: articles serves an
// empty fixture, nonfiction serves populated books, and the returned message
// must contain a candidate table (retry fired and matched).
func TestGetPaper_CitationRetriesNonfiction(t *testing.T) {
	client := newCitationClient(t, map[string]string{
		"a": "../libgen/testdata/search_empty.html",
		"l": "../libgen/testdata/search_books.html",
	})
	res, err := handleGetPaper(context.Background(), client, &mcp.GetPromptRequest{
		Params: &mcp.GetPromptParams{Arguments: map[string]string{"citation": "hallmarks of cancer"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	txt := res.Messages[0].Content.(*mcp.TextContent).Text
	if !strings.Contains(txt, paperTableHeader) {
		t.Errorf("expected candidate table after nonfiction retry:\n%s", txt)
	}
}

// TestGetPaper_CitationNoMatches proves that when both the articles and the
// nonfiction searches return zero results, the message is the recovery guidance
// and carries no candidate table.
func TestGetPaper_CitationNoMatches(t *testing.T) {
	client := newCitationClient(t, map[string]string{
		"a": "../libgen/testdata/search_empty.html",
		"l": "../libgen/testdata/search_empty.html",
	})
	res, err := handleGetPaper(context.Background(), client, &mcp.GetPromptRequest{
		Params: &mcp.GetPromptParams{Arguments: map[string]string{"citation": "no such paper exists"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	txt := res.Messages[0].Content.(*mcp.TextContent).Text
	if !strings.Contains(txt, "No candidate papers were found") {
		t.Errorf("expected recovery guidance:\n%s", txt)
	}
	if strings.Contains(txt, paperTableHeader) {
		t.Errorf("unexpected candidate table in no-match message:\n%s", txt)
	}
}

// TestGetPaper_CitationSearchError proves that a failing search (HTTP 500 from
// every mirror) propagates as a non-nil error rather than being swallowed.
func TestGetPaper_CitationSearchError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	client := libgen.New(staticMirrors{srv.URL}, cfg)

	if _, err := handleGetPaper(context.Background(), client, &mcp.GetPromptRequest{
		Params: &mcp.GetPromptParams{Arguments: map[string]string{"citation": "anything"}},
	}); err == nil {
		t.Fatal("expected error when the search fails")
	}
}

func TestGetPaper_RequiresExactlyOne(t *testing.T) {
	client := newFixtureClient(t)
	if _, err := handleGetPaper(context.Background(), client, &mcp.GetPromptRequest{
		Params: &mcp.GetPromptParams{Arguments: map[string]string{}},
	}); err == nil {
		t.Fatal("expected error when neither doi nor citation is given")
	}
	if _, err := handleGetPaper(context.Background(), client, &mcp.GetPromptRequest{
		Params: &mcp.GetPromptParams{Arguments: map[string]string{"doi": "10.1/x", "citation": "y"}},
	}); err == nil {
		t.Fatal("expected error when both doi and citation are given")
	}
}

func TestResearchTopic_RequiresTopic(t *testing.T) {
	client := newFixtureClient(t)
	_, err := handleResearchTopic(context.Background(), client, &mcp.GetPromptRequest{
		Params: &mcp.GetPromptParams{Arguments: map[string]string{}},
	})
	if err == nil {
		t.Fatal("expected error when topic is missing")
	}
}

func TestResearchTopic_BothSections(t *testing.T) {
	client := newFixtureClient(t)
	res, err := handleResearchTopic(context.Background(), client, &mcp.GetPromptRequest{
		Params: &mcp.GetPromptParams{Arguments: map[string]string{"topic": "linux", "kind": "both"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Role != "user" {
		t.Fatalf("want one user-role message, got %+v", res.Messages)
	}
	txt := res.Messages[0].Content.(*mcp.TextContent).Text
	if !strings.Contains(txt, "## Papers") {
		t.Errorf("missing Papers section:\n%s", txt)
	}
	if !strings.Contains(txt, "## Books") {
		t.Errorf("missing Books section:\n%s", txt)
	}
	if !strings.Contains(txt, "Next actions") {
		t.Errorf("missing Next actions block:\n%s", txt)
	}
}

func TestResearchTopic_BadLimitClamped(t *testing.T) {
	client := newFixtureClient(t)
	for _, limit := range []string{"0", "-3", "abc"} {
		res, err := handleResearchTopic(context.Background(), client, &mcp.GetPromptRequest{
			Params: &mcp.GetPromptParams{Arguments: map[string]string{"topic": "linux", "limit": limit}},
		})
		if err != nil {
			t.Fatalf("unexpected error for limit=%q: %v", limit, err)
		}
		txt := res.Messages[0].Content.(*mcp.TextContent).Text
		if txt == "" {
			t.Errorf("expected non-empty message for limit=%q", limit)
		}
	}
}
