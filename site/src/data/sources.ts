/**
 * The download chain and the discovery providers, as data.
 *
 * One ordered list of sources exists in `internal/config.KnownSources`, and the
 * site restates it in five different renderings across five pages — a position
 * table, a role table, twenty-one prose sections, an inline arrow string and a
 * numbered list — in two languages. That is ten hand-maintained copies of one
 * fact, and it is the highest drift risk on the site.
 *
 * This file is the single copy those renderings are built from. The order here
 * is `config.KnownSources` verbatim and it is load-bearing: the chain is fixed
 * in code, the legal open-access providers lead, and the shadow libraries are
 * reached only after all of them have declined.
 *
 * It is deliberately NOT generated from the Go source yet. Adding a build-time
 * generator during a design change would mean two moving gates at once.
 */

/** What identifier a source can be asked for. */
export type KeyedBy = "doi" | "md5" | "isbn" | "doi-or-isbn";

/** Whether a source needs a credential to take part in the chain. */
export type KeyRequirement = "none" | "required" | "optional";

/** The stage of the chain a source belongs to. */
export type SourceGroup =
	"resolvers" | "publishers" | "aggregators" | "books" | "fallbacks";

export interface Source {
	/** The name the `download` tool's `source` enum uses. */
	id: string;
	group: SourceGroup;
	keyedBy: KeyedBy;
	keys: KeyRequirement;
	/** The environment variable a keyed source reads, if any. */
	keyVar?: string;
}

/**
 * The chain, in order. Position in this array IS the chain position, so no
 * rendering has to number the rows by hand.
 */
export const SOURCES: Source[] = [
	{
		id: "unpaywall",
		group: "resolvers",
		keyedBy: "doi",
		keys: "required",
		keyVar: "LIBGEN_MCP_UNPAYWALL_EMAIL",
	},
	{ id: "openalex", group: "resolvers", keyedBy: "doi", keys: "none" },
	{ id: "europepmc", group: "resolvers", keyedBy: "doi", keys: "none" },
	{ id: "biorxiv", group: "resolvers", keyedBy: "doi", keys: "none" },
	{ id: "rfc", group: "publishers", keyedBy: "doi", keys: "none" },
	{ id: "nist", group: "publishers", keyedBy: "doi", keys: "none" },
	{ id: "dagstuhl", group: "publishers", keyedBy: "doi", keys: "none" },
	{ id: "acl", group: "publishers", keyedBy: "doi", keys: "none" },
	{ id: "zenodo", group: "publishers", keyedBy: "doi", keys: "none" },
	{ id: "scielo", group: "publishers", keyedBy: "doi", keys: "none" },
	{ id: "fao", group: "publishers", keyedBy: "doi", keys: "none" },
	{ id: "fatcat", group: "aggregators", keyedBy: "doi", keys: "none" },
	{
		id: "core",
		group: "aggregators",
		keyedBy: "doi",
		keys: "required",
		keyVar: "LIBGEN_MCP_CORE_KEY",
	},
	{ id: "crossref", group: "aggregators", keyedBy: "doi", keys: "none" },
	{ id: "oapen", group: "books", keyedBy: "doi-or-isbn", keys: "none" },
	{ id: "archive", group: "books", keyedBy: "isbn", keys: "none" },
	{ id: "scihub", group: "fallbacks", keyedBy: "doi", keys: "none" },
	{ id: "scidb", group: "fallbacks", keyedBy: "doi", keys: "none" },
	{ id: "libgen", group: "fallbacks", keyedBy: "md5", keys: "none" },
	{ id: "randombook", group: "fallbacks", keyedBy: "md5", keys: "none" },
	{
		id: "annas",
		group: "fallbacks",
		keyedBy: "md5",
		keys: "optional",
		keyVar: "LIBGEN_MCP_ANNAS_KEY",
	},
];

/**
 * The searchers `search` consults beyond the Library Genesis catalog, in the
 * order `discovery.ExtraProviders` assembles them: the four defaults first,
 * then the four the `extra_sources` policy adds.
 */
export const DISCOVERY_PROVIDERS = [
	"arxiv",
	"crossref",
	"openlibrary",
	"gutenberg",
	"dblp",
	"pubmed",
	"eric",
	"annas",
] as const;

/** The MCP tools this server exposes. */
export const TOOLS = ["search", "get_details", "download", "read"] as const;

/** The sources in one stage of the chain, in chain order. */
export const sourcesIn = (group: SourceGroup): Source[] =>
	SOURCES.filter((source) => source.group === group);

/**
 * How many sources take part with no configuration at all. The landing page
 * states this number, and it is counted rather than typed so a source that
 * gains a credential cannot leave the claim behind.
 */
export const KEYLESS_COUNT = SOURCES.filter(
	(source) => source.keys === "none",
).length;

/** Sources that will not run until a credential is supplied. */
export const KEYED_COUNT = SOURCES.filter(
	(source) => source.keys === "required",
).length;
