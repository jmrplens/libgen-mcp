---
title: Política de privacidad
description: "Qué maneja libgen-mcp y a dónde va: sin telemetría, sin analítica, y cada destino de red listado herramienta por herramienta."
datePublished: "2026-07-25"
# Traducción de PRIVACY.md. El digest de abajo fija la versión del original de la
# que procede: scripts/sync-privacy.mjs --check falla cuando el original cambia y
# esta traducción se queda atrás.
privacySource: "2c0da46466882194"
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
              "text": "No. El servidor no tiene telemetría, ni analíticas, ni informes de fallos, ni backend propio. No crea ninguna base de datos ni ningún fichero de telemetría, y registra únicamente en la salida de error estándar, donde tu cliente MCP los recoge si es que los recoge. El mantenedor nunca recibe tus consultas, tus descargas ni ninguna información de uso."
            }
          },
          {
            "@type": "Question",
            "name": "¿Qué datos salen de mi máquina, y quién los recibe?",
            "acceptedAnswer": {
              "@type": "Answer",
              "text": "Solo los identificadores que pides, y solo al servicio al que se pregunta. Una búsqueda envía el texto de tu consulta a un mirror de Library Genesis; una descarga por DOI envía ese DOI a las fuentes de artículos de la cadena; una descarga por ISBN envía ese ISBN a OAPEN y al Internet Archive. Todos los destinos están listados en Flujos de datos. No se envía nada al mantenedor, y no hay conexiones en segundo plano: cada petición es consecuencia directa de una llamada a una herramienta."
            }
          },
          {
            "@type": "Question",
            "name": "¿Almacena libgen-mcp mis credenciales?",
            "acceptedAnswer": {
              "@type": "Answer",
              "text": "No se requiere ninguna credencial, y ninguna se persiste. Las dos opcionales — una clave de membresía de Anna's Archive y una clave gratuita de la API de CORE — se leen del entorno y se envían solo al único servicio al que corresponden. Una credencial proporcionada por llamada mediante la elicitación de tu cliente se usa para esa única petición y nunca se escribe en disco."
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

**libgen-mcp** es un servidor Model Context Protocol (MCP) local. Se ejecuta
enteramente en tu máquina y actúa como puente entre tu cliente MCP (Claude
Desktop, Claude Code, Cursor, VS Code, …) y los mirrors públicos de Library
Genesis. No necesita **ninguna cuenta, ningún token y ninguna credencial**. Esta
política describe qué datos maneja el servidor y a dónde van.

## Qué recopilamos

**Nada.** El servidor no tiene telemetría, ni analítica, ni informes de fallos,
ni backend propio. No hay ninguna cuenta que crear ni nada a lo que iniciar
sesión. El mantenedor nunca recibe, almacena ni tiene acceso a ninguno de tus
datos ni a tu información de uso.

## Flujos de datos

Cada petición de red es consecuencia directa de una llamada de herramienta que tú
(a través de tu asistente de IA) haces. No hay conexiones en segundo plano. Los
destinos son:

- **Mirrors de Library Genesis.** `search` y `get_details` consultan los mirrors
  de Library Genesis (por ejemplo `libgen.li`, `libgen.gl`, `libgen.la`,
  `libgen.bz`, `libgen.vg`), que se descubren automáticamente y se cachean, o se
  fijan con `LIBGEN_MIRROR`. Las peticiones de `download` de libros (por `md5`)
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
  DOI).** Antes de cualquier alternativa de biblioteca en la sombra, la cadena de
  `download` de artículos pregunta a los repositorios abiertos por una copia con
  licencia libre: [Europe PMC](https://europepmc.org) (`ebi.ac.uk`,
  `europepmc.org`), [bioRxiv/medRxiv](https://www.biorxiv.org)
  (`api.biorxiv.org`, más los hosts de contenido `biorxiv.org`/`medrxiv.org`) e
  Internet Archive Scholar / fatcat (`scholar.archive.org`, y después
  `web.archive.org` para el fichero). Un DOI de monografía se ofrece además a
  [OAPEN](https://library.oapen.org) (`library.oapen.org`). Cada petición lleva
  únicamente el DOI.
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
  [Crossref](https://www.crossref.org), [OpenLibrary](https://openlibrary.org),
  Project Gutenberg a través de la API de terceros
  [Gutendex](https://gutendex.com) (`gutendex.com`; los ficheros de libro a los
  que enlaza viven en `gutenberg.org`, que solo se contacta si obtienes uno),
  [dblp](https://dblp.org) (`dblp.org`),
  [PubMed](https://pubmed.ncbi.nlm.nih.gov) (`eutils.ncbi.nlm.nih.gov`) y
  [ERIC](https://eric.ed.gov) (`api.ies.ed.gov`). Cuándo ocurre esto está bajo tu
  control, mediante el argumento `extra_sources` o `LIBGEN_MCP_EXTRA_SOURCES`:
  por defecto (`auto`) solo cuando el catálogo de Library Genesis no devuelve
  nada o falla, con `always` en cada búsqueda, y con `never` nunca. Cuando — y
  solo cuando — has configurado `LIBGEN_MCP_UNPAYWALL_EMAIL`, la petición a
  Crossref lleva esa misma dirección como contacto de su polite pool, y las
  peticiones a PubMed la llevan como la dirección de contacto que pide la
  etiqueta de uso de NCBI; sin dirección configurada, no se envía ninguna ni se
  inventa ninguna. Un resultado de ERIC para un documento que ERIC aloja lleva
  una URL de texto completo en `files.eric.ed.gov`; ese host se nombra en el
  resultado pero **este servidor nunca lo contacta** — no se obtiene nada de él
  salvo que sigas el enlace tú mismo. `get_details` también consulta a Anna's
  Archive, enviando **solo el md5**, cuando el catálogo no tiene registro de él.
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
búsqueda sin clave que se usan aquí no necesitan cuenta ni token. Dos
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
  telemetría.
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
  `LIBGEN_MCP_READ_CACHE_TTL`). Una descarga interrumpida deja igualmente un
  fichero `.part` en el directorio de destino para que una llamada posterior
  pueda reanudarla.

## Retención y cesión de datos

Lo único que el servidor deja tras salir son los ficheros descritos en
[Almacenamiento local y descargas](#almacenamiento-local-y-descargas): lo que le
pediste descargar, la caché de mirrors de 24 horas y los ficheros temporales de
`read` aún no desalojados. Ninguno de ellos registra una consulta ni un
identificador tuyo, más allá de los nombres de los ficheros que elegiste obtener.
No comparte datos con terceros más allá de los destinos listados en [Flujos de
datos](#flujos-de-datos) — los mirrors de Library Genesis, los buscadores
adicionales a los que puede llegar un `search`, y las fuentes de descarga de
artículos y libros que invocas.

## Uso responsable

Esta herramienta accede a mirrors de terceros de Library Genesis. Eres
responsable de respetar las leyes de derechos de autor y de propiedad intelectual
que apliquen en tu lugar de residencia. Úsala solo para contenido al que tengas
derecho legal de acceder.

## Preguntas frecuentes

### ¿Recoge libgen-mcp telemetría o analíticas?

No. El servidor no tiene telemetría, ni analíticas, ni informes de fallos, ni
backend propio. No crea ninguna base de datos ni ningún fichero de telemetría, y
registra únicamente en la salida de error estándar, donde tu cliente MCP los
recoge si es que los recoge. El mantenedor nunca recibe tus consultas, tus
descargas ni ninguna información de uso.

### ¿Qué datos salen de mi máquina, y quién los recibe?

Solo los identificadores que pides, y solo al servicio al que se pregunta. Una
búsqueda envía el texto de tu consulta a un mirror de Library Genesis; una
descarga por DOI envía ese DOI a las fuentes de artículos de la cadena; una
descarga por ISBN envía ese ISBN a OAPEN y al Internet Archive. Todos los
destinos están listados en [Flujos de datos](#flujos-de-datos). No se envía nada
al mantenedor, y no hay conexiones en segundo plano: cada petición es
consecuencia directa de una llamada a una herramienta.

### ¿Almacena libgen-mcp mis credenciales?

No se requiere ninguna credencial, y ninguna se persiste. Las dos opcionales —
una clave de membresía de Anna's Archive y una clave gratuita de la API de CORE
— se leen del entorno y se envían solo al único servicio al que corresponden.
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
