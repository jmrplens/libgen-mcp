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
// [MdTitleLink] from its raw halves and is never escaped afterwards.
func EscapeMdTableCell(s string) string {
	s = StripControlBytes(s)
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.TrimSpace(s)
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
// and returns "" for a value with nothing in it.
//
// It is what an address that may not be linked is written as: the reader keeps
// the bytes the source sent, and nothing in them is live.
func MdCodeSpan(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(StripControlBytes(s), "\n", " "))
	if s == "" {
		return ""
	}
	fence := strings.Repeat("`", longestBacktickRun(s)+1)
	pad := ""
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		pad = " "
	}
	return fence + pad + strings.ReplaceAll(s, "|", "\\|") + pad + fence
}

// MdTitleLink renders title as a Markdown link to rawURL.
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
	escaped := EscapeMdTableCell(title)
	if strings.TrimSpace(rawURL) == "" {
		return escaped
	}
	if !LinkableDestination(rawURL) {
		if strings.TrimSpace(title) == "" || title == rawURL {
			return MdCodeSpan(rawURL)
		}
		return escaped + " " + MdCodeSpan(rawURL)
	}
	return fmt.Sprintf("[%s](%s)", EscapeMdLinkLabel(escaped), EscapeMdLinkDestination(rawURL))
}

// MdAutolink renders rawURL as an address a reader both sees and can click.
//
// It is the right shape when the address is the whole of what is being shown:
// a link whose label is its own destination writes the address twice, and a
// code span alone is not clickable. An address that may not be linked falls
// back to a code span, so nothing a third party sent is ever live.
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
