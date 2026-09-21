#!/usr/bin/env node

// Verifies that local (relative) links and paths in this repository's Markdown
// and MDX files resolve to a real file on disk. External URLs, anchors, and
// absolute paths are skipped — only same-repo references are checked. Exits
// non-zero and lists the offending targets when any local link is broken.
//
// `--others --exclude-standard` is why a page written five minutes ago is
// covered. The listing used to be `--cached` only, which is the run where the
// answer matters least: a new page is untracked until it is committed, so
// running this before the commit reported "local links are valid" while having
// read nothing of the page whose links were the reason for running it. That is
// how docs/development/release-chain.md was written with two links to a page
// that did not exist on its branch, past a green local check. A gate that
// reports nothing because it found nothing to look at reads exactly like a gate
// that passed.
//
// `--exclude-standard` keeps `plan/` and every other ignored path out, and the
// explicit filters below keep the two skill trees out.

import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import path from "node:path";

const repoRoot = execFileSync("git", ["rev-parse", "--show-toplevel"], {
	encoding: "utf8",
}).trim();
const trackedDocs = [
	...new Set(
		execFileSync(
			"git",
			[
				"ls-files",
				"--cached",
				"--others",
				"--exclude-standard",
				"*.md",
				"*.mdx",
				":(glob)**/*.md",
				":(glob)**/*.mdx",
			],
			{
				cwd: repoRoot,
				encoding: "utf8",
			},
		)
			.trim()
			.split("\n")
			.filter(Boolean),
	),
]
	.filter((file) => !file.startsWith("plan/"))
	.filter((file) => !file.startsWith(".github/skills/"))
	.filter((file) => !file.startsWith(".claude/skills/"))
	// `--cached` lists what the index holds, which includes a file deleted from
	// the working tree but not yet staged as a deletion. Reading it would throw
	// before a single link was checked.
	.filter((file) => existsSync(path.join(repoRoot, file)));

const issues = [];

for (const file of trackedDocs) {
	const absoluteFile = path.join(repoRoot, file);
	const content = readFileSync(absoluteFile, "utf8");
	let inFence = false;
	const lines = content.split("\n");

	lines.forEach((line, index) => {
		if (/^\s*(```|~~~)/.test(line)) {
			inFence = !inFence;
			return;
		}
		if (inFence) {
			return;
		}

		for (const target of inlineLinks(line)) {
			checkTarget(file, index + 1, target);
		}

		const reference = line.match(/^\s*\[(?!\^)[^\]]+\]:\s*(<[^>]+>|\S+)/);
		if (reference) {
			checkTarget(file, index + 1, reference[1]);
		}
	});
}

if (issues.length > 0) {
	console.error("Broken local documentation links:");
	for (const issue of issues) {
		console.error(`- ${issue.file}:${issue.line} -> ${issue.target}`);
	}
	process.exit(1);
}

console.log(
	`Checked ${trackedDocs.length} Markdown/MDX files; local links are valid.`,
);

function inlineLinks(line) {
	const targets = [];
	const pattern = /!?\[[^\]]*\]\(\s*(<[^>]+>|[^)\s]+)(?:\s+"[^"]*")?\s*\)/g;
	let match;
	const prose = withoutCodeSpans(line);
	while ((match = pattern.exec(prose)) !== null) {
		targets.push(match[1]);
	}
	return targets;
}

// withoutCodeSpans blanks the contents of every backtick span, keeping the line
// the same length so a reported column still points at the right place.
//
// Text inside a code span is quoted, not linked: prose about a formatter that
// must not hand-write `[%s](%s)` was read as a link to a file named `%s` and
// failed this check, which is the checker being wrong about Markdown rather
// than the sentence being wrong about the code.
function withoutCodeSpans(line) {
	return line.replace(
		/(`+)([^`]|[^`][\s\S]*?[^`])\1(?!`)/g,
		(span, fence, body) => fence + " ".repeat(body.length) + fence,
	);
}

function checkTarget(file, line, rawTarget) {
	const target = normalizeTarget(rawTarget);
	if (shouldSkipTarget(target)) {
		return;
	}

	const withoutFragment = target.replace(/[?#].*$/, "");
	if (withoutFragment === "") {
		return;
	}

	const decoded = safeDecodeURIComponent(withoutFragment);
	const basePath = path.resolve(
		path.dirname(path.join(repoRoot, file)),
		decoded,
	);
	const candidates = [
		basePath,
		`${basePath}.md`,
		`${basePath}.mdx`,
		path.join(basePath, "README.md"),
		path.join(basePath, "index.md"),
		path.join(basePath, "index.mdx"),
	];

	if (!candidates.some((candidate) => existsSync(candidate))) {
		issues.push({ file, line, target });
	}
}

function normalizeTarget(rawTarget) {
	return rawTarget.trim().replace(/^<|>$/g, "");
}

function shouldSkipTarget(target) {
	if (target === "") {
		return true;
	}
	if (/^(?:[a-z][a-z0-9+.-]*:|#|\/)/i.test(target)) {
		return true;
	}
	if (/^(?:url|link|path|file|filename|directory|fn)$/i.test(target)) {
		return true;
	}
	if (/^[<{].*[>}]$/.test(target)) {
		return true;
	}
	return false;
}

function safeDecodeURIComponent(value) {
	try {
		return decodeURIComponent(value);
	} catch {
		return value;
	}
}
