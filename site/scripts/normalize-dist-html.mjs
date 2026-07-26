// Normalizes the built HTML in dist/ before the linters see it.
//
// Two passes, both of which fix output no source edit can reach:
//
//   1. Trailing whitespace on any line. Cosmetic, but it made real diffs of the
//      built output unreadable.
//   2. `<br></br>` collapsed to `<br>`. Mermaid writes node labels as HTML inside
//      <foreignObject>, and a `<br/>` in a diagram source comes back through
//      rehype-mermaid's serializer as a start tag plus a bogus end tag. `br` is a
//      void element, so `</br>` is invalid — htmlhint's tag-pair rule is right to
//      reject it, and the fix belongs here because the markup is generated.
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

function normalize(html) {
	return html
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
