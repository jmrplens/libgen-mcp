# Fase 5 — Descargas multi-fuente + mejoras de búsqueda — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Extender `download` a múltiples fuentes con fallback (libgen + randombook para libros; Unpaywall + Sci-Hub para artículos por DOI), exponer el cap de paginación de libgen (~2000 alcanzables de un total mayor) en `search`, y dar un nombre de fichero limpio por defecto desde los metadatos. Todo HTTP puro, sin navegador ni dependencias pesadas.

**Architecture:** Nueva abstracción `DownloadSource` (interfaz `Name`/`Supports`/`Resolve`) + una cadena de fallback. El `Download` actual (fetch → stream → resume/Range → progreso → size-cap → escritura atómica) ya es agnóstico de fuente salvo la verificación MD5; se generaliza para (1) recorrer la cadena de fuentes, (2) alimentar la URL resuelta al pipeline existente, (3) hacer la verificación MD5 **condicional** (`Resolved.VerifyMD5`: true en libgen/randombook keyed-by-md5; false en artículos keyed-by-DOI). Se deja un *seam* para una futura fuente con navegador (opt-in), sin implementarla.

**Tech Stack:** Go stdlib (`net/http`, `encoding/json`, `crypto/md5`) + `golang.org/x/net/html` (ya vendorizado). SIN chromedp/go-rod. Extiende el diseño aprobado `docs/superpowers/specs/2026-07-18-libgen-mcp-robustez-tests-docs-design.md`.

## Global Constraints

- **Fase 1 y Fase 4 completas** (esta fase construye sobre el download manager y el código ya en inglés).
- **TODO comentario/godoc en INGLÉS.** Strict `.golangci.yml`: `golangci-lint run --build-tags e2e ./...` = 0; `golangci-lint fmt`. godoc en exportados (gate godoc en CI). `go test ./... -race` verde; `govulncheck` limpio.
- Tests unit NUNCA tocan la red: fixtures + httptest. Fuentes externas se testean con fixtures capturadas (JSON de Unpaywall/randombook, HTML de sci-hub) servidas por httptest.
- Fuentes HTTP puro; ninguna dependencia nueva. Sci-Hub y AA NO usan navegador (AA se descarta; navegador se deja como seam sin implementar).
- Config nueva (opcional, prefijo `LIBGEN_MCP_`): `UNPAYWALL_EMAIL` (default `mail@jmrp.io`), `SCIHUB_HOSTS` (lista ordenada, default `sci-hub.ee,sci-hub.se,sci-hub.st,sci-hub.ru,sci-hub.wf`), `SOURCES` (orden/enable para libros, default `libgen,randombook`; artículos `unpaywall,scihub`). Validadas en `config.Validate()`.
- Commits path-scoped con trailer `Claude-Session: https://claude.ai/code/session_01U7oY5WU1y2cFrJz9TkAfsQ`.

---

### Task 1: Cap de paginación en `search`

**Files:** Modify `internal/libgen/search.go`, `internal/libgen/search_test.go`, `internal/tools/tools.go`, `internal/tools/tools_test.go`.

**Interfaces:** `SearchPage` gana `Reachable int` y `Truncated bool`. Nuevo parser `paginatorReach(doc) int` que extrae `totalPages*perPage` del init JS `new Paginator("paginator_example_top", <totalPages>, <perPage>, ...)` (regex `new Paginator\("paginator_example_top",\s*(\d+),\s*(\d+),`). `Truncated = TotalFiles(int) > Reachable`.

- [ ] **Step 1: Tests** — `TestParsePaginatorReach` sobre `search_books.html` (la fixture "golang": Paginator con 6 páginas × 25 = 150 reachable; total_files 135 → no truncated). Añadir/usar una fixture con total grande (capturar `?req=physics&topics[]=l&res=100` que da ~26000/2000) para `Truncated==true`, `Reachable==2000`. En tools: `SearchOutput` expone `total_files`, `reachable`, `truncated`; cuando truncated, un campo `hint` "Only the first N of M results are reachable; refine your query (add author/year, use title-only columns, or narrow topics)".
- [ ] **Step 2–4** — fallar, implementar el parser + wiring, pasar. Ajustar literales a las fixtures reales.
- [ ] **Step 5: Commit** — `-- internal/libgen internal/tools`.

---

### Task 2: Nombre de fichero limpio desde metadatos

**Files:** Modify `internal/libgen/download.go`, `internal/libgen/download_test.go`.

**Interfaces:** helper `cleanFileName(author, title, year, ext string) string` → `sanitize(author) + " - " + sanitize(title) + " (" + year + ")." + ext`, omitiendo piezas vacías (sin año → `Author - Title.ext`; sin autor → `Title.ext`), colapsando espacios y quitando caracteres ilegales (reutiliza el `sanitizeFilename` existente). El orden de nombres en `Download`: `filename` explícito > `Content-Disposition` del CDN > `cleanFileName(metadata)` > `md5`. La metadata llega vía un struct opcional en los parámetros de `Download` (p. ej. `meta *FileMeta{Author,Title,Year,Ext}`), que la tool rellena desde el resultado/`get_details`.

- [ ] **Step 1: Tests** — `TestCleanFileName` (tabla: todos los campos; sin año; sin autor; caracteres ilegales → saneados; espacios colapsados). `TestDownloadUsesCleanNameWhenNoDisposition`: CDN sin `Content-Disposition`, `meta` con autor/título/año → el fichero final se llama `Author - Title (Year).ext`.
- [ ] **Step 2–4** — fallar, implementar, pasar.
- [ ] **Step 5: Commit** — `-- internal/libgen`.

---

### Task 3: Abstracción `DownloadSource` + generalizar `Download`

**Files:** Create `internal/libgen/source.go`, `internal/libgen/source_test.go`; Modify `internal/libgen/download.go`, `internal/libgen/download_test.go`.

**Interfaces:**
```go
type Item struct { MD5, DOI string; Meta *FileMeta }
type Resolved struct { FileURL string; Header http.Header; VerifyMD5 bool; Ext string }
type DownloadSource interface {
    Name() string
    Supports(it Item) bool
    Resolve(ctx context.Context, it Item) (Resolved, error)
}
```
El `Download` actual se refactoriza: la lógica libgen (ads.php→get.php→CDN) pasa a un `libgenSource` que implementa `Resolve` (devuelve la URL del CDN + `VerifyMD5:true`). `Download` recorre la cadena de fuentes aplicable al `Item`, llama `Resolve`, y pasa `FileURL`+`Header` al pipeline existente (`fetchFile`/`streamToPartAndVerify`), con la verificación MD5 **condicionada a `Resolved.VerifyMD5`**. Un `Resolve` fallido o un stream que devuelva HTML/verify-error avanza a la siguiente fuente. `DownloadResult` gana `Source string` (qué fuente sirvió).

- [ ] **Step 1: Tests** — refactor con paridad: los tests de descarga existentes deben seguir verdes vía `libgenSource`. Nuevo `TestDownloadSourceChainFallback`: una fuente stub que falla en `Resolve` y otra que resuelve a un CDN httptest con el fichero → la descarga usa la segunda, `Source` == su nombre. `TestVerifyMD5Conditional`: una fuente con `VerifyMD5:false` sirve bytes cuyo md5 ≠ Item.MD5 → NO error (no se verifica); con `VerifyMD5:true` → sí verifica (mismatch → error).
- [ ] **Step 2–4** — fallar, refactor, pasar (`-race`).
- [ ] **Step 5: Commit** — `-- internal/libgen`.

---

### Task 4: Fuente Unpaywall (artículos OA por DOI)

**Files:** Create `internal/libgen/source_unpaywall.go`, `internal/libgen/source_unpaywall_test.go`; Create fixture `internal/libgen/testdata/unpaywall.json` + `unpaywall_notoa.json`; Modify `internal/config`.

**Interfaces:** `unpaywallSource{email string; http *http.Client}` implementa `DownloadSource`. `Supports`: `it.DOI != ""`. `Resolve`: `GET https://api.unpaywall.org/v2/<doi>?email=<email>`; si `is_oa` y `best_oa_location.url_for_pdf` presente → `Resolved{FileURL:url_for_pdf, VerifyMD5:false, Ext:"pdf"}`; si `is_oa:false` o sin pdf → error (fall-through). Config: `UnpaywallEmail` (default `mail@jmrp.io`), validado (formato email básico) en `Validate()`.

- [ ] **Step 1: Tests** — capturar (una vez) `unpaywall.json` (un DOI OA, p. ej. un PLoS) y `unpaywall_notoa.json` (`is_oa:false`). `TestUnpaywallResolveOA` (httptest sirve el JSON OA → FileURL == url_for_pdf, VerifyMD5 false). `TestUnpaywallResolveNotOA` (is_oa false → error). `TestUnpaywallSupports` (DOI vacío → false).
- [ ] **Step 2–4** — fallar, implementar, pasar.
- [ ] **Step 5: Commit** — `-- internal/libgen internal/config`.

---

### Task 5: Fuente Sci-Hub (artículos por DOI)

**Files:** Create `internal/libgen/source_scihub.go`, `internal/libgen/source_scihub_test.go`; Create fixture `internal/libgen/testdata/scihub_article.html`; Modify `internal/config`.

**Interfaces:** `scihubSource{hosts []string; http *http.Client}` implementa `DownloadSource`. `Supports`: `it.DOI != ""`. `Resolve`: por cada host en orden, `GET https://<host>/<doi>`; el primero que devuelva HTML de artículo con un `<... id="pdf" src="...">` (iframe/embed) gana. Extraer el `src` (o el `location.href='...pdf'`), desescapar backslashes, normalizar `//` → `https://` → `Resolved{FileURL, VerifyMD5:false, Ext:"pdf", Header: Referer=<host>}`. Si ningún host sirve artículo → error. Config: `ScihubHosts []string` (default lista ordenada), validado (no vacío) en `Validate()`. Extracción dirigida por el `#pdf` del HTML (no reconstruir la URL desde el DOI).

- [ ] **Step 1: Tests** — capturar `scihub_article.html` (de `sci-hub.ee/<doi paywalled>`; si no accesible, construir una fixture representativa con el `<iframe id="pdf" src="https://sci.bban.top/pdf/....pdf#view=FitH">`). `TestScihubExtractPDF` (parsea la fixture → URL correcta, protocolo normalizado). `TestScihubResolveFirstHostWins` (httptest con 2 hosts: el primero devuelve una página sin `#pdf` → se salta; el segundo con `#pdf` → gana). `TestScihubNoArticle` (ningún host con `#pdf` → error).
- [ ] **Step 2–4** — fallar, implementar, pasar.
- [ ] **Step 5: Commit** — `-- internal/libgen internal/config`.

---

### Task 6: Fuente Randombook (libros por md5) — descubridor de mirrors

**Files:** Create `internal/libgen/source_randombook.go`, `internal/libgen/source_randombook_test.go`; Create fixtures `internal/libgen/testdata/randombook_byid.json` + `randombook_links.json`.

**Interfaces:** `randombookSource` implementa `DownloadSource`. `Supports`: `it.MD5 != ""`. `Resolve`: `GET https://randombook.org/api/search/by-id?id=<md5>` → `result.id` (numérico; `result:null` → error "not indexed"); `GET .../api/download/links-by-id?id=<numericId>` → `result.list[]` (hostnames de mirrors libgen frescos). Como los `links[]` son tokens opacos a landing pages (no ficheros directos), esta fuente NO resuelve a un fichero directo: en su lugar, alimenta hostnames de mirror frescos y reintenta la cadena **libgen** contra esos hosts (ads.php→get.php→CDN por md5, `VerifyMD5:true`). Si no aporta mirrors nuevos utilizables → error (fall-through). (Valor medio: rescata cuando la familia libgen.li primaria está caída.)

- [ ] **Step 1: Tests** — fixtures `randombook_byid.json` (`{"result":{"id":"123",...},"isError":false}`) y `randombook_links.json` (`{"result":{"list":["https://libgen.net",...]}}`). `TestRandombookResolvesMirrors` (httptest sirve ambos JSON → devuelve los hostnames; luego intenta libgen contra un mirror httptest que sí sirve el fichero). `TestRandombookNotIndexed` (`result:null` → error). Mantener el diseño simple; si la integración libgen-contra-mirror-fresco es compleja, la fuente puede exponer los mirrors y dejar que la cadena los pruebe.
- [ ] **Step 2–4** — fallar, implementar, pasar.
- [ ] **Step 5: Commit** — `-- internal/libgen`.

---

### Task 7: Tool `download` multi-fuente + `doi` + wiring de config

**Files:** Modify `internal/tools/tools.go`, `internal/tools/tools_test.go`, `internal/libgen/client.go` (construir las fuentes desde config), `internal/libgen/search.go` (asegurar que `Result.DOI` se parsea/expone para scimag).

**Interfaces:** El `download` tool acepta `md5` (opcional si hay doi) y **`doi` (opcional)**; al menos uno requerido. Construye el `Item{MD5,DOI,Meta}` (Meta desde `get_details` cuando falte, para el nombre limpio) y llama `Download`. El `Client` construye la lista de fuentes desde config (`SOURCES`, `SCIHUB_HOSTS`, `UNPAYWALL_EMAIL`): cadena libros `[libgen, randombook]`, cadena artículos `[unpaywall, scihub]`, seleccionadas por `Supports(item)`. `search` ya expone `download_options`; asegurar que los resultados de artículos incluyen el `doi` para que el modelo pueda pasarlo. Documentar en el schema qué fuentes se intentan.

- [ ] **Step 1: Tests** — `TestDownloadToolByDOI` (in-memory session: download con `doi` de un artículo → resuelve vía una fuente httptest). `TestDownloadToolRequiresMD5OrDOI` (ninguno → tool error). `TestDownloadToolMD5Book` (md5 → cadena de libros, sigue funcionando). Actualizar `Result` si hace falta para incluir `doi` (verificar que el parser scimag lo captura; si no, añadirlo con una fixture de artículo).
- [ ] **Step 2–4** — fallar, implementar, pasar (`-race`).
- [ ] **Step 5: Commit** — `-- internal/tools internal/libgen`.

---

### Task 8: Config nuevas vars + validación + verificación final

**Files:** Modify `internal/config/config.go`, `internal/config/config_test.go`; verificación global.

- [ ] **Step 1: Tests** — `Config` gana `UnpaywallEmail string`, `ScihubHosts []string`, `Sources []string` (o el shape decidido). Defaults y `Validate()` (email básico; hosts no vacío; sources conocidas). Tests de defaults + overrides + inválidos.
- [ ] **Step 2–4** — fallar, implementar, pasar.
- [ ] **Step 5** — verificación final Fase 5: `gofmt`/`vet`/`golangci-lint(e2e)`=0/`govulncheck` limpios; `go test -race ./...` verde; `godoc_tool audit --fail-on-findings` limpio; `make cover-check` ≥85%; smoke `tools/list` = 3 tools; un download por md5 (libro) y un resolve por DOI (artículo, contra fixture) end-to-end.
- [ ] **Step 6: Commit** — `-- internal/config` (+ lo que quede).

## Self-Review (hecho)
- Cubre las peticiones del usuario del screenshot: multi-fuente con fallback (libgen+randombook / unpaywall+scihub), cap de paginación, nombre limpio. Sin dependencias nuevas; navegador/AA descartados con seam para el futuro. MD5 condicional por fuente (clave del refactor). Sci-Hub por lista de hosts (absorbe rotación). Sin placeholders (fixtures se capturan como en fases previas; extracción sci-hub dirigida por `#pdf`). Depende de Fases 1/4.
