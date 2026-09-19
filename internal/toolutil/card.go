package toolutil

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Card is the one shape a tool result about a single record takes, and the
// only writer of its rows: the heading the formatter composes, then one
// "- **Label**: value" item per field, then the record's long text, then the
// next-step guidance last.
//
// The list shape is the one that survives whatever is written around it. A
// list item is a complete block wherever it lands, so a renderer can extend a
// card another renderer started, and a card can follow a quote, a fence or a
// table. A table row is a row only while nothing but rows has been written
// since its header, and anywhere else it is a line of literal pipes.
//
// A Card streams into the caller's builder rather than buffering a document,
// so a renderer keeps the seams it has today: a fenced citation, a table of
// results, a second renderer adding rows. What keeps that composable is the
// mark. The card records the builder's length after each of its own writes,
// and when anything else has been written since, the next row starts after a
// blank line, so it opens a list of its own instead of continuing a foreign
// block.
//
// Every value is escaped at the write that renders it, by the helper the
// value's shape needs, and a caller passes catalog text as it arrived: the
// cell escaper for an inline value, [MdCodeSpan] for an identifier, [MdTitleLink]
// for a link, [WrapQuotedBody] for prose. An empty, blank or absent value writes
// nothing where the zero is an absence: the card shows what the catalog sent,
// and never a label with nothing after it.
//
// It is deliberately smaller than one row per shape. Nested cards, sections
// and tables are absent because no renderer here writes one; a collection of
// records is a table of its own, which [MarkdownTableHeader] and the search
// renderer already write.
type Card struct {
	b *strings.Builder
	// mark is b.Len() after the last write this card made, and -1 before the
	// first, so a first row on a builder the caller already wrote to separates
	// itself the same way a later one does.
	mark int
	// secrets are the labels [Card.Secret] wrote, for the hint [Card.End]
	// adds.
	secrets []string
}

// NewCard starts a card in b. A non-empty heading is written as an H2 through
// [EscapeMdHeading]; the renderer composes it, and the whole composition is
// escaped, so it must not begin with '#'. An empty heading writes none, for a
// card that continues under a heading the caller wrote.
func NewCard(b *strings.Builder, heading string) *Card {
	c := &Card{b: b, mark: -1}
	if blank(heading) {
		return c
	}
	c.separate()
	fmt.Fprintf(b, "## %s\n\n", EscapeMdHeading(heading))
	c.wrote()
	return c
}

// Field writes "- **label**: value" with both halves through the cell
// escaper. A blank value writes nothing, which is how every optional field is
// written: the card shows what the catalog sent.
func (c *Card) Field(label, value string) {
	c.row(label, cardInline(value))
}

// FieldOr writes value like [Card.Field], or absent in its place when the
// value is blank, for a field whose absence is the answer. A blank absent
// writes nothing.
func (c *Card) FieldOr(label, value, absent string) {
	if blank(value) {
		value = absent
	}
	c.row(label, cardInline(value))
}

// Int writes a number that is an answer at zero: a size, a page count, a
// figure the source always sends.
func (c *Card) Int(label string, v int64) {
	c.row(label, strconv.FormatInt(v, 10))
}

// Count writes a number only when it is not zero, for a figure whose zero
// means the source did not say. It is the distinction that decides whether a
// row appears at all: a book with zero citations and a book whose citation
// count nobody reported are different facts, and only one of them is worth a
// row.
func (c *Card) Count(label string, v int64) {
	if v == 0 {
		return
	}
	c.Int(label, v)
}

// Code writes the value as a code span through [MdCodeSpan], for a value the
// reader has to copy exactly: an md5, a DOI, a path. Nothing inside the span
// is Markdown. A blank value writes nothing.
func (c *Card) Code(label, value string) {
	c.row(label, MdCodeSpan(value))
}

// Secret writes a value that is shown once, as [Card.Code] does, and records
// it so [Card.End] adds the hint that it is not stored. A blank value writes
// nothing and adds no hint.
//
// Nothing writes a secret into a card today. It is here because this server
// has two per-call credentials and a stated rule about where a secret may
// appear, and a writer that makes the safe form the easy form is how such a
// rule survives the next contributor: the alternative is that the first person
// who needs to echo one reaches for Field.
func (c *Card) Secret(label, secret string) {
	span := MdCodeSpan(secret)
	if span == "" {
		return
	}
	c.row(label, span)
	c.noteSecret(label)
}

// Link writes a row whose value is a link to url with text as its label,
// through [MdTitleLink]: a destination that is not an absolute http or https
// address is shown in a code span rather than linked. A row with neither text
// nor url writes nothing.
func (c *Card) Link(label, text, url string) {
	c.row(label, MdTitleLink(text, url))
}

// URL writes the address as a row that both shows and links it, through
// [MdAutolink], for the value a reader is meant to act on. A blank address
// writes nothing.
func (c *Card) URL(label, url string) {
	if blank(url) {
		return
	}
	c.row(label, MdAutolink(url))
}

// Flag writes "- **label**" when on is true and nothing otherwise, for a
// condition worth stating only when it holds: a resumed download, a trimmed
// list. A flag that is false is not a row that says "no", it is no row.
func (c *Card) Flag(label string, on bool) {
	if !on {
		return
	}
	c.separate()
	fmt.Fprintf(c.b, "- **%s**\n", cardInline(label))
	c.wrote()
}

// Text writes prose a third party typed: a book description, a provenance
// note. A one-line body stays on the field's line through the cell escaper. A
// longer one becomes a blockquote under the label through [WrapQuotedBody],
// indented so the quote belongs to the item, and nothing in it can add a
// field, a heading or a list item to the card. Trailing line breaks are
// dropped first, so a one-line body that ends in a newline stays inline. A
// blank body writes nothing.
func (c *Card) Text(label, body string) {
	body = strings.TrimRight(body, "\r\n")
	if blank(body) {
		return
	}
	if !strings.ContainsAny(body, "\r\n") {
		c.row(label, cardInline(body))
		return
	}
	c.separate()
	fmt.Fprintf(c.b, "- **%s**:\n", cardInline(label))
	for line := range strings.SplitSeq(WrapQuotedBody(body), "\n") {
		c.b.WriteString("  " + line + "\n")
	}
	c.wrote()
}

// There is deliberately no method that writes a value as given. Such a row
// would be the one hole the escaping gate cannot see — the write inside it has
// no verb and no construct of its own, so the gate would have to judge the
// call site instead, which is a rule this repository's audit does not have. A
// renderer that needs to compose a value out of escaped parts builds it with
// [MdTitleLink] or [MdCodeSpan] and passes it to [Card.Field], which escaping
// again does not change.

// Section writes a heading one level below the card's own and returns the card
// that writes the rows under it. The title is the server's own words, escaped
// the same way so the rule has one shape rather than two.
//
// It is the one nesting this writer has. A section is a part of the same
// record — a citation, the external metadata — and its rows are rows; a
// collection of other records is a table, which is not a card at all.
func (c *Card) Section(title string) *Card {
	c.endBlock()
	fmt.Fprintf(c.b, "### %s\n\n", EscapeMdHeading(title))
	c.wrote()
	return &Card{b: c.b, mark: c.b.Len()}
}

// Fence writes body inside a fenced code block sized past the longest backtick
// run in it, through [MarkdownFencedBlock], so nothing inside can close the
// block and be read as Markdown. lang is the info string. A blank body writes
// nothing.
//
// It is a block rather than a row: a citation export, a mirror's error text
// and an extracted page are each the whole of what is being shown, and a fence
// is the only containment that survives a value carrying line breaks.
func (c *Card) Fence(lang, body string) {
	if blank(body) {
		return
	}
	c.endBlock()
	c.b.WriteString(MarkdownFencedBlock(lang, body) + "\n")
	c.wrote()
}

// Quote writes prose as a blockquote of its own, through [WrapQuotedBody]: a
// provenance line, a caveat that travels with the block above it. A blank body
// writes nothing.
func (c *Card) Quote(body string) {
	if blank(body) {
		return
	}
	c.endBlock()
	c.b.WriteString(WrapQuotedBody(strings.TrimRight(body, "\r\n")) + "\n")
	c.wrote()
}

// End writes the next-step guidance through [WriteNextSteps] and is always the
// last write of a card: a row written after it lands below the guidance, where
// a reader no longer reads it as a field. A secret the card showed adds its
// step ahead of the caller's.
func (c *Card) End(steps ...string) {
	all := make([]string, 0, len(c.secrets)+len(steps))
	for _, label := range c.secrets {
		all = append(all, "The "+strings.ToLower(label)+" above is shown once and is not stored; keep it if you need it again.")
	}
	WriteNextSteps(c.b, append(all, steps...)...)
}

// row writes one "- **label**: rendered" item, where rendered is the value
// already through the helper its shape needs. A blank rendering writes
// nothing.
func (c *Card) row(label, rendered string) {
	if blank(rendered) {
		return
	}
	c.separate()
	fmt.Fprintf(c.b, "- **%s**: %s\n", cardInline(label), rendered)
	c.wrote()
}

// separate ends whatever the builder holds that this card did not write, so
// the next row opens a list of its own. Nothing is written when the card's own
// last line is the builder's last line, which is how consecutive rows stay one
// list.
func (c *Card) separate() {
	if c.b.Len() == c.mark {
		return
	}
	c.endBlock()
}

// endBlock is separate for a write that is a block rather than a row: a
// heading, a fence and a quote each open one of their own, so they end the
// previous block whoever wrote it — this card's own rows included, since a
// heading that continued a list would be a line of that list.
func (c *Card) endBlock() {
	EndBlock(c.b)
}

// wrote records that the builder's last line is this card's.
func (c *Card) wrote() {
	c.mark = c.b.Len()
}

// noteSecret records a label [Card.Secret] wrote, once.
func (c *Card) noteSecret(label string) {
	if slices.Contains(c.secrets, label) {
		return
	}
	c.secrets = append(c.secrets, label)
}

// cardInline renders an externally-sourced value on a line the card wrote: the
// cell escaper collapses line breaks, drops control bytes and neutralizes the
// pipe, and the server's own guidance heading is defused so a value carrying
// it is shown as the text it is rather than opening a second one.
func cardInline(s string) string {
	return DefuseNextStepsHeading(EscapeMdTableCell(s))
}

// blank reports whether s holds nothing a reader would see.
func blank(s string) bool {
	return strings.TrimSpace(s) == ""
}
