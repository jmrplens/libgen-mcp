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
 * The pages that carry an FAQPage, the envelope each one uses, and where its
 * questions are.
 *
 * Every FAQPage carries its own `@id` and belongs to the page's `#article`,
 * except `index`: it is a splash page, Head.astro mints no `#article` node for
 * it, and it points at `#website` instead. Three other pages used to point
 * there too, with no `@id`, for no reason but history, which left each of them
 * with two page-level nodes that nothing tied together.
 *
 * `questions` says where a page keeps them. Most pages have a "Frequently asked
 * questions" section of `###` headings, and the answer is everything under
 * each. responsible-use is the exception: the whole page is its questions,
 * one `##` each, and the answer is the paragraph that opens the section, since
 * what follows it (the 21-row source table, for one) is reference material a
 * spoken answer should not carry.
 */
const PAGES = [
	{ slug: "architecture", withId: true, isPartOf: "article" },
	{ slug: "how-search-works", withId: true, isPartOf: "article" },
	{ slug: "eval-results", withId: true, isPartOf: "article" },
	{ slug: "tools", withId: true, isPartOf: "article" },
	{ slug: "configuration", withId: true, isPartOf: "article" },
	{ slug: "getting-started", withId: true, isPartOf: "article" },
	{ slug: "index", withId: true, isPartOf: "website" },
	{ slug: "troubleshooting", withId: true, isPartOf: "article" },
	{ slug: "sources", withId: true, isPartOf: "article" },
	{ slug: "citations", withId: true, isPartOf: "article" },
	{ slug: "download-a-paper", withId: true, isPartOf: "article" },
	{ slug: "limitations", withId: true, isPartOf: "article" },
	{ slug: "comparison", withId: true, isPartOf: "article" },
	{
		slug: "responsible-use",
		withId: true,
		isPartOf: "article",
		questions: "sections",
	},
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
function entriesOf(body, locale, questions) {
	if (questions === "sections") return sectionEntriesOf(body);
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

/**
 * The questions of a page that is nothing but questions: every `##` heading,
 * answered by the paragraph that opens its section.
 */
function sectionEntriesOf(body) {
	const entries = [];
	for (const chunk of body.split(/^## /m).slice(1)) {
		const nl = chunk.indexOf("\n");
		if (nl < 0) continue;
		const q = chunk.slice(0, nl).trim();
		const firstParagraph = chunk
			.slice(nl)
			.trim()
			.split(/\n\s*\n/)[0];
		const a = answerText(firstParagraph);
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
		const entries = entriesOf(before.slice(bodyStart), locale, page.questions);
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
