package libgen

import (
	"slices"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

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
