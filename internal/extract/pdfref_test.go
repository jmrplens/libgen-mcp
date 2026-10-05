package extract

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

// TestLeadingRef reads a reference at the start of a value's text, the way a
// dictionary or an array prints one before what follows it, and refuses what
// only looks like one: two numbers that are not followed by R, an R that is
// part of a word, and a number or generation too large for a reference.
func TestLeadingRef(t *testing.T) {
	for _, tc := range []struct {
		text string
		want objRef
		n    int
	}{
		{"5 0 R", objRef{id: 5}, 5},
		{"12 3 R /Next 4 0 R", objRef{id: 12, gen: 3}, 6},
		{"7 0 R>>", objRef{id: 7}, 5},
		{"7 0 R]", objRef{id: 7}, 5},
		{"4294967295 65535 R", objRef{id: 4294967295, gen: 65535}, 18},
		{"4294967296 0 R", objRef{}, 0},
		{"1 65536 R", objRef{}, 0},
		{"5 0 Rx", objRef{}, 0},
		{"3 /First 5 0 R", objRef{}, 0},
		{"1 0 0 R", objRef{}, 0},
		{"/R 5 0 R", objRef{}, 0},
		{"", objRef{}, 0},
	} {
		t.Run(tc.text, func(t *testing.T) {
			if got, n := leadingRef(tc.text); got != tc.want || n != tc.n {
				t.Errorf("leadingRef = %+v %d, want %+v %d", got, n, tc.want, tc.n)
			}
		})
	}
}

// refLines writes a map of references as "key=id.gen", sorted, so a whole map
// compares as one string.
func refLines[K comparable](refs map[K]objRef) string {
	var lines []string
	for k, ref := range refs {
		lines = append(lines, fmt.Sprintf("%v=%d.%d", k, ref.id, ref.gen))
	}
	slices.Sort(lines)
	return strings.Join(lines, " ")
}

// TestDictRefs reads the references of the catalog's /T in a built file, a
// dictionary holding every kind of value beside them: the first and the last
// key a reference, a title whose text says "/Next 9 0 R", a name holding a
// space, a dictionary in place holding a /Next of its own, a null, and an
// array that opens with a reference. Only the dictionary's own references are
// read, each under its own key.
func TestDictRefs(t *testing.T) {
	for _, tc := range []struct {
		name string
		dict string
		want string
	}{
		{"every kind of value", "<</A 3 0 R/B(/Next 9 0 R)/C/one#20two/D<</Next 9 0 R>>/E null/F[4 0 R/Fit]/G 1.5/H true/Z 2 1 R>>", "A=3.0 Z=2.1"},
		{"no references", "<</Count 3/Title(x)>>", ""},
		{"only references", "<</First 3 0 R/Last 4 0 R>>", "First=3.0 Last=4.0"},
		{"empty", "<<>>", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := readerFor(t, buildPDF([]string{"<</Type/Catalog/T " + tc.dict + ">>"})).Trailer().Key("Root").Key("T")
			if got := refLines(dictRefs(v)); got != tc.want {
				t.Errorf("dictRefs(%s) = %q, want %q", v, got, tc.want)
			}
		})
	}
}

// TestArrayRefs reads the references of the catalog's /T array in a built
// file: one at either end, a number before one, which must not be read as
// part of it, a string and an array in place between them, and none.
func TestArrayRefs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		array string
		want  string
	}{
		{"references and values", "[3 0 R (a b) [4 0 R/Fit] 1 5 0 R]", "0=3.0 4=5.0"},
		{"names and references", "[(one) 3 0 R (two) 4 0 R]", "1=3.0 3=4.0"},
		{"no references", "[0 0 612 792]", ""},
		{"empty", "[]", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := readerFor(t, buildPDF([]string{"<</Type/Catalog/T " + tc.array + ">>"})).Trailer().Key("Root").Key("T")
			if got := refLines(arrayRefs(v)); got != tc.want {
				t.Errorf("arrayRefs(%s) = %q, want %q", v, got, tc.want)
			}
		})
	}
}

// TestValueRefs follows a text form whose values print as direct says, and
// keeps what it read before the first place the text and the values part:
// a text that does not open as it should, a label that is not where it
// should be, and a value that does not print as the text has it.
func TestValueRefs(t *testing.T) {
	labels := []string{"/A ", "/B ", "/C "}
	direct := func(int) string { return "1" }
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{"whole", "<</A 3 0 R /B 1 /C 4 0 R>>", "0=3.0 2=4.0"},
		{"another opening", "[/A 3 0 R /B 1 /C 4 0 R]", ""},
		{"a label out of place", "<</A 3 0 R /X 1 /C 4 0 R>>", "0=3.0"},
		{"no space before a label", "<</A 3 0 R/B 1 /C 4 0 R>>", "0=3.0"},
		{"a value printed otherwise", "<</A 3 0 R /B 2 /C 4 0 R>>", "0=3.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := valueRefs(tc.text, "<<", labels, direct)
			if lines := refLines(got); lines != tc.want {
				t.Errorf("valueRefs = %q, want %q", lines, tc.want)
			}
		})
	}
}

// TestValueRefs_TheFirstValueHasNoSpace holds the first value to its label
// with no space before it, and every later one to a space, which is how a
// dictionary prints them.
func TestValueRefs_TheFirstValueHasNoSpace(t *testing.T) {
	got := valueRefs("[3 0 R 4 0 R]", "[", make([]string, 2), func(int) string { return "" })
	if want := map[int]objRef{0: {id: 3}, 1: {id: 4}}; !maps.Equal(got, want) {
		t.Errorf("valueRefs = %v, want %v", got, want)
	}
}
