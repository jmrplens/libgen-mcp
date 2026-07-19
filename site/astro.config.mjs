import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";
import starlightLinksValidator from "starlight-links-validator";
import mermaid from "astro-mermaid";

const siteDescription =
	"Open source Model Context Protocol server for Library Genesis — search books and resolve download links from your AI assistant.";

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
	"libgen-mcp — search and download from Library Genesis over MCP";

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
	"Three MCP tools: search, get_details, download",
	"Multi-source downloads: libgen and randombook for books; Unpaywall and Sci-Hub for articles by DOI",
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
		{
			"@type": "Person",
			"@id": authorId,
			name: "José Manuel Requena Plens",
			alternateName: "jmrplens",
			jobTitle: "R&D Engineer",
			url: authorUrl,
			image: "https://github.com/jmrplens.png",
			knowsAbout: [
				"Model Context Protocol",
				"Go",
				"Library Genesis",
				"Developer tooling",
				"AI assistants",
			],
			sameAs: [
				"https://github.com/jmrplens",
				"https://www.linkedin.com/in/jmrplens",
				"https://mstdn.jmrp.io/@jmrplens",
				"https://scholar.google.com/citations?user=9b0kPaUAAAAJ",
				"https://orcid.org/0000-0003-1250-6212",
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
			potentialAction: {
				"@type": "SearchAction",
				target: {
					"@type": "EntryPoint",
					urlTemplate:
						"https://jmrplens.github.io/libgen-mcp/?q={search_term_string}",
				},
				"query-input": "required name=search_term_string",
			},
		},
		{
			"@type": "SoftwareApplication",
			"@id": softwareId,
			name: "libgen-mcp",
			...(softwareVersion ? { softwareVersion } : {}),
			applicationCategory: "DeveloperApplication",
			applicationSubCategory: "Search Tools",
			operatingSystem: "Windows, macOS, Linux",
			programmingLanguage: "Go",
			url: repositoryUrl,
			downloadUrl: "https://github.com/jmrplens/libgen-mcp/releases/latest",
			installUrl: "https://jmrplens.github.io/libgen-mcp/getting-started/",
			releaseNotes:
				"https://github.com/jmrplens/libgen-mcp/releases/tag/v1.0.0",
			codeRepository: repositoryUrl,
			image: socialImage,
			screenshot: {
				"@type": "ImageObject",
				url: socialImageUrl,
				width: 1200,
				height: 630,
			},
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
			offers: {
				"@type": "Offer",
				price: "0",
				priceCurrency: "USD",
			},
			author: { "@id": authorId },
			sameAs: [`${fullUrl}/`, repositoryUrl],
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
			social: [
				{
					icon: "github",
					label: "GitHub",
					href: "https://github.com/jmrplens/libgen-mcp",
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
					],
				},
			],
		}),
	],
});
