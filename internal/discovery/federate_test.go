package discovery

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// stubProvider is a canned Provider for the federation tests: it returns its
// preset results (and optional error) without any network access, and records the
// number of times it was searched so concurrency can be asserted.
type stubProvider struct {
	name    string
	results []DiscoveryResult
	err     error
	// block, when non-nil, holds Search until it is closed or the context ends, so a
	// slow provider can be simulated without a sleep.
	block chan struct{}

	mu    sync.Mutex
	calls int
	// gotQuery, gotLimit and gotCtxErr record what the last call was handed, so a
	// test can assert Federate forwarded them rather than substituting its own.
	gotQuery  string
	gotLimit  int
	gotCtxErr error
}

// Name reports the stub's origin label.
func (p *stubProvider) Name() string { return p.name }

// Search returns the stub's canned results and error, recording the call along with
// the arguments it was given so a test can assert what Federate forwarded.
func (p *stubProvider) Search(ctx context.Context, query string, limit int) ([]DiscoveryResult, error) {
	p.mu.Lock()
	p.calls++
	p.gotQuery = query
	p.gotLimit = limit
	p.gotCtxErr = ctx.Err()
	p.mu.Unlock()
	if p.block != nil {
		select {
		case <-p.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return p.results, p.err
}

// searched reports how many times Search was called.
func (p *stubProvider) searched() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// TestFederateForwardsTheQueryAndLimitToEveryProvider verifies the arguments reach
// the providers unchanged.
//
// Every other test in this file uses providers that ignore their arguments, so
// Federate could pass "" and 0 — or drop the context — and the whole file would
// still pass while federated search silently returned each provider's default page
// of results for an empty query.
func TestFederateForwardsTheQueryAndLimitToEveryProvider(t *testing.T) {
	a := &stubProvider{name: "a"}
	b := &stubProvider{name: "b"}

	Federate(context.Background(), "quantum error correction", 7, a, b)

	for _, p := range []*stubProvider{a, b} {
		if p.searched() != 1 {
			t.Errorf("provider %q searched %d times, want 1", p.name, p.searched())
		}
		if p.gotQuery != "quantum error correction" {
			t.Errorf("provider %q got query %q, want the caller's query", p.name, p.gotQuery)
		}
		if p.gotLimit != 7 {
			t.Errorf("provider %q got limit %d, want 7", p.name, p.gotLimit)
		}
		if p.gotCtxErr != nil {
			t.Errorf("provider %q got an already-failed context: %v", p.name, p.gotCtxErr)
		}
	}
}

// TestFederateIsNotSunkByAProviderThatOutlivesTheDeadline verifies the isolation
// Federate actually offers: it has no timeout of its own and blocks on wg.Wait, so
// a provider that hangs is bounded only by the context it was handed. With a
// deadline in play the hanging provider must abandon its work and the healthy
// provider's results must still come back.
//
// This is the scenario best-effort exists for and nothing covered it: every existing
// test uses providers that return instantly, so a Federate that ran its providers
// sequentially, or one whose slow provider held the merge, would pass them all.
func TestFederateIsNotSunkByAProviderThatOutlivesTheDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// block is never closed: the provider can only leave through ctx.Done().
	hanging := &stubProvider{name: "slow", block: make(chan struct{})}
	healthy := &stubProvider{name: "crossref", results: []DiscoveryResult{
		{Origin: "crossref", Title: "Delivered anyway", DOI: "10.1/a"},
	}}

	started := time.Now()
	got := Federate(ctx, "q", 10, hanging, healthy)
	elapsed := time.Since(started)

	if len(got) != 1 {
		t.Fatalf("Federate() returned %d results, want 1 (the healthy provider's): %+v", len(got), got)
	}
	if got[0].Origin != "crossref" {
		t.Errorf("kept result = %+v, want the healthy crossref result", got[0])
	}
	if limit := 5 * time.Second; elapsed >= limit {
		t.Errorf("Federate took %v, want well under %v: the hanging provider held the merge", elapsed, limit)
	}
}

// TestFederateRunsProvidersConcurrently verifies the providers really do overlap.
//
// TestFederate_Concurrent counts results, which a sequential implementation
// satisfies just as well. Here each provider blocks until every provider has been
// entered, so the call can only return if they are in flight at the same time; a
// sequential Federate deadlocks and the test times out rather than passing.
func TestFederateRunsProvidersConcurrently(t *testing.T) {
	const n = 4
	var (
		mu      sync.Mutex
		entered int
		allIn   = make(chan struct{})
	)
	providers := make([]Provider, n)
	for i := range providers {
		providers[i] = providerFunc(func(ctx context.Context, _ string, _ int) ([]DiscoveryResult, error) {
			mu.Lock()
			entered++
			if entered == n {
				close(allIn)
			}
			mu.Unlock()
			select {
			case <-allIn:
				return []DiscoveryResult{{Origin: "p", DOI: "10.1/shared"}}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got := Federate(ctx, "q", 10, providers...)

	if len(got) != 1 {
		t.Fatalf("Federate() returned %d results, want 1 after dedup", len(got))
	}
	if entered != n {
		t.Errorf("%d of %d providers ran", entered, n)
	}
}

// providerFunc adapts a plain function to the Provider interface so a test can
// supply behavior inline.
type providerFunc func(context.Context, string, int) ([]DiscoveryResult, error)

// Name reports a fixed label; the concurrency test does not distinguish providers.
func (f providerFunc) Name() string { return "func" }

// Search calls the underlying function.
func (f providerFunc) Search(ctx context.Context, q string, limit int) ([]DiscoveryResult, error) {
	return f(ctx, q, limit)
}

// TestEveryProviderPropagatesContextCancellation verifies the one error a Provider
// is allowed to return actually comes back as itself, for every provider at once.
//
// The contract is that Search degrades every failure to an empty slice and returns
// ONLY context errors. Each provider had a cancellation test, but every one of them
// asserted `err != nil` alone — so a provider returning fmt.Errorf("bad url") in
// place of ctx.Err() passed. That distinction is load-bearing in both directions:
// Federate discards results either way, so an unclassified error is invisible, and a
// caller that wanted to know the user had walked away cannot tell.
//
// Running the whole registry in one table also means a provider added later is
// covered without anyone remembering to write this test again. No request leaves the
// machine: the context is already canceled, so the transport refuses before dialing.
func TestEveryProviderPropagatesContextCancellation(t *testing.T) {
	providers := ExtraProviders("test@example.com", staticMirrors{"https://annas-archive.invalid"})
	if len(providers) < 8 {
		t.Fatalf("ExtraProviders returned %d providers; the registry shrank and this test may be skipping one", len(providers))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, p := range providers {
		t.Run(p.Name(), func(t *testing.T) {
			got, err := p.Search(ctx, "anything", 5)
			if err == nil {
				t.Fatalf("Search() on a canceled context returned no error (%d results)", len(got))
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Search() error = %v, want it to wrap context.Canceled", err)
			}
			if len(got) != 0 {
				t.Errorf("Search() returned %d results alongside a context error, want none", len(got))
			}
		})
	}
}

// hostileResponses are the answers a provider must survive without breaking the
// federated search. Each is something a real deployment produces: a captive portal
// or CDN interstitial in place of the API payload, a body cut off mid-document by a
// dropped connection, an upstream that is simply down, and a 200 carrying nothing.
var hostileResponses = []struct {
	name   string
	status int
	body   string
}{
	{name: "empty body under a 200", status: http.StatusOK, body: ""},
	{name: "html interstitial instead of the payload", status: http.StatusOK, body: "<html><body>Attention Required | Cloudflare</body></html>"},
	{name: "body truncated mid-document", status: http.StatusOK, body: `{"message":{"items":[{"title":["Half a res`},
	{name: "xml truncated mid-document", status: http.StatusOK, body: `<?xml version="1.0"?><feed><entry><title>Half`},
	{name: "nul bytes and binary noise", status: http.StatusOK, body: "\x00\x01\x02\xff\xfe binary"},
	{name: "upstream is down", status: http.StatusServiceUnavailable, body: "service unavailable"},
	{name: "upstream is rate limiting", status: http.StatusTooManyRequests, body: "slow down"},
	{name: "upstream reports not found", status: http.StatusNotFound, body: "no such endpoint"},
}

// TestEveryProviderDegradesToEmptyOnAHostileResponse verifies the other half of the
// Provider contract, for every provider at once: everything that is not a context
// error must become an empty slice and a nil error.
//
// This is the promise Federate is built on — it discards a provider's results the
// moment Search returns non-nil, so a provider that surfaces a parse failure removes
// itself from the federated result silently. Four of the eight providers asserted
// this only against their private parse helper (parseArxivFeed(garbage) == nil),
// which says nothing about what Search does with that nil, and no provider was tested
// against an empty body, an HTML interstitial, or a body cut off mid-document.
//
// Every provider is driven through one table so a provider added later inherits the
// check instead of relying on someone remembering to write it.
func TestEveryProviderDegradesToEmptyOnAHostileResponse(t *testing.T) {
	providers := []struct {
		name    string
		setBase func(*testing.T, string)
		build   func(base string) Provider
	}{
		{name: "arxiv", setBase: setArxivBase, build: func(string) Provider { return NewArxiv() }},
		{name: "crossref", setBase: setCrossrefBase, build: func(string) Provider { return NewCrossref("") }},
		{name: "openlibrary", setBase: setOpenLibraryBase, build: func(string) Provider { return NewOpenLibrary("") }},
		{name: "gutenberg", setBase: withGutendexBase, build: func(string) Provider { return NewGutenberg() }},
		{name: "dblp", setBase: setDblpBase, build: func(string) Provider { return NewDBLP() }},
		{name: "pubmed", setBase: setPubMedBase, build: func(string) Provider { return NewPubMed("") }},
		{name: "eric", setBase: setERICBase, build: func(string) Provider { return NewERIC() }},
		{
			// Anna's takes its base from an injected mirror list rather than a package
			// variable, so it opts out of setBase and is wired through build instead.
			name:    "annas",
			setBase: func(*testing.T, string) {},
			build:   func(base string) Provider { return NewAnnas(staticMirrors{base}) },
		},
	}

	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			for _, hr := range hostileResponses {
				t.Run(hr.name, func(t *testing.T) {
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.WriteHeader(hr.status)
						_, _ = io.WriteString(w, hr.body)
					}))
					defer srv.Close()
					p.setBase(t, srv.URL)

					got, err := p.build(srv.URL).Search(context.Background(), "anything", 5)
					if err != nil {
						t.Errorf("Search() error = %v, want nil: only context errors may surface", err)
					}
					if len(got) != 0 {
						t.Errorf("Search() returned %d results from a hostile response: %+v", len(got), got)
					}
				})
			}
		})
	}
}

// TestEveryProviderDegradesToEmptyOnATransportFailure verifies the same contract for
// a dead socket rather than a bad answer. It is separated from the table above
// because it needs a server that is closed rather than one that responds, and
// because Anna's — whose mirror loop is the only per-provider failover in the
// package — had no transport-failure test at all.
func TestEveryProviderDegradesToEmptyOnATransportFailure(t *testing.T) {
	// A server that is started and immediately closed leaves a port nothing listens
	// on, so every request fails at the dial rather than at the response.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := dead.URL
	dead.Close()

	providers := []struct {
		name    string
		setBase func(*testing.T, string)
		build   func(base string) Provider
	}{
		{name: "arxiv", setBase: setArxivBase, build: func(string) Provider { return NewArxiv() }},
		{name: "crossref", setBase: setCrossrefBase, build: func(string) Provider { return NewCrossref("") }},
		{name: "openlibrary", setBase: setOpenLibraryBase, build: func(string) Provider { return NewOpenLibrary("") }},
		{name: "gutenberg", setBase: withGutendexBase, build: func(string) Provider { return NewGutenberg() }},
		{name: "dblp", setBase: setDblpBase, build: func(string) Provider { return NewDBLP() }},
		{name: "pubmed", setBase: setPubMedBase, build: func(string) Provider { return NewPubMed("") }},
		{name: "eric", setBase: setERICBase, build: func(string) Provider { return NewERIC() }},
		{name: "annas", setBase: func(*testing.T, string) {}, build: func(b string) Provider { return NewAnnas(staticMirrors{b}) }},
	}

	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			p.setBase(t, base)

			got, err := p.build(base).Search(context.Background(), "anything", 5)
			if err != nil {
				t.Errorf("Search() error = %v, want nil: a dead upstream must not sink the federated search", err)
			}
			if len(got) != 0 {
				t.Errorf("Search() returned %d results from a dead upstream", len(got))
			}
		})
	}
}

// TestAnnasProviderWithNoMirrorsDegradesToEmpty verifies the one provider that can be
// asked with nothing to ask: an empty mirror list must produce an empty result rather
// than an error, since discovery failing is not the caller's problem.
func TestAnnasProviderWithNoMirrorsDegradesToEmpty(t *testing.T) {
	got, err := NewAnnas(staticMirrors{}).Search(context.Background(), "anything", 5)
	if err != nil {
		t.Errorf("Search() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("Search() returned %d results with no mirrors configured", len(got))
	}
}

// TestEveryProviderNamesItself verifies each provider reports a non-empty Name.
// The name is what tags a DiscoveryResult's Origin, so an empty one silently strips
// the provenance the search tool shows for every hit that provider contributes.
func TestEveryProviderNamesItself(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range ExtraProviders("", staticMirrors{"https://annas-archive.invalid"}) {
		name := p.Name()
		if name == "" {
			t.Errorf("a provider (%T) reports an empty Name", p)
			continue
		}
		if seen[name] {
			t.Errorf("two providers both call themselves %q; their results are indistinguishable", name)
		}
		seen[name] = true
	}
}

// TestFederate_MergesAndDedupsByDOI verifies that when two providers each return a
// result carrying the same DOI, Federate keeps only the first (provider-order)
// occurrence and preserves the other, distinct result.
func TestFederate_MergesAndDedupsByDOI(t *testing.T) {
	first := &stubProvider{name: "arxiv", results: []DiscoveryResult{
		{Origin: "arxiv", Title: "Shared Work", DOI: "10.1000/XYZ"},
	}}
	second := &stubProvider{name: "crossref", results: []DiscoveryResult{
		{Origin: "crossref", Title: "Shared Work (dup)", DOI: "10.1000/xyz "},
		{Origin: "crossref", Title: "Distinct Work", DOI: "10.2000/abc"},
	}}

	got := Federate(context.Background(), "q", 10, first, second)
	if len(got) != 2 {
		t.Fatalf("Federate() returned %d results, want 2: %+v", len(got), got)
	}
	if got[0].Origin != "arxiv" || got[0].DOI != "10.1000/XYZ" {
		t.Errorf("first result = %+v, want the arxiv DOI kept (provider order)", got[0])
	}
	if got[1].DOI != "10.2000/abc" {
		t.Errorf("second result = %+v, want the distinct crossref DOI", got[1])
	}
}

// TestFederate_DedupsByTitleYear verifies that two DOI-less results with the same
// normalized title+year (differing only in case, whitespace and punctuation)
// collapse to a single kept result.
func TestFederate_DedupsByTitleYear(t *testing.T) {
	first := &stubProvider{name: "arxiv", results: []DiscoveryResult{
		{Origin: "arxiv", Title: "Neural   Networks!", Year: "2021"},
	}}
	second := &stubProvider{name: "openlibrary", results: []DiscoveryResult{
		{Origin: "openlibrary", Title: "neural networks", Year: "2021"},
	}}

	got := Federate(context.Background(), "q", 10, first, second)
	if len(got) != 1 {
		t.Fatalf("Federate() returned %d results, want 1: %+v", len(got), got)
	}
	if got[0].Origin != "arxiv" {
		t.Errorf("kept result = %+v, want the first (arxiv) occurrence", got[0])
	}
}

// TestFederate_BestEffortProviderError verifies that a provider returning an error
// contributes nothing yet never sinks the others: the healthy provider's results
// are still returned.
func TestFederate_BestEffortProviderError(t *testing.T) {
	broken := &stubProvider{name: "arxiv", err: errors.New("boom")}
	healthy := &stubProvider{name: "crossref", results: []DiscoveryResult{
		{Origin: "crossref", Title: "Works", DOI: "10.1/a"},
	}}

	got := Federate(context.Background(), "q", 10, broken, healthy)
	if len(got) != 1 {
		t.Fatalf("Federate() returned %d results, want 1: %+v", len(got), got)
	}
	if got[0].Origin != "crossref" {
		t.Errorf("kept result = %+v, want the healthy crossref result", got[0])
	}
}

// TestFederate_Concurrent verifies that Federate runs many providers and returns
// every distinct result. Run under -race, it also proves the shared collection is
// free of data races.
func TestFederate_Concurrent(t *testing.T) {
	const n = 12
	providers := make([]Provider, n)
	for i := range providers {
		providers[i] = &stubProvider{
			name:    "p",
			results: []DiscoveryResult{{Origin: "p", DOI: string(rune('a'+i)) + "-doi"}},
		}
	}

	got := Federate(context.Background(), "q", 10, providers...)
	if len(got) != n {
		t.Fatalf("Federate() returned %d results, want %d", len(got), n)
	}
}

// TestNormalizeHelpers documents the shared normalizers used for cross-source and
// libgen dedup: NormalizeDOI lowercases and trims, and TitleYearKey lowercases,
// strips punctuation, collapses whitespace and appends the year.
func TestNormalizeHelpers(t *testing.T) {
	if got := NormalizeDOI("  10.1000/XYZ "); got != "10.1000/xyz" {
		t.Errorf("NormalizeDOI = %q, want %q", got, "10.1000/xyz")
	}
	if NormalizeDOI("   ") != "" {
		t.Errorf("NormalizeDOI of blank should be empty")
	}
	a := TitleYearKey("Neural   Networks!", "2021")
	b := TitleYearKey("neural networks", "2021")
	if a != b {
		t.Errorf("TitleYearKey mismatch: %q vs %q", a, b)
	}
	if TitleYearKey("", "2021") != "" {
		t.Errorf("TitleYearKey with empty title should be empty")
	}
}

// panicProvider is a Provider whose Search panics, used to prove Federate
// isolates a misbehaving provider instead of crashing the process.
type panicProvider struct{}

// Name reports the panicking stub's origin label.
func (panicProvider) Name() string { return "panic" }

// Search panics to simulate a provider that violates the best-effort contract.
func (panicProvider) Search(_ context.Context, _ string, _ int) ([]DiscoveryResult, error) {
	panic("provider blew up")
}

// TestFederate_RecoversProviderPanic verifies a provider that panics in its own
// goroutine is contained: it contributes nothing and the healthy provider's
// results are still returned.
func TestFederate_RecoversProviderPanic(t *testing.T) {
	good := &stubProvider{name: "arxiv", results: []DiscoveryResult{{Origin: "arxiv", Title: "Survivor", DOI: "10.1/ok"}}}
	got := Federate(context.Background(), "q", 10, panicProvider{}, good)
	if len(got) != 1 || got[0].Title != "Survivor" {
		t.Fatalf("expected the healthy provider's single result to survive the panic, got %+v", got)
	}
}

// TestDefaultProviders verifies DefaultProviders returns the four standard keyless
// providers in the documented order: arxiv, crossref, openlibrary, gutenberg.
func TestDefaultProviders(t *testing.T) {
	providers := DefaultProviders("")
	if len(providers) != 4 {
		t.Fatalf("DefaultProviders() returned %d providers, want 4", len(providers))
	}
	want := []string{"arxiv", "crossref", "openlibrary", "gutenberg"}
	for i, name := range want {
		if got := providers[i].Name(); got != name {
			t.Errorf("provider[%d].Name() = %q, want %q", i, got, name)
		}
	}
}

// TestDedupKeysOnMD5 verifies two results sharing an md5 collapse to one, even when
// their titles differ — the md5 is the stronger identity for file-keyed results.
func TestDedupKeysOnMD5(t *testing.T) {
	const md5 = "d64efd386ed7227592499460aca2044b"
	merged := dedupResults([][]DiscoveryResult{
		{{Origin: "annas", MD5: md5, Title: "Data Science Essentials"}},
		{{Origin: "annas", MD5: md5, Title: "Data Science Essentials (2nd printing)"}},
	})
	if len(merged) != 1 {
		t.Fatalf("got %d results, want 1 after md5 dedup", len(merged))
	}
}

// TestDedupKeepsDistinctMD5s verifies distinct md5s survive.
func TestDedupKeepsDistinctMD5s(t *testing.T) {
	merged := dedupResults([][]DiscoveryResult{
		{{Origin: "annas", MD5: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Title: "A"}},
		{{Origin: "annas", MD5: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Title: "B"}},
	})
	if len(merged) != 2 {
		t.Fatalf("got %d results, want both kept", len(merged))
	}
}

// TestExtraProvidersIncludesAnnasAndOA verifies the extra set is the open-access
// providers, the bibliographic indexes, and Anna's, in that order — the indexes must
// follow the open-access providers so dedup keeps the fetchable copy of a shared DOI.
func TestExtraProvidersIncludesAnnasAndOA(t *testing.T) {
	got := ExtraProviders("", staticMirrors{"https://annas-archive.gl"})
	want := []string{"arxiv", "crossref", "openlibrary", "gutenberg", "dblp", "pubmed", "eric", "annas"}
	if len(got) != len(want) {
		t.Fatalf("ExtraProviders() returned %d providers, want %d", len(got), len(want))
	}
	for i, name := range want {
		if gotName := got[i].Name(); gotName != name {
			t.Errorf("provider[%d].Name() = %q, want %q", i, gotName, name)
		}
	}
}
