/**
 * The current release, written into the pages at build time.
 *
 * A page that shows how to pin the current release — `npx -y
 * @jmrp.io/libgen-mcp@…`, `ghcr.io/jmrplens/libgen-mcp:…`, a `releases/download/v…/`
 * URL, the output of `--version` — goes stale on the day of the next release
 * if the number is written into it. So such a page writes a token instead, and
 * the build replaces it: in the HTML (remarkReleaseVersion, below), in each
 * page's Markdown copy and in llms-docs.txt (page-markdown.mjs), and
 * scripts/check-version-token.mjs fails the build if one survives anywhere in
 * dist. There are three:
 *
 * - `%%VERSION%%`, the repository's VERSION file.
 * - `%%RELEASE_YEAR%%` and `%%RELEASE_MONTH%%` (BibTeX's `jan`…`dec`), the
 *   `date-released` of CITATION.cff, for the citation sample. The release
 *   stamper writes that date at tag time, so between a VERSION bump and the
 *   stamp they still give the previous release's date.
 *
 * A version that is a fact about the past — "up to 2.1.0", a changelog section,
 * a measured build — is never a token. Only "the current release" is.
 *
 * The tokens are `%%…%%` because that is inert everywhere a page can hold it:
 * MDX reads `{` as an expression and `<` as JSX, Markdown reads `__x__` and
 * `*x*` as emphasis and `[x]` as a link, and prettier rewrites none of
 * `%`-delimited text, inside or outside code.
 *
 * docs/*.md is read raw on GitHub and cannot be built, so it keeps the values
 * literally; `make gen-doc-versions` rewrites those spots, finding them at the
 * positions the tokens hold in the English twin.
 */
import { readFileSync } from "node:fs";

/** The placeholder a page writes where the current release belongs. */
export const VERSION_TOKEN = "%%VERSION%%";

/** The placeholder for the year the current release was published. */
export const YEAR_TOKEN = "%%RELEASE_YEAR%%";

/** The placeholder for its month, as BibTeX abbreviates it. */
export const MONTH_TOKEN = "%%RELEASE_MONTH%%";

/** Every token, for the check that none reaches the output. */
export const RELEASE_TOKENS = [VERSION_TOKEN, YEAR_TOKEN, MONTH_TOKEN];

/** BibTeX's month macros, which GitHub's CITATION.cff converter writes. */
const MONTHS = [
	"jan",
	"feb",
	"mar",
	"apr",
	"may",
	"jun",
	"jul",
	"aug",
	"sep",
	"oct",
	"nov",
	"dec",
];

/**
 * What the VERSION file must hold: a release number, nothing around it. The
 * prerelease suffix is non-empty fields joined by `.` or `-`, the same shape
 * cmd/gen_doc_versions accepts, so `2.1.0-..` is refused by both.
 */
const RELEASE = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$/;

/** The `date-released:` line of CITATION.cff, quoted or not. */
const DATE_RELEASED =
	/^date-released:\s*["']?(\d{4})-(\d{2})-(\d{2})["']?\s*$/m;

/**
 * Reads a file, or stops the build naming it.
 *
 * @param {URL|string} file
 * @returns {string}
 */
function readOrStop(file) {
	try {
		return readFileSync(file, "utf8");
	} catch (error) {
		throw new Error(
			`[release-version] cannot read ${String(file)}: ${error.message}. ` +
				`The pages name the current release from it, so the build stops here.`,
			{ cause: error },
		);
	}
}

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
	const version = readOrStop(file).trim();
	if (!RELEASE.test(version)) {
		throw new Error(
			`[release-version] ${String(file)} holds ${JSON.stringify(version)}, ` +
				`which is not a release number.`,
		);
	}
	return version;
}

/**
 * Reads `date-released` from CITATION.cff, with the same strictness: a file
 * without a parseable date stops the build.
 *
 * @param {URL|string} [file] - The citation file. Defaults to the repository's.
 * @returns {{year: string, month: string}} e.g. `{year: "2026", month: "oct"}`.
 */
export function readReleaseDate(
	file = new URL("../../../CITATION.cff", import.meta.url),
) {
	const match = DATE_RELEASED.exec(readOrStop(file));
	// A date that rolls over (2026-02-30 is 2026-03-02 to Date.UTC) is not
	// the date that was written, so the round trip must give back all three.
	const [year, month, day] = match ? match.slice(1).map(Number) : [];
	const parsed = match ? new Date(Date.UTC(year, month - 1, day)) : undefined;
	if (
		!parsed ||
		parsed.getUTCFullYear() !== year ||
		parsed.getUTCMonth() !== month - 1 ||
		parsed.getUTCDate() !== day
	) {
		throw new Error(
			`[release-version] ${String(file)} has no date-released that is a real YYYY-MM-DD date.`,
		);
	}
	return { year: match[1], month: MONTHS[month - 1] };
}

/**
 * Every token's value, read from the repository root.
 *
 * @param {URL|string} root - The repository root, as a directory URL or path.
 * @returns {Record<string, string>} Token to value.
 */
export function readReleaseValues(root) {
	const at = (name) =>
		root instanceof URL ? new URL(name, root) : `${root}/${name}`;
	const version = readReleaseVersion(at("VERSION"));
	const { year, month } = readReleaseDate(at("CITATION.cff"));
	return { [VERSION_TOKEN]: version, [YEAR_TOKEN]: year, [MONTH_TOKEN]: month };
}

/**
 * Whether a string holds any release token.
 *
 * @param {string} text
 * @returns {boolean}
 */
export function hasReleaseToken(text) {
	return RELEASE_TOKENS.some((token) => text.includes(token));
}

/**
 * Replaces every release token in a string.
 *
 * @param {string} text
 * @param {Record<string, string>} values - Token to value, from readReleaseValues.
 * @returns {string}
 */
export function substituteRelease(text, values) {
	let out = text;
	for (const [token, value] of Object.entries(values)) {
		out = out.replaceAll(token, value);
	}
	return out;
}

/** The string-valued node fields a token can sit in. */
const FIELDS = ["value", "url", "title", "alt", "meta"];

/**
 * Replaces the tokens everywhere in an mdast tree: text, inline code, fenced
 * code (and its meta, where an Expressive Code title lives), raw HTML, link and
 * image URLs and titles, image alt text, and the string attributes of MDX
 * elements and of directives.
 *
 * It is a remark plugin on purpose. Expressive Code renders fenced code in a
 * rehype plugin, so a token replaced here is already the value when the code
 * block is highlighted, and a link URL is replaced before mdast-util-to-hast
 * percent-encodes the `%`.
 *
 * @param {{values: Record<string, string>}} options - From readReleaseValues.
 * @returns {(tree: object) => void}
 */
export function remarkReleaseVersion({ values }) {
	const replace = (value) =>
		typeof value === "string" && hasReleaseToken(value)
			? substituteRelease(value, values)
			: value;
	const visit = (node) => {
		for (const field of FIELDS) {
			if (field in node) node[field] = replace(node[field]);
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
				holder[key] = replace(value);
			}
		}
		for (const child of Array.isArray(node.children) ? node.children : []) {
			visit(child);
		}
	};
	return (tree) => visit(tree);
}
