import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";
import starlightLinksValidator from "starlight-links-validator";
import mermaid from "astro-mermaid";

const siteDescription =
  "Open source Model Context Protocol server for Library Genesis — search books and resolve download links from your AI assistant.";

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
      logo: {
        dark: "./src/assets/logo-dark.svg",
        light: "./src/assets/logo-light.svg",
        alt: "LibGen MCP",
      },
      favicon: "/favicon.svg",
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
