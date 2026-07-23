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

// Family describes one mirror family discoverable from the shadowlibraries
// catalog. Discovery, caching, ordering and fallback semantics are identical
// across families, so a family is data rather than duplicated code.
type Family struct {
	// Name identifies the family in error messages.
	Name string
	// SourceURL is the catalog page listing the family's mirrors.
	SourceURL string
	// Preferred is the mirror ordered first when present.
	Preferred string
	// CacheFile is the basename of the family's on-disk cache, kept distinct per
	// family so two managers never overwrite each other's list.
	CacheFile string
	// HostRe matches a catalog anchor href, capturing the bare host.
	HostRe *regexp.Regexp
	// Fallback is used when there is no network and no usable cache.
	Fallback []string
}

// LibgenFamily is the Library Genesis mirror family, the historical default.
var LibgenFamily = Family{
	Name:      "libgen",
	SourceURL: DefaultSourceURL,
	Preferred: DefaultPreferred,
	CacheFile: "mirrors.json",
	HostRe:    mirrorHostRe,
	Fallback:  DefaultFallback,
}

// AnnasFamily is the Anna's Archive mirror family, discovered from the same
// catalog site as the libgen family (verified 2026-07-23: .gl, .pk, .gd).
var AnnasFamily = Family{
	Name:      "annas",
	SourceURL: "https://shadowlibraries.github.io/DirectDownloads/AnnasArchive/",
	Preferred: "https://annas-archive.gl",
	CacheFile: "annas-mirrors.json",
	HostRe:    regexp.MustCompile(`^https?://(annas-archive\.[a-z]{2,6})/?$`),
	Fallback: []string{
		"https://annas-archive.gl", "https://annas-archive.pk", "https://annas-archive.gd",
	},
}

// Parse extracts the libgen mirror base URLs from the shadowlibraries page. It
// is retained for callers that only need the default family and delegates to
// ParseFamily.
func Parse(r io.Reader) ([]string, error) { return ParseFamily(r, LibgenFamily) }

// ParseFamily extracts f's mirror base URLs from a shadowlibraries catalog page.
func ParseFamily(r io.Reader, f Family) ([]string, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parsing mirrors page: %w", err)
	}
	out := collectMirrors(doc, f)
	if len(out) == 0 {
		return nil, fmt.Errorf("no %s mirrors found in page (layout change?)", f.Name)
	}
	return out, nil
}

// collectMirrors walks the parsed document and returns the deduplicated mirror
// base URLs for f in document order.
func collectMirrors(doc *html.Node, f Family) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		appendMirrorsFromNode(n, f, seen, &out)
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

// appendMirrorsFromNode appends any new mirror base URLs for f found in n's
// anchor href attributes to out, tracking already-seen URLs in seen.
func appendMirrorsFromNode(n *html.Node, f Family, seen map[string]bool, out *[]string) {
	if n.Type != html.ElementNode || n.Data != "a" {
		return
	}
	for _, a := range n.Attr {
		if a.Key != "href" {
			continue
		}
		m := f.HostRe.FindStringSubmatch(strings.TrimSpace(a.Val))
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

	// family selects which mirror family this manager discovers. A zero value
	// means the libgen family (see resolveFamily), so a Manager built by setting
	// fields directly keeps the historical behavior.
	family Family

	mu                 sync.Mutex
	cached             []string
	cachedAt           time.Time
	cachedFromFallback bool
}

// resolveFamily returns the manager's family, falling back to the libgen family
// for a zero-value Manager built by a caller (or test) that set fields directly.
func (m *Manager) resolveFamily() Family {
	if m.family.HostRe == nil || m.family.Fallback == nil {
		return LibgenFamily
	}
	return m.family
}

// NewManager builds a Manager for the libgen family from the configuration,
// using the configured mirror as preferred (or DefaultPreferred when unset) and
// the OS cache dir for the cache.
func NewManager(cfg *config.Config) (*Manager, error) {
	return NewManagerFor(LibgenFamily, cfg)
}

// NewManagerFor builds a Manager for the given family. cfg.Mirror (LIBGEN_MIRROR)
// is a libgen-family setting, so it overrides the preferred mirror only for that
// family; other families keep their own default. Each family caches to its own
// file under the OS cache dir.
func NewManagerFor(f Family, cfg *config.Config) (*Manager, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("resolving cache dir: %w", err)
	}
	preferred := f.Preferred
	if f.Name == LibgenFamily.Name && cfg.Mirror != "" {
		preferred = cfg.Mirror
	}
	return &Manager{
		SourceURL: f.SourceURL,
		CachePath: filepath.Join(cacheDir, "libgen-mcp", f.CacheFile),
		Preferred: preferred,
		HTTP:      &http.Client{Timeout: cfg.Timeout},
		family:    f,
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
	return slices.Clone(m.resolveFamily().Fallback), true
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
	return ParseFamily(resp.Body, m.resolveFamily())
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
