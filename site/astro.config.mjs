import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";
import starlightLinksValidator from "starlight-links-validator";
import rehypeMermaid from "rehype-mermaid";

import { rehypeWideTables } from "./src/lib/wide-tables.mjs";

const siteDescription =
	"Open-source MCP server in Go for federated search, citation and reading of books and papers: four tools spanning the Library Genesis catalog and open-access sources — no account required.";

// --- Identity, URLs and structured-data ids ------------------------------
const siteUrl = "https://jmrplens.github.io";
const basePath = "/libgen-mcp";
const fullUrl = `${siteUrl}${basePath}`;
const repositoryUrl = "https://github.com/jmrplens/libgen-mcp";
const authorUrl = "https://jmrp.io";
const socialImageUrl = `${fullUrl}/og.png`;
const authorId = `${authorUrl}/#person`;
const websiteId = `${fullUrl}/#website`;
const softwareId = `${repositoryUrl}#software`;
const sourceCodeId = `${repositoryUrl}#source-code`;
const socialImageAlt =
	"libgen-mcp — search, read and download books and papers from Library Genesis, plus open-access discovery, over MCP";

// Product version — single-sourced from the repo-root VERSION file when present.
const softwareVersion = (() => {
	try {
		return readFileSync(new URL("../VERSION", import.meta.url), "utf8").trim();
	} catch {
		return undefined;
	}
})();

const socialImage = {
	"@type": "ImageObject",
	url: socialImageUrl,
	width: 1200,
	height: 630,
};

// Freshness signals for the SoftwareApplication node. `datePublished` is the
// first public release (v1.0.0, 2026-07-19) and is intentionally fixed. To avoid
// stamping a false "modified today" on every rebuild, `dateModified` tracks the
// last repository change (HEAD commit date), falling back to build time only when
// git history is unavailable.
const datePublished = "2026-07-19";
const dateModified = (() => {
	try {
		return execFileSync("git", ["log", "-1", "--format=%cI"], {
			encoding: "utf8",
		})
			.trim()
			.slice(0, 10);
	} catch {
		return new Date().toISOString().slice(0, 10);
	}
})();

// Human-readable capability list and requirements. These feed AI "what can it
// do?" and "what do I need?" queries directly from structured data.
const featureList = [
	"Federated search for books, papers, comics, magazines and standards across the Library Genesis catalog and open-access sources",
	"Four MCP tools: search, get_details, download, read",
	"Open access first: articles resolve through Unpaywall, Europe PMC, bioRxiv, the RFC Editor, NIST, Schloss Dagstuhl, the ACL Anthology, Zenodo, Internet Archive Scholar, CORE and OAPEN before any shadow-library fallback",
	"Refuses what it may not redistribute: OAPEN identifiers are confirmed, lending-restricted Internet Archive scans and permission-hosted Gutenberg records are skipped",
	"Twenty-one download sources in a fixed chain with transparent per-source failover",
	"Automatic mirror discovery, caching and transparent failover",
	"Single cross-platform static Go binary (Linux, macOS, Windows; amd64 and arm64)",
	"stdio and streamable HTTP transports; no account or API key required",
];
const softwareRequirements =
	"None — a single static binary; Go 1.27+ only to build from source.";

// Site-wide JSON-LD @graph: the stable Person / WebSite / SoftwareApplication /
// SourceCode nodes that per-page TechArticle + BreadcrumbList nodes link into.
// --- Canonical identity --------------------------------------------------
// The `#person` entity is no longer restated here; it is fetched from its
// single source of truth on jmrp.io and spliced into the graph verbatim. Every
// property this site used to hand-copy (jobTitle, description, image, sameAs,
// alternateName) had to be kept in sync by hand across five project sites, and
// it drifted: two ended up publishing values that contradicted the canonical
// node, one an avatar URL that had started returning 404.
//
// Fetched from raw.githubusercontent.com rather than https://jmrp.io on
// purpose: this build runs on a CI runner, and jmrp.io sits behind Cloudflare,
// CrowdSec and a MikroTik bouncer, where a blocked runner IP would silently
// degrade this site to a stale snapshot. GitHub serves the same bytes and is
// already a hard dependency of the build — the checkout comes from it.
const CANONICAL_IDENTITY_URL =
	"https://raw.githubusercontent.com/jmrplens/jmrp.io/main/public/identity/person.jsonld";

// Committed fallback, only reached if the fetch fails — which, given the URL is
// on the same host as the checkout, effectively means GitHub is down and there
// is no build anyway. The warning is deliberately loud so a stale identity
// never ships unnoticed. Refresh with `pnpm run identity:sync`.
const identitySnapshot = JSON.parse(
	readFileSync(
		new URL("./identity/person.snapshot.json", import.meta.url),
		"utf8",
	),
);

// The live document. Falls back to the snapshot only on a fetch failure.
const identityDocument = await fetch(CANONICAL_IDENTITY_URL, {
	signal: AbortSignal.timeout(10_000),
})
	.then((response) =>
		response.ok
			? response.json()
			: Promise.reject(new Error(`HTTP ${response.status}`)),
	)
	.catch((error) => {
		console.warn(
			`\n⚠ [identity] Could not fetch the canonical Person entity (${error.message}).\n` +
				`  Falling back to the committed snapshot — this build may ship a stale identity.\n`,
		);
		return identitySnapshot;
	});

// `@context` is stripped: the document is standalone, but here it becomes one
// node of a graph that already declares the context once. Filtered rather
// than rest-destructured so no unused binding is left for linters to flag.
const canonicalPerson = Object.fromEntries(
	Object.entries(identityDocument).filter(([key]) => key !== "@context"),
);

/**
 * Topics specific to this project, kept alongside the canonical expertise list
 * rather than replacing it. `knowsAbout` is multi-valued, so the two sets merge
 * instead of conflicting — which is why these may stay local while the
 * single-valued properties above may not. The full original set is listed, not
 * just the entries the canonical lacks, so this site keeps asserting its own
 * topics even if the canonical list changes.
 */
const projectTopics = [
	{
		"@type": "Thing",
		name: "Model Context Protocol",
		"@id": "http://www.wikidata.org/entity/Q133436854",
	},
	{
		"@type": "Thing",
		name: "Go",
		"@id": "http://www.wikidata.org/entity/Q37227",
	},
	{
		"@type": "Thing",
		name: "Library Genesis",
		"@id": "http://www.wikidata.org/entity/Q22017206",
	},
	{
		"@type": "Thing",
		name: "Programming tool",
		"@id": "http://www.wikidata.org/entity/Q1077784",
	},
	{
		"@type": "Thing",
		name: "Open access",
		"@id": "http://www.wikidata.org/entity/Q232932",
	},
	{
		"@type": "Thing",
		name: "Digital library",
		"@id": "http://www.wikidata.org/entity/Q212805",
	},
];

/**
 * Reads a `knowsAbout` entry's label, which may be a string or a Thing.
 *
 * @param {string | { name?: string }} topic - Entry from a `knowsAbout` array.
 * @returns {string} The entry's label.
 */
const topicName = (topic) =>
	typeof topic === "string" ? topic : (topic.name ?? "");

/**
 * Canonical topics first, then this project's own, de-duplicated by label
 * (case-insensitively). Canonical entries win a collision because they carry a
 * verified Wikidata `@id`.
 */
const personNode = (() => {
	const canonical = /** @type {(string | { name?: string })[]} */ (
		canonicalPerson.knowsAbout ?? []
	);
	const seen = new Set(canonical.map((t) => topicName(t).toLowerCase()));
	return {
		...canonicalPerson,
		knowsAbout: [
			...canonical,
			...projectTopics.filter((t) => !seen.has(topicName(t).toLowerCase())),
		],
	};
})();

const jsonLd = JSON.stringify({
	"@context": "https://schema.org",
	"@graph": [
		// The canonical Person entity, fetched at build time — see the
		// "Canonical identity" block above. Spliced verbatim so this document
		// and jmrp.io describe the same node with the same values, instead of
		// two hand-maintained copies that drift.
		personNode,
		{
			"@type": "WebSite",
			"@id": websiteId,
			name: "libgen-mcp",
			url: `${fullUrl}/`,
			description: siteDescription,
			inLanguage: ["en", "es"],
			image: socialImage,
			publisher: { "@id": authorId },
			about: { "@id": softwareId },
			// No SearchAction: site search is Pagefind, which only opens from the
			// search button. There is no `?q=` entry point to advertise, and Google
			// retired the sitelinks searchbox rich result in 2024, so declaring one
			// would be a claim nothing honours.
		},
		{
			"@type": "SoftwareApplication",
			"@id": softwareId,
			name: "libgen-mcp",
			// Three other GitHub projects are also called "libgen-mcp". The registry
			// id is the only globally unique handle this server has, so it is
			// declared alongside the names an engine is likely to see.
			// "Library Genesis MCP Server" is the title the MCP registry publishes
			// this server under. Declaring it here is what lets an engine following
			// sameAs to that listing reconcile it with this entity instead of
			// treating it as a different project.
			alternateName: [
				"LibGen MCP",
				"libgen-mcp (Go)",
				"Library Genesis MCP Server",
			],
			identifier: "io.github.jmrplens/libgen-mcp",
			...(softwareVersion ? { softwareVersion } : {}),
			applicationCategory: "DeveloperApplication",
			applicationSubCategory: "Search Tools",
			operatingSystem: "Windows, macOS, Linux",
			// programmingLanguage and codeRepository are deliberately absent here:
			// schema.org scopes both to SoftwareSourceCode, so on a
			// SoftwareApplication they are domain errors on every page of the site.
			// The SoftwareSourceCode node below carries them, and links back via
			// targetProduct; `keywords` and `url` keep the Go/GitHub signal on this
			// node without inventing a property that does not belong to it.
			url: repositoryUrl,
			downloadUrl: "https://github.com/jmrplens/libgen-mcp/releases/latest",
			installUrl: "https://jmrplens.github.io/libgen-mcp/getting-started/",
			// Track the current release so this never lags behind softwareVersion.
			releaseNotes: softwareVersion
				? `https://github.com/jmrplens/libgen-mcp/releases/tag/v${softwareVersion}`
				: "https://github.com/jmrplens/libgen-mcp/releases/latest",
			image: socialImage,
			license: "https://opensource.org/licenses/MIT",
			isAccessibleForFree: true,
			datePublished,
			dateModified,
			softwareRequirements,
			featureList,
			description:
				"MCP server for federated search, citation and reading of books, research papers, comics, magazines and standards across the Library Genesis catalog and open-access sources. No account required.",
			keywords:
				"Model Context Protocol, MCP, Library Genesis, libgen, books, research papers, AI assistants, Go",
			// The two subjects this software is about, as Wikidata entities. An
			// engine that has never heard of libgen-mcp has certainly heard of
			// these, which is what lets it place an unknown tool in a known field.
			about: [
				{
					"@type": "Thing",
					name: "Library Genesis",
					"@id": "http://www.wikidata.org/entity/Q22017206",
				},
				{
					"@type": "Thing",
					name: "Model Context Protocol",
					"@id": "http://www.wikidata.org/entity/Q133436854",
				},
			],
			// No aggregateRating or review: the SoftwareApplication rich result wants
			// one, but there is no legitimate source for either here — GitHub stars
			// are not ratings — so the result stays ineligible rather than invented.
			offers: {
				"@type": "Offer",
				price: "0",
				priceCurrency: "USD",
				availability: "https://schema.org/InStock",
				url: "https://github.com/jmrplens/libgen-mcp/releases/latest",
			},
			author: { "@id": authorId },
			// Same three agents jmrp.io/projects/ emits for this @id, so the merged
			// node states one consistent set instead of a partial one per document.
			creator: { "@id": authorId },
			maintainer: { "@id": authorId },
			// Every listing that carries this server, so an engine resolving the
			// name lands on the same entity wherever it finds it. Note the
			// Smithery namespace is `jmrp`, not `jmrplens`.
			sameAs: [
				`${fullUrl}/`,
				repositoryUrl,
				"https://registry.modelcontextprotocol.io/v0/servers?search=io.github.jmrplens/libgen-mcp",
				// /server/ 308-redirects to /servers/; sameAs should name the
				// destination, not a hop.
				"https://smithery.ai/servers/@jmrp/libgen-mcp",
				"https://mcp.so/servers/libgen-mcp-d62341",
				"https://lobehub.com/mcp/jmrplens-libgen-mcp",
				"https://pkg.go.dev/github.com/jmrplens/libgen-mcp",
				"https://hub.docker.com/r/jmrplens/libgen-mcp",
				"https://cursor.directory/plugins/libgen-mcp",
				"https://glama.ai/mcp/servers/jmrplens/libgen-mcp",
			],
		},
		{
			"@type": "SoftwareSourceCode",
			"@id": sourceCodeId,
			name: "libgen-mcp source code",
			codeRepository: repositoryUrl,
			programmingLanguage: "Go",
			runtimePlatform: "Windows, macOS, Linux",
			license: "https://opensource.org/licenses/MIT",
			isPartOf: { "@id": softwareId },
			// The forward edge to the product; `isPartOf` alone only points back.
			targetProduct: { "@id": softwareId },
			author: { "@id": authorId },
			// Same three agents jmrp.io/projects/ emits for this @id, so the merged
			// node states one consistent set instead of a partial one per document.
			creator: { "@id": authorId },
			maintainer: { "@id": authorId },
		},
	],
});

export default defineConfig({
	site: "https://jmrplens.github.io",
	base: "/libgen-mcp",
	// Mermaid is rendered to inline SVG at build time. astro-mermaid did it in the
	// browser, which cost ~199 KB gzip over ~35 requests on each of the four
	// diagram pages, shipped ~2.7 MB of unreachable diagram types (cytoscape,
	// katex, gantt, c4…) for two flowcharts, and left crawlers reading the raw
	// DSL inside <pre class="mermaid"> instead of a picture or any text. Rendering
	// here costs a headless Chromium at build time and nothing at all at runtime.
	markdown: {
		rehypePlugins: [
			// BEFORE rehypeMermaid, deliberately: at this point a diagram is still
			// a fenced code block, so its markup cannot be mistaken for a content
			// table. Moving this after rehypeMermaid would hand it the rendered
			// SVG instead.
			rehypeWideTables,
			[
				rehypeMermaid,
				{
					strategy: "inline-svg",
					// The SVG is baked once, so it cannot follow the theme toggle on its
					// own. Emit it in neutral light colours and let custom.css repaint it
					// for dark mode through CSS variables — no JS, no second render, and
					// the switch stays instant.
					mermaidConfig: {
						theme: "base",
						themeVariables: {
							background: "transparent",
							textColor: "#1f2328",
							primaryColor: "#f6f8fa",
							primaryTextColor: "#1f2328",
							primaryBorderColor: "#d1d9e0",
							lineColor: "#59636e",
							secondaryColor: "#f6f8fa",
							tertiaryColor: "#ffffff",
							mainBkg: "#f6f8fa",
							nodeBorder: "#d1d9e0",
							clusterBkg: "#ffffff",
							clusterBorder: "#d1d9e0",
							edgeLabelBackground: "#ffffff",
							fontSize: "15px",
						},
					},
				},
			],
		],
	},
	integrations: [
		starlight({
			title: "LibGen MCP",
			// Restores the mobile menu button on the two splash landings, which
			// Starlight builds without a sidebar. See src/routeData.ts.
			routeMiddleware: "./src/routeData.ts",
			description: siteDescription,
			expressiveCode: {
				// Otherwise the code-block stylesheet is emitted as a <link> inside
				// <body>, mid-content — it blocks render of everything after it and
				// flashes unstyled code blocks. Inlining it puts the styles in <head>.
				emitExternalStylesheet: false,
			},
			plugins: [
				starlightLinksValidator({
					errorOnRelativeLinks: false,
					errorOnFallbackPages: false,
				}),
			],
			components: {
				// Per-page structured data (TechArticle / BreadcrumbList) and per-page
				// Twitter card tags, layered on top of the default head.
				Head: "./src/components/Head.astro",
				// Adds a human-visible maintainer block below the default footer,
				// corroborating the Person node in the site-wide @graph.
				Footer: "./src/components/Footer.astro",
				// Keeps the theme toggle and EN/ES language select visible in the
				// header below the `md` breakpoint, where Starlight otherwise hides the
				// whole header right-group.
				Header: "./src/components/Header.astro",
				// Inlines the brand mark so the palette can paint it, and sets the
				// header's brand presence. Replaces the `logo` option, which renders an
				// <img> that cannot follow the theme toggle.
				SiteTitle: "./src/components/SiteTitle.astro",
				// Supplies the landing hero's figure as inline markup, so the palette
				// paints the mark and the brand SVG has one copy rather than one per
				// locale.
				Hero: "./src/components/Hero.astro",
				// Mounts the page actions: under the sticky table-of-contents bar on a
				// phone, above "On this page" on desktop.
				PageSidebar: "./src/components/PageSidebar.astro",
				// Renders the page title, then the chip row its frontmatter declares.
				PageTitle: "./src/components/PageTitle.astro",
			},
			favicon: "/favicon.svg",
			head: [
				{
					tag: "link",
					attrs: {
						rel: "icon",
						type: "image/png",
						href: "/libgen-mcp/favicon.png",
						sizes: "any",
					},
				},
				// GitHub Pages cannot send X-Robots-Tag, and without the meta
				// equivalent both Google's AI surfaces and Bing fall back to a
				// truncated snippet and a thumbnail-sized image. Everything here is
				// public documentation, so grant the full length explicitly.
				{
					tag: "meta",
					attrs: {
						name: "robots",
						content:
							"index, follow, max-snippet:-1, max-image-preview:large, max-video-preview:-1",
					},
				},
				// llms.txt is discovered at the *origin* root, which this project does
				// not own — jmrplens.github.io is a shared user page. Without a link
				// from the pages themselves, /libgen-mcp/llms.txt is unreachable
				// except by guessing the base-path variant.
				{
					tag: "link",
					attrs: {
						rel: "alternate",
						type: "text/plain",
						href: "/libgen-mcp/llms.txt",
						title: "llms.txt",
					},
				},
				{
					tag: "meta",
					attrs: {
						property: "og:image",
						content: socialImageUrl,
					},
				},
				{
					tag: "meta",
					attrs: { property: "og:image:alt", content: socialImageAlt },
				},
				{
					tag: "meta",
					attrs: { property: "og:image:type", content: "image/png" },
				},
				{
					tag: "meta",
					attrs: { property: "og:image:width", content: "1200" },
				},
				{
					tag: "meta",
					attrs: { property: "og:image:height", content: "630" },
				},
				{
					tag: "meta",
					attrs: { name: "twitter:card", content: "summary_large_image" },
				},
				{
					tag: "meta",
					attrs: {
						name: "twitter:image",
						content: socialImageUrl,
					},
				},
				{
					tag: "meta",
					attrs: { name: "twitter:image:alt", content: socialImageAlt },
				},
				// Author
				{
					tag: "meta",
					attrs: { name: "author", content: "José Manuel Requena Plens" },
				},
				// Search Console and Bing Webmaster ownership. Verifying the host
				// root at jmrplens.github.io covers this sub-path, but a sub-path
				// registered as a property in its own right — which is what lets you
				// see this project's queries and sitemap status separately — has to
				// carry the tag itself. Same tokens as the hub and the sibling docs
				// sites, so all three verify against one account.
				{
					tag: "meta",
					attrs: {
						name: "google-site-verification",
						content: "4Hx_PJ1seU_BgKfWpo_FA7_Hkh7GeYVNrvnvzqCjF0Q",
					},
				},
				{
					tag: "meta",
					attrs: {
						name: "msvalidate.01",
						content: "7574EB3B44624C239F14920DBC34EE25",
					},
				},
				// GitHub Pages cannot set response headers, so the policies that do
				// have a <meta> equivalent are declared here. X-Frame-Options,
				// X-Content-Type-Options and Permissions-Policy are header-only and
				// would need the site fronted by a CDN.
				{
					tag: "meta",
					attrs: {
						name: "referrer",
						content: "strict-origin-when-cross-origin",
					},
				},
				// Theme color (brand teal accent)
				{
					tag: "meta",
					attrs: { name: "theme-color", content: "#8a5a12" },
				},
				// rel="me" identity links
				{
					tag: "link",
					attrs: { rel: "me", href: "https://github.com/jmrplens" },
				},
				{
					tag: "link",
					attrs: { rel: "me", href: "https://linkedin.com/in/jmrplens" },
				},
				// JSON-LD structured data (@graph)
				{
					tag: "script",
					attrs: { type: "application/ld+json" },
					content: jsonLd,
				},
			],
			// The profiles a reader might actually follow. They double as visible
			// corroboration of the Person node's sameAs set.
			social: [
				{
					icon: "github",
					label: "GitHub",
					href: "https://github.com/jmrplens/libgen-mcp",
				},
				{
					icon: "mastodon",
					label: "Mastodon",
					href: "https://mstdn.jmrp.io/@jmrplens",
				},
				{
					icon: "linkedin",
					label: "LinkedIn",
					href: "https://www.linkedin.com/in/jmrplens",
				},
			],
			editLink: {
				baseUrl: "https://github.com/jmrplens/libgen-mcp/edit/main/site/",
			},
			lastUpdated: true,
			defaultLocale: "root",
			locales: {
				root: { label: "English", lang: "en" },
				es: { label: "Español", lang: "es" },
			},
			customCss: [
				"./src/styles/theme.css",
				"./src/styles/typography.css",
				"./src/styles/sidebar.css",
				"./src/styles/splash-menu.css",
				"./src/styles/home.css",
				"./src/styles/facts.css",
				"./src/styles/schema-table.css",
				"./src/styles/source-chain.css",
				"./src/styles/tables.css",
				"./src/styles/page-actions.css",
				"./src/styles/page-chips.css",
				"./src/styles/custom.css",
			],
			sidebar: [
				{
					label: "Guide",
					translations: { es: "Guía" },
					items: [
						{
							slug: "getting-started",
							label: "Getting Started",
							translations: { es: "Primeros pasos" },
						},
						{
							slug: "configuration",
							label: "Configuration",
							translations: { es: "Configuración" },
						},
						{
							slug: "troubleshooting",
							label: "Troubleshooting",
							translations: { es: "Solución de problemas" },
						},
					],
				},
				{
					label: "Reference",
					translations: { es: "Referencia" },
					items: [
						{
							slug: "tools",
							label: "Tools",
							translations: { es: "Herramientas" },
						},
						{
							slug: "architecture",
							label: "Architecture",
							translations: { es: "Arquitectura" },
						},
						{
							slug: "sources",
							label: "Download sources",
							translations: { es: "Fuentes de descarga" },
						},
						{
							slug: "how-search-works",
							label: "How search works",
							translations: { es: "Cómo funciona la búsqueda" },
						},
						{
							slug: "eval-results",
							label: "LLM eval results",
							translations: { es: "Resultados del eval con LLM" },
						},
					],
				},
				{
					label: "Project",
					translations: { es: "Proyecto" },
					items: [
						{
							slug: "responsible-use",
							label: "Responsible use",
							translations: { es: "Uso responsable" },
						},
						{
							slug: "privacy",
							label: "Privacy policy",
							translations: { es: "Política de privacidad" },
						},
						{
							label: "Security policy",
							translations: { es: "Política de seguridad" },
							link: "https://github.com/jmrplens/libgen-mcp/blob/main/SECURITY.md",
							attrs: { target: "_blank", rel: "noopener noreferrer" },
						},
					],
				},
			],
		}),
	],
});
