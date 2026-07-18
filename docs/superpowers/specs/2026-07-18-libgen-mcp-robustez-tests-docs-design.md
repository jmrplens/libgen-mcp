# libgen-mcp — Robustez, auditoría de tests y documentación — Diseño

Fecha: 2026-07-18
Estado: aprobado

## Objetivo

Llevar libgen-mcp (v0.1.0, ya funcional y desplegado) a "realmente robusto":
recuperación ante fallos, gestión de descargas completa, logging con niveles,
una auditoría de tests exhaustiva (unit + e2e cubriendo todas las categorías,
queries, opciones de descarga, todas las tools y todas las rutas de error) y
documentación completa (README + `docs/` markdown + sitio Astro Starlight en
`site/`). Se trae del proyecto hermano `../gitlab-mcp-server` todo lo aplicable,
adaptado y simplificado a un proyecto de 3 tools.

Ejecución en 3 fases: **Fase 1 (robustez) → Fase 2 (tests) → Fase 3 (docs)**.
Las fases 2 y 3 dependen de la superficie final de la 1.

## Nota sobre el protocolo MCP (spec RC 2026-07-28, SEP-2577)

La última versión del spec **elimina** las capabilities de **logging, roots y
sampling**. Por tanto:
- **No** se usa logging por protocolo MCP. El logging es **solo a stderr** con
  niveles (visible en los logs del servidor MCP del cliente). El usuario ve "lo
  que ocurre" ahí, con verbosidad controlada por `LOG_LEVEL`.
- **Progress SÍ se mantiene** en el spec: se usa para el progreso de descargas.
- No se implementan roots ni sampling.

---

## Fase 1 — Robustez y fiabilidad

### 1.1 Logging (`internal/logging`)

- **`log/slog` con JSONHandler a `os.Stderr`**, configurado una vez en `main`
  con `slog.SetDefault` (después de parsear flags; nunca a stdout, que es el
  canal JSON-RPC en stdio).
- Paquete `internal/logging`:
  - `ParseLevel(s string) slog.Level` — `debug|info|warn(=warning)|error`, default `info`.
  - `Setup(level slog.Level)` — instala el handler por defecto.
  - `ToolCall(tool string, start time.Time, err error)` — loguea `tool`,
    `duration`, y `error`/nivel (Info éxito, Error fallo). Se llama una vez al
    final de cada una de las 3 tools.
- Env: `LIBGEN_MCP_LOG_LEVEL` (default `info`). Validado (nivel desconocido =
  error de config, no silencioso).
- Eventos y niveles:
  - Startup/lifecycle → Info (versión, transporte, addr).
  - Intento de mirror → Debug (`mirror attempt`, url, attempt).
  - Failover de mirror → Warn (`mirror failed, trying next`, url, error).
  - Todos los mirrors caídos → Error.
  - Espera de rate limit / mirror en cooldown → Debug.
  - Descarga inicio/fin → Info (`bytes`, `duration`, `mirror`); retry → Warn.

### 1.2 Validación de configuración (`internal/config`)

`Load()` amplía el parseo y añade `Validate() error` con un mensaje claro y
accionable por variable, y topes máximos (clamp) sanos. Variables nuevas
(todas opcionales con default seguro):

| Env                                   | Default        | Validación                   |
| ------------------------------------- | -------------- | ---------------------------- |
| `LIBGEN_MCP_LOG_LEVEL`                | `info`         | uno de debug/info/warn/error |
| `LIBGEN_MCP_RATE_RPS`                 | `1`            | > 0, ≤ 20                    |
| `LIBGEN_MCP_RATE_BURST`               | `1`            | ≥ 1, ≤ 100                   |
| `LIBGEN_MCP_MAX_DOWNLOAD_BYTES`       | `0` (sin tope) | ≥ 0, ≤ 50 GiB                |
| `LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS` | `2`            | ≥ 1, ≤ 16                    |
| `LIBGEN_MCP_RETRY_ATTEMPTS`           | `3`            | ≥ 1, ≤ 10                    |

Existentes: `LIBGEN_MIRROR` (validar esquema http/https + host), `LIBGEN_MCP_DOWNLOAD_DIR`
(validar escribible: crear dir + fichero de prueba), `LIBGEN_MCP_TIMEOUT` (> 0,
≤ 10m). Fallo de validación = el servidor no arranca, con mensaje concreto.

### 1.3 Cliente HTTP endurecido (`internal/libgen/client.go`)

- **Timeouts por petición** vía `context.WithTimeout(cfg.Timeout)`; `io.LimitReader`
  ya se aplica a cuerpos parseados (mantener el cap de 20 MiB para HTML/JSON).
- **Retry con backoff + jitter integrado con el failover de mirrors**: un
  `get` resiliente que, ante error transitorio (timeout, 5xx, conexión), aplica
  backoff exponencial con jitter y prueba el siguiente mirror, hasta
  `RETRY_ATTEMPTS` intentos totales. Respeta el rate limiter y el `ctx`. Errores
  no transitorios (404, parse) no se reintentan.
- **Salud/cooldown por mirror**: mapa `mirror → lastFailure` protegido por mutex;
  un mirror con fallo reciente se salta durante un cooldown (~45s) salvo que sea
  el único disponible. Patrón atómico timestamp + mutex (evita "thundering herd"
  sobre un mirror que se recupera).
- **Rate limit configurable**: `rate.NewLimiter(rate.Limit(RPS), BURST)`;
  `limiter.Wait(ctx)` antes de cada petición (pacear, no descartar).

`ErrAllMirrorsFailed` sigue encadenando los errores por mirror. Se añade
distinción de error transitorio vs permanente para decidir el retry.

### 1.4 Recuperación de panics (`internal/tools`)

Wrapper que envuelve cada handler de tool: `defer recover()` que convierte un
panic (p. ej. parseo de HTML inesperado) en un resultado MCP `IsError` con
mensaje claro y un log `Error` a stderr con el stack. Una página malformada
nunca tumba el servidor.

### 1.5 Apagado limpio (`cmd/server`)

`signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)` en
`main`; el `ctx` se propaga a `server.Run` (stdio) y, en HTTP, a un bucle que
hace `http.Server.Shutdown(shutdownCtx)` (timeout 5s) al cancelarse. Una
descarga en curso se cancela vía `ctx`.

### 1.6 Gestor de descargas (`internal/libgen/download.go`)

Amplía `Download` (mantiene escritura atómica temp+rename y verificación de
content-length ya existentes) con:

- **Tope de tamaño**: si `Content-Length` > `MAX_DOWNLOAD_BYTES` (>0), error
  antes de escribir; además un *counting writer* aborta si el stream supera el
  tope (defensa si no hay Content-Length).
- **Chequeo de espacio en disco**: antes de una descarga grande, comprobar
  espacio libre en el directorio destino. Best-effort: `unix.Statfs` en unix
  (`golang.org/x/sys/unix`), no-op en otros SO. Si no hay espacio suficiente
  (tamaño esperado + margen), error claro.
- **Límite de descargas concurrentes**: semáforo (canal con buffer) de tamaño
  `MAX_CONCURRENT_DOWNLOADS`; `download` adquiere antes de empezar, libera al
  terminar. Respeta `ctx` mientras espera turno.
- **Reanudables (HTTP Range)**: se descarga a un parcial estable
  `<dir>/.libgen-mcp-<md5>.part`. Si existe y el CDN soporta Range (los CDN de
  libgen lo soportan, verificado en el probe), se envía `Range: bytes=<size>-`
  y se anexa; si el servidor ignora Range (responde 200), se reinicia desde 0.
  Al completar y verificar, `rename` al nombre final.
- **Verificación de integridad MD5**: se calcula el MD5 mientras se escribe
  (`io.MultiWriter(file, md5.New())`); al terminar se compara con el `md5`
  solicitado. Si no coincide, se borra el parcial y se devuelve error de
  integridad (fichero corrupto / mirror manipulado). En una reanudación, el
  MD5 se recalcula releyendo el parcial existente antes de continuar.
- **Progreso en vivo (MCP progress)**: si la petición trae `progressToken`, se
  emiten notificaciones `notifications/progress` (throttled, p. ej. cada ~500ms
  o cada 5%) con bytes/total, para que el usuario vea el avance en el cliente.
  Sin token, no-op.

`DownloadResult` añade `verified bool` (MD5 comprobado) y `resumed bool`.

---

## Fase 2 — Auditoría de tests

### 2.1 Tests unitarios (fixtures + httptest, sin red real)

**Fixtures nuevas** (capturadas del sitio real una vez, como en la Task 2
original) en `internal/libgen/testdata/`: páginas de búsqueda para las
categorías que faltan — `fiction`, `magazines`, `comics`, `standards`,
`fiction_rus` — además de las existentes (`nonfiction`/books, `articles`,
`empty`). Más: una página de error HTML servida como binario (para el sniff),
un binario pequeño de prueba, y un JSON de detalles adicional.

**Matriz de cobertura** (tests a añadir/ampliar):
- **Categorías**: parseo correcto de resultados para las 7 topics.
- **Query**: `search_in` (todas las columnas), `order`+`order_mode`,
  `results_per_page` (25/50/100), `page` (paginación), y combinaciones;
  validación de enums (valores inválidos → error).
- **Opciones de descarga**: parseo de los enlaces por resultado (libgen
  ads.php + los mirrors externos), y que el de libgen va primero.
- **3 tools**: happy path + validación de entrada de `search`, `get_details`
  (md5 XOR id), `download`.
- **Todas las rutas de error**: todos-los-mirrors-caídos (`ErrAllMirrorsFailed`),
  layout cambiado (`ErrLayoutChanged`) distinguido de 0 resultados, rechazo de
  HTML (Content-Type y magic-bytes), tope de tamaño excedido, disco insuficiente,
  md5 inválido/no encontrado, Range no soportado (fallback a 0), timeout/ctx
  cancelado.
- **Robustez nueva**: retry con backoff (mirror transitorio → siguiente),
  cooldown por mirror (mirror caído se salta), panic recovery (handler que
  paniquea → IsError), validación de config (cada variable inválida → error),
  semáforo de concurrencia, verificación MD5 (match y mismatch), reanudación
  (parcial existente + Range).

Umbral de cobertura del CI se sube a **85%** sobre `./internal/...`.

### 2.2 Tests e2e (contra el sitio real, gated)

- Nuevo `test/e2e/` con build tag `e2e` (paquete propio; el código e2e nunca
  compila en `go test ./...`).
- **Gating/skip**: cada test hace `t.Skip` salvo que `LIBGEN_E2E=1` **y** una
  sonda rápida de alcanzabilidad al mirror configurado responda. Nunca falla por
  sitio caído; se salta.
- **Cobertura e2e**: búsqueda en las **7 categorías**, `get_details` por md5,
  y una **descarga real** de un fichero pequeño con verificación MD5 y (si
  aplica) reanudación. Aserciones de **estructura, no de valores exactos**
  (slice no vacío, cada resultado con título no vacío + md5 `^[a-f0-9]{32}$` +
  opciones de descarga; total_files presente). Timeouts por test 30–60s.
  **Pacing educado** reutilizando el rate limiter entre peticiones.
- **CI**: workflow `e2e.yml` con `schedule` (nightly) + `workflow_dispatch`
  (manual), NO en cada PR. `gotestsum` con JUnit como artefacto. El CI normal
  mantiene un **check de compilación** (`go test -tags e2e -c -o /dev/null
  ./test/e2e/...`) para que el código e2e no se pudra.
- Se migra el `internal/libgen/e2e_test.go` actual a `test/e2e/` ampliado.

---

## Fase 3 — Documentación

Todo manuscrito (3 tools no justifican maquinaria de generación). Idioma
**inglés** (coherente con el README y `server.json` actuales; monolingüe).

### 3.1 README (ampliado)

Secciones: **badges** (release, licencia, plataforma, SonarCloud quality gate +
coverage, Go Report Card, Go Reference) · qué es · **tabla de botones de
instalación en un clic** (como gitlab-mcp-server) · configuración del cliente
MCP · las 3 tools con parámetros · tabla de variables de entorno (incluidas las
nuevas de robustez) · robustez (failover, retry, rate limit, gestión de
descargas, verificación MD5) · enlace a `docs/` y al sitio · mantenimiento
(probe, e2e) · uso responsable.

**Tabla de botones de instalación** (HTML, estilo hermano). Cada botón registra
el servidor **basado en Docker** (`ghcr.io/jmrplens/libgen-mcp:latest`, sin
token — libgen no requiere auth, así que los configs son más simples que los de
gitlab). Filas:
- **VS Code** / **VS Code Insiders**: `https://insiders.vscode.dev/redirect/mcp/install?name=libgen&config=<json-url-encoded>`.
- **Cursor**: `https://cursor.com/install-mcp?name=libgen&config=<base64>` con la insignia oficial.
- **LM Studio**: `https://lmstudio.ai/install-mcp?name=libgen&config=<base64>`.
- **Kiro**: `https://kiro.dev/launch/mcp/add?name=libgen&config=<json-url-encoded>`.
- **Claude Desktop**: botón de descarga del **`.mcpb`** desde el release
  (`.../releases/latest/download/libgen-mcp.mcpb`).

Config base para los deep-links (docker):
`{"command":"docker","args":["run","-i","--rm","ghcr.io/jmrplens/libgen-mcp:latest"]}`.
Además, bloque **Claude Code** con `claude mcp add libgen ...` (binario o docker).

### 3.6 Extensión `.mcpb` para Claude Desktop

Bundle instalable en un clic para Claude Desktop (nativo, sin Docker):
- **`mcpb/manifest.json`** (manifest_version `0.4`, confirmado con Context7):
  `server.type: "binary"`, `entry_point: server/libgen-mcp`,
  `mcp_config.command: "${__dirname}/server/libgen-mcp"`, `platform_overrides.win32`
  con `.exe`. `user_config` (todos opcionales, sin secretos) mapeando las env:
  `mirror` (string), `download_dir` (type `directory`, default `${HOME}/Downloads`),
  `timeout`, `max_download_bytes` (number), `log_level` (string). Lista las 3
  tools. `compatibility.platforms: [darwin, win32]`, `license: MIT`, icono.
- **`mcpb/icon.png`** (512×512).
- **`scripts/build-mcpb.sh`** (adaptado del hermano): ensambla `bundle/`
  (manifest con versión sellada + icono + `server/` con el binario **darwin
  universal** y el `.exe` de windows de GoReleaser) y empaqueta con la CLI
  oficial **`mcpb pack`** (pin de versión). Salida `dist/libgen-mcp.mcpb`.
- **`.goreleaser.yml`**: añadir `universal_binaries` (darwin arm64+amd64) para
  que el `.mcpb` tenga un binario darwin universal (`replace: false` para
  conservar los assets por-arch).
- **`.github/workflows/release.yml`**: tras GoReleaser, `bash scripts/build-mcpb.sh
  <version>` y `gh release upload v<version> dist/libgen-mcp.mcpb --clobber`.
- Opcional: añadir el `.mcpb` como paquete `mcpb` en `server.json` (ya tiene esa
  forma para los binarios).
- **CI** (`ci.yml`): validar `mcpb/manifest.json` contra su esquema (`mcpb validate`
  o jsonschema) y que su versión coincide con `VERSION`.

### 3.2 `docs/` (markdown plano, un nivel)

```
docs/
├── README.md            # índice (tabla)
├── getting-started.md   # instalar + configurar cliente + primera búsqueda
├── configuration.md     # todas las variables de entorno
├── tools.md             # search / get_details / download (input/output/errores)
├── architecture.md      # cliente con failover + flujo de descarga (1 mermaid)
└── troubleshooting.md    # mirror caído, descarga fallida, errores comunes
```

### 3.3 `site/` (Astro Starlight)

Esqueleto adaptado del hermano (versiones: `@astrojs/starlight ^0.41.3`,
`astro ^7`, `astro-mermaid`, `starlight-links-validator`, `sharp`; `pnpm`,
Node ≥22). Monolingüe (sin i18n dual, sin componentes SEO pesados, sin
maquinaria de stats/llms). Contenido:

```
site/
├── package.json, astro.config.mjs, tsconfig.json, pnpm-lock.yaml
├── public/  (favicon.svg)
└── src/
    ├── content.config.ts
    ├── assets/  (logo-light.svg, logo-dark.svg)
    ├── styles/custom.css   (color acento)
    └── content/docs/
        ├── index.mdx            # hero + 3 LinkCards
        ├── getting-started.mdx
        ├── configuration.mdx
        ├── tools.mdx
        ├── architecture.mdx     # diagrama mermaid
        └── troubleshooting.mdx
```

Sidebar manual (Guía / Referencia). `astro.config.mjs` con `site` +
`base: "/libgen-mcp"`, mermaid, links-validator, logo, editLink, custom.css.

### 3.4 Despliegue (`.github/workflows/pages.yml`)

Build del sitio (pnpm install --frozen-lockfile → build) + `upload-pages-artifact`
+ `deploy-pages`. Trigger: push a `main` con cambios en `site/**` +
`workflow_dispatch`. **Pages ya está activado en el repo** (build_type=workflow,
URL https://jmrplens.github.io/libgen-mcp/); el deploy publica ahí. Permisos
`pages: write`, `id-token: write`, `concurrency` group pages.

### 3.5 Dependabot

Añadir ecosistema `npm` en `/site` a `.github/dependabot.yml`.

---

## Cambios de estructura (resumen)

- Nuevo: `internal/logging/`, `test/e2e/`, `site/`, `docs/*.md`.
- Ampliado: `internal/config` (nuevas env + Validate), `internal/libgen`
  (client retry/cooldown/rate; download size/disk/concurrency/resume/md5/progress),
  `internal/tools` (panic recovery + progress + logging), `cmd/server`
  (slog setup + graceful shutdown), `.github/workflows` (e2e.yml, pages.yml),
  `.github/dependabot.yml`, `README.md`, `.golangci.yml`/Makefile si hace falta
  para `golang.org/x/sys`.
- Dependencias nuevas: `golang.org/x/sys` (statfs, unix), posiblemente nada más
  (md5/semaphore/backoff con stdlib).

## Fuera de alcance

- Logging/roots/sampling por protocolo MCP (eliminados del spec).
- i18n del sitio, SEO pesado, generadores de docs/stats.
- Circuit breaker completo, pool multi-sesión, resume entre reinicios más allá
  del fichero `.part` por md5.

## Estrategia de testing (global)

TDD por tarea. Unit sin red (fixtures + httptest). E2E gated tras build tag.
Cobertura ≥85% en `internal/`. golangci-lint + govulncheck a cero. El CI normal
no toca la red; el e2e nightly sí, con skip si el sitio no responde.
