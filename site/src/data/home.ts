/**
 * The landing page's copy, one object per locale.
 *
 * Home.astro renders the structure; this file supplies the words. That split is
 * the whole point: with the two locales as two objects of one type, the English
 * and the Spanish landing cannot drift apart structurally — a block added to
 * one is a type error until it is added to the other. The page used to be two
 * MDX files carrying the same six sections in parallel by hand, with the chain
 * of twenty-one sources spelled out four times in each.
 *
 * Numbers are not written here. They are counted from src/data/sources.ts in
 * Home.astro, so a source added to the chain updates the landing by itself.
 */
import type { KeyedBy } from "./sources";

export interface Stat {
	value: string;
	label: string;
	href?: string;
}

export interface Step {
	title: string;
	body: string;
	code?: string;
	href: string;
	linkText: string;
}

export interface ChainGroup {
	/** Matches a SourceGroup in sources.ts; the ids come from there. */
	id: "resolvers" | "publishers" | "aggregators" | "books" | "fallbacks";
	name: string;
	summary: string;
}

export interface HomeContent {
	statsLabel: string;
	/** The three labels the readout pairs with counted values. */
	statLabels: {
		tools: string;
		sources: string;
		providers: string;
		keys: string;
	};
	statHrefs: {
		tools: string;
		sources: string;
		providers: string;
		keys: string;
	};
	what: { title: string; body: string[] };
	who: { title: string; items: string[] };
	honest: { title: string; items: string[] };
	proof: {
		title: string;
		lead: string;
		installTitle: string;
		installNote: string;
		install: string;
		promptsTitle: string;
		promptsNote: string;
		promptHeaders: [string, string];
		prompts: [string, string][];
	};
	chain: {
		title: string;
		lead: string;
		headers: [string, string];
		groups: ChainGroup[];
		keyedBy: Record<KeyedBy, string>;
		/** Explains the ringed chips. A tooltip cannot be the only carrier. */
		legend: string;
	};
	start: { title: string; lead: string; steps: Step[] };
}

const INSTALL =
	"claude mcp add libgen -- docker run -i --rm ghcr.io/jmrplens/libgen-mcp:latest";

export const en: HomeContent = {
	statsLabel: "libgen-mcp at a glance",
	statLabels: {
		tools: "MCP tools",
		sources: "download sources, in a fixed chain",
		providers: "search providers beyond the catalog",
		keys: "keys required",
	},
	statHrefs: {
		tools: "/libgen-mcp/tools/",
		sources: "/libgen-mcp/sources/",
		providers: "/libgen-mcp/how-search-works/",
		keys: "/libgen-mcp/configuration/",
	},
	what: {
		title: "What it is",
		body: [
			'<strong>libgen-mcp</strong> is a <a href="https://modelcontextprotocol.io">Model Context Protocol</a> server, written in Go, that lets an AI assistant find a book or a paper, cite it, fetch it and read it back to you. It talks to the <strong>Library Genesis</strong> catalog and to a long list of open-access sources, and it exposes the whole thing as four tools any MCP client can call.',
			'You talk to your assistant, not to the tools. It decides which one to call and with what arguments — and that decision is not asserted here, it is <a href="/libgen-mcp/eval-results/">measured against a live, graded evaluation</a>.',
			"Mirrors are discovered and cached automatically, with transparent failover, so the server keeps working as individual mirrors go up and down.",
		],
	},
	who: {
		title: "Who it is for",
		items: [
			"Anyone who reads papers and would rather ask for one in a sentence than assemble a DOI lookup by hand.",
			"People running an MCP client already — Claude Code, Claude Desktop, Cursor, VS Code, or their own agent.",
			"Anyone who wants the open-access copy first, and wants to be told when there isn't one.",
			"Anyone who does not want to create an account to search a library catalog.",
		],
	},
	honest: {
		title: "What it is not",
		items: [
			"Not a way around what you may not read. It refuses material it cannot redistribute, and reports a refusal as a miss rather than dressing it up as a file.",
			"Not a mirror or an index of its own. It queries other people's services and identifies itself while doing so.",
			"Not a guarantee. Third-party mirrors go down, and a source that has nothing for an identifier says so.",
			"Not a legal opinion. The copyright law that applies where you live is yours to observe.",
		],
	},
	proof: {
		title: "What it looks like in use",
		lead: "Two things, in the order you would meet them: getting it running, and then talking to it.",
		installTitle: "Install in one line",
		installNote:
			"Or point your client at a prebuilt binary — no account, API key or token at any step.",
		install: INSTALL,
		promptsTitle: "Then just ask",
		promptsNote:
			"Every prompt below is taken from the evaluation suite, where a real model is graded on handling it against the live site.",
		promptHeaders: ["Ask your assistant", "What libgen-mcp does"],
		prompts: [
			[
				"“I want to read <em>Dune</em> — can you find it?”",
				"Forms a catalog search unaided, with no collection or field hints",
			],
			[
				"“Download <em>Clean Code</em> for me”",
				"Searches, picks an md5 from the results, and chooses the download source itself",
			],
			[
				"“Get me a PDF of <em>Hallmarks of Cancer</em>”",
				"Works out that articles are keyed by DOI, not md5, and resolves it through the open-access chain",
			],
			[
				"“Find this paper and summarize the first page”",
				"Calls <code>read</code> instead of <code>download</code>, and summarizes the extracted text rather than dumping it",
			],
			[
				"“What does this book say about pointers?”",
				"Calls <code>read</code> with <code>find</code>, searching inside the document without fetching the whole file",
			],
			[
				"“Get me this book by its ISBN”",
				"Routes to OAPEN and public-domain Internet Archive scans — never a shadow-library fallback",
			],
			[
				"“Give me the direct link, don't download it”",
				"Sets <code>resolve_only</code> and returns a URL instead of writing a file",
			],
		],
	},
	chain: {
		title: "The chain, in order",
		lead: "A download tries each source that supports the item, in this order, and fails over to the next. The order is fixed in code: the legal open-access providers lead, and the shadow libraries are reached only after every one of them has declined.",
		headers: ["Stage", "Sources"],
		groups: [
			{
				id: "resolvers",
				name: "Open-access resolvers",
				summary:
					"Asked first, for any DOI: is there a legally free copy, and where?",
			},
			{
				id: "publishers",
				name: "Publisher-direct",
				summary:
					"Prefix-gated routes straight to a publisher that gives its own work away.",
			},
			{
				id: "aggregators",
				name: "Aggregators and preservation",
				summary:
					"Broad indexes and archives, for the DOIs the resolvers missed.",
			},
			{
				id: "books",
				name: "Open-access and public-domain books",
				summary:
					"The only two sources for an ISBN. Lending-restricted scans are refused.",
			},
			{
				id: "fallbacks",
				name: "Shadow-library fallbacks",
				summary:
					"Reached last, and only after everything above has come up empty.",
			},
		],
		keyedBy: {
			doi: "keyed by DOI",
			md5: "keyed by MD5",
			isbn: "keyed by ISBN",
			"doi-or-isbn": "keyed by DOI or ISBN",
		},
		legend:
			"A ringed source reads a credential. None is required: leave it unset and that source simply stays out of the chain, while every other one still runs.",
	},
	start: {
		title: "Start here",
		lead: "Three doors, depending on what you came for.",
		steps: [
			{
				title: "Install it and run a first search",
				body: "A prebuilt binary, Docker, or go install; then the mcpServers JSON for Claude Desktop, Cursor and VS Code.",
				code: INSTALL,
				href: "/libgen-mcp/getting-started/",
				linkText: "Getting Started",
			},
			{
				title: "Read what the tools actually do",
				body: "search, get_details, download and read — every input, every output field, and what each one does when it fails.",
				href: "/libgen-mcp/tools/",
				linkText: "Tools",
			},
			{
				title: "Decide what it may reach",
				body: "Every environment variable with its default and valid range — including which sources are in the chain at all.",
				href: "/libgen-mcp/configuration/",
				linkText: "Configuration",
			},
		],
	},
};

export const es: HomeContent = {
	statsLabel: "libgen-mcp de un vistazo",
	statLabels: {
		tools: "herramientas MCP",
		sources: "fuentes de descarga, en cadena fija",
		providers: "proveedores de búsqueda más allá del catálogo",
		keys: "claves necesarias",
	},
	statHrefs: {
		tools: "/libgen-mcp/es/tools/",
		sources: "/libgen-mcp/es/sources/",
		providers: "/libgen-mcp/es/how-search-works/",
		keys: "/libgen-mcp/es/configuration/",
	},
	what: {
		title: "Qué es",
		body: [
			'<strong>libgen-mcp</strong> es un servidor <a href="https://modelcontextprotocol.io">Model Context Protocol</a>, escrito en Go, que permite a un asistente de IA encontrar un libro o un artículo, citarlo, obtenerlo y leértelo en lenguaje natural. Habla con el catálogo de <strong>Library Genesis</strong> y con una larga lista de fuentes de acceso abierto, y lo expone todo como cuatro herramientas que puede llamar cualquier cliente MCP.',
			'Hablas con tu asistente, no con las herramientas. Él decide cuál llamar y con qué argumentos — y esa decisión no se da por supuesta aquí, se <a href="/libgen-mcp/es/eval-results/">mide contra una evaluación real y calificada</a>.',
			"Los mirrors se descubren y se cachean automáticamente, con failover transparente, así que el servidor sigue funcionando aunque algunos caigan y vuelvan.",
		],
	},
	who: {
		title: "Para quién es",
		items: [
			"Para quien lee artículos y prefiere pedir uno en una frase antes que montar a mano la búsqueda de un DOI.",
			"Para quien ya usa un cliente MCP: Claude Code, Claude Desktop, Cursor, VS Code o su propio agente.",
			"Para quien quiere primero la copia de acceso abierto, y que se le diga cuando no la hay.",
			"Para quien no quiere crearse una cuenta para consultar el catálogo de una biblioteca.",
		],
	},
	honest: {
		title: "Qué no es",
		items: [
			"No es una forma de saltarse lo que no puedes leer. Rechaza el material que no puede redistribuir, y da el rechazo como un fallo en vez de disfrazarlo de fichero.",
			"No es un mirror ni un índice propio. Consulta servicios ajenos y se identifica al hacerlo.",
			"No es una garantía. Los mirrors de terceros se caen, y una fuente que no tiene nada para un identificador lo dice.",
			"No es una opinión jurídica. La ley de propiedad intelectual que se aplica donde vives es cosa tuya.",
		],
	},
	proof: {
		title: "Cómo se ve en uso",
		lead: "Dos cosas, en el orden en que te las encuentras: ponerlo en marcha y luego hablar con él.",
		installTitle: "Instalación en una línea",
		installNote:
			"O apunta tu cliente a un binario precompilado — sin cuenta, sin clave de API y sin token en ningún paso.",
		install: INSTALL,
		promptsTitle: "Y luego, pídeselo",
		promptsNote:
			"Todas las peticiones de abajo salen de la suite de evaluación, donde un modelo real se califica resolviéndolas contra el sitio en vivo.",
		promptHeaders: ["Le pides a tu asistente", "Qué hace libgen-mcp"],
		prompts: [
			[
				"«Quiero leer <em>Dune</em>, ¿me lo buscas?»",
				"Forma una búsqueda en el catálogo sin ayuda, sin pistas de colección ni de campo",
			],
			[
				"«Descárgame <em>Clean Code</em>»",
				"Busca, elige un md5 de los resultados y escoge él mismo la fuente de descarga",
			],
			[
				"«Consígueme el PDF de <em>Hallmarks of Cancer</em>»",
				"Deduce que los artículos van por DOI y no por md5, y lo resuelve por la cadena de acceso abierto",
			],
			[
				"«Busca este artículo y resúmeme la primera página»",
				"Llama a <code>read</code> en vez de a <code>download</code>, y resume el texto extraído en vez de volcarlo",
			],
			[
				"«¿Qué dice este libro sobre punteros?»",
				"Llama a <code>read</code> con <code>find</code>, buscando dentro del documento sin descargar el fichero entero",
			],
			[
				"«Consígueme este libro por su ISBN»",
				"Va a OAPEN y a los escaneos de dominio público del Internet Archive — nunca a una biblioteca en la sombra",
			],
			[
				"«Dame el enlace directo, no lo descargues»",
				"Activa <code>resolve_only</code> y devuelve una URL en vez de escribir un fichero",
			],
		],
	},
	chain: {
		title: "La cadena, en orden",
		lead: "Una descarga prueba cada fuente que admite el elemento, en este orden, y pasa a la siguiente si falla. El orden está fijado en el código: los proveedores legales de acceso abierto van primero, y las bibliotecas en la sombra solo se alcanzan cuando todos ellos han declinado.",
		headers: ["Etapa", "Fuentes"],
		groups: [
			{
				id: "resolvers",
				name: "Resolutores de acceso abierto",
				summary:
					"Se preguntan primero, para cualquier DOI: ¿existe una copia libre y legal, y dónde?",
			},
			{
				id: "publishers",
				name: "Directo del editor",
				summary: "Rutas por prefijo hacia un editor que regala su propia obra.",
			},
			{
				id: "aggregators",
				name: "Agregadores y preservación",
				summary:
					"Índices amplios y archivos, para los DOI que los resolutores no encontraron.",
			},
			{
				id: "books",
				name: "Libros de acceso abierto y dominio público",
				summary:
					"Las dos únicas fuentes para un ISBN. Los escaneos restringidos a préstamo se rechazan.",
			},
			{
				id: "fallbacks",
				name: "Bibliotecas en la sombra",
				summary:
					"Se alcanzan las últimas, y solo cuando todo lo anterior ha salido vacío.",
			},
		],
		keyedBy: {
			doi: "identificada por DOI",
			md5: "identificada por MD5",
			isbn: "identificada por ISBN",
			"doi-or-isbn": "identificada por DOI o ISBN",
		},
		legend:
			"Una fuente con anillo lee una credencial. Ninguna es obligatoria: si la dejas sin definir, esa fuente se queda fuera de la cadena y todas las demás siguen funcionando.",
	},
	start: {
		title: "Empieza aquí",
		lead: "Tres puertas, según a qué hayas venido.",
		steps: [
			{
				title: "Instálalo y haz una primera búsqueda",
				body: "Un binario precompilado, Docker o go install; y después el JSON de mcpServers para Claude Desktop, Cursor y VS Code.",
				code: INSTALL,
				href: "/libgen-mcp/es/getting-started/",
				linkText: "Primeros pasos",
			},
			{
				title: "Lee qué hacen de verdad las herramientas",
				body: "search, get_details, download y read — cada entrada, cada campo de salida y qué hace cada una cuando falla.",
				href: "/libgen-mcp/es/tools/",
				linkText: "Herramientas",
			},
			{
				title: "Decide a qué puede llegar",
				body: "Cada variable de entorno con su valor por defecto y su rango válido — incluyendo qué fuentes entran siquiera en la cadena.",
				href: "/libgen-mcp/es/configuration/",
				linkText: "Configuración",
			},
		],
	},
};
