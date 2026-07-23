package tools

import (
	"strings"
	"testing"

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
