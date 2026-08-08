package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
)

// The fixtures pinned by this file are a real, live pairing: LibGen edition
// 137198203 carries Nassim Taleb's Antifragile under 10.1371/journal.pmed.0020124,
// which Crossref registers to John Ioannidis' "Why Most Published Research
// Findings Are False" in PLoS Medicine. The catalog record is third-party garbage
// we cannot fix; what we can refuse to do is launder it into a formatted citation
// that asserts the pairing as fact.

// mismatchedDOI is the DOI the corrupt catalog record claims.
const mismatchedDOI = "10.1371/journal.pmed.0020124"

// loadRecordFixture reads one of the testdata JSON records into the untyped map
// shape the details output carries.
func loadRecordFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err = json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// crossrefFixtureServer serves the recorded Crossref answer for mismatchedDOI on
// every request, standing in for api.crossref.org.
func crossrefFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile("testdata/crossref_pmed0020124.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// clientWithCrossref returns a client whose enrichment and corroboration lookups
// are aimed at the given Crossref stand-in.
func clientWithCrossref(t *testing.T, base string) (*libgen.Client, *config.Config) {
	t.Helper()
	cfg := &config.Config{
		DownloadDir: t.TempDir(), Timeout: 5 * time.Second,
		RateRPS: 1000, RateBurst: 100, RetryAttempts: 1, EnrichEnabled: true,
	}
	return libgen.New(staticMirrors{"http://127.0.0.1:0"}, cfg, libgen.WithEnrichBaseURLs(base, base)), cfg
}

// assertDOINotAsserted fails when either export states the mismatched DOI. This is
// the property the whole fix exists for: a citation may be poorer than the catalog
// record, never more confident than it.
func assertDOINotAsserted(t *testing.T, c *Citations) {
	t.Helper()
	if c == nil {
		t.Fatal("expected citations for a titled record, got nil")
	}
	for _, block := range []string{c.BibTeX, c.RIS} {
		if strings.Contains(block, mismatchedDOI) {
			t.Errorf("citation asserts a DOI that names a different work:\n%s", block)
		}
	}
	if strings.Contains(c.BibTeX, "doi = {") || strings.Contains(c.RIS, "DO  - ") {
		t.Errorf("citation carries a DOI field at all:\n%s\n%s", c.BibTeX, c.RIS)
	}
}

// TestBuildCitations_MismatchedDOIIsNeverAsserted is the regression test for the
// corrupt-record failure mode: given the real catalog record whose DOI belongs to
// another work, the exports must omit the DOI, doi_status must say "mismatch", and
// the provenance must name the work Crossref actually registers so the caller can
// see why. The entry must also stay a @book — the bad DOI is what used to typeset
// a 544-page Random House title as a journal article.
func TestBuildCitations_MismatchedDOIIsNeverAsserted(t *testing.T) {
	srv := crossrefFixtureServer(t)
	client, _ := clientWithCrossref(t, srv.URL)

	c := buildCitations(context.Background(), client, "",
		loadRecordFixture(t, "libgen_file_mismatched_doi.json"),
		loadRecordFixture(t, "libgen_edition_mismatched_doi.json"))

	assertDOINotAsserted(t, c)
	if c.DOIStatus != string(libgen.DOIMismatch) {
		t.Errorf("DOIStatus = %q, want %q", c.DOIStatus, libgen.DOIMismatch)
	}
	if !strings.HasPrefix(c.BibTeX, "@book{") || !strings.HasPrefix(c.RIS, "TY  - BOOK") {
		t.Errorf("a book must not be retyped as an article by a bad DOI:\n%s\n%s", c.BibTeX, c.RIS)
	}
	for _, want := range []string{mismatchedDOI, "Why Most Published Research Findings Are False", "different work"} {
		if !strings.Contains(c.Provenance, want) {
			t.Errorf("provenance missing %q: %q", want, c.Provenance)
		}
	}
	// The catalog's own fields still stand; only the identifier link was refused.
	if !strings.Contains(c.BibTeX, "Antifragile") {
		t.Errorf("the record's own metadata should survive:\n%s", c.BibTeX)
	}
}

// TestBuildCitations_MismatchSurfacesInMarkdown checks the caveat reaches the
// human-readable channel too. A caller reading the rendered text is the one most
// likely to paste the block into a bibliography, so the warning has to travel with
// the code fence, not only in the structured JSON beside it.
func TestBuildCitations_MismatchSurfacesInMarkdown(t *testing.T) {
	srv := crossrefFixtureServer(t)
	client, cfg := clientWithCrossref(t, srv.URL)
	out := DetailsOutput{
		File:    loadRecordFixture(t, "libgen_file_mismatched_doi.json"),
		Edition: loadRecordFixture(t, "libgen_edition_mismatched_doi.json"),
	}
	attachCitations(context.Background(), client, cfg, false, &out)

	md := renderDetailsMarkdown(out)
	if !strings.Contains(md, "> Bibliographic fields come from the Library Genesis catalog") {
		t.Errorf("provenance line missing from the Markdown:\n%s", md)
	}
	if strings.Contains(md, "doi = {") {
		t.Errorf("rendered BibTeX still states a DOI:\n%s", md)
	}
}

// TestAttachCitations_ReusesEnrichmentCrossrefTitle proves the enrich=true path
// reaches the same verdict from the Crossref record enrichment already fetched.
// The verdict must not depend on which path the caller took, and the registry must
// not be asked the same question twice: the stand-in counts its requests.
func TestAttachCitations_ReusesEnrichmentCrossrefTitle(t *testing.T) {
	body, err := os.ReadFile("testdata/crossref_pmed0020124.json")
	if err != nil {
		t.Fatal(err)
	}
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/works/") {
			hits++
			_, _ = w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	client, cfg := clientWithCrossref(t, srv.URL)
	out := DetailsOutput{
		File:    loadRecordFixture(t, "libgen_file_mismatched_doi.json"),
		Edition: loadRecordFixture(t, "libgen_edition_mismatched_doi.json"),
	}
	attachCitations(context.Background(), client, cfg, true, &out)

	assertDOINotAsserted(t, out.Citations)
	if out.Citations.DOIStatus != string(libgen.DOIMismatch) {
		t.Errorf("DOIStatus = %q, want %q", out.Citations.DOIStatus, libgen.DOIMismatch)
	}
	if hits != 1 {
		t.Errorf("Crossref /works hits = %d, want 1 (enrichment's lookup must be reused)", hits)
	}
}

// TestAttachCitations_EnrichDisabledStaysOffline checks the deployment
// kill-switch: with enrichment disabled there is no outbound corroboration, so the
// citation is still built — the keyless default must keep working — but the
// unverifiable DOI is left out rather than asserted.
func TestAttachCitations_EnrichDisabledStaysOffline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("no outbound lookup may happen when enrichment is disabled")
	}))
	t.Cleanup(srv.Close)

	client, cfg := clientWithCrossref(t, srv.URL)
	cfg.EnrichEnabled = false
	out := DetailsOutput{
		File:    loadRecordFixture(t, "libgen_file_mismatched_doi.json"),
		Edition: loadRecordFixture(t, "libgen_edition_mismatched_doi.json"),
	}
	attachCitations(context.Background(), client, cfg, false, &out)

	assertDOINotAsserted(t, out.Citations)
	if out.Citations.DOIStatus != string(libgen.DOIUnverified) {
		t.Errorf("DOIStatus = %q, want %q", out.Citations.DOIStatus, libgen.DOIUnverified)
	}
}

// TestAttachCitations_RegistryOutageDegrades checks that a dead Crossref costs the
// citation its DOI and nothing else. Corroboration fails closed on purpose: failing
// open would restore the fabrication the moment a third party had a bad day.
func TestAttachCitations_RegistryOutageDegrades(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	client, cfg := clientWithCrossref(t, srv.URL)
	out := DetailsOutput{
		File:    loadRecordFixture(t, "libgen_file_mismatched_doi.json"),
		Edition: loadRecordFixture(t, "libgen_edition_mismatched_doi.json"),
	}
	attachCitations(context.Background(), client, cfg, false, &out)

	assertDOINotAsserted(t, out.Citations)
	if out.Citations.DOIStatus != string(libgen.DOIUnverified) {
		t.Errorf("DOIStatus = %q, want %q", out.Citations.DOIStatus, libgen.DOIUnverified)
	}
	if !strings.Contains(out.Citations.BibTeX, "Antifragile") {
		t.Errorf("the citation itself must survive a registry outage:\n%s", out.Citations.BibTeX)
	}
}
