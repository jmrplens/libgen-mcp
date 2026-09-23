package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

// TestAnnasProviderStopsAtAChallengeAndTriesOnWithoutOne verifies the two
// refusals are handled oppositely, which is the whole point of telling them
// apart.
//
// A challenged mirror ends the call: every Anna's mirror is the same deployment
// behind the same provider, so the siblings would answer the same way and the
// only thing asking them adds is refused requests against a host that has just
// said no. An ordinary refusal is what the mirror list exists for, and must
// still fall through to the next one.
func TestAnnasProviderStopsAtAChallengeAndTriesOnWithoutOne(t *testing.T) {
	fixture := annasFixture(t)
	const interstitial = `<html><head><title>DDoS-Guard</title>` +
		`<script src="/.well-known/ddos-guard/js-challenge/index.js"></script></head></html>`

	serve := func(hits *atomic.Int32, status int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
	}

	t.Run("a challenge ends the call", func(t *testing.T) {
		var challenged, sibling atomic.Int32
		first := serve(&challenged, http.StatusForbidden, interstitial)
		defer first.Close()
		second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			sibling.Add(1)
			_, _ = w.Write(fixture)
		}))
		defer second.Close()

		p := &AnnasProvider{mirrors: staticMirrors{first.URL, second.URL}, http: first.Client()}
		got, err := p.Search(context.Background(), "dune", 3)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("Search returned %d results, want none: the provider gave up", len(got))
		}
		if n := sibling.Load(); n != 0 {
			t.Errorf("the sibling mirror was asked %d time(s); a challenge must not be retried across mirrors", n)
		}
	})

	t.Run("an ordinary refusal falls through", func(t *testing.T) {
		var refused, sibling atomic.Int32
		first := serve(&refused, http.StatusForbidden, "forbidden")
		defer first.Close()
		second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			sibling.Add(1)
			_, _ = w.Write(fixture)
		}))
		defer second.Close()

		p := &AnnasProvider{mirrors: staticMirrors{first.URL, second.URL}, http: first.Client()}
		got, err := p.Search(context.Background(), "dune", 3)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(got) == 0 {
			t.Error("Search returned nothing; a plain 403 must fall through to the next mirror")
		}
		if n := sibling.Load(); n != 1 {
			t.Errorf("the sibling mirror was asked %d time(s), want exactly 1", n)
		}
	})
}

// TestAnnasProviderStaysQuietAfterAChallengeAndComesBack verifies the cooldown
// in both directions, which is the whole of its value: no request at all while
// it holds, and one request to find out the site is welcoming again once it
// lapses.
//
// The clock is moved rather than waited on. A test that slept for the real
// window would take fifteen minutes and would pin nothing a shorter constant
// does not.
func TestAnnasProviderStaysQuietAfterAChallengeAndComesBack(t *testing.T) {
	fixture := annasFixture(t)
	const interstitial = `<html><head><title>DDoS-Guard</title>` +
		`<script src="/.well-known/ddos-guard/js-challenge/index.js"></script></head></html>`

	var hits atomic.Int32
	var challenge atomic.Bool
	challenge.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if challenge.Load() {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(interstitial))
			return
		}
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	now := time.Now()
	p := &AnnasProvider{
		mirrors: staticMirrors{srv.URL},
		http:    srv.Client(),
		now:     func() time.Time { return now },
	}

	if _, err := p.Search(context.Background(), "dune", 3); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("the first search made %d request(s), want 1", n)
	}

	// Inside the window: the site is answering again, and must not be asked.
	challenge.Store(false)
	for i := range 3 {
		if _, err := p.Search(context.Background(), "dune", 3); err != nil {
			t.Fatalf("Search %d during the cooldown: %v", i, err)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("the cooldown made %d request(s) in total, want the original 1", n)
	}

	// Past the window: exactly one request, and the results come back.
	now = now.Add(challengeCooldown + time.Second)
	got, err := p.Search(context.Background(), "dune", 3)
	if err != nil {
		t.Fatalf("Search after the cooldown: %v", err)
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("after the cooldown the provider made %d request(s) in total, want 2", n)
	}
	if len(got) == 0 {
		t.Error("the provider did not come back: no results once the challenge lifted")
	}
}

// TestBeQuietOnlyEverMovesTheDeadlineLater pins the rule a plain Swap breaks: a
// goroutine that computed its deadline, was descheduled, and woke up after a
// later one had been stored must not shorten the window.
//
// Concurrent challenges are ordinary — Federate runs providers in their own
// goroutines and a server answers several searches at once — and a shortened
// window is the one failure the cooldown exists to prevent. The stale deadline
// is applied deliberately here rather than raced for, so the case fails every
// time against a Swap instead of once in a thousand runs.
func TestBeQuietOnlyEverMovesTheDeadlineLater(t *testing.T) {
	now := time.Now()
	p := &AnnasProvider{now: func() time.Time { return now }}

	if !p.beQuiet() {
		t.Fatal("the first challenge did not report itself as opening the window")
	}
	opened := p.quietUntil.Load()

	// A second challenge a minute later extends the window and says nothing,
	// because the window it belongs to has already been announced.
	now = now.Add(time.Minute)
	if p.beQuiet() {
		t.Error("a challenge inside an open window reported itself as opening one")
	}
	extended := p.quietUntil.Load()
	if extended <= opened {
		t.Errorf("the deadline did not move later: %d then %d", opened, extended)
	}

	// The descheduled goroutine: its clock still reads the original instant, so
	// the deadline it computes is earlier than the one already stored.
	stale := now.Add(-time.Minute)
	p.now = func() time.Time { return stale }
	if p.beQuiet() {
		t.Error("a stale challenge reported itself as opening a window")
	}
	if got := p.quietUntil.Load(); got != extended {
		t.Errorf("a stale deadline overwrote a later one: %d, want %d", got, extended)
	}

	// Past the window, a challenge opens a new one and says so again.
	p.now = func() time.Time { return now.Add(challengeCooldown + time.Minute) }
	if !p.beQuiet() {
		t.Error("a challenge after the window lapsed did not report a fresh one")
	}
}

// TestAnnasProviderBoundedClient verifies NewAnnas equips the provider with a
// bounded http.Client (non-nil, with a timeout) rather than leaving it to fall back
// on the timeout-less http.DefaultClient, so a stalled mirror can never hang a
// federated search indefinitely.
func TestAnnasProviderBoundedClient(t *testing.T) {
	p := NewAnnas(staticMirrors{})
	if p.http == nil {
		t.Fatal("NewAnnas left http nil, want a bounded client")
	}
	if p.http.Timeout <= 0 {
		t.Errorf("NewAnnas client timeout = %v, want a positive bound", p.http.Timeout)
	}
}

// TestAnnasProviderSearchHonorsContextDeadline verifies Search threads its context
// through the mirror request: a mirror that blocks until the caller's short deadline
// expires makes Search return the context error rather than hanging, the same
// bounded behavior arXiv/Crossref/OpenLibrary give. It also exercises the per-call
// timeout wiring, since the request is issued under the deadline-bearing context.
func TestAnnasProviderSearchHonorsContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // block until the client's context expires
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	got, err := NewAnnas(staticMirrors{srv.URL}).Search(ctx, "q", 3)
	if err == nil {
		t.Fatal("Search() error = nil, want a context deadline error")
	}
	if got != nil {
		t.Errorf("Search() = %v, want nil results on a deadline error", got)
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

// TestAnnasSearchCarriesFormatAndSize verifies a result says what it is. Anna's
// own cards read "English [en] · EPUB · 12.0MB · 2021"; keeping only the title
// leaves an escalated result incomparable with a catalog one on the two attributes
// people sort by — and it is why an evaluator scenario had to stop asking for a
// file's format and size, the question being unanswerable from a search alone.
func TestAnnasSearchCarriesFormatAndSize(t *testing.T) {
	got := parseAnnasSearch(mustReadFixture(t, "annas_search.html"), 20)
	if len(got) == 0 {
		t.Fatal("the fixture parsed to no results")
	}
	var withBoth int
	for _, r := range got {
		if r.Extension != "" && r.Size != "" {
			withBoth++
		}
	}
	if withBoth == 0 {
		t.Fatalf("no result carried a format and size; first = %+v", got[0])
	}
	// The cards are uniform enough that nearly all of them should parse; a handful
	// missing one is fine, a majority missing means the descriptor moved.
	if withBoth*2 < len(got) {
		t.Errorf("only %d of %d results carried both; the card layout may have changed", withBoth, len(got))
	}
	for _, r := range got {
		if r.Extension != "" && strings.ToLower(r.Extension) != r.Extension {
			t.Errorf("extension %q should be normalized to lowercase", r.Extension)
		}
	}
}
