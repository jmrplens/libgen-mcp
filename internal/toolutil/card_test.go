package toolutil

import (
	"strings"
	"testing"
)

// TestCard_AValueCannotReshapeTheCardItIsIn is the assertion the writer exists
// for: one value carrying every character that changes a Markdown document is
// rendered into a row, and the card is still one card of the rows it wrote.
func TestCard_AValueCannotReshapeTheCardItIsIn(t *testing.T) {
	const hostile = "Title | with a pipe\n# a heading of its own\n- and a row of its own\nclosing ) bracket"

	var b strings.Builder
	card := NewCard(&b, "Record")
	card.Field("Authors", hostile)
	card.Field("Year", "2020")
	got := b.String()

	testCases := []struct {
		name string
		want bool
		text string
	}{
		{name: "the heading is the one the card wrote", want: true, text: "## Record\n"},
		{name: "the hostile value is on its own row", want: true, text: "- **Authors**: Title \\| with a pipe"},
		{name: "the next field is still a row", want: true, text: "- **Year**: 2020\n"},
		{name: "no forged heading", want: false, text: "\n# a heading"},
		{name: "no forged row", want: false, text: "\n- and a row"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Contains(got, tc.text) != tc.want {
				t.Errorf("card = %q, contains %q = %v, want %v", got, tc.text, !tc.want, tc.want)
			}
		})
	}
	if rows := strings.Count(got, "\n- **"); rows != 2 {
		t.Errorf("card = %q has %d rows, want the two that were written", got, rows)
	}
}

// TestCard_AnAbsentValueIsNoRow verifies the rule that makes every optional
// field one line of code: a card shows what the source sent, and never a label
// with nothing after it.
func TestCard_AnAbsentValueIsNoRow(t *testing.T) {
	var b strings.Builder
	card := NewCard(&b, "")
	card.Field("Authors", "")
	card.Field("Year", "   ")
	card.Code("md5", "")
	card.Text("Description", "\n\n")
	card.Link("Record", "", "")
	card.URL("URL", "")
	card.Flag("Resumed", false)
	card.Secret("Key", "")
	card.Fence("bibtex", "  ")
	card.Quote("")
	card.FieldOr("Expires", "", "")

	if got := b.String(); got != "" {
		t.Errorf("a card of absent values wrote %q, want nothing", got)
	}
}

// TestCard_FieldOrWritesTheAbsenceWhenItIsTheAnswer verifies the row for a
// field whose blank is a fact rather than a gap: an expiry that never comes, a
// quota that is unlimited. A blank replacement is still no row.
func TestCard_FieldOrWritesTheAbsenceWhenItIsTheAnswer(t *testing.T) {
	testCases := []struct {
		name          string
		value, absent string
		want          string
	}{
		{name: "the value when there is one", value: "2026-01-01", absent: "never", want: "- **Expires**: 2026-01-01\n"},
		{name: "the absence when there is not", value: "", absent: "never", want: "- **Expires**: never\n"},
		{name: "nothing when neither says anything", value: "  ", absent: "", want: ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			NewCard(&b, "").FieldOr("Expires", tc.value, tc.absent)
			if got := b.String(); got != tc.want {
				t.Errorf("card = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCard_FlagIsARowOrNoRow verifies the shape for a condition worth stating
// only when it holds: a flag that is false is not a row that says "no", it is
// no row.
func TestCard_FlagIsARowOrNoRow(t *testing.T) {
	var b strings.Builder
	card := NewCard(&b, "")
	card.Flag("Resumed from a partial download", true)
	card.Flag("Trimmed by max_depth", false)

	if got := b.String(); got != "- **Resumed from a partial download**\n" {
		t.Errorf("card = %q, want the flag that holds and no other row", got)
	}
}

// TestCard_TheSameSecretIsAnnouncedOnce verifies the guidance does not repeat
// itself when one label is written twice.
func TestCard_TheSameSecretIsAnnouncedOnce(t *testing.T) {
	var b strings.Builder
	card := NewCard(&b, "")
	card.Secret("Per-call key", "sk-one")
	card.Secret("Per-call key", "sk-two")
	card.End()

	if count := strings.Count(b.String(), "is shown once and is not stored"); count != 1 {
		t.Errorf("card = %q says it %d times, want once", b.String(), count)
	}
}

// TestCard_IntIsAnAnswerAndCountIsAnAbsence pins the distinction the two
// numeric rows exist for: a zero-byte file is a fact worth a row, and a
// citation count nobody reported is not.
func TestCard_IntIsAnAnswerAndCountIsAnAbsence(t *testing.T) {
	testCases := []struct {
		name  string
		write func(*Card)
		want  string
	}{
		{name: "Int writes a zero", write: func(c *Card) { c.Int("Size in bytes", 0) }, want: "- **Size in bytes**: 0\n"},
		{name: "Int writes a number", write: func(c *Card) { c.Int("Size in bytes", 42) }, want: "- **Size in bytes**: 42\n"},
		{name: "Count omits a zero", write: func(c *Card) { c.Count("Times cited", 0) }, want: ""},
		{name: "Count writes a number", write: func(c *Card) { c.Count("Times cited", 7) }, want: "- **Times cited**: 7\n"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			tc.write(NewCard(&b, ""))
			if got := b.String(); got != tc.want {
				t.Errorf("card = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCard_TextQuotesABodyThatRunsToLines verifies the one row whose shape
// depends on its value: a one-line body stays on the row, and a longer one
// becomes a quote that nothing inside can break out of.
func TestCard_TextQuotesABodyThatRunsToLines(t *testing.T) {
	t.Run("one line stays inline", func(t *testing.T) {
		var b strings.Builder
		NewCard(&b, "").Text("Description", "A classic.\n")
		if got := b.String(); got != "- **Description**: A classic.\n" {
			t.Errorf("card = %q, want the body on the row", got)
		}
	})

	t.Run("a longer body is quoted line by line", func(t *testing.T) {
		var b strings.Builder
		NewCard(&b, "").Text("Description", "First paragraph.\r\n## Forged heading\n- forged item")
		got := b.String()
		for _, want := range []string{"- **Description**:\n", "  > First paragraph.\n", "  > ## Forged heading\n", "  > - forged item\n"} {
			t.Run(want, func(t *testing.T) {
				if !strings.Contains(got, want) {
					t.Errorf("card = %q, want it to contain %q", got, want)
				}
			})
		}
		if strings.Contains(got, "\n## Forged") || strings.Contains(got, "\n- forged") {
			t.Errorf("card = %q, want nothing in the body at the document's level", got)
		}
	})
}

// TestCard_SeparatesItselfFromWhatItDidNotWrite is what lets a renderer keep
// its seams: a card that follows a table, a quote or a fence opens a list of
// its own rather than continuing a foreign block, and consecutive rows stay
// one list.
func TestCard_SeparatesItselfFromWhatItDidNotWrite(t *testing.T) {
	var b strings.Builder
	b.WriteString("| a | b |\n| - | - |\n| 1 | 2 |\n")
	card := NewCard(&b, "")
	card.Field("Authors", "Ada")
	card.Field("Year", "1843")
	b.WriteString("Something else entirely.")
	card.Field("Publisher", "Taylor")
	got := b.String()

	if !strings.Contains(got, "| 1 | 2 |\n\n- **Authors**") {
		t.Errorf("card = %q, want a blank line between the table and the first row", got)
	}
	if !strings.Contains(got, "- **Authors**: Ada\n- **Year**: 1843\n") {
		t.Errorf("card = %q, want consecutive rows in one list", got)
	}
	if !strings.Contains(got, "Something else entirely.\n\n- **Publisher**") {
		t.Errorf("card = %q, want a row after a foreign write to open a block of its own", got)
	}
}

// TestCard_SecretIsShownOnceAndSaysSo verifies the member nothing calls yet:
// the value is contained in a code span, and the card's guidance says it is
// not stored. It is here so the safe form is the easy form the day something
// does call it.
func TestCard_SecretIsShownOnceAndSaysSo(t *testing.T) {
	var b strings.Builder
	card := NewCard(&b, "")
	card.Secret("Per-call key", "sk-abc|def")
	card.End("Call download again with the same identifier.")
	got := b.String()

	if !strings.Contains(got, "- **Per-call key**: `sk-abc\\|def`") {
		t.Errorf("card = %q, want the secret in a code span", got)
	}
	if !strings.Contains(got, "is shown once and is not stored") {
		t.Errorf("card = %q, want the guidance to say the value is not stored", got)
	}
	if !strings.Contains(got, "- Call download again with the same identifier.") {
		t.Errorf("card = %q, want the caller's own step kept", got)
	}
}

// TestCard_CodeAndLinkTakeTheirOwnContainment verifies each row reaches for the
// helper its value's shape needs rather than for one escaper everywhere.
func TestCard_CodeAndLinkTakeTheirOwnContainment(t *testing.T) {
	var b strings.Builder
	card := NewCard(&b, "")
	card.Code("md5", "abc`def")
	card.Link("Record", "A title", "https://example.org/x(y)")
	card.URL("URL", "javascript:alert(1)")
	got := b.String()

	testCases := []struct{ name, want string }{
		{name: "a code span outruns the backtick inside it", want: "- **md5**: ``abc`def``\n"},
		{name: "a link's destination is encoded", want: "- **Record**: [A title](https://example.org/x%28y%29)\n"},
		{name: "an address no client should open is shown, not linked", want: "- **URL**: `javascript:alert(1)`\n"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(got, tc.want) {
				t.Errorf("card = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// TestCard_SectionAndFenceOpenBlocksOfTheirOwn verifies the two block members:
// a section heading and a fenced body each separate themselves, and content
// inside the fence cannot close it.
func TestCard_SectionAndFenceOpenBlocksOfTheirOwn(t *testing.T) {
	var b strings.Builder
	card := NewCard(&b, "Record")
	card.Field("Year", "2020")
	section := card.Section("Citation (BibTeX)")
	section.Fence("bibtex", "@book{x,\n  title = {A ``` title}\n}")
	section.Quote("Built from catalog metadata.")
	got := b.String()

	if !strings.Contains(got, "- **Year**: 2020\n\n### Citation (BibTeX)\n\n") {
		t.Errorf("card = %q, want the section heading in a block of its own", got)
	}
	if !strings.Contains(got, "````bibtex\n") || !strings.Contains(got, "\n````\n") {
		t.Errorf("card = %q, want a fence longer than the run inside it", got)
	}
	if !strings.Contains(got, "\n> Built from catalog metadata.\n") {
		t.Errorf("card = %q, want the provenance quoted", got)
	}
}

// TestCard_AHeadingCannotBePromotedByItsOwnTitle verifies the heading is the
// level the card wrote it at, whatever the composed title starts with.
func TestCard_AHeadingCannotBePromotedByItsOwnTitle(t *testing.T) {
	var b strings.Builder
	NewCard(&b, "### Not a heading of its own\nsecond line")
	got := b.String()

	if !strings.HasPrefix(got, "## Not a heading of its own second line\n") {
		t.Errorf("card = %q, want one H2 with the hashes and the line break gone", got)
	}
	if strings.Count(got, "#") != 2 {
		t.Errorf("card = %q, want the two hashes the card wrote and no more", got)
	}
}
