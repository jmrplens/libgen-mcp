// Package mirrors discovers and caches the live mirrors of the libgen.li family.
package mirrors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

const (
	DefaultSourceURL = "https://shadowlibraries.github.io/DirectDownloads/libgen/"
	DefaultPreferred = "https://libgen.li"
	cacheTTL         = 24 * time.Hour
)

// DefaultFallback is the fallback list used when there is no network or cache (verified 2026-07-17).
var DefaultFallback = []string{
	"https://libgen.li", "https://libgen.vg", "https://libgen.la",
	"https://libgen.bz", "https://libgen.gl",
}

var mirrorHostRe = regexp.MustCompile(`^https?://(libgen\.[a-z]{2,6})/?$`)

// Parse extracts the libgen mirror base URLs from the shadowlibraries page.
func Parse(r io.Reader) ([]string, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parsing mirrors page: %w", err)
	}
	out := collectMirrors(doc)
	if len(out) == 0 {
		return nil, errors.New("no libgen mirrors found in page (layout change?)")
	}
	return out, nil
}

// collectMirrors walks the parsed document and returns the deduplicated mirror
// base URLs in document order.
func collectMirrors(doc *html.Node) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		appendMirrorsFromNode(n, seen, &out)
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

// appendMirrorsFromNode appends any new mirror base URLs found in n's anchor
// href attributes to out, tracking already-seen URLs in seen.
func appendMirrorsFromNode(n *html.Node, seen map[string]bool, out *[]string) {
	if n.Type != html.ElementNode || n.Data != "a" {
		return
	}
	for _, a := range n.Attr {
		if a.Key != "href" {
			continue
		}
		m := mirrorHostRe.FindStringSubmatch(strings.TrimSpace(a.Val))
		if m == nil {
			continue
		}
		u := "https://" + m[1]
		if !seen[u] {
			seen[u] = true
			*out = append(*out, u)
		}
	}
}

type cacheFile struct {
	FetchedAt time.Time `json:"fetched_at"`
	Mirrors   []string  `json:"mirrors"`
}

// Manager discovers, caches and orders the libgen mirrors, keeping the preferred
// mirror first.
type Manager struct {
	SourceURL string
	CachePath string
	Preferred string
	HTTP      *http.Client

	mu                 sync.Mutex
	cached             []string
	cachedAt           time.Time
	cachedFromFallback bool
}

// NewManager builds a Manager from the configuration, using the configured mirror
// as preferred (or DefaultPreferred when unset) and the OS cache dir for the cache.
func NewManager(cfg *config.Config) (*Manager, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("resolving cache dir: %w", err)
	}
	preferred := cfg.Mirror
	if preferred == "" {
		preferred = DefaultPreferred
	}
	return &Manager{
		SourceURL: DefaultSourceURL,
		CachePath: filepath.Join(cacheDir, "libgen-mcp", "mirrors.json"),
		Preferred: preferred,
		HTTP:      &http.Client{Timeout: cfg.Timeout},
	}, nil
}

// Mirrors returns the base URLs with the preferred mirror first. Never empty.
//
// In-memory memoization only persists results from a live discovery or a valid
// disk cache: a fallback (no network and no usable cache) is NOT memoized, so the
// next call retries discovery instead of staying pinned to the fallback list.
// Also, after a successful memoization, discovery is retried once the in-memory
// result exceeds cacheTTL, so a long-running server picks up mirror changes
// without a restart.
func (m *Manager) Mirrors(ctx context.Context) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cached == nil || m.cachedFromFallback || time.Since(m.cachedAt) >= cacheTTL {
		list, fromFallback := m.load(ctx)
		m.cached = orderPreferred(list, m.Preferred)
		m.cachedAt = time.Now()
		m.cachedFromFallback = fromFallback
	}
	return m.cached
}

// load returns the mirror list and whether it had to fall back to the hardcoded
// list (no live fetch and no usable disk cache).
func (m *Manager) load(ctx context.Context) (list []string, fromFallback bool) {
	if c, err := m.readCache(); err == nil && time.Since(c.FetchedAt) < cacheTTL {
		return c.Mirrors, false
	}
	if fetched, err := m.fetch(ctx); err == nil {
		m.writeCache(fetched)
		return fetched, false
	}
	if c, err := m.readCache(); err == nil { // a stale cache is better than nothing
		return c.Mirrors, false
	}
	return slices.Clone(DefaultFallback), true
}

func (m *Manager) fetch(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.SourceURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mirrors source: status %d", resp.StatusCode)
	}
	return Parse(resp.Body)
}

func (m *Manager) readCache() (*cacheFile, error) {
	data, err := os.ReadFile(m.CachePath)
	if err != nil {
		return nil, err
	}
	var c cacheFile
	if uerr := json.Unmarshal(data, &c); uerr != nil {
		return nil, uerr
	}
	if len(c.Mirrors) == 0 {
		return nil, errors.New("empty cache")
	}
	return &c, nil
}

// jsonMarshal is a test seam. Marshaling a cacheFile (a time.Time plus a
// []string) cannot fail for values produced at runtime, so it is overridden in
// tests to exercise the defensive marshal-error branch below.
var jsonMarshal = json.Marshal

func (m *Manager) writeCache(list []string) {
	data, err := jsonMarshal(cacheFile{FetchedAt: time.Now(), Mirrors: list})
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(m.CachePath), 0o750) != nil {
		return
	}
	_ = os.WriteFile(m.CachePath, data, 0o600) // best-effort cache
}

func orderPreferred(list []string, preferred string) []string {
	out := []string{preferred}
	for _, u := range list {
		if u != preferred {
			out = append(out, u)
		}
	}
	return out
}
