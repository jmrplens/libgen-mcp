// Fails the build when dist/ carries markup that renders wrong while every
// linter downstream passes it.
//
// Astro directives such as `set:html` are compile-time instructions. One that
// reaches the built HTML is printed as an ordinary attribute and does nothing:
// `<style set:html="…">` is an empty style element with its rules parked in an
// attribute. That is how the whole of Expressive Code's and Mermaid's CSS went
// out unapplied on every MDX page after the Astro 7.3 upgrade (see
// restoreCollapsedContent in normalize-dist-html.mjs, which runs just before
// this and mends that one shape). html-validate and htmlhint accept it, because an
// unknown attribute with a colon in its name is still well-formed HTML, and the
// page renders, only badly. So the check is made here, on the files that ship.
//
// Usage: node scripts/check-dist-html.mjs
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const distDir = join(dirname(fileURLToPath(import.meta.url)), "..", "dist");

// An Astro template directive written out as an attribute: `set:html=`,
// `set:text=`, `class:list=`, `is:inline`, `define:vars=`.
const leakedDirective =
	/<[a-z][a-z0-9-]*\s[^>]*\b(set:html|set:text|class:list|is:inline|is:raw|define:vars)\b/i;

function htmlFiles(dir) {
	const out = [];
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const p = join(dir, entry.name);
		if (entry.isDirectory()) out.push(...htmlFiles(p));
		else if (entry.isFile() && entry.name.endsWith(".html")) out.push(p);
	}
	return out;
}

const findings = [];
for (const file of htmlFiles(distDir)) {
	const html = readFileSync(file, "utf8");
	const match = leakedDirective.exec(html);
	if (match) {
		findings.push(
			`${relative(distDir, file)}: ${match[1]} reached the built HTML: ${match[0].slice(0, 80)}`,
		);
	}
}

if (findings.length > 0) {
	console.error(
		`[check-dist-html] ${findings.length} page(s) ship an Astro directive as an attribute:`,
	);
	for (const f of findings) console.error(`  ${f}`);
	process.exit(1);
}
console.log("[check-dist-html] no Astro directive reached dist");
