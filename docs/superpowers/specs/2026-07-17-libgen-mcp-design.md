# libgen-mcp — Diseño

Fecha: 2026-07-17
Estado: aprobado

## Objetivo

Servidor MCP en Go que permite a un modelo de IA buscar bibliografía en Library
Genesis (familia de mirrors libgen.li), obtener metadatos completos y opciones de
descarga, y descargar ficheros a disco — sustituyendo el uso manual de la web.

Módulo: `github.com/jmrplens/libgen-mcp`. Licencia MIT. Repo local por ahora;
GitHub más adelante.

## Principios

- **Superficie mínima de tools** (concepto "Dynamic" de gitlab-mcp-server): pocas
  tools genéricas cuyo schema documenta los valores posibles, en lugar de una tool
  por colección. Menos tokens al cargar el MCP.
- Mismo stack y estilo que `../gitlab-mcp-server`: Go 1.26, SDK oficial
  `github.com/modelcontextprotocol/go-sdk`, layout `cmd/` + `internal/`.
- Ser buen ciudadano: rate limit propio de 1 req/s hacia los mirrors.

## Superficie MCP: 3 tools

### 1. `search`

Busca en el catálogo. Parámetros (el schema JSON documenta valores permitidos):

| Campo | Tipo | Descripción |
|---|---|---|
| `query` | string, requerido | Texto de búsqueda |
| `topics` | array de enum, opcional | Colecciones: `nonfiction` (l), `fiction` (f), `articles` (a), `magazines` (m), `comics` (c), `standards` (s), `fiction_rus` (r). Sin especificar: todas |
| `search_in` | array de enum, opcional | Columnas: `title` (t), `author` (a), `series` (s), `year` (y), `publisher` (p), `isbn` (i). Sin especificar: todas |
| `results_per_page` | enum 25/50/100, opcional (def. 25) | |
| `page` | int, opcional (def. 1) | |
| `order` | enum opcional: `title`, `author`, `year`, `size`, `id`, `time_added` | |
| `order_mode` | enum `asc`/`desc`, opcional | |

Devuelve JSON: lista de resultados con `edition_id`, `file_id`, `md5`, `title`,
`isbns`, `authors`, `publisher`, `year`, `language`, `pages`, `size` (bytes y
legible), `extension`, `type` (badge b/a/…), `download_options` (enlace libgen del
mirror activo por md5 + mirrors externos: Anna's Archive, libgen.pw, Randombook),
y bloque de paginación (`page`, `total_results` si está disponible).

Errores distinguibles: "0 resultados" ≠ "parser no encontró la tabla" (posible
cambio de maquetado) ≠ "mirror inaccesible".

### 2. `get_details`

Metadatos completos de un registro vía API JSON (`json.php`).

| Campo | Tipo | Descripción |
|---|---|---|
| `md5` | string, opcional* | MD5 del fichero |
| `id` | string, opcional* | ID de edición o fichero |
| `object` | enum opcional: `edition` (def.), `file` | Tipo de objeto para `id` |

(*) Exactamente uno de `md5` o `id` es requerido.

Devuelve: todos los campos de json.php — título completo, `title_add`, serie,
autores, editorial, ciudad, edición, año, páginas, ISBNs/identificadores, DOI,
descripción, URL de portada, tamaño, extensión, fechas de alta/modificación.

### 3. `download`

Descarga un fichero resolviendo la cadena no-directa de libgen.li.

| Campo | Tipo | Descripción |
|---|---|---|
| `md5` | string, requerido | |
| `path` | string, opcional | Directorio destino. Def.: `LIBGEN_MCP_DOWNLOAD_DIR` o `~/Downloads` |
| `filename` | string, opcional | Nombre destino. Def.: el de `content-disposition` del CDN, saneado |

Cadena (verificada 2026-07-17):
1. `GET /ads.php?md5=X` → extraer `get.php?md5=X&key=CLAVE` del HTML
2. `GET /get.php?md5=X&key=CLAVE` → redirect 307 a CDN (p. ej. `cdn3.booksdl.lc`)
3. Seguir redirect → fichero con nombre real en `content-disposition`

Devuelve: ruta absoluta final, tamaño en bytes, nombre original. Streaming a
disco (sin cargar el fichero en memoria); escritura a fichero temporal + rename
para no dejar descargas corruptas a medias.

## Arquitectura

```
cmd/server/          main: stdio por defecto, --http <addr> para streamable HTTP
cmd/probe/           CLI de diagnóstico (ver abajo)
internal/config/     env vars y defaults
internal/mirrors/    descubrimiento y failover de mirrors
internal/libgen/     cliente HTTP: búsqueda (parser HTML), json.php, descarga
internal/tools/      registro de las 3 tools MCP y schemas
```

Dependencias: `modelcontextprotocol/go-sdk`, `google/jsonschema-go`,
`golang.org/x/net` (html parser), `golang.org/x/time` (rate limit). Nada exótico.

### internal/mirrors

- Fuente: `https://shadowlibraries.github.io/DirectDownloads/libgen/` — lista
  `<ul>` de mirrors (hoy: libgen.li, .vg, .la, .bz, .gl; todos la misma familia
  de software ⇒ un único parser vale para todos).
- Caché en `~/.cache/libgen-mcp/mirrors.json`, TTL 24 h. Si la página de
  shadowlibraries no responde, usar caché aunque esté caducada; si tampoco hay
  caché, lista hardcodeada de respaldo (los 5 actuales).
- Preferencia: `LIBGEN_MIRROR` si está definida; si no, `libgen.li`.
- Failover: si una petición falla (timeout, 5xx, Cloudflare challenge), reintentar
  la misma operación en el siguiente mirror de la lista. Si todos fallan, error
  MCP claro ("todos los mirrors inaccesibles; posible bloqueo de red — prueba
  VPN/DNS").

### internal/libgen — datos verificados del sitio (2026-07-17)

- Búsqueda: `GET /index.php?req=Q&topics[]=…&columns[]=…&objects[]=f&objects[]=e&res=N&page=N&order=…&ordermode=…&filesuns=all`
- Resultados en `<table id="tablelibgen"><tbody>`, una `<tr>` por fichero; celdas:
  título+ISBNs+badges (con `edition.php?id=` y file id), autores, editorial, año,
  idioma, páginas, tamaño (`file.php?id=`), extensión, columna de mirrors con
  `/ads.php?md5=…` + 3 externos.
- API JSON: `GET /json.php?object={e|f|a|s|p|w}&ids=…` (también `limit1/limit2`,
  `mode=last|modified`). Devuelve mapa id→objeto.
- La key de `get.php` se genera por petición a `ads.php`; no es cacheable.

## cmd/probe — diagnóstico

CLI que ejecuta contra un mirror real las mismas rutas que usa el servidor y
reporta OK/ROTO por paso: descubrimiento de mirrors, búsqueda en cada topic,
parseo de la tabla, json.php, extracción de key en ads.php, redirect del CDN
(HEAD, sin descargar). Uso: mantenimiento cuando LibGen cambie el HTML.

## Configuración

| Env | Default | Descripción |
|---|---|---|
| `LIBGEN_MIRROR` | (auto) | Fuerza un mirror concreto, p. ej. `https://libgen.li` |
| `LIBGEN_MCP_DOWNLOAD_DIR` | `~/Downloads` | Directorio de descargas |
| `LIBGEN_MCP_TIMEOUT` | `30s` | Timeout por petición HTTP |

Flags de `cmd/server`: `--http <addr>` (transporte streamable HTTP), sin flag =
stdio.

## Manejo de errores

- Failover automático entre mirrors (arriba).
- Parser: si no encuentra `#tablelibgen`, error explícito de "estructura HTML
  inesperada" — nunca confundir con 0 resultados.
- Descarga: validar que la respuesta final del CDN es binaria (no una página HTML
  de error); tamaño recibido vs `content-length`.
- Rate limit 1 req/s por proceso hacia mirrors.

## Testing

- **Unit** (grueso del valor): parser de búsqueda, parser de ads.php/key, parser
  de mirrors de shadowlibraries — con fixtures HTML reales en `testdata/`
  capturadas del sitio (una por colección + ficha + página ads).
- **Integración/e2e** contra red real, opt-in con build tag `e2e`: detecta cuándo
  cambia el HTML real. `cmd/probe` cubre el diagnóstico manual.
- Tests de tools: servidor MCP en memoria con el cliente del SDK contra un
  `httptest.Server` que sirve las fixtures.

## Fuera de alcance (v1)

- Descarga vía mirrors externos (Anna's Archive, libgen.pw): solo se devuelven
  sus URLs.
- Búsqueda full-text dentro de ficheros, portadas como recurso MCP, torrents,
  autenticación/login en libgen.
- Soporte de mirrors de otra familia de software (libgen.rs/.is/.st).
