// Postbuild: writes a Markdown copy of every documentation page beside its HTML.
//
// `<page>/index.html` gains a sibling `<page>/index.md`, which is what the page
// actions link to and copy from. It runs after the HTML is built and does not
// touch it; the existing postbuild steps (normalize-dist-html, then
// add-sitemap-lastmod) are unaffected and must keep running before it.
//
// Rendering is src/lib/page-markdown.mjs. Any component it does not recognise
// aborts the build rather than shipping raw JSX to a reader.
import {
	mkdirSync,
	readdirSync,
	readFileSync,
	statSync,
	writeFileSync,
} from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

import { residualComponents, toMarkdown } from "../src/lib/page-markdown.mjs";

const root = fileURLToPath(new URL("..", import.meta.url));
const docsDir = join(root, "src/content/docs");
const distDir = join(root, "dist");

const schema = JSON.parse(
	readFileSync(join(root, "src/data/tool-schema.json"), "utf8"),
);

/**
 * The download chain's source ids, in order, read from src/data/sources.ts.
 *
 * A .mjs script cannot import a .ts module, so the array is parsed rather than
 * loaded. The parse is strict on purpose: it asserts the shape it expects and
 * the count the chain is known to have, so a refactor that changes either
 * aborts the build instead of quietly emitting a shorter chain into every
 * Markdown copy.
 */
function downloadChain() {
	const ts = readFileSync(join(root, "src/data/sources.ts"), "utf8");
	const array = /export const SOURCES: Source\[\] = \[([\s\S]*?)\n\];/.exec(ts);
	if (!array) {
		throw new Error(
			"[page-markdown] cannot find SOURCES in src/data/sources.ts",
		);
	}
	const ids = [...array[1].matchAll(/\bid:\s*"([a-z0-9_]+)"/g)].map(
		(m) => m[1],
	);
	const declared = (ts.match(/\bid:\s*"/g) ?? []).length;
	if (ids.length === 0 || ids.length !== declared) {
		throw new Error(
			`[page-markdown] parsed ${ids.length} source ids but the file declares ${declared}`,
		);
	}
	return ids;
}

const chain = downloadChain();

const labels = {
	en: JSON.parse(readFileSync(join(root, "src/content/i18n/en.json"), "utf8")),
	es: JSON.parse(readFileSync(join(root, "src/content/i18n/es.json"), "utf8")),
};

function walk(dir) {
	const out = [];
	for (const entry of readdirSync(dir)) {
		const p = join(dir, entry);
		if (statSync(p).isDirectory()) out.push(...walk(p));
		else if (/\.mdx?$/.test(entry)) out.push(p);
	}
	return out;
}

const problems = [];
let written = 0;

for (const source of walk(docsDir)) {
	const rel = relative(docsDir, source);
	const locale = rel.startsWith(`es${"/"}`) ? "es" : "en";
	const slug = rel.replace(/\.mdx?$/, "").replace(/(^|\/)index$/, "");

	const markdown = toMarkdown(
		readFileSync(source, "utf8"),
		labels[locale],
		schema,
		chain,
	);
	const residual = residualComponents(markdown);
	if (residual.length) {
		problems.push(`${rel}: unrendered component(s) ${residual.join(", ")}`);
		continue;
	}

	// Mirror the HTML's own layout: Astro emits `<slug>/index.html`, so the
	// Markdown sits at `<slug>/index.md` and the two share a directory.
	const target = join(distDir, slug, "index.md");
	mkdirSync(dirname(target), { recursive: true });
	writeFileSync(target, markdown);
	written++;
}

if (problems.length) {
	console.error("[page-markdown] FAILED:");
	for (const p of problems) console.error(`  ${p}`);
	console.error(
		"\nTeach src/lib/page-markdown.mjs to render it, or the Markdown copy ships JSX.",
	);
	process.exit(1);
}

console.log(`[page-markdown] wrote ${written} Markdown copies`);
