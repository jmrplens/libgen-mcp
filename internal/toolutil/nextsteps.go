package toolutil

import "strings"

// The guidance block is the one part of a tool result that speaks in the
// server's own voice: it tells the model what call to make next. Everything
// above it is a record — a title somebody uploaded, a description a catalog
// stored, an error a mirror returned — and a model reading the document has
// nothing but the heading to tell the two apart.
//
// So the heading is treated as a marker rather than as decoration. A value
// carrying it is defused on the way in, and the block itself is written in one
// place, which is what makes "the server said this" a claim the document can
// support.
const (
	nextStepsHeading = "\U0001F4A1 **Next steps:**"
	// defusedNextStepsHeading is the heading with its glyph written as the
	// HTML entity for the same character. A Markdown client renders the two
	// identically, so a reader loses nothing and sees the text that really was
	// in the record; what changes is that the line is no longer the marker.
	defusedNextStepsHeading = "&#128161; **Next steps:**"
)

// DefuseNextStepsHeading rewrites every copy of the server's guidance heading
// in s so it can no longer pass for one. Text with no such heading is returned
// unchanged.
//
// Nothing in this server parses the block back out of the Markdown — the
// structured next_steps field is built separately — so this is not about a
// parser being fooled. It is about the reader: a book description that opens
// with the heading and continues with three bullets is, to a model reading the
// result, three instructions on the server's authority.
func DefuseNextStepsHeading(s string) string {
	if !strings.Contains(s, nextStepsHeading) {
		return s
	}
	return strings.ReplaceAll(s, nextStepsHeading, defusedNextStepsHeading)
}

// WriteNextSteps appends the guidance section to b: the "next steps" heading
// and one bullet per step. Nothing is written when no step survives, which is
// what an empty list means — not a heading with nothing under it.
//
// The section separates itself from whatever precedes it, so a renderer that
// stopped mid-line does not turn the heading into a continuation of its last
// paragraph. Each step goes through the cell escaper: a step is one line of a
// list by construction, and every builder of one quotes something a third
// party sent — a pinned source name, a resolved mirror URL, the path a file
// was saved under — so a value carrying a newline would end its bullet and
// write the rest as a step of its own.
//
// Whatever the steps are, a guidance heading already in the document is
// defused first. This is the one place every renderer passes through on its
// way to a result, which is what makes it the place to do it.
func WriteNextSteps(b *strings.Builder, steps ...string) {
	kept := make([]string, 0, len(steps))
	for _, step := range steps {
		if !blank(step) {
			kept = append(kept, step)
		}
	}
	if written := b.String(); DefuseNextStepsHeading(written) != written {
		b.Reset()
		b.WriteString(DefuseNextStepsHeading(written))
	}
	if len(kept) == 0 {
		return
	}
	EndBlock(b)
	b.WriteString(nextStepsHeading + "\n")
	for _, step := range kept {
		b.WriteString("- " + cardInline(step) + "\n")
	}
}
