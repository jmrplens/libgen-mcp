// Postbuild: writes dist/llms-docs.txt, every English documentation page as
// Markdown in one file, in the order llms.txt links them.
//
// llms-full.txt is the tool and configuration reference cmd/gen_llms writes from
// the live surface. The prose a reader would quote — how the download chain is
// ordered, what each source covers, what the server refuses — was in no
// machine-oriented file at all, so anything that ingests the llms files as the
// whole corpus never saw it. This is that file.
//
// It is assembled from the per-page Markdown emit-page-markdown.mjs has just
// written, so it must run after it, and it takes its page list from llms.txt
// rather than keeping a third one: cmd/gen_llms owns the list, and
// TestDocPagesCoverEverySitePage holds it to the pages the site publishes.
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const distDir = fileURLToPath(new URL("../dist", import.meta.url));
const OUTPUT = "llms-docs.txt";

function fail(message) {
	console.error(`[llms-docs] FAILED: ${message}`);
	process.exit(1);
}

const llms = readFileSync(join(distDir, "llms.txt"), "utf8");

// The address llms.txt gives the site. Every page link starts with it, and the
// site's own root-relative links are rewritten onto it, so a link in the corpus
// resolves to the same place as the page's entry in llms.txt.
const base = llms.match(
	/^- \[Documentation site\]\((https:\/\/[^)]+\/)\)/m,
)?.[1];
if (!base) fail("llms.txt does not link the documentation site");

const section = llms
	.split(/^## /m)
	.find((part) => part.startsWith("Documentation\n"));
if (!section) fail("llms.txt has no `## Documentation` section");

const pages = [...section.matchAll(/^- \[([^\]]+)\]\(([^)]+)\): .*$/gm)]
	.map(([, title, url]) => ({ title, url }))
	.filter(({ url }) => url.startsWith(base));
if (pages.length === 0) fail("llms.txt links no page of the site");

// The site is served under /libgen-mcp/ and every page links its siblings from
// there. Anchors are rewritten too: `#search` means one heading on its own page
// and any of several in a file holding every page.
function absolutize(markdown, pageURL) {
	return markdown
		.replaceAll("](/libgen-mcp/", `](${base}`)
		.replaceAll("](#", `](${pageURL}#`);
}

const missing = [];
const parts = [
	"# libgen-mcp documentation",
	"",
	"> Every English page of the libgen-mcp documentation site as Markdown, in reading order. " +
		"The tool schemas and the full configuration table are in llms-full.txt, " +
		"and each page is also served on its own as `index.md` beside its HTML.",
	"",
	`Generated from the site's sources when it is built. The page list is the one llms.txt links: ${base}llms.txt`,
];

for (const { title, url } of pages) {
	const slug = url.slice(base.length);
	const source = join(distDir, slug, "index.md");
	if (!existsSync(source)) {
		missing.push(`${url} (no ${join(slug, "index.md")} in dist)`);
		continue;
	}
	const body = absolutize(readFileSync(source, "utf8").trim(), url);
	parts.push("", "---", "", `# ${title}`, "", `Source: ${url}`, "", body);
}

if (missing.length) {
	fail(
		`llms.txt links pages the build did not emit as Markdown:\n  ${missing.join("\n  ")}`,
	);
}

writeFileSync(join(distDir, OUTPUT), `${parts.join("\n")}\n`);
console.log(`[llms-docs] wrote ${OUTPUT} from ${pages.length} pages`);
