/**
 * Serves each page's Markdown copy under `astro dev`.
 *
 * The copies are written by scripts/emit-page-markdown.mjs in postbuild, which
 * means they exist in `dist` and nowhere else — so under the dev server both
 * "Copy page" and "View as Markdown" reached a 404 and the button flashed its
 * failure label. A feature that only works in production is a feature nobody
 * develops against.
 *
 * This renders on demand from the same source, with the same inputs, so what a
 * developer sees locally is what ships. It is dev-only by construction: the
 * hook only runs when Astro starts a dev server.
 */
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

import { residualComponents, toMarkdown } from "./page-markdown.mjs";
import { loadInputs } from "./page-markdown-inputs.mjs";

/**
 * @param {object} options
 * @param {string} options.root - The site directory.
 * @param {string} options.base - The site's base path, e.g. "/libgen-mcp".
 * @returns {import('astro').AstroIntegration}
 */
export function devMarkdown({ root, base }) {
	return {
		name: "libgen-mcp:dev-markdown",
		hooks: {
			"astro:server:setup": ({ server, logger }) => {
				const docsDir = join(root, "src/content/docs");
				// Vite strips the configured base before a middleware sees the URL, so
				// the request arrives as /sources/index.md rather than
				// /libgen-mcp/sources/index.md. Both forms are accepted: which one
				// shows up is an implementation detail of the dev server, not a
				// contract.
				const prefix = `${base.replace(/\/$/, "")}/`;

				server.middlewares.use((req, res, next) => {
					let url = (req.url ?? "").split("?")[0];
					if (!url.endsWith("/index.md")) return next();
					if (url.startsWith(prefix)) url = url.slice(prefix.length - 1);

					// /es/sources/index.md -> es/sources.mdx
					// /index.md            -> index.mdx
					const slug = url
						.replace(/^\//, "")
						.slice(0, -"index.md".length)
						.replace(/\/$/, "");
					const candidates = [
						join(docsDir, `${slug || "index"}.mdx`),
						join(docsDir, `${slug || "index"}.md`),
					];
					const source = candidates.find((path) => existsSync(path));
					if (!source) return next();

					try {
						const { labels, schema, chain } = loadInputs(root);
						const locale =
							slug.startsWith("es/") || slug === "es" ? "es" : "en";
						const markdown = toMarkdown(
							readFileSync(source, "utf8"),
							labels[locale],
							schema,
							chain,
						);
						const residual = residualComponents(markdown);
						if (residual.length > 0) {
							// Loud rather than silent: the same condition aborts the build,
							// and finding it here first is the point of serving these in dev.
							logger.warn(
								`${slug || "index"}: unrendered component(s) ${residual.join(", ")}`,
							);
						}
						res.setHeader("Content-Type", "text/markdown; charset=utf-8");
						res.end(markdown);
					} catch (error) {
						logger.error(`rendering ${slug || "index"}: ${error}`);
						next();
					}
				});
			},
		},
	};
}
