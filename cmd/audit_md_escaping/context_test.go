package main

import "testing"

// TestContextOf_ClassifiesTheLineAValueLandsOn pins the line-level rule: every
// construct this audit judges is recognized by how its line opens, so the text
// written before a value is what decides which one it is in.
func TestContextOf_ClassifiesTheLineAValueLandsOn(t *testing.T) {
	testCases := []struct {
		name   string
		before string
		want   mdContext
	}{
		{name: "an empty line is prose", before: "", want: ctxProse},
		{name: "mid-sentence is prose", before: "Downloaded ", want: ctxProse},
		{name: "after a pipe is a cell", before: "| 1 | ", want: ctxCell},
		{name: "an indented pipe is still a cell", before: "   | ", want: ctxCell},
		{name: "after hashes and a space is a heading", before: "## ", want: ctxHeading},
		{name: "seven hashes is not a heading", before: "####### ", want: ctxProse},
		{name: "hashes with no space is not a heading", before: "##x", want: ctxProse},
		{name: "a bullet opens a list item", before: "- ", want: ctxListItem},
		{name: "an indented bullet opens a list item", before: "  - ", want: ctxListItem},
		{name: "an ordered marker opens a list item", before: "1. ", want: ctxListItem},
		{name: "a parenthesized ordinal opens a list item", before: "12) ", want: ctxListItem},
		{name: "a bullet mid-line is prose", before: "text - ", want: ctxProse},
		{name: "inside a link label", before: "see [", want: ctxLinkLabel},
		{name: "inside a link destination", before: "see [title](", want: ctxLinkDest},
		{name: "after a closed link is prose", before: "see [title](url) ", want: ctxProse},
		{name: "a lone bracket pair is not a link", before: "see [x] ", want: ctxProse},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := contextOf(tc.before); got != tc.want {
				t.Errorf("contextOf(%q) = %s, want %s", tc.before, got, tc.want)
			}
		})
	}
}

// TestDocCursor_FollowsAFenceAcrossWrites is the case the line alone cannot
// answer: a fence is opened by one write and closed by another, so a value
// written between them is contained whatever its own line looks like.
func TestDocCursor_FollowsAFenceAcrossWrites(t *testing.T) {
	cursor := docCursor{}.writeText("Citation\n\n```bibtex\n")
	if got := cursor.context(); got != ctxFence {
		t.Errorf("after opening a fence the context is %s, want %s", got, ctxFence)
	}
	cursor = cursor.writeValue().writeText("\n```\n")
	if got := cursor.context(); got != ctxProse {
		t.Errorf("after closing the fence the context is %s, want %s", got, ctxProse)
	}
}

// TestDocCursor_AShortFenceDoesNotCloseALongOne pins the CommonMark rule the
// escaper rests on: a closing run must be at least as long as the one that
// opened the block, so a shorter run inside it closes nothing.
func TestDocCursor_AShortFenceDoesNotCloseALongOne(t *testing.T) {
	cursor := docCursor{}.writeText("````\n").writeText("```\n")
	if !cursor.fenceOpen {
		t.Errorf("a run of three closed a fence of four")
	}
	if cursor = cursor.writeText("`````\n"); cursor.fenceOpen {
		t.Errorf("a run of five did not close a fence of four")
	}
}

// TestDocCursor_ClosedDropsTheFenceAndKeepsTheLine pins the direction the
// error is taken in when something the audit cannot read writes into the
// document: a stale open fence would condemn every value after it, so the
// fence goes and the line the writes had reached stays.
func TestDocCursor_ClosedDropsTheFenceAndKeepsTheLine(t *testing.T) {
	cursor := docCursor{}.writeText("```\n").writeText("- ").closed()
	if cursor.fenceOpen {
		t.Errorf("closed() kept the fence open")
	}
	if got := cursor.context(); got != ctxListItem {
		t.Errorf("closed() lost the line: context is %s, want %s", got, ctxListItem)
	}
}

// TestDocCursor_AValueOpensNothing pins the other half of that direction: a
// value is taken to be text that opens and closes no construct, because
// assuming otherwise would mean assuming the breakout this rule prevents.
func TestDocCursor_AValueOpensNothing(t *testing.T) {
	cursor := docCursor{}.writeValue().writeText("- ")
	if got := cursor.context(); got != ctxProse {
		t.Errorf("a bullet after a value is %s, want %s: the value is on that line", got, ctxProse)
	}
}

// TestParseContexts_RefusesAnUnknownName verifies a misspelled context is an
// error rather than an empty selection, which would read as a gate that
// passed.
func TestParseContexts_RefusesAnUnknownName(t *testing.T) {
	testCases := []struct {
		name    string
		value   string
		wantErr bool
		judges  mdContext
	}{
		{name: "all selects every structural context", value: "all", judges: ctxFence},
		{name: "the empty value means all", value: "", judges: ctxCell},
		{name: "one name selects one context", value: "table-cell", judges: ctxCell},
		{name: "an unknown name is refused", value: "paragraph", wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sel, err := parseContexts(tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseContexts(%q) accepted an unknown context", tc.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseContexts(%q) = %v", tc.value, err)
			}
			if !sel.judges(tc.judges) {
				t.Errorf("parseContexts(%q) does not judge %s", tc.value, tc.judges)
			}
			if sel.judges(ctxProse) {
				t.Errorf("parseContexts(%q) judges prose, which no selection may", tc.value)
			}
		})
	}
}

// TestSelectedContextsNameTheHelperTheyWant verifies every judged context tells
// its reader what to reach for: a finding that names no helper is a finding
// nobody can act on.
func TestSelectedContextsNameTheHelperTheyWant(t *testing.T) {
	for _, ctx := range structuralContexts {
		t.Run(ctx.String(), func(t *testing.T) {
			if ctx.wants() == "" {
				t.Errorf("%s names no helper", ctx)
			}
			if !ctx.structural() {
				t.Errorf("%s is in structuralContexts but does not report as structural", ctx)
			}
		})
	}
}
