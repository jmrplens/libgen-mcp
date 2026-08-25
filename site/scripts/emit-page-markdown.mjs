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

import {
	COMPONENTS,
	residualComponents,
	toMarkdown,
} from "../src/lib/page-markdown.mjs";
import { loadInputs } from "../src/lib/page-markdown-inputs.mjs";

const root = fileURLToPath(new URL("..", import.meta.url));
const docsDir = join(root, "src/content/docs");

const { labels, schema, chain } = loadInputs(root);
const distDir = join(root, "dist");

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

	// The 404 is the one page Astro emits as `<slug>.html` rather than
	// `<slug>/index.html`, so mirroring the layout would leave an orphan
	// directory holding nothing but a Markdown file. It is also the one page
	// with no table of contents, which means no page actions and nothing to
	// read the copy.
	if (/(^|\/)404$/.test(slug)) continue;

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
		"\nTeach src/lib/page-markdown.mjs to render it, or the Markdown copy ships JSX." +
			`\nIt currently knows: ${COMPONENTS.join(", ")}.`,
	);
	process.exit(1);
}

console.log(`[page-markdown] wrote ${written} Markdown copies`);
