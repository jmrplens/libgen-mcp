package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/v2/internal/openalex"
)

// openAlexClock is the fixed instant the provider's budget is judged against.
var openAlexClock = time.Date(2026, 10, 3, 19, 0, 0, 0, time.UTC)

// openAlexStub is a fake OpenAlex works endpoint. It answers with the configured
// status, body and rate-limit headers, and records every request it receives so a
// test can assert both what was asked and whether anything was asked at all.
type openAlexStub struct {
	status    int
	body      []byte
	remaining string
	reset     string

	calls atomic.Int32
	query url.Values
	path  string
	auth  string
}

// ServeHTTP records the request and writes the configured response.
func (s *openAlexStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.calls.Add(1)
	s.query, s.path, s.auth = r.URL.Query(), r.URL.Path, r.Header.Get("Authorization")
	if s.remaining != "" {
		w.Header().Set("X-RateLimit-Remaining", s.remaining)
		w.Header().Set("X-RateLimit-Reset", s.reset)
	}
	if s.status != 0 {
		w.WriteHeader(s.status)
	}
	_, _ = w.Write(s.body)
}

// startOpenAlex serves stub, points openAlexBase at it for the test, and returns a
// provider with the given key and a private budget, so no test reads or writes
// the process-wide one.
func startOpenAlex(t *testing.T, stub *openAlexStub, key string) *OpenAlexProvider {
	t.Helper()
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	old := openAlexBase
	openAlexBase = srv.URL
	t.Cleanup(func() { openAlexBase = old })
	p := NewOpenAlex(key)
	p.budget = &openalex.Budget{}
	p.now = func() time.Time { return openAlexClock }
	return p
}

// TestOpenAlexSearch_MapsTheLiveFixture runs the provider against a works page
// recorded from the live API (search "crispr off-target effects", 2026-10-03) and
// checks the three shapes it holds: a closed paper, an open-access paper with a
// direct PDF, and an open-access paper whose best location is a landing page.
func TestOpenAlexSearch_MapsTheLiveFixture(t *testing.T) {
	stub := &openAlexStub{body: readFixture(t, "openalex_works.json")}
	p := startOpenAlex(t, stub, "")

	got, err := p.Search(context.Background(), "crispr off-target effects", 5)
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
		{name: "closed", got: got[1], want: DiscoveryResult{
			Origin: "openalex", Title: "Characterizing CRISPR off-target effects", Year: "2013",
			DOI: "10.1038/nrg3651", Venue: "Nature Reviews Genetics",
		}},
		{name: "open access with a pdf", got: got[3], want: DiscoveryResult{
			Origin: "openalex", Year: "2018", DOI: "10.1093/nar/gky1165", Venue: "Nucleic Acids Research",
			PDFURL:     "https://academic.oup.com/nar/article-pdf/47/3/e13/27853903/gky1165.pdf",
			OpenAccess: true,
		}},
		{name: "open access with a landing page only", got: got[4], want: DiscoveryResult{
			Origin: "openalex", Year: "2022", DOI: "10.1093/procel/pwac018", Venue: "Protein & Cell",
			OpenAccess: true,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.got
			if tc.want.Title == "" {
				got.Title = ""
			}
			got.Authors = ""
			if got != tc.want {
				t.Errorf("result = %+v\nwant     %+v", got, tc.want)
			}
		})
	}
	if strings.Count(got[3].Authors, "; ") != 6 || strings.Contains(got[3].Authors, "et al.") {
		t.Errorf("authors = %q, want the seven names joined", got[3].Authors)
	}
}

// TestOpenAlexSearch_RequestShape pins the request: the works route, the query as
// search, the clamped page size, the projection, no mailto (OpenAlex ignores it)
// and the key in the header and never in the URL.
func TestOpenAlexSearch_RequestShape(t *testing.T) {
	stub := &openAlexStub{body: []byte(`{"results":[]}`)}
	p := startOpenAlex(t, stub, "oa-secret")

	if _, err := p.Search(context.Background(), "attention is all you need", 500); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if stub.path != "/works" {
		t.Errorf("path = %q, want /works", stub.path)
	}
	checks := map[string]string{
		"search":   "attention is all you need",
		"per_page": strconv.Itoa(openAlexMaxLimit),
		"select":   openAlexSelect,
		"mailto":   "",
		"api_key":  "",
	}
	for name, want := range checks {
		t.Run(name, func(t *testing.T) {
			if got := stub.query.Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		})
	}
	if stub.auth != "Bearer oa-secret" {
		t.Errorf("Authorization = %q, want the key as a bearer token", stub.auth)
	}
}

// TestOpenAlexSearch_KeylessBudgetGuard checks the reserve: a keyless provider
// whose budget cannot pay for a search without dipping into the reserve sends
// nothing and returns an empty result, while a provider with a key searches
// regardless, because its allowance is not the keyless one.
func TestOpenAlexSearch_KeylessBudgetGuard(t *testing.T) {
	cases := []struct {
		name      string
		key       string
		remaining int
		wantCalls int32
	}{
		{name: "keyless with room", remaining: openalex.KeylessReserve + openalex.SearchCost, wantCalls: 1},
		{name: "keyless at the reserve", remaining: openalex.KeylessReserve + openalex.SearchCost - 1, wantCalls: 0},
		{name: "keyless and exhausted", remaining: 0, wantCalls: 0},
		{name: "keyed and exhausted", key: "oa-secret", remaining: 0, wantCalls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &openAlexStub{body: readFixture(t, "openalex_works.json")}
			p := startOpenAlex(t, stub, tc.key)
			p.budget.Observe(http.StatusOK, http.Header{
				"X-Ratelimit-Remaining": {strconv.Itoa(tc.remaining)},
				"X-Ratelimit-Reset":     {"3600"},
			}, openAlexClock)

			got, err := p.Search(context.Background(), "crispr", 5)
			if err != nil {
				t.Fatalf("Search() error = %v", err)
			}
			if calls := stub.calls.Load(); calls != tc.wantCalls {
				t.Errorf("requests sent = %d, want %d", calls, tc.wantCalls)
			}
			if tc.wantCalls == 0 && len(got) != 0 {
				t.Errorf("a skipped search returned %d results", len(got))
			}
		})
	}
}

// TestOpenAlexSearch_ReportsTheBudget checks the rate-limit headers of an
// answered response reach the budget, and that a refusal's count does not: a 429
// closes the budget for its wait instead.
func TestOpenAlexSearch_ReportsTheBudget(t *testing.T) {
	p := startOpenAlex(t, &openAlexStub{body: readFixture(t, "openalex_works.json"), remaining: "740", reset: "15000"}, "")
	got, err := p.Search(context.Background(), "crispr", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(got) != 5 {
		t.Errorf("Search() returned %d results, want 5", len(got))
	}
	if credits, _, ok := p.budget.Remaining(openAlexClock); !ok || credits != 740 {
		t.Errorf("budget = %d (known %v), want 740", credits, ok)
	}

	refused := startOpenAlex(t, &openAlexStub{status: http.StatusTooManyRequests, body: []byte(`{"error":"Rate limit exceeded"}`), remaining: "0", reset: "15000"}, "")
	if _, refusedErr := refused.Search(context.Background(), "crispr", 5); refusedErr != nil {
		t.Fatalf("Search() error = %v", refusedErr)
	}
	if _, _, ok := refused.budget.Remaining(openAlexClock); ok {
		t.Error("a 429's remaining count was recorded as the day's")
	}
	if refused.budget.Spend(1, 0, openAlexClock) {
		t.Error("the budget is open right after a 429")
	}
}

// TestOpenAlexSearch_BareRefusalClosesTheBudget checks a 429 with nothing but a
// Retry-After keeps the next search from being sent, and only for that wait.
func TestOpenAlexSearch_BareRefusalClosesTheBudget(t *testing.T) {
	stub := &openAlexStub{status: http.StatusTooManyRequests, body: []byte(`{"error":"Too many requests"}`)}
	p := startOpenAlex(t, stub, "")
	if _, err := p.Search(context.Background(), "crispr", 5); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if _, err := p.Search(context.Background(), "crispr", 5); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if calls := stub.calls.Load(); calls != 1 {
		t.Errorf("requests sent = %d, want the refused one only", calls)
	}
	p.now = func() time.Time { return openAlexClock.Add(2 * time.Minute) }
	if _, err := p.Search(context.Background(), "crispr", 5); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if calls := stub.calls.Load(); calls != 2 {
		t.Errorf("requests sent = %d, want a second once the refusal's wait passed", calls)
	}
}

// TestOpenAlexSearch_DegradesToEmpty covers the failures that must cost a
// federated search nothing: an error status, a body that is not JSON, and a
// server that cannot be reached.
func TestOpenAlexSearch_DegradesToEmpty(t *testing.T) {
	cases := []struct {
		name string
		stub *openAlexStub
	}{
		{name: "server error", stub: &openAlexStub{status: http.StatusInternalServerError, body: []byte("oops")}},
		{name: "not json", stub: &openAlexStub{body: []byte("<html>")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := startOpenAlex(t, tc.stub, "")
			got, err := p.Search(context.Background(), "q", 5)
			if err != nil || len(got) != 0 {
				t.Errorf("Search() = %d results, %v; want none and no error", len(got), err)
			}
		})
	}
	t.Run("unreachable", func(t *testing.T) {
		p := startOpenAlex(t, &openAlexStub{}, "")
		openAlexBase = "http://127.0.0.1:1"
		got, err := p.Search(context.Background(), "q", 5)
		if err != nil || len(got) != 0 {
			t.Errorf("Search() = %d results, %v; want none and no error", len(got), err)
		}
	})
	t.Run("malformed base", func(t *testing.T) {
		p := startOpenAlex(t, &openAlexStub{}, "")
		openAlexBase = "http://[::1"
		got, err := p.Search(context.Background(), "q", 5)
		if err != nil || len(got) != 0 {
			t.Errorf("Search() = %d results, %v; want none and no error", len(got), err)
		}
	})
}

// TestOpenAlexSearch_RefundsASearchNeverSent checks a keyless search that never
// reached OpenAlex, because the caller went away or the host could not be
// reached, gives its ten credits back.
func TestOpenAlexSearch_RefundsASearchNeverSent(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name string
		ctx  context.Context
		base string
	}{
		{name: "canceled before the limiter", ctx: canceled},
		{name: "unreachable", ctx: context.Background(), base: "http://127.0.0.1:1"},
		{name: "malformed base", ctx: context.Background(), base: "http://[::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := startOpenAlex(t, &openAlexStub{}, "")
			if tc.base != "" {
				openAlexBase = tc.base
			}
			p.budget.Observe(http.StatusOK, http.Header{
				"X-Ratelimit-Remaining": {"500"},
				"X-Ratelimit-Reset":     {"3600"},
			}, openAlexClock)
			_, _ = p.Search(tc.ctx, "q", 5)
			if credits, _, _ := p.budget.Remaining(openAlexClock); credits != 500 {
				t.Errorf("remaining = %d after a search never sent, want 500", credits)
			}
		})
	}
}

// TestOpenAlexWorkToResult covers the mapping rules the fixture does not reach.
func TestOpenAlexWorkToResult(t *testing.T) {
	oaPDF := func(raw string) *openAlexLocation { return &openAlexLocation{IsOA: true, PDFURL: raw} }
	cases := []struct {
		name   string
		work   openAlexWork
		want   DiscoveryResult
		wantOK bool
	}{
		{name: "neither title nor doi", work: openAlexWork{PublicationYear: 2020}, wantOK: false},
		{
			name: "a title without a doi or a year", work: openAlexWork{DisplayName: "  A   Title "},
			want: DiscoveryResult{Origin: "openalex", Title: "A Title"}, wantOK: true,
		},
		{
			name: "a bare doi", work: openAlexWork{DOI: "10.1/x"},
			want: DiscoveryResult{Origin: "openalex", DOI: "10.1/x"}, wantOK: true,
		},
		{
			name: "a doi: prefix", work: openAlexWork{DOI: "DOI:10.1/x"},
			want: DiscoveryResult{Origin: "openalex", DOI: "10.1/x"}, wantOK: true,
		},
		{
			name: "an http resolver", work: openAlexWork{DOI: "http://doi.org/10.1/x"},
			want: DiscoveryResult{Origin: "openalex", DOI: "10.1/x"}, wantOK: true,
		},
		{
			name: "a dx resolver", work: openAlexWork{DOI: "https://DX.DOI.ORG/10.1/x"},
			want: DiscoveryResult{Origin: "openalex", DOI: "10.1/x"}, wantOK: true,
		},
		{
			name: "doi.org without a scheme is left alone", work: openAlexWork{DOI: "doi.org/10.1/x"},
			want: DiscoveryResult{Origin: "openalex", DOI: "doi.org/10.1/x"}, wantOK: true,
		},
		{
			name: "a pdf at a closed location", work: openAlexWork{DOI: "10.1/x", BestOALocation: &openAlexLocation{PDFURL: "https://h/x.pdf"}},
			want: DiscoveryResult{Origin: "openalex", DOI: "10.1/x"}, wantOK: true,
		},
		{
			name: "a relative pdf", work: openAlexWork{DOI: "10.1/x", BestOALocation: oaPDF("/x.pdf")},
			want: DiscoveryResult{Origin: "openalex", DOI: "10.1/x"}, wantOK: true,
		},
		{
			name: "a script pdf", work: openAlexWork{DOI: "10.1/x", BestOALocation: oaPDF("javascript:alert(1)")},
			want: DiscoveryResult{Origin: "openalex", DOI: "10.1/x"}, wantOK: true,
		},
		{
			name: "an unparseable pdf", work: openAlexWork{DOI: "10.1/x", BestOALocation: oaPDF("http://[::1")},
			want: DiscoveryResult{Origin: "openalex", DOI: "10.1/x"}, wantOK: true,
		},
		{
			name: "an http pdf", work: openAlexWork{DOI: "10.1/x", BestOALocation: oaPDF(" http://h/x.pdf ")},
			want: DiscoveryResult{Origin: "openalex", DOI: "10.1/x", PDFURL: "http://h/x.pdf"}, wantOK: true,
		},
		{
			name: "a primary location with no source", work: openAlexWork{DOI: "10.1/x", PrimaryLocation: &openAlexLocation{}},
			want: DiscoveryResult{Origin: "openalex", DOI: "10.1/x"}, wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := openAlexWorkToResult(&tc.work)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Errorf("result = %+v\nwant     %+v", got, tc.want)
			}
		})
	}
}

// TestOpenAlexAuthors checks names are joined, blanks skipped, and a list past
// maxHitAuthors cut with "et al.".
func TestOpenAlexAuthors(t *testing.T) {
	named := func(names ...string) []openAlexAuthorship {
		out := make([]openAlexAuthorship, len(names))
		for i, n := range names {
			out[i].Author.DisplayName = n
		}
		return out
	}
	many := make([]string, maxHitAuthors+3)
	for i := range many {
		many[i] = "A" + strconv.Itoa(i)
	}
	cases := []struct {
		name string
		in   []openAlexAuthorship
		want string
	}{
		{name: "none", in: nil, want: ""},
		{name: "two with a blank", in: named("Ada  Lovelace", " ", "Alan Turing"), want: "Ada Lovelace; Alan Turing"},
		{name: "exactly the cap", in: named(many[:maxHitAuthors]...), want: strings.Join(many[:maxHitAuthors], "; ")},
		{name: "past the cap", in: named(many...), want: strings.Join(many[:maxHitAuthors], "; ") + "; et al."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := openAlexAuthors(tc.in); got != tc.want {
				t.Errorf("openAlexAuthors() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClampOpenAlexLimit covers the default, the clamp, and a value in range.
func TestClampOpenAlexLimit(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{name: "zero", in: 0, want: openAlexDefaultLimit},
		{name: "negative", in: -3, want: openAlexDefaultLimit},
		{name: "in range", in: 7, want: 7},
		{name: "too many", in: 1000, want: openAlexMaxLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampOpenAlexLimit(tc.in); got != tc.want {
				t.Errorf("clampOpenAlexLimit(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestNewOpenAlex_UsesTheSharedBudget checks a provider built for production spends
// from the process-wide budget the download source reports to, and is named after
// the origin it stamps.
func TestNewOpenAlex_UsesTheSharedBudget(t *testing.T) {
	p := NewOpenAlex("  ")
	if p.budget != openalex.Shared() {
		t.Error("NewOpenAlex did not use openalex.Shared")
	}
	if p.key != "" {
		t.Errorf("key = %q, want a blank key trimmed to keyless", p.key)
	}
	if p.Name() != "openalex" {
		t.Errorf("Name() = %q, want openalex", p.Name())
	}
}
