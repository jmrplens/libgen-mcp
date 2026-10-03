package libgen

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/v2/internal/openalex"
)

// openAlexStub stands in for OpenAlex with the answers recorded on 2026-10-03
// for 10.1038/nature12373, and records each request's path, query and
// Authorization header.
type openAlexStub struct {
	mu       sync.Mutex
	requests []*http.Request
	// work overrides the single-entity answer when set.
	work string
	// listStatus, when set, is the status every list query answers with.
	listStatus int
	// emptyList answers every list query with no results.
	emptyList bool
}

// handler serves the recorded fixtures.
func (s *openAlexStub) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	work, refs, citing := read("openalex_related_work.json"), read("openalex_related_references.json"), read("openalex_related_cited_by.json")
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Clone(r.Context()))
		s.mu.Unlock()
		w.Header().Set("X-RateLimit-Remaining", "500")
		w.Header().Set("X-RateLimit-Reset", "3600")
		filter := r.URL.Query().Get("filter")
		switch {
		case strings.HasPrefix(r.URL.Path, "/works/doi:10.9999"):
			http.NotFound(w, r)
		case strings.HasPrefix(r.URL.Path, "/works/doi:") && s.work != "":
			_, _ = w.Write([]byte(s.work))
		case strings.HasPrefix(r.URL.Path, "/works/doi:"):
			_, _ = w.Write(work)
		case s.listStatus != 0:
			w.WriteHeader(s.listStatus)
		case s.emptyList:
			_, _ = w.Write([]byte(`{"meta":{"count":0},"results":[]}`))
		case strings.HasPrefix(filter, "openalex:"):
			_, _ = w.Write(refs)
		case strings.HasPrefix(filter, "cites:"):
			_, _ = w.Write(citing)
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}
}

// relatedClient is a client aimed at stub, with a budget of its own and the
// given key.
func relatedClient(t *testing.T, stub *openAlexStub, key string) (*Client, *openalex.Budget) {
	t.Helper()
	srv := httptest.NewServer(stub.handler(t))
	t.Cleanup(srv.Close)
	budget := &openalex.Budget{}
	c := newVerifyClient(t, srv.URL)
	WithOpenAlexBase(srv.URL, budget)(c)
	c.openAlexKey = key
	return c, budget
}

// TestRelated_References lists a work's references from one batched filter,
// the most cited first, with the total from the work itself, and sends the key
// in the header only.
func TestRelated_References(t *testing.T) {
	stub := &openAlexStub{}
	c, budget := relatedClient(t, stub, "secret-key")
	got := c.Related(context.Background(), "10.1038/nature12373", RelatedReferences, 3)
	if got.Total != 36 || len(got.Works) != 3 || got.Note != "" {
		t.Fatalf("got %+v", got)
	}
	first := got.Works[0]
	if first.DOI != "10.1038/nmeth818" || first.Title != "Deep tissue two-photon microscopy" || first.Year != 2005 || first.CitedByCount == 0 {
		t.Errorf("first = %+v", first)
	}
	if !got.Works[1].OpenAccess {
		t.Errorf("second should be open access: %+v", got.Works[1])
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.requests) != 2 {
		t.Fatalf("made %d requests, want the entity and one list", len(stub.requests))
	}
	list := stub.requests[1].URL.Query()
	if ids := strings.Count(list.Get("filter"), "|") + 1; ids != 36 || list.Get("sort") != "cited_by_count:desc" || list.Get("per_page") != "3" {
		t.Errorf("list query %v (%d ids)", list, ids)
	}
	for _, r := range stub.requests {
		if r.Header.Get("Authorization") != "Bearer secret-key" || strings.Contains(r.URL.String(), "secret-key") {
			t.Errorf("key handling on %s: header %q", r.URL, r.Header.Get("Authorization"))
		}
	}
	if left, _, ok := budget.Remaining(time.Now()); !ok || left != 499 {
		t.Errorf("budget = %d, %v, want 500 observed less the one credit spent", left, ok)
	}
}

// TestRelated_CitedBy lists the citing works through a cites filter on the
// work's own id, with the total OpenAlex counts.
func TestRelated_CitedBy(t *testing.T) {
	stub := &openAlexStub{}
	c, _ := relatedClient(t, stub, "")
	got := c.Related(context.Background(), "10.1038/nature12373", RelatedCitedBy, 3)
	if got.Total != 1980 || len(got.Works) != 3 || got.Works[0].DOI != "10.1103/revmodphys.89.035002" {
		t.Fatalf("got %+v", got)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if f := stub.requests[1].URL.Query().Get("filter"); f != "cites:W2159974629" {
		t.Errorf("filter = %q", f)
	}
	if h := stub.requests[0].Header.Get("Authorization"); h != "" {
		t.Errorf("a keyless lookup sent %q", h)
	}
}

// TestRelated_Notes covers every way the list comes back empty or partial,
// each with a note and never an error.
func TestRelated_Notes(t *testing.T) {
	ids := make([]string, relatedMaxIDs+5)
	for i := range ids {
		ids[i] = fmt.Sprintf(`"https://openalex.org/W%d"`, i+1)
	}
	long := `{"id":"https://openalex.org/W1","referenced_works_count":105,"referenced_works":[` + strings.Join(ids, ",") + `]}`
	tests := []struct {
		name, doi, kind, work string
		listStatus            int
		spent, empty          bool
		want                  string
	}{
		{name: "unknown doi", doi: "10.9999/x", kind: RelatedReferences, want: "no work with this DOI"},
		{name: "no references", doi: "10.1/x", kind: RelatedReferences, work: `{"id":"https://openalex.org/W1"}`, want: "no references"},
		{name: "entity with no id", doi: "10.1/x", kind: RelatedCitedBy, work: `{}`, want: "no work with this DOI"},
		{name: "list refused", doi: "10.1/x", kind: RelatedCitedBy, listStatus: http.StatusTooManyRequests, want: "allowance"},
		{name: "list failing", doi: "10.1/x", kind: RelatedCitedBy, listStatus: http.StatusBadGateway, want: "did not answer"},
		{name: "budget spent", doi: "10.1/x", kind: RelatedCitedBy, spent: true, want: "allowance"},
		{name: "long reference list", doi: "10.1/x", kind: RelatedReferences, work: long, want: "first 100 of its 105"},
		{name: "a work nobody cites", doi: "10.1/x", kind: RelatedCitedBy, work: `{"id":"https://openalex.org/W1"}`, empty: true, want: "cites this one"},
		{name: "references that resolve to nothing", doi: "10.1/x", kind: RelatedReferences, empty: true, want: "returned none"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &openAlexStub{work: tc.work, listStatus: tc.listStatus, emptyList: tc.empty}
			c, budget := relatedClient(t, stub, "")
			if tc.spent {
				budget.Observe(http.StatusOK, http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"3600"}}, time.Now())
			}
			got := c.Related(context.Background(), tc.doi, tc.kind, 3)
			if got.Kind != tc.kind || !strings.Contains(got.Note, tc.want) {
				t.Errorf("got %+v, want a note containing %q", got, tc.want)
			}
		})
	}
}

// TestRelated_Unreachable names nothing but the service when OpenAlex cannot
// be reached at all.
func TestRelated_Unreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()
	c := newVerifyClient(t, base)
	WithOpenAlexBase(base, &openalex.Budget{})(c)
	got := c.Related(context.Background(), "10.1/x", RelatedReferences, 3)
	if got.Note != "OpenAlex did not answer, so the list is not available now." {
		t.Errorf("note = %q", got.Note)
	}
}

// TestOpenAlexDefaults covers the production defaults the test seam replaces.
func TestOpenAlexDefaults(t *testing.T) {
	c := newVerifyClient(t, "http://unused")
	if c.openAlexURL() != openalex.APIBase || c.openAlexBudgetOrShared() != openalex.Shared() {
		t.Error("the defaults are not the public API and the shared budget")
	}
	if shortID("https://openalex.org/W42") != "W42" || shortID("W7") != "W7" {
		t.Error("shortID")
	}
}
