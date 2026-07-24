package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// staticMirrors is a MirrorLister over a fixed list, pointing the provider at a
// local server instead of the live mirrors.
type staticMirrors []string

// Mirrors returns the fixed base URLs.
func (s staticMirrors) Mirrors(context.Context) []string { return s }

// annasFixture loads the captured Anna's search page.
func annasFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/annas_search.html")
	if err != nil {
		t.Fatalf("reading Anna's fixture: %v", err)
	}
	return b
}

// TestParseAnnasSearchExtractsResults verifies the captured page yields md5-keyed
// results carrying a title, and that the limit is honored.
func TestParseAnnasSearchExtractsResults(t *testing.T) {
	got := parseAnnasSearch(annasFixture(t), 5)
	if len(got) == 0 {
		t.Fatal("parseAnnasSearch returned nothing for a real result page")
	}
	if len(got) > 5 {
		t.Fatalf("returned %d results, want the limit of 5 honored", len(got))
	}
	for i, r := range got {
		if len(r.MD5) != 32 {
			t.Errorf("result[%d].MD5 = %q, want 32 hex chars", i, r.MD5)
		}
		if strings.TrimSpace(r.Title) == "" {
			t.Errorf("result[%d] has no title", i)
		}
		if r.Origin != "annas" {
			t.Errorf("result[%d].Origin = %q, want annas", i, r.Origin)
		}
		if r.OpenAccess {
			t.Errorf("result[%d].OpenAccess = true; Anna's is not an open-access provider", i)
		}
	}
}

// TestParseAnnasSearchLayoutChange verifies an unrecognized page yields no results
// rather than garbage, so a layout change degrades quietly.
func TestParseAnnasSearchLayoutChange(t *testing.T) {
	if got := parseAnnasSearch([]byte(`<html><body><p>nothing here</p></body></html>`), 10); len(got) != 0 {
		t.Fatalf("parseAnnasSearch returned %d results for an unrecognized page, want 0", len(got))
	}
}

// TestAnnasProviderSearchesFirstReachableMirror verifies the provider queries the
// mirror list in order and skips one that errors.
func TestAnnasProviderSearchesFirstReachableMirror(t *testing.T) {
	fixture := annasFixture(t)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()

	var gotQuery string
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		_, _ = w.Write(fixture)
	}))
	defer good.Close()

	p := &AnnasProvider{mirrors: staticMirrors{bad.URL, good.URL}, http: good.Client()}
	got, err := p.Search(context.Background(), "python programming", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotQuery != "python programming" {
		t.Errorf("query sent = %q, want the raw query", gotQuery)
	}
	if len(got) == 0 {
		t.Fatal("Search returned nothing from the reachable mirror")
	}
	if p.Name() != "annas" {
		t.Errorf("Name() = %q, want annas", p.Name())
	}
}

// TestAnnasProviderAllMirrorsDown verifies a total outage yields no results and no
// error, honoring the best-effort Provider contract.
func TestAnnasProviderAllMirrorsDown(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer down.Close()

	p := &AnnasProvider{mirrors: staticMirrors{down.URL}, http: down.Client()}
	got, err := p.Search(context.Background(), "x", 5)
	if err != nil {
		t.Fatalf("Search must not error on a provider outage, got: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d results from a dead mirror", len(got))
	}
}
