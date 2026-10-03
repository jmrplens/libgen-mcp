package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// startEuropePMC serves status and body at /search, points europePMCBase at the
// server for the test, and returns where the request's query is recorded.
func startEuropePMC(t *testing.T, status int, body []byte) *url.Values {
	t.Helper()
	var seen url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Query()
		if status != 0 {
			w.WriteHeader(status)
		}
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := europePMCBase
	europePMCBase = srv.URL
	t.Cleanup(func() { europePMCBase = old })
	return &seen
}

// TestEuropePMCSearch_MapsTheLiveFixture runs the provider against a lite search
// recorded from the live API (query "crispr off-target effects", 2026-10-03): an
// open-access PMC article becomes an open_access hit with its JATS full text, and a
// PubMed-only record a bibliographic one.
func TestEuropePMCSearch_MapsTheLiveFixture(t *testing.T) {
	seen := startEuropePMC(t, 0, readFixture(t, "europepmc_search.json"))
	got, err := NewEuropePMC().Search(context.Background(), "crispr off-target effects", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("Search() returned %d results, want 5", len(got))
	}
	cases := []struct {
		name string
		got  DiscoveryResult
		want DiscoveryResult
	}{
		{name: "pubmed only", got: got[2], want: DiscoveryResult{
			Origin: "europepmc", Title: "Quantifying CRISPR off-target effects", Authors: "Gkazi SA",
			Year: "2019", DOI: "10.1042/etls20180146", Venue: "Emerg Top Life Sci",
		}},
		{name: "open access in pmc", got: got[3], want: DiscoveryResult{
			Origin:  "europepmc",
			Title:   "Synthetic switch to minimize CRISPR off-target effects by self-restricting Cas9 transcription and translation",
			Authors: "Shen CC; Hsu MN; Chang CW; Lin MW; Hwu JR; Tu Y; Hu YC",
			Year:    "2019", DOI: "10.1093/nar/gky1165", Venue: "Nucleic Acids Res",
			FullTextURL: europePMCBase + "/PMC6379646/fullTextXML",
			OpenAccess:  true,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("result = %+v\nwant     %+v", tc.got, tc.want)
			}
		})
	}
	checks := map[string]string{
		"query":      "crispr off-target effects",
		"format":     "json",
		"resultType": "lite",
		"pageSize":   "5",
	}
	for name, want := range checks {
		t.Run("request "+name, func(t *testing.T) {
			if got := seen.Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		})
	}
}

// TestEuropePMCSearch_HeldButNotOpenIsNotOpenAccess checks the case the flags were
// measured for: a full text Europe PMC holds under an author-manuscript or
// free-to-read arrangement, inEPMC=Y with isOpenAccess=N, is not marked open access
// and gets no full-text URL, since the REST endpoint refuses it.
func TestEuropePMCSearch_HeldButNotOpenIsNotOpenAccess(t *testing.T) {
	startEuropePMC(t, 0, readFixture(t, "europepmc_free_not_oa.json"))
	got, err := NewEuropePMC().Search(context.Background(), "crispr", 2)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Search() returned %d results, want 2", len(got))
	}
	for _, r := range got {
		t.Run(r.DOI, func(t *testing.T) {
			if r.OpenAccess || r.FullTextURL != "" {
				t.Errorf("held-but-not-open record = %+v, want no open_access and no full_text_url", r)
			}
		})
	}
}

// TestEuropePMCSearch_DegradesToEmpty covers the failures that must cost a
// federated search nothing, and the zero-hit answer the live API gives.
func TestEuropePMCSearch_DegradesToEmpty(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   []byte
	}{
		{name: "zero hits", body: readFixture(t, "europepmc_zero_hits.json")},
		{name: "server error", status: http.StatusInternalServerError, body: []byte("oops")},
		{name: "not json", body: []byte("<html>")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			startEuropePMC(t, tc.status, tc.body)
			got, err := NewEuropePMC().Search(context.Background(), "q", 5)
			if err != nil || len(got) != 0 {
				t.Errorf("Search() = %d results, %v; want none and no error", len(got), err)
			}
		})
	}
	t.Run("unreachable", func(t *testing.T) {
		old := europePMCBase
		europePMCBase = "http://127.0.0.1:1"
		t.Cleanup(func() { europePMCBase = old })
		got, err := NewEuropePMC().Search(context.Background(), "q", 5)
		if err != nil || len(got) != 0 {
			t.Errorf("Search() = %d results, %v; want none and no error", len(got), err)
		}
	})
}

// TestEuropePMCRecordToResult covers the mapping rules the fixtures do not reach.
func TestEuropePMCRecordToResult(t *testing.T) {
	cases := []struct {
		name   string
		rec    europePMCRecord
		want   DiscoveryResult
		wantOK bool
	}{
		{name: "neither title nor doi", rec: europePMCRecord{PubYear: "2020"}, wantOK: false},
		{
			name: "open access without a pmcid", rec: europePMCRecord{DOI: "10.1/x", IsOpenAccess: "Y", InEPMC: "Y"},
			want: DiscoveryResult{Origin: "europepmc", DOI: "10.1/x"}, wantOK: true,
		},
		{
			name: "open access not held", rec: europePMCRecord{DOI: "10.1/x", PMCID: "PMC1", IsOpenAccess: "Y", InEPMC: "N"},
			want: DiscoveryResult{Origin: "europepmc", DOI: "10.1/x"}, wantOK: true,
		},
		{
			name: "a title only", rec: europePMCRecord{Title: " A  Title. "},
			want: DiscoveryResult{Origin: "europepmc", Title: "A Title"}, wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := europePMCRecordToResult(&tc.rec)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Errorf("result = %+v\nwant     %+v", got, tc.want)
			}
		})
	}
}

// TestEuropePMCAuthors checks the authorString is split on its commas, its final
// period dropped, and a long list capped.
func TestEuropePMCAuthors(t *testing.T) {
	names := make([]string, maxHitAuthors+2)
	for i := range names {
		names[i] = "A" + strconv.Itoa(i) + " X"
	}
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "a period only", in: " . ", want: ""},
		{name: "two", in: "Gkazi SA, Smith J.", want: "Gkazi SA; Smith J"},
		{name: "past the cap", in: strings.Join(names, ", ") + ".", want: strings.Join(names[:maxHitAuthors], "; ") + "; et al."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := europePMCAuthors(tc.in); got != tc.want {
				t.Errorf("europePMCAuthors() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClampEuropePMCLimit covers the default, the clamp, and a value in range.
func TestClampEuropePMCLimit(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{name: "zero", in: 0, want: europePMCDefaultLimit},
		{name: "in range", in: 7, want: 7},
		{name: "too many", in: 1000, want: europePMCMaxLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampEuropePMCLimit(tc.in); got != tc.want {
				t.Errorf("clampEuropePMCLimit(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestNewEuropePMC_NamesItself checks the origin the provider stamps.
func TestNewEuropePMC_NamesItself(t *testing.T) {
	if got := NewEuropePMC().Name(); got != "europepmc" {
		t.Errorf("Name() = %q, want europepmc", got)
	}
}
