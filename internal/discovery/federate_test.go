package discovery

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// stubProvider is a canned Provider for the federation tests: it returns its
// preset results (and optional error) without any network access, and records the
// number of times it was searched so concurrency can be asserted.
type stubProvider struct {
	name    string
	results []DiscoveryResult
	err     error

	mu    sync.Mutex
	calls int
}

// Name reports the stub's origin label.
func (p *stubProvider) Name() string { return p.name }

// Search returns the stub's canned results and error, counting the call.
func (p *stubProvider) Search(_ context.Context, _ string, _ int) ([]DiscoveryResult, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return p.results, p.err
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

// TestDefaultProviders verifies DefaultProviders returns the three standard
// keyless providers in the documented order: arxiv, crossref, openlibrary.
func TestDefaultProviders(t *testing.T) {
	providers := DefaultProviders("")
	if len(providers) != 3 {
		t.Fatalf("DefaultProviders() returned %d providers, want 3", len(providers))
	}
	want := []string{"arxiv", "crossref", "openlibrary"}
	for i, name := range want {
		if got := providers[i].Name(); got != name {
			t.Errorf("provider[%d].Name() = %q, want %q", i, got, name)
		}
	}
}
