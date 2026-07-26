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
const body = source
	.replace(/^#\s+.*\n/, "")
	.replace(/^Last updated:.*\n/m, "")
	.trim();

// The description carries a colon, so it has to be quoted to stay valid YAML.
const frontmatter = (title, description, extra = "") => `---
title: ${title}
description: "${description}"
datePublished: "${lastUpdated}"
# Generated from PRIVACY.md by scripts/sync-privacy.mjs — do not edit by hand.
privacySource: "${digest}"
---
${extra}`;

const enPage = `${frontmatter(
	"Privacy policy",
	"What libgen-mcp handles and where it goes: no telemetry, no analytics, and every network destination listed per tool.",
)}
${body}
`;

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
	if (readDigest(PAGE_ES) !== digest) {
		console.log(
			`note: ${PAGE_ES} still records digest ${readDigest(PAGE_ES) || "none"}; review the translation and set privacySource to ${digest}`,
		);
	}
}
