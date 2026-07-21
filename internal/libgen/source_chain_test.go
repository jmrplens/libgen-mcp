package libgen

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

// TestResolveLink verifies the resolve-only path returns the first resolving
// source's URL, headers and flags without downloading, fails over past a source
// that errors, and errors when nothing supports the item.
func TestResolveLink(t *testing.T) {
	cfg := baseChainConfig()
	good := stubSource{
		name: "libgen", supports: true,
		resolved: Resolved{FileURL: "https://cdn/x.pdf", VerifyMD5: true, Ext: "pdf", Header: http.Header{"Referer": {"https://h/"}}},
	}

	c := New(staticMirrors{}, cfg, WithSources(good))
	r, err := c.ResolveLink(context.Background(), Item{MD5: "abc"})
	if err != nil {
		t.Fatalf("ResolveLink: %v", err)
	}
	if r.URL != "https://cdn/x.pdf" || r.Source != "libgen" || !r.VerifyMD5 || r.Ext != "pdf" {
		t.Errorf("resolved = %+v", r)
	}
	if r.Header.Get("Referer") != "https://h/" {
		t.Error("required header not carried through")
	}

	bad := stubSource{name: "bad", supports: true, resolveErr: errors.New("boom")}
	c2 := New(staticMirrors{}, cfg, WithSources(bad, good))
	if r2, err2 := c2.ResolveLink(context.Background(), Item{MD5: "abc"}); err2 != nil || r2.Source != "libgen" {
		t.Errorf("failover: got %+v err=%v", r2, err2)
	}

	c3 := New(staticMirrors{}, cfg, WithSources(stubSource{name: "x", supports: false}))
	if _, err3 := c3.ResolveLink(context.Background(), Item{MD5: "abc"}); err3 == nil {
		t.Error("want error when no source supports the item")
	}

	// A named source that is not in the chain surfaces the selectSources error
	// straight out of ResolveLink (before any resolution is attempted).
	if _, err4 := c.ResolveLink(context.Background(), Item{MD5: "abc", Source: "nope"}); err4 == nil {
		t.Error("want error when the named source is not enabled")
	}

	// When every supporting source errors, the per-source errors are joined and
	// returned.
	c5 := New(staticMirrors{}, cfg, WithSources(bad))
	if _, err5 := c5.ResolveLink(context.Background(), Item{MD5: "abc"}); err5 == nil {
		t.Error("want joined error when all sources fail to resolve")
	}

	// A canceled context stops the failover loop after the first erroring source
	// rather than trying the rest.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	c6 := New(staticMirrors{}, cfg, WithSources(bad, good))
	r6, err6 := c6.ResolveLink(canceled, Item{MD5: "abc"})
	if err6 == nil {
		t.Error("want error when the context is canceled mid-chain")
	}
	if r6.Source == "libgen" {
		t.Error("a canceled context should stop before the second source resolves")
	}
}

// TestSelectSourcesUnpaywallHint verifies that asking for the unpaywall source when
// it is not enabled yields the actionable error naming its email gate.
func TestSelectSourcesUnpaywallHint(t *testing.T) {
	c := New(staticMirrors{}, baseChainConfig(), WithSources(stubSource{name: "libgen", supports: true}))

	if _, err := c.selectSources("scihub"); err == nil {
		t.Error("want error when a non-unpaywall source is not enabled")
	}

	_, err := c.selectSources("unpaywall")
	if err == nil {
		t.Fatal("want error when unpaywall is not enabled")
	}
	if !strings.Contains(err.Error(), "LIBGEN_MCP_UNPAYWALL_EMAIL") {
		t.Errorf("unpaywall error %q should point at the email gate", err)
	}
}

// sourceNames returns the Name() of every source in the client's chain, in order.
func sourceNames(c *Client) []string {
	names := make([]string, 0, len(c.sources))
	for _, s := range c.sources {
		names = append(names, s.Name())
	}
	return names
}

// baseChainConfig is a minimal config for exercising New's source wiring.
func baseChainConfig() *config.Config {
	return &config.Config{
		Timeout:                5 * time.Second,
		RateRPS:                1000,
		RateBurst:              100,
		RetryAttempts:          1,
		MaxConcurrentDownloads: 2,
		UnpaywallEmail:         "mail@jmrp.io",
		ScihubHosts:            []string{"sci-hub.se"},
	}
}

// TestNewWiresSourceChainFromConfig verifies New assembles the full ordered chain
// [unpaywall, scihub, libgen, randombook] and that Supports filters it into the
// right per-item order: articles get [unpaywall, scihub], books get
// [libgen, randombook].
func TestNewWiresSourceChainFromConfig(t *testing.T) {
	c := New(staticMirrors{}, baseChainConfig())

	if got, want := sourceNames(c), []string{"unpaywall", "scihub", "libgen", "randombook"}; !slices.Equal(got, want) {
		t.Fatalf("chain = %v, want %v", got, want)
	}

	var book, article []string
	for _, s := range c.sources {
		if s.Supports(Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}) {
			book = append(book, s.Name())
		}
		if s.Supports(Item{DOI: "10.1/x"}) {
			article = append(article, s.Name())
		}
	}
	if want := []string{"libgen", "randombook"}; !slices.Equal(book, want) {
		t.Errorf("book chain = %v, want %v", book, want)
	}
	if want := []string{"unpaywall", "scihub"}; !slices.Equal(article, want) {
		t.Errorf("article chain = %v, want %v", article, want)
	}
}

// TestEnabledSourceNames verifies EnabledSourceNames splits the enabled chain
// into book (md5) and article (doi) sources, and that an empty unpaywall email
// drops unpaywall from the article list.
func TestEnabledSourceNames(t *testing.T) {
	book, article := New(staticMirrors{}, baseChainConfig()).EnabledSourceNames()
	if want := []string{"libgen", "randombook"}; !slices.Equal(book, want) {
		t.Errorf("book = %v, want %v", book, want)
	}
	if want := []string{"unpaywall", "scihub"}; !slices.Equal(article, want) {
		t.Errorf("article = %v, want %v", article, want)
	}

	noEmail := baseChainConfig()
	noEmail.UnpaywallEmail = ""
	book, article = New(staticMirrors{}, noEmail).EnabledSourceNames()
	if want := []string{"libgen", "randombook"}; !slices.Equal(book, want) {
		t.Errorf("book (no email) = %v, want %v", book, want)
	}
	if want := []string{"scihub"}; !slices.Equal(article, want) {
		t.Errorf("article (no email) = %v, want %v", article, want)
	}
}

// TestNewSourcesFilter verifies LIBGEN_MCP_SOURCES disables sources by name while
// preserving the relative order of the remaining ones.
func TestNewSourcesFilter(t *testing.T) {
	cfg := baseChainConfig()
	cfg.Sources = []string{"libgen", "unpaywall"}
	c := New(staticMirrors{}, cfg)

	if got, want := sourceNames(c), []string{"unpaywall", "libgen"}; !slices.Equal(got, want) {
		t.Errorf("filtered chain = %v, want %v", got, want)
	}
}
