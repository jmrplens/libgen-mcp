package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
)

// TestEnrichmentNextStep_NoData verifies the helper returns an empty string when
// there is no Crossref enrichment to report (nil enrichment or nil Crossref).
func TestEnrichmentNextStep_NoData(t *testing.T) {
	if got := enrichmentNextStep(nil); got != "" {
		t.Errorf("nil enrichment: got %q, want empty", got)
	}
	if got := enrichmentNextStep(&libgen.Enrichment{}); got != "" {
		t.Errorf("nil Crossref: got %q, want empty", got)
	}
	// Crossref present but with no reportable fields → still empty.
	if got := enrichmentNextStep(&libgen.Enrichment{Crossref: &libgen.CrossrefWork{}}); got != "" {
		t.Errorf("empty Crossref: got %q, want empty", got)
	}
}

// TestEnrichmentNextStep_Facts verifies the helper names the journal, year and
// citation count so the model surfaces them, and escapes the untrusted journal.
func TestEnrichmentNextStep_Facts(t *testing.T) {
	step := enrichmentNextStep(&libgen.Enrichment{Crossref: &libgen.CrossrefWork{
		ContainerTitle: "Cell",
		PublishedYear:  2011,
		CitationCount:  56374,
	}})
	for _, want := range []string{"Cell", "2011", "56374", "journal"} {
		if !strings.Contains(step, want) {
			t.Errorf("next step %q should mention %q", step, want)
		}
	}
	// An untrusted journal title with a newline must be neutralized (no raw newline).
	evil := enrichmentNextStep(&libgen.Enrichment{Crossref: &libgen.CrossrefWork{ContainerTitle: "Evil\nJournal"}})
	if strings.Contains(evil, "Evil\nJournal") {
		t.Errorf("untrusted journal title must be escaped, got %q", evil)
	}
}

// TestWriteEnrichment_UserFacingLabels verifies the enrichment markdown uses
// user-facing labels (Journal, Times cited, Published year) rather than Crossref
// jargon, and renders OpenLibrary fields too.
func TestWriteEnrichment_UserFacingLabels(t *testing.T) {
	out := renderDetailsMarkdown(DetailsOutput{
		File: map[string]any{"md5": "d48739b6ac9e01d70dda1de46805d797"},
		Enrichment: &libgen.Enrichment{
			Crossref:    &libgen.CrossrefWork{ContainerTitle: "Cell", PublishedYear: 2011, CitationCount: 56374},
			OpenLibrary: &libgen.OLBook{OpenLibURL: "https://openlibrary.org/works/OL1W", Description: "A classic."},
		},
	})
	for _, want := range []string{"Journal / container: Cell", "Times cited: 56374", "Published year: 2011", "OpenLibrary record:", "A classic."} {
		if !strings.Contains(out, want) {
			t.Errorf("enrichment markdown should contain %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Crossref container") {
		t.Error("enrichment markdown should not use the old 'Crossref container' jargon")
	}
}

// TestDetailsEnrich_AppendsNextStep drives detailsEnrich against an httptest
// Crossref server: with a DOI in the edition record and enrichment enabled, it
// must populate out.Enrichment and append an enrichment next-step naming the
// journal and citation count, covering the enrichment wiring end to end.
func TestDetailsEnrich_AppendsNextStep(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","message":{` +
			`"container-title":["Cell"],"published":{"date-parts":[[2011,3,1]]},` +
			`"is-referenced-by-count":56374,"subject":["Oncology"]}}`))
	}))
	defer srv.Close()

	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	client := libgen.New(staticMirrors{"http://127.0.0.1:0"}, cfg,
		libgen.WithEnrichBaseURLs(srv.URL, "http://openlibrary.invalid"))

	out := DetailsOutput{Edition: map[string]any{"doi": "10.1016/j.cell.2011.02.013"}}
	detailsEnrich(context.Background(), client, &out)

	if out.Enrichment == nil || out.Enrichment.Crossref == nil {
		t.Fatalf("expected Crossref enrichment, got %+v", out.Enrichment)
	}
	if out.Enrichment.Crossref.ContainerTitle != "Cell" {
		t.Errorf("journal = %q, want Cell", out.Enrichment.Crossref.ContainerTitle)
	}
	joined := strings.Join(out.NextSteps, " ")
	for _, want := range []string{"Cell", "56374"} {
		if !strings.Contains(joined, want) {
			t.Errorf("next_steps should mention %q; got %q", want, joined)
		}
	}
}

// TestDetailsEnrich_NoDOINoStep verifies detailsEnrich adds nothing when the
// record carries no DOI/ISBN (Enrich returns nil, so no next-step is appended).
func TestDetailsEnrich_NoDOINoStep(t *testing.T) {
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	client := libgen.New(staticMirrors{"http://127.0.0.1:0"}, cfg)
	out := DetailsOutput{Edition: map[string]any{"title": "No identifiers here"}}
	detailsEnrich(context.Background(), client, &out)
	if out.Enrichment != nil {
		t.Errorf("no DOI/ISBN should yield nil enrichment, got %+v", out.Enrichment)
	}
	if len(out.NextSteps) != 0 {
		t.Errorf("no enrichment should append no next-step, got %v", out.NextSteps)
	}
}
