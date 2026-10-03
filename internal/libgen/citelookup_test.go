package libgen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// corpusEntry is one recorded reference: the text as pasted and Crossref's
// answer to it, captured from the live API on 2026-10-03.
type corpusEntry struct {
	Citation string          `json:"citation"`
	Crossref json.RawMessage `json:"crossref"`
}

// loadCitationCorpus reads testdata/citation_corpus.json.
func loadCitationCorpus(t *testing.T) []corpusEntry {
	t.Helper()
	raw, err := os.ReadFile("testdata/citation_corpus.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var entries []corpusEntry
	if err = json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("decode corpus: %v", err)
	}
	return entries
}

// TestJudgeCitation_Corpus replays the 30 references the thresholds were chosen
// from and pins the verdict on each. A row with no DOI is one whose right
// answer is not Crossref's best candidate, or is not in Crossref at all, so the
// lookup must hand the candidates back rather than choose. Each row logs the
// two measured quantities, which is the evidence the thresholds rest on.
func TestJudgeCitation_Corpus(t *testing.T) {
	want := []struct {
		name, doi string
	}{
		{"Hanahan 2011 Vancouver", "10.1016/j.cell.2011.02.013"},
		{"Ioannidis 2005 against its own correction", "10.1371/journal.pmed.0020124"},
		{"LeCun 2015 APA", "10.1038/nature14539"},
		{"Watson and Crick against reprints", "10.1038/171737a0"},
		{"He 2016 CVPR", "10.1109/cvpr.2016.90"},
		{"Vaswani 2017 has no Crossref DOI", ""},
		{"Shannon 1948 against part two", "10.1002/j.1538-7305.1948.tb01338.x"},
		{"Kucsko 2013 Nature style", "10.1038/nature12373"},
		{"Lowry 1951 MLA", "10.1016/s0021-9258(19)52451-6"},
		{"Knuth book leads with an erratum", ""},
		{"fabricated salmon paper", ""},
		{"fabricated protein folding paper", ""},
		{"terse Hanahan 2000 is too close to call", ""},
		{"Taleb book leads with a review", ""},
		{"Jumper 2021 against its companion", "10.1038/s41586-021-03819-2"},
		{"Kingma arXiv preprint", ""},
		{"Fisher 1936", "10.1111/j.1469-1809.1936.tb02137.x"},
		{"Bradford 1976 long title", "10.1016/0003-2697(76)90527-3"},
		{"Kahneman 1979 against reprints", "10.2307/1914185"},
		{"Hochreiter 1997", "10.1162/neco.1997.9.8.1735"},
		{"Einstein 1905 without the umlaut", "10.1002/andp.19053221004"},
		{"Altschul 1990", "10.1016/s0022-2836(05)80360-2"},
		{"Hanahan 2011 with typos", "10.1016/j.cell.2011.02.013"},
		{"fabricated Hinton paper", ""},
		{"PageRank tech report", ""},
		{"Cohen 1992 two-word title", "10.1037/0033-2909.112.1.155"},
		{"FAIR principles lead with a paper quoting them", ""},
		{"Granovetter 1973 Chicago", "10.1086/225469"},
		{"bare title words", ""},
		{"Tversky 1974 against its book reprint", ""},
	}
	corpus := loadCitationCorpus(t)
	if len(corpus) != len(want) {
		t.Fatalf("corpus has %d entries, table has %d", len(corpus), len(want))
	}
	for i, tc := range want {
		t.Run(tc.name, func(t *testing.T) {
			candidates, err := parseCitationCandidates(bytes.NewReader(corpus[i].Crossref))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := JudgeCitation(corpus[i].Citation, candidates)
			t.Logf("ratio %.3f coverage %.2f", scoreRatio(candidates), CitationTitleCoverage(candidates[0].Title, corpus[i].Citation))
			if got.DOI != tc.doi {
				t.Errorf("DOI = %q, want %q (reason %q)", got.DOI, tc.doi, got.Reason)
			}
			if wantStatus := statusFor(tc.doi); got.Status != wantStatus {
				t.Errorf("status = %q, want %q", got.Status, wantStatus)
			}
			if !got.IsResolved() && len(got.Candidates) == 0 {
				t.Error("an unresolved match must hand the candidates back")
			}
		})
	}
}

// statusFor is the status a row expects: resolved when it names a DOI.
func statusFor(doi string) string {
	if doi == "" {
		return CitationUnresolved
	}
	return CitationResolved
}

// TestJudgeCitation_Edges covers the shapes the corpus does not: no candidate
// at all, a lone candidate, a runner-up scored at zero, and a candidate with no
// title to compare.
func TestJudgeCitation_Edges(t *testing.T) {
	const cite = "Cohen J. A power primer. Psychol Bull. 1992"
	tests := []struct {
		name       string
		candidates []CitationCandidate
		wantDOI    string
		wantReason string
	}{
		{"no candidates", nil, "", "no candidate"},
		{"lone candidate with its title", []CitationCandidate{{DOI: "10.1/a", Title: "A power primer", Score: 10}}, "10.1/a", ""},
		{"runner-up at zero", []CitationCandidate{{DOI: "10.1/a", Title: "A power primer", Score: 10}, {DOI: "10.1/b", Score: 0}}, "10.1/a", ""},
		{"lone candidate without a title", []CitationCandidate{{DOI: "10.1/a", Score: 10}}, "", "0% of its title"},
		{"too close to call", []CitationCandidate{{DOI: "10.1/a", Title: "A power primer", Score: 10}, {DOI: "10.1/b", Title: "A power primer", Score: 9}}, "", "No candidate stands out"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := JudgeCitation(cite, tc.candidates)
			if got.DOI != tc.wantDOI {
				t.Errorf("DOI = %q, want %q", got.DOI, tc.wantDOI)
			}
			if !strings.Contains(got.Reason, tc.wantReason) {
				t.Errorf("reason = %q, want it to contain %q", got.Reason, tc.wantReason)
			}
		})
	}
}

// TestCitationTitleCoverage pins the word comparison: a typo or a lost
// diacritic in a long word still matches, a short word does not stretch, and a
// title with no content words scores nothing.
func TestCitationTitleCoverage(t *testing.T) {
	tests := []struct {
		name, title, citation string
		want                  float64
	}{
		{"exact", "Deep learning", "LeCun Y. Deep learning. Nature 2015", 1},
		{"typo in a long word", "Hallmarks of Cancer", "Hanahan. Hallmarks of cancr", 1},
		{"diacritic dropped", "Zur Elektrodynamik bewegter Körper", "Einstein. Zur Elektrodynamik bewegter Korper", 1},
		{"short words do not stretch", "Cell call", "cell", 0.5},
		{"two edits are not one", "generation", "generatn", 0},
		{"insertion matches", "generation", "generations", 1},
		{"no content words", "The", "The", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CitationTitleCoverage(tc.title, tc.citation); math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("coverage = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWithinOneEdit covers each kind of single edit and the shapes just past
// one.
func TestWithinOneEdit(t *testing.T) {
	tests := []struct {
		name, a, b string
		want       bool
	}{
		{"equal", "cancer", "cancer", true},
		{"substitution", "cancer", "cancor", true},
		{"substitution at the end", "cancer", "cancex", true},
		{"deletion", "cancer", "cancr", true},
		{"insertion at the end", "cancer", "cancers", true},
		{"two substitutions", "cancer", "canxxr", false},
		{"length differs by two", "cancer", "canc", false},
		{"insertion then a difference", "cancer", "caxncxr", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := withinOneEdit([]rune(tc.a), []rune(tc.b)); got != tc.want {
				t.Errorf("withinOneEdit(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestCandidateAuthors covers person and organization names and the cut to
// three with et al.
func TestCandidateAuthors(t *testing.T) {
	tests := []struct {
		name    string
		authors []crossrefAuthor
		want    string
	}{
		{"none", nil, ""},
		{"person and organization", []crossrefAuthor{{Given: "Douglas", Family: "Hanahan"}, {Name: "The AlphaFold Team"}}, "Douglas Hanahan, The AlphaFold Team"},
		{"blank entries skipped", []crossrefAuthor{{}, {Family: "Cohen"}}, "Cohen"},
		{"cut at three", []crossrefAuthor{{Family: "A"}, {Family: "B"}, {Family: "C"}, {Family: "D"}}, "A, B, C et al."},
		{"exactly three", []crossrefAuthor{{Family: "A"}, {Family: "B"}, {Family: "C"}}, "A, B, C"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := candidateAuthors(tc.authors); got != tc.want {
				t.Errorf("candidateAuthors = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParseCitationCandidates checks the decode: items without a DOI are
// dropped, markup leaves the title, and the list is re-sorted best first
// whatever order the body used.
func TestParseCitationCandidates(t *testing.T) {
	body := `{"message":{"items":[
		{"DOI":"10.1/low","score":5,"title":["Low"]},
		{"DOI":"","score":50,"title":["No DOI"]},
		{"DOI":"10.1/high","score":9,"title":["The Role of H<sub>2</sub>O"],"container-title":["J"],"issued":{"date-parts":[[1999,2]]}}
	]}}`
	got, err := parseCitationCandidates(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 2 || got[0].DOI != "10.1/high" || got[1].DOI != "10.1/low" {
		t.Fatalf("candidates = %+v", got)
	}
	if got[0].Title != "The Role of H2O" || got[0].Year != 1999 || got[0].Container != "J" {
		t.Errorf("best = %+v", got[0])
	}
	if _, err = parseCitationCandidates(strings.NewReader("not json")); err == nil {
		t.Error("a malformed body must be an error")
	}
}

// TestResolveCitation drives the network path: the query reaches Crossref's
// works search with the bibliographic field and the polite User-Agent, a
// recorded answer resolves, and a failing or malformed registry is an error
// that names nothing but the registry.
func TestResolveCitation(t *testing.T) {
	corpus := loadCitationCorpus(t)
	var gotQuery, gotPath, gotUA atomic.Value
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.Path)
		gotQuery.Store(r.URL.Query().Get("query.bibliographic"))
		gotUA.Store(r.UserAgent())
		_, _ = w.Write(corpus[0].Crossref)
	}))
	t.Cleanup(ok.Close)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(down.Close)
	garbled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>"))
	}))
	t.Cleanup(garbled.Close)

	c := newVerifyClient(t, ok.URL)
	c.enrichEmail = "ops@example.org"
	got, err := c.ResolveCitation(context.Background(), corpus[0].Citation)
	if err != nil {
		t.Fatalf("ResolveCitation: %v", err)
	}
	if got.DOI != "10.1016/j.cell.2011.02.013" || got.Title != "Hallmarks of Cancer: The Next Generation" {
		t.Errorf("match = %+v", got)
	}
	if gotPath.Load() != "/works" || gotQuery.Load() != corpus[0].Citation {
		t.Errorf("request path %v query %v", gotPath.Load(), gotQuery.Load())
	}
	if ua, _ := gotUA.Load().(string); !strings.Contains(ua, "mailto:ops@example.org") {
		t.Errorf("User-Agent %q lacks the polite-pool contact", ua)
	}

	for _, base := range []string{down.URL, garbled.URL} {
		t.Run(base, func(t *testing.T) {
			_, searchErr := newVerifyClient(t, base).ResolveCitation(context.Background(), "anything")
			if !errors.Is(searchErr, errCitationSearch) {
				t.Errorf("err = %v, want errCitationSearch", searchErr)
			}
		})
	}
}

// TestResolveCitation_ContextEnded reports a call whose deadline passed as the search
// failing because of the cancellation, so the caller can tell the two apart.
func TestResolveCitation_ContextEnded(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		http.Error(w, "late", http.StatusGatewayTimeout)
	}))
	t.Cleanup(slow.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := newVerifyClient(t, slow.URL).ResolveCitation(ctx, "anything")
	if !errors.Is(err, errCitationSearch) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want errCitationSearch wrapping the deadline", err)
	}
}
