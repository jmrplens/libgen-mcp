package discovery

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// TestParseYear covers the shapes catalogs write a year in.
func TestParseYear(t *testing.T) {
	cases := []struct {
		in     string
		want   int
		wantOK bool
	}{
		{in: "2019", want: 2019, wantOK: true},
		{in: "2019-05-01", want: 2019, wantOK: true},
		{in: "c1999", want: 1999, wantOK: true},
		{in: "[1887?]", want: 1887, wantOK: true},
		{in: "vol 12, 2004", want: 2004, wantOK: true},
		{in: "12345", wantOK: false},
		{in: "99", wantOK: false},
		{in: "", wantOK: false},
		{in: "n.d.", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := ParseYear(tc.in)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("ParseYear(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestYearRange_Validate checks the bounds and the order a range must keep, and
// that the refusal names the argument at fault.
func TestYearRange_Validate(t *testing.T) {
	cases := []struct {
		name    string
		years   YearRange
		wantErr string
	}{
		{name: "zero", years: YearRange{}},
		{name: "closed", years: YearRange{From: 2015, To: 2020}},
		{name: "one year", years: YearRange{From: 2020, To: 2020}},
		{name: "from only", years: YearRange{From: MinYear}},
		{name: "to only", years: YearRange{To: MaxYear}},
		{name: "from too early", years: YearRange{From: 999}, wantErr: "year_from must be a year between"},
		{name: "to too late", years: YearRange{To: 20201}, wantErr: "year_to must be a year between"},
		{name: "negative", years: YearRange{From: -5}, wantErr: "year_from"},
		{name: "reversed", years: YearRange{From: 2020, To: 2015}, wantErr: "year_from (2020) is after year_to (2015)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.years.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate() = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// TestYearRange_Admits covers what a range keeps: an open side admits everything
// on it, and a year that cannot be read is kept only from a provider that applied
// the range itself.
func TestYearRange_Admits(t *testing.T) {
	cases := []struct {
		name   string
		years  YearRange
		year   string
		pushed bool
		want   bool
	}{
		{name: "zero range keeps anything", years: YearRange{}, year: "", want: true},
		{name: "inside", years: YearRange{From: 2015, To: 2016}, year: "2016", want: true},
		{name: "below", years: YearRange{From: 2015, To: 2016}, year: "2014", want: false},
		{name: "above", years: YearRange{From: 2015, To: 2016}, year: "2017-01", want: false},
		{name: "open upper side", years: YearRange{From: 2015}, year: "2099", want: true},
		{name: "open lower side", years: YearRange{To: 1900}, year: "1066", want: true},
		{name: "unknown year from a filtering provider", years: YearRange{From: 2015}, year: "", pushed: true, want: true},
		{name: "unknown year filtered here", years: YearRange{From: 2015}, year: "", pushed: false, want: false},
		{name: "known year outside from a filtering provider", years: YearRange{From: 2015}, year: "2001", pushed: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.years.Admits(tc.year, tc.pushed); got != tc.want {
				t.Errorf("Admits(%q, %v) = %v, want %v", tc.year, tc.pushed, got, tc.want)
			}
		})
	}
}

// rangeStub is a provider that can take a year range: it records the range it was
// handed and answers with its canned results.
type rangeStub struct {
	stubProvider
	mu        sync.Mutex
	gotYears  YearRange
	yearCalls int
}

// SearchYears records the range and answers as Search does.
func (p *rangeStub) SearchYears(ctx context.Context, query string, limit int, years YearRange) ([]DiscoveryResult, error) {
	p.mu.Lock()
	p.gotYears = years
	p.yearCalls++
	p.mu.Unlock()
	return p.Search(ctx, query, limit)
}

// TestFederateYears_PushesOrFilters checks the two paths: a provider that can take
// the range is handed it and its undated hits survive, while every other
// provider's hits are filtered here and its undated ones dropped. Both keep only
// in-range years either way.
func TestFederateYears_PushesOrFilters(t *testing.T) {
	hits := []DiscoveryResult{
		{Title: "In range", Year: "2015"},
		{Title: "Out of range", Year: "1999"},
		{Title: "Undated"},
	}
	pushing := &rangeStub{name: "pushing", results: hits}
	plain := &stubProvider{name: "plain", results: []DiscoveryResult{
		{Title: "Plain in range", Year: "2016"},
		{Title: "Plain out of range", Year: "2030"},
		{Title: "Plain undated"},
	}}
	years := YearRange{From: 2015, To: 2016}

	got := FederateYears(context.Background(), "q", 5, years, pushing, plain)

	var titles []string
	for _, r := range got {
		titles = append(titles, r.Title)
	}
	if want := "In range|Undated|Plain in range"; strings.Join(titles, "|") != want {
		t.Errorf("kept %q, want %q", strings.Join(titles, "|"), want)
	}
	if pushing.gotYears != years || pushing.yearCalls != 1 {
		t.Errorf("range provider handed %+v in %d calls, want %+v once", pushing.gotYears, pushing.yearCalls, years)
	}
}

// TestFederateYears_ZeroRangeIsFederate checks a zero range asks every provider
// through Search and filters nothing, which is what Federate does.
func TestFederateYears_ZeroRangeIsFederate(t *testing.T) {
	pushing := &rangeStub{name: "pushing", results: []DiscoveryResult{{Title: "Undated"}}}
	got := Federate(context.Background(), "q", 5, pushing)
	if len(got) != 1 || pushing.yearCalls != 0 || pushing.searched() != 1 {
		t.Errorf("got %d results, %d range calls, %d searches; want 1, 0 and 1", len(got), pushing.yearCalls, pushing.searched())
	}
}

// TestSearchProvider_PassesErrorsThrough checks a provider's context error reaches
// the federation layer unfiltered.
func TestSearchProvider_PassesErrorsThrough(t *testing.T) {
	failing := &rangeStub{name: "failing", err: context.Canceled}
	if _, err := searchProvider(context.Background(), failing, "q", 5, YearRange{From: 2015}); !errors.Is(err, context.Canceled) {
		t.Errorf("searchProvider() error = %v, want context.Canceled", err)
	}
}

// TestRangeSearchers_PushTheRangeIntoTheirQuery drives every provider that takes a
// range against a stand-in for its API and checks the range reached the request
// in that API's own syntax, measured against each live service on 2026-10-03.
func TestRangeSearchers_PushTheRangeIntoTheirQuery(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.RawQuery)
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(SetBasesForTest(ProviderBases{
		Arxiv: srv.URL, Crossref: srv.URL, OpenLibrary: srv.URL, DBLP: srv.URL, PubMed: srv.URL,
		Gutendex: srv.URL, ERIC: srv.URL, OpenAlex: srv.URL, EuropePMC: srv.URL,
	}))
	cases := []struct {
		name  string
		p     RangeSearcher
		years YearRange
		want  string
	}{
		{name: "arxiv", p: NewArxiv(), years: YearRange{From: 2015, To: 2016}, want: "search_query=%28all:q%29+AND+submittedDate:%5B201501010000+TO+201612312359%5D"},
		// The second arXiv case asks the same base again, inside arXiv's
		// three-second pace, so it uses a provider that does not wait for a token.
		{name: "arxiv open side", p: newUnpacedArxiv(), years: YearRange{To: 1999}, want: "submittedDate:%5B100001010000+TO+199912312359%5D"},
		{name: "crossref", p: NewCrossref(""), years: YearRange{From: 2015, To: 2016}, want: "filter=" + url.QueryEscape("from-pub-date:2015,until-pub-date:2016")},
		{name: "crossref open side", p: NewCrossref(""), years: YearRange{From: 2015}, want: "filter=" + url.QueryEscape("from-pub-date:2015")},
		{name: "openalex", p: NewOpenAlex("key"), years: YearRange{From: 2015, To: 2016}, want: "filter=" + url.QueryEscape("publication_year:2015-2016")},
		{name: "europepmc", p: NewEuropePMC(), years: YearRange{From: 2015, To: 2016}, want: "query=" + url.QueryEscape("(q) AND PUB_YEAR:[2015 TO 2016]")},
		{name: "pubmed", p: NewPubMed(""), years: YearRange{From: 2015, To: 2016}, want: "maxdate=2016&mindate=2015"},
		{name: "eric", p: NewERIC(), years: YearRange{From: 2015, To: 2016}, want: url.QueryEscape("(q) AND publicationdateyear:[2015 TO 2016]")},
		{name: "openlibrary", p: NewOpenLibrary(""), years: YearRange{From: 2015, To: 2016}, want: "q=" + url.QueryEscape("(q) first_publish_year:[2015 TO 2016]")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			seen = nil
			mu.Unlock()
			if _, err := tc.p.SearchYears(context.Background(), "q", 5, tc.years); err != nil {
				t.Fatalf("SearchYears() error = %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(seen) == 0 || !strings.Contains(seen[0], tc.want) {
				t.Errorf("request query %q, want it to contain %q", seen, tc.want)
			}
		})
	}
}

// TestExtraProviders_WhichFilterThemselves pins the providers that cannot take a
// range and so are filtered after they answer, which the search tool's
// documentation names: Gutenberg (Gutendex dates authors, not editions, so its
// hits carry no year and a range drops them all) and Anna's Archive (an HTML
// search page). dblp is not among them: its SPARQL query filters on the year.
func TestExtraProviders_WhichFilterThemselves(t *testing.T) {
	var unranged []string
	for _, p := range ExtraProviders(Settings{AnnasMirrors: staticMirrors{"https://annas-archive.invalid"}}) {
		if _, ok := p.(RangeSearcher); !ok {
			unranged = append(unranged, p.Name())
		}
	}
	if got, want := strings.Join(unranged, ","), "gutenberg,annas"; got != want {
		t.Errorf("providers filtered after the fact = %s, want %s", got, want)
	}
}
