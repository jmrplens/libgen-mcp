/**
 * The three inputs the Markdown renderer needs, loaded once.
 *
 * Shared by the postbuild emitter and the dev-server integration so the copy a
 * reader gets from a running site and the copy shipped in dist are produced from
 * exactly the same data — including the download chain, which is parsed rather
 * than imported because a .mjs script cannot load a .ts module.
 */
import { readFileSync } from "node:fs";
import { join } from "node:path";

/**
 * @param {string} root - The site directory.
 * @returns {{labels: Record<string, Record<string,string>>, schema: object, chain: string[]}}
 */
export function loadInputs(root) {
	return {
		labels: {
			en: JSON.parse(
				readFileSync(join(root, "src/content/i18n/en.json"), "utf8"),
			),
			es: JSON.parse(
				readFileSync(join(root, "src/content/i18n/es.json"), "utf8"),
			),
		},
		schema: JSON.parse(
			readFileSync(join(root, "src/data/tool-schema.json"), "utf8"),
		),
		chain: downloadChain(root),
	};
}

/**
 * The download chain's source ids, in order, read from src/data/sources.ts.
 *
 * The parse is strict on purpose: it asserts the shape it expects and the count
 * the file declares, so a refactor that changes either aborts rather than
 * quietly emitting a shorter chain into every Markdown copy.
 *
 * @param {string} root - The site directory.
 * @returns {string[]}
 */
export function downloadChain(root) {
	const ts = readFileSync(join(root, "src/data/sources.ts"), "utf8");
	const array = /export const SOURCES: Source\[\] = \[([\s\S]*?)\n\];/.exec(ts);
	if (!array) {
		throw new Error(
			"[page-markdown] cannot find SOURCES in src/data/sources.ts",
		);
	}
	const ids = [...array[1].matchAll(/\bid:\s*"([a-z0-9_]+)"/g)].map(
		(m) => m[1],
	);
	const declared = (ts.match(/\bid:\s*"/g) ?? []).length;
	if (ids.length === 0 || ids.length !== declared) {
		throw new Error(
			`[page-markdown] parsed ${ids.length} source ids but the file declares ${declared}`,
		);
	}
	return ids;
}
