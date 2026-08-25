// Structural audit of the source spec sheets (src/components/docs/Facts.astro).
//
// Two jobs, and the first is a gate.
//
// PARITY. `<Fact kind="…">` carries a language-independent vocabulary, which is
// what lets an English declaration and its Spanish twin be recognised as the
// same declaration. So every sheet must present the same sequence of kinds in
// both locales. This is not a nicety: check-i18n-parity.mjs protects the pages
// by counting headings and backticked identifiers, and neither notices if one
// locale quietly loses a "what it does not cover" row — the labels live in the
// component now, not in the prose it compares.
//
// CENSUS. Every `data-fact="not-covered"` row is a promise about a boundary,
// and the set of them is the honest answer to "what will this server fail to
// fetch?". A source with no such row has not been audited rather than having
// nothing to declare, so the census names the gaps. It reports rather than
// fails: the remedy is a sentence somebody has to verify, not a sentence this
// script can write.
//
// Only sheets that describe a SOURCE are censused. The same component also
// carries the troubleshooting page's symptom/meaning/fixes triples, and a
// symptom has no boundary to declare — counting those as gaps would make the
// census read as nine missing claims that were never owed.
//
// CHIPS. The page-title chips live in frontmatter and are rendered by a
// component, which puts them outside the reach of BOTH existing guards:
// check-i18n-parity.mjs compares headings and prose, and
// starlight-links-validator resolves links found in the content AST. So a
// locale can quietly lose a chip, and a chip can quietly point at a page that
// does not exist. Both are checked here.
//
// TABLE LAYOUT. Whether a markdown table stacks on a narrow screen is decided
// from its own contents, per file — so a page and its Spanish twin can land on
// opposite sides of the threshold and silently render with different layouts. A
// translated heading is easily a few characters wider, and architecture's source
// table sits within about a dozen pixels of flipping. The two locales must reach
// the same verdict for the same table.
//
// ENUM COVERAGE. A constrained parameter's allowed values are generated into
// src/data/tool-schema.json from the live surface, and the pages spell the same
// values out in prose because a reader needs them in a sentence, not a list. So
// a value added to a Go enum can reach the tool surface while the page keeps
// describing the old set — the docs would then be wrong in the one place a
// reader looks for what they may pass. Every generated value must appear in the
// slot that describes its field, in both locales.
//
// LEGAL NOTICE. The copyright notice used to sit on four pages per locale in
// two wordings that differed only in their opening subject. <LegalNotice />
// collapsed those eight to one string, but a ninth copy survives in PRIVACY.md
// as plain prose — and has to, because sync-privacy.mjs byte-compares that file
// and its Spanish twin is gated on a digest of it. Two copies cannot be one, so
// they are asserted equal instead.
//
// Run: node scripts/check-facts.mjs [--json]
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

import { answerText } from "../src/lib/faq-schema.mjs";
import { classify } from "../src/lib/wide-tables.mjs";

const docsDir = fileURLToPath(new URL("../src/content/docs", import.meta.url));
const esDir = join(docsDir, "es");

/** Every .mdx under `dir`, recursively, excluding the Spanish subtree. */
function walk(dir) {
	const out = [];
	for (const entry of readdirSync(dir)) {
		const p = join(dir, entry);
		if (statSync(p).isDirectory()) {
			if (p === esDir) continue;
			out.push(...walk(p));
		} else if (/\.mdx?$/.test(entry)) {
			out.push(p);
		}
	}
	return out;
}

/**
 * The spec sheets of one page: for each, the heading that introduces it and the
 * ordered kinds it declares. A free-text `label` row becomes "note", matching
 * what the component emits, because its wording is allowed to differ between
 * locales while its position is not.
 */
function sheets(path) {
	const text = readFileSync(path, "utf8");
	const found = [];
	// Headings and sheets in document order, so each sheet can be named by the
	// nearest heading above it.
	const marks = [
		...text.matchAll(/^(#{2,4}) (.+)$/gm),
		...text.matchAll(/<Facts[\s>]/g),
	].sort((a, b) => a.index - b.index);

	let heading = "(no heading)";
	for (const m of marks) {
		if (m[1]) {
			heading = m[2].trim();
			continue;
		}
		const end = text.indexOf("</Facts>", m.index);
		const body = text.slice(m.index, end === -1 ? undefined : end);
		const kinds = [...body.matchAll(/<Fact\s+(kind|label)="([^"]*)"/g)].map(
			(f) => (f[1] === "kind" ? f[2] : "note"),
		);
		found.push({ heading, kinds });
	}
	return found;
}

/** The frontmatter chip declarations of one page, in order. */
function chipsOf(path) {
	const text = readFileSync(path, "utf8");
	const frontmatter = /^---\n([\s\S]*?)\n---/.exec(text);
	if (!frontmatter) return [];
	const block = /^chips:\n((?:[ \t]+.*\n?)*)/m.exec(frontmatter[1]);
	if (!block) return [];
	return [
		...block[1].matchAll(/-\s+text:\s*"(.*?)"(?:\n\s+href:\s*"(.*?)")?/g),
	].map((m) => ({ text: m[1], href: m[2] }));
}

/** Every page slug the docs collection actually builds, as a route path. */
function knownRoutes() {
	const routes = new Set();
	for (const p of [...walk(docsDir), ...walk(esDir)]) {
		const rel = relative(docsDir, p)
			.replace(/\.mdx?$/, "")
			.replace(/(^|\/)index$/, "");
		routes.add(`/libgen-mcp/${rel}${rel ? "/" : ""}`);
	}
	return routes;
}

/** A sheet describes a download source if it declares what identifier keys it. */
const describesASource = (kinds) => kinds.includes("keyedBy");

/**
 * The English notice, as the i18n collection states it and as PRIVACY.md
 * repeats it. PRIVACY.md is prose, so the sentences are located rather than
 * parsed: the notice is whatever runs from its first sentence to the end of
 * its last.
 */
function legalNoticeCopies() {
	const strings = JSON.parse(
		readFileSync(
			new URL("../src/content/i18n/en.json", import.meta.url),
			"utf8",
		),
	);
	const fromI18n = strings["lgm.legal.notice"];
	const privacy = readFileSync(
		fileURLToPath(new URL("../../PRIVACY.md", import.meta.url)),
		"utf8",
	);
	// Anchored on the CLOSING sentence, and the paragraph is taken backwards
	// from it. Anchoring on the opening sentence instead makes the commonest
	// drift — a reworded first clause — read as "the notice is gone" rather
	// than as the mismatch it is.
	const TAIL = "legally entitled to access.";
	const end = privacy.indexOf(TAIL);
	if (end === -1) return { fromI18n, fromPrivacy: null };
	const paragraphStart = privacy.lastIndexOf("\n\n", end);
	const fromPrivacy = privacy
		.slice(paragraphStart === -1 ? 0 : paragraphStart, end + TAIL.length)
		.replace(/\s+/g, " ")
		.trim();
	return { fromI18n, fromPrivacy };
}

const problems = [];
const census = { withBoundary: [], withoutBoundary: [] };
const routes = knownRoutes();
let sheetCount = 0;
let otherSheets = 0;
let chipCount = 0;

for (const enPath of walk(docsDir)) {
	const rel = relative(docsDir, enPath);
	const esPath = join(esDir, rel);
	let es;
	try {
		es = sheets(esPath);
	} catch {
		problems.push(`${rel}: has no Spanish twin`);
		continue;
	}
	const en = sheets(enPath);

	// Chip parity, and chip destinations.
	const enChips = chipsOf(enPath);
	const esChips = chipsOf(esPath);
	if (enChips.length !== esChips.length) {
		problems.push(
			`${rel}: ${enChips.length} chip(s) in EN, ${esChips.length} in ES`,
		);
	} else {
		enChips.forEach((chip, i) => {
			chipCount++;
			if (Boolean(chip.href) !== Boolean(esChips[i].href)) {
				problems.push(
					`${rel} — chip ${i + 1}: only one locale gives it a link ` +
						`(EN "${chip.text}", ES "${esChips[i].text}")`,
				);
			}
		});
	}
	for (const [locale, list] of [
		["EN", enChips],
		["ES", esChips],
	]) {
		for (const chip of list) {
			if (chip.href && !routes.has(chip.href)) {
				problems.push(
					`${rel} (${locale}) — chip "${chip.text}" links to ${chip.href}, which is not a page`,
				);
			}
		}
	}

	// Spec sheets, for the pages that carry them.
	if (en.length === 0) continue;

	if (en.length !== es.length) {
		problems.push(
			`${rel}: ${en.length} spec sheet(s) in EN, ${es.length} in ES`,
		);
		continue;
	}

	en.forEach((sheet, i) => {
		sheetCount++;
		const twin = es[i];
		const a = sheet.kinds.join(",");
		const b = twin.kinds.join(",");
		if (a !== b) {
			problems.push(
				`${rel} — sheet ${i + 1} (${sheet.heading}):\n` +
					`      EN declares ${a}\n` +
					`      ES declares ${b}`,
			);
		}
		if (!describesASource(sheet.kinds)) {
			otherSheets++;
			return;
		}
		const name = `${rel}:${sheet.heading}`;
		(sheet.kinds.includes("notCovered")
			? census.withBoundary
			: census.withoutBoundary
		).push(name);
	});
}

/**
 * Every generated enum value that its field's prose fails to mention.
 *
 * The prose is authored and the values are generated, so this compares two
 * things that cannot be merged: the sentence a reader needs and the list the
 * server advertises.
 */
function enumGaps() {
	const schema = JSON.parse(
		readFileSync(
			new URL("../src/data/tool-schema.json", import.meta.url),
			"utf8",
		),
	);
	const gaps = [];
	for (const [locale, page] of [
		["EN", "tools.mdx"],
		["ES", "es/tools.mdx"],
	]) {
		let text;
		try {
			text = readFileSync(join(docsDir, page), "utf8");
		} catch {
			gaps.push(`${page}: not found`);
			continue;
		}
		for (const [tool, sections] of Object.entries(schema.tools)) {
			for (const [section, rows] of Object.entries(sections)) {
				const table = new RegExp(
					`<SchemaTable name="${tool}" section="${section}">([\\s\\S]*?)</SchemaTable>`,
				).exec(text);
				if (!table) continue;
				for (const row of rows) {
					if (!row.enum?.length) continue;
					const slot = new RegExp(
						`<Fragment slot="${row.name}">([\\s\\S]*?)</Fragment>`,
					).exec(table[1]);
					if (!slot) continue;
					const absent = row.enum.filter((value) => !slot[1].includes(value));
					if (absent.length) {
						gaps.push(
							`${locale} ${tool}.${section}.${row.name}: the schema allows ` +
								`${absent.join(", ")}, which the description never mentions`,
						);
					}
				}
			}
		}
	}
	return gaps;
}

/**
 * Every markdown table in a page, as rows of cell text.
 *
 * The cells are rendered to plain text first. The build classifies a table from
 * the RENDERED tree, where a code span is its contents and a link is its label;
 * measuring the raw Markdown instead counts backticks and `](…)` and reaches a
 * different verdict, which is a gate reporting a divergence that does not exist.
 * answerText is the same transform the FAQ schema uses.
 */
function markdownTables(text) {
	const body = text.slice(text.indexOf("\n---\n", 3) + 5);
	const tables = [];
	for (const block of body.matchAll(/^\|.*\|\n\|[-: |]+\|\n(?:\|.*\|\n)+/gm)) {
		tables.push(
			block[0]
				.trim()
				.split("\n")
				.filter((line, index) => index !== 1) // the separator row
				.map((line) =>
					line
						.replace(/^\||\|$/g, "")
						.split("|")
						.map((cell) => answerText(cell.trim())),
				),
		);
	}
	return tables;
}

/**
 * Pages whose two locales would render a table differently.
 *
 * Runs the site's own classifier — the one the build uses — over both sides, so
 * the thresholds live in exactly one place.
 */
function layoutDisagreements() {
	const out = [];
	for (const enPath of walk(docsDir)) {
		const rel = relative(docsDir, enPath);
		let esText;
		try {
			esText = readFileSync(join(esDir, rel), "utf8");
		} catch {
			continue;
		}
		const en = markdownTables(readFileSync(enPath, "utf8"));
		const es = markdownTables(esText);
		if (en.length !== es.length) {
			out.push(
				`${rel}: ${en.length} markdown table(s) in EN, ${es.length} in ES`,
			);
			continue;
		}
		en.forEach((rows, index) => {
			const a = classify(rows);
			const b = classify(es[index]);
			if (a !== b) {
				out.push(
					`${rel} — table ${index + 1} ("${rows[0]?.[0] ?? ""}"): ` +
						`EN ${a === null ? "stays a table" : `stacks (${a || "below the breakpoint"})`}, ` +
						`ES ${b === null ? "stays a table" : `stacks (${b || "below the breakpoint"})`}`,
				);
			}
		});
	}
	return out;
}

const layoutProblems = layoutDisagreements();
problems.push(...layoutProblems);

const enumProblems = enumGaps();
problems.push(...enumProblems);

const legal = legalNoticeCopies();
if (!legal.fromI18n) {
	problems.push("i18n/en.json declares no lgm.legal.notice");
} else if (!legal.fromPrivacy) {
	problems.push("PRIVACY.md no longer carries the copyright notice");
} else if (legal.fromI18n !== legal.fromPrivacy) {
	problems.push(
		"the copyright notice differs between the i18n collection and PRIVACY.md:\n" +
			`      i18n    : ${legal.fromI18n}\n` +
			`      PRIVACY : ${legal.fromPrivacy}`,
	);
}

if (process.argv.includes("--json")) {
	console.log(JSON.stringify({ problems, census, legal }, null, 2));
} else {
	const total = census.withBoundary.length + census.withoutBoundary.length;
	console.log(
		`spec sheets: ${sheetCount} in each locale, with matching declarations`,
	);
	console.log(
		`coverage claims: ${census.withBoundary.length} of ${total} source sheets declare a boundary` +
			` (${otherSheets} further sheet(s) describe something other than a source)`,
	);
	console.log(
		`page chips: ${chipCount} in each locale, matching, every link resolving`,
	);
	console.log(
		layoutProblems.length === 0
			? "table layout: both locales reach the same stacking verdict for every table"
			: "table layout: DISAGREEMENT (see below)",
	);
	console.log(
		enumProblems.length === 0
			? "enum coverage: every generated allowed value appears in its description"
			: "enum coverage: MISSING (see below)",
	);
	console.log(
		legal.fromI18n === legal.fromPrivacy
			? "legal notice: the component and PRIVACY.md state it identically"
			: "legal notice: MISMATCH (see below)",
	);
	for (const name of census.withoutBoundary) {
		console.log(`  no "what it does not cover" row: ${name}`);
	}
	if (problems.length) {
		console.error("\ncheck-facts FAILED:");
		for (const p of problems) console.error(`  ${p}`);
		if (problems.some((p) => p.includes("declares"))) {
			console.error(
				"\nA <Fact kind> is the language-independent half of the contract; the " +
					"two locales must declare the same kinds in the same order.",
			);
		}
	}
}

process.exit(problems.length ? 1 : 0);
