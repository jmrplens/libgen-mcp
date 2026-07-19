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
      url: authorUrl,
      image: "https://github.com/jmrplens.png",
      sameAs: [
        "https://github.com/jmrplens",
        "https://linkedin.com/in/jmrplens",
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
    },
    {
      "@type": "SoftwareApplication",
      "@id": softwareId,
      name: "libgen-mcp",
      ...(softwareVersion ? { softwareVersion } : {}),
      applicationCategory: "DeveloperApplication",
      operatingSystem: "Windows, macOS, Linux",
      programmingLanguage: "Go",
      url: repositoryUrl,
      codeRepository: repositoryUrl,
      image: socialImage,
      license: "https://opensource.org/licenses/MIT",
      isAccessibleForFree: true,
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
