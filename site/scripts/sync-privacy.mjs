// Publishes the repo-root PRIVACY.md as a page on the documentation site.
//
// The policy is the strongest trust artifact this project has, and it used to be
// reachable only from a small footer link to GitHub — so an AI crawler judging
// the documentation domain never read it. It is generated rather than copied so
// the repo file stays the single source of truth.
//
// The Spanish page is translated by hand, which is a drift risk the English page
// does not have. Both carry the source digest in their frontmatter, and --check
// fails when either has fallen behind PRIVACY.md.
//
// Usage:
//   node scripts/sync-privacy.mjs
//   node scripts/sync-privacy.mjs --check

import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = join(here, "..", "..");
const SOURCE = join(repoRoot, "PRIVACY.md");
const PAGE_EN = join(repoRoot, "site/src/content/docs/privacy.md");
const PAGE_ES = join(repoRoot, "site/src/content/docs/es/privacy.md");

const check = process.argv.includes("--check");
const source = readFileSync(SOURCE, "utf8");
const digest = createHash("sha256").update(source).digest("hex").slice(0, 16);

// Drop the H1 and the "Last updated" line: Starlight renders the title from
// frontmatter, and the date belongs in the frontmatter too.
const lastUpdated = /^Last updated:\s*(\S+)/m.exec(source)?.[1] ?? "";
// A link to the documentation is written with the jmrp.io address in the repo,
// which is where a reader of PRIVACY.md on GitHub should land. On the site
// itself that address is a redirect to the page next door, so it becomes a
// site-relative link and a reader, or a crawler, takes no hop to get there.
const body = source
	.replace(/^#\s+.*\n/, "")
	.replace(/^Last updated:.*\n/m, "")
	.replaceAll("https://jmrp.io/docs/libgen-mcp/", "/libgen-mcp/")
	.trim();

// The description carries a colon, so it has to be quoted to stay valid YAML.
// `extra` is appended inside the frontmatter block, so a caller can add
// structured YAML keys (e.g. a `head:` entry) rather than body content.
const frontmatter = (title, description, extra = "") => `---
title: ${title}
description: "${description}"
datePublished: "${lastUpdated}"
# Generated from PRIVACY.md by scripts/sync-privacy.mjs — do not edit by hand.
privacySource: "${digest}"
${extra}---
`;

// FAQPage for the "Frequently asked questions" section of PRIVACY.md. The
// answers are the visible prose verbatim, as everywhere else on the site — an
// answer that exists only in markup is a guideline violation, and this page in
// particular is the one an assistant consults before recommending the tool.
// Derived from the prose it describes so the two cannot drift: a question
// renamed in PRIVACY.md renames it here too, and the Spanish page's block is
// derived from the Spanish page's own FAQ section the same way. That page's
// block used to be written by hand, and it kept answering "no" to the telemetry
// question after its prose had learned about the opt-in exporter.
function faqSchema(text, sectionHeading, inLanguage, pageUrl) {
	const section = text.split(`## ${sectionHeading}`)[1]?.split("\n## ")[0];
	if (!section) return "";
	// Split on the H3s rather than matching them: an `$` under /m matches at the
	// blank line that follows every question, so a lookahead-terminated capture
	// silently yields an empty answer for each one.
	const entries = [];
	for (const chunk of section.split(/^### /m).slice(1)) {
		const nl = chunk.indexOf("\n");
		if (nl < 0) continue;
		const question = chunk.slice(0, nl).trim();
		const answer = chunk
			.slice(nl)
			.trim()
			.replace(/\[([^\]]+)\]\([^)]+\)/g, "$1") // links → their text
			.replace(/[`*]/g, "")
			.replace(/\s*\n\s*/g, " ");
		if (question && answer) entries.push({ q: question, a: answer });
	}
	if (entries.length === 0) return "";
	const json = JSON.stringify(
		{
			"@context": "https://schema.org",
			"@type": "FAQPage",
			"@id": `${pageUrl}#faq`,
			inLanguage,
			isPartOf: { "@id": pageUrl },
			mainEntity: entries.map(({ q, a }) => ({
				"@type": "Question",
				name: q,
				acceptedAnswer: { "@type": "Answer", text: a },
			})),
		},
		null,
		2,
	);
	return `head:
  - tag: script
    attrs:
      type: application/ld+json
    content: |
${json
	.split("\n")
	.map((l) => `      ${l}`)
	.join("\n")}
`;
}

const enPage = `${frontmatter(
	"Privacy policy",
	"What libgen-mcp handles and where it goes: nothing reaches the maintainer, telemetry is off by default and goes to your own collector, and every network destination is listed per tool.",
	faqSchema(
		source,
		"Frequently asked questions",
		"en",
		"https://jmrplens.github.io/libgen-mcp/privacy/",
	),
)}
${body}
`;

// The Spanish page as it should be: its hand-translated frontmatter and body
// untouched, and its `head:` block, which closes the frontmatter, regenerated
// from its own "Preguntas frecuentes" section.
function expectedEsPage() {
	const page = readFileSync(PAGE_ES, "utf8");
	const end = page.indexOf("\n---\n", 4);
	const head = page.indexOf("\nhead:\n");
	if (end < 0 || head < 0 || head > end) return page;
	const esBody = page.slice(end + "\n---\n".length);
	const block = faqSchema(
		esBody,
		"Preguntas frecuentes",
		"es",
		"https://jmrplens.github.io/libgen-mcp/es/privacy/",
	);
	return `${page.slice(0, head + 1)}${block}${page.slice(end + 1)}`;
}

function readDigest(path) {
	try {
		return (
			/^privacySource:\s*"([^"]+)"/m.exec(readFileSync(path, "utf8"))?.[1] ?? ""
		);
	} catch {
		return "";
	}
}

if (check) {
	const problems = [];
	if (readFileSync(PAGE_EN, "utf8") !== enPage) {
		problems.push(`${PAGE_EN} is out of date`);
	}
	if (readDigest(PAGE_ES) !== digest) {
		problems.push(
			`${PAGE_ES} was translated from an older PRIVACY.md — review it and update its privacySource to ${digest}`,
		);
	}
	if (readFileSync(PAGE_ES, "utf8") !== expectedEsPage()) {
		problems.push(
			`${PAGE_ES}: its FAQ structured data no longer matches its own "Preguntas frecuentes" prose`,
		);
	}
	if (problems.length > 0) {
		console.error(problems.join("\n"));
		console.error(
			"run `node scripts/sync-privacy.mjs` (and update the Spanish page)",
		);
		process.exit(1);
	}
	console.log("privacy pages are in sync with PRIVACY.md");
} else {
	writeFileSync(PAGE_EN, enPage);
	console.log(`wrote ${PAGE_EN} (source digest ${digest})`);
	writeFileSync(PAGE_ES, expectedEsPage());
	console.log(`regenerated the FAQ structured data of ${PAGE_ES}`);
	if (readDigest(PAGE_ES) !== digest) {
		console.log(
			`note: ${PAGE_ES} still records digest ${readDigest(PAGE_ES) || "none"}; review the translation and set privacySource to ${digest}`,
		);
	}
}
