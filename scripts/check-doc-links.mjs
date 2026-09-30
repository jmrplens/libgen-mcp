#!/usr/bin/env node

// Verifies that local links and paths in this repository's Markdown and MDX
// files resolve, and that the anchor half of each one names a heading or an
// explicit id in the file it lands in. External URLs are skipped. Exits
// non-zero and lists the offending targets, with the reason, when any local
// link is broken.
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
import { existsSync, readFileSync, statSync } from "node:fs";
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

// ---------------------------------------------------------------------------
// Anchors
//
// One file is read in one place, and the place decides how its headings are
// named. A file under site/src/content is a page of the documentation site,
// rendered by Starlight; every other tracked file is read in the repository,
// rendered by GitHub. The two agree on almost every heading in this corpus and
// disagree in ways that are easy to hit by accident, so each is taught here
// rather than one being made to stand for both.
// ---------------------------------------------------------------------------

// GitHub's anchors come from html-pipeline's TableOfContentsFilter: the text is
// downcased, every character outside Ruby's \p{Word} plus hyphen and space is
// dropped, the spaces become hyphens, and a repeat takes "-N" from a counter
// kept per original slug. Ruby's \p{Word} is Alphabetic, Mark, Digit,
// Connector_Punctuation and Join_Control, which is why the two zero-width
// joiners of an emoji sequence survive in a GitHub anchor and nowhere else.
const gitHubPunctuation =
	/[^\p{L}\p{M}\p{Nd}\p{Nl}\p{Pc}\p{Join_Control}\- ]/gu;

// Starlight's come from Astro's rehypeHeadingIds, which is github-slugger: the
// same shape without Join_Control, and a repeat resolved by walking up from
// "-1" until a free name is found rather than by a counter. The two orders part
// company on a page whose headings are "Foo", "Foo" and "Foo 1": GitHub hands
// the third the "foo-1" it already gave the second, github-slugger gives it
// "foo-1-1".
const sluggerPunctuation = /[^\p{L}\p{M}\p{Nd}\p{Nl}\p{Pc}\- ]/gu;

const gitHubDialect = {
	name: "GitHub",
	// A page read in the repository has only the anchors its own headings and
	// HTML make.
	pageAnchors: [],
	newSlugger() {
		const counts = new Map();
		return (text) => {
			const id = text
				.toLowerCase()
				.replace(gitHubPunctuation, "")
				.replaceAll(" ", "-");
			const seen = counts.get(id) ?? 0;
			counts.set(id, seen + 1);
			return seen > 0 ? `${id}-${seen}` : id;
		};
	},
};

const starlightDialect = {
	name: "Starlight",
	// Starlight gives every page a "_top" anchor, on the <h1> it renders from
	// the frontmatter title. That title is not a Markdown heading and never
	// becomes a slug, which is why a site page is linked by "#_top" and a
	// repository file by the slug of the "# Title" it carries in its body.
	pageAnchors: ["_top"],
	newSlugger() {
		const occurrences = new Map();
		return (text) => {
			const original = text
				.toLowerCase()
				.replace(sluggerPunctuation, "")
				.replaceAll(" ", "-");
			let result = original;
			while (occurrences.has(result)) {
				const next = (occurrences.get(original) ?? 0) + 1;
				occurrences.set(original, next);
				result = `${original}-${next}`;
			}
			occurrences.set(result, 0);
			return result;
		};
	},
};

const siteContentRoot =
	path.join(repoRoot, "site", "src", "content") + path.sep;

// The documentation site is served under this base, so a page links another as
// "/libgen-mcp/<route>/" or "/libgen-mcp/es/<route>/" rather than by a relative
// path to its source. Such a route names a file under site/src/content/docs,
// and it is resolved to that file so its anchor can be held against the same
// headings a relative link would be. The value is astro.config.mjs's basePath.
const siteBase = "/libgen-mcp";
const siteDocsRoot = path.join(repoRoot, "site", "src", "content", "docs");

const anchorCache = new Map();

const issues = [];
let anchorsChecked = 0;

for (const file of trackedDocs) {
	const absoluteFile = path.join(repoRoot, file);
	const content = readFileSync(absoluteFile, "utf8");

	eachProseLine(content, (line, lineNumber) => {
		for (const target of inlineLinks(line)) {
			checkTarget(file, lineNumber, target);
		}

		const reference = line.match(/^\s*\[(?!\^)[^\]]+\]:\s*(<[^>]+>|\S+)/);
		if (reference) {
			checkTarget(file, lineNumber, reference[1]);
		}
	});
}

if (issues.length > 0) {
	console.error("Broken local documentation links:");
	for (const issue of issues) {
		console.error(
			`- ${issue.file}:${issue.line} -> ${issue.target} (${issue.reason})`,
		);
	}
	process.exit(1);
}

console.log(
	`Checked ${trackedDocs.length} Markdown/MDX files; local links are valid, ` +
		`including ${anchorsChecked} anchors.`,
);

// eachProseLine visits the lines a reader is shown as prose, which is every
// line outside a fenced code block.
//
// A fence closes on the marker that opened it, at the same length or longer,
// and on nothing else. A toggle that flips on any line starting a fence reads
// the ``` inside a ```` block as its end, and every line after that in the
// opposite state: whole sections scanned as prose or skipped as code depending
// on how many inner fences had gone by.
function eachProseLine(content, visit) {
	const lines = content.split("\n");
	let fence = null;

	for (let index = 0; index < lines.length; index++) {
		const line = lines[index].replace(/\r$/, "");
		const marker = line.match(/^\s*(`{3,}|~{3,})(.*)$/);

		if (fence) {
			if (
				marker &&
				marker[1][0] === fence.char &&
				marker[1].length >= fence.length &&
				marker[2].trim() === ""
			) {
				fence = null;
			}
			continue;
		}

		if (marker) {
			fence = { char: marker[1][0], length: marker[1].length };
			continue;
		}

		visit(line, index + 1);
	}
}

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

	const hash = target.indexOf("#");
	const fragment = hash === -1 ? "" : target.slice(hash + 1);
	const withoutFragment = (
		hash === -1 ? target : target.slice(0, hash)
	).replace(/\?.*$/, "");

	// A bare "#anchor" points into the file the link is written in.
	if (withoutFragment === "") {
		checkFragment(file, line, target, path.join(repoRoot, file), fragment);
		return;
	}

	if (withoutFragment.startsWith("/")) {
		checkSiteRoute(file, line, target, withoutFragment, fragment);
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

	const resolved = candidates.find((candidate) => existsSync(candidate));
	if (!resolved) {
		issues.push({ file, line, target, reason: "no such file" });
		return;
	}

	checkFragment(file, line, target, pageOf(resolved), fragment);
}

// checkSiteRoute resolves a root-relative link to the page of the
// documentation site it names. A route under the site's base with no file
// extension is a Starlight page, and its source is
// site/src/content/docs/<route>.md(x) or <route>/index.md(x). Anything else
// rooted at "/" (a generated asset such as llms.txt, an image, a route outside
// the base) is not a page this checker can read, and starlight-links-validator
// answers for it at build time.
function checkSiteRoute(file, line, target, routePath, fragment) {
	const decoded = safeDecodeURIComponent(routePath);
	if (decoded !== siteBase && !decoded.startsWith(`${siteBase}/`)) {
		return;
	}
	const route = decoded.slice(siteBase.length).replace(/^\/+|\/+$/g, "");
	if (/\.[A-Za-z0-9]+$/.test(route)) {
		return;
	}

	const base = route === "" ? siteDocsRoot : path.join(siteDocsRoot, route);
	const candidates =
		route === ""
			? [path.join(base, "index.mdx"), path.join(base, "index.md")]
			: [
					`${base}.mdx`,
					`${base}.md`,
					path.join(base, "index.mdx"),
					path.join(base, "index.md"),
				];
	const resolved = candidates.find((candidate) => existsSync(candidate));
	if (!resolved) {
		issues.push({ file, line, target, reason: "no such site page" });
		return;
	}

	checkFragment(file, line, target, resolved, fragment);
}

// pageOf names the file whose headings answer a fragment. A link can resolve to
// a directory, since that is the first candidate and a directory exists, and
// both renderers answer one with the page inside it: GitHub draws the README
// below the listing, Starlight routes to the index. Without this the fragment
// on such a link was dropped for landing on something that is not Markdown.
//
// The page is looked for inside the directory rather than by letting the
// candidate list run on, because the next candidates are "<dir>.md" and
// "<dir>.mdx", siblings of the directory rather than anything a reader of that
// link would be shown.
function pageOf(resolvedPath) {
	if (!isDirectory(resolvedPath)) {
		return resolvedPath;
	}
	const inside = ["README.md", "index.md", "index.mdx"].map((name) =>
		path.join(resolvedPath, name),
	);
	return inside.find((candidate) => existsSync(candidate)) ?? resolvedPath;
}

function isDirectory(candidate) {
	return (
		statSync(candidate, { throwIfNoEntry: false })?.isDirectory() ?? false
	);
}

// checkFragment holds the other half of a link, the half that used to be
// dropped: the anchor. A link to a heading that was renamed, or that never
// existed, resolved its file and passed.
//
// Only a Markdown target is judged. A fragment on anything else is a line
// reference into source a renderer numbers for itself, and nothing here can
// read it.
function checkFragment(file, line, target, resolvedPath, fragment) {
	if (fragment === "" || !/\.mdx?$/i.test(resolvedPath)) {
		return;
	}

	const anchor = safeDecodeURIComponent(fragment);
	anchorsChecked++;

	if (anchorsFor(resolvedPath).has(anchor)) {
		return;
	}

	issues.push({
		file,
		line,
		target,
		reason:
			`no ${dialectFor(resolvedPath).name} anchor #${anchor} ` +
			`in ${path.relative(repoRoot, resolvedPath)}`,
	});
}

function normalizeTarget(rawTarget) {
	return rawTarget.trim().replace(/^<|>$/g, "");
}

// shouldSkipTarget names what this checker cannot answer for. A scheme is
// somebody else's server.
//
// A "#anchor" is deliberately absent from this list: it is the one shape that
// is always answerable, since the file it points into is the file it is
// written in. So is a root-relative path, which checkSiteRoute resolves when it
// names a page of the documentation site.
function shouldSkipTarget(target) {
	if (target === "") {
		return true;
	}
	if (/^[a-z][a-z0-9+.-]*:/i.test(target) || target.startsWith("//")) {
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

function dialectFor(absolutePath) {
	return absolutePath.startsWith(siteContentRoot)
		? starlightDialect
		: gitHubDialect;
}

function anchorsFor(absolutePath) {
	let anchors = anchorCache.get(absolutePath);
	if (!anchors) {
		anchors = collectAnchors(absolutePath);
		anchorCache.set(absolutePath, anchors);
	}
	return anchors;
}

function collectAnchors(absolutePath) {
	const dialect = dialectFor(absolutePath);
	const anchors = new Set(dialect.pageAnchors);
	const slug = dialect.newSlugger();
	const content = withoutFrontmatter(readFileSync(absolutePath, "utf8"));
	// The line before the current one, when it could be the text of a heading
	// underlined in the Setext style.
	let underlinable = null;

	eachProseLine(content, (line) => {
		for (const id of explicitIds(line)) {
			anchors.add(id);
		}

		const atx = line.match(/^ {0,3}#{1,6}(?:[ \t]+(.*?))?[ \t]*$/);
		if (atx) {
			const { text, id } = headingIdAttribute(atx[1] ?? "");
			anchors.add(id ?? slug(headingText(text)));
			underlinable = null;
			return;
		}

		const underline = line.match(/^ {0,3}(=+|-+)[ \t]*$/);
		if (
			underline &&
			underlinable !== null &&
			// A row of dashes under a line carrying pipes is a table, not a
			// heading; a row of equals signs is never anything else.
			(underline[1][0] === "=" || !underlinable.includes("|"))
		) {
			anchors.add(slug(headingText(underlinable)));
			underlinable = null;
			return;
		}

		underlinable = isParagraphLine(line) ? line.trim() : null;
	});

	return anchors;
}

// headingIdAttribute separates a trailing "{#custom-id}" from a heading. No
// renderer this repository uses honours the syntax today, so nothing here is
// written with it; it is read so that the day a remark plugin adds it, a link
// to the id it declares is not reported as broken, and the heading is not
// slugged with the attribute's text in it.
function headingIdAttribute(raw) {
	const match = raw.match(/^(.*?)[ \t]*\{#([^}\s]+)\}[ \t]*$/);
	return match ? { text: match[1], id: match[2] } : { text: raw, id: null };
}

// withoutFrontmatter drops the YAML block a page opens with. It carries the
// title Starlight renders the <h1> from, and GitHub draws it as a table: a
// heading in neither, and a "title: x" line above the closing "---" that would
// otherwise read as a Setext heading in both.
function withoutFrontmatter(content) {
	const frontmatter = content.match(
		/^---[ \t]*\r?\n[\s\S]*?\r?\n(?:---|\.\.\.)[ \t]*(?:\r?\n|$)/,
	);
	return frontmatter ? content.slice(frontmatter[0].length) : content;
}

// headingText renders a heading's source the way a reader sees it, and only as
// far as the slug can tell the difference. The characters a slugger keeps are
// letters, digits, underscores, hyphens and spaces, so a construct matters here
// exactly when dropping its markup changes one of those: a link's URL, an
// image's URL, a reference label, a tag name and the text of a comment all
// disappear on the page and would otherwise be slugged. Backticks, asterisks
// and tildes need no handling at all, since the slugger drops them wherever
// they stand.
function headingText(raw) {
	const withoutComments = stripRepeatedly(
		raw.replace(/[ \t]+#+[ \t]*$/, ""),
		/<!--[\s\S]*?-->/g,
	);
	const withoutLinks = withoutComments
		.replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1")
		.replace(/\[([^\]]*)\]\((?:[^()]|\([^()]*\))*\)/g, "$1")
		.replace(/\[([^\]]*)\]\[[^\]]*\]/g, "$1");
	return (
		stripRepeatedly(
			withoutLinks,
			/<\/?[A-Za-z][A-Za-z0-9-]*(?:\s[^<>]*)?\/?>/g,
		)
			// Underscores are kept by both sluggers, so emphasis written with
			// them has to go while the ones inside a name like
			// audit_md_escaping stay. Only a pair on a word boundary is
			// emphasis.
			.replace(
				/(?<![\p{L}\p{N}_])__?([^_]+?)__?(?![\p{L}\p{N}_])/gu,
				"$1",
			)
			.trim()
	);
}

// stripRepeatedly removes every match and then looks again, because one pass
// over nested markup leaves a piece of the outer construct behind: "<<b>i>"
// loses the "<b>" and leaves "<i>", and "<!--<!-- -->-->" leaves "-->". What
// is left would go on to be slugged, and a heading whose anchor nobody can
// guess is exactly the failure this file is meant to report rather than cause.
// It is also the single-pass shape CodeQL names as incomplete sanitization.
function stripRepeatedly(text, pattern) {
	let previous;
	let stripped = text;
	do {
		previous = stripped;
		stripped = stripped.replace(pattern, "");
	} while (stripped !== previous);
	return stripped;
}

// explicitIds collects the anchors a page states rather than derives. Both
// renderers honour one: Astro keeps an id a heading or component already
// carries instead of slugging it, and GitHub resolves a fragment to the
// "<a id>" or "<a name>" it prefixed.
function explicitIds(line) {
	const ids = [];
	const pattern =
		/<[A-Za-z][^<>]*?\s(?:id|name)\s*=\s*["']([^"']+)["'][^<>]*>/g;
	let match;
	while ((match = pattern.exec(line)) !== null) {
		ids.push(match[1]);
	}
	return ids;
}

// isParagraphLine answers whether a line could be the text of a Setext
// heading, which is to say an ordinary paragraph: not a blank, not indented
// code, and not the opening of a block that owns its line. A bullet with
// nothing after it is one of those blocks, which is how the two empty list
// items of .github/pull_request_template.md would read as a heading underlined
// by the second.
function isParagraphLine(line) {
	if (!/\S/.test(line) || /^ {4,}/.test(line)) {
		return false;
	}
	return !/^ {0,3}(?:[>#|<]|[-*+](?:[ \t]|$)|\d+[.)](?:[ \t]|$))/.test(line);
}
