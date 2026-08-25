/**
 * Prepares the tables that cannot fit a phone to stack instead of scroll.
 *
 * The problem is measured, not assumed. At 390px the browser's automatic layout
 * shrinks a table's text column to whatever is left over: 95px on
 * getting-started's prompt table, 98px on architecture's icon table, 231px on
 * configuration's variable table — twelve to twenty-eight characters a line,
 * which is not reading. Starlight makes a wide table a scroll container, so the
 * alternative on offer is swiping a reference table sideways.
 *
 * Neither is good, and both are avoidable, because these rows are RECORDS
 * rather than comparisons: one variable, one source, one prompt per row. A
 * record reads perfectly well stacked — label above value — which is what the
 * rest of this site already does with a source's declarations and a tool's
 * parameters. So below the `md` breakpoint tables.css turns each row into a
 * block, and this pass supplies the two things CSS cannot get on its own:
 *
 *  - `data-label` on every cell, carrying its column's heading, so a stacked
 *    cell can print the label the header row no longer provides;
 *  - explicit ARIA roles, because `display: block` on a table element DROPS its
 *    implicit role. Without them a stacked table stops being a table to a
 *    screen reader exactly where the visible headers are hidden too.
 *
 * Which tables qualify is decided here rather than in a media query, because
 * `min-width` cannot ask whether it is needed. A blanket rule by column count —
 * the obvious one — would also catch responsible-use's four-column chain
 * table, which is a comparison: stacking its 21 rows measured 2.15x taller,
 * each block headed by a bare ordinal. (An earlier version of this comment said
 * its longest cell was eleven characters. It is 38 — eleven is the Status of
 * the first three rows only, which is what a one-row sample shows you.)
 */

/** Below this many columns a table has room to wrap without help. */
const MIN_COLUMNS = 3;

/** Longest cell in the last column, below which the table needs no help. */
const MIN_PROSE = 60;

/**
 * The width, in characters, past which a column wraps rather than widening the
 * table. Credit each column with at most this much and the sum estimates the
 * table's minimum, not its max-content width — the two are far apart, and the
 * second is the wrong question: architecture's flag table wants 1424px with no
 * wrapping and settles into 720px perfectly well.
 *
 * 24 is not a guess. Every table on the site was measured at 1440px for actual
 * container overflow, and 24 is the only value tried (against 28, 32 and 40)
 * that reproduces all eight verdicts: configuration's variable table overflows
 * and stacks, and the other seven fit and stay tables.
 */
const COMFORTABLE_CHARS = 24;

/** Approximate advance width of a character at the table's size, in px. */
const CHAR_PX = 7.5;

/** Horizontal padding a cell carries, in px. */
const CELL_PADDING_PX = 24;

/**
 * The reading column, in px: --sl-content-width is 45rem. A table whose
 * estimated minimum exceeds this can never fit, at any viewport, so it stacks
 * everywhere rather than only on a phone — which is the case configuration.mdx
 * has always been in: measured at 1440px, its variable table still overflowed
 * its container by 255px.
 */
const READING_COLUMN_PX = 45 * 16;

/** All text under a hast node. */
function textOf(node) {
	if (node.type === "text") return node.value;
	if (!node.children) return "";
	return node.children.map(textOf).join("");
}

/** Every element with the given tag name, depth first. */
function collect(node, tagName, out = []) {
	if (node.type === "element" && node.tagName === tagName) out.push(node);
	for (const child of node.children ?? []) collect(child, tagName, out);
	return out;
}

/** The `td`/`th` children of a row, in order. */
const cellsOf = (row) =>
	row.children.filter(
		(cell) =>
			cell.type === "element" &&
			(cell.tagName === "td" || cell.tagName === "th"),
	);

/** Adds properties to a hast element without dropping what it already has. */
function set(node, props) {
	node.properties = { ...node.properties, ...props };
}

/**
 * A rehype transformer. Marks qualifying tables and annotates their cells.
 *
 * @returns {(tree: import('hast').Root) => void}
 */
export function rehypeWideTables() {
	return (tree) => {
		for (const table of collect(tree, "table")) {
			const rows = collect(table, "tr");
			if (rows.length === 0) continue;

			const columns = Math.max(...rows.map((row) => cellsOf(row).length));
			if (columns < MIN_COLUMNS) continue;

			const bodyRows = rows.filter((row) =>
				cellsOf(row).some((cell) => cell.tagName === "td"),
			);
			// Every column but the first, not just the last. The first is the
			// record's name and is always short; the prose can be anywhere after
			// it. tools.mdx's "How the saved file is named" table keeps its prose
			// in column two and a 21-character last column, so a last-column-only
			// test skipped it entirely — no stacking, no labels, no roles.
			const longest = Math.max(
				0,
				...bodyRows.flatMap((row) =>
					cellsOf(row)
						.slice(1)
						.map((cell) => textOf(cell).trim().length),
				),
			);
			if (longest < MIN_PROSE) continue;

			const headings = cellsOf(rows[0]).map((cell) => textOf(cell).trim());

			// Can this table ever fit the reading column? Each column is credited
			// with its longest cell, capped at a comfortable measure, since a
			// column wider than that wraps rather than pushing the table out.
			const perColumn = headings.map((_, index) =>
				Math.max(
					0,
					...rows.map((row) => {
						const cell = cellsOf(row)[index];
						return cell ? textOf(cell).trim().length : 0;
					}),
				),
			);
			const estimated = perColumn.reduce(
				(total, chars) =>
					total +
					Math.min(chars, COMFORTABLE_CHARS) * CHAR_PX +
					CELL_PADDING_PX,
				0,
			);

			set(table, {
				"data-stacks": estimated > READING_COLUMN_PX ? "always" : "",
				role: "table",
			});
			for (const group of ["thead", "tbody"]) {
				for (const node of collect(table, group))
					set(node, { role: "rowgroup" });
			}
			for (const row of rows) {
				set(row, { role: "row" });
				cellsOf(row).forEach((cell, index) => {
					if (cell.tagName === "th") {
						// A header in the first row labels a column; one at the start of
						// a body row labels that row.
						const isColumnHeader = row === rows[0];
						set(cell, {
							role: isColumnHeader ? "columnheader" : "rowheader",
							scope: isColumnHeader ? "col" : "row",
						});
						if (!isColumnHeader)
							set(cell, { "data-label": headings[index] ?? "" });
						return;
					}
					set(cell, { role: "cell", "data-label": headings[index] ?? "" });
				});
			}
		}
	};
}
