/**
 * Builds a page's FAQPage JSON-LD from the questions the page actually shows.
 *
 * The site states each answer twice: once as visible prose under a heading, and
 * once inside a hand-written FAQPage block in the page's frontmatter. Eighty-four
 * answers across two locales, with nothing comparing them — and two had already
 * drifted where no reader could see it. troubleshooting's "Where does libgen-mcp
 * save downloaded files?" shipped structured data 178 characters shorter than
 * the page, dropping the claim that a hosted server never writes a file at all;
 * and its JSON-LD named the download source `sci-hub`, a spelling that appears
 * nowhere else in the project.
 *
 * So the schema is derived from the prose. scripts/sync-privacy.mjs has done
 * exactly this for privacy.md since before the redesign; this module is that
 * idea generalised, and the transform below is deliberately shared so the two
 * generators cannot disagree about what an answer says.
 *
 * WHY A GENERATOR AND NOT A COMPONENT. A component rendering the questions would
 * own their headings, and a heading emitted by a component is invisible to
 * Starlight's table of contents — the same trap Facts.astro's doc comment
 * records for the source sheets. All 76 questions are live entries in "On this
 * page" today; a component would silently delete them. A generator leaves every
 * `###` in the Markdown exactly where it is.
 */

/**
 * Renders one answer's Markdown as the plain sentence the schema carries.
 *
 * @param {string} markdown - The answer's body, as authored.
 * @returns {string} One line of plain text.
 */
export function answerText(markdown) {
	return (
		markdown
			.trim()
			// A link whose text is its own URL without the scheme is really a bare
			// URL that Markdown auto-linked, and the schema should carry the URL —
			// "on GitHub at https://github.com/…" reads as written. Any other link
			// keeps its text, since the href is noise in a spoken answer.
			.replace(/\[([^\]]+)\]\(([^)]+)\)/g, (_, text, href) =>
				href.replace(/^https?:\/\//, "") === text ? href : text,
			)
			.replace(/\*\*([^*]+)\*\*/g, "$1")
			// Emphasis only, never a glob. Requiring non-space on both outer edges
			// leaves a filename like `.libgen-mcp-*.part` intact, where a blanket
			// asterisk strip would silently rewrite it.
			.replace(/(?<!\S)\*([^*\n]+)\*(?!\S)/g, "$1")
			.replace(/`/g, "")
			.replace(/\s*\n\s*/g, " ")
			.trim()
	);
}

/**
 * Builds the FAQPage node.
 *
 * @param {object} options
 * @param {{q: string, a: string}[]} options.entries
 * @param {string} options.inLanguage
 * @param {string} options.pageUrl - The page's canonical URL.
 * @param {boolean} options.withId - Whether the node carries its own `@id`.
 * @param {string} options.isPartOf - The `@id` this page is part of.
 * @param {string} [options.about] - The `@id` this page is about, when it has one.
 * @returns {object} The node, with its keys in the order the files already use.
 */
export function faqNode({
	entries,
	inLanguage,
	pageUrl,
	withId,
	isPartOf,
	about,
}) {
	return {
		"@context": "https://schema.org",
		"@type": "FAQPage",
		...(withId ? { "@id": `${pageUrl}#faq` } : {}),
		inLanguage,
		isPartOf: { "@id": isPartOf },
		...(about ? { about: { "@id": about } } : {}),
		mainEntity: entries.map(({ q, a }) => ({
			"@type": "Question",
			name: q,
			acceptedAnswer: { "@type": "Answer", text: a },
		})),
	};
}

/**
 * Serializes a node the way the frontmatter already writes it: two-space JSON,
 * except that a lone `{ "@id": … }` stays on one line. Plain JSON.stringify
 * expands those onto three lines, which would rewrite every file on the first
 * run for no reason.
 *
 * @param {object} node
 * @returns {string}
 */
export function serialize(node) {
	return JSON.stringify(node, null, 2).replace(
		/\{\n\s*("@id": "[^"]*")\n\s*\}/g,
		"{ $1 }",
	);
}
