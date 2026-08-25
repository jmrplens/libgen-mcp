// Rewrites each page's FAQPage JSON-LD from the questions the page shows.
//
// The answers were stated twice — once as visible prose, once in a hand-written
// block in the frontmatter — with nothing comparing them across 84 answers in
// two locales. Two had drifted: troubleshooting's "Where does libgen-mcp save
// downloaded files?" shipped structured data 178 characters shorter than its own
// page, and named the source `sci-hub`, a spelling used nowhere else. Deriving
// the schema from the prose is what makes that impossible rather than merely
// unlikely.
//
// scripts/sync-privacy.mjs already does this for privacy.md and is the
// precedent; the answer transform lives in src/lib/faq-schema.mjs so the two
// cannot disagree. privacy.md is NOT handled here — it keeps its own generator,
// its own envelope and its digest gate.
//
// Usage:
//   node scripts/gen-faq-schema.mjs           # rewrite
//   node scripts/gen-faq-schema.mjs --check   # fail if any page is stale (CI)
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { answerText, faqNode, serialize } from "../src/lib/faq-schema.mjs";

const docsDir = fileURLToPath(new URL("../src/content/docs", import.meta.url));
const SITE = "https://jmrplens.github.io";
const BASE = "/libgen-mcp";

/**
 * The pages that carry an FAQPage, and the envelope each one already uses.
 *
 * The envelopes are NOT uniform, and the differences are preserved rather than
 * normalised. `index` points at `#website` because it is a splash page and
 * Head.astro mints no `#article` node for it; four other pages do the same
 * without that reason, which is a pre-existing inconsistency this generator
 * deliberately does not "fix" — changing where a node says it belongs is a
 * structured-data decision, not a refactor.
 */
const PAGES = [
	{ slug: "architecture", withId: true, isPartOf: "article" },
	{ slug: "how-search-works", withId: true, isPartOf: "article" },
	{ slug: "eval-results", withId: true, isPartOf: "article" },
	{ slug: "tools", withId: true, isPartOf: "article" },
	{ slug: "configuration", withId: false, isPartOf: "website" },
	{ slug: "getting-started", withId: false, isPartOf: "website" },
	{ slug: "index", withId: false, isPartOf: "website" },
	{ slug: "troubleshooting", withId: false, isPartOf: "website" },
];

const SOFTWARE_ID = "https://github.com/jmrplens/libgen-mcp#software";

/** The heading that opens the FAQ region, per locale. */
const FAQ_HEADING = {
	en: "## Frequently asked questions",
	es: "## Preguntas frecuentes",
};

/**
 * The questions and answers a page shows, read from its body.
 *
 * Split on the `###` rather than matched with a lookahead: under /m an `$`
 * matches at the blank line after every question, so a lookahead-terminated
 * capture yields an empty answer for each one. sync-privacy.mjs records the
 * same trap.
 */
function entriesOf(body, locale) {
	const region = body.split(FAQ_HEADING[locale])[1];
	if (!region) return [];
	const untilNextSection = region.split(/^## /m)[0];
	const entries = [];
	for (const chunk of untilNextSection.split(/^### /m).slice(1)) {
		const nl = chunk.indexOf("\n");
		if (nl < 0) continue;
		const q = chunk.slice(0, nl).trim();
		const a = answerText(chunk.slice(nl));
		if (q && a) entries.push({ q, a });
	}
	return entries;
}

/** The page's canonical URL. */
const urlFor = (slug, locale) =>
	slug === "index"
		? `${SITE}${BASE}/${locale === "es" ? "es/" : ""}`
		: `${SITE}${BASE}/${locale === "es" ? "es/" : ""}${slug}/`;

/**
 * Replaces the FAQPage object inside a page's frontmatter, leaving every other
 * `head:` entry — HowTo, ItemList, Dataset, the `<title>` override, the og:type
 * meta — byte-identical.
 */
function rewrite(text, node) {
	const marker = '"@type": "FAQPage"';
	const at = text.indexOf(marker);
	if (at === -1) return null;

	// The object runs from the `{` that opens it to the matching `}`. Braces are
	// counted rather than matched with a regex: the block nests three levels and
	// carries braces inside quoted answers.
	const open = text.lastIndexOf("{", at);
	let depth = 0;
	let close = -1;
	for (let i = open; i < text.length; i++) {
		if (text[i] === "{") depth++;
		else if (text[i] === "}") {
			depth--;
			if (depth === 0) {
				close = i;
				break;
			}
		}
	}
	if (close === -1) return null;

	// Every line of the emitted JSON takes the indent the opening brace sits at.
	const lineStart = text.lastIndexOf("\n", open) + 1;
	const indent = text.slice(lineStart, open);
	const body = serialize(node)
		.split("\n")
		.map((line, i) => (i === 0 ? line : indent + line))
		.join("\n");
	return text.slice(0, open) + body + text.slice(close + 1);
}

const check = process.argv.includes("--check");
const stale = [];
let written = 0;
let questions = 0;

for (const page of PAGES) {
	for (const locale of ["en", "es"]) {
		const path = join(docsDir, locale === "es" ? "es" : "", `${page.slug}.mdx`);
		const before = readFileSync(path, "utf8");
		const bodyStart = before.indexOf("\n---\n", 3) + 5;
		const entries = entriesOf(before.slice(bodyStart), locale);
		if (entries.length === 0) {
			throw new Error(
				`${page.slug} (${locale}): no FAQ questions found in the body`,
			);
		}
		questions += entries.length;

		const pageUrl = urlFor(page.slug, locale);
		const node = faqNode({
			entries,
			inLanguage: locale,
			pageUrl,
			withId: page.withId,
			isPartOf:
				page.isPartOf === "article"
					? `${pageUrl}#article`
					: `${SITE}${BASE}/#website`,
			about: SOFTWARE_ID,
		});

		const after = rewrite(before, node);
		if (after === null) {
			throw new Error(
				`${page.slug} (${locale}): no FAQPage block in the frontmatter`,
			);
		}
		if (after === before) continue;
		if (check) {
			stale.push(`${locale === "es" ? "es/" : ""}${page.slug}.mdx`);
		} else {
			writeFileSync(path, after);
			written++;
		}
	}
}

if (check) {
	if (stale.length) {
		console.error(
			"FAQ schema check FAILED — these pages' JSON-LD no longer matches their prose:",
		);
		for (const s of stale) console.error(`  ${s}`);
		console.error("\nRun `node scripts/gen-faq-schema.mjs` to regenerate.");
		process.exit(1);
	}
	console.log(
		`FAQ schema OK: ${questions} answers across ${PAGES.length * 2} pages match their prose`,
	);
} else {
	console.log(`FAQ schema: ${questions} answers, ${written} page(s) rewritten`);
}
