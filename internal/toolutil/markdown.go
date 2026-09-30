package toolutil

import (
	"fmt"
	"net/url"
	"strings"
)

// Every value this file touches is third-party text: a catalog title, an
// author, a mirror's URL, a BibTeX entry built from metadata nobody checked.
// It is rendered into Markdown that a model reads as instructions-adjacent
// context, so the question each helper answers is the same one: what can this
// value do to the document it is placed in, and how is that taken away without
// changing what the value says.
//
// They live here rather than in internal/tools because internal/prompts writes
// Markdown too. Two packages with two vocabularies is how one of them ends up
// with a hole the other already closed.

// isRenderControl reports whether r is a control character with no place in
// rendered Markdown. A tab, a newline and a carriage return are handled by the
// callers that care about lines; everything else in that range only confuses a
// renderer or hides bytes from a reader.
func isRenderControl(r rune) bool {
	switch r {
	case '\t', '\n', '\r':
		return false
	}
	return r < 0x20 || r == 0x7f
}

// StripControlBytes removes the control characters a renderer has no use for.
//
// It runs before every other check here, because a control byte inside a value
// is how a check made on the bytes as sent is evaded: "java\x00script:" is the
// destination "javascript:" once a renderer has dropped the NUL, and a scheme
// check that ran first would have let it through.
func StripControlBytes(s string) string {
	if strings.IndexFunc(s, isRenderControl) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isRenderControl(r) {
			return -1
		}
		return r
	}, s)
}

// EscapeMdTableCell sanitizes a value for a Markdown table cell: it collapses
// newlines and escapes pipes so the value cannot break the table layout.
//
// A finished link does not survive this function. A link is built by
// [MdTitleLink] or [MdTitleLinkCell] from its raw halves and is never escaped
// afterwards.
//
// The result reads the same in a list item, which is where a card row puts it:
// every escape written is a backslash escape, and outside a code span both
// places process those alike.
func EscapeMdTableCell(s string) string {
	return escapeCellPipes(oneLine(s))
}

// oneLine drops the control bytes, collapses every line ending to a space and
// trims the result, so a value stays on the line it was written into.
func oneLine(s string) string {
	s = StripControlBytes(s)
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// escapeCellPipes backslash-escapes every pipe, doubling the backslashes
// already in front of one.
//
// The doubling is the part that matters. GFM splits a row before it reads any
// inline content, and a backslash there takes the byte after it along, so a
// backslash the value already had in front of a pipe takes the added one and
// leaves the pipe live: `a\|b` escaped to `a\\|b` is two cells. With each such
// backslash doubled, every backslash pairs with its copy, the added one pairs
// with the pipe, and the reader sees the backslashes the value had. A
// backslash anywhere else is left as it was.
func escapeCellPipes(s string) string {
	if !strings.Contains(s, "|") {
		return s
	}
	var b strings.Builder
	run := 0
	for i := range len(s) {
		switch s[i] {
		case '\\':
			run++
		case '|':
			b.WriteString(strings.Repeat(`\`, run))
			b.WriteByte('\\')
			run = 0
		default:
			run = 0
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// EscapeMdHeading sanitizes a value for a Markdown heading: it strips the
// characters that would promote or demote the heading level and collapses
// newlines, so the heading stays one line and stays the level it was written
// at.
func EscapeMdHeading(s string) string {
	s = StripControlBytes(s)
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimLeft(strings.TrimSpace(s), "# ")
}

// mdLinkLabelEscaper backslash-escapes the characters that let text inside a
// link label end that label, or open a construct of its own.
//
// The angle brackets are there because CommonMark gives autolinks and raw HTML
// precedence over the link brackets around them: a label holding
// `<a href="http://attacker.invalid">` opens an anchor of its own inside the
// one this package wrote, and an HTML-rendering client closes the outer link
// at it. All of these are backslash-escapable ASCII punctuation, so the
// visible text is what it always was.
var mdLinkLabelEscaper = strings.NewReplacer(
	`\`, `\\`, "[", `\[`, "]", `\]`, "`", "\\`", "<", `\<`, ">", `\>`,
)

// EscapeMdLinkLabel renders s as the visible text of a Markdown link.
func EscapeMdLinkLabel(s string) string {
	return mdLinkLabelEscaper.Replace(StripControlBytes(s))
}

// mdLinkDestEscaper percent-encodes the characters that would end a link
// destination early or split it in two. Each encoding resolves to the same
// resource, so the link still works.
//
// The pipe and the line endings are encoded for the cell the link sits in
// rather than for the link itself: a destination is not escaped by
// [EscapeMdTableCell], so a URL carrying a pipe ends the cell in the middle of
// the link, and one carrying a line break ends the row.
var mdLinkDestEscaper = strings.NewReplacer(
	"(", "%28", ")", "%29", "<", "%3C", ">", "%3E",
	" ", "%20", `"`, "%22", "|", "%7C", "\r", "%0D", "\n", "%0A",
)

// EscapeMdLinkDestination renders rawURL as the destination of a Markdown link.
func EscapeMdLinkDestination(rawURL string) string {
	return mdLinkDestEscaper.Replace(StripControlBytes(rawURL))
}

// linkableSchemes are the only schemes this server writes as a live link.
//
// Plain http is in the set on purpose: the catalog mirrors this server reaches
// serve over it, and refusing to link them would not make anything safer — it
// would render a working address as inert text. What the allow list is for is
// the schemes a client executes rather than fetches.
var linkableSchemes = map[string]bool{"http": true, "https": true}

// LinkableDestination reports whether rawURL may be written as the destination of
// a link: an absolute address on one of [linkableSchemes], with a host.
//
// The escaper above contains the delimiters, so a value cannot end the link it
// is in, and it says nothing about the scheme. A javascript, data or vbscript
// destination is a whole link a client renders live, and it is the client's
// renderer that decides what a click does. Every address this server links is
// one a catalog or a provider published as a download, so the allow list costs
// nothing legitimate.
//
// The scheme is read by parsing rather than by matching a prefix: a parser is
// what a client uses, so it is what decides whether two readings of the same
// bytes can disagree.
func LinkableDestination(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(StripControlBytes(rawURL)))
	if err != nil {
		return false
	}
	return linkableSchemes[strings.ToLower(parsed.Scheme)] && parsed.Host != ""
}

// MdCodeSpan renders s inside a code span long enough that s cannot close it,
// and returns "" for a value with nothing in it. It is for anywhere but a
// table cell, which takes [MdCodeSpanCell].
//
// It is what an address that may not be linked is written as, and what a card
// row writes a value the reader copies as: the reader keeps the bytes the
// source sent, and nothing in them is live. No pipe is escaped, because a code
// span processes no backslash escape and a list item splits on no pipe, so an
// escaped pipe here is a backslash the value never had.
func MdCodeSpan(s string) string {
	s = oneLine(s)
	if s == "" {
		return ""
	}
	return backtickSpan(s)
}

// mdLiteralEscaper backslash-escapes every ASCII punctuation character but the
// pipe, which is every character a backslash can escape in CommonMark, so the
// text it produces is read as the characters it holds and opens nothing: no
// code span, no emphasis, no link, no escape of its own. The pipe is left for
// the cell layer.
var mdLiteralEscaper = strings.NewReplacer(
	`\`, `\\`, "`", "\\`", "!", `\!`, `"`, `\"`, "#", `\#`, "$", `\$`,
	"%", `\%`, "&", `\&`, "'", `\'`, "(", `\(`, ")", `\)`, "*", `\*`,
	"+", `\+`, ",", `\,`, "-", `\-`, ".", `\.`, "/", `\/`, ":", `\:`,
	";", `\;`, "<", `\<`, "=", `\=`, ">", `\>`, "?", `\?`, "@", `\@`,
	"[", `\[`, "]", `\]`, "^", `\^`, "_", `\_`, "{", `\{`, "}", `\}`,
	"~", `\~`,
)

// MdCodeSpanCell renders s as [MdCodeSpan] does, for a table cell.
//
// GFM splits a row on its pipes before it reads any code span, and it honors a
// backslash-escaped pipe inside one, so the pipe is escaped and the span is
// otherwise the same. The one value a span cannot carry there is a backslash
// already in front of a pipe: a backslash takes the byte after it along when
// the row is split, so one added backslash leaves the pipe live, and two show a
// backslash the value never had, since the span keeps both. That value is
// written as text instead, every punctuation character backslash-escaped and
// each pipe escaped once more for the row, and the reader keeps the bytes the
// source sent at the cost of the monospace.
func MdCodeSpanCell(s string) string {
	s = oneLine(s)
	if s == "" {
		return ""
	}
	if strings.Contains(s, `\|`) {
		return strings.ReplaceAll(mdLiteralEscaper.Replace(s), "|", `\|`)
	}
	return backtickSpan(strings.ReplaceAll(s, "|", `\|`))
}

// backtickSpan wraps s in a code span whose fence outruns every backtick run
// inside it, padded when s starts or ends with a backtick, or the fence and the
// value would run together and the span would not close where it should.
func backtickSpan(s string) string {
	fence := strings.Repeat("`", longestBacktickRun(s)+1)
	pad := ""
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		pad = " "
	}
	return fence + pad + s + pad + fence
}

// MdTitleLink renders title as a Markdown link to rawURL, for anywhere but a
// table cell, which takes [MdTitleLinkCell].
//
// Both halves are escaped, so neither the title nor the URL can end the link
// they are in — which is the hole this function exists to close: a download
// URL carrying a close parenthesis used to end the link early, and everything
// after it rendered as prose beside a link that pointed at a truncated
// address.
//
// A destination that is not an http or https address is not linked at all. It
// is written in a code span instead, after the title when the title says
// something else, so the reader keeps what the source sent and nothing in it
// is live.
func MdTitleLink(title, rawURL string) string {
	return titleLink(title, rawURL, EscapeMdLinkLabel(oneLine(title)), MdCodeSpan(rawURL))
}

// MdTitleLinkCell renders title as [MdTitleLink] does, for a table cell.
//
// The label escaper doubles every backslash, so a pipe escaped for the row
// after it is always one backslash past a pair and ends no cell, and the
// address that may not be linked goes through [MdCodeSpanCell].
func MdTitleLinkCell(title, rawURL string) string {
	label := strings.ReplaceAll(EscapeMdLinkLabel(oneLine(title)), "|", `\|`)
	return titleLink(title, rawURL, label, MdCodeSpanCell(rawURL))
}

// titleLink is the decision [MdTitleLink] and [MdTitleLinkCell] share, handed
// the label and the fallback span each has already escaped for where it lands.
// The title shown beside a span goes through [EscapeMdTableCell], whose
// backslash escapes read the same in a cell and in a list item.
func titleLink(title, rawURL, label, span string) string {
	if strings.TrimSpace(rawURL) == "" {
		return EscapeMdTableCell(title)
	}
	if !LinkableDestination(rawURL) {
		if strings.TrimSpace(title) == "" || title == rawURL {
			return span
		}
		return EscapeMdTableCell(title) + " " + span
	}
	return fmt.Sprintf("[%s](%s)", label, EscapeMdLinkDestination(rawURL))
}

// MdAutolink renders rawURL as an address a reader both sees and can click.
//
// It is the right shape when the address is the whole of what is being shown:
// a link whose label is its own destination writes the address twice, and a
// code span alone is not clickable. An address that may not be linked falls
// back to a code span, so nothing a third party sent is ever live. The span
// is [MdCodeSpan]'s, so this is not for a table cell.
func MdAutolink(rawURL string) string {
	if !LinkableDestination(rawURL) {
		return MdCodeSpan(rawURL)
	}
	return "<" + EscapeMdLinkDestination(rawURL) + ">"
}

// MarkdownCodeFence returns a fence long enough that content cannot close it
// early. Per the CommonMark rule a closing fence must be at least as long as
// the opening one, so the fence is max(3, longest run of backticks + 1).
func MarkdownCodeFence(content string) string {
	return strings.Repeat("`", max(3, longestBacktickRun(content)+1))
}

// MarkdownFencedBlock wraps content in a fenced code block whose fence content
// cannot close, so untrusted-derived text — a BibTeX entry built from catalog
// metadata, a provider's error body — cannot break out of the fence and be
// read as Markdown or as instructions. lang is the info string.
func MarkdownFencedBlock(lang, content string) string {
	fence := MarkdownCodeFence(content)
	return fence + lang + "\n" + content + "\n" + fence
}

// WrapQuotedBody renders a body a third party typed as a Markdown blockquote,
// one "> " per line.
//
// A quote is containment for a whole block rather than for a character: a
// heading, a list item or a table row inside it stays inside it, so prose
// nobody checked cannot add a field to the card it was written under or a
// section to the document. An empty body quotes nothing.
//
// The line endings are normalized first, and that is load-bearing. CommonMark
// counts a CRLF and a bare CR as line endings too, so splitting on LF alone
// leaves whatever follows a CR outside the quote — and a heading there is
// structure rather than a lazy paragraph continuation, which is precisely what
// the quote exists to prevent.
func WrapQuotedBody(body string) string {
	if body == "" {
		return ""
	}
	body = DefuseNextStepsHeading(StripControlBytes(body))
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if line == "" {
			lines[i] = ">"
			continue
		}
		lines[i] = "> " + line
	}
	return strings.Join(lines, "\n")
}

// EndBlock leaves the builder empty or ending in a blank line, whatever it
// ends with now, so a heading, a quote, a fence or a card row opens a block of
// its own rather than continuing the last line as a lazy paragraph.
func EndBlock(b *strings.Builder) {
	written := b.String()
	switch {
	case written == "" || strings.HasSuffix(written, "\n\n"):
	case strings.HasSuffix(written, "\n"):
		b.WriteString("\n")
	default:
		b.WriteString("\n\n")
	}
}

// longestBacktickRun returns the length of the longest run of consecutive
// backticks in s, or 0 when s contains none.
func longestBacktickRun(s string) int {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
			continue
		}
		run = 0
	}
	return longest
}
