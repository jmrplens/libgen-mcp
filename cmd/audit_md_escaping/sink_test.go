package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// segmentText renders a split template as one string per piece, so a case
// asserts on the order the pieces came out in.
func segmentText(segments []segment) []string {
	rendered := make([]string, 0, len(segments))
	for _, seg := range segments {
		if seg.verb != "" {
			rendered = append(rendered, seg.verb)
			continue
		}
		rendered = append(rendered, "«"+seg.text+"»")
	}
	return rendered
}

// TestSplitTemplate_KeepsVerbsAndOperandsLinedUp is what the whole cursor rests
// on: a hole is judged against the text written before it, so a verb counted
// wrong would judge every later value in the template against the wrong line.
func TestSplitTemplate_KeepsVerbsAndOperandsLinedUp(t *testing.T) {
	testCases := []struct {
		name     string
		template string
		want     []string
	}{
		{name: "text alone", template: "plain", want: []string{"«plain»"}},
		{name: "one verb", template: "| %s |", want: []string{"«| »", "%s", "« |»"}},
		{name: "a doubled percent is text", template: "100%% of %d", want: []string{"«100% of »", "%d", "«»"}},
		{name: "flags and width belong to the verb", template: "%-10.3f", want: []string{"«»", "%f", "«»"}},
		{name: "a star consumes an operand of its own", template: "%*d", want: []string{"«»", "%*", "%d", "«»"}},
		{name: "a template ending in a percent", template: "done %", want: []string{"«done »", "%v", "«»"}},
		{name: "two verbs keep their order", template: "- %s (p.%d)\n", want: []string{"«- »", "%s", "« (p.»", "%d", "«)\n»"}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := segmentText(splitTemplate(tc.template))
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("splitTemplate(%q) = %v, want %v", tc.template, got, tc.want)
			}
		})
	}
}

// parseExpr parses one expression for a test that asks a question about its
// shape alone.
func parseExpr(t *testing.T, source string) ast.Expr {
	t.Helper()
	expr, err := parser.ParseExpr(source)
	if err != nil {
		t.Fatalf("parse %q: %v", source, err)
	}
	return expr
}

// TestIndentText_OnlyAcceptsWhitespace verifies the one write the cursor reads
// through rather than over stays what it says it is: a repeat of anything but
// spaces or tabs is a value, not indentation.
func TestIndentText_OnlyAcceptsWhitespace(t *testing.T) {
	testCases := []struct {
		name   string
		source string
		want   string
	}{
		{name: "two spaces", source: `strings.Repeat("  ", n)`, want: "  "},
		{name: "a tab", source: `strings.Repeat("\t", n)`, want: "\t"},
		{name: "a dash is not indentation", source: `strings.Repeat("-", n)`, want: ""},
		{name: "an empty string is not indentation", source: `strings.Repeat("", n)`, want: ""},
		{name: "another function is not a repeat", source: `strings.Join(parts, " ")`, want: ""},
		{name: "a non-literal is not read", source: `strings.Repeat(pad, n)`, want: ""},
		{name: "a plain value is not a repeat", source: `r.Title`, want: ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := indentText(parseExpr(t, tc.source))
			if ok != (tc.want != "") || got != tc.want {
				t.Errorf("indentText(%s) = %q, %v, want %q", tc.source, got, ok, tc.want)
			}
		})
	}
}

// TestDestinationOf_NamesOneDocumentWhicheverWayItIsPassed pins the rule that
// lets a cursor follow a builder at all: these formatters write into &b and
// into b in the same function, and both are the same document.
func TestDestinationOf_NamesOneDocumentWhicheverWayItIsPassed(t *testing.T) {
	fset := token.NewFileSet()
	testCases := []struct {
		name   string
		source string
		want   string
	}{
		{name: "the address of a builder", source: "&b", want: "b"},
		{name: "the builder itself", source: "b", want: "b"},
		{name: "a field", source: "w.out", want: "w.out"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := destinationOf(fset, parseExpr(t, tc.source)); got != tc.want {
				t.Errorf("destinationOf(%s) = %q, want %q", tc.source, got, tc.want)
			}
		})
	}
}

// TestSafeVerbsExcludeTheQuotingOnes pins the verb table's one surprise: %q
// contains a newline and leaves a pipe a pipe, so a quoted value still ends
// the table cell it sits in.
func TestSafeVerbsExcludeTheQuotingOnes(t *testing.T) {
	testCases := []struct {
		verb string
		want bool
	}{
		{verb: "%d", want: false},
		{verb: "%t", want: false},
		{verb: "%x", want: false},
		{verb: "%s", want: true},
		{verb: "%v", want: true},
		{verb: "%q", want: true},
		{verb: verbWrite, want: true},
	}

	for _, tc := range testCases {
		t.Run(tc.verb, func(t *testing.T) {
			if got := (sinkHole{verb: tc.verb}).escapable(); got != tc.want {
				t.Errorf("escapable(%s) = %v, want %v", tc.verb, got, tc.want)
			}
		})
	}
}

// TestExprText_CollapsesToOneLine verifies a finding names its value the way an
// exemption is written against it: on one line, whatever the source's wrapping.
func TestExprText_CollapsesToOneLine(t *testing.T) {
	fset := token.NewFileSet()
	got := exprText(fset, parseExpr(t, "strings.Join(\n\tparts,\n\t\" | \",\n)"))
	if strings.Contains(got, "\n") || got != `strings.Join(parts, " | ")` {
		t.Errorf("exprText() = %q, want the call on one line", got)
	}
}
