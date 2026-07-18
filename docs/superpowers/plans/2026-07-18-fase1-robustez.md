# Fase 1 — Robustez y fiabilidad — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Hacer libgen-mcp robusto: logging con niveles a stderr, validación de config, cliente HTTP con retry+backoff integrado al failover y cooldown por mirror, rate limit configurable, recuperación de panics, apagado limpio, y un gestor de descargas completo (tope de tamaño, chequeo de disco, concurrencia, reanudación, verificación MD5, progreso MCP).

**Architecture:** Se amplían los paquetes existentes (`internal/config`, `internal/libgen`, `internal/tools`, `cmd/server`) y se añade `internal/logging`. Sin romper la API MCP (3 tools). TDD, commits path-scoped por tarea.

**Tech Stack:** Go ≥1.26, `github.com/modelcontextprotocol/go-sdk/mcp` v1.6.1, `golang.org/x/time/rate`, `golang.org/x/net/html`, nuevo `golang.org/x/sys/unix` (statfs). Spec: `docs/superpowers/specs/2026-07-18-libgen-mcp-robustez-tests-docs-design.md`.

## Global Constraints

- Módulo `github.com/jmrplens/libgen-mcp`. Licencia MIT. Go directive `go 1.26`.
- Logging SOLO a stderr (el spec MCP RC 2026-07-28 SEP-2577 eliminó logging/roots/sampling). Progress SÍ se mantiene.
- Nunca escribir a stdout (canal JSON-RPC en stdio).
- `gofmt -l .` vacío, `go vet ./...` limpio, `golangci-lint run --build-tags e2e ./...` a CERO, `govulncheck` limpio antes de cada commit.
- Tests unitarios NUNCA tocan red real (httptest + t.TempDir). Reutilizan helpers `staticMirrors`/`newTestClient` de `internal/libgen/client_test.go` sin redefinir.
- Env nuevas (todas opcionales, prefijo `LIBGEN_MCP_`): `LOG_LEVEL` (info), `RATE_RPS` (1), `RATE_BURST` (1), `MAX_DOWNLOAD_BYTES` (0=ilimitado), `MAX_CONCURRENT_DOWNLOADS` (2), `RETRY_ATTEMPTS` (3). Validación con clamps: RPS(0,20], BURST[1,100], MAX_DOWNLOAD_BYTES[0,50GiB], CONCURRENCY[1,16], RETRY[1,10], TIMEOUT(0,10m].
- Commits con trailer `Claude-Session: https://claude.ai/code/session_01U7oY5WU1y2cFrJz9TkAfsQ`.

---

### Task 1: Paquete `internal/logging`

**Files:**
- Create: `internal/logging/logging.go`, `internal/logging/logging_test.go`

**Interfaces (Produces):**
- `logging.ParseLevel(s string) (slog.Level, error)` — `debug|info|warn|warning|error` (case-insensitive, trim); default a `info` con string vacío; error en valor desconocido.
- `logging.Setup(level slog.Level)` — `slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})))`.
- `logging.ToolCall(tool string, start time.Time, err error)` — `slog.Info("tool call completed", "tool", tool, "duration", time.Since(start))` en éxito; `slog.Error("tool call failed", "tool", tool, "duration", ..., "error", err)` si `err != nil`.

**Steps:**
- [ ] **Step 1: Tests que fallan** — `TestParseLevel` (tabla: ""→Info sin error; "debug"→Debug; "WARN"/"warning"→Warn; "error"→Error; "banana"→error no-nil). `TestToolCall` captura la salida instalando un handler de test (`slog.New` sobre un `bytes.Buffer`) y verifica que un `err` produce nivel error y la clave `tool`. `TestSetupWritesStderr` verifica que Setup no paniquea.
- [ ] **Step 2: Verificar fallo** — `go test ./internal/logging/` → FAIL (símbolos indefinidos).
- [ ] **Step 3: Implementar** `logging.go` con los tres símbolos. `ParseLevel` con `switch strings.ToLower(strings.TrimSpace(s))`. Doc comments en exportados (revive `exported`).
- [ ] **Step 4: Verificar** — `go test ./internal/logging/` PASS; `gofmt`/`go vet` limpios en el paquete.
- [ ] **Step 5: Commit** — `git commit -m "feat(logging): slog stderr logger with levels and ToolCall helper" -- internal/logging`.

---

### Task 2: Config — nuevas variables + `Validate()`

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `logging.ParseLevel` (Task 1) para validar el nivel.
- Produces: `Config` gana campos `LogLevel slog.Level`, `RateRPS float64`, `RateBurst int`, `MaxDownloadBytes int64`, `MaxConcurrentDownloads int`, `RetryAttempts int`. `Load()` los parsea de env con defaults. Nuevo `(c *Config) Validate() error`.

**Steps:**
- [ ] **Step 1: Tests que fallan** — Ampliar `config_test.go`:
  - `TestLoadNewDefaults`: sin env, `RateRPS==1`, `RateBurst==1`, `MaxDownloadBytes==0`, `MaxConcurrentDownloads==2`, `RetryAttempts==3`, `LogLevel==slog.LevelInfo`.
  - `TestLoadNewOverrides`: `t.Setenv` de cada nueva var, verifica el parseo.
  - `TestValidate`: casos que deben fallar — `RateRPS=0`, `RateRPS=21`, `RateBurst=0`, `MaxConcurrentDownloads=0`, `RetryAttempts=0`, `MaxDownloadBytes` negativo, `Timeout` 0, mirror con esquema inválido (`ftp://x`), y un `DownloadDir` no escribible (usar un path bajo un fichero, p.ej. crear un fichero temporal y usar `<file>/sub`). Un caso válido (`t.TempDir()` como DownloadDir) debe pasar.
- [ ] **Step 2: Verificar fallo** — `go test ./internal/config/` FAIL.
- [ ] **Step 3: Implementar** — parseo con helpers (`envInt`, `envFloat`, `envInt64`) tolerantes (valor inválido → error de `Load`, no default silencioso, para números). `Validate()`: rangos con mensajes concretos (`"LIBGEN_MCP_RATE_RPS must be in (0, 20], got %v"`), validación de esquema del mirror (`url.Parse` + `scheme in {http,https}` + host no vacío), y escritura de prueba en `DownloadDir` (`os.MkdirAll` + `os.CreateTemp` + borrar). `LogLevel` vía `logging.ParseLevel`.
- [ ] **Step 4: Verificar** — `go test ./internal/config/` PASS.
- [ ] **Step 5: Commit** — `-- internal/config`.

---

### Task 3: Cliente — rate limit configurable + retry/backoff + cooldown por mirror

**Files:**
- Modify: `internal/libgen/client.go`
- Test: `internal/libgen/client_test.go`

**Interfaces:**
- Consumes: `MirrorLister`, `config.Config`.
- Produces:
  - `libgen.New(m MirrorLister, cfg *config.Config) *Client` — **cambia la firma** (antes `New(m, timeout)`). Guarda rps/burst/retry del cfg. (Actualizar todos los llamadores: `cmd/server`, `cmd/probe`, `newTestClient`.)
  - `newTestClient` pasa un `*config.Config` con defaults sanos (RateRPS alto para no esperar en tests, RetryAttempts=1 salvo tests de retry).
  - Comportamiento interno de `get`: clasifica error transitorio (timeout, error de red, status 5xx/429) vs permanente (2xx parseable, 4xx≠429). En transitorio: cooldown del mirror (mapa `map[string]time.Time` bajo `sync.Mutex`, cooldown 45s) y prueba el siguiente mirror; reintenta con backoff exponencial+jitter hasta `RetryAttempts` intentos totales; respeta `ctx` y el limiter. Un mirror en cooldown se salta salvo que sea el único.
  - `ErrAllMirrorsFailed` se mantiene, encadenando errores.

**Steps:**
- [ ] **Step 1: Tests que fallan** — en `client_test.go` (actualizar `newTestClient` a la nueva firma):
  - `TestGetRetriesTransient`: primer mirror devuelve 503 dos veces luego 200 (contador), o dos mirrors: el primero 503, el segundo 200 → éxito vía el segundo, y el primero queda en cooldown (siguiente llamada lo salta: verificar con un contador de hits que el mirror malo no se re-consulta dentro del cooldown).
  - `TestGetPermanentNoRetry`: mirror devuelve 404 → no reintenta (contador de hits == 1), error propagado.
  - `TestGetAllMirrorsFailed`: todos 500 → `errors.Is(err, ErrAllMirrorsFailed)`.
  - `TestCooldownSkip`: dos mirrors, el primero falla una vez (entra en cooldown); segunda llamada va directa al segundo sin tocar el primero (hits del primero no aumentan).
  - Backoff: para no ralentizar tests, inyectar el backoff base vía un campo del Client (`c.backoffBase time.Duration`, default 200ms) y ponerlo a ~1ms en `newTestClient`.
- [ ] **Step 2: Verificar fallo** — FAIL.
- [ ] **Step 3: Implementar** — refactor de `get`: bucle de intentos; selección de mirror saltando cooldown; `limiter.Wait(ctx)`; en fallo transitorio marcar cooldown + `sleep(backoff*2^n + jitter)` respetando `ctx`; clasificación de error. Logs: Debug intento, Warn failover, Error give-up (usar `slog`). Mantener `maxBodySize` LimitReader.
- [ ] **Step 4: Verificar** — `go test ./internal/libgen/` PASS (todo el paquete; los tests existentes deben seguir verdes con la nueva firma).
- [ ] **Step 5: Commit** — `-- internal/libgen`.

---

### Task 4: Tools — recuperación de panics + logging + cfg

**Files:**
- Modify: `internal/tools/tools.go`
- Test: `internal/tools/tools_test.go`

**Interfaces:**
- Consumes: `logging.ToolCall`, `libgen.Client`, `config.Config`.
- Produces: cada handler se envuelve con `withRecovery(name string, h handlerFunc)` que hace `defer func(){ if r:=recover(); ... }()` convirtiendo el panic en `(*mcp.CallToolResult{IsError:true,...}, zero, nil)` y logueando `slog.Error("tool handler panicked", "tool", name, "panic", r, "stack", debug.Stack())`. Además cada handler llama `logging.ToolCall(name, start, err)` al terminar.

**Steps:**
- [ ] **Step 1: Tests que fallan** — `TestHandlerRecoversPanic`: registrar una tool de prueba (o forzar un panic inyectando un client stub cuyo `Search` paniquee) y verificar que `CallTool` devuelve `IsError` sin propagar el panic. `TestToolCallLogged` opcional (verificar que no rompe). Reusar la sesión in-memory del test existente.
- [ ] **Step 2: Verificar fallo** — FAIL.
- [ ] **Step 3: Implementar** — helper `withRecovery`; envolver los 3 handlers; añadir `logging.ToolCall`. Importar `runtime/debug`.
- [ ] **Step 4: Verificar** — `go test ./internal/tools/` PASS.
- [ ] **Step 5: Commit** — `-- internal/tools`.

---

### Task 5: `cmd/server` — slog setup + apagado limpio + cfg

**Files:**
- Modify: `cmd/server/main.go`, `cmd/server/main_test.go`
- Modify: `cmd/probe/main.go` (adaptar a la nueva firma `libgen.New(m, cfg)`)

**Interfaces:**
- Consumes: `logging.Setup`, `config` (LogLevel, Validate), `libgen.New(m, cfg)`.
- Produces: `run` recibe `ctx context.Context`; `main` crea `ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM); defer stop()`. Tras `config.Load` llama `cfg.Validate()` (si falla, `log.Fatal`). `logging.Setup(cfg.LogLevel)`. En HTTP: `http.Server` con `ReadHeaderTimeout` (ya lo puso el lint) y `Shutdown` en `<-ctx.Done()`. En stdio: `server.Run(ctx, ...)`.

**Steps:**
- [ ] **Step 1: Test** — `TestRunValidatesConfig` (config inválida → error). Mantener `TestHealthEndpoint`/`TestIsCleanShutdown`. Añadir `TestNewHTTPServerShutdown` opcional (arrancar en :0, cancelar ctx, verificar cierre limpio) — si es frágil, dejar solo la verificación de compilación + smoke manual.
- [ ] **Step 2: Verificar fallo** — FAIL.
- [ ] **Step 3: Implementar** — cablear cfg/validate/logging/signal/shutdown; actualizar `libgen.New` call. Adaptar `cmd/probe` a la nueva firma (`libgen.New(mgr, cfg)` con un cfg de defaults).
- [ ] **Step 4: Verificar** — `go build ./... && go test ./cmd/...`; smoke stdio `tools/list` sigue devolviendo las 3 tools.
- [ ] **Step 5: Commit** — `-- cmd/server cmd/probe`.

---

### Task 6: Descargas — tope de tamaño + counting writer + chequeo de disco

**Files:**
- Modify: `internal/libgen/download.go`; Create: `internal/libgen/diskspace_unix.go`, `internal/libgen/diskspace_other.go`
- Test: `internal/libgen/download_test.go`
- Modify: `go.mod` (añadir `golang.org/x/sys`)

**Interfaces:**
- Consumes: `config` (MaxDownloadBytes).
- Produces: `Download` acepta el tope vía el `Client` (guardado del cfg). `freeSpace(dir string) (uint64, error)` con build tags: unix vía `unix.Statfs`, otros SO devuelve `(math.MaxUint64, nil)` (no-op). Un `countingWriter` que aborta con error si supera el tope.

**Steps:**
- [ ] **Step 1: Tests** — `TestDownloadSizeCapContentLength` (CDN con Content-Length > cap → error, sin fichero). `TestDownloadSizeCapStream` (sin Content-Length pero cuerpo > cap → error vía counting writer). `TestDownloadDiskCheck` (mockear freeSpace vía variable de paquete inyectable `freeSpaceFn` para simular disco insuficiente → error). Regresión: descarga normal bajo el cap sigue funcionando.
- [ ] **Step 2: Verificar fallo** — FAIL.
- [ ] **Step 3: Implementar** — `go get golang.org/x/sys`; los dos ficheros diskspace con build tags (`//go:build unix` / `//go:build !unix`); `freeSpaceFn` como var para test; enforcement de cap pre-stream (Content-Length) y en-stream (counting writer). Mensajes claros.
- [ ] **Step 4: Verificar** — `go test ./internal/libgen/` PASS; `go build ./...`.
- [ ] **Step 5: Commit** — `-- internal/libgen go.mod go.sum`.

---

### Task 7: Descargas — semáforo de concurrencia

**Files:**
- Modify: `internal/libgen/download.go`
- Test: `internal/libgen/download_test.go`

**Interfaces:**
- Produces: el `Client` tiene un semáforo `chan struct{}` de tamaño `MaxConcurrentDownloads`; `Download` adquiere antes de la petición (respetando `ctx` mientras espera) y libera con `defer`.

**Steps:**
- [ ] **Step 1: Test** — `TestConcurrencyLimit`: con `MaxConcurrentDownloads=1` y un CDN que bloquea hasta recibir señal, lanzar 2 descargas concurrentes y verificar que la segunda no empieza (el server solo ve 1 conexión) hasta que la primera libera. Usar canales para sincronizar. `TestConcurrencyContextCancel`: si el ctx se cancela mientras se espera turno, error de ctx.
- [ ] **Step 2: Verificar fallo** — FAIL.
- [ ] **Step 3: Implementar** — inicializar el semáforo en `New` según cfg; `select { case sem<-struct{}{}: defer func(){<-sem}() ; case <-ctx.Done(): return ctx.Err() }`.
- [ ] **Step 4: Verificar** — PASS.
- [ ] **Step 5: Commit** — `-- internal/libgen`.

---

### Task 8: Descargas — reanudación (Range + .part) + verificación MD5

**Files:**
- Modify: `internal/libgen/download.go`
- Test: `internal/libgen/download_test.go`

**Interfaces:**
- Produces: `DownloadResult` gana `Verified bool` y `Resumed bool`. La descarga usa un parcial estable `<dir>/.libgen-mcp-<md5>.part`; si existe, `Range: bytes=<size>-` y append; si el CDN responde 200 (ignora Range), reinicia desde 0 (truncar). MD5 se calcula sobre todo el contenido (releyendo el parcial previo en reanudación) vía `io.MultiWriter(file, md5.New())`; al terminar, comparar hex con el `md5` pedido; mismatch → borrar parcial + error de integridad. Éxito → `rename` a destino final.

**Steps:**
- [ ] **Step 1: Tests** — `TestDownloadVerifiesMD5Match` (CDN sirve bytes cuyo md5 == el pedido → `Verified==true`, fichero presente). `TestDownloadMD5Mismatch` (bytes con md5 distinto → error, sin fichero final, sin .part). `TestDownloadResume`: pre-crear un `.part` con la primera mitad; CDN que soporta Range (responde 206 con la segunda mitad) → resultado completo, `Resumed==true`, md5 correcto. `TestDownloadResumeServerIgnoresRange`: CDN responde 200 con todo el contenido pese al Range → reinicio correcto, md5 correcto. Calcular los md5 esperados en el test con `crypto/md5`.
- [ ] **Step 2: Verificar fallo** — FAIL.
- [ ] **Step 3: Implementar** — lógica de parcial/Range/append/truncate; MD5 incremental (rehash del parcial al reanudar); comparación; rename. Cuidado con cerrar ficheros en todas las rutas de error (borrar .part en mismatch; conservarlo en fallo transitorio para permitir reanudar después — documentar la decisión).
- [ ] **Step 4: Verificar** — `go test ./internal/libgen/` PASS (paquete completo).
- [ ] **Step 5: Commit** — `-- internal/libgen`.

---

### Task 9: Descargas — progreso MCP + wiring en la tool

**Files:**
- Modify: `internal/libgen/download.go`, `internal/tools/tools.go`
- Test: `internal/libgen/download_test.go`, `internal/tools/tools_test.go`

**Interfaces:**
- Produces: `Download` acepta un callback opcional `progress func(done, total int64)` (o un tipo `ProgressFunc`), invocado throttled (~cada 500ms o 5%). La tool `download` construye ese callback para emitir `notifications/progress` vía el SDK usando el `progressToken` de la petición (verificar API real: `req.Session` + `mcp.ProgressNotificationParams{ProgressToken, Progress, Total, Message}` — consultar `go doc github.com/modelcontextprotocol/go-sdk/mcp` para el nombre exacto del método de envío, p. ej. `req.Session.NotifyProgress` o `ss.NotifyProgress`). Sin token → callback nil → no-op.

**Steps:**
- [ ] **Step 1: Tests** — `TestDownloadProgressCallback`: descarga con un callback que acumula llamadas; verificar que se invoca al menos una vez y que la última reporta `done==total`. En tools: `TestDownloadToolWithProgressToken` (opcional) verifica que no rompe cuando el cliente in-memory manda un progressToken. Si el envío de progreso es difícil de aislar, testear a nivel de callback en libgen y dejar el wiring de la tool cubierto por compilación + smoke.
- [ ] **Step 2: Verificar fallo** — FAIL.
- [ ] **Step 3: Implementar** — throttle con `time.Now` de última emisión (o contador de bytes); wiring en la tool consultando la API real del SDK para progreso. Documentar el método usado.
- [ ] **Step 4: Verificar** — `go test ./internal/libgen/ ./internal/tools/` PASS.
- [ ] **Step 5: Commit** — `-- internal/libgen internal/tools`.

---

### Task 10: Verificación final Fase 1

- [ ] **Step 1** — `gofmt -l .` vacío; `go vet ./...` limpio; `golangci-lint run --build-tags e2e ./...` CERO; `govulncheck ./...` limpio; `go test ./...` verde; `make build` y `./dist/libgen-mcp --version` OK.
- [ ] **Step 2** — smoke: `--http :0`/`/health` 200; stdio `tools/list` = 3 tools; una búsqueda in-memory contra fixture.
- [ ] **Step 3: Commit** si quedó algo (p. ej. doc de env en README diferido a Fase 3). Si no, nada.

## Self-Review (hecho)
- Cubre §1.1–1.6 del spec. Firmas consistentes (`libgen.New(m, cfg)` propagada a Tasks 3/5/6/7). Sin placeholders. Progreso: el nombre exacto del método del SDK se resuelve en la Task 9 con `go doc` (no inventar).
