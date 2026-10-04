/**
 * The current release, written into the pages at build time.
 *
 * A page that shows how to pin the current release — `npx -y
 * @jmrp.io/libgen-mcp@…`, `ghcr.io/jmrplens/libgen-mcp:…`, a `releases/download/v…/`
 * URL, the output of `--version` — goes stale on the day of the next release
 * if the number is written into it. So such a page writes VERSION_TOKEN instead,
 * and the build replaces it with the repository's VERSION file: in the HTML
 * (remarkReleaseVersion, below), in each page's Markdown copy and in
 * llms-docs.txt (page-markdown.mjs), and scripts/check-version-token.mjs fails
 * the build if one survives anywhere in dist.
 *
 * A version that is a fact about the past — "up to 2.1.0", a changelog section,
 * a measured build — is never the token. Only "the current release" is.
 *
 * The token is `%%VERSION%%` because it is inert everywhere a page can hold it:
 * MDX reads `{` as an expression and `<` as JSX, Markdown reads `__x__` and
 * `*x*` as emphasis and `[x]` as a link, and prettier rewrites none of
 * `%`-delimited text, inside or outside code.
 *
 * docs/*.md is read raw on GitHub and cannot be built, so it keeps the number
 * literally; `make gen-doc-versions` rewrites those spots from VERSION, finding
 * them at the positions this token holds in the English twin.
 */
import { readFileSync } from "node:fs";

/** The placeholder a page writes where the current release belongs. */
export const VERSION_TOKEN = "%%VERSION%%";

/** What the VERSION file must hold: a release number, nothing around it. */
const RELEASE = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/;

/**
 * Reads the repository's VERSION file. Throws, rather than falling back, when
 * the file is missing or holds anything but a release number: a page that
 * shipped the token, or an empty string where the version goes, is a page that
 * tells a reader to install nothing.
 *
 * @param {URL|string} [file] - The VERSION file. Defaults to the repository's.
 * @returns {string} The trimmed release number, e.g. `2.2.0`.
 */
export function readReleaseVersion(
	file = new URL("../../../VERSION", import.meta.url),
) {
	let raw;
	try {
		raw = readFileSync(file, "utf8");
	} catch (error) {
		throw new Error(
			`[release-version] cannot read ${String(file)}: ${error.message}. ` +
				`The pages name the current release from it, so the build stops here.`,
			{ cause: error },
		);
	}
	const version = raw.trim();
	if (!RELEASE.test(version)) {
		throw new Error(
			`[release-version] ${String(file)} holds ${JSON.stringify(version)}, ` +
				`which is not a release number.`,
		);
	}
	return version;
}

/**
 * Replaces every VERSION_TOKEN in a string.
 *
 * @param {string} text
 * @param {string} version
 * @returns {string}
 */
export function substituteVersion(text, version) {
	return text.replaceAll(VERSION_TOKEN, version);
}

/** The string-valued node fields a token can sit in. */
const FIELDS = ["value", "url", "title", "alt", "meta"];

/**
 * Replaces the token everywhere in an mdast tree: text, inline code, fenced code
 * (and its meta, where an Expressive Code title lives), raw HTML, link and image
 * URLs and titles, image alt text, and the string attributes of MDX elements and
 * of directives.
 *
 * It is a remark plugin on purpose. Expressive Code renders fenced code in a
 * rehype plugin, so a token replaced here is already the number when the code
 * block is highlighted, and a link URL is replaced before mdast-util-to-hast
 * percent-encodes the `%`.
 *
 * @param {{version?: string}} [options] - The release to write. Defaults to the
 *   VERSION file.
 * @returns {(tree: object) => void}
 */
export function remarkReleaseVersion(options = {}) {
	const version = options.version ?? readReleaseVersion();
	const visit = (node) => {
		for (const field of FIELDS) {
			if (
				typeof node[field] === "string" &&
				node[field].includes(VERSION_TOKEN)
			) {
				node[field] = substituteVersion(node[field], version);
			}
		}
		// An MDX element keeps its attributes as an array of {name, value}; a
		// remark-directive node (Starlight's `:::note` asides) as a plain object.
		const attributes = Array.isArray(node.attributes)
			? node.attributes
			: node.attributes && typeof node.attributes === "object"
				? [node.attributes]
				: [];
		for (const holder of attributes) {
			for (const [key, value] of Object.entries(holder)) {
				if (typeof value === "string" && value.includes(VERSION_TOKEN)) {
					holder[key] = substituteVersion(value, version);
				}
			}
		}
		for (const child of Array.isArray(node.children) ? node.children : []) {
			visit(child);
		}
	};
	return (tree) => visit(tree);
}
