// Package mirrors descubre y cachea los mirrors vivos de la familia libgen.li.
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

// DefaultFallback es la lista de respaldo si no hay red ni caché (verificada 2026-07-17).
var DefaultFallback = []string{
	"https://libgen.li", "https://libgen.vg", "https://libgen.la",
	"https://libgen.bz", "https://libgen.gl",
}

var mirrorHostRe = regexp.MustCompile(`^https?://(libgen\.[a-z]{2,6})/?$`)

// Parse extrae las URLs base de mirrors libgen de la página de shadowlibraries.
func Parse(r io.Reader) ([]string, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parsing mirrors page: %w", err)
	}
	var out []string
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key != "href" {
					continue
				}
				if m := mirrorHostRe.FindStringSubmatch(strings.TrimSpace(a.Val)); m != nil {
					u := "https://" + m[1]
					if !seen[u] {
						seen[u] = true
						out = append(out, u)
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if len(out) == 0 {
		return nil, errors.New("no libgen mirrors found in page (layout change?)")
	}
	return out, nil
}

type cacheFile struct {
	FetchedAt time.Time `json:"fetched_at"`
	Mirrors   []string  `json:"mirrors"`
}

type Manager struct {
	SourceURL string
	CachePath string
	Preferred string
	HTTP      *http.Client

	mu     sync.Mutex
	cached []string
}

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

// Mirrors devuelve las URLs base con el mirror preferido primero. Nunca vacío.
func (m *Manager) Mirrors(ctx context.Context) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cached == nil {
		m.cached = orderPreferred(m.load(ctx), m.Preferred)
	}
	return m.cached
}

func (m *Manager) load(ctx context.Context) []string {
	if c, err := m.readCache(); err == nil && time.Since(c.FetchedAt) < cacheTTL {
		return c.Mirrors
	}
	if list, err := m.fetch(ctx); err == nil {
		m.writeCache(list)
		return list
	}
	if c, err := m.readCache(); err == nil { // caché caducada mejor que nada
		return c.Mirrors
	}
	return slices.Clone(DefaultFallback)
}

func (m *Manager) fetch(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.SourceURL, nil)
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
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if len(c.Mirrors) == 0 {
		return nil, errors.New("empty cache")
	}
	return &c, nil
}

func (m *Manager) writeCache(list []string) {
	data, err := json.Marshal(cacheFile{FetchedAt: time.Now(), Mirrors: list})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.CachePath), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(m.CachePath, data, 0o644) // caché best-effort
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
