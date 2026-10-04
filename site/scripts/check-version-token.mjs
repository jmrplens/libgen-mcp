// Postbuild: fails if a release token survived anywhere in dist.
//
// Pages write %%VERSION%% (and, in the citation sample, %%RELEASE_YEAR%% and
// %%RELEASE_MONTH%%) where they show the current release, and three places
// replace them: remarkReleaseVersion for the HTML, toMarkdown for each page's
// Markdown copy, and therefore llms-docs.txt, which is assembled from those
// copies. Anything else a page feeds — frontmatter, structured data built from
// the prose, a component that reads an entry's raw body — does not pass through
// any of them, and a token that reached a reader there would be a command that
// installs nothing. So the output is read back, every text file of it, rather
// than trusting that each path was covered. Runs last in postbuild.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

import { RELEASE_TOKENS } from "../src/lib/release-version.mjs";

const distDir = fileURLToPath(new URL("../dist", import.meta.url));
const TEXT = /\.(?:html|md|txt|xml|json|js|css|svg|webmanifest)$/;

function walk(dir) {
	const out = [];
	for (const entry of readdirSync(dir)) {
		const path = join(dir, entry);
		if (statSync(path).isDirectory()) out.push(...walk(path));
		else if (TEXT.test(entry)) out.push(path);
	}
	return out;
}

const leftovers = walk(distDir).flatMap((path) => {
	const text = readFileSync(path, "utf8");
	return RELEASE_TOKENS.filter((token) => text.includes(token)).map(
		(token) => `${relative(distDir, path)}: ${token}`,
	);
});

if (leftovers.length) {
	console.error("[version-token] FAILED: a release token reached the output:");
	for (const line of leftovers) console.error(`  ${line}`);
	console.error(
		"\nThe tokens are replaced in a page's Markdown body only (see src/lib/release-version.mjs)." +
			"\nWrite the value some other way where it appears, or teach that path to replace it.",
	);
	process.exit(1);
}

console.log(`[version-token] no ${RELEASE_TOKENS.join(", ")} left in dist`);
