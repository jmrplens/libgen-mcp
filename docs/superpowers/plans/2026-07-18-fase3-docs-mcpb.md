# Fase 3 — Documentación + sitio + bundle .mcpb — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Documentación completa (README con badges y tabla de botones de instalación, `docs/` markdown, sitio Astro Starlight en `site/` con deploy a Pages), y una extensión `.mcpb` para instalar en Claude Desktop, integrada en el release.

**Architecture:** Todo manuscrito (3 tools). Sitio Starlight monolingüe (inglés). Pages ya activado en el repo (build_type=workflow, https://jmrplens.github.io/libgen-mcp/). El `.mcpb` se empaqueta con la CLI oficial `mcpb pack` desde los binarios de GoReleaser (darwin universal + windows) y se sube al release.

**Tech Stack:** Markdown, Astro `^7` + Starlight `^0.41.3` + astro-mermaid + starlight-links-validator + sharp (pnpm, Node ≥22), `mcpb` CLI, GoReleaser.

## Global Constraints

- **Fases 1 y 2 completas** (documentar el estado final: env de robustez, gestión de descargas, e2e).
- Idioma **inglés** (coherente con README/server.json). Sitio **monolingüe**, sin i18n/SEO pesado/generadores.
- Config base de los deep-links de instalación (docker, sin token): `{"command":"docker","args":["run","-i","--rm","ghcr.io/jmrplens/libgen-mcp:latest"]}`.
- `.mcpb`: `manifest_version` `0.4`, `server.type` `binary`, `user_config` todos opcionales (sin secretos). Validar que su `version` == `VERSION`.
- Markdown pasa `markdownlint-cli2`; sitio compila con `pnpm build` (links-validator sin errores). Commits path-scoped con trailer `Claude-Session`.

---

### Task 1: README ampliado (badges + tabla de botones + secciones)

**Files:** Modify `README.md`.

- [ ] **Step 1** — Badges arriba: GitHub Release, License MIT, Platform (Win/Linux/macOS · amd64/arm64), SonarCloud Quality Gate + Coverage (`https://sonarcloud.io/api/project_badges/measure?project=jmrplens_libgen-mcp&metric=alert_status|coverage`), Go Report Card, Go Reference.
- [ ] **Step 2** — Sección "Install in one click": tabla HTML (estilo `../gitlab-mcp-server/README.md` líneas ~31-75) con filas VS Code, VS Code Insiders (deep-links `insiders.vscode.dev/redirect/mcp/install?name=libgen&config=<url-encoded JSON>`), Cursor (`cursor.com/install-mcp?name=libgen&config=<base64>` + insignia oficial), LM Studio (`lmstudio.ai/install-mcp?...<base64>`), Kiro (`kiro.dev/launch/mcp/add?...`), y Claude Desktop (botón de descarga `releases/latest/download/libgen-mcp.mcpb`). Generar los `config` con el JSON base (url-encode y base64 según cada cliente). Nota: sin token requerido.
- [ ] **Step 3** — Bloque "Claude Code": `claude mcp add libgen -- /usr/local/bin/libgen-mcp` (binario) y variante docker. Secciones: qué es, las 3 tools (tabla de params), tabla de env (incluidas las de Fase 1), robustez (failover/retry/rate/descargas/MD5), enlaces a `docs/` y al sitio, mantenimiento (probe, `make test-e2e`), uso responsable.
- [ ] **Step 4** — `npx markdownlint-cli2 "README.md"` sin errores.
- [ ] **Step 5: Commit** — `-- README.md`.

---

### Task 2: `docs/` markdown

**Files:** Create `docs/README.md`, `docs/getting-started.md`, `docs/configuration.md`, `docs/tools.md`, `docs/architecture.md`, `docs/troubleshooting.md`.

- [ ] **Step 1** — Escribir las 6 páginas: índice (tabla), inicio rápido (instalar + config cliente + primera búsqueda), configuración (todas las env con defaults/rangos), tools (search/get_details/download con input/output/errores), arquitectura (cliente con failover + cadena de descarga, 1 diagrama ` ```mermaid `), troubleshooting (mirror caído, descarga fallida, MD5 mismatch, cómo subir LOG_LEVEL, errores comunes).
- [ ] **Step 2** — `npx markdownlint-cli2 "docs/**/*.md"` sin errores (la config ya ignora `docs/superpowers`).
- [ ] **Step 3: Commit** — `-- docs`.

---

### Task 3: `site/` Astro Starlight — esqueleto

**Files:** Create `site/package.json`, `site/astro.config.mjs`, `site/tsconfig.json`, `site/src/content.config.ts`, `site/src/styles/custom.css`, `site/public/favicon.svg`, `site/src/assets/logo-light.svg`, `site/src/assets/logo-dark.svg`, `site/.gitignore` (node_modules, dist, .astro). Base en `../gitlab-mcp-server/site/` (config recortada, monolingüe).

- [ ] **Step 1** — `package.json` con las versiones del hermano (starlight ^0.41.3, astro ^7, astro-mermaid, starlight-links-validator, sharp), scripts `dev|build|preview`, `packageManager: pnpm@…`. `astro.config.mjs` recortado (~40 líneas): `site: "https://jmrplens.github.io"`, `base: "/libgen-mcp"`, mermaid, links-validator, logo, social github, editLink, `customCss`, sidebar manual (Guía/Referencia). `content.config.ts` boilerplate (colección docs, sin i18n). `custom.css` (color acento). Logos/favicon SVG simples (placeholder propio; no copiar los del hermano).
- [ ] **Step 2** — `cd site && pnpm install && pnpm build` compila sin errores (genera `pnpm-lock.yaml`). Si falta contenido, crear un `index.mdx` mínimo temporal para que compile (se completa en Task 4).
- [ ] **Step 3: Commit** — `-- site` (incluye `pnpm-lock.yaml`, excluye node_modules/dist vía .gitignore).

---

### Task 4: `site/` — páginas de contenido MDX

**Files:** Create `site/src/content/docs/{index.mdx,getting-started.mdx,configuration.mdx,tools.mdx,architecture.mdx,troubleshooting.mdx}`.

- [ ] **Step 1** — `index.mdx` con hero + 3 `LinkCard` (Starlight components). Las otras 5 páginas: adaptar el contenido de `docs/*.md` a MDX (frontmatter `title`/`description`; usar componentes Starlight donde aporte). `architecture.mdx` con diagrama mermaid (via astro-mermaid, bloque ` ```mermaid `).
- [ ] **Step 2** — `cd site && pnpm build`: compila y `starlight-links-validator` no reporta enlaces rotos.
- [ ] **Step 3: Commit** — `-- site/src/content`.

---

### Task 5: Pages workflow

**Files:** Create `.github/workflows/pages.yml`.

- [ ] **Step 1** — Adaptar de `../gitlab-mcp-server/.github/workflows/pages.yml` (sin indexnow/sitemap): triggers push a `main` con `paths: ["site/**"]` + `workflow_dispatch`; permisos `pages: write`, `id-token: write`; `concurrency: pages`; job build (checkout, pnpm/action-setup con `package_json_file: site/package.json`, setup-node con cache pnpm, `pnpm install --frozen-lockfile`, `pnpm run build` en `working-directory: site`, `upload-pages-artifact` de `site/dist`) → job deploy (`actions/deploy-pages@v5`, environment `github-pages`).
- [ ] **Step 2** — validar YAML. (El deploy real ocurre al pushear; Pages ya está activado.)
- [ ] **Step 3: Commit** — `-- .github/workflows/pages.yml`.

---

### Task 6: Bundle `.mcpb` + integración en release

**Files:** Create `mcpb/manifest.json`, `mcpb/icon.png`, `scripts/build-mcpb.sh`; Modify `.goreleaser.yml` (universal_binaries darwin), `.github/workflows/release.yml` (build+upload .mcpb), `.github/workflows/ci.yml` (validar manifest), `Makefile` (`mcpb` target opcional), `.github/dependabot.yml` (npm /site).

- [ ] **Step 1** — `mcpb/manifest.json` (v0.4): `server.type binary`, `entry_point "server/libgen-mcp"`, `mcp_config.command "${__dirname}/server/libgen-mcp"`, `platform_overrides.win32.command "${__dirname}/server/libgen-mcp.exe"`, `user_config` (mirror:string, download_dir:directory default `${HOME}/Downloads`, timeout:string, max_download_bytes:number, log_level:string — todos opcionales, `env` mapea a `LIBGEN_MIRROR`/`LIBGEN_MCP_*`), `tools` (las 3), `compatibility.platforms [darwin,win32]`, `license MIT`, `icon`. `version` = `0.1.0` (== VERSION). `icon.png` 512×512 (generar uno simple).
- [ ] **Step 2** — `scripts/build-mcpb.sh` adaptado del hermano: ensambla `bundle/` (manifest con versión sellada por jq + icon + `server/` con el binario darwin universal `*darwin_all*` y el `.exe` windows de GoReleaser), empaqueta con `mcpb pack` (pin de versión de la CLI). Salida `dist/libgen-mcp.mcpb`.
- [ ] **Step 3** — `.goreleaser.yml`: añadir bloque `universal_binaries` (id libgen-mcp-universal, ids [libgen-mcp], name_template libgen-mcp, `replace: false`, mod_timestamp). `release.yml` (job release, tras GoReleaser): `bash scripts/build-mcpb.sh "${GITHUB_REF#refs/tags/v}"` + `gh release upload "v${VERSION}" dist/libgen-mcp.mcpb --clobber` (env GH_TOKEN). `ci.yml` (job server-json o uno nuevo `mcpb`): validar `mcpb/manifest.json` (jq parse + `.version == VERSION` + opcional `mcpb validate`). `.github/dependabot.yml`: añadir ecosistema `npm` directory `/site`.
- [ ] **Step 4** — validar: `jq empty mcpb/manifest.json`; versión coincide con VERSION; `goreleaser check`; YAML de workflows válido; `bash -n scripts/build-mcpb.sh`.
- [ ] **Step 5: Commit** — `-- mcpb scripts/build-mcpb.sh .goreleaser.yml .github Makefile`.

---

### Task 7: Verificación final + release de prueba

- [ ] **Step 1** — `make golangci-lint` OK, `go test ./...` verde, markdownlint OK, `cd site && pnpm build` OK, `goreleaser check` OK.
- [ ] **Step 2** — Push de todo a main → verificar CI verde (incluido Pages build si tocó site/, y el nuevo job mcpb).
- [ ] **Step 3** — (Con aprobación del usuario) bump `VERSION` a la siguiente (p. ej. 0.2.0), tag → verificar que el release genera binarios + `.mcpb` + (si público) Pages desplegado, y que el `.mcpb` descargado abre en Claude Desktop.

## Self-Review (hecho)
- Cubre §3.1–3.6 del spec (README+botones, docs/, site/, pages.yml, .mcpb, dependabot). Monolingüe inglés. Sin placeholders (los deep-link configs se generan con el JSON base dado; versiones de Starlight fijadas del hermano). Depende de Fases 1–2 para documentar el estado final.
