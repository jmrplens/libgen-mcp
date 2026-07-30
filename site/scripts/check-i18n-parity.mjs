// Validates EN/ES translation parity on two levels.
//
// Structural: every English page under src/content/docs/ (outside es/) must
// have a Spanish counterpart at the same relative path under
// src/content/docs/es/, and vice-versa.
//
// Content: a page pair must keep the same number of headings, and every
// identifier-shaped token in the English page (LIBGEN_MCP_* variables, tool and
// source names) must appear in its Spanish counterpart. File-level parity alone
// let real drift through — a Spanish page silently lost `LIBGEN_MCP_DOWNLOAD_DIR`
// from a sentence, and another dropped a "never more than one" constraint — so
// the checker now compares what the pages say, not only that both exist.
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const docsDir = fileURLToPath(new URL("../src/content/docs", import.meta.url));
const esDir = join(docsDir, "es");

function walk(dir) {
	const out = [];
	for (const entry of readdirSync(dir)) {
		const p = join(dir, entry);
		if (statSync(p).isDirectory()) {
			if (p === esDir) continue; // skip the ES subtree when collecting EN
			out.push(...walk(p));
		} else if (/\.(md|mdx)$/.test(entry)) {
			out.push(p);
		}
	}
	return out;
}

const enPages = walk(docsDir).map((p) => relative(docsDir, p));
const esPages = walk(esDir).map((p) => relative(esDir, p));

const enSet = new Set(enPages);
const esSet = new Set(esPages);

const missingEs = enPages.filter((p) => !esSet.has(p)).sort();
const orphanEs = esPages.filter((p) => !enSet.has(p)).sort();

if (missingEs.length || orphanEs.length) {
	console.error("i18n parity check FAILED:");
	for (const p of missingEs)
		console.error(`  EN page missing ES translation: ${p}`);
	for (const p of orphanEs)
		console.error(`  ES page with no EN original:    es/${p}`);
	process.exit(1);
}

// Strip frontmatter and fenced code: both legitimately differ between locales
// (translated titles, and code that is identical by design and would swamp the
// token diff).
function prose(path) {
	let text = readFileSync(path, "utf8");
	text = text.replace(/^---\n[\s\S]*?\n---\n/, "");
	return text.replace(/```[\s\S]*?```/g, "");
}

// Tokens that must survive translation. A translated sentence that drops one is
// describing different behaviour from the English page, which is the drift worth
// failing on.
//
// Environment variables are matched by shape. Tool and source names cannot be —
// `read`, `search` and `core` are ordinary English words a sentence may use
// incidentally — so they are matched only inside backticks, where they are
// unambiguously the identifier rather than the word.
const ENV_VAR = /\b(LIBGEN_[A-Z0-9_]+)\b/g;
const CODE_IDENTIFIER = new RegExp(
	"`(" +
		[
			"search",
			"get_details",
			"download",
			"read",
			"unpaywall",
			"europepmc",
			"biorxiv",
			"rfc",
			"nist",
			"dagstuhl",
			"acl",
			"zenodo",
			"fatcat",
			"core",
			"oapen",
			"archive",
			"scihub",
			"scidb",
			"libgen",
			"randombook",
			"annas",
			"arxiv",
			"crossref",
			"openlibrary",
			"gutenberg",
			"dblp",
			"pubmed",
			"eric",
		].join("|") +
		")`",
	"g",
);

// Every tracked token in a page, normalized so the two sides compare equal.
function identifiers(text) {
	const found = new Set(text.match(ENV_VAR) ?? []);
	for (const m of text.matchAll(CODE_IDENTIFIER)) found.add("`" + m[1] + "`");
	return found;
}

function headingCount(text) {
	return (text.match(/^#{2,6} /gm) ?? []).length;
}

const contentProblems = [];
for (const page of enPages) {
	const en = prose(join(docsDir, page));
	const es = prose(join(esDir, page));

	const enHeadings = headingCount(en);
	const esHeadings = headingCount(es);
	if (enHeadings !== esHeadings) {
		contentProblems.push(
			`  ${page}: ${enHeadings} headings in EN, ${esHeadings} in ES`,
		);
	}

	const esTokens = identifiers(es);
	const missing = [...identifiers(en)]
		.filter((token) => !esTokens.has(token))
		.sort();
	if (missing.length > 0) {
		contentProblems.push(
			`  ${page}: present in EN but not in ES — ${missing.join(", ")}`,
		);
	}
}

if (contentProblems.length > 0) {
	console.error("i18n content parity check FAILED:");
	for (const problem of contentProblems) console.error(problem);
	console.error(
		"\nUpdate the Spanish page so it states the same thing as the English one.",
	);
	process.exit(1);
}

console.log(
	`i18n parity OK: ${enPages.length} EN pages each have an ES translation, with matching headings and identifiers.`,
);
