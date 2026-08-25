// Renders an authored .mdx page back to plain Markdown.
//
// The page actions offer every documentation page as Markdown, for readers who
// want to paste one into an assistant. The obvious source for that is the .mdx
// file itself — except that this site's pages are no longer plain Markdown:
// a source's declarations are <Facts>/<Fact> elements, and handing an LLM raw
// JSX is worse than handing it the bullets those elements replaced.
//
// So the component vocabulary is rendered back. That is only safe because the
// vocabulary is small, closed and ours: six element names, listed in
// COMPONENTS below and asserted by the caller. Anything left over is an error
// rather than a passthrough — a page that starts using a seventh component must
// teach this file about it, not silently ship JSX to a reader.
//
// The labels come from the same i18n JSON the Fact component reads, so the
// Markdown copy says "Qué no cubre" on a Spanish page for the same reason the
// page does.

/** Element names this renderer knows how to unwrap. */
export const COMPONENTS = [
	"Facts",
	"Fact",
	"Aside",
	"Tabs",
	"TabItem",
	"LinkCard",
	"Home",
];

/** Reads a JSX-ish attribute off an opening tag. */
const attr = (tag, name) => {
	const m = new RegExp(`${name}="([^"]*)"`).exec(tag);
	return m ? m[1].replace(/&quot;/g, '"') : undefined;
};

const unescape = (text) =>
	text
		.replace(/&#123;/g, "{")
		.replace(/&#125;/g, "}")
		.replace(/&quot;/g, '"');

/**
 * @param {string} source - The .mdx file's full text.
 * @param {Record<string,string>} labels - The locale's `lgm.*` strings.
 * @param {object} [schema] - The generated tool-schema.json, for <SchemaTable>.
 * @returns {string} Markdown.
 */
export function toMarkdown(source, labels, schema) {
	let text = source;

	// Frontmatter and the import block are machinery, not content.
	text = text.replace(/^---\n[\s\S]*?\n---\n/, "");
	text = text.replace(/^import .*$/gm, "");

	// <Home /> renders the landing from src/data/home.ts. There is no Markdown
	// equivalent of a component-built page, and the rest of the landing (the
	// disambiguation table, the notice, the FAQ) is ordinary Markdown that
	// survives on its own.
	text = text.replace(/<Home[^>]*\/>/g, "");

	// A spec sheet becomes the labelled list it replaced: the label in bold,
	// then the prose, which is already Markdown.
	text = text.replace(/<\/?Facts[^>]*>/g, "");
	text = text.replace(
		/^[ \t]*<Fact\s+(kind|label)="([^"]*)"\s*>([\s\S]*?)<\/Fact>/gm,
		(_, key, value, body) => {
			const label =
				key === "kind"
					? (labels[`lgm.fact.${value}`] ?? value)
					: unescape(value);
			// The body may have been wrapped across lines by prettier; a bullet
			// wants one paragraph per line, indented so the list holds together.
			const paragraphs = body
				.trim()
				.split(/\n\s*\n/)
				.map((p) => p.replace(/\s*\n\s*/g, " ").trim())
				.filter(Boolean)
				.map(unescape);
			const [first, ...rest] = paragraphs;
			return (
				`- **${label}** ${first ?? ""}` + rest.map((p) => `\n\n  ${p}`).join("")
			);
		},
	);

	// <LegalNotice /> prints one string from the i18n collection. It is expanded
	// before the Aside pass below so it arrives as an ordinary caution alert,
	// which means the Markdown copy carries the notice rather than dropping it —
	// the one piece of text on these pages that must not go missing.
	text = text.replace(
		/<LegalNotice\s*\/>/g,
		() => `<Aside type="caution">${labels["lgm.legal.notice"] ?? ""}</Aside>`,
	);

	// A <SchemaTable> becomes the table it replaced on the page. The rendered
	// page uses a description list because these descriptions run to 500
	// characters and a four-column table crushes them; a Markdown copy has no
	// layout to crush, is read by machines as often as by people, and a table is
	// the densest way to state four facts per row. Same data, and the type and
	// required columns still come from the generated schema rather than from
	// anything written here.
	text = text.replace(
		/<SchemaTable([^>]*)>([\s\S]*?)<\/SchemaTable>/g,
		(whole, tag, body) => {
			const name = attr(tag, "name");
			const section = attr(tag, "section");
			// The column headings are the locale's, like every other string the
			// components print: a Spanish page's Markdown copy should not be
			// headed in English.
			const col = (key, fallback) => labels[`lgm.schema.${key}`] ?? fallback;
			const rows =
				section === "arguments"
					? schema?.prompts?.[name]?.arguments
					: schema?.tools?.[name]?.[section];
			if (!rows) return whole;

			const described = new Map(
				[
					...body.matchAll(/<Fragment slot="([^"]+)">([\s\S]*?)<\/Fragment>/g),
				].map((m) => [m[1], m[2].replace(/\s*\n\s*/g, " ").trim()]),
			);
			const head =
				`| ${col("name", "Name")} | ${col("type", "Type")} | ` +
				`${col("required", "Required")} | ${col("description", "Description")} |\n` +
				"| --- | --- | --- | --- |";
			const lines = rows.map((row) => {
				const cell = unescape(described.get(row.name) ?? "").replace(
					/\|/g,
					"\\|",
				);
				return `| \`${row.name}\` | ${row.type} | ${row.required ? "yes" : "no"} | ${cell} |`;
			});
			return [head, ...lines].join("\n");
		},
	);

	// Asides become GitHub-style alerts, which every Markdown reader that
	// matters renders and every LLM understands as emphasis.
	text = text.replace(/<Aside([^>]*)>([\s\S]*?)<\/Aside>/g, (_, tag, body) => {
		const type = (attr(tag, "type") ?? "note").toUpperCase();
		const title = attr(tag, "title");
		const lines = body
			.trim()
			.split("\n")
			.map((l) => `> ${l.trim()}`.trimEnd());
		return `> [!${type}]${title ? `\n> **${title}**\n>` : ""}\n${lines.join("\n")}`;
	});

	// A tab set has no Markdown equivalent, so each tab becomes a labelled
	// section. Dropping the labels would silently merge alternatives that are
	// meant to be read one instead of the other.
	text = text.replace(/<\/?Tabs[^>]*>/g, "");
	text = text.replace(
		/<TabItem([^>]*)>([\s\S]*?)<\/TabItem>/g,
		(_, tag, body) => `#### ${attr(tag, "label") ?? ""}\n${body.trim()}\n`,
	);

	text = text.replace(/<LinkCard([^>]*)\/>/g, (_, tag) => {
		const title = attr(tag, "title") ?? "";
		const href = attr(tag, "href") ?? "";
		const description = attr(tag, "description");
		return `- [${title}](${href})${description ? ` — ${description}` : ""}`;
	});

	return `${unescape(text)
		.replace(/\n{3,}/g, "\n\n")
		.trim()}\n`;
}

/**
 * Any JSX-looking element left after rendering. Should always be empty.
 *
 * Code is excluded before scanning. A capital-letter tag inside a fence or a
 * code span is content — `<PID>` is a placeholder in a shell command, and
 * flagging it would make the check cry wolf on a page that renders perfectly.
 */
export function residualComponents(markdown) {
	const prose = markdown
		.replace(/```[\s\S]*?```/g, "")
		.replace(/`[^`\n]*`/g, "");
	return [
		...new Set([...prose.matchAll(/<\/?([A-Z][A-Za-z]*)/g)].map((m) => m[1])),
	];
}
