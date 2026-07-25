package libgen

import "testing"

// TestNormalizeISBN verifies the accepted spellings of an ISBN collapse to the
// bare digit form and that anything not shaped like an ISBN is rejected, so a
// junk identifier never reaches a provider as a query.
func TestNormalizeISBN(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"9789286150616", "9789286150616"},
		{"978-92-86-15061-6", "9789286150616"},
		{" 978 92 86 15061 6 ", "9789286150616"},
		{"0306406152", "0306406152"},
		{"043942089x", "043942089X"},
		{"", ""},
		{"12345", ""},
		{"97892861506161", ""},           // 14 digits: too long
		{"9789286150X16", ""},            // X is only legal as an ISBN-10 check digit
		{"1234567890123", ""},            // 13 digits without a 978/979 prefix
		{"not-an-isbn-at-all-here", ""},  // plain junk
		{"10.2867/768526", ""},           // a DOI must never pass as an ISBN
		{"87a4ebdaf21fa6cc70009a3d", ""}, // nor an md5 fragment
	}
	for _, c := range cases {
		if got := NormalizeISBN(c.in); got != c.want {
			t.Errorf("NormalizeISBN(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestISBNMatches verifies the two ISBN spellings of the same book compare equal:
// a caller holding the ISBN-10 must match a record that only states the ISBN-13,
// otherwise a held book would be reported as not indexed.
func TestISBNMatches(t *testing.T) {
	if !isbnMatches("0306406152", "9780306406157") {
		t.Error("isbnMatches(ISBN-10, its ISBN-13) = false, want true")
	}
	if !isbnMatches("978-0-306-40615-7", "0306406152") {
		t.Error("isbnMatches(hyphenated ISBN-13, its ISBN-10) = false, want true")
	}
	if isbnMatches("0306406152", "9789286150616") {
		t.Error("isbnMatches(two different books) = true, want false")
	}
	if isbnMatches("", "9789286150616") || isbnMatches("9789286150616", "") {
		t.Error("isbnMatches with an empty side = true, want false")
	}
	if isbnMatches("junk", "junk") {
		t.Error("isbnMatches on two unparseable strings = true, want false")
	}
}

// TestISBN13Of verifies the ISBN-10 to ISBN-13 conversion, including the check
// digit recomputation, and that an already-13 ISBN passes through unchanged.
func TestISBN13Of(t *testing.T) {
	if got := isbn13Of("0306406152"); got != "9780306406157" {
		t.Errorf("isbn13Of(0306406152) = %q, want 9780306406157", got)
	}
	if got := isbn13Of("043942089X"); got != "9780439420891" {
		t.Errorf("isbn13Of(043942089X) = %q, want 9780439420891", got)
	}
	if got := isbn13Of("9789286150616"); got != "9789286150616" {
		t.Errorf("isbn13Of(ISBN-13) = %q, want it unchanged", got)
	}
	if got := isbn13Of("junk"); got != "" {
		t.Errorf("isbn13Of(junk) = %q, want empty", got)
	}
}
