package toolutil

import (
	"strings"
	"testing"
)

// TestStripControlBytes_RemovesWhatARendererHides verifies the control bytes go
// and the three whitespace characters a caller may still want do not.
func TestStripControlBytes_RemovesWhatARendererHides(t *testing.T) {
	testCases := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text is untouched", in: "a plain title", want: "a plain title"},
		{name: "a NUL is dropped", in: "java\x00script:alert(1)", want: "javascript:alert(1)"},
		{name: "a bell is dropped", in: "ti\atle", want: "title"},
		{name: "DEL is dropped", in: "ti\x7ftle", want: "title"},
		{name: "tab, newline and CR survive", in: "a\tb\nc\rd", want: "a\tb\nc\rd"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripControlBytes(tc.in); got != tc.want {
				t.Errorf("StripControlBytes(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestEscapeMdTableCell_CannotBreakTheRow verifies a cell value cannot end its
// cell or its row, which is what a pipe and a newline respectively do.
func TestEscapeMdTableCell_CannotBreakTheRow(t *testing.T) {
	got := EscapeMdTableCell("  a | b\r\nsecond line\x00  ")
	for _, unwanted := range []string{"\n", "\r", "\x00"} {
		t.Run(unwanted, func(t *testing.T) {
			if strings.Contains(got, unwanted) {
				t.Errorf("EscapeMdTableCell() = %q, which still carries %q", got, unwanted)
			}
		})
	}
	if strings.Contains(got, "| b") && !strings.Contains(got, `\| b`) {
		t.Errorf("EscapeMdTableCell() = %q, want the pipe escaped", got)
	}
}

// TestEscapeMdHeading_StaysOneLineAtItsOwnLevel verifies a value cannot promote
// itself to a heading of its own or run onto a second line.
func TestEscapeMdHeading_StaysOneLineAtItsOwnLevel(t *testing.T) {
	got := EscapeMdHeading("### Fake heading\nsecond line")
	if strings.HasPrefix(got, "#") {
		t.Errorf("EscapeMdHeading() = %q, want the leading hashes gone", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("EscapeMdHeading() = %q, want one line", got)
	}
}

// TestLinkableDestination_OnlyAbsoluteHTTP verifies the allow list: an address
// a client would render live under any other scheme is not linked at all.
func TestLinkableDestination_OnlyAbsoluteHTTP(t *testing.T) {
	testCases := []struct {
		url  string
		want bool
	}{
		{url: "https://libgen.li/get.php?md5=abc", want: true},
		{url: "http://mirror.example/file.pdf", want: true},
		{url: "HTTPS://Libgen.li/x", want: true},
		{url: "  https://libgen.li/x  ", want: true},
		{url: "https://", want: false},
		{url: "javascript:alert(1)", want: false},
		{url: "java\x00script:alert(1)", want: false},
		{url: "data:text/html,<script>", want: false},
		{url: "/relative/path.pdf", want: false},
		{url: "", want: false},
	}

	for _, tc := range testCases {
		t.Run(tc.url, func(t *testing.T) {
			if got := LinkableDestination(tc.url); got != tc.want {
				t.Errorf("LinkableDestination(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

// TestMdTitleLink_NeitherHalfCanEndTheLink is the assertion this whole file
// exists for: the leak it replaces built [label](url) with the URL raw, so a
// download URL carrying a close parenthesis ended the link early and the rest
// rendered as prose beside a link pointing at a truncated address.
func TestMdTitleLink_NeitherHalfCanEndTheLink(t *testing.T) {
	got := MdTitleLink("Title | with ] brackets", "https://mirror.example/get.php?f=a(b)c&x=1")

	if strings.Contains(got, "(b)c") {
		t.Errorf("MdTitleLink() = %q, want the parentheses in the destination encoded", got)
	}
	if !strings.Contains(got, "%28") || !strings.Contains(got, "%29") {
		t.Errorf("MdTitleLink() = %q, want %%28 and %%29 in the destination", got)
	}
	if strings.Count(got, "](") != 1 {
		t.Errorf("MdTitleLink() = %q, want exactly one link", got)
	}
	if !strings.HasSuffix(got, ")") {
		t.Errorf("MdTitleLink() = %q, want it to end where the link ends", got)
	}
}

// TestMdTitleLink_AnUnlinkableAddressIsShownNotLinked verifies the other half
// of the rule: a destination no client should open is written as text.
func TestMdTitleLink_AnUnlinkableAddressIsShownNotLinked(t *testing.T) {
	testCases := []struct {
		name          string
		title, url    string
		wantLink      bool
		wantSubstring string
	}{
		{
			name:  "a javascript destination is a code span",
			title: "click me", url: "javascript:alert(1)",
			wantSubstring: "`javascript:alert(1)`",
		},
		{
			name:  "an empty url is just the title",
			title: "a title", url: "",
			wantSubstring: "a title",
		},
		{
			name:  "a blank url is just the title",
			title: "a title", url: "   ",
			wantSubstring: "a title",
		},
		{
			name:  "an http destination is linked",
			title: "a title", url: "https://example.org/x",
			wantLink: true, wantSubstring: "](https://example.org/x)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := MdTitleLink(tc.title, tc.url)
			if isLink := strings.Contains(got, "]("); isLink != tc.wantLink {
				t.Errorf("MdTitleLink(%q, %q) = %q, linked = %v, want %v", tc.title, tc.url, got, isLink, tc.wantLink)
			}
			if !strings.Contains(got, tc.wantSubstring) {
				t.Errorf("MdTitleLink(%q, %q) = %q, want it to contain %q", tc.title, tc.url, got, tc.wantSubstring)
			}
		})
	}
}

// TestMdTitleLink_ATitleThatIsTheAddressIsNotRepeated verifies the one-value
// case: when the title says nothing the address does not, the address alone is
// written.
func TestMdTitleLink_ATitleThatIsTheAddressIsNotRepeated(t *testing.T) {
	const addr = "ftp://example.org/x"
	if got := MdTitleLink(addr, addr); strings.Count(got, addr) != 1 {
		t.Errorf("MdTitleLink(addr, addr) = %q, want the address once", got)
	}
}

// TestEscapeMdLinkLabel_CannotOpenAConstructOfItsOwn verifies the label
// escaper: a label holding raw HTML or a link of its own must not close the
// link it is inside.
func TestEscapeMdLinkLabel_CannotOpenAConstructOfItsOwn(t *testing.T) {
	got := EscapeMdLinkLabel(`<a href="http://attacker.invalid">x</a> [y](z) ` + "`code`")

	// Every character that could open a construct is preceded by a backslash,
	// which is what stops a renderer obeying it instead of showing it.
	for _, escaped := range []string{`\<a `, `\>x`, `\[y\]`, "\\`code\\`"} {
		t.Run(escaped, func(t *testing.T) {
			if !strings.Contains(got, escaped) {
				t.Errorf("EscapeMdLinkLabel() = %q, want it to carry %q", got, escaped)
			}
		})
	}
	// And nothing is left able to open one: no bare opener survives.
	for _, opener := range []string{"<", "[", "]", "`"} {
		t.Run(opener, func(t *testing.T) {
			if strings.Contains(strings.ReplaceAll(got, `\`+opener, ""), opener) {
				t.Errorf("EscapeMdLinkLabel() = %q, which still carries a bare %q", got, opener)
			}
		})
	}
}

// TestMarkdownFencedBlock_ContentCannotCloseTheFence verifies the CommonMark
// rule this rests on: a closing fence must be at least as long as the opening
// one, so the opening fence is one backtick longer than the longest run inside.
func TestMarkdownFencedBlock_ContentCannotCloseTheFence(t *testing.T) {
	testCases := []struct {
		name      string
		content   string
		wantFence string
	}{
		{name: "no backticks takes the minimum", content: "plain", wantFence: "```"},
		{name: "an embedded fence is outrun", content: "a\n```\nb", wantFence: "````"},
		{name: "a longer embedded fence is outrun too", content: "a\n`````\nb", wantFence: "``````"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			block := MarkdownFencedBlock("text", tc.content)
			if !strings.HasPrefix(block, tc.wantFence+"text\n") {
				t.Errorf("MarkdownFencedBlock() = %q, want it to open with %q", block, tc.wantFence)
			}
			if !strings.HasSuffix(block, "\n"+tc.wantFence) {
				t.Errorf("MarkdownFencedBlock() = %q, want it to close with %q", block, tc.wantFence)
			}
		})
	}
}

// TestMdCodeSpan_ContentCannotCloseTheSpan verifies the same rule for a span,
// in both of its forms, and that an empty value writes nothing rather than an
// empty pair of ticks.
func TestMdCodeSpan_ContentCannotCloseTheSpan(t *testing.T) {
	helpers := []struct {
		name string
		span func(string) string
	}{
		{name: "MdCodeSpan", span: MdCodeSpan},
		{name: "MdCodeSpanCell", span: MdCodeSpanCell},
	}
	for _, h := range helpers {
		t.Run(h.name, func(t *testing.T) {
			if got := h.span(" \n "); got != "" {
				t.Errorf("%s(blank) = %q, want the empty string", h.name, got)
			}
			got := h.span("a ` b `` c")
			if !strings.HasPrefix(got, "```") || !strings.HasSuffix(got, "```") {
				t.Errorf("%s() = %q, want a fence longer than the run inside", h.name, got)
			}
			if lines := h.span("a\r\nb\rc"); strings.ContainsAny(lines, "\r\n") {
				t.Errorf("%s() = %q, want every line ending collapsed", h.name, lines)
			}
		})
	}
}

// gfmPunctuation is the ASCII punctuation CommonMark lets a backslash escape.
const gfmPunctuation = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

// gfmTableCells splits one table row into its cells the way cmark-gfm does,
// which is the order that decides whether a pipe inside a code span survives:
// the row is split before any inline content is read. A backslash takes the
// byte after it along, so only a pipe no backslash took ends a cell, and every
// backslash-pipe pair in a cell is then replaced by a bare pipe, inside a code
// span as much as outside one.
func gfmTableCells(row string) []string {
	row = strings.TrimPrefix(strings.TrimSpace(row), "|")
	var cells []string
	var cell strings.Builder
	for i := 0; i < len(row); i++ {
		switch {
		case row[i] == '\\' && i+1 < len(row):
			cell.WriteString(row[i : i+2])
			i++
		case row[i] == '|':
			cells = append(cells, gfmUnescapePipes(strings.TrimSpace(cell.String())))
			cell.Reset()
		default:
			cell.WriteByte(row[i])
		}
	}
	if rest := strings.TrimSpace(cell.String()); rest != "" {
		cells = append(cells, gfmUnescapePipes(rest))
	}
	return cells
}

// gfmUnescapePipes is cmark-gfm's unescape_pipes: every backslash directly in
// front of a pipe is dropped, with no regard for a backslash in front of it.
func gfmUnescapePipes(cell string) string {
	return strings.ReplaceAll(cell, `\|`, "|")
}

// gfmInline renders the inline text a reader sees, for the two constructs the
// helpers under test write: a backslash escape, which a code span does not
// process, and a code span, whose content is shown as it stands. Everything
// else is taken as literal text, which is why the cases below hold no
// emphasis.
func gfmInline(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\\' && i+1 < len(s) && strings.IndexByte(gfmPunctuation, s[i+1]) >= 0:
			out.WriteByte(s[i+1])
			i += 2
		case s[i] == '`':
			content, next := gfmCodeSpanAt(s, i)
			out.WriteString(content)
			i = next
		default:
			out.WriteByte(s[i])
			i++
		}
	}
	return out.String()
}

// gfmCodeSpanAt reads the code span a backtick run at i opens: the content up
// to the next run of the same length, with one space stripped from each end
// when both ends have one. A run nothing closes is literal backticks.
func gfmCodeSpanAt(s string, i int) (content string, next int) {
	n := backtickRunAt(s, i)
	for j := i + n; j < len(s); {
		if s[j] != '`' {
			j++
			continue
		}
		m := backtickRunAt(s, j)
		if m == n {
			content = s[i+n : j]
			if len(content) >= 2 && content[0] == ' ' && content[len(content)-1] == ' ' && strings.TrimSpace(content) != "" {
				content = content[1 : len(content)-1]
			}
			return content, j + m
		}
		j += m
	}
	return s[i : i+n], i + n
}

// backtickRunAt counts the backticks starting at i.
func backtickRunAt(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == '`' {
		n++
	}
	return n
}

// spanValues are values a code span has to carry as sent, each chosen for the
// pipe or the backslash in it.
var spanValues = []string{
	"a|b",
	`a\|b`,
	`a\\|b`,
	`C:\dir|x`,
	`trailing\`,
	"a`|b",
	`\|`,
	"`x\\|y`",
	`*a*\|b`,
	"`leading|tick",
	"trailing|tick`",
}

// TestMdCodeSpan_ShowsTheValueAsSentInAListItem pins the defect a list row had:
// a card row is a list item, not a table cell, and a backslash in front of a
// pipe inside a code span there is shown, because a code span processes no
// backslash escape. The value is rendered as a reader sees it and compared
// with what was sent.
func TestMdCodeSpan_ShowsTheValueAsSentInAListItem(t *testing.T) {
	for _, value := range spanValues {
		t.Run(value, func(t *testing.T) {
			span := MdCodeSpan(value)
			if got := gfmInline(span); got != value {
				t.Errorf("MdCodeSpan(%q) = %q, which a list item shows as %q", value, span, got)
			}
		})
	}
}

// TestMdCodeSpanCell_KeepsTheCellWholeAndTheValueAsSent pins the defect a table
// cell had: the row is split before any code span is read, so a backslash
// already in front of a pipe takes the added one along and leaves the pipe
// live. Each value is placed in the middle of a three-cell row, which must
// still be three cells, the middle one showing the value as sent.
func TestMdCodeSpanCell_KeepsTheCellWholeAndTheValueAsSent(t *testing.T) {
	for _, value := range spanValues {
		t.Run(value, func(t *testing.T) {
			rendered := MdCodeSpanCell(value)
			cells := gfmTableCells("| before | " + rendered + " | after |")
			if len(cells) != 3 {
				t.Fatalf("MdCodeSpanCell(%q) = %q splits the row into %d cells %q, want 3", value, rendered, len(cells), cells)
			}
			if got := gfmInline(cells[1]); got != value {
				t.Errorf("MdCodeSpanCell(%q) = %q, which the cell shows as %q", value, rendered, got)
			}
		})
	}
}

// TestEscapeMdTableCell_KeepsABackslashedPipeInItsCell pins the same defect in
// the plain cell escaper: a backslash already in front of a pipe takes the one
// the escaper adds, so the pipe ends the cell. The row must stay three cells,
// and the value must read as sent both there and in a list item, where the
// same text lands through a card row.
func TestEscapeMdTableCell_KeepsABackslashedPipeInItsCell(t *testing.T) {
	for _, value := range []string{"a|b", `a\|b`, `a\\|b`, `\|`, `C:\dir|x`, `trailing\`} {
		t.Run(value, func(t *testing.T) {
			rendered := EscapeMdTableCell(value)
			cells := gfmTableCells("| before | " + rendered + " | after |")
			if len(cells) != 3 {
				t.Fatalf("EscapeMdTableCell(%q) = %q splits the row into %d cells %q, want 3", value, rendered, len(cells), cells)
			}
			if got := gfmInline(cells[1]); got != value {
				t.Errorf("EscapeMdTableCell(%q) = %q, which the cell shows as %q", value, rendered, got)
			}
			if got := gfmInline(rendered); got != value {
				t.Errorf("EscapeMdTableCell(%q) = %q, which a list item shows as %q", value, rendered, got)
			}
		})
	}
}

// inlineValues are values a list item or a paragraph has to show as sent: a
// pipe inside a code span the value carries is where the cell escaper's escape
// becomes a backslash the reader sees, since a list item splits on no pipe and
// a code span processes no escape.
var inlineValues = []struct{ value, want string }{
	{value: "a|b", want: "a|b"},
	{value: "a`|`b", want: "a|b"},
	{value: "``x|y``", want: "x|y"},
	{value: `a\|b`, want: "a|b"},
	{value: "one\ntwo\r\nthree", want: "one two three"},
}

// TestEscapeMdInline_ShowsTheValueAsSentInAListItem pins the list-item form of
// the cell escaper: every value stays on its line and reads as it would had
// it been written there by hand.
func TestEscapeMdInline_ShowsTheValueAsSentInAListItem(t *testing.T) {
	for _, tc := range inlineValues {
		t.Run(tc.value, func(t *testing.T) {
			rendered := EscapeMdInline(tc.value)
			if strings.ContainsAny(rendered, "\r\n") {
				t.Fatalf("EscapeMdInline(%q) = %q, want one line", tc.value, rendered)
			}
			if got := gfmInline("- " + rendered); got != "- "+tc.want {
				t.Errorf("EscapeMdInline(%q) = %q, which a list item shows as %q, want %q", tc.value, rendered, got, "- "+tc.want)
			}
		})
	}
}

// titleLinkCases are link titles and addresses whose pipe or backslash must
// reach the reader as sent, through the link path and through the fallback
// that shows an address no client should open.
var titleLinkCases = []struct{ name, title, url, want string }{
	{name: "a pipe in a linked title", title: "a|b", url: "https://example.org/", want: "[a|b](https://example.org/)"},
	{name: "a backslash-pipe in a linked title", title: `a\|b`, url: "https://example.org/", want: `[a\|b](https://example.org/)`},
	{name: "a pipe in an unlinkable address", title: "mirror", url: "ftp://example.org/a|b", want: "mirror ftp://example.org/a|b"},
	{name: "a backslash-pipe in an unlinkable address", title: "", url: `ftp://example.org/a\|b`, want: `ftp://example.org/a\|b`},
	// A backtick in the title beside the span used to pair with the span's
	// fence, so part of the address left the span and the containment around
	// an address no client should open was gone.
	{name: "a backtick in a title beside a span", title: "Mirror `x", url: "ftp://evil.example/a`b", want: "Mirror `x ftp://evil.example/a`b"},
	{name: "a backtick in a title with no address", title: "Mirror `x", url: "", want: "Mirror `x"},
	// A destination ending in a backslash used to escape the parenthesis that
	// closes it, and the link was lost with its label.
	{name: "a backslash ending a destination", title: "Mirror", url: `https://m.example/a\`, want: "[Mirror](https://m.example/a%5C)"},
}

// TestMdTitleLink_ShowsTheValueAsSentInAListItem is the list-item half for a
// link: neither the title nor a fallback address gains a backslash.
func TestMdTitleLink_ShowsTheValueAsSentInAListItem(t *testing.T) {
	for _, tc := range titleLinkCases {
		t.Run(tc.name, func(t *testing.T) {
			rendered := MdTitleLink(tc.title, tc.url)
			if got := gfmInline(rendered); got != tc.want {
				t.Errorf("MdTitleLink(%q, %q) = %q, which a list item shows as %q, want %q", tc.title, tc.url, rendered, got, tc.want)
			}
		})
	}
}

// TestMdTitleLinkCell_KeepsTheCellWholeAndTheValueAsSent is the table half for
// a link: the row stays one cell and the cell shows what was sent.
func TestMdTitleLinkCell_KeepsTheCellWholeAndTheValueAsSent(t *testing.T) {
	for _, tc := range titleLinkCases {
		t.Run(tc.name, func(t *testing.T) {
			rendered := MdTitleLinkCell(tc.title, tc.url)
			cells := gfmTableCells("| " + rendered + " |")
			if len(cells) != 1 {
				t.Fatalf("MdTitleLinkCell(%q, %q) = %q splits the row into %d cells %q, want 1", tc.title, tc.url, rendered, len(cells), cells)
			}
			if got := gfmInline(cells[0]); got != tc.want {
				t.Errorf("MdTitleLinkCell(%q, %q) = %q, which the cell shows as %q, want %q", tc.title, tc.url, rendered, got, tc.want)
			}
		})
	}
}

// TestMdAutolink_ShowsTheAddressOnceAndSafely verifies the shape for a value
// that is itself the address: it is clickable, it appears once, and an address
// no client should open is shown rather than linked.
func TestMdAutolink_ShowsTheAddressOnceAndSafely(t *testing.T) {
	got := MdAutolink("https://example.org/get?f=a(b)c")
	if got != "<https://example.org/get?f=a%28b%29c>" {
		t.Errorf("MdAutolink() = %q, want an autolink with the parentheses encoded", got)
	}
	if unsafe := MdAutolink("javascript:alert(1)"); strings.HasPrefix(unsafe, "<") {
		t.Errorf("MdAutolink(javascript:) = %q, want a code span rather than a live link", unsafe)
	}
}

// TestLinkableDestination_ReadsTheSchemeTheWayAClientDoes pins the difference
// between parsing the address and matching a prefix on it. A client parses, so
// a check that matched bytes could disagree with the renderer about the same
// value.
func TestLinkableDestination_ReadsTheSchemeTheWayAClientDoes(t *testing.T) {
	testCases := []struct {
		name string
		url  string
		want bool
	}{
		{name: "a scheme with no host is not an address", url: "https://", want: false},
		{name: "one slash is not an authority", url: "https:/example.org/x", want: false},
		{name: "the scheme is read case-insensitively", url: "HtTpS://example.org/x", want: true},
		{name: "a host with no path is still an address", url: "https://example.org", want: true},
		{name: "a scheme-relative address names no scheme", url: "//example.org/x", want: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LinkableDestination(tc.url); got != tc.want {
				t.Errorf("LinkableDestination(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

// TestWrapQuotedBody_ContainsAWholeBlockNotACharacter verifies what a quote
// takes away that an escaper cannot: a heading, a list item and a table row
// inside the body all stay inside it, so prose nobody checked cannot add a
// section to the document it was written into.
func TestWrapQuotedBody_ContainsAWholeBlockNotACharacter(t *testing.T) {
	got := WrapQuotedBody("First line.\n\n## Forged heading\n- forged item\n| a | b |")

	for line := range strings.SplitSeq(got, "\n") {
		t.Run(line, func(t *testing.T) {
			if !strings.HasPrefix(line, ">") {
				t.Errorf("WrapQuotedBody() left %q outside the quote", line)
			}
		})
	}
	if !strings.Contains(got, ">\n") {
		t.Errorf("WrapQuotedBody() = %q, want a blank body line quoted as a bare marker", got)
	}
}

// TestWrapQuotedBody_ReadsEveryLineEndingCommonMarkDoes pins the detail the
// containment rests on: a bare CR is a line ending too, so splitting on LF
// alone would leave whatever follows it outside the quote — and a heading
// there is structure, which is exactly what the quote prevents.
func TestWrapQuotedBody_ReadsEveryLineEndingCommonMarkDoes(t *testing.T) {
	testCases := []struct {
		name string
		body string
		want string
	}{
		{name: "empty", body: "", want: ""},
		{name: "one line", body: "plain", want: "> plain"},
		{name: "a bare CR", body: "a\r## b", want: "> a\n> ## b"},
		{name: "a CRLF", body: "a\r\n## b", want: "> a\n> ## b"},
		{name: "a control byte is dropped", body: "a\x00b", want: "> ab"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WrapQuotedBody(tc.body); got != tc.want {
				t.Errorf("WrapQuotedBody(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

// TestEndBlock_LeavesTheBuilderReadyForABlock verifies the rule every block
// write rests on: whatever the builder ends with, what follows opens a block
// rather than continuing the last line as a lazy paragraph.
func TestEndBlock_LeavesTheBuilderReadyForABlock(t *testing.T) {
	testCases := []struct {
		name   string
		before string
		want   string
	}{
		{name: "empty stays empty", before: "", want: ""},
		{name: "mid-line gets two", before: "text", want: "text\n\n"},
		{name: "a line end gets one", before: "text\n", want: "text\n\n"},
		{name: "a blank line is enough", before: "text\n\n", want: "text\n\n"},
		{name: "more than a blank line is left alone", before: "text\n\n\n", want: "text\n\n\n"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			b.WriteString(tc.before)
			EndBlock(&b)
			if got := b.String(); got != tc.want {
				t.Errorf("EndBlock(%q) left %q, want %q", tc.before, got, tc.want)
			}
		})
	}
}
