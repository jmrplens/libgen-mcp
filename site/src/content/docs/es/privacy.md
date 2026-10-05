---
title: Política de privacidad
description: "Qué maneja libgen-mcp y a dónde va: nada llega al mantenedor, la telemetría está apagada por defecto y cada destino está listado por herramienta."
mentions:
  - name: "Model Context Protocol"
    wikidata: Q133436854
  - name: "OpenTelemetry"
    wikidata: Q121746046
datePublished: "2026-10-05"
# Traducción de PRIVACY.md. El digest de abajo fija la versión del original de la
# que procede: scripts/sync-privacy.mjs --check falla cuando el original cambia y
# esta traducción se queda atrás.
privacySource: "4b527c8088efd530"
head:
  - tag: script
    attrs:
      type: application/ld+json
    content: |
      {
        "@context": "https://schema.org",
        "@type": "FAQPage",
        "@id": "https://jmrplens.github.io/libgen-mcp/es/privacy/#faq",
        "inLanguage": "es",
        "isPartOf": {
          "@id": "https://jmrplens.github.io/libgen-mcp/es/privacy/"
        },
        "mainEntity": [
          {
            "@type": "Question",
            "name": "¿Recoge libgen-mcp telemetría o analíticas?",
            "acceptedAnswer": {
              "@type": "Answer",
              "text": "No, salvo que lo actives tú, y nunca al mantenedor. No hay analíticas, ni informes de fallos, ni backend propio; el servidor no crea ninguna base de datos ni ningún fichero de telemetría, y registra en la salida de error estándar, donde tu cliente MCP los recoge si es que los recoge. El mantenedor nunca recibe tus consultas, tus descargas ni ninguna información de uso, configures lo que configures. LIBGEN_MCP_TELEMETRY te permite a ti exportar trazas, métricas y registros de OpenTelemetry a un colector que tú ejecutas; está apagado por defecto, el único destino por defecto de los exportadores es tu propio localhost, y lo que llevan describe operaciones y no lo que se buscó. Consulta OpenTelemetry, si lo activas."
            }
          },
          {
            "@type": "Question",
            "name": "¿Qué datos salen de mi máquina, y quién los recibe?",
            "acceptedAnswer": {
              "@type": "Answer",
              "text": "Solo los identificadores que pides, y solo al servicio al que se pregunta. Una búsqueda envía el texto de tu consulta a un mirror de Library Genesis; una cita que pegas en get_details se envía a Crossref para encontrar su DOI; una descarga por DOI envía ese DOI a las fuentes de artículos de la cadena; una descarga por ISBN envía ese ISBN a OAPEN y al Internet Archive. Todos los destinos están listados en Flujos de datos. No se envía nada al mantenedor, y no hay conexiones en segundo plano: cada petición es consecuencia directa de una llamada a una herramienta."
            }
          },
          {
            "@type": "Question",
            "name": "¿Almacena libgen-mcp mis credenciales?",
            "acceptedAnswer": {
              "@type": "Answer",
              "text": "No se requiere ninguna credencial, y ninguna se persiste. Las tres opcionales — una clave de membresía de Anna's Archive, una clave gratuita de la API de CORE y una clave gratuita de la API de OpenAlex — se leen del entorno y se envían solo al único servicio al que corresponden. Una credencial proporcionada por llamada mediante la elicitación de tu cliente se usa para esa única petición y nunca se escribe en disco."
            }
          },
          {
            "@type": "Question",
            "name": "¿Los archivos descargados se quedan en mi máquina?",
            "acceptedAnswer": {
              "@type": "Answer",
              "text": "Sí. Las descargas se escriben únicamente en el directorio de destino local (LIBGEN_MCP_DOWNLOAD_DIR, por defecto ~/Downloads, o el argumento path por llamada) y no se sube nada a ningún sitio. La herramienta read extrae texto en local de un archivo que ya tienes."
            }
          }
        ]
      }
---

**libgen-mcp** es un servidor Model Context Protocol (MCP) que ejecutas tú. En su
uso normal se ejecuta enteramente en tu máquina y actúa como puente entre tu
cliente MCP (Claude Desktop, Claude Code, Cursor, VS Code, …) y los mirrors
públicos de Library Genesis. No necesita **ninguna cuenta, ningún token y ninguna
credencial**. Esta política describe qué datos maneja el software y a dónde van.

Hay una vía distinta, descrita aparte más abajo: el endpoint público alojado en
`mcp.jmrp.io/libgen`, donde el software se ejecuta en la máquina de otra persona y
no en la tuya. Ver [Endpoint alojado](#endpoint-alojado).

## Qué recopilamos

**Nada.** El servidor no envía telemetría salvo que la actives tú, y aun entonces
solo a un colector que ejecutas tú (ver más abajo). No tiene analítica, ni
informes de fallos, ni backend propio. No hay ninguna cuenta que crear ni nada a
lo que iniciar sesión. Cuando lo ejecutas tú — que es como esta documentación recomienda usarlo —
el mantenedor nunca recibe, almacena ni tiene acceso a ninguno de tus datos ni a tu
información de uso, porque nada se envía jamás a ningún sitio que el mantenedor
controle.

Esa última frase habla del software y se cumple lo ejecutes donde lo ejecutes. No
habla del [endpoint alojado](#endpoint-alojado), donde ese mismo software corre
en una máquina que opera el mantenedor.

### OpenTelemetry, si lo activas

El servidor puede exportar trazas, métricas y registros, y esta sección existe
para que el párrafo de arriba siga siendo exactamente cierto en lugar de
convertirse en un tecnicismo.

Está **apagado por defecto** (`LIBGEN_MCP_TELEMETRY`). Cuando lo activas, la
telemetría va a un colector que **tú** configuras y ejecutas. No hay ningún camino
por el que pueda llegar al mantenedor: el único valor por defecto que tienen los
exportadores es `https://localhost:4318` — tu propia máquina, donde la exportación
falla y lo dice en tu propio registro del servidor salvo que tengas un colector
ahí. Nada en ninguna ruta de código lleva la dirección de otra persona. Activarlo
es una decisión que tomas sobre tu propio despliegue y sobre quienes lo usan.

**Lo que registra describe operaciones, nunca su contenido:** el método invocado,
la herramienta nombrada, si tuvo éxito, cuánto tardó, qué fuente de descarga sirvió
el fichero y de qué mirror vino. **Lo que alguien buscó queda excluido por diseño y
por ningún ajuste**: no hay valor de ninguna variable que meta una consulta de
búsqueda o el título de un registro en una señal exportada, porque no hay ningún
operador para quien un colector con «lo que esta persona buscó» sea el resultado
correcto. Los argumentos de las herramientas, sus resultados y cualquier credencial
suministrada para una sola llamada quedan excluidos en los mismos términos.

El identificador del ítem al que se refiere una llamada (un md5, un DOI, un ISBN)
tampoco se exporta hoy: nombra un libro o un artículo concreto, que es la misma
revelación por otra vía.

**Quién hizo una llamada solo se registra si lo pides**, con
`LIBGEN_MCP_TELEMETRY_IDENTITY`. El valor por defecto, `none`, no registra nada de
quien llama. `pseudonymous` registra un digest con clave de la dirección a la que
se cargó la petición, que distingue el tráfico de un llamante del de otro sin
nombrar a nadie. `full` registra esa dirección y el nombre y la versión del propio
cliente, y es para quien opera esto para un grupo conocido de personas en su propio
colector — en un endpoint público una dirección es dato personal, y por eso no es
ni el valor por defecto ni algo que nada active por ti.

El detalle completo, incluido lo que lleva cada señal y las cuatro trampas de las
variables `OTEL_*` estándar, está en la [guía de telemetría](/libgen-mcp/es/telemetry/).

## Flujos de datos

Cada petición de red es consecuencia directa de una llamada de herramienta que tú
(a través de tu asistente de IA) haces. No hay conexiones en segundo plano. Los
destinos son:

- **Mirrors de Library Genesis.** `search` y `get_details` consultan los mirrors
  de Library Genesis (por ejemplo `libgen.li`, `libgen.gl`, `libgen.la`,
  `libgen.bz`, `libgen.vg`), que se descubren automáticamente y se cachean, o se
  fijan con `LIBGEN_MIRROR`. El descubrimiento lee las listas públicas de mirrors
  de Library Genesis y de Anna's Archive en `shadowlibraries.github.io`, un sitio
  de GitHub Pages, cuando la lista en caché falta, tiene más de un día, o era la
  lista de reserva incorporada en lugar de una descargada. Esa petición no lleva
  nada de tu llamada. Las peticiones de `download` de libros (por `md5`)
  obtienen el fichero del mirror que lo sirve y de sus CDN de descarga. Si la vía
  del mirror principal falla, se prueba la fuente `randombook`
  (`randombook.org`) como alternativa.
- **API de Unpaywall (solo cuando pides un artículo por DOI, y solo si la
  activas).** `LIBGEN_MCP_UNPAYWALL_EMAIL` está **vacía por defecto**, lo que
  desactiva la fuente `unpaywall`: no se hace ninguna petición a Unpaywall, y
  nunca se sustituye tu dirección por la del mantenedor ni por la de nadie.
  Hay exactamente dos formas de que se envíe una dirección, y ambas las inicias
  tú. Fija la variable a tu propia dirección de contacto y resolver un
  `download` de artículo por `doi` consultará la API de
  [Unpaywall](https://unpaywall.org) (`api.unpaywall.org`) con esa dirección
  como parámetro, que es lo que su API exige. O déjala sin definir: un cliente
  compatible con la elicitación de MCP puede entonces ofrecerte pedir una
  dirección puntual para esa única llamada, que se usa solo para esa petición,
  nunca se escribe en disco y nunca se reutiliza — y el aviso se omite por
  completo cuando se ha fijado `source` de forma explícita. Si lo rechazas, la
  petición continúa sin Unpaywall. No se envía ningún otro dato personal.
- **Proveedores de acceso abierto sin clave (solo cuando pides un artículo por
  DOI).** Antes de cualquier alternativa de shadow library, la cadena de
  `download` de artículos pregunta a los repositorios abiertos por una copia con
  licencia libre: [OpenAlex](https://openalex.org) (`api.openalex.org`, y
  después el host del repositorio o del editor que nombra su enlace al PDF de
  acceso abierto), [Europe PMC](https://europepmc.org) (`ebi.ac.uk`, con el PDF
  en sí descargado de los PMC Article Datasets del NCBI en
  `pmc-oa-opendata.s3.amazonaws.com`), [bioRxiv/medRxiv](https://www.biorxiv.org)
  (`api.biorxiv.org`, más los hosts de contenido `biorxiv.org`/`medrxiv.org`), el
  [RFC Editor](https://www.rfc-editor.org) (`www.rfc-editor.org`) para un DOI de
  RFC, [NIST](https://nvlpubs.nist.gov) para un DOI `10.6028` (la petición va a
  `doi.org`, cuya redirección lleva a `nvlpubs.nist.gov`),
  [Schloss Dagstuhl](https://drops.dagstuhl.de) (`drops.dagstuhl.de`) para un DOI
  `10.4230`, la [ACL Anthology](https://aclanthology.org) (`aclanthology.org`)
  para un DOI `10.18653`/`10.3115`, [Zenodo](https://zenodo.org) (`zenodo.org`)
  para un DOI `10.5281/zenodo`, [SciELO](https://www.scielo.br) para un DOI
  `10.1590` (la petición va a `doi.org`, cuya redirección lleva a
  `www.scielo.br`), el
  [FAO Knowledge Repository](https://openknowledge.fao.org)
  (`openknowledge.fao.org`) para un DOI `10.4060` e
  Internet Archive Scholar / fatcat (`scholar.archive.org`, y después
  `web.archive.org` para el fichero). Tras ellos, el DOI va a
  [Crossref](https://www.crossref.org) (`api.crossref.org`) en busca del enlace
  al texto completo que el editor depositó allí, que se obtiene después del host
  de ese mismo editor. Un DOI de monografía se ofrece además a
  [OAPEN](https://library.oapen.org) (`library.oapen.org`). Cada petición lleva
  únicamente el DOI, con dos añadidos que controlas tú: una petición a OpenAlex
  lleva tu `LIBGEN_MCP_OPENALEX_KEY` como bearer token cuando fijas una, y la
  petición a Crossref lleva tu `LIBGEN_MCP_UNPAYWALL_EMAIL` como contacto de su
  polite pool cuando fijas esa.
- **Fuentes de libros de acceso abierto (solo cuando pides un libro por ISBN).**
  Un `download` por `isbn` envía **solo ese ISBN** a
  [OAPEN](https://library.oapen.org) (`library.oapen.org`) y a
  [OpenLibrary](https://openlibrary.org) (`openlibrary.org`), a la que se
  pregunta qué escaneos de [Internet Archive](https://archive.org) contienen el
  libro; los escaneos candidatos se confirman después y se obtienen de
  `archive.org` (cuya URL de descarga redirige a uno de sus propios nodos CDN).
  En ninguna de estas peticiones interviene cuenta, clave ni dirección de
  contacto alguna.
- **CORE (solo cuando pides un artículo por DOI y configuras una clave).**
  `LIBGEN_MCP_CORE_KEY` está vacía por defecto, lo que deja la fuente `core`
  fuera de la cadena. Cuando la fijas, el DOI se envía a `api.core.ac.uk` con la
  clave como bearer token; la clave nunca se adjunta a la URL de fichero que CORE
  devuelve.
- **Mirrors de Sci-Hub (solo cuando pides un artículo por DOI).** Si ninguno de
  los proveedores de acceso abierto anteriores da una copia, la cadena de
  `download` de artículos cae hacia los hosts de Sci-Hub configurados
  (`LIBGEN_MCP_SCIHUB_HOSTS`, p. ej. `sci-hub.ee`), pidiendo
  `https://<host>/<doi>` hasta que uno sirva el artículo.
- **Los buscadores adicionales (cuando una búsqueda va más allá del catálogo).**
  Un `search` puede enviar **el texto de tu consulta** a Anna's Archive
  (`annas-archive.gl` y sus mirrors), [arXiv](https://arxiv.org),
  [OpenAlex](https://openalex.org) (`api.openalex.org`),
  [Europe PMC](https://europepmc.org) (su servicio de búsqueda en
  `www.ebi.ac.uk`), [Crossref](https://www.crossref.org), [OpenLibrary](https://openlibrary.org),
  Project Gutenberg a través de la API de terceros
  [Gutendex](https://gutendex.com) (`gutendex.com`; los ficheros de libro a los
  que enlaza viven en `gutenberg.org`, que solo se contacta si obtienes uno),
  [dblp](https://dblp.org) (su servicio SPARQL, `sparql.dblp.org`),
  [PubMed](https://pubmed.ncbi.nlm.nih.gov) (`eutils.ncbi.nlm.nih.gov`) y
  [ERIC](https://eric.ed.gov) (`api.ies.ed.gov`). Cuándo ocurre esto está bajo tu
  control, mediante el argumento `extra_sources` o `LIBGEN_MCP_EXTRA_SOURCES`:
  por defecto (`auto`) solo cuando el catálogo de Library Genesis no devuelve
  nada o falla, con `always` en cada búsqueda, y con `never` nunca. Cuando — y
  solo cuando — has configurado `LIBGEN_MCP_UNPAYWALL_EMAIL`, la petición a
  Crossref lleva esa misma dirección como contacto de su polite pool, la petición
  a OpenLibrary la lleva en su User-Agent como pide la etiqueta de OpenLibrary, y
  las peticiones a PubMed la llevan como la dirección de contacto que pide la
  etiqueta de uso de NCBI; sin dirección configurada, no se envía ninguna ni se
  inventa ninguna. La petición a OpenAlex lleva `LIBGEN_MCP_OPENALEX_KEY` como
  bearer token si fijas una, y nada parecido si no. Una búsqueda acotada con
  `year_from` o `year_to` envía esos años junto con la consulta. Un resultado de ERIC para un documento que ERIC aloja lleva
  una URL de texto completo en `files.eric.ed.gov`; ese host se nombra en el
  resultado pero **este servidor nunca lo contacta** — no se obtiene nada de él
  salvo que sigas el enlace tú mismo. `get_details` también consulta a Anna's
  Archive, enviando **solo el md5**, cuando el catálogo no tiene registro de él.
- **Servicios de metadatos (cuando `get_details` busca un registro).** Más allá
  del catálogo de Library Genesis, `get_details` envía solo lo que identifica la
  obra, y solo al servicio cuya respuesta necesita. Un registro que lleva DOI ve
  ese DOI comprobado contra [Crossref](https://www.crossref.org)
  (`api.crossref.org`), al que también se pregunta por un DOI que el catálogo no
  tiene. Una referencia pegada en `citation` se le envía **tal como la pegaste**,
  para encontrar el DOI que nombra. `enrich` pregunta a Crossref por DOI y a
  [OpenLibrary](https://openlibrary.org) (`openlibrary.org`) por ISBN. Un DOI que
  ni el catálogo ni Crossref conocen, y cada estilo de `cite_as` para un registro
  cuyo DOI quedó confirmado, se piden al resolvedor de DOI (`doi.org`), que
  reenvía la petición a la agencia que registró ese DOI, por ejemplo
  `api.crossref.org` para Crossref, `data.crosscite.org` para DataCite,
  `data.medra.org` para mEDRA, `japanlinkcenter.org` para JaLC, o
  `data-doi.airiti.com` para Airiti, al que doi.org envía por `http` sin cifrar.
  `related` pregunta a [OpenAlex](https://openalex.org) (`api.openalex.org`) por
  DOI, con tu `LIBGEN_MCP_OPENALEX_KEY` como bearer token si fijas una. Las
  peticiones a Crossref, a OpenLibrary y a `doi.org` llevan tu `LIBGEN_MCP_UNPAYWALL_EMAIL` como
  dirección de contacto en su User-Agent cuando configuraste una, y ninguna en
  otro caso. La dirección se queda en el servicio al que se envió: cuando una
  redirección sale de ese origen, incluida la de doi.org hacia una agencia de
  registro, el servidor la quita del User-Agent antes de seguirla, así que el
  host de la agencia recibe el nombre y la versión del producto y ninguna
  dirección. `LIBGEN_MCP_ENRICH=false` apaga todas estas consultas.
- **Anna's Archive y pasarelas IPFS (solo cuando descargas a través de ellas).**
  La fuente `scidb` resuelve un `download` de artículo por `doi` a través de
  Anna's Archive, y la fuente `annas` resuelve un `download` de libro por `md5`
  allí, y después obtiene el fichero de una pasarela IPFS pública (`dweb.link`,
  `w3s.link`, `ipfs.io`, `gateway.pinata.cloud`). Si fijas
  `LIBGEN_MCP_ANNAS_KEY` — o proporcionas una clave para una única llamada cuando
  se te pide — esa clave se envía a Anna's Archive para usar el nivel de descarga
  más rápido de tu suscripción. Se usa para esa petición y nunca se escribe en
  disco.

Estos servicios externos tratan tus consultas bajo sus propias políticas; el
mantenedor de este proyecto no tiene relación con ellos ni visibilidad sobre esas
peticiones. Puedes restringir qué fuentes de descarga participan con
`LIBGEN_MCP_SOURCES`, y a qué buscadores puede llegar un `search` con
`LIBGEN_MCP_EXTRA_SOURCES=never`. No hay otros destinos de red — ni comprobación
de actualizaciones, ni llamadas a casa.

## Credenciales

No se requiere ninguna. Library Genesis, sus mirrors y las fuentes de artículos y
búsqueda sin clave que se usan aquí no necesitan cuenta ni token. Tres
credenciales son opcionales:

- Una **clave de socio de Anna's Archive** (`LIBGEN_MCP_ANNAS_KEY`, o
  proporcionada para una única llamada mediante la elicitación de tu cliente),
  que desbloquea el nivel de descarga más rápido de ese sitio. Se envía solo a
  Anna's Archive, solo en una descarga que tú has pedido, y el servidor nunca la
  persiste.
- Una **clave de API de CORE** (`LIBGEN_MCP_CORE_KEY`, registro gratuito en
  core.ac.uk), que habilita la fuente de artículos de acceso abierto `core`. Se
  envía solo a `api.core.ac.uk`, y nunca junto a la URL de fichero que CORE
  resuelve.
- Una **clave de API de OpenAlex** (`LIBGEN_MCP_OPENALEX_KEY`, gratuita en
  openalex.org), que por sí sola no habilita nada: lleva cada petición a OpenAlex
  a la cuota diaria propia de la clave en lugar de la que OpenAlex concede a tu
  dirección. Se envía solo a `api.openalex.org`, como bearer token y nunca en una
  URL.

El email de contacto de Unpaywall (`LIBGEN_MCP_UNPAYWALL_EMAIL`) no es una
credencial — es una dirección de atribución que la API de Unpaywall exige — pero
es igualmente opcional y está sin definir por defecto.

## Almacenamiento local y descargas

- Las **descargas** se escriben solo en el directorio de destino local
  (`LIBGEN_MCP_DOWNLOAD_DIR`, por defecto `~/Downloads`, o el argumento `path`
  por llamada). Los ficheros se quedan en tu máquina; no se sube nada a ningún
  sitio.
- Los **logs** van solo a la salida de error estándar (recogidos, si acaso, por
  tu cliente MCP). El servidor no crea ninguna base de datos ni fichero de
  telemetría. Con [OpenTelemetry](#opentelemetry-si-lo-activas) activado, los
  registros por encima de un umbral INFO se envían además al colector que hayas
  configurado — sigue sin haber nada en disco, y sigue sin haber ningún sitio al
  que el mantenedor pueda llegar.
- **Caché de mirrors.** Las listas de mirrors descubiertos de Library Genesis y
  Anna's Archive se cachean en disco durante 24 horas, como `mirrors.json` y
  `annas-mirrors.json` bajo el directorio de caché del sistema
  (`~/.cache/libgen-mcp/` en Linux, `~/Library/Caches/libgen-mcp/` en macOS).
  Contienen únicamente URLs públicas de mirrors — ninguna consulta, ningún
  identificador y nada sobre ti. Borrarlas solo fuerza un descubrimiento nuevo en
  la siguiente llamada.
- **Ficheros temporales.** `read` descarga el fichero del que extrae texto a un
  directorio temporal en la máquina que ejecuta el servidor, de modo que páginas
  sucesivas de un mismo documento reutilizan una única descarga; esos ficheros se
  desalojan por un límite de tamaño y un TTL (`LIBGEN_MCP_READ_CACHE_BYTES` /
  `LIBGEN_MCP_READ_CACHE_TTL`) y se borran cuando el servidor termina. Un
  servidor que muere de golpe los deja hasta que el siguiente servidor que
  descargue un fichero para `read` (un libro o artículo identificado por `md5` o
  `doi`) en el mismo directorio temporal los borra, por poco tiempo que haya
  pasado desde que murió. Si ese directorio no admite un candado de fichero que
  vean todos los servidores que lo usan (en Linux, un directorio en NFS, SMB,
  9p, AFS o un sistema FUSE), el servidor lo indica en su registro y los
  ficheros de un servidor que muere de golpe se quedan hasta que alguien los
  borre. Una descarga interrumpida deja igualmente un fichero
  `.part` en el directorio de destino para que una llamada posterior pueda
  reanudarla.

## Endpoint alojado

Hay una instancia pública de este servidor en `https://mcp.jmrp.io/libgen`. Usarla
es opcional y nunca es lo predeterminado: nada la instala, y ninguna configuración
de este repositorio apunta ahí.

Lo que cambia al usarla es sencillo y conviene decirlo sin rodeos. Tus llamadas de
herramienta — los títulos, autores, DOI e identificadores que buscas — viajan por la
red hasta una máquina que opera el mantenedor de este proyecto, en lugar de quedarse
en la tuya. Las peticiones a Library Genesis y a las fuentes de acceso abierto las
hace entonces esa máquina y no la tuya, así que esos terceros ven su dirección en
lugar de la tuya.

Esa máquina puede además estar ejecutando la
[exportación de OpenTelemetry](#opentelemetry-si-lo-activas) del propio servidor
hacia un colector que controla quien la opera, que es lo único útil que esta
política puede decir al respecto: el valor por defecto del software es apagado, y
si un despliegue que no configuraste tú lo ha encendido, y con qué política de
identidad, es una pregunta para quien lo opera. La propia card de la instancia en
`GET /.well-known/mcp/server-card.json` publica la respuesta —si la telemetría está
encendida, qué registra cada señal activada y qué registra sobre quien llama—, así
que puede leerse en vez de preguntarse.

Esa instancia se opera como parte de [mcp.jmrp.io](https://mcp.jmrp.io/) y su
tratamiento de las peticiones se rige por [las políticas de ese servicio](https://mcp.jmrp.io/policies/),
no por esta, que describe el software. Este documento solo puede contarte qué hace el software; no puede prometer
nada en nombre de un servidor que no ejecutas tú.

Si lo que buscas es sensible para ti, ejecuta el servidor en local. Ese es todo el
consejo, y es la razón de que todas las vías de instalación de la documentación
lleven ahí primero.

## Retención y cesión de datos

Lo único que el servidor deja tras salir son los ficheros descritos en
[Almacenamiento local y descargas](#almacenamiento-local-y-descargas): lo que le
pediste descargar, la caché de mirrors de 24 horas y, solo si el servidor murió
de golpe en vez de pararse, los ficheros temporales de `read` que aún tenía.
Ninguno de ellos registra una consulta ni un identificador tuyo, más allá de los
nombres de los ficheros que elegiste obtener.
No comparte datos con terceros más allá de los destinos listados en [Flujos de
datos](#flujos-de-datos) — los mirrors de Library Genesis, los buscadores
adicionales a los que puede llegar un `search`, los servicios de metadatos a los
que pregunta `get_details`, y las fuentes de descarga de artículos y libros que
invocas. Con
[OpenTelemetry](#opentelemetry-si-lo-activas) activado existe un quinto destino, y
es uno que nombraste tú: el colector que configuraste, cuya retención te toca fijar
a ti.

## Uso responsable

Esta herramienta accede a mirrors de terceros de Library Genesis. Eres
responsable de respetar las leyes de derechos de autor y de propiedad intelectual
que apliquen en tu lugar de residencia. Úsala solo para contenido al que tengas
derecho legal de acceder.

## Preguntas frecuentes

### ¿Recoge libgen-mcp telemetría o analíticas?

No, salvo que lo actives tú, y nunca al mantenedor. No hay analíticas, ni informes
de fallos, ni backend propio; el servidor no crea ninguna base de datos ni ningún
fichero de telemetría, y registra en la salida de error estándar, donde tu cliente
MCP los recoge si es que los recoge. El mantenedor nunca recibe tus consultas, tus
descargas ni ninguna información de uso, configures lo que configures.

`LIBGEN_MCP_TELEMETRY` te permite a **ti** exportar trazas, métricas y registros
de OpenTelemetry a un colector que **tú** ejecutas; está apagado por defecto, el
único destino por defecto de los exportadores es tu propio `localhost`, y lo que
llevan describe operaciones y no lo que se buscó. Consulta
[OpenTelemetry, si lo activas](#opentelemetry-si-lo-activas).

### ¿Qué datos salen de mi máquina, y quién los recibe?

Solo los identificadores que pides, y solo al servicio al que se pregunta. Una
búsqueda envía el texto de tu consulta a un mirror de Library Genesis; una cita
que pegas en `get_details` se envía a Crossref para encontrar su DOI; una
descarga por DOI envía ese DOI a las fuentes de artículos de la cadena; una
descarga por ISBN envía ese ISBN a OAPEN y al Internet Archive. Todos los
destinos están listados en [Flujos de datos](#flujos-de-datos). No se envía nada
al mantenedor, y no hay conexiones en segundo plano: cada petición es
consecuencia directa de una llamada a una herramienta.

### ¿Almacena libgen-mcp mis credenciales?

No se requiere ninguna credencial, y ninguna se persiste. Las tres opcionales —
una clave de membresía de Anna's Archive, una clave gratuita de la API de CORE y
una clave gratuita de la API de OpenAlex — se leen del entorno y se envían solo al único servicio al que corresponden.
Una credencial proporcionada por llamada mediante la elicitación de tu cliente
se usa para esa única petición y nunca se escribe en disco.

### ¿Los archivos descargados se quedan en mi máquina?

Sí. Las descargas se escriben únicamente en el directorio de destino local
(`LIBGEN_MCP_DOWNLOAD_DIR`, por defecto `~/Downloads`, o el argumento `path` por
llamada) y no se sube nada a ningún sitio. La herramienta `read` extrae texto en
local de un archivo que ya tienes.

## Cambios

Los cambios en esta política se publican en este fichero y se anotan en los
changelogs de las releases.

## Contacto

Dudas o problemas: [abre una incidencia](https://github.com/jmrplens/libgen-mcp/issues)
o escribe a <mail@jmrp.io>.
