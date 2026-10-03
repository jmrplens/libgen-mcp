package tools

import (
	"encoding/json"
	"testing"

	"github.com/jmrplens/libgen-mcp/v2/internal/libgen"
)

// The records the local styles are pinned against: a book as the catalog
// holds one, an article whose DOI was confirmed and whose journal Crossref
// named, and the same article with no journal known.
var (
	localBook = citeFields{
		author: "Donald E. Knuth", title: "The Art of Computer Programming", year: "1997",
		publisher: "Addison-Wesley", address: "Reading, MA", edition: "3",
	}
	localArticle = citeFields{
		author: "Douglas Hanahan; Robert A. Weinberg", title: "Hallmarks of Cancer: The Next Generation",
		year: "2011", volume: "144", number: "5", startPg: "646", endPg: "674",
		doi: "10.1016/j.cell.2011.02.013", container: "Cell", isArticle: true,
	}
	localArticleNoJournal = citeFields{
		author: "Douglas Hanahan; Robert A. Weinberg", title: "Hallmarks of Cancer: The Next Generation",
		year: "2011", volume: "144", number: "5", startPg: "646", endPg: "674",
		doi: "10.1016/j.cell.2011.02.013", isArticle: true,
	}
)

// TestFormatLocal pins every style on each record. Each is checked by eye
// against the style's own published examples, and the point of pinning them is
// that an edit to one formatter cannot silently change another.
func TestFormatLocal(t *testing.T) {
	tests := []struct {
		name, style string
		f           citeFields
		want        string
	}{
		{"book", "apa", localBook, "Knuth, D. E. (1997). The Art of Computer Programming (3rd ed.). Addison-Wesley."},
		{"article", "apa", localArticle, "Hanahan, D., & Weinberg, R. A. (2011). Hallmarks of Cancer: The Next Generation. Cell, 144(5), 646-674. https://doi.org/10.1016/j.cell.2011.02.013"},
		{"book", "mla", localBook, "Knuth, Donald E. The Art of Computer Programming. 3rd ed., Addison-Wesley, 1997."},
		{"article", "mla", localArticle, "Hanahan, Douglas, and Robert A. Weinberg. \"Hallmarks of Cancer: The Next Generation.\" Cell, vol. 144, no. 5, 2011, pp. 646-674. https://doi.org/10.1016/j.cell.2011.02.013."},
		{"book", "chicago", localBook, "Knuth, Donald E. 1997. The Art of Computer Programming. 3rd ed. Reading, MA: Addison-Wesley."},
		{"article", "chicago", localArticle, "Hanahan, Douglas and Robert A. Weinberg. 2011. \"Hallmarks of Cancer: The Next Generation.\" Cell 144 (5): 646-674. https://doi.org/10.1016/j.cell.2011.02.013."},
		{"book", "harvard", localBook, "Knuth, D.E., 1997. The Art of Computer Programming. 3rd ed. Addison-Wesley, Reading, MA."},
		{"article", "harvard", localArticle, "Hanahan, D., Weinberg, R.A., 2011. Hallmarks of Cancer: The Next Generation. Cell 144, 646-674. https://doi.org/10.1016/j.cell.2011.02.013"},
		{"book", "vancouver", localBook, "Knuth DE. The Art of Computer Programming. 3rd ed. Reading, MA: Addison-Wesley; 1997."},
		{"article", "vancouver", localArticle, "Hanahan D, Weinberg RA. Hallmarks of Cancer: The Next Generation. Cell. 2011;144(5):646-674. https://doi.org/10.1016/j.cell.2011.02.013"},
		{"book", "ieee", localBook, "D. E. Knuth, The Art of Computer Programming, 3rd ed. Reading, MA: Addison-Wesley, 1997."},
		{"article", "ieee", localArticle, "D. Hanahan and R. A. Weinberg, \"Hallmarks of Cancer: The Next Generation,\" Cell, vol. 144, no. 5, pp. 646-674, 2011, doi: 10.1016/j.cell.2011.02.013."},
		{"no journal", "apa", localArticleNoJournal, "Hanahan, D., & Weinberg, R. A. (2011). Hallmarks of Cancer: The Next Generation. https://doi.org/10.1016/j.cell.2011.02.013"},
		{"no journal", "vancouver", localArticleNoJournal, "Hanahan D, Weinberg RA. Hallmarks of Cancer: The Next Generation. 2011. https://doi.org/10.1016/j.cell.2011.02.013"},
		{"no journal", "ieee", localArticleNoJournal, "D. Hanahan and R. A. Weinberg, \"Hallmarks of Cancer: The Next Generation,\" 2011, doi: 10.1016/j.cell.2011.02.013."},
		{"book", "unknown", localBook, ""},
	}
	for _, tc := range tests {
		t.Run(tc.style+"/"+tc.name, func(t *testing.T) {
			if got := formatLocal(tc.style, tc.f); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestFormatLocal_Names covers the author fields the catalog writes that a
// naive split turns into invented initials: the trailing-initials form, an
// organization, a particle, a suffix, and a list nothing could split.
func TestFormatLocal_Names(t *testing.T) {
	tests := []struct {
		name, style, author, want string
	}{
		{
			"trailing initials in APA", "apa", "Knuth D.E., Graham R.L., Patashnik O.",
			"Knuth, D. E., Graham, R. L., & Patashnik, O. (1994). Concrete Mathematics.",
		},
		{
			"trailing initials in IEEE", "ieee", "Knuth D.E., Graham R.L., Patashnik O.",
			"D. E. Knuth, R. L. Graham, and O. Patashnik, Concrete Mathematics. 1994.",
		},
		{
			"trailing initials in Vancouver", "vancouver", "Knuth DE; Graham RL",
			"Knuth DE, Graham RL. Concrete Mathematics. 1994.",
		},
		{
			"the catalog's inverted list", "apa", "Kucsko, G.; Maurer, P. C.; Yao, N. Y. ",
			"Kucsko, G., Maurer, P. C., & Yao, N. Y. (1994). Concrete Mathematics.",
		},
		{
			"an organization stays whole", "apa", "World Health Organization",
			"World Health Organization. (1994). Concrete Mathematics.",
		},
		{
			"a suffix keeps the name whole", "ieee", "Martin Luther King Jr.",
			"Martin Luther King Jr., Concrete Mathematics. 1994.",
		},
		{
			"a particle keeps the name whole", "harvard", "Ludwig van Beethoven",
			"Ludwig van Beethoven, 1994. Concrete Mathematics.",
		},
		{
			"an unsplittable list stays one name", "apa", "Smith, John, Ann Lee",
			"Smith, John, Ann Lee. (1994). Concrete Mathematics.",
		},
		{
			"a lowercase name is not guessed at", "vancouver", "jean-paul sartre",
			"jean-paul sartre. Concrete Mathematics. 1994.",
		},
		{"an all-caps surname is not initials", "apa", "LEE J.", "LEE, J. (1994). Concrete Mathematics."},
		{"all-caps two-letter surnames", "apa", "WU X., LI Y.", "WU, X., & LI, Y. (1994). Concrete Mathematics."},
		{"a longer all-caps surname", "vancouver", "WANG H.; XU Q", "WANG H, XU Q. Concrete Mathematics. 1994."},
		{"et al. in APA", "apa", "Smith J., et al.", "Smith, J., et al. (1994). Concrete Mathematics."},
		{"et al. in MLA", "mla", "Smith J.; Jones K. et al", "Smith, J., et al. Concrete Mathematics. 1994."},
		{"et al. in Chicago", "chicago", "Smith J., Jones K., et al.", "Smith, J., K. Jones, et al. 1994. Concrete Mathematics."},
		{"et al. in Harvard", "harvard", "Smith J., et al.", "Smith, J., et al., 1994. Concrete Mathematics."},
		{"et al. in Vancouver", "vancouver", "Smith J., et al.", "Smith J, et al. Concrete Mathematics. 1994."},
		{"et al. in IEEE", "ieee", "Smith J., Jones K., et al.", "J. Smith et al., Concrete Mathematics. 1994."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := citeFields{author: tc.author, title: "Concrete Mathematics", year: "1994"}
			if got := formatLocal(tc.style, f); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestFormatLocal_Sparse covers records that hold little: no author, no year,
// many authors, and an inverted name with no given part. Nothing absent may
// appear, and APA's own "n.d." is the one stand-in a style defines.
func TestFormatLocal_Sparse(t *testing.T) {
	many := citeFields{author: "Ann One; Bo Two; Cy Three; Dee Four; Ed Five; Fay Six; Gus Seven", title: "Big", year: "2020"}
	tests := []struct {
		name, style string
		f           citeFields
		want        string
	}{
		{"apa without author or year", "apa", citeFields{title: "Anonymous Tract", publisher: "Self"}, "Anonymous Tract. (n.d.). Self."},
		{"apa first edition unstated", "apa", citeFields{author: "Knuth", title: "TAOCP", year: "1968", edition: "1"}, "Knuth. (1968). TAOCP."},
		{"mla three or more", "mla", many, "One, Ann, et al. Big. 2020."},
		{"mla two", "mla", citeFields{author: "Ann One and Bo Two", title: "Pair"}, "One, Ann, and Bo Two. Pair."},
		{"vancouver cut at six", "vancouver", many, "One A, Two B, Three C, Four D, Five E, Six F, et al. Big. 2020."},
		{"ieee three", "ieee", citeFields{author: "Ann One; Bo Two; Cy Three", title: "Trio"}, "A. One, B. Two, and C. Three, Trio."},
		{"chicago three", "chicago", citeFields{author: "Ann One; Bo Two; Cy Three", title: "Trio", edition: "Revised edition"}, "One, Ann, Bo Two, and Cy Three. Trio. Revised edition."},
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
// other year a literal, a name kept whole is a literal name, and an absent
// field is absent.
func TestLocalCSL(t *testing.T) {
	tests := []struct {
		name string
		f    citeFields
		want map[string]any
	}{
		{"article", localArticle, map[string]any{
			"type": "article-journal", "title": "Hallmarks of Cancer: The Next Generation", "container-title": "Cell",
			"issued": map[string]any{"date-parts": []any{[]any{2011.0}}}, "volume": "144", "issue": "5",
			"page": "646-674", "DOI": "10.1016/j.cell.2011.02.013",
			"author": []any{map[string]any{"family": "Hanahan", "given": "Douglas"}, map[string]any{"family": "Weinberg", "given": "Robert A."}},
		}},
		{"article with no journal", localArticleNoJournal, map[string]any{
			"type": "article-journal", "title": "Hallmarks of Cancer: The Next Generation",
			"issued": map[string]any{"date-parts": []any{[]any{2011.0}}}, "DOI": "10.1016/j.cell.2011.02.013",
			"author": []any{map[string]any{"family": "Hanahan", "given": "Douglas"}, map[string]any{"family": "Weinberg", "given": "Robert A."}},
		}},
		{"book with a literal year", citeFields{author: "Plato", title: "Republic", year: "c. 375 BC"}, map[string]any{
			"type": "book", "title": "Republic", "issued": map[string]any{"literal": "c. 375 BC"},
			"author": []any{map[string]any{"literal": "Plato"}},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got map[string]any
			if err := json.Unmarshal([]byte(formatLocal(libgen.CiteStyleCSLJSON, tc.f)), &got); err != nil {
				t.Fatal(err)
			}
			gotJSON, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, err := json.Marshal(tc.want)
			if err != nil {
				t.Fatal(err)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("got  %s\nwant %s", gotJSON, wantJSON)
			}
		})
	}
}

// TestParseCitePerson covers the name shapes the catalog writes: the three it
// splits, and the ones it keeps whole rather than guess at.
func TestParseCitePerson(t *testing.T) {
	tests := []struct {
		in, family, given, literal, initials string
	}{
		{"Knuth, Donald E.", "Knuth", "Donald E.", "", "D. E."},
		{"Donald E. Knuth", "Knuth", "Donald E.", "", "D. E."},
		{"Knuth D.E.", "Knuth", "D.E.", "", "D. E."},
		{"Knuth DE", "Knuth", "DE", "", "D. E."},
		{"LEE J.", "LEE", "J.", "", "J."},
		{"WU X.", "WU", "X.", "", "X."},
		{"LI Y", "LI", "Y", "", "Y."},
		{"WANG", "", "", "WANG", ""},
		{"Wang LEE", "LEE", "Wang", "", "W."},
		{"Sartre J.-P.", "Sartre", "J.-P.", "", "J. P."},
		{"Van Der Berg A.", "Van Der Berg", "A.", "", "A."},
		{"Wei Li", "Li", "Wei", "", "W."},
		{"'t Hooft, Gerard", "'t Hooft", "Gerard", "", "G."},
		{"Plato", "", "", "Plato", ""},
		{"DE", "", "", "DE", ""},
		{"Knuth,", "", "", "Knuth,", ""},
		{"jean-paul sartre", "", "", "jean-paul sartre", ""},
		{"World Health Organization", "", "", "World Health Organization", ""},
		{"Martin Luther King Jr.", "", "", "Martin Luther King Jr.", ""},
		{"King, Martin Luther, Jr.", "", "", "King, Martin Luther, Jr.", ""},
		{"Ludwig van Beethoven", "", "", "Ludwig van Beethoven", ""},
		{"Beethoven, Ludwig van", "", "", "Beethoven, Ludwig van", ""},
		{"Anna Maria Lucia Rossi", "", "", "Anna Maria Lucia Rossi", ""},
		{"R2 Team", "", "", "R2 Team", ""},
		{"  ", "", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			p := parseCitePerson(tc.in)
			if p.family != tc.family || p.given != tc.given || p.literal != tc.literal || p.initials(" ") != tc.initials {
				t.Errorf("got %+v initials %q", p, p.initials(" "))
			}
		})
	}
}

// TestCitePersonForms checks that a name kept whole is written the same in
// every form.
func TestCitePersonForms(t *testing.T) {
	p := parseCitePerson("World Health Organization")
	for _, got := range []string{p.inverted(), p.natural(), p.withInitials(" "), p.vancouver(), p.initialsFirst()} {
		t.Run(got, func(t *testing.T) {
			if got != "World Health Organization" {
				t.Errorf("form = %q", got)
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
