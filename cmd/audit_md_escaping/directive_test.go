package main

import "testing"

// TestParseDirective_RequiresBothHalves pins the reason the mechanism exists.
// A declaration with no reason is not read as one at all, because the reason is
// what lets the next reader tell a value that needs no escaping from one
// somebody decided not to escape.
func TestParseDirective_RequiresBothHalves(t *testing.T) {
	testCases := []struct {
		name       string
		comment    string
		expression string
		reason     string
	}{
		{
			name:       "an expression and a reason",
			comment:    "//libgen:allow-unescaped r.Source: the source name is one of a closed set this server compiled in",
			expression: "r.Source",
			reason:     "the source name is one of a closed set this server compiled in",
		},
		{
			// Go's own directives are written with no space after the
			// slashes, and gofmt reflows a comment that has one; a
			// declaration that survived reformatting into prose would be an
			// exemption nobody could see had stopped working.
			name:    "a space after the slashes is prose, as it is for every Go directive",
			comment: "//  libgen:allow-unescaped   r.Source :  a reason  ",
		},
		{
			name:    "no reason is not a declaration",
			comment: "//libgen:allow-unescaped r.Source",
		},
		{
			name:    "an empty reason is not a declaration",
			comment: "//libgen:allow-unescaped r.Source:",
		},
		{
			name:    "no expression is not a declaration",
			comment: "//libgen:allow-unescaped : a reason",
		},
		{
			name:    "another directive is not this one",
			comment: "//nolint:gocognit // it reads better whole",
		},
		{
			name:    "ordinary prose is not a declaration",
			comment: "// the value is escaped: see below",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			expression, reason, ok := parseDirective(tc.comment)
			if ok != (tc.expression != "") {
				t.Fatalf("parseDirective(%q) accepted = %v, want %v", tc.comment, ok, tc.expression != "")
			}
			if expression != tc.expression {
				t.Errorf("parseDirective(%q) excused %q, want %q", tc.comment, expression, tc.expression)
			}
			if ok && reason != tc.reason {
				t.Errorf("parseDirective(%q) reason = %q, want %q", tc.comment, reason, tc.reason)
			}
		})
	}
}

// TestParseDirective_ReadsTheFirstColonAsTheSeparator pins what happens to a
// reason that carries a colon of its own: the split is on the first one, so
// the expression is what the report printed and the rest is the reason whole.
func TestParseDirective_ReadsTheFirstColonAsTheSeparator(t *testing.T) {
	expression, reason, ok := parseDirective("//libgen:allow-unescaped r.Kind: a closed set: book, article")
	if !ok || expression != "r.Kind" {
		t.Fatalf("parseDirective() excused %q (accepted = %v), want r.Kind", expression, ok)
	}
	if reason != "a closed set: book, article" {
		t.Errorf("parseDirective() reason = %q, want the whole rest of the line", reason)
	}
}
