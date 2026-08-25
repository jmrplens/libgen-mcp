// Normalizes the built HTML in dist/ before the linters see it.
//
// Three passes, all of which fix output no source edit can reach:
//
//   1. Trailing whitespace on any line. Cosmetic, but it made real diffs of the
//      built output unreadable.
//   2. `<br></br>` collapsed to `<br>`. Mermaid writes node labels as HTML inside
//      <foreignObject>, and a `<br/>` in a diagram source comes back through
//      rehype-mermaid's serializer as a start tag plus a bogus end tag. `br` is a
//      void element, so `</br>` is invalid — htmlhint's tag-pair rule is right to
//      reject it, and the fix belongs here because the markup is generated.
//   3. Same-page fragments decoded to match the heading id they point at. A
//      Spanish heading slugs to a raw UTF-8 id — `id="cómo-se-nombra-el-archivo-
//      guardado"` — while the markdown pipeline percent-encodes the same text
//      once it becomes a URL, so the document contradicts itself: the link works
//      (a browser decodes before matching) and every checker comparing the two
//      strings reports an anchor that does not exist. es/tools.mdx carried three
//      and was the one page failing the accessibility audit. An audit that cries
//      wolf is an audit nobody reads, so the markup is made self-consistent
//      rather than the checker taught to ignore it.
//
//      It happens here rather than in a rehype plugin because heading ids do not
//      exist yet when a user rehype plugin runs — measured: the tree carries the
//      links and zero ids. By this stage both are final.
//
// Usage: node scripts/normalize-dist-html.mjs
import { readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const distDir = join(dirname(fileURLToPath(import.meta.url)), "..", "dist");

function htmlFiles(dir) {
	const out = [];
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const p = join(dir, entry.name);
		if (entry.isDirectory()) out.push(...htmlFiles(p));
		else if (entry.isFile() && entry.name.endsWith(".html")) out.push(p);
	}
	return out;
}

/**
 * Decodes a same-page fragment when, and only when, the decoded form matches an
 * id this document actually has and the encoded form does not.
 *
 * Deliberately conservative: an id that genuinely contains a percent-encoded
 * character keeps its link untouched, and a link to a missing anchor stays
 * missing rather than being rewritten into something that happens to resolve.
 */
function decodeFragments(html) {
	const ids = new Set([...html.matchAll(/\sid="([^"]+)"/g)].map((m) => m[1]));
	return html.replace(/href="#([^"]+)"/g, (whole, fragment) => {
		if (ids.has(fragment)) return whole;
		let decoded;
		try {
			decoded = decodeURIComponent(fragment);
		} catch {
			return whole;
		}
		return decoded !== fragment && ids.has(decoded)
			? `href="#${decoded}"`
			: whole;
	});
}

function normalize(html) {
	return decodeFragments(html)
		.split("\n")
		.map((line) => line.replace(/[ \t\r]+$/, ""))
		.join("\n")
		.replaceAll("<br></br>", "<br>");
}

try {
	statSync(distDir);
} catch {
	console.warn("[normalize-dist-html] dist/ not found — skipping");
	process.exit(0);
}

let changed = 0;
for (const file of htmlFiles(distDir)) {
	const before = readFileSync(file, "utf8");
	const after = normalize(before);
	if (after !== before) {
		writeFileSync(file, after);
		changed++;
	}
}
console.log(`[normalize-dist-html] normalized ${changed} file(s)`);
