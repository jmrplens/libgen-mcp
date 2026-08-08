package libgen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

// TestCheckDOITitle covers the three verdict bands the title comparison resolves
// to, and the shapes that must land in each. The middle band matters as much as
// the ends: two titles that merely share a stem are not evidence of a mismatch,
// and quietly declining to confirm is the only honest answer there.
func TestCheckDOITitle(t *testing.T) {
	tests := []struct {
		name          string
		record        string
		crossref      string
		wantVerdict   DOIVerdict
		wantCrossref  string
		reasonForBand string
	}{
		{
			name: "identical titles confirm", record: "Hallmarks of Cancer: The Next Generation",
			crossref: "Hallmarks of Cancer: The Next Generation", wantVerdict: DOIConfirmed,
			wantCrossref: "Hallmarks of Cancer: The Next Generation",
		},
		{
			name: "catalog dropped the subtitle", record: "Hallmarks of Cancer",
			crossref: "Hallmarks of Cancer: The Next Generation", wantVerdict: DOIConfirmed,
			wantCrossref: "Hallmarks of Cancer: The Next Generation",
		},
		{
			name: "punctuation and case differ only", record: "why most published research findings are FALSE",
			crossref: "Why Most Published Research Findings Are False!", wantVerdict: DOIConfirmed,
			wantCrossref: "Why Most Published Research Findings Are False!",
		},
		{
			name: "JATS markup is not content", record: "The Role of H2O in Catalysis",
			crossref: "The Role of H<sub>2</sub>O in Catalysis", wantVerdict: DOIConfirmed,
			wantCrossref: "The Role of H<sub>2</sub>O in Catalysis",
		},
		{
			name: "the live corrupt catalog record", record: "Antifragile: Things That Gain from Disorder",
			crossref: "Why Most Published Research Findings Are False", wantVerdict: DOIMismatch,
			wantCrossref: "Why Most Published Research Findings Are False",
		},
		{
			name: "sibling papers are not judged", record: "Hallmarks of Aging",
			crossref: "Hallmarks of Cancer", wantVerdict: DOIUnverified,
			wantCrossref: "Hallmarks of Cancer",
		},
		{
			name: "no comparable words on one side", record: "The",
			crossref: "Why Most Published Research Findings Are False", wantVerdict: DOIUnverified,
			wantCrossref: "Why Most Published Research Findings Are False",
		},
		{
			name: "registry returned no title", record: "Antifragile",
			crossref: "", wantVerdict: DOIUnverified, wantCrossref: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckDOITitle(tc.record, tc.crossref)
			if got.Verdict != tc.wantVerdict {
				t.Errorf("verdict = %q, want %q", got.Verdict, tc.wantVerdict)
			}
			if got.CrossrefTitle != tc.wantCrossref {
				t.Errorf("CrossrefTitle = %q, want %q", got.CrossrefTitle, tc.wantCrossref)
			}
		})
	}
}

// TestTitleTokensDropsStopwordsAndRepeats checks the tokenizer keeps only the
// content words and keeps each once, so a title padded with function words or a
// repeated term cannot inflate an overlap score.
func TestTitleTokensDropsStopwordsAndRepeats(t *testing.T) {
	got := titleTokens("The Rise and the Rise of the Machine")
	want := map[string]bool{"rise": true, "machine": true}
	if len(got) != len(want) {
		t.Fatalf("tokens = %v, want %v", got, want)
	}
	for tok := range want {
		if !got[tok] {
			t.Errorf("missing token %q in %v", tok, got)
		}
	}
}

// TestTitleTokensKeepsNonASCIILetters checks that accented and non-Latin titles
// keep their words while typographic punctuation still separates them: dropping
// every rune above ASCII would empty such a title and make every non-English
// record permanently unverifiable, and keeping every rune above ASCII would glue
// "Antifragile—Things" into one token no plainer spelling could ever match.
func TestTitleTokensKeepsNonASCIILetters(t *testing.T) {
	got := titleTokens("Éléments—mathématique")
	for _, want := range []string{"éléments", "mathématique"} {
		if !got[want] {
			t.Errorf("missing token %q in %v", want, got)
		}
	}
	if len(got) != 2 {
		t.Errorf("tokens = %v, want exactly the two content words", got)
	}
}

// newVerifyClient returns a client whose Crossref lookups go to base.
func newVerifyClient(t *testing.T, base string) *Client {
	t.Helper()
	cfg := &config.Config{
		DownloadDir: t.TempDir(), Timeout: 5 * time.Second,
		RateRPS: 1000, RateBurst: 100, RetryAttempts: 1,
	}
	return New(staticMirrorList{}, cfg, WithEnrichBaseURLs(base, base))
}

// staticMirrorList is an empty MirrorLister: these tests never touch the catalog.
type staticMirrorList []string

// Mirrors implements MirrorLister.
func (s staticMirrorList) Mirrors(context.Context) []string { return s }

// TestVerifyDOI covers the network path end to end: a registry that names a
// different work yields DOIMismatch, and every way the lookup can come up empty —
// a blank DOI, a blank record title, a failing registry — yields DOIUnverified
// without an error, because corroboration is advisory and must never break a call.
func TestVerifyDOI(t *testing.T) {
	const body = `{"message":{"title":["Why Most Published Research Findings Are False"]}}`
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ok.Close)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(down.Close)

	tests := []struct {
		name, base, doi, title string
		want                   DOIVerdict
	}{
		{"registry names another work", ok.URL, "10.1371/journal.pmed.0020124", "Antifragile: Things That Gain from Disorder", DOIMismatch},
		{"registry confirms", ok.URL, "10.1371/journal.pmed.0020124", "Why Most Published Research Findings Are False", DOIConfirmed},
		{"no doi to check", ok.URL, "  ", "Antifragile", DOIUnverified},
		{"no title to compare", ok.URL, "10.1371/journal.pmed.0020124", "  ", DOIUnverified},
		{"registry unreachable", down.URL, "10.1371/journal.pmed.0020124", "Antifragile", DOIUnverified},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := newVerifyClient(t, tc.base).VerifyDOI(context.Background(), tc.doi, tc.title)
			if got.Verdict != tc.want {
				t.Errorf("verdict = %q, want %q", got.Verdict, tc.want)
			}
		})
	}
}
