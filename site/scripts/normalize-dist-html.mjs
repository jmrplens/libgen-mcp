// Normalizes the built HTML in dist/ before the linters see it.
//
// Four passes, all of which fix output no source edit can reach. The fourth,
// putting a style's rules back inside the element an MDX compile emptied, is
// documented at restoreCollapsedContent below.
//
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

/** Decodes the character references hast-util-to-html writes into an attribute. */
function decodeAttribute(value) {
	const named = { amp: "&", quot: '"', apos: "'", lt: "<", gt: ">" };
	return value.replace(
		/&(#x[0-9a-f]+|#\d+|amp|quot|apos|lt|gt);/gi,
		(whole, ref) => {
			if (ref[0] !== "#") return named[ref.toLowerCase()] ?? whole;
			const code =
				ref[1] === "x" || ref[1] === "X"
					? Number.parseInt(ref.slice(2), 16)
					: Number.parseInt(ref.slice(1), 10);
			return String.fromCodePoint(code);
		},
	);
}

/**
 * Puts a style or script back inside its element when an MDX compile left its
 * contents in a `set:html` attribute.
 *
 * Two plugins in @astrojs/markdown-remark 7.3 disagree about these elements.
 * rehypeCollapseScriptStyle moves a style's contents into a `set:html`
 * property, and rehypeOptimizeStatic, which Starlight enables for MDX, then
 * serializes the static subtree around it with toHtml, printing the property as
 * an ordinary attribute: `<style set:html="…"></style>`, an empty style element
 * with its rules parked where nothing reads them. Every MDX page shipped
 * Expressive Code's and Mermaid's styles that way, so code blocks lost their
 * frame, diagram edges drew as black wedges, and pages scrolled sideways on a
 * phone.
 *
 * It is repaired here because the source cannot reach it. Taking style and
 * script out of the optimization (mdx's `ignoreElementNames`) does move them
 * back inside their elements, and in doing so compiles their SVG siblings as
 * JSX, whose attributes Astro prints in camelCase: every Mermaid edge lost its
 * arrowhead to `markerEnd`, an attribute no browser reads. So the one element
 * is mended after the fact, and scripts/check-dist-html.mjs fails the build if
 * a directive of any kind still reaches dist.
 */
function restoreCollapsedContent(html) {
	return html.replace(
		/<(style|script)(\s[^>]*?)?\sset:html="([^"]*)"([^>]*)>\s*<\/\1>/g,
		(_, tag, before = "", value, after) =>
			`<${tag}${before}${after}>${decodeAttribute(value)}</${tag}>`,
	);
}

function normalize(html) {
	return decodeFragments(restoreCollapsedContent(html))
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
