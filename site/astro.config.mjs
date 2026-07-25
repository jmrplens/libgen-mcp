import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";
import starlightLinksValidator from "starlight-links-validator";
import mermaid from "astro-mermaid";

const siteDescription =
	"Open-source MCP server in Go for Library Genesis: four tools to search, read and download books, papers and more from your AI assistant — no account required.";

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
	"Search Library Genesis for books, papers, comics, magazines and standards",
	"Four MCP tools: search, get_details, download, read",
	"Open access first: articles resolve through Unpaywall, Europe PMC, bioRxiv, Internet Archive Scholar, CORE and OAPEN before any shadow-library fallback",
	"Refuses what it may not redistribute: OAPEN identifiers are confirmed, lending-restricted Internet Archive scans and permission-hosted Gutenberg records are skipped",
	"Twelve download sources in a fixed chain with transparent per-source failover",
	"Automatic mirror discovery, caching and transparent failover",
	"Single cross-platform static Go binary (Linux, macOS, Windows; amd64 and arm64)",
	"stdio and streamable HTTP transports; no account or API key required",
];
const softwareRequirements =
	"None — a single static binary; Go 1.26+ only to build from source.";

// Site-wide JSON-LD @graph: the stable Person / WebSite / SoftwareApplication /
// SourceCode nodes that per-page TechArticle + BreadcrumbList nodes link into.
const jsonLd = JSON.stringify({
	"@context": "https://schema.org",
	"@graph": [
		// This node shares its @id with the canonical Person published at
		// https://jmrp.io/#person. Two nodes under one @id that disagree weaken
		// the entity rather than reinforcing it, so jobTitle, description, image
		// and sameAs are kept identical to the portfolio's — that site is the
		// source of truth for the identity, this one only restates it.
		{
			"@type": "Person",
			"@id": authorId,
			name: "José Manuel Requena Plens",
			alternateName: "jmrplens",
			jobTitle: "R&D · Firmware & Software Engineer",
			description:
				"Firmware and software engineer in Valencia, Spain — industrial embedded systems, open-source tooling, and self-hosted infrastructure.",
			url: authorUrl,
			image: {
				"@type": "ImageObject",
				url: "https://github.com/jmrplens.png",
				width: 460,
				height: 460,
			},
			// Wikidata-linked rather than bare strings, so an engine resolves each
			// topic to a known entity instead of guessing from a label. The Q-ids
			// match the ones the portfolio already uses.
			knowsAbout: [
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
			],
			sameAs: [
				"https://github.com/jmrplens",
				"https://www.linkedin.com/in/jmrplens",
				"https://mstdn.jmrp.io/@jmrplens",
				"https://bsky.app/profile/jmrp.io",
				"https://scholar.google.com/citations?user=9b0kPaUAAAAJ",
				"https://orcid.org/0000-0003-1250-6212",
				"https://www.researchgate.net/profile/Jose-Requena-Plens-2",
				"https://www.mathworks.com/matlabcentral/profile/authors/5890853",
				"https://matrix.to/#/@jmrplens:matrix.jmrp.io",
				"https://keyoxide.org/0A993B268654DBBA52B7E8D3FCF653391E2C91FC",
			],
		},
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
			alternateName: ["LibGen MCP", "libgen-mcp (Go)"],
			identifier: "io.github.jmrplens/libgen-mcp",
			...(softwareVersion ? { softwareVersion } : {}),
			applicationCategory: "DeveloperApplication",
			applicationSubCategory: "Search Tools",
			operatingSystem: "Windows, macOS, Linux",
			programmingLanguage: "Go",
			url: repositoryUrl,
			downloadUrl: "https://github.com/jmrplens/libgen-mcp/releases/latest",
			installUrl: "https://jmrplens.github.io/libgen-mcp/getting-started/",
			// Track the current release so this never lags behind softwareVersion.
			releaseNotes: softwareVersion
				? `https://github.com/jmrplens/libgen-mcp/releases/tag/v${softwareVersion}`
				: "https://github.com/jmrplens/libgen-mcp/releases/latest",
			codeRepository: repositoryUrl,
			image: socialImage,
			license: "https://opensource.org/licenses/MIT",
			isAccessibleForFree: true,
			datePublished,
			dateModified,
			softwareRequirements,
			featureList,
			description:
				"MCP server to search and download books, research papers, comics, magazines and standards from Library Genesis. No account required.",
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
			offers: {
				"@type": "Offer",
				price: "0",
				priceCurrency: "USD",
			},
			author: { "@id": authorId },
			// Every listing that carries this server, so an engine resolving the
			// name lands on the same entity wherever it finds it. Note the
			// Smithery namespace is `jmrp`, not `jmrplens`.
			sameAs: [
				`${fullUrl}/`,
				repositoryUrl,
				"https://registry.modelcontextprotocol.io/v0/servers?search=io.github.jmrplens/libgen-mcp",
				"https://smithery.ai/server/@jmrp/libgen-mcp",
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
		},
	],
});

export default defineConfig({
	site: "https://jmrplens.github.io",
	base: "/libgen-mcp",
	integrations: [
		mermaid({ theme: "default", autoTheme: true }),
		starlight({
			title: "LibGen MCP",
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
			},
			logo: {
				dark: "./src/assets/logo-dark.svg",
				light: "./src/assets/logo-light.svg",
				alt: "LibGen MCP",
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
					attrs: { name: "theme-color", content: "#0d9488" },
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
			customCss: ["./src/styles/custom.css"],
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
