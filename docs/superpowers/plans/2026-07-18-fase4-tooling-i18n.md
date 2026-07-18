# Fase 4 — Tooling de documentación + inglés en todo el código — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Adoptar dos utilidades del proyecto hermano (`format_md_tables` para normalizar tablas markdown, `godoc_tool` para auditar/rellenar godoc), traducir TODOS los comentarios/godoc del código de español a inglés, y añadir gates de CI para que el código quede 100% documentado en inglés.

**Architecture:** Se portan `cmd/format_md_tables` + `cmd/internal/docgen` y `cmd/godoc_tool` desde `../gitlab-mcp-server`, adaptando el module path a `github.com/jmrplens/libgen-mcp` y comentarios a inglés. Makefile + CI wiring. Traducción de comentarios existentes.

**Tech Stack:** Go stdlib (go/ast, go/doc, go/parser). Sin dependencias nuevas.

## Global Constraints

- **Se ejecuta DESPUÉS de la Fase 1** (código estable) y ANTES de Fase 2/3.
- **TODO godoc y comentarios de código en INGLÉS** (solo el sitio Astro será bilingüe, Fase 3).
- Módulo `github.com/jmrplens/libgen-mcp`. `golangci-lint run --build-tags e2e ./...` = 0, `go test ./...` verde, `gofmt`/`vet` limpios antes de cada commit.
- Fuentes a portar (rutas absolutas): `/Users/jmrplens/GIT/gitlab-mcp-server/cmd/format_md_tables/` (main.go + main_test.go), `/Users/jmrplens/GIT/gitlab-mcp-server/cmd/internal/docgen/` (856 LOC: doc.go, section.go, table.go, markdown_tables.go + tests), `/Users/jmrplens/GIT/gitlab-mcp-server/cmd/godoc_tool/` (2201 LOC: main.go, audit.go, fix.go + tests).
- Commits path-scoped con trailer `Claude-Session: https://claude.ai/code/session_01U7oY5WU1y2cFrJz9TkAfsQ`.

---

### Task 1: Portar `cmd/internal/docgen` + `cmd/format_md_tables`

**Files:** Create `cmd/internal/docgen/*.go` (copiados), `cmd/format_md_tables/main.go` + `main_test.go` (copiados). Modify `Makefile`, `.github/workflows/ci.yml`.

- [ ] **Step 1** — Copiar los ficheros de docgen y format_md_tables; reemplazar el import path `github.com/jmrplens/gitlab-mcp-server/v2` → `github.com/jmrplens/libgen-mcp` en todos. Los comentarios ya están en inglés (verificar). Ajustar el default de paths si hace falta (`README.md`, `docs`).
- [ ] **Step 2** — `go build ./cmd/format_md_tables/`; `go test ./cmd/format_md_tables/ ./cmd/internal/docgen/` verde (los tests vienen con la utilidad); `golangci-lint run --build-tags e2e ./...` = 0.
- [ ] **Step 3** — `go run ./cmd/format_md_tables/` (formatea README/docs actuales) y verificar que `--check` sale limpio después.
- [ ] **Step 4** — Makefile: target `format-md-tables:` (`go run ./cmd/format_md_tables/`) y `check-md-tables:` (`go run ./cmd/format_md_tables/ --check`). CI (`ci.yml`, job analyze-md o nuevo): añadir paso `go run ./cmd/format_md_tables/ --check`.
- [ ] **Step 5: Commit** — `-- cmd/internal/docgen cmd/format_md_tables Makefile .github/workflows/ci.yml`.

---

### Task 2: Portar `cmd/godoc_tool`

**Files:** Create `cmd/godoc_tool/*.go` (copiados: main.go, audit.go, fix.go + tests). Modify `Makefile`.

- [ ] **Step 1** — Copiar; reemplazar module path a `github.com/jmrplens/libgen-mcp`; comentarios en inglés (ya lo están). Subcomandos `audit` (`--format markdown|json`, `--output`, `--fail-on-findings`, `--include-tests`) y `fix` (`--dry-run <paths>`).
- [ ] **Step 2** — `go build ./cmd/godoc_tool/`; `go test ./cmd/godoc_tool/` verde; `golangci-lint` 0.
- [ ] **Step 3** — Makefile: `godoc-audit:` (`go run ./cmd/godoc_tool/ audit --format=markdown`) y `godoc-check:` (`go run ./cmd/godoc_tool/ audit --fail-on-findings`).
- [ ] **Step 4: Commit** — `-- cmd/godoc_tool Makefile`.

---

### Task 3: Traducir comentarios español→inglés + rellenar godoc + gate CI

**Files:** Modify todos los `.go` de `internal/` y `cmd/` con comentarios en español; Modify `.github/workflows/ci.yml`.

- [ ] **Step 1** — `go run ./cmd/godoc_tool/ audit --include-tests --format=markdown` para inventariar godoc faltante/malformado. Además localizar comentarios en español: `grep -rnE '// .*(descarga|fichero|petición|cliente|búsqueda|error de|servidor|intento|mirror caído|niveles|el usuario)' internal cmd` y revisión manual fichero por fichero.
- [ ] **Step 2** — Traducir a inglés TODOS los comentarios de línea y godoc en `internal/**` y `cmd/**` (código y tests), preservando el significado. Ejecutar `go run ./cmd/godoc_tool/ fix` (o `fix --dry-run` primero) para rellenar godoc faltante en exportados; revisar y pulir el texto generado a inglés claro.
- [ ] **Step 3** — `go run ./cmd/godoc_tool/ audit --fail-on-findings` sale limpio (0 findings) para el código no-test; decidir si incluir tests en el gate (`--include-tests`) según cuántos findings queden razonables.
- [ ] **Step 4** — `go test ./...` verde; `golangci-lint run --build-tags e2e ./...` = 0 (`misspell` no debe encontrar español); `gofmt`/`vet` limpios.
- [ ] **Step 5** — CI (`ci.yml`): añadir job/paso `godoc` que corre `go run ./cmd/godoc_tool/ audit --fail-on-findings` (el nivel de estrictitud —con o sin `--include-tests`— según Step 3).
- [ ] **Step 6: Commit** — `-- internal cmd .github/workflows/ci.yml` (traducción + gate).

## Self-Review (hecho)
- Adopta format_md_tables + godoc_tool (descarta audit_doc_coverage, específico de gitlab). Traduce el código a inglés y lo verifica con godoc_tool + misspell. Gates de CI para tablas markdown y cobertura godoc. Sin placeholders (las utilidades traen sus tests). Depende de Fase 1 (código estable).
