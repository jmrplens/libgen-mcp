package libgen

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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
	// listRemaining and listRetryAfter, when set, are the rate-limit headers
	// a list query answers with.
	listRemaining, listRetryAfter string
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
		if filter != "" && s.listRemaining != "" {
			w.Header().Set("X-RateLimit-Remaining", s.listRemaining)
		}
		if filter != "" && s.listRetryAfter != "" {
			w.Header().Set("Retry-After", s.listRetryAfter)
		}
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
	short := `{"id":"https://openalex.org/W1","referenced_works_count":40,"referenced_works":["https://openalex.org/W2"]}`
	tests := []struct {
		name, doi, kind, work, key string
		listStatus                 int
		listRemaining, retryAfter  string
		spent, paused, empty       bool
		want, notWant              string
	}{
		{name: "unknown doi", doi: "10.9999/x", kind: RelatedReferences, want: "no work with this DOI"},
		{name: "no references", doi: "10.1/x", kind: RelatedReferences, work: `{"id":"https://openalex.org/W1"}`, want: "lists no references"},
		{name: "counted but not named", doi: "10.1/x", kind: RelatedReferences, work: `{"id":"https://openalex.org/W1","referenced_works_count":7}`, want: "counts 7 references for this work but names none"},
		{name: "entity with no id", doi: "10.1/x", kind: RelatedCitedBy, work: `{}`, want: "no work with this DOI"},
		{name: "a burst refusal with credits left", doi: "10.1/x", kind: RelatedCitedBy, listStatus: http.StatusTooManyRequests, want: "Try again shortly", notWant: "allowance"},
		{name: "a burst refusal with a short wait", doi: "10.1/x", kind: RelatedCitedBy, listStatus: http.StatusTooManyRequests, listRemaining: "0", retryAfter: "2", want: "Try again shortly"},
		{name: "a refusal for the day", doi: "10.1/x", kind: RelatedCitedBy, listStatus: http.StatusTooManyRequests, listRemaining: "0", retryAfter: "40000", want: "LIBGEN_MCP_OPENALEX_KEY raises it"},
		{name: "a refusal for the day with a key set", doi: "10.1/x", kind: RelatedCitedBy, key: "k", listStatus: http.StatusTooManyRequests, listRemaining: "0", want: "midnight UTC", notWant: "LIBGEN_MCP_OPENALEX_KEY"},
		{name: "list failing", doi: "10.1/x", kind: RelatedCitedBy, listStatus: http.StatusBadGateway, want: "did not answer"},
		{name: "a rejected key", doi: "10.1/x", kind: RelatedCitedBy, key: "bad", listStatus: http.StatusUnauthorized, want: "refused the API key", notWant: "bad"},
		{name: "a 403 with no key", doi: "10.1/x", kind: RelatedCitedBy, listStatus: http.StatusForbidden, want: "did not answer"},
		{name: "budget spent", doi: "10.1/x", kind: RelatedCitedBy, spent: true, want: "allowance"},
		{name: "budget paused by a refusal", doi: "10.1/x", kind: RelatedCitedBy, paused: true, want: "Try again shortly"},
		{name: "long reference list", doi: "10.1/x", kind: RelatedReferences, work: long, want: "first 100 of the 105 it names"},
		{name: "count and names disagree", doi: "10.1/x", kind: RelatedReferences, work: short, want: "counts 40 references but names 1"},
		{name: "a work nobody cites", doi: "10.1/x", kind: RelatedCitedBy, work: `{"id":"https://openalex.org/W1"}`, empty: true, want: "cites this one"},
		{name: "references that resolve to nothing", doi: "10.1/x", kind: RelatedReferences, empty: true, want: "returned none"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &openAlexStub{
				work: tc.work, listStatus: tc.listStatus, emptyList: tc.empty,
				listRemaining: tc.listRemaining, listRetryAfter: tc.retryAfter,
			}
			c, budget := relatedClient(t, stub, tc.key)
			if tc.spent {
				budget.Observe(http.StatusOK, http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"3600"}}, time.Now())
			}
			if tc.paused {
				budget.Observe(http.StatusTooManyRequests, http.Header{"Retry-After": {"30"}}, time.Now())
			}
			got := c.Related(context.Background(), tc.doi, tc.kind, 3)
			if got.Kind != tc.kind || !strings.Contains(got.Note, tc.want) {
				t.Errorf("got %+v, want a note containing %q", got, tc.want)
			}
			if tc.notWant != "" && strings.Contains(got.Note, tc.notWant) {
				t.Errorf("note %q should not contain %q", got.Note, tc.notWant)
			}
		})
	}
}

// TestRelated_RefundsAnUnsentQuery gives the credit back when the list query
// never reached OpenAlex, and keeps it spent when OpenAlex answered.
func TestRelated_RefundsAnUnsentQuery(t *testing.T) {
	var lists atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "10")
		w.Header().Set("X-RateLimit-Reset", "3600")
		if strings.HasPrefix(r.URL.Path, "/works/doi:") {
			_, _ = w.Write([]byte(`{"id":"https://openalex.org/W1"}`))
			return
		}
		lists.Add(1)
		// Hijack and close: the request was sent but nothing came back,
		// which is the same to the client as a request never answered.
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(srv.Close)
	budget := &openalex.Budget{}
	c := newVerifyClient(t, srv.URL)
	WithOpenAlexBase(srv.URL, budget)(c)
	got := c.Related(context.Background(), "10.1/x", RelatedCitedBy, 3)
	if !strings.Contains(got.Note, "did not answer") {
		t.Errorf("note = %q", got.Note)
	}
	if left, _, ok := budget.Remaining(time.Now()); !ok || left != 10 {
		t.Errorf("budget = %d, %v, want the credit refunded to 10", left, ok)
	}
	if lists.Load() == 0 {
		t.Error("the list query never reached the server")
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
