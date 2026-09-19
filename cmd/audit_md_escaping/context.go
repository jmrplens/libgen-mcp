package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// mdContext is where in a Markdown document a value lands, which decides both
// whether it needs escaping and which helper is the right one.
type mdContext int

const (
	// ctxProse is a paragraph. A pipe means nothing there and a newline is
	// legal, so only a heading or a list marker at the start of a line would
	// change the document's structure, and neither can be typed mid-line.
	ctxProse mdContext = iota
	// ctxCell is between two pipes of a table row.
	ctxCell
	// ctxHeading is on a line that opens with one to six '#'.
	ctxHeading
	// ctxListItem is on a line that opens with a bullet or an ordered marker.
	ctxListItem
	// ctxLinkLabel is between '[' and the '](' that closes the label.
	ctxLinkLabel
	// ctxLinkDest is between '](' and the ')' that closes the destination.
	ctxLinkDest
	// ctxFence is inside a fenced code block the formatter opened with a
	// backtick run of its own, the info string of that opening fence included.
	// It is the one context decided by the writes around the hole rather than
	// by the line it sits on, because a fence is opened on one line and closed
	// on another.
	ctxFence
	// ctxCard is a card row a renderer wrote by hand: a line whose constant
	// text is "- **Label**: " or "**Label**: ". It is not a place a value
	// lands but a shape the constant text has, and it is reported as it is,
	// because the fix is the same whatever the value: toolutil.Card writes the
	// row, escapes the value by its shape, omits the row when there is nothing
	// to say, and separates itself from whatever came before.
	ctxCard
)

// structuralContexts are the contexts a value can change the shape of, in the
// order a report lists them. Prose is absent because a paragraph holds a pipe,
// an angle bracket and a newline without the document changing shape.
//
// They are what "all" selects. The staged context below is selectable by name
// and is deliberately not in this list.
var structuralContexts = []mdContext{ctxCell, ctxHeading, ctxListItem, ctxLinkLabel, ctxLinkDest, ctxFence}

// stagedContexts are the rules that are asked for by name, so a run that says
// "all" keeps the meaning it had before the rule existed and the Makefile
// stages a rule into the gate by naming it beside "all".
var stagedContexts = []mdContext{ctxCard}

// contextLabels name each context for the command line and for a report.
var contextLabels = map[mdContext]string{
	ctxProse:     "prose",
	ctxCell:      "table-cell",
	ctxHeading:   "heading",
	ctxListItem:  "list-item",
	ctxLinkLabel: "link-label",
	ctxLinkDest:  "link-destination",
	ctxFence:     "fence",
	ctxCard:      "card",
}

// String names the context for a report.
func (c mdContext) String() string {
	if label, ok := contextLabels[c]; ok {
		return label
	}
	return "prose"
}

// wants names the helper that belongs in this context, which is what a finding
// tells its reader to reach for.
func (c mdContext) wants() string {
	switch c {
	case ctxCell, ctxListItem:
		return "toolutil.EscapeMdTableCell"
	case ctxHeading:
		return "toolutil.EscapeMdHeading"
	case ctxLinkLabel, ctxLinkDest:
		return "toolutil.MdTitleLink"
	case ctxFence:
		return "toolutil.MarkdownFencedBlock"
	case ctxCard:
		return "toolutil.Card"
	default:
		return ""
	}
}

// shape reports whether this context judges what the constant text around a
// value looks like rather than what the value can do to it. Such a finding is
// the line, not the value, so nothing is classified for it.
func (c mdContext) shape() bool { return c == ctxCard }

// structural reports whether a value landing in this context can change the
// shape of the document around it rather than only its own text.
func (c mdContext) structural() bool { return c != ctxProse }

// allContexts is the value of -contexts that judges every structural context.
const allContexts = "all"

// selection is the set of contexts one run judges. Staging the sweep by
// context is a flag rather than a branch because the six contexts carry
// genuinely different strengths of claim: a raw value ends a table cell
// outright, while in a list item it costs the containment and a line break.
type selection struct {
	chosen map[mdContext]bool
	label  string
}

// judges reports whether this run judges values landing in c.
func (s selection) judges(c mdContext) bool {
	return c.structural() && s.chosen[c]
}

// contextNames lists the accepted -contexts values, for the flag's own help
// and for the error a wrong one produces.
func contextNames() string {
	names := make([]string, 0, len(structuralContexts)+len(stagedContexts))
	for _, c := range append(slices.Clone(structuralContexts), stagedContexts...) {
		names = append(names, c.String())
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// parseContexts turns the -contexts value into the set of contexts to judge.
//
// An unknown name is an error rather than an empty selection, because a
// misspelled context would otherwise read as a gate that passed.
func parseContexts(value string) (selection, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = allContexts
	}
	sel := selection{chosen: map[mdContext]bool{}}
	choose := func(c mdContext) {
		if sel.chosen[c] {
			return
		}
		sel.chosen[c] = true
		if sel.label != "" {
			sel.label += ", "
		}
		sel.label += c.String()
	}
	for name := range strings.SplitSeq(value, ",") {
		if err := chooseContext(strings.TrimSpace(name), choose); err != nil {
			return selection{}, err
		}
	}
	if len(sel.chosen) == 0 {
		return selection{}, fmt.Errorf("no Markdown context selected: expected %s, or %s", allContexts, contextNames())
	}
	return sel, nil
}

// chooseContext adds the contexts one name in the -contexts list stands for.
func chooseContext(name string, choose func(mdContext)) error {
	switch name {
	case "":
		return nil
	case allContexts:
		for _, c := range structuralContexts {
			choose(c)
		}
		return nil
	}
	for _, c := range append(slices.Clone(structuralContexts), stagedContexts...) {
		if c.String() == name {
			choose(c)
			return nil
		}
	}
	return fmt.Errorf("unknown Markdown context %q: expected %s, or %s", name, allContexts, contextNames())
}

// minFenceLength is the shortest backtick run that opens a fenced code block.
// A run of one or two backticks opens an inline code span instead, which ends
// at the end of its own line and so cannot swallow the rest of the document.
const minFenceLength = 3

// maxFenceIndent is how far a fence may be indented and still open a block.
// CommonMark allows three spaces; a fourth makes the line indented code.
const maxFenceIndent = 3

// valueMark stands for a value the audit cannot read, in the line a cursor is
// assembling. It is a character no formatter writes and that opens no
// construct, so a hole neither creates a table cell nor prevents one.
const valueMark = "�"

// docCursor is what the audit knows about the Markdown one destination is
// being assembled into, at one point in a function body: the text written so
// far on the current line, and whether a fenced code block is open.
//
// It is a value rather than a pointer so that saving and restoring it across a
// nested block is a copy.
type docCursor struct {
	fenceOpen bool
	fenceLen  int
	// line is the text written since the last newline. The empty string means
	// the next byte written begins a line, which is the only place a fence
	// marker counts and the only place a heading or a list marker does.
	line string
}

// writeText advances the cursor over literal text a formatter writes.
//
// The closing-fence rule is deliberately more permissive than CommonMark,
// which also demands the line hold nothing else: a cursor wrongly left open
// would report every later value in the function, and a cursor wrongly closed
// reports nothing, so the error is taken in the direction that invents no
// finding.
func (c docCursor) writeText(text string) docCursor {
	for text != "" {
		segment, rest, hasNewline := strings.Cut(text, "\n")
		if c.line == "" {
			c = c.mark(segment)
		}
		if !hasNewline {
			c.line += segment
			return c
		}
		c.line = ""
		text = rest
	}
	return c
}

// writeValue advances the cursor over a value the audit cannot read.
//
// The document's shape is taken to be the one the server wrote, so a value is
// text that opens and closes nothing: assuming otherwise would mean assuming
// the very breakout this rule exists to prevent, and every hole after the
// first would be judged against a document that never renders.
func (c docCursor) writeValue() docCursor {
	c.line += valueMark
	return c
}

// closed is the cursor for a destination the audit has stopped being able to
// follow, because something it does not read wrote into it. The fence is
// dropped rather than kept: a stale open fence would condemn every value
// written after it.
func (c docCursor) closed() docCursor {
	return docCursor{line: c.line}
}

// mark applies the fence marker a line opens with, if it opens with one.
func (c docCursor) mark(line string) docCursor {
	run, ok := fenceMarker(line)
	if !ok {
		return c
	}
	if !c.fenceOpen {
		return docCursor{fenceOpen: true, fenceLen: run, line: c.line}
	}
	if run >= c.fenceLen {
		return docCursor{line: c.line}
	}
	return c
}

// fenceMarker reads the backtick run a line opens with, after the indentation
// CommonMark allows in front of one.
func fenceMarker(line string) (run int, ok bool) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > maxFenceIndent {
		return 0, false
	}
	for indent+run < len(line) && line[indent+run] == '`' {
		run++
	}
	if run < minFenceLength {
		return 0, false
	}
	return run, true
}

// context is the Markdown construct a value written now would land in.
//
// A fence answers before the line does, because a value inside a fenced block
// is contained whatever the line around it looks like — and, when it is not,
// it is the fence it breaks out of that matters.
func (c docCursor) context() mdContext {
	if c.fenceOpen {
		return ctxFence
	}
	return contextOf(c.line)
}

// contextOf classifies the Markdown context a value lands in, given the text
// written before it on its line.
//
// The line is what decides it, because every Markdown construct this audit
// cares about is a line-level one: a table row, a heading and a list item are
// each recognized by how their line opens. A link is looked for on the line as
// well, since a link written across two lines is not a link.
func contextOf(before string) mdContext {
	if ctx, ok := linkContext(before); ok {
		return ctx
	}
	opening := strings.TrimLeft(before, " \t")
	switch {
	case strings.HasPrefix(opening, "|"):
		return ctxCell
	case headingPrefix(opening):
		return ctxHeading
	case listPrefix(opening):
		return ctxListItem
	default:
		return ctxProse
	}
}

// linkContext reports whether the text before the hole leaves it inside a
// Markdown link, and in which half.
//
// The scan is over the unclosed brackets on the line: a hole after a '[' that
// nothing has closed is in a label, and a hole after the '](' that closed one
// is in a destination until the ')' arrives.
func linkContext(before string) (mdContext, bool) {
	label := strings.LastIndex(before, "[")
	if label < 0 {
		return ctxProse, false
	}
	rest := before[label:]
	_, destination, closed := strings.Cut(rest, "](")
	if !closed {
		// The label is still open, unless a lone ']' already ended it without
		// opening a destination, which is not a link at all.
		if strings.Contains(rest, "]") {
			return ctxProse, false
		}
		return ctxLinkLabel, true
	}
	if strings.Contains(destination, ")") {
		return ctxProse, false
	}
	return ctxLinkDest, true
}

// headingPrefix reports whether a line opens an ATX heading: one to six '#'
// followed by a space, which is what CommonMark requires.
func headingPrefix(opening string) bool {
	hashes := 0
	for hashes < len(opening) && opening[hashes] == '#' {
		hashes++
	}
	if hashes == 0 || hashes > 6 {
		return false
	}
	return hashes < len(opening) && (opening[hashes] == ' ' || opening[hashes] == '\t')
}

// listPrefix reports whether a line opens a list item, bulleted or ordered.
func listPrefix(opening string) bool {
	if len(opening) >= 2 && strings.IndexByte("-*+", opening[0]) >= 0 && opening[1] == ' ' {
		return true
	}
	digits := 0
	for digits < len(opening) && opening[digits] >= '0' && opening[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits+1 >= len(opening) {
		return false
	}
	return (opening[digits] == '.' || opening[digits] == ')') && opening[digits+1] == ' '
}
