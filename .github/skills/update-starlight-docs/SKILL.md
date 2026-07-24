---
name: update-starlight-docs
description: "Update the Astro Starlight docs site (site/src/content/docs/) in EN + ES parity when code changes affect user-facing features. Use when adding a tool, changing configuration, or modifying behavior in libgen-mcp."
---

# Update Starlight Documentation — libgen-mcp

Keep the Astro Starlight docs site in sync with user-facing code changes, in
both languages.

## Before Starting

1. Identify what changed that a user would notice (a tool, an option, behavior).
2. Read the current structure under `site/src/content/docs/`.
3. Determine the affected pages in **both** EN and ES.

## Documentation Architecture

Two systems coexist and both must stay current:

| System | Path | Audience | Format |
| --- | --- | --- | --- |
| Developer docs | `docs/` | Contributors, agents | Markdown (EN only) |
| User docs | `site/src/content/docs/` | End users | MDX (Starlight, EN + ES) |

**Rule:** a change to user-facing behavior updates BOTH systems, and within the
site, BOTH languages.

## Layout

English pages live at the root of the collection; Spanish mirrors them under
`es/`:

```text
site/src/content/docs/
├── index.mdx
├── getting-started.mdx
├── configuration.mdx
├── tools.mdx
├── how-search-works.mdx
├── architecture.mdx
├── troubleshooting.mdx
├── eval-results.mdx
└── es/
    ├── index.mdx
    ├── getting-started.mdx
    └── …            # one mirror per EN page
```

## Map code changes to pages

| Code change | Pages to touch |
| --- | --- |
| New/changed MCP tool | `tools.mdx` (+ `es/tools.mdx`) |
| New `LIBGEN_MCP_*` option | `configuration.mdx` (+ es) |
| Search/source behavior | `how-search-works.mdx` (+ es) |
| Structural/design change | `architecture.mdx` (+ es) |
| New failure mode / fix | `troubleshooting.mdx` (+ es) |

## Steps

1. **Edit the EN page first**, then translate the same change into the matching
   `es/` page. Do not leave English text in an ES page.
2. **Frontmatter** on every `.mdx`:

   ```yaml
   ---
   title: "Page Title"
   description: "Brief description for SEO and the sidebar"
   ---
   ```

3. **Use Starlight components** rather than raw HTML:

   ```mdx
   import { Aside, Tabs, TabItem, Steps } from '@astrojs/starlight/components';

   <Aside type="tip">Helpful tip</Aside>
   <Aside type="caution">Warning</Aside>
   ```

4. **Link between pages** with relative paths (e.g. `./configuration`), and keep
   sidebar ordering consistent across locales.

## Verify

```bash
cd site
pnpm install --frozen-lockfile   # first time only
pnpm run build                   # must be zero errors
pnpm run i18n:check              # EN/ES parity gate
pnpm run check                   # astro type/link check
```

Also run the repo-wide local link checker from the root, which covers the site
MDX and the developer docs together:

```bash
make check-doc-links
```

## Rules

- Update BOTH EN and ES pages; translations must be accurate, not placeholders.
- Keep parity: every EN page has an ES mirror (enforced by `pnpm run i18n:check`).
- Use Starlight components (Aside, Tabs, Steps) instead of raw HTML.
- Do not edit `astro.config.mjs` or the content config unless adding a new
  collection.
- If the tool surface changed, regenerate `llms.txt` (`go run ./cmd/gen_llms/`)
  and update `docs/tools.md` too.

## Validation Checklist

- [ ] All affected EN pages updated
- [ ] All affected ES pages updated with real translations
- [ ] Frontmatter (title, description) correct on each page
- [ ] Starlight components used (imports present)
- [ ] `pnpm run build`, `pnpm run i18n:check`, `pnpm run check` all pass
- [ ] `make check-doc-links` passes
- [ ] Developer docs (`docs/`) updated where applicable
