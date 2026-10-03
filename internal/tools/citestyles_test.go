package tools

import (
	"encoding/json"
	"testing"

	"github.com/jmrplens/libgen-mcp/v2/internal/libgen"
)

// Two records the local styles are pinned against: a book as the catalog
// holds one, and an article whose DOI was confirmed.
var (
	localBook = citeFields{
		author: "Donald E. Knuth", title: "The Art of Computer Programming", year: "1997",
		publisher: "Addison-Wesley", address: "Reading, MA", edition: "3",
	}
	localArticle = citeFields{
		author: "Douglas Hanahan; Robert A. Weinberg", title: "Hallmarks of Cancer: The Next Generation",
		year: "2011", volume: "144", number: "5", startPg: "646", endPg: "674",
		doi: "10.1016/j.cell.2011.02.013", isArticle: true,
	}
)

// TestFormatLocal pins every style on both records. Each is checked by eye
// against the style's own published examples, and the point of pinning them is
// that an edit to one formatter cannot silently change another.
func TestFormatLocal(t *testing.T) {
	tests := []struct {
		style string
		f     citeFields
		want  string
	}{
		{"apa", localBook, "Knuth, D. E. (1997). The Art of Computer Programming (3rd ed.). Addison-Wesley."},
		{"apa", localArticle, "Hanahan, D., & Weinberg, R. A. (2011). Hallmarks of Cancer: The Next Generation. 144(5), 646-674. https://doi.org/10.1016/j.cell.2011.02.013"},
		{"mla", localBook, "Knuth, Donald E. The Art of Computer Programming. 3rd ed., Addison-Wesley, 1997."},
		{"mla", localArticle, "Hanahan, Douglas, and Robert A. Weinberg. \"Hallmarks of Cancer: The Next Generation.\" vol. 144, no. 5, 2011, pp. 646-674. https://doi.org/10.1016/j.cell.2011.02.013."},
		{"chicago", localBook, "Knuth, Donald E. 1997. The Art of Computer Programming. 3rd ed. Reading, MA: Addison-Wesley."},
		{"chicago", localArticle, "Hanahan, Douglas and Robert A. Weinberg. 2011. \"Hallmarks of Cancer: The Next Generation.\" 144 (5): 646-674. https://doi.org/10.1016/j.cell.2011.02.013."},
		{"harvard", localBook, "Knuth, D.E., 1997. The Art of Computer Programming. 3rd ed. Addison-Wesley, Reading, MA."},
		{"harvard", localArticle, "Hanahan, D., Weinberg, R.A., 2011. Hallmarks of Cancer: The Next Generation. 144, 646-674. https://doi.org/10.1016/j.cell.2011.02.013"},
		{"vancouver", localBook, "Knuth DE. The Art of Computer Programming. 3rd ed. Reading, MA: Addison-Wesley; 1997."},
		{"vancouver", localArticle, "Hanahan D, Weinberg RA. Hallmarks of Cancer: The Next Generation. 2011;144(5):646-674. https://doi.org/10.1016/j.cell.2011.02.013"},
		{"ieee", localBook, "D. E. Knuth, The Art of Computer Programming, 3rd ed. Reading, MA: Addison-Wesley, 1997."},
		{"ieee", localArticle, "D. Hanahan and R. A. Weinberg, \"Hallmarks of Cancer: The Next Generation,\" vol. 144, no. 5, pp. 646-674, 2011, doi: 10.1016/j.cell.2011.02.013."},
		{"unknown", localBook, ""},
	}
	for _, tc := range tests {
		t.Run(tc.style+"/"+tc.f.title, func(t *testing.T) {
			if got := formatLocal(tc.style, tc.f); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestFormatLocal_Sparse covers records that hold little: no author, no year,
// many authors, and an inverted name with no given part. Nothing absent may
// appear, and APA's own "n.d." is the one stand-in a style defines.
func TestFormatLocal_Sparse(t *testing.T) {
	many := citeFields{author: "A One; B Two; C Three; D Four; E Five; F Six; G Seven", title: "Big", year: "2020"}
	tests := []struct {
		name, style string
		f           citeFields
		want        string
	}{
		{"apa without author or year", "apa", citeFields{title: "Anonymous Tract", publisher: "Self"}, "Anonymous Tract. (n.d.). Self."},
		{"apa first edition unstated", "apa", citeFields{author: "Knuth", title: "TAOCP", year: "1968", edition: "1"}, "Knuth. (1968). TAOCP."},
		{"mla three or more", "mla", many, "One, A, et al. Big. 2020."},
		{"mla two", "mla", citeFields{author: "A One and B Two", title: "Pair"}, "One, A, and B Two. Pair."},
		{"vancouver cut at six", "vancouver", many, "One A, Two B, Three C, Four D, Five E, Six F, et al. Big. 2020."},
		{"ieee three", "ieee", citeFields{author: "A One; B Two; C Three", title: "Trio"}, "A. One, B. Two, and C. Three, Trio."},
		{"chicago three", "chicago", citeFields{author: "A One; B Two; C Three", title: "Trio", edition: "Revised edition"}, "One, A, B Two, and C Three. Trio. Revised edition."},
		{"harvard named edition", "harvard", citeFields{author: "Ann Lee", title: "T", edition: "Second"}, "Lee, A. T. Second ed."},
		{"title already punctuated", "apa", citeFields{title: "Why?", year: "2001"}, "Why? (2001)."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatLocal(tc.style, tc.f); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestLocalCSL checks the CSL-JSON built from a record: the catalog's
// classification decides the type, a numeric year becomes date-parts and any
// other year a literal, and an absent field is absent.
func TestLocalCSL(t *testing.T) {
	tests := []struct {
		name string
		f    citeFields
		want map[string]any
	}{
		{"article", localArticle, map[string]any{
			"type": "article-journal", "title": "Hallmarks of Cancer: The Next Generation",
			"issued": map[string]any{"date-parts": []any{[]any{2011.0}}}, "volume": "144", "issue": "5",
			"page": "646-674", "DOI": "10.1016/j.cell.2011.02.013",
			"author": []any{map[string]any{"family": "Hanahan", "given": "Douglas"}, map[string]any{"family": "Weinberg", "given": "Robert A."}},
		}},
		{"book with a literal year", citeFields{author: "Plato", title: "Republic", year: "c. 375 BC"}, map[string]any{
			"type": "book", "title": "Republic", "issued": map[string]any{"literal": "c. 375 BC"},
			"author": []any{map[string]any{"family": "Plato"}},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got map[string]any
			if err := json.Unmarshal([]byte(formatLocal(libgen.CiteStyleCSLJSON, tc.f)), &got); err != nil {
				t.Fatal(err)
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tc.want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("got  %s\nwant %s", gotJSON, wantJSON)
			}
		})
	}
}

// TestParseCitePerson covers the name shapes the catalog writes.
func TestParseCitePerson(t *testing.T) {
	tests := []struct {
		in, family, given, initials string
	}{
		{"Knuth, Donald E.", "Knuth", "Donald E.", "D. E."},
		{"Donald E. Knuth", "Knuth", "Donald E.", "D. E."},
		{"Plato", "Plato", "", ""},
		{"Knuth,", "Knuth,", "", ""},
		{"jean-paul sartre", "sartre", "jean-paul", "J."},
		{"'t Hooft, Gerard", "'t Hooft", "Gerard", "G."},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			p := parseCitePerson(tc.in)
			if p.family != tc.family || p.given != tc.given || p.initials(" ") != tc.initials {
				t.Errorf("got %+v initials %q", p, p.initials(" "))
			}
		})
	}
}

// TestOrdinal covers the English suffixes, including the teens.
func TestOrdinal(t *testing.T) {
	for n, want := range map[int]string{2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 13: "13th", 21: "21st", 22: "22nd", 23: "23rd", 101: "101st", 111: "111th"} {
		t.Run(want, func(t *testing.T) {
			if got := ordinal(n); got != want {
				t.Errorf("ordinal(%d) = %q", n, got)
			}
		})
	}
}
