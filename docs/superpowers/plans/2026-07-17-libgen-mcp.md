# libgen-mcp Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Servidor MCP en Go que busca en Library Genesis (familia libgen.li), devuelve metadatos + opciones de descarga y descarga ficheros a disco.

**Architecture:** Cliente HTTP con failover entre mirrors auto-descubiertos desde shadowlibraries.github.io; búsqueda parseando la tabla HTML de `index.php`, detalles vía API `json.php`, descarga resolviendo la cadena `ads.php→get.php→CDN`. 3 tools MCP (`search`, `get_details`, `download`) sobre el SDK oficial. Spec: `docs/superpowers/specs/2026-07-17-libgen-mcp-design.md`.

**Tech Stack:** Go ≥1.25, `github.com/modelcontextprotocol/go-sdk/mcp`, `golang.org/x/net/html`, `golang.org/x/time/rate`.

## Global Constraints

- Módulo: `github.com/jmrplens/libgen-mcp`. Licencia MIT.
- Rate limit propio: 1 req/s hacia mirrors (`rate.NewLimiter(rate.Every(time.Second), 1)`).
- Env vars: `LIBGEN_MIRROR` (mirror forzado), `LIBGEN_MCP_DOWNLOAD_DIR` (def. `~/Downloads`), `LIBGEN_MCP_TIMEOUT` (def. `30s`).
- Errores siempre distinguibles: 0 resultados ≠ cambio de maquetado (`ErrLayoutChanged`) ≠ mirrors inaccesibles (`ErrAllMirrorsFailed`).
- Los tests unitarios NUNCA tocan la red real: fixtures en `testdata/` + `httptest`. Red real solo bajo build tag `e2e` y en `cmd/probe`.
- Tras cada tarea: `gofmt -l .` sin salida y `go vet ./...` limpio antes de commitear.

**Datos del sitio verificados el 2026-07-17** (referencia para todo el plan):

- Búsqueda: `GET /index.php?req=Q` + opcionales `topics[]=` (`l` nonfiction, `f` fiction, `a` articles, `m` magazines, `c` comics, `s` standards, `r` fiction_rus), `columns[]=` (`t` title, `a` author, `s` series, `y` year, `p` publisher, `i` isbn), `res=` (25/50/100), `page=`, `order=` (`f_id`, `time_added`, `title`, `author`, `year`, `filesize`), `ordermode=` (`asc`/`desc`). Sin `topics[]`/`columns[]` busca en todo.
- Resultados: `<table id="tablelibgen"><tbody>`, una `<tr>` por fichero, 9 `<td>`: [0] título (enlaces `edition.php?id=`, ISBNs en verde, badges), [1] autores, [2] editorial, [3] año, [4] idioma, [5] páginas, [6] tamaño (`<a href="/file.php?id=FILEID">17 MB</a>`), [7] extensión, [8] mirrors (`/ads.php?md5=MD5` + externos con `title=` tooltip).
- Total de resultados: pestaña `<a class="nav-link…" href="…curtab=f">Files <span class="badge badge-primary">N</span></a>`. En búsqueda vacía no hay tabla pero las pestañas muestran `0`.
- API JSON: `GET /json.php?object={e|f}&ids=ID` o `object=f&md5=MD5`; con `addkeys=*` los ficheros incluyen subarray `editions` con `e_id`. Devuelve mapa `id → objeto`. 
- Descarga: `GET /ads.php?md5=X` → HTML con `get.php?md5=X&key=CLAVE` → `GET get.php…` → 307 a CDN → fichero con `content-disposition: attachment; filename="…"`.
- Mirrors: `https://shadowlibraries.github.io/DirectDownloads/libgen/` lista `<ul>` con libgen.li/.vg/.la/.bz/.gl (misma familia de software).

---

### Task 1: Bootstrap del módulo Go

**Files:**
- Create: `go.mod`, `.gitignore`, `LICENSE`

**Interfaces:**
- Consumes: nada
- Produces: módulo `github.com/jmrplens/libgen-mcp` con dependencias resueltas

- [ ] **Step 1: Inicializar módulo y dependencias**

```bash
cd /Users/jmrplens/GIT/libgen-mcp
go mod init github.com/jmrplens/libgen-mcp
go get github.com/modelcontextprotocol/go-sdk@latest golang.org/x/net@latest golang.org/x/time@latest
```

Expected: `go.mod` y `go.sum` creados sin errores.

- [ ] **Step 2: Crear `.gitignore`**

```gitignore
/libgen-mcp
/probe
/dist/
coverage.out
*.test
```

- [ ] **Step 3: Crear `LICENSE`** — texto MIT estándar con `Copyright (c) 2026 jmrplens`.

- [ ] **Step 4: Verificar**

Run: `go build ./... && gofmt -l .`
Expected: sin salida, exit 0.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum .gitignore LICENSE
git commit -m "chore: bootstrap Go module"
```

---

### Task 2: Capturar fixtures HTML/JSON reales

**Files:**
- Create: `internal/mirrors/testdata/shadowlibraries.html`
- Create: `internal/libgen/testdata/{search_books.html,search_articles.html,search_empty.html,ads.html,file_by_md5.json,edition.json}`

**Interfaces:**
- Consumes: red real (una única vez; las fixtures quedan committeadas)
- Produces: fixtures que usan los tests de las Tasks 4–8

- [ ] **Step 1: Descargar fixtures**

```bash
mkdir -p internal/mirrors/testdata internal/libgen/testdata
curl -sf "https://shadowlibraries.github.io/DirectDownloads/libgen/" -o internal/mirrors/testdata/shadowlibraries.html
curl -sf "https://libgen.li/index.php?req=golang&topics%5B%5D=l" -o internal/libgen/testdata/search_books.html
curl -sf "https://libgen.li/index.php?req=neural+network&topics%5B%5D=a" -o internal/libgen/testdata/search_articles.html
curl -sf "https://libgen.li/index.php?req=zzqxjvkwpqmznoresult" -o internal/libgen/testdata/search_empty.html
curl -sf "https://libgen.li/ads.php?md5=87a4ebdaf21fa6cc70009a3dd63194ee" -o internal/libgen/testdata/ads.html
curl -sf "https://libgen.li/json.php?object=f&md5=87a4ebdaf21fa6cc70009a3dd63194ee&addkeys=*" -o internal/libgen/testdata/file_by_md5.json
curl -sf "https://libgen.li/json.php?object=e&ids=138281637" -o internal/libgen/testdata/edition.json
```

(Si libgen.li no responde, sustituir por `libgen.la` u otro mirror de la lista.)

- [ ] **Step 2: Verificar contenido**

```bash
grep -l 'tablelibgen' internal/libgen/testdata/search_books.html internal/libgen/testdata/search_articles.html
grep -c 'get.php?md5=87a4ebdaf21fa6cc70009a3dd63194ee' internal/libgen/testdata/ads.html
grep -o 'libgen\.[a-z]*' internal/mirrors/testdata/shadowlibraries.html | sort -u
python3 -m json.tool internal/libgen/testdata/file_by_md5.json > /dev/null && echo JSON_OK
python3 -m json.tool internal/libgen/testdata/edition.json > /dev/null && echo JSON_OK
```

Expected: las dos páginas de búsqueda contienen la tabla; ads contiene ≥1 enlace get.php; shadowlibraries lista ≥3 dominios libgen; ambos JSON válidos.

- [ ] **Step 3: Anotar los valores reales de la primera fila** (se usan como literales esperados en el test de la Task 6):

```bash
grep -o 'ads.php?md5=[0-9a-f]*' internal/libgen/testdata/search_books.html | head -3
grep -o 'edition.php?id=[0-9]*' internal/libgen/testdata/search_books.html | head -3
```

- [ ] **Step 4: Commit**

```bash
git add internal/mirrors/testdata internal/libgen/testdata
git commit -m "test: add real HTML/JSON fixtures from libgen.li"
```

---

### Task 3: `internal/config`

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.Config{Mirror string; DownloadDir string; Timeout time.Duration}`, `config.Load() (*Config, error)`

- [ ] **Step 1: Test que falla**

```go
package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("LIBGEN_MIRROR", "")
	t.Setenv("LIBGEN_MCP_DOWNLOAD_DIR", "")
	t.Setenv("LIBGEN_MCP_TIMEOUT", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Mirror != "" {
		t.Errorf("Mirror = %q, want empty", cfg.Mirror)
	}
	if filepath.Base(cfg.DownloadDir) != "Downloads" {
		t.Errorf("DownloadDir = %q, want ~/Downloads", cfg.DownloadDir)
	}
	if cfg.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", cfg.Timeout)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("LIBGEN_MIRROR", "https://libgen.la/")
	t.Setenv("LIBGEN_MCP_DOWNLOAD_DIR", "/tmp/books")
	t.Setenv("LIBGEN_MCP_TIMEOUT", "5s")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Mirror != "https://libgen.la" {
		t.Errorf("Mirror = %q, want https://libgen.la (sin barra final)", cfg.Mirror)
	}
	if cfg.DownloadDir != "/tmp/books" {
		t.Errorf("DownloadDir = %q", cfg.DownloadDir)
	}
	if cfg.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want 5s", cfg.Timeout)
	}
}

func TestLoadBadTimeout(t *testing.T) {
	t.Setenv("LIBGEN_MCP_TIMEOUT", "banana")
	if _, err := Load(); err == nil {
		t.Fatal("Load() con timeout inválido debería fallar")
	}
}
```

- [ ] **Step 2: Verificar que falla** — Run: `go test ./internal/config/ -v` — Expected: FAIL (Load no definido).

- [ ] **Step 3: Implementación**

```go
// Package config carga la configuración del servidor desde variables de entorno.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	Mirror      string        // LIBGEN_MIRROR: mirror forzado, p. ej. https://libgen.li
	DownloadDir string        // LIBGEN_MCP_DOWNLOAD_DIR: destino de descargas
	Timeout     time.Duration // LIBGEN_MCP_TIMEOUT: timeout por petición HTTP
}

func Load() (*Config, error) {
	cfg := &Config{
		Mirror:  strings.TrimRight(os.Getenv("LIBGEN_MIRROR"), "/"),
		Timeout: 30 * time.Second,
	}
	if dir := os.Getenv("LIBGEN_MCP_DOWNLOAD_DIR"); dir != "" {
		cfg.DownloadDir = dir
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolving home dir: %w", err)
		}
		cfg.DownloadDir = filepath.Join(home, "Downloads")
	}
	if v := os.Getenv("LIBGEN_MCP_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("LIBGEN_MCP_TIMEOUT: %w", err)
		}
		cfg.Timeout = d
	}
	return cfg, nil
}
```

- [ ] **Step 4: Verificar que pasa** — Run: `go test ./internal/config/ -v` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat: config from environment variables"
```

---

### Task 4: `internal/mirrors` — descubrimiento, caché y orden

**Files:**
- Create: `internal/mirrors/mirrors.go`
- Test: `internal/mirrors/mirrors_test.go`

**Interfaces:**
- Consumes: `config.Config`
- Produces:
  - `mirrors.Parse(r io.Reader) ([]string, error)`
  - `mirrors.NewManager(cfg *config.Config) (*Manager, error)`
  - `(*Manager) Mirrors(ctx context.Context) []string` — bases `https://…` sin barra final, preferido primero, nunca vacío
  - `mirrors.DefaultFallback []string`

- [ ] **Step 1: Tests que fallan**

```go
package mirrors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestParseFixture(t *testing.T) {
	f, err := os.Open("testdata/shadowlibraries.html")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := Parse(f)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := []string{"https://libgen.li", "https://libgen.vg", "https://libgen.la", "https://libgen.bz", "https://libgen.gl"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse() = %v, want %v", got, want)
	}
}

func TestParseNoMirrors(t *testing.T) {
	if _, err := Parse(strings.NewReader("<html><body>nada</body></html>")); err == nil {
		t.Fatal("Parse() sin mirrors debería fallar")
	}
}

func TestOrderPreferred(t *testing.T) {
	list := []string{"https://libgen.li", "https://libgen.vg"}
	got := orderPreferred(list, "https://libgen.vg")
	if got[0] != "https://libgen.vg" || len(got) != 2 {
		t.Errorf("orderPreferred() = %v", got)
	}
	// preferido ausente de la lista: se añade delante
	got = orderPreferred(list, "https://libgen.example")
	if got[0] != "https://libgen.example" || len(got) != 3 {
		t.Errorf("orderPreferred() con preferido nuevo = %v", got)
	}
}

func TestManagerFetchesAndCaches(t *testing.T) {
	page, _ := os.ReadFile("testdata/shadowlibraries.html")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(page)
	}))
	defer srv.Close()
	cachePath := filepath.Join(t.TempDir(), "mirrors.json")
	m := &Manager{SourceURL: srv.URL, CachePath: cachePath, Preferred: "https://libgen.li", HTTP: srv.Client()}
	got := m.Mirrors(context.Background())
	if len(got) != 5 || got[0] != "https://libgen.li" {
		t.Fatalf("Mirrors() = %v", got)
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("caché no escrita: %v", err)
	}
}

func TestManagerUsesStaleCacheWhenSourceDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	cachePath := filepath.Join(t.TempDir(), "mirrors.json")
	stale := cacheFile{FetchedAt: time.Now().Add(-48 * time.Hour), Mirrors: []string{"https://libgen.la"}}
	data, _ := json.Marshal(stale)
	os.WriteFile(cachePath, data, 0o644)
	m := &Manager{SourceURL: srv.URL, CachePath: cachePath, Preferred: "https://libgen.la", HTTP: srv.Client()}
	got := m.Mirrors(context.Background())
	if got[0] != "https://libgen.la" {
		t.Errorf("Mirrors() con caché caducada = %v, esperaba usarla", got)
	}
}

func TestManagerFallsBackToHardcoded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	m := &Manager{SourceURL: srv.URL, CachePath: filepath.Join(t.TempDir(), "mirrors.json"), Preferred: "https://libgen.li", HTTP: srv.Client()}
	got := m.Mirrors(context.Background())
	if !reflect.DeepEqual(got, DefaultFallback) {
		t.Errorf("Mirrors() = %v, want fallback %v", got, DefaultFallback)
	}
}
```

(Añadir `"strings"` al import del test.)

- [ ] **Step 2: Verificar que falla** — Run: `go test ./internal/mirrors/ -v` — Expected: FAIL (símbolos no definidos).

- [ ] **Step 3: Implementación**

```go
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
```

- [ ] **Step 4: Verificar que pasa** — Run: `go test ./internal/mirrors/ -v` — Expected: PASS (los 5 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/mirrors
git commit -m "feat: mirror discovery with cache and fallback"
```

---

### Task 5: `internal/libgen` — cliente HTTP con failover y rate limit

**Files:**
- Create: `internal/libgen/client.go`
- Test: `internal/libgen/client_test.go`

**Interfaces:**
- Consumes: `MirrorLister` (lo implementa `mirrors.Manager`)
- Produces:
  - `libgen.MirrorLister interface { Mirrors(ctx context.Context) []string }`
  - `libgen.New(m MirrorLister, timeout time.Duration) *Client`
  - `(*Client) get(ctx context.Context, path string, q url.Values) (body []byte, mirrorBase string, err error)` (interno al paquete)
  - `libgen.ErrAllMirrorsFailed`
  - Campos internos del Client usados por tasks posteriores: `c.http` (con timeout), `c.dl` (sin timeout, para streaming de descargas), `c.limiter`

- [ ] **Step 1: Tests que fallan**

```go
package libgen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

type staticMirrors []string

func (s staticMirrors) Mirrors(context.Context) []string { return s }

func newTestClient(m MirrorLister) *Client {
	c := New(m, 5*time.Second)
	c.limiter.SetLimit(1000) // sin espera en tests
	return c
}

func TestGetFailsOver(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("req") != "golang" {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		w.Write([]byte("ok-body"))
	}))
	defer good.Close()

	c := newTestClient(staticMirrors{bad.URL, good.URL})
	body, base, err := c.get(context.Background(), "/index.php", url.Values{"req": {"golang"}})
	if err != nil {
		t.Fatalf("get() error = %v", err)
	}
	if string(body) != "ok-body" || base != good.URL {
		t.Errorf("get() = %q desde %q, esperaba ok-body desde %q", body, base, good.URL)
	}
}

func TestGetAllMirrorsFailed(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer bad.Close()
	c := newTestClient(staticMirrors{bad.URL})
	_, _, err := c.get(context.Background(), "/index.php", nil)
	if !errors.Is(err, ErrAllMirrorsFailed) {
		t.Fatalf("err = %v, want ErrAllMirrorsFailed", err)
	}
}
```

- [ ] **Step 2: Verificar que falla** — Run: `go test ./internal/libgen/ -v` — Expected: FAIL.

- [ ] **Step 3: Implementación**

```go
// Package libgen implementa el cliente HTTP contra la familia de mirrors libgen.li:
// búsqueda (HTML), detalles (json.php) y descarga (ads.php → get.php → CDN).
package libgen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/time/rate"
)

const (
	userAgent   = "libgen-mcp/0.1.0 (+https://github.com/jmrplens/libgen-mcp)"
	maxBodySize = 20 << 20 // 20 MiB para páginas HTML/JSON (no descargas)
)

// ErrAllMirrorsFailed indica que ningún mirror respondió correctamente.
var ErrAllMirrorsFailed = errors.New("all libgen mirrors unreachable (network block? try a VPN or different DNS)")

// MirrorLister aporta las URLs base candidatas, preferida primero.
type MirrorLister interface {
	Mirrors(ctx context.Context) []string
}

type Client struct {
	mirrors MirrorLister
	http    *http.Client // páginas: con timeout
	dl      *http.Client // descargas en streaming: sin timeout global, gobierna ctx
	limiter *rate.Limiter
}

func New(m MirrorLister, timeout time.Duration) *Client {
	return &Client{
		mirrors: m,
		http:    &http.Client{Timeout: timeout},
		dl:      &http.Client{},
		limiter: rate.NewLimiter(rate.Every(time.Second), 1),
	}
}

// get prueba path?q en cada mirror hasta obtener un 200. Devuelve el cuerpo y
// la URL base del mirror que respondió.
func (c *Client) get(ctx context.Context, path string, q url.Values) ([]byte, string, error) {
	var errs []error
	for _, base := range c.mirrors.Mirrors(ctx) {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, "", err
		}
		u := base + path
		if len(q) > 0 {
			u += "?" + q.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := c.http.Do(req)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", base, err))
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			errs = append(errs, fmt.Errorf("%s: status %d", base, resp.StatusCode))
			continue
		}
		if readErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", base, readErr))
			continue
		}
		return body, base, nil
	}
	return nil, "", fmt.Errorf("%w: %w", ErrAllMirrorsFailed, errors.Join(errs...))
}
```

- [ ] **Step 4: Verificar que pasa** — Run: `go test ./internal/libgen/ -v` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/libgen
git commit -m "feat: libgen HTTP client with mirror failover and rate limit"
```

---

### Task 6: búsqueda — construcción de query y parser HTML

**Files:**
- Create: `internal/libgen/search.go`
- Test: `internal/libgen/search_test.go`

**Interfaces:**
- Consumes: `(*Client).get` de Task 5
- Produces:
  - `libgen.SearchParams{Query string; Topics, SearchIn []string; ResultsPerPage, Page int; Order, OrderMode string}` con `Validate() error` y `values() url.Values`
  - `libgen.Result{EditionID, FileID, MD5, Title, Authors, Publisher, Year, Language, Pages, Size, Extension, Type string; ISBNs []string; Downloads []DownloadOption}` (todos con tags JSON snake_case)
  - `libgen.DownloadOption{Label, URL string}`
  - `libgen.SearchPage{Results []Result; TotalFiles string}`
  - `libgen.ParseSearch(r io.Reader, base string) (*SearchPage, error)`
  - `libgen.ErrLayoutChanged`
  - `(*Client) Search(ctx context.Context, p SearchParams) (*SearchPage, string, error)` — la string es el mirror usado

- [ ] **Step 1: Tests que fallan**

Nota: los literales `wantMD5`/`wantEditionID` salen del Step 3 de la Task 2 (contenido real de la fixture committeada); los valores de abajo corresponden a la captura de 2026-07-17 — ajustarlos si la fixture difiere.

```go
package libgen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

var md5Re = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestSearchParamsValues(t *testing.T) {
	p := SearchParams{
		Query:          "golang",
		Topics:         []string{"nonfiction", "articles"},
		SearchIn:       []string{"title", "isbn"},
		ResultsPerPage: 50,
		Page:           2,
		Order:          "year",
		OrderMode:      "desc",
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	v := p.values()
	if v.Get("req") != "golang" {
		t.Errorf("req = %q", v.Get("req"))
	}
	if got := v["topics[]"]; len(got) != 2 || got[0] != "l" || got[1] != "a" {
		t.Errorf("topics[] = %v", got)
	}
	if got := v["columns[]"]; len(got) != 2 || got[0] != "t" || got[1] != "i" {
		t.Errorf("columns[] = %v", got)
	}
	if v.Get("res") != "50" || v.Get("page") != "2" || v.Get("order") != "year" || v.Get("ordermode") != "desc" {
		t.Errorf("values = %v", v)
	}
}

func TestSearchParamsMinimalOmitsDefaults(t *testing.T) {
	v := SearchParams{Query: "golang"}.values()
	for _, k := range []string{"topics[]", "columns[]", "res", "page", "order", "ordermode"} {
		if _, ok := v[k]; ok {
			t.Errorf("values() incluye %q sin haberse pedido", k)
		}
	}
}

func TestSearchParamsValidate(t *testing.T) {
	cases := []SearchParams{
		{Query: ""},
		{Query: "x", Topics: []string{"cooking"}},
		{Query: "x", SearchIn: []string{"body"}},
		{Query: "x", ResultsPerPage: 30},
		{Query: "x", Order: "pages"},
		{Query: "x", OrderMode: "up"},
	}
	for i, p := range cases {
		if err := p.Validate(); err == nil {
			t.Errorf("caso %d: Validate() = nil, esperaba error", i)
		}
	}
}

func parseFixture(t *testing.T, name string) *SearchPage {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	page, err := ParseSearch(f, "https://libgen.li")
	if err != nil {
		t.Fatalf("ParseSearch(%s) error = %v", name, err)
	}
	return page
}

func TestParseSearchBooks(t *testing.T) {
	page := parseFixture(t, "search_books.html")
	if len(page.Results) == 0 {
		t.Fatal("0 resultados en fixture de libros")
	}
	if page.TotalFiles == "" {
		t.Error("TotalFiles vacío")
	}
	for i, r := range page.Results {
		if !md5Re.MatchString(r.MD5) {
			t.Errorf("resultado %d: md5 inválido %q", i, r.MD5)
		}
		if r.Title == "" {
			t.Errorf("resultado %d: título vacío", i)
		}
		if len(r.Downloads) == 0 {
			t.Errorf("resultado %d: sin opciones de descarga", i)
		}
		if r.Downloads[0].Label != "libgen" || !strings.HasPrefix(r.Downloads[0].URL, "https://libgen.li/ads.php?md5=") {
			t.Errorf("resultado %d: primera descarga = %+v", i, r.Downloads[0])
		}
	}
	// Fila conocida de la captura 2026-07-17 (ajustar a la fixture committeada):
	const wantMD5 = "87a4ebdaf21fa6cc70009a3dd63194ee"
	var found *Result
	for i := range page.Results {
		if page.Results[i].MD5 == wantMD5 {
			found = &page.Results[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no aparece el md5 conocido %s", wantMD5)
	}
	if !strings.Contains(found.Title, "Golang") {
		t.Errorf("Title = %q", found.Title)
	}
	if found.EditionID != "138281637" || found.FileID != "93485370" {
		t.Errorf("EditionID/FileID = %s/%s", found.EditionID, found.FileID)
	}
	if found.Extension != "pdf" || found.Year != "2018" || found.Language != "English" {
		t.Errorf("ext/año/idioma = %s/%s/%s", found.Extension, found.Year, found.Language)
	}
	if len(found.ISBNs) == 0 {
		t.Error("sin ISBNs")
	}
}

func TestParseSearchArticles(t *testing.T) {
	page := parseFixture(t, "search_articles.html")
	if len(page.Results) == 0 {
		t.Fatal("0 resultados en fixture de artículos")
	}
}

func TestParseSearchEmpty(t *testing.T) {
	page := parseFixture(t, "search_empty.html")
	if len(page.Results) != 0 {
		t.Errorf("resultados = %d, esperaba 0", len(page.Results))
	}
	if page.TotalFiles != "0" {
		t.Errorf("TotalFiles = %q, esperaba \"0\"", page.TotalFiles)
	}
}

func TestParseSearchLayoutChanged(t *testing.T) {
	_, err := ParseSearch(strings.NewReader("<html><body><p>hola</p></body></html>"), "https://libgen.li")
	if err == nil || !strings.Contains(err.Error(), "layout") {
		t.Fatalf("err = %v, esperaba ErrLayoutChanged", err)
	}
}

func TestClientSearch(t *testing.T) {
	fixture, _ := os.ReadFile("testdata/search_books.html")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.php" || r.URL.Query().Get("req") != "golang" {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		w.Write(fixture)
	}))
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	page, mirror, err := c.Search(context.Background(), SearchParams{Query: "golang"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if mirror != srv.URL || len(page.Results) == 0 {
		t.Errorf("Search() mirror=%q results=%d", mirror, len(page.Results))
	}
}
```

- [ ] **Step 2: Verificar que falla** — Run: `go test ./internal/libgen/ -v` — Expected: FAIL.

- [ ] **Step 3: Implementación**

```go
package libgen

import (
	"context"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// ErrLayoutChanged indica que la página no tiene la estructura esperada:
// no confundir con "cero resultados".
var ErrLayoutChanged = errors.New("libgen page layout not recognized (site may have changed)")

var (
	topicCodes  = map[string]string{"nonfiction": "l", "fiction": "f", "articles": "a", "magazines": "m", "comics": "c", "standards": "s", "fiction_rus": "r"}
	columnCodes = map[string]string{"title": "t", "author": "a", "series": "s", "year": "y", "publisher": "p", "isbn": "i"}
	orderCodes  = map[string]string{"id": "f_id", "time_added": "time_added", "title": "title", "author": "author", "year": "year", "size": "filesize"}
)

func allowed[V any](m map[string]V) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return strings.Join(keys, ", ")
}

type SearchParams struct {
	Query          string
	Topics         []string
	SearchIn       []string
	ResultsPerPage int
	Page           int
	Order          string
	OrderMode      string
}

func (p SearchParams) Validate() error {
	if strings.TrimSpace(p.Query) == "" {
		return errors.New("query is required")
	}
	for _, t := range p.Topics {
		if _, ok := topicCodes[t]; !ok {
			return fmt.Errorf("unknown topic %q (allowed: %s)", t, allowed(topicCodes))
		}
	}
	for _, c := range p.SearchIn {
		if _, ok := columnCodes[c]; !ok {
			return fmt.Errorf("unknown search_in %q (allowed: %s)", c, allowed(columnCodes))
		}
	}
	if p.ResultsPerPage != 0 && p.ResultsPerPage != 25 && p.ResultsPerPage != 50 && p.ResultsPerPage != 100 {
		return errors.New("results_per_page must be 25, 50 or 100")
	}
	if p.Order != "" {
		if _, ok := orderCodes[p.Order]; !ok {
			return fmt.Errorf("unknown order %q (allowed: %s)", p.Order, allowed(orderCodes))
		}
	}
	if p.OrderMode != "" && p.OrderMode != "asc" && p.OrderMode != "desc" {
		return errors.New("order_mode must be asc or desc")
	}
	return nil
}

func (p SearchParams) values() url.Values {
	v := url.Values{}
	v.Set("req", p.Query)
	for _, t := range p.Topics {
		v.Add("topics[]", topicCodes[t])
	}
	for _, c := range p.SearchIn {
		v.Add("columns[]", columnCodes[c])
	}
	if p.ResultsPerPage != 0 {
		v.Set("res", strconv.Itoa(p.ResultsPerPage))
	}
	if p.Page > 1 {
		v.Set("page", strconv.Itoa(p.Page))
	}
	if p.Order != "" {
		v.Set("order", orderCodes[p.Order])
	}
	if p.OrderMode != "" {
		v.Set("ordermode", p.OrderMode)
	}
	return v
}

type DownloadOption struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type Result struct {
	EditionID string           `json:"edition_id,omitempty"`
	FileID    string           `json:"file_id,omitempty"`
	MD5       string           `json:"md5"`
	Title     string           `json:"title"`
	ISBNs     []string         `json:"isbns,omitempty"`
	Authors   string           `json:"authors,omitempty"`
	Publisher string           `json:"publisher,omitempty"`
	Year      string           `json:"year,omitempty"`
	Language  string           `json:"language,omitempty"`
	Pages     string           `json:"pages,omitempty"`
	Size      string           `json:"size,omitempty"`
	Extension string           `json:"extension,omitempty"`
	Type      string           `json:"type,omitempty"`
	Downloads []DownloadOption `json:"downloads"`
}

type SearchPage struct {
	Results    []Result `json:"results"`
	TotalFiles string   `json:"total_files,omitempty"`
}

// Search ejecuta la búsqueda y devuelve la página parseada y el mirror usado.
func (c *Client) Search(ctx context.Context, p SearchParams) (*SearchPage, string, error) {
	if err := p.Validate(); err != nil {
		return nil, "", err
	}
	body, base, err := c.get(ctx, "/index.php", p.values())
	if err != nil {
		return nil, "", err
	}
	page, err := ParseSearch(bytes.NewReader(body), base)
	if err != nil {
		return nil, "", err
	}
	return page, base, nil
}

// ParseSearch parsea la página de resultados. base absolutiza los enlaces relativos.
func ParseSearch(r io.Reader, base string) (*SearchPage, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parsing search page: %w", err)
	}
	page := &SearchPage{TotalFiles: filesTabCount(doc)}
	table := findByID(doc, "tablelibgen")
	if table == nil {
		if page.TotalFiles == "0" {
			return page, nil // búsqueda válida sin resultados
		}
		return nil, ErrLayoutChanged
	}
	for _, tr := range elements(table, "tr") {
		cells := childElements(tr, "td")
		if len(cells) < 9 {
			continue // cabecera u otra fila auxiliar
		}
		if res := parseRow(cells, base); res != nil {
			page.Results = append(page.Results, *res)
		}
	}
	return page, nil
}

func parseRow(cells []*html.Node, base string) *Result {
	r := Result{}
	for _, a := range elements(cells[0], "a") {
		href := attr(a, "href")
		if !strings.Contains(href, "edition.php?id=") {
			continue
		}
		if r.EditionID == "" {
			r.EditionID = queryParam(href, "id")
			r.Title = strings.TrimSpace(nodeText(a))
			continue
		}
		if r.ISBNs == nil { // segundo enlace edition.php: identificadores
			for _, s := range strings.Split(nodeText(a), ";") {
				if s = strings.TrimSpace(s); s != "" {
					r.ISBNs = append(r.ISBNs, s)
				}
			}
		}
	}
	for _, s := range elements(cells[0], "span") {
		if strings.Contains(attr(s, "class"), "badge-primary") {
			r.Type = strings.TrimSpace(nodeText(s))
			break
		}
	}
	r.Authors = strings.TrimSpace(nodeText(cells[1]))
	r.Publisher = strings.TrimSpace(nodeText(cells[2]))
	r.Year = strings.TrimSpace(nodeText(cells[3]))
	r.Language = strings.TrimSpace(nodeText(cells[4]))
	r.Pages = strings.TrimSpace(nodeText(cells[5]))
	r.Size = strings.TrimSpace(nodeText(cells[6]))
	for _, a := range elements(cells[6], "a") {
		if strings.Contains(attr(a, "href"), "file.php?id=") {
			r.FileID = queryParam(attr(a, "href"), "id")
			break
		}
	}
	r.Extension = strings.TrimSpace(nodeText(cells[7]))
	for _, a := range elements(cells[8], "a") {
		href := attr(a, "href")
		label := attr(a, "title")
		if strings.Contains(href, "ads.php?md5=") {
			r.MD5 = strings.ToLower(queryParam(href, "md5"))
			if strings.HasPrefix(href, "/") {
				href = base + href
			}
			label = "libgen"
		}
		if href != "" {
			r.Downloads = append(r.Downloads, DownloadOption{Label: label, URL: href})
		}
	}
	if r.MD5 == "" && r.Title == "" {
		return nil
	}
	return &r
}

// filesTabCount devuelve el contador de la pestaña "Files" ("138", "1000+", "0")
// o "" si no se encuentra.
func filesTabCount(doc *html.Node) string {
	for _, a := range elements(doc, "a") {
		if !strings.Contains(attr(a, "class"), "nav-link") || !strings.Contains(attr(a, "href"), "curtab=f") {
			continue
		}
		for _, s := range elements(a, "span") {
			if strings.Contains(attr(s, "class"), "badge") {
				return strings.TrimSpace(nodeText(s))
			}
		}
	}
	return ""
}

// --- helpers de DOM ---

func findByID(n *html.Node, id string) *html.Node {
	if n.Type == html.ElementNode && attr(n, "id") == id {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findByID(c, id); found != nil {
			return found
		}
	}
	return nil
}

// elements devuelve todos los descendientes con ese tag.
func elements(n *html.Node, tag string) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(m *html.Node) {
		if m != n && m.Type == html.ElementNode && m.Data == tag {
			out = append(out, m)
		}
		for c := m.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

// childElements devuelve solo hijos directos con ese tag.
func childElements(n *html.Node, tag string) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == tag {
			out = append(out, c)
		}
	}
	return out
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(m *html.Node) {
		if m.Type == html.TextNode {
			b.WriteString(m.Data)
		}
		for c := m.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func queryParam(href, key string) string {
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	return u.Query().Get(key)
}
```

Nota para el implementador: si `TestParseSearchBooks` falla porque las filas de la fixture usan `<tr>` dentro de `<tbody>` con celdas anidadas de otra forma, inspeccionar la fixture (`grep -A5 'tablelibgen' …`) y ajustar `childElements`/índices — el test con la fixture real es la fuente de verdad.

- [ ] **Step 4: Verificar que pasa** — Run: `go test ./internal/libgen/ -v` — Expected: PASS (todos).

- [ ] **Step 5: Commit**

```bash
git add internal/libgen
git commit -m "feat: search query builder and results HTML parser"
```

---

### Task 7: detalles vía `json.php`

**Files:**
- Create: `internal/libgen/details.go`
- Test: `internal/libgen/details_test.go`

**Interfaces:**
- Consumes: `(*Client).get` de Task 5
- Produces:
  - `(*Client) DetailsByMD5(ctx context.Context, md5 string) (file, edition map[string]any, err error)`
  - `(*Client) DetailsByID(ctx context.Context, object, id string) (map[string]any, error)` — object `"e"` o `"f"`

- [ ] **Step 1: Tests que fallan**

```go
package libgen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func jsonFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	fileJSON, err := os.ReadFile("testdata/file_by_md5.json")
	if err != nil {
		t.Fatal(err)
	}
	editionJSON, err := os.ReadFile("testdata/edition.json")
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json.php" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("object") {
		case "f":
			w.Write(fileJSON)
		case "e":
			w.Write(editionJSON)
		default:
			http.Error(w, "bad object", http.StatusBadRequest)
		}
	}))
}

func TestDetailsByMD5(t *testing.T) {
	srv := jsonFixtureServer(t)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	file, edition, err := c.DetailsByMD5(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee")
	if err != nil {
		t.Fatalf("DetailsByMD5() error = %v", err)
	}
	if file["md5"] != "87a4ebdaf21fa6cc70009a3dd63194ee" {
		t.Errorf("file.md5 = %v", file["md5"])
	}
	if edition == nil {
		t.Fatal("edition = nil, esperaba la edición relacionada")
	}
	if edition["title"] == "" || edition["title"] == nil {
		t.Errorf("edition.title vacío: %v", edition["title"])
	}
}

func TestDetailsByID(t *testing.T) {
	srv := jsonFixtureServer(t)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	ed, err := c.DetailsByID(context.Background(), "e", "138281637")
	if err != nil {
		t.Fatalf("DetailsByID() error = %v", err)
	}
	if ed["title"] == nil {
		t.Error("edition sin title")
	}
	if _, err := c.DetailsByID(context.Background(), "x", "1"); err == nil {
		t.Error("object inválido debería fallar")
	}
}

func TestDetailsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	if _, _, err := c.DetailsByMD5(context.Background(), "00000000000000000000000000000000"); err == nil {
		t.Error("md5 inexistente debería fallar")
	}
}
```

- [ ] **Step 2: Verificar que falla** — Run: `go test ./internal/libgen/ -run TestDetails -v` — Expected: FAIL.

- [ ] **Step 3: Implementación**

```go
package libgen

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// decodeObjects interpreta la respuesta de json.php (mapa id → objeto).
// Una respuesta `[]` (array vacío) significa "sin resultados".
func decodeObjects(body []byte) (map[string]map[string]any, error) {
	var objs map[string]map[string]any
	if err := json.Unmarshal(body, &objs); err != nil {
		var empty []any
		if json.Unmarshal(body, &empty) == nil && len(empty) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected json.php response: %w", err)
	}
	return objs, nil
}

// DetailsByMD5 devuelve el registro de fichero y su primera edición relacionada.
func (c *Client) DetailsByMD5(ctx context.Context, md5 string) (map[string]any, map[string]any, error) {
	body, _, err := c.get(ctx, "/json.php", url.Values{"object": {"f"}, "md5": {md5}, "addkeys": {"*"}})
	if err != nil {
		return nil, nil, err
	}
	files, err := decodeObjects(body)
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("no file found for md5 %s", md5)
	}
	var file map[string]any
	for id, f := range files {
		f["file_id"] = id
		file = f
		break
	}
	var edition map[string]any
	if eds, ok := file["editions"].(map[string]any); ok {
		for _, e := range eds {
			em, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if eid, _ := em["e_id"].(string); eid != "" {
				edition, _ = c.DetailsByID(ctx, "e", eid) // best-effort
				break
			}
		}
	}
	return file, edition, nil
}

// DetailsByID devuelve un registro por id. object: "e" (edición) o "f" (fichero).
func (c *Client) DetailsByID(ctx context.Context, object, id string) (map[string]any, error) {
	if object != "e" && object != "f" {
		return nil, fmt.Errorf("object must be \"e\" or \"f\", got %q", object)
	}
	q := url.Values{"object": {object}, "ids": {id}}
	if object == "f" {
		q.Set("addkeys", "*")
	}
	body, _, err := c.get(ctx, "/json.php", q)
	if err != nil {
		return nil, err
	}
	objs, err := decodeObjects(body)
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, fmt.Errorf("no %s record found with id %s", object, id)
	}
	for oid, o := range objs {
		o["id"] = oid
		return o, nil
	}
	return nil, nil // inalcanzable
}
```

- [ ] **Step 4: Verificar que pasa** — Run: `go test ./internal/libgen/ -run TestDetails -v` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/libgen
git commit -m "feat: record details via libgen json.php API"
```

---

### Task 8: descarga — extracción de key y streaming a disco

**Files:**
- Create: `internal/libgen/download.go`
- Test: `internal/libgen/download_test.go`

**Interfaces:**
- Consumes: `(*Client).get`, `c.dl`, `c.limiter` de Task 5
- Produces:
  - `libgen.ExtractGetLink(body []byte) (string, error)` — enlace relativo `get.php?md5=…&key=…`
  - `(*Client) ResolveGetURL(ctx context.Context, md5 string) (fileURL, mirrorBase string, err error)`
  - `(*Client) Download(ctx context.Context, md5, dir, filename string) (*DownloadResult, error)`
  - `libgen.DownloadResult{Path string; SizeBytes int64; OriginalFilename string; Mirror string}` (tags JSON snake_case)

- [ ] **Step 1: Tests que fallan**

```go
package libgen

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractGetLinkFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/ads.html")
	if err != nil {
		t.Fatal(err)
	}
	link, err := ExtractGetLink(body)
	if err != nil {
		t.Fatalf("ExtractGetLink() error = %v", err)
	}
	if !strings.HasPrefix(link, "get.php?md5=87a4ebdaf21fa6cc70009a3dd63194ee&key=") {
		t.Errorf("link = %q", link)
	}
	if strings.Contains(link, "&amp;") {
		t.Errorf("link sin desescapar: %q", link)
	}
}

func TestExtractGetLinkMissing(t *testing.T) {
	if _, err := ExtractGetLink([]byte("<html>no hay enlace</html>")); err == nil {
		t.Fatal("debería fallar sin enlace get.php")
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"a/b\\c:d*e?f\"g<h>i|j.pdf": "a_b_c_d_e_f_g_h_i_j.pdf",
		"  normal.epub  ":           "normal.epub",
		"":                          "download",
		"...":                       "download",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func downloadTestServer(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, r *http.Request) {
		md5 := r.URL.Query().Get("md5")
		fmt.Fprintf(w, `<html><a href="get.php?md5=%s&key=TESTKEY123">GET</a></html>`, md5)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "TESTKEY123" {
			http.Error(w, "bad key", http.StatusForbidden)
			return
		}
		http.Redirect(w, r, srv.URL+"/cdn/file", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/cdn/file", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="Author - Title (2020).pdf"`)
		w.Write(payload)
	})
	srv = httptest.NewServer(mux)
	return srv
}

func TestDownload(t *testing.T) {
	payload := []byte("%PDF-1.4 fake book content")
	srv := downloadTestServer(t, payload)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()
	res, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", dir, "")
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if res.OriginalFilename != "Author - Title (2020).pdf" {
		t.Errorf("OriginalFilename = %q", res.OriginalFilename)
	}
	if res.Path != filepath.Join(dir, "Author - Title (2020).pdf") {
		t.Errorf("Path = %q", res.Path)
	}
	data, err := os.ReadFile(res.Path)
	if err != nil || string(data) != string(payload) {
		t.Errorf("contenido = %q, err = %v", data, err)
	}
	if res.SizeBytes != int64(len(payload)) {
		t.Errorf("SizeBytes = %d, want %d", res.SizeBytes, len(payload))
	}
	// sin ficheros temporales huérfanos
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("quedan %d entradas en dir, esperaba 1", len(entries))
	}
}

func TestDownloadCustomFilename(t *testing.T) {
	srv := downloadTestServer(t, []byte("data"))
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	dir := t.TempDir()
	res, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", dir, "mi libro.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(res.Path) != "mi libro.pdf" {
		t.Errorf("Path = %q", res.Path)
	}
}

func TestDownloadRejectsHTMLResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ads.php", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a href="get.php?md5=87a4ebdaf21fa6cc70009a3dd63194ee&key=K1">x</a>`)
	})
	mux.HandleFunc("/get.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html>error page</html>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestClient(staticMirrors{srv.URL})
	if _, err := c.Download(context.Background(), "87a4ebdaf21fa6cc70009a3dd63194ee", t.TempDir(), ""); err == nil {
		t.Fatal("respuesta HTML debería fallar")
	}
}
```

- [ ] **Step 2: Verificar que falla** — Run: `go test ./internal/libgen/ -run 'TestExtract|TestSanitize|TestDownload' -v` — Expected: FAIL.

- [ ] **Step 3: Implementación**

```go
package libgen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"
)

var getLinkRe = regexp.MustCompile(`get\.php\?md5=[0-9a-fA-F]{32}&(?:amp;)?key=[A-Za-z0-9]+`)

// ExtractGetLink localiza el enlace get.php?md5=…&key=… dentro de la página ads.php.
func ExtractGetLink(body []byte) (string, error) {
	m := getLinkRe.Find(body)
	if m == nil {
		return "", fmt.Errorf("%w: no get.php key link in ads page", ErrLayoutChanged)
	}
	return xhtml.UnescapeString(string(m)), nil
}

// ResolveGetURL obtiene la URL directa de descarga (con key fresca) para un md5.
func (c *Client) ResolveGetURL(ctx context.Context, md5 string) (string, string, error) {
	body, base, err := c.get(ctx, "/ads.php", url.Values{"md5": {md5}})
	if err != nil {
		return "", "", err
	}
	link, err := ExtractGetLink(body)
	if err != nil {
		return "", "", err
	}
	return base + "/" + link, base, nil
}

type DownloadResult struct {
	Path             string `json:"path"`
	SizeBytes        int64  `json:"size_bytes"`
	OriginalFilename string `json:"original_filename,omitempty"`
	Mirror           string `json:"mirror"`
}

// Download descarga el fichero md5 a dir. Si filename está vacío usa el nombre
// que anuncia el CDN (content-disposition), saneado.
func (c *Client) Download(ctx context.Context, md5, dir, filename string) (*DownloadResult, error) {
	fileURL, base, err := c.ResolveGetURL(ctx, md5)
	if err != nil {
		return nil, err
	}
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.dl.Do(req) // c.dl sin timeout global: descargas largas, gobierna ctx
	if err != nil {
		return nil, fmt.Errorf("downloading file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: status %d from %s", resp.StatusCode, base)
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		return nil, errors.New("mirror returned an HTML page instead of the file (key expired or download blocked)")
	}
	original := filenameFromDisposition(resp.Header.Get("Content-Disposition"))
	name := filename
	if name == "" {
		name = original
	}
	if name == "" {
		name = md5
	}
	name = sanitizeFilename(name)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating download dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".libgen-mcp-*")
	if err != nil {
		return nil, err
	}
	n, copyErr := io.Copy(tmp, resp.Body)
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(tmp.Name())
		return nil, fmt.Errorf("writing file: %w", errors.Join(copyErr, closeErr))
	}
	if resp.ContentLength > 0 && n != resp.ContentLength {
		os.Remove(tmp.Name())
		return nil, fmt.Errorf("truncated download: got %d of %d bytes", n, resp.ContentLength)
	}
	dest := filepath.Join(dir, name)
	if err := os.Rename(tmp.Name(), dest); err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	return &DownloadResult{Path: dest, SizeBytes: n, OriginalFilename: original, Mirror: base}, nil
}

func filenameFromDisposition(header string) string {
	if header == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(header)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(params["filename"])
}

func sanitizeFilename(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, s)
	s = strings.Trim(s, " .")
	if runes := []rune(s); len(runes) > 200 {
		s = string(runes[:200])
	}
	if s == "" {
		return "download"
	}
	return s
}
```

- [ ] **Step 4: Verificar que pasa** — Run: `go test ./internal/libgen/ -v` — Expected: PASS (todo el paquete).

- [ ] **Step 5: Commit**

```bash
git add internal/libgen
git commit -m "feat: file download via ads.php key resolution"
```

---

### Task 9: `internal/tools` — las 3 tools MCP

**Files:**
- Create: `internal/tools/tools.go`
- Test: `internal/tools/tools_test.go`

**Interfaces:**
- Consumes: `libgen.Client` (Search, DetailsByMD5, DetailsByID, Download), `config.Config`
- Produces: `tools.Register(server *mcp.Server, client *libgen.Client, cfg *config.Config)` — registra `search`, `get_details`, `download`

- [ ] **Step 1: Tests que fallan**

```go
package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
)

type staticMirrors []string

func (s staticMirrors) Mirrors(context.Context) []string { return s }

// newSession levanta servidor MCP + cliente in-memory con un mirror httptest
// que sirve las fixtures del paquete libgen.
func newSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	searchHTML, err := os.ReadFile("../libgen/testdata/search_books.html")
	if err != nil {
		t.Fatal(err)
	}
	fileJSON, _ := os.ReadFile("../libgen/testdata/file_by_md5.json")
	editionJSON, _ := os.ReadFile("../libgen/testdata/edition.json")
	mux := http.NewServeMux()
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, r *http.Request) { w.Write(searchHTML) })
	mux.HandleFunc("/json.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("object") == "f" {
			w.Write(fileJSON)
		} else {
			w.Write(editionJSON)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := libgen.New(staticMirrors{srv.URL}, 5*time.Second)
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second}
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	Register(server, client, cfg)

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := mcpClient.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func TestToolsRegistered(t *testing.T) {
	session := newSession(t)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"search", "get_details", "download"} {
		if !names[want] {
			t.Errorf("falta la tool %q; registradas: %v", want, names)
		}
	}
	if len(res.Tools) != 3 {
		t.Errorf("hay %d tools, esperaba 3", len(res.Tools))
	}
}

func TestSearchTool(t *testing.T) {
	session := newSession(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "golang", "topics": []string{"nonfiction"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	data, _ := json.Marshal(res.StructuredContent)
	var out struct {
		Results []struct {
			MD5   string `json:"md5"`
			Title string `json:"title"`
		} `json:"results"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) == 0 || out.Results[0].MD5 == "" {
		t.Errorf("resultados inesperados: %+v", out)
	}
}

func TestSearchToolBadTopic(t *testing.T) {
	session := newSession(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search",
		Arguments: map[string]any{"query": "x", "topics": []string{"cooking"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("topic inválido debería devolver tool error")
	}
}

func TestGetDetailsTool(t *testing.T) {
	session := newSession(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_details",
		Arguments: map[string]any{"md5": "87a4ebdaf21fa6cc70009a3dd63194ee"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	data, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(data), "87a4ebdaf21fa6cc70009a3dd63194ee") {
		t.Errorf("salida sin md5: %s", data)
	}
}

func TestGetDetailsToolValidation(t *testing.T) {
	session := newSession(t)
	for _, args := range []map[string]any{
		{},
		{"md5": "87a4ebdaf21fa6cc70009a3dd63194ee", "id": "1"},
	} {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_details", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Errorf("args %v deberían devolver tool error", args)
		}
	}
}
```

(El happy-path de `download` ya está cubierto a nivel de cliente en Task 8; aquí solo se registra y valida entrada.)

- [ ] **Step 2: Verificar que falla** — Run: `go test ./internal/tools/ -v` — Expected: FAIL.

- [ ] **Step 3: Implementación**

```go
// Package tools registra las tools MCP del servidor: search, get_details y download.
package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
)

var md5Re = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

const searchDescription = `Search the Library Genesis catalog. Returns file results with
metadata, md5 hash and download options. Allowed values:
- topics: nonfiction, fiction, articles, magazines, comics, standards, fiction_rus (omit = all collections)
- search_in: title, author, series, year, publisher, isbn (omit = all fields)
- results_per_page: 25, 50, 100 (default 25)
- order: id, time_added, title, author, year, size; order_mode: asc, desc
Use get_details with a result md5 for full metadata, and download to fetch the file.`

type SearchInput struct {
	Query          string   `json:"query" jsonschema:"search text,required"`
	Topics         []string `json:"topics,omitempty" jsonschema:"collections to search: nonfiction fiction articles magazines comics standards fiction_rus (omit for all)"`
	SearchIn       []string `json:"search_in,omitempty" jsonschema:"fields to match: title author series year publisher isbn (omit for all)"`
	ResultsPerPage int      `json:"results_per_page,omitempty" jsonschema:"results per page: 25 50 or 100"`
	Page           int      `json:"page,omitempty" jsonschema:"result page starting at 1"`
	Order          string   `json:"order,omitempty" jsonschema:"sort by: id time_added title author year size"`
	OrderMode      string   `json:"order_mode,omitempty" jsonschema:"asc or desc"`
}

type SearchOutput struct {
	Results        []libgen.Result `json:"results"`
	Page           int             `json:"page"`
	ResultsPerPage int             `json:"results_per_page"`
	TotalFiles     string          `json:"total_files,omitempty"`
	HasMore        bool            `json:"has_more"`
	Mirror         string          `json:"mirror"`
}

type DetailsInput struct {
	MD5    string `json:"md5,omitempty" jsonschema:"file md5 hash from a search result (use md5 OR id, not both)"`
	ID     string `json:"id,omitempty" jsonschema:"edition or file id (use md5 OR id, not both)"`
	Object string `json:"object,omitempty" jsonschema:"with id: edition (default) or file"`
}

type DetailsOutput struct {
	File    map[string]any `json:"file,omitempty"`
	Edition map[string]any `json:"edition,omitempty"`
}

type DownloadInput struct {
	MD5      string `json:"md5" jsonschema:"file md5 hash from a search result,required"`
	Path     string `json:"path,omitempty" jsonschema:"destination directory (default: LIBGEN_MCP_DOWNLOAD_DIR or ~/Downloads)"`
	Filename string `json:"filename,omitempty" jsonschema:"destination filename (default: name announced by the mirror)"`
}

func Register(server *mcp.Server, client *libgen.Client, cfg *config.Config) {
	truthy, falsy := true, false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search",
		Title:       "Search Library Genesis",
		Description: searchDescription,
		Annotations: &mcp.ToolAnnotations{Title: "Search Library Genesis", ReadOnlyHint: true, OpenWorldHint: &truthy},
	}, searchHandler(client))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_details",
		Title:       "Get record details",
		Description: "Full metadata for a Library Genesis record (description, identifiers, DOI, cover, related edition) via its JSON API. Look up by md5 (returns file + related edition) or by edition/file id.",
		Annotations: &mcp.ToolAnnotations{Title: "Get record details", ReadOnlyHint: true, OpenWorldHint: &truthy},
	}, detailsHandler(client))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "download",
		Title:       "Download file",
		Description: "Download a file by md5 to a local directory, resolving the libgen mirror download chain (ads.php key + CDN redirect). Returns the saved path and size.",
		Annotations: &mcp.ToolAnnotations{Title: "Download file", DestructiveHint: &falsy, IdempotentHint: true, OpenWorldHint: &truthy},
	}, downloadHandler(client, cfg))
}

func searchHandler(c *libgen.Client) mcp.ToolHandlerFor[SearchInput, SearchOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
		var zero SearchOutput
		params := libgen.SearchParams{
			Query:          in.Query,
			Topics:         in.Topics,
			SearchIn:       in.SearchIn,
			ResultsPerPage: in.ResultsPerPage,
			Page:           in.Page,
			Order:          in.Order,
			OrderMode:      in.OrderMode,
		}
		page, mirror, err := c.Search(ctx, params)
		if err != nil {
			return nil, zero, err
		}
		per := in.ResultsPerPage
		if per == 0 {
			per = 25
		}
		curPage := in.Page
		if curPage == 0 {
			curPage = 1
		}
		out := SearchOutput{
			Results:        page.Results,
			Page:           curPage,
			ResultsPerPage: per,
			TotalFiles:     page.TotalFiles,
			HasMore:        len(page.Results) >= per,
			Mirror:         mirror,
		}
		if out.Results == nil {
			out.Results = []libgen.Result{}
		}
		return nil, out, nil
	}
}

func detailsHandler(c *libgen.Client) mcp.ToolHandlerFor[DetailsInput, DetailsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DetailsInput) (*mcp.CallToolResult, DetailsOutput, error) {
		var zero DetailsOutput
		switch {
		case in.MD5 != "" && in.ID != "":
			return nil, zero, fmt.Errorf("provide md5 or id, not both")
		case in.MD5 != "":
			if !md5Re.MatchString(in.MD5) {
				return nil, zero, fmt.Errorf("md5 must be a 32-char hex string")
			}
			file, edition, err := c.DetailsByMD5(ctx, strings.ToLower(in.MD5))
			if err != nil {
				return nil, zero, err
			}
			return nil, DetailsOutput{File: file, Edition: edition}, nil
		case in.ID != "":
			object := "e"
			switch in.Object {
			case "", "edition":
			case "file":
				object = "f"
			default:
				return nil, zero, fmt.Errorf("object must be edition or file, got %q", in.Object)
			}
			rec, err := c.DetailsByID(ctx, object, in.ID)
			if err != nil {
				return nil, zero, err
			}
			if object == "f" {
				return nil, DetailsOutput{File: rec}, nil
			}
			return nil, DetailsOutput{Edition: rec}, nil
		default:
			return nil, zero, fmt.Errorf("provide md5 or id")
		}
	}
}

func downloadHandler(c *libgen.Client, cfg *config.Config) mcp.ToolHandlerFor[DownloadInput, libgen.DownloadResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DownloadInput) (*mcp.CallToolResult, libgen.DownloadResult, error) {
		var zero libgen.DownloadResult
		if !md5Re.MatchString(in.MD5) {
			return nil, zero, fmt.Errorf("md5 must be a 32-char hex string")
		}
		dir := in.Path
		if dir == "" {
			dir = cfg.DownloadDir
		}
		res, err := c.Download(ctx, strings.ToLower(in.MD5), dir, in.Filename)
		if err != nil {
			return nil, zero, err
		}
		return nil, *res, nil
	}
}
```

Nota: si `mcp.ToolAnnotations` en la versión instalada del SDK usa `ReadOnlyHint bool` vs puntero u otros nombres, ajustar a la firma real (`go doc github.com/modelcontextprotocol/go-sdk/mcp.ToolAnnotations`). Igual con `ToolHandlerFor`.

- [ ] **Step 4: Verificar que pasa** — Run: `go test ./internal/tools/ -v` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tools
git commit -m "feat: MCP tools search, get_details and download"
```

---

### Task 10: `cmd/server` — binario principal

**Files:**
- Create: `cmd/server/main.go`

**Interfaces:**
- Consumes: `config.Load`, `mirrors.NewManager`, `libgen.New`, `tools.Register`
- Produces: binario `libgen-mcp` (stdio por defecto, `--http addr` para streamable HTTP)

- [ ] **Step 1: Implementación**

```go
// libgen-mcp es un servidor MCP para buscar y descargar de Library Genesis.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
	"github.com/jmrplens/libgen-mcp/internal/tools"
)

const version = "0.1.0"

func main() {
	httpAddr := flag.String("http", "", "serve streamable HTTP on this address (e.g. :8080) instead of stdio")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if err := run(*httpAddr); err != nil {
		log.Fatal(err)
	}
}

func run(httpAddr string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		return err
	}
	client := libgen.New(mgr, cfg.Timeout)
	server := mcp.NewServer(&mcp.Implementation{Name: "libgen-mcp", Version: version}, nil)
	tools.Register(server, client, cfg)

	if httpAddr != "" {
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
		log.Printf("libgen-mcp %s listening on %s (streamable HTTP)", version, httpAddr)
		return http.ListenAndServe(httpAddr, handler)
	}
	fmt.Fprintf(os.Stderr, "libgen-mcp %s serving on stdio\n", version)
	return server.Run(context.Background(), &mcp.StdioTransport{})
}
```

- [ ] **Step 2: Verificar build y arranque**

```bash
go build ./... && go run ./cmd/server --version
```

Expected: imprime `0.1.0`.

Smoke test stdio (initialize + tools/list por JSON-RPC):

```bash
printf '%s\n' \
 '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
 '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
 '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | go run ./cmd/server 2>/dev/null
```

Expected: dos respuestas JSON; la segunda lista las tools `search`, `get_details`, `download`.

- [ ] **Step 3: Commit**

```bash
git add cmd/server
git commit -m "feat: server binary with stdio and streamable HTTP transports"
```

---

### Task 11: `cmd/probe` — diagnóstico contra el sitio real

**Files:**
- Create: `cmd/probe/main.go`

**Interfaces:**
- Consumes: `mirrors.NewManager`, `libgen.New`, `(*Client).Search`, `(*Client).DetailsByMD5`, `(*Client).ResolveGetURL`
- Produces: binario `probe`; exit code 0 si todo OK, 1 si algo falla

- [ ] **Step 1: Implementación**

```go
// probe verifica contra los mirrors reales que todas las rutas que usa
// libgen-mcp siguen funcionando (búsqueda por topic, API JSON, cadena de
// descarga). Uso: go run ./cmd/probe [-mirror https://libgen.li]
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
)

func main() {
	mirror := flag.String("mirror", "", "force a specific mirror base URL")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		fmt.Println("[FAIL] config:", err)
		os.Exit(1)
	}
	if *mirror != "" {
		cfg.Mirror = *mirror
	}
	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		fmt.Println("[FAIL] mirrors manager:", err)
		os.Exit(1)
	}
	client := libgen.New(mgr, cfg.Timeout)

	failed := false
	report := func(name string, err error, okMsg string) {
		if err != nil {
			failed = true
			fmt.Printf("[FAIL] %s: %v\n", name, err)
			return
		}
		fmt.Printf("[OK]   %s: %s\n", name, okMsg)
	}

	list := mgr.Mirrors(ctx)
	report("mirrors", nil, fmt.Sprintf("%d discovered, preferred %s", len(list), list[0]))

	searches := []struct{ topic, query string }{
		{"nonfiction", "golang"},
		{"fiction", "dune"},
		{"articles", "neural network"},
		{"magazines", "science"},
		{"comics", "batman"},
		{"standards", "safety"},
		{"fiction_rus", "мастер"},
	}
	var sampleMD5 string
	for _, s := range searches {
		page, mirrorUsed, err := client.Search(ctx, libgen.SearchParams{Query: s.query, Topics: []string{s.topic}})
		msg := ""
		if err == nil {
			msg = fmt.Sprintf("%d results (total %s) via %s", len(page.Results), page.TotalFiles, mirrorUsed)
			if sampleMD5 == "" && len(page.Results) > 0 {
				sampleMD5 = page.Results[0].MD5
			}
			if len(page.Results) == 0 {
				err = fmt.Errorf("0 results for %q (query too narrow or parser broken)", s.query)
			}
		}
		report("search "+s.topic, err, msg)
	}

	if sampleMD5 == "" {
		fmt.Println("[FAIL] no sample md5 available, skipping details/download checks")
		os.Exit(1)
	}

	file, edition, err := client.DetailsByMD5(ctx, sampleMD5)
	msg := ""
	if err == nil {
		msg = fmt.Sprintf("file fields=%d, edition present=%v", len(file), edition != nil)
	}
	report("json.php details", err, msg)

	getURL, base, err := client.ResolveGetURL(ctx, sampleMD5)
	report("ads.php key", err, fmt.Sprintf("resolved via %s", base))

	if err == nil {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
		req.Header.Set("Range", "bytes=0-0")
		resp, err := http.DefaultClient.Do(req)
		msg := ""
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("status %d", resp.StatusCode)
			} else {
				msg = fmt.Sprintf("status %d, content-disposition present=%v",
					resp.StatusCode, resp.Header.Get("Content-Disposition") != "")
			}
		}
		report("CDN download", err, msg)
	}

	if failed {
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Verificar contra el sitio real**

Run: `go run ./cmd/probe`
Expected: todas las líneas `[OK]` (con red disponible). Si algún topic falla por query concreta, ajustar la query de ese topic — no es fallo del código.

- [ ] **Step 3: Commit**

```bash
git add cmd/probe
git commit -m "feat: probe CLI to diagnose live mirror routes"
```

---

### Task 12: test e2e opt-in, README y verificación final

**Files:**
- Create: `internal/libgen/e2e_test.go`, `README.md`

**Interfaces:**
- Consumes: todo lo anterior
- Produces: repo documentado y verificado

- [ ] **Step 1: Test e2e con build tag**

```go
//go:build e2e

package libgen

import (
	"context"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
)

// TestE2ESearchRealSite valida contra la red real que el HTML del sitio sigue
// siendo parseable. Ejecutar con: go test -tags e2e ./internal/libgen/ -run E2E -v
func TestE2ESearchRealSite(t *testing.T) {
	cfg := &config.Config{Timeout: 30 * time.Second}
	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := New(mgr, cfg.Timeout)
	page, mirror, err := c.Search(context.Background(), SearchParams{Query: "golang", Topics: []string{"nonfiction"}})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	t.Logf("mirror=%s results=%d total=%s", mirror, len(page.Results), page.TotalFiles)
	if len(page.Results) == 0 {
		t.Fatal("0 resultados en el sitio real: HTML cambiado o bloqueo")
	}
}
```

- [ ] **Step 2: Verificar** — Run: `go test -tags e2e ./internal/libgen/ -run E2E -v` — Expected: PASS con red; y `go test ./...` NO lo ejecuta (sin tag).

- [ ] **Step 3: README.md**

Contenido mínimo (redactar en inglés, como gitlab-mcp-server):
- Qué es: MCP server for searching and downloading from Library Genesis (libgen.li mirror family).
- Instalación: `go install github.com/jmrplens/libgen-mcp/cmd/server@latest` (binario `server`; mencionar `go build -o libgen-mcp ./cmd/server` como alternativa).
- Configuración cliente MCP (ejemplo para Claude Code):

```json
{
  "mcpServers": {
    "libgen": {
      "command": "libgen-mcp"
    }
  }
}
```

- Tabla de las 3 tools con sus parámetros (copiar de los schemas de la Task 9).
- Tabla de env vars (`LIBGEN_MIRROR`, `LIBGEN_MCP_DOWNLOAD_DIR`, `LIBGEN_MCP_TIMEOUT`).
- Sección "Mirrors": auto-descubrimiento desde shadowlibraries + caché 24h + failover; `--http` para transporte HTTP.
- Sección "Maintenance": `go run ./cmd/probe` para diagnosticar cambios del sitio; `go test -tags e2e ./...` para e2e.
- Nota breve de uso responsable (respetar la legislación local de propiedad intelectual).

- [ ] **Step 4: Verificación final completa**

```bash
gofmt -l . && go vet ./... && go test ./... && go build ./...
```

Expected: `gofmt` sin salida, todo lo demás OK/PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/libgen/e2e_test.go README.md
git commit -m "docs: README and opt-in e2e test"
```
