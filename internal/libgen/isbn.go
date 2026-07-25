package libgen

import "strings"

// isbn10Len and isbn13Len are the two legal ISBN lengths once separators are
// stripped: ten characters (nine digits plus a digit-or-X check character) and
// thirteen digits.
const (
	isbn10Len = 10
	isbn13Len = 13
)

// isbn13Prefixes are the two Bookland EAN prefixes an ISBN-13 must start with. A
// thirteen-digit number that begins with anything else is some other product code
// (an EAN, a UPC), not an ISBN, and querying a book provider with it wastes a
// request on a guaranteed miss.
var isbn13Prefixes = []string{"978", "979"}

// NormalizeISBN reduces an ISBN to its bare, comparable form — separators removed
// and a trailing check character uppercased — returning "" when the input is not
// shaped like an ISBN at all.
//
// It validates SHAPE, not the check digit: a ten- or thirteen-character run of
// digits (with X allowed only as the ISBN-10 check character, and an ISBN-13
// required to carry a Bookland prefix). That is deliberately the weaker test. A
// mistyped check digit is indistinguishable from a catalog that recorded one
// wrong, and the sources verify the identifier against the record they find
// anyway, so rejecting on arithmetic would turn a recoverable near-miss into a
// hard error while catching nothing the record check does not already catch.
//
// It is exported because the tools layer validates the download tool's isbn
// argument with exactly this rule, and two spellings of "what counts as an ISBN"
// would let a value pass validation and then be rejected by every source.
func NormalizeISBN(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == 'x' || r == 'X':
			b.WriteRune('X')
		case r == '-' || r == ' ' || r == '\t':
			// Separators are decoration: ISBNs are printed hyphenated or spaced.
		default:
			return "" // any other character means this is not an ISBN
		}
	}
	s := b.String()
	switch len(s) {
	case isbn10Len:
		// X is legal only as the final check character of an ISBN-10.
		if strings.ContainsRune(s[:isbn10Len-1], 'X') {
			return ""
		}
		return s
	case isbn13Len:
		if strings.ContainsRune(s, 'X') {
			return ""
		}
		for _, p := range isbn13Prefixes {
			if strings.HasPrefix(s, p) {
				return s
			}
		}
		return ""
	default:
		return ""
	}
}

// isbn13Of converts a normalized ISBN to its thirteen-digit form: an ISBN-13 is
// returned unchanged, an ISBN-10 is re-prefixed with 978 and given a recomputed
// EAN check digit, and anything that is not an ISBN yields "". It exists so the
// two spellings of one book compare equal.
func isbn13Of(raw string) string {
	s := NormalizeISBN(raw)
	if len(s) != isbn10Len {
		return s // already thirteen digits, or not an ISBN at all
	}
	body := "978" + s[:isbn10Len-1]
	sum := 0
	for i, r := range body {
		d := int(r - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return body + string(rune('0'+(10-sum%10)%10))
}

// isbnMatches reports whether two ISBN spellings identify the same book, comparing
// them in their thirteen-digit form so an ISBN-10 matches its ISBN-13. Two values
// that are not ISBNs never match, so an empty or malformed pair is always false.
func isbnMatches(a, b string) bool {
	x, y := isbn13Of(a), isbn13Of(b)
	return x != "" && x == y
}
