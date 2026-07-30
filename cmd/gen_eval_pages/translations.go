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
	"S45": "**Europe PMC** — pide un DOI de acceso abierto «desde Europe PMC» con Unpaywall desactivado, así que el modelo debe traducir el nombre en prosa del proveedor a `source:\"europepmc\"` y esa fuente debe servir los bytes",
	"S46": "**bioRxiv** — un preprint real con prefijo `10.1101`, sin nombrar fuente y con Unpaywall desactivado: Europe PMC indexa el DOI pero no tiene su texto completo en acceso abierto, así que la única vía por la que puede llegar el fichero es que bioRxiv reclame el prefijo de preprint, y la fuente que sirve demuestra que la puerta por prefijo encamina en vez de dejar pasar",
	"S47": "**fatcat** — el mismo DOI de acceso abierto pedido «desde fatcat»; la fuente usa el frontend de Internet Archive Scholar desde que murió su API JSON, así que resuelve de verdad y la copia preservada tiene que llegar",
	"S48": "**Una fuente sin clave se queda fuera de la superficie** — la clave de API de CORE se fuerza vacía, así que `core` debe estar ausente del enum `source` de `download` y el modelo no debe pedirla. Evalúa la propia superficie de herramientas, así que no toca ningún tercero ni descarga nada",
	"S49": "**Orden de la cadena** — un DOI de acceso abierto sin nombrar fuente y con Unpaywall desactivado: debe servirlo uno de los proveedores de acceso abierto y no una biblioteca en la sombra, que es la promesa que hace el orden de la cadena y que nada más comprobaba",
	"S50": "**Un libro por su ISBN** — se pide una copia legalmente gratuita de una novela sin nombrar ni el argumento ni la fuente; el modelo debe descubrir que `download` acepta un `isbn`, y la cadena debe encaminarlo más allá de OAPEN hasta el escaneo de Internet Archive",
	"S51": "**OAPEN por DOI** — una monografía de acceso abierto pedida «desde OAPEN», así que el modelo traduce el nombre en prosa a `source:\"oapen\"` y la fuente sirve el PDF",
	"S52": "**OAPEN por ISBN** — la misma monografía por el otro identificador que acepta la fuente, que es lo que prueba que la clave ISBN resuelve y no solo que se acepta",
	"S53": "**OAPEN no sirve el libro equivocado** — un DOI que OAPEN no tiene; su búsqueda es texto libre, así que responde con una página de monografías no relacionadas y la fuente debe rechazarlas todas en vez de entregar el primer resultado",
	"S54": "**Internet Archive por ISBN** — una novela de dominio público pedida «desde Internet Archive», alcanzada vía OpenLibrary; el fichero que llegue debe ser un escaneo real, no una página de préstamo",
	"S55": "**Un libro restringido a préstamo se rechaza** — un libro que Archive solo tiene en préstamo; un ítem de préstamo anuncia ficheros PDF/EPUB normales, así que un fichero que llegara aquí estaría cifrado con DRM o truncado y el único resultado correcto es un rechazo limpio que el modelo traslade",
	"S56": "**Project Gutenberg** — un ebook de dominio público cuyo resultado lleva un `full_text_url` y ningún identificador que `download` acepte, así que el modelo debe darle el enlace al usuario en lugar de decir que no se puede obtener",
	"S57": "**ERIC** — literatura gris educativa (informes de agencias, sin DOI) cuyo texto completo alojado viaja en `pdf_url`, con la misma forma «lo descarga quien pregunta» que un ebook de Gutenberg",
	"S58": "**dblp** — una consulta de informática a la que el índice bibliográfico debería aportar metadatos de congresos; dblp limita el tráfico de forma agresiva y no documentada y su latencia crece con la consulta, así que una ejecución en la que no participe se marca como SKIP, nunca como fallo",
	"S59": "**PubMed** — el equivalente biomédico: una aportación de índice, citada como registro en lugar de ofrecerse como texto completo gratuito",
	"S60": "**Enfriamiento por fuente** — sci-hub encabeza una cadena de dos fuentes con un host muerto, y la confirmación de guardado sondea antes el tamaño del fichero, así que la cadena se recorre dos veces dentro de una misma llamada: el primer recorrido debe clasificar el fallo como fuente no disponible y el segundo debe actuar en consecuencia. Se evalúa desde el log de servidor de la propia llamada",
	"S61": "**Un RFC solo con su número** — el prompt nombra el RFC 9110 como lo haría una persona y nunca dice «DOI», así que el modelo debe saber que un RFC se alcanza como DOI `10.17487` y construirlo; nada más en la cadena responde a ese prefijo, de modo que la llegada del fichero demuestra a la vez que el modelo encontró la puerta y que la compuerta enrutó",
	"S62": "**NIST por DOI** — la contraparte de enrutado con el DOI dado: evalúa la compuerta `10.6028` y, a través de ella, que la redirección doi.org → nvlpubs sobre la que se apoya la fuente siga terminando en un PDF y no en una página de aterrizaje",
	"S63": "**El RFC Editor por su nombre** — el mismo documento pedido «desde la fuente RFC Editor», de modo que el modelo mapea el nombre en prosa a `source:\"rfc\"` en lugar de dejar que enrute la cadena",
	"S64": "**Leer un RFC como texto** — las demás fuentes por DOI entregan PDF; esta es la única vía en la que un DOI llega a `extract` como texto plano y pagina por desplazamiento de caracteres, y el modelo debe citar el documento en vez de limitarse a llamar a la herramienta",
	"S65": "**Las fuentes de normas se anuncian** — no toca a ningún tercero: una fuente con compuerta de prefijo puede correr en la cadena y faltar en el enum que se muestra al modelo, lo que la hace alcanzable por el servidor e invisible para quien llama",
	"S66": "**Dagstuhl por DOI** — el DOI se da hecho, así que lo que se evalúa es el análisis de la página de aterrizaje que la fuente no puede evitar: los ficheros de DROPS viven bajo una ruta de almacenamiento que incluye el número de volumen, que el DOI no lleva, de modo que la URL del PDF sale del propio `citation_pdf_url` de la página o no sale de ninguna parte",
	"S67": "**La ACL Anthology por su nombre, sobre un identificador con letra de volumen** — el nombre en prosa mapeado a `source:\"acl\"` y, detrás, la regla de mayúsculas: el identificador de este DOI lleva letra de volumen, la Anthology responde 404 a la grafía en minúsculas, así que la llegada del fichero es la única prueba de que el sufijo se pasó a mayúsculas",
	"S68": "**Un DOI de concepto de Zenodo** — el identificador que Zenodo entrega para citar todas las versiones de un depósito no tiene listado de ficheros propio y responde 404, de modo que aprobar significa que la fuente lo detectó y preguntó a la página del registro qué versión servir; sin ese salto, cerca de la mitad de los DOI de Zenodo no resuelven a nada",
	"S69": "**SciELO sobre un artículo reciente** — un artículo de 2025 que Unpaywall marca como de acceso abierto sin aportar enlace al PDF y que fatcat aún no ha ingerido, así que ninguna otra fuente de la cadena puede producir bytes; lo que se evalúa es el aterrizaje del resolutor, ya que ningún identificador que tenga quien llama predice la dirección de la página del artículo y la URL del PDF sale del propio `citation_pdf_url` de esa página",
	"S70": "**El FAO Knowledge Repository por DOI** — la página del ítem anuncia una ruta del frontend Angular que entrega a un cliente HTTP simple 372 862 bytes del armazón de la aplicación en lugar del fichero, así que la fuente la reescribe al endpoint de bitstream del backend; una regresión ahí devuelve HTML con un 200, que la canalización rechaza, de modo que la llegada de un PDF es la prueba",
	"S71": "**Unpaywall por su nombre** — la cabecera de la cadena de artículos y la única fuente que ningún escenario había fijado: S7 parece cubrirla y no lo hace, porque evalúa a cualquier proveedor de acceso abierto que gane la carrera, así que desde la llegada de Europe PMC cualquier ejecución pudo haberla servido otro sin que nada lo dijera",
	"S72": "**SciDB** — la única entrada de `config.KnownSources` a la que no llegaba ningún escenario. Aparece en media docena de listas de fuentes como aquello que _no_ debe ganar, y una fuente evaluada solo como perdedora es indistinguible de una que no puede funcionar",
	"S73": "**La confirmación de guardado, renunciada** — el prompt dice que la descarga ya está aprobada, así que el modelo debe descubrir `skip_confirmation`; el host cuenta cada confirmación que responde, de modo que «no se levantó ninguna» es evidencia dura y no una inferencia. Es el único argumento cuyo efecto es retirar una salvaguarda, y S26 solo probaba que la salvaguarda salta",
	"S74": "**Seguir leyendo más allá del primer fragmento** — los demás escenarios de lectura toman un fragmento y paran. El modelo debe continuar desde el cursor que le devolvió `read` y recibir texto distinto; un texto idéntico al primer fragmento es el fallo, porque es lo que produce repetir la misma llamada y la respuesta que alimenta parece exactamente una continuación",
	"S75": "**Una fuente con clave se anuncia** — S48 leído al revés: con una clave de CORE configurada, `core` debe aparecer en el enum `source` de `download`. S48 por sí solo lo satisface una compuerta atascada en cerrado, que es idéntica a una compuerta que funciona; la pareja dice que el enum sigue al despliegue. No toca a ningún tercero",
}

// The Spanish summary sentences that carry the run's counts. They live beside the
// scenario translations rather than in main.go because they are Spanish prose,
// and .golangci.yml excludes this file from the English spell checker.
const (
	scenarioSummaryES = "La suite son **%d escenarios** (%s%s). %d de ellos ejercitan un servidor en modo remoto (`--http`); el resto lo ejecutan sobre stdio."
	resultsSummaryES  = "La tabla siguiente es %s contra `%s` (API real de Anthropic, mirrors reales, descargas reales): **%d pasaron, %d fallaron, %d en SKIP** %s%s"

	// El alcance del recuento: la suite entera, o la parte de ella ya medida.
	remoteShareES   = "incluidos los %d que se ejecutan contra un servidor en modo remoto (`--http`)."
	scopeAllES      = "de %d — todos los escenarios, %s"
	scopeMeasuredES = "de los %d medidos hasta ahora, %s %s"

	// Los escenarios listados arriba que aún no tienen fila en la tabla.
	unmeasuredOneES  = "Un escenario de la lista anterior aún no tiene fila aquí: se añadió después de la última ejecución en vivo, y un resultado solo se publica una vez medido."
	unmeasuredManyES = "%d escenarios de la lista anterior aún no tienen fila aquí: se añadieron después de la última ejecución en vivo, y un resultado solo se publica una vez medido."

	// Las variantes de la frase anterior según cuándo se midieron las filas.
	measuredSpanOneES   = "una única ejecución en vivo de la suite completa el %s"
	measuredSpanRangeES = "un conjunto de ejecuciones en vivo entre el %s y el %s"
	measuredTailRangeES = " Cada fila lleva la fecha en que se midió por última vez: una ejecución parcial solo refresca los escenarios que ejecutó."
	measuredSpanNoneES  = "una única ejecución en vivo de la suite completa"

	// Singular and plural tails for the lettered scenario variants. Spanish puts
	// the noun before the id, so each template takes the joined ids.
	variantSuffixES       = " más la variante %s"
	variantSuffixPluralES = " más las variantes %s"
)
