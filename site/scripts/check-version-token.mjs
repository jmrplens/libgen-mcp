// Postbuild: fails if the release-version token survived anywhere in dist.
//
// Pages write %%VERSION%% where they show the current release, and three
// places replace it: remarkReleaseVersion for the HTML, toMarkdown for each
// page's Markdown copy, and therefore llms-docs.txt, which is assembled from
// those copies. Anything else a page feeds — frontmatter, structured data built
// from the prose, a component that reads an entry's raw body — does not pass
// through any of them, and a token that reached a reader there would be a
// command that installs nothing. So the output is read back, every text file of
// it, rather than trusting that each path was covered. Runs last in postbuild.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

import { VERSION_TOKEN } from "../src/lib/release-version.mjs";

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

const leftovers = walk(distDir)
	.filter((path) => readFileSync(path, "utf8").includes(VERSION_TOKEN))
	.map((path) => relative(distDir, path));

if (leftovers.length) {
	console.error(
		`[version-token] FAILED: ${VERSION_TOKEN} reached the output unreplaced in:`,
	);
	for (const path of leftovers) console.error(`  ${path}`);
	console.error(
		"\nThe token is replaced in a page's Markdown body only (see src/lib/release-version.mjs)." +
			"\nWrite the number some other way where it appears, or teach that path to replace it.",
	);
	process.exit(1);
}

console.log(`[version-token] no ${VERSION_TOKEN} left in dist`);
