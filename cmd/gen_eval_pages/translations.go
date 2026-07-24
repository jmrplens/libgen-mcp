package main

// scenariosES is the Spanish description of each scenario, keyed by id.
//
// The translations live here rather than on the page because the page is
// generated: a scenario added to the evaluator without one fails the build, which
// is the point. Keeping both languages beside the generator also keeps them
// describing the same scenario, which drifted while each was edited by hand.
//
// The English text is the evaluator README's, which is where descriptions are
// written and reviewed; this is its translation.
var scenariosES = map[string]string{
	"S1":  "Búsqueda de libros: colección nonfiction, columnas título/autor, el primer resultado tiene un md5 de 32 hex",
	"S2":  "Búsqueda de artículos: colección articles, al menos un resultado con DOI válido",
	"S3":  "Búsqueda de normas (SKIP si el mirror devuelve 0)",
	"S4":  "`get_details` sobre un md5 tomado de una búsqueda previa",
	"S5":  "Descarga de libro por md5: ruta guardada y tamaño distinto de cero",
	"S6":  "Descarga **eligiendo fuente**: el modelo fija `source:\"scihub\"` para un DOI de pago",
	"S6b": "Descarga eligiendo fuente de libro: el modelo fija `source:\"randombook\"` para un md5",
	"S7":  "Artículo de acceso abierto por DOI vía unpaywall (necesita email de contacto)",
	"S8":  "«Encuéntrame un buen libro», ambiguo — pasa si el modelo pide aclaración o la herramienta lo rechaza",
	"S9":  "**Reintentos de arranque**: sci-hub fijado a un host muerto, así que la tanda de reintentos se agota y la herramienta debe devolver el error accionable — y el modelo no debe inventar un éxito",
	"S10": "**Búsqueda de libro sin guía** («quiero leer _Dune_…») — el modelo debe formar la búsqueda desde una petición desnuda, sin pistas de colección ni campos",
	"S11": "**Búsqueda sin guía, cómics** («encuentra la novela gráfica _Watchmen_») — comprueba si el modelo descubre la colección correcta sin ayuda",
	"S12": "**Descarga de libro sin guía** («descarga _Clean Code_…») — el modelo debe buscar y luego descargar por un md5 que ha descubierto, eligiendo él la fuente",
	"S13": "**Descarga de artículo sin guía** («consígueme un PDF de _Hallmarks of Cancer_») — el modelo debe descubrir que los artículos se identifican por DOI, no por md5",
	"S14": "**Progreso de descarga** — adjunta un token de progreso y comprueba que las notificaciones llegan de verdad al cliente, de extremo a extremo",
	"S15": "**Tabla ordenada con enlaces** — petición grande y ordenada; comprueba que el modelo fija un tamaño de página grande más ordenación e incluye los enlaces de descarga en su respuesta (el next_steps de la herramienta se lo indica)",
	"S16": "**Enlace sin descargar** («dame la URL directa, no lo descargues») — comprueba que el modelo fija `resolve_only=true` y que la herramienta devuelve una URL (como `resource_link`) en lugar de un fichero — la vía de entrega remota/hosteada",
	"S17": "**Descarga remota (libro)** — la misma petición de descarga, pero contra un servidor arrancado en modo **remoto** (`--http`): `download` devuelve un enlace en lugar de guardar, y el arnés — haciendo de herramienta de fetch del agente — lo descarga a disco local",
	"S18": "**Descarga remota (artículo)** — lo mismo para un DOI de pago: el modelo llama a `download`, el servidor remoto devuelve un enlace y el arnés lo descarga en local",
	"S19": "**Buscar → leer → resumir**: el modelo busca un artículo por título, llama a `read` (no a `download`) con el DOI hallado en los resultados y escribe su propio resumen de la primera página extraída en lugar de volcar el texto NO CONFIABLE literal",
	"S20": "**Descubrimiento de acceso abierto** — sin especificar, como S10–S13: el prompt pide «consultar también la literatura de acceso abierto» sin nombrar `extra_sources`; el modelo debe fijarlo a `always` por su cuenta y citar uno de los resultados federados de arXiv/Crossref en su respuesta (SKIP si los proveedores sin clave no devuelven nada en vivo)",
	"S21": "**Citas** — pide una cita BibTeX; el modelo debe llegar a `get_details` (que la construye) en vez de fabricarla",
	"S22": "**Enriquecimiento** — pide la revista y el número de citas de un DOI de pago, así que el modelo debe fijar `enrich=true` en `get_details` para traer los metadatos de Crossref",
	"S23": "**Búsqueda dentro del documento** — pide buscar _dentro_ de un libro, así que el modelo debe llamar a `read` con un argumento `find` en lugar de descargar el fichero entero",
	"S24": "**Índice** — pide la tabla de contenidos de un libro, así que el modelo debe llamar a `read` con `outline=true`",
	"S25": "**Email de Unpaywall por elicitación** — el email del despliegue se fuerza vacío, así que la descarga solo puede tener éxito con el email por llamada que suministra el manejador de elicitación del host",
	"S26": "**Confirmación de guardado por elicitación** — una descarga que escribe a disco debe levantar la confirmación; el host cuenta las confirmaciones que responde, así que la aserción es dura, no inferida",
	"S27": "**Búsqueda en documento, remoto** — S23 contra un servidor en modo remoto",
	"S28": "**Índice, remoto** — S24 contra un servidor en modo remoto",
	"S29": "**Descubrimiento de acceso abierto, remoto** — S20 contra un servidor en modo remoto, formulado como una petición de investigación abierta",
	"S30": "**Enriquecimiento, remoto** — S22 contra un servidor en modo remoto",
	"S31": "**Citas, remoto** — S21 contra un servidor en modo remoto",
	"S32": "**Escalada de búsqueda** — el título es uno que el catálogo de Library Genesis no tiene, así que un acierto solo puede venir de la escalada automática a Anna's Archive; el modelo debe decir que lo ha encontrado sin que se le pida usar fuentes adicionales",
	"S33": "**Escalada de búsqueda, remoto** — S32 contra un servidor en modo remoto",
	"S34": "**Escalada → descarga** — el mismo título ausente del catálogo, pero el modelo debe además descargarlo, probando que un resultado escalado lleva un md5 que la herramienta `download` acepta",
	"S35": "**Escalada → descarga, remoto** — S34 contra un servidor en modo remoto: `download` devuelve un enlace y el arnés lo descarga en local",
	"S36": "**Consulta de registro escalado** — el modelo debe seguir la búsqueda escalada con `get_details` sobre un md5 del que el catálogo no tiene registro, que solo puede responderse por el respaldo de Anna's; se evalúa por el `origin` del registro",
	"S37": "**Consulta de registro escalado, remoto** — S36 contra un servidor en modo remoto",
	"S38": "**Un despliegue en never es un cierre** — el valor por defecto del servidor es `never` y el prompt es un fallo conocido del catálogo; se evalúa que las fuentes adicionales queden fuera de los resultados _y_ que el modelo reporte el fallo en vez de inventarse uno",
	"S39": "**Un despliegue en always fuerza las adicionales** — una consulta ordinaria que el catálogo responde de sobra; los resultados de origen adicional solo pueden estar ahí porque el valor por defecto del despliegue los forzó",
	"S40": "**Leer un ítem escalado** — la más estricta de las comprobaciones de escalada: la búsqueda, la ruta de descarga de Anna's, el tipo de fichero y la extracción de texto tienen que sostenerse todas para que el modelo cite un pasaje",
	"S41": "**Opt-in de membresía de Anna's** — el prompt menciona tener cuenta sin nombrar `annas_member`, así que el modelo debe descubrir el argumento; la clave llega por elicitación y no se almacena nunca",
	"S42": "**No existe nada con ese nombre** — un libro y un autor inventados para esta prueba, así que toda llamada sale vacía y la única respuesta correcta es decirlo; se evalúa la admisión _y_ que no aparezca igualmente un ISBN o un número de páginas",
	"S43": "**Un despliegue restringido aguanta** — `LIBGEN_MCP_SOURCES` permite solo el catálogo, así que la descarga por DOI debe rechazarse; se evalúa el rechazo y que nada fuera de la lista haya servido el fichero, sea cual sea la ruta que el modelo encuentre después",
	"S44": "**Paginación** — pide la segunda página de resultados, así que el modelo debe descubrir el argumento `page` en lugar de repetir la misma búsqueda o continuar la lista de memoria",
}
