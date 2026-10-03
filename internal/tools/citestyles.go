// Citation styles built locally from a record's own fields.

package tools

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/jmrplens/libgen-mcp/v2/internal/libgen"
)

// The styles here are the fallback for a record doi.org cannot format: a book
// found by md5 or ISBN, a DOI the catalog states but Crossref did not confirm,
// or a registry that did not answer. They follow the same rule buildCitations
// does: every part comes from a field the record holds, an absent field drops
// its part rather than being guessed at, and the DOI is the corroborated one or
// none. What they add is arrangement — order, punctuation and initials — and
// nothing a reader could mistake for a fact the record did not state.
//
// They are plain text. A style that sets the title in italics is written with
// the title unmarked, since the text lands in a code block and a JSON string,
// where markup would be literal characters.

// citePerson is one author split into the parts the styles arrange.
type citePerson struct {
	family, given string
}

// parseCitePerson splits a name as the catalog writes it. "Knuth, Donald E."
// is inverted at the comma, and "Donald E. Knuth" takes its last word as the
// family name. A one-word name is a family name alone, which is also how an
// organization is kept whole when it has no spaces to split on.
func parseCitePerson(name string) citePerson {
	name = strings.TrimSpace(name)
	if family, given, ok := strings.Cut(name, ","); ok && strings.TrimSpace(given) != "" {
		return citePerson{family: strings.TrimSpace(family), given: strings.TrimSpace(given)}
	}
	words := strings.Fields(name)
	if len(words) < 2 {
		return citePerson{family: name}
	}
	return citePerson{family: words[len(words)-1], given: strings.Join(words[:len(words)-1], " ")}
}

// initials returns the given names as initials, each followed by a period
// and separated by sep ("D. E." with a space, "D.E." without).
func (p citePerson) initials(sep string) string {
	words := strings.Fields(p.given)
	parts := make([]string, 0, len(words))
	for _, w := range words {
		if r := firstLetter(w); r != 0 {
			parts = append(parts, string(r)+".")
		}
	}
	return strings.Join(parts, sep)
}

// bareInitials returns the initials run together with no periods, the
// Vancouver form ("DE").
func (p citePerson) bareInitials() string {
	return strings.ReplaceAll(p.initials(""), ".", "")
}

// firstLetter is the first letter of w, 0 when it has none.
func firstLetter(w string) rune {
	for _, r := range w {
		if unicode.IsLetter(r) {
			return unicode.ToUpper(r)
		}
	}
	return 0
}

// inverted is "Family, Given", or the family name alone.
func (p citePerson) inverted() string {
	if p.given == "" {
		return p.family
	}
	return p.family + ", " + p.given
}

// natural is "Given Family", or the family name alone.
func (p citePerson) natural() string {
	return strings.TrimSpace(p.given + " " + p.family)
}

// withInitials is "Family, G. G." (sep " ") or "Family, G.G." (sep "").
func (p citePerson) withInitials(sep string) string {
	if ini := p.initials(sep); ini != "" {
		return p.family + ", " + ini
	}
	return p.family
}

// citePeople parses a record's author field into people, in order.
func citePeople(author string) []citePerson {
	names := splitAuthors(author)
	out := make([]citePerson, 0, len(names))
	for _, n := range names {
		if p := parseCitePerson(n); p.family != "" {
			out = append(out, p)
		}
	}
	return out
}

// serialAnd is the separator before the last of three or more names, with the
// serial comma the MLA, Chicago and IEEE styles all write.
const serialAnd = ", and "

// joinSerial joins names with sep and puts last before the final one: "A, B,
// and C" or, with two, "A and C" (last is used alone when sep would only
// separate two).
func joinSerial(names []string, sep, last, pair string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + pair + names[1]
	}
	return strings.Join(names[:len(names)-1], sep) + last + names[len(names)-1]
}

// editionText renders an edition for a reference: a bare number as an ordinal
// ("2" as "2nd ed."), a value that already names itself as it is.
func editionText(e string) string {
	e = strings.TrimSpace(e)
	if e == "" {
		return ""
	}
	n, err := strconv.Atoi(e)
	switch {
	case err == nil && n > 1:
		return ordinal(n) + " ed."
	case err == nil:
		// A first edition is not stated in a reference.
		return ""
	case strings.Contains(strings.ToLower(e), "ed"):
		return e
	default:
		return e + " ed."
	}
}

// ordinal renders n with its English suffix.
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}

// sentence ends s with a period unless it already ends in punctuation.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.ContainsRune(".?!", rune(s[len(s)-1])) {
		return s
	}
	return s + "."
}

// joinNonEmpty joins the non-empty parts with sep.
func joinNonEmpty(sep string, parts ...string) string {
	kept := parts[:0:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, strings.TrimSpace(p))
		}
	}
	return strings.Join(kept, sep)
}

// articlePages is an article's page range with a hyphen, or "".
func articlePages(f citeFields) string {
	switch {
	case f.startPg != "" && f.endPg != "":
		return f.startPg + "-" + f.endPg
	case f.startPg != "":
		return f.startPg
	default:
		return ""
	}
}

// doiURL is the DOI as a resolver URL, or "".
func doiURL(doi string) string {
	if doi == "" {
		return ""
	}
	return "https://doi.org/" + doi
}

// volumeIssue is "500(7460)", "500" or "".
func volumeIssue(f citeFields) string {
	if f.volume == "" {
		return ""
	}
	if f.number != "" {
		return f.volume + "(" + f.number + ")"
	}
	return f.volume
}

// placePublisher is "Place: Publisher", either alone, or "".
func placePublisher(f citeFields) string {
	return joinNonEmpty(": ", f.address, f.publisher)
}

// formatLocal renders one style from a record's fields, "" for a style it
// does not know.
func formatLocal(style string, f citeFields) string {
	switch style {
	case "apa":
		return localAPA(f)
	case "mla":
		return localMLA(f)
	case "chicago":
		return localChicago(f)
	case "harvard":
		return localHarvard(f)
	case "vancouver":
		return localVancouver(f)
	case "ieee":
		return localIEEE(f)
	case libgen.CiteStyleCSLJSON:
		return localCSL(f)
	}
	return ""
}

// localAPA follows APA 7: inverted names with initials, "&" before the last,
// the year in parentheses ("n.d." when the record has none), and the DOI as a
// URL.
func localAPA(f citeFields) string {
	people := citePeople(f.author)
	names := make([]string, len(people))
	for i, p := range people {
		names[i] = p.withInitials(" ")
	}
	year := f.year
	if year == "" {
		year = "n.d."
	}
	head := joinSerial(names, ", ", ", & ", ", & ")
	title := f.title
	if ed := editionText(f.edition); ed != "" && !f.isArticle {
		title += " (" + ed + ")"
	}
	var tail string
	if f.isArticle {
		tail = joinNonEmpty(", ", volumeIssue(f), articlePages(f))
	} else {
		tail = f.publisher
	}
	if head == "" {
		return joinNonEmpty(" ", sentence(title), "("+year+").", sentence(tail), doiURL(f.doi))
	}
	return joinNonEmpty(" ", sentence(head), "("+year+").", sentence(title), sentence(tail), doiURL(f.doi))
}

// localMLA follows MLA 9: the first author inverted, two joined by "and",
// three or more cut to "et al.".
func localMLA(f citeFields) string {
	people := citePeople(f.author)
	var head string
	switch {
	case len(people) == 1:
		head = people[0].inverted()
	case len(people) == 2:
		head = people[0].inverted() + serialAnd + people[1].natural()
	case len(people) > 2:
		head = people[0].inverted() + ", et al"
	}
	if f.isArticle {
		var vol string
		if f.volume != "" {
			vol = "vol. " + f.volume
		}
		var no string
		if f.number != "" {
			no = "no. " + f.number
		}
		var pp string
		if p := articlePages(f); p != "" {
			pp = "pp. " + p
		}
		return joinNonEmpty(" ", sentence(head), "\""+sentence(f.title)+"\"",
			sentence(joinNonEmpty(", ", vol, no, f.year, pp)), sentence(doiURL(f.doi)))
	}
	return joinNonEmpty(" ", sentence(head), sentence(f.title),
		sentence(joinNonEmpty(", ", editionText(f.edition), f.publisher, f.year)), sentence(doiURL(f.doi)))
}

// chicagoNames is the Chicago list: the first name inverted, the rest natural,
// "and" before the last.
func chicagoNames(people []citePerson) string {
	names := make([]string, len(people))
	for i, p := range people {
		if i == 0 {
			names[i] = p.inverted()
		} else {
			names[i] = p.natural()
		}
	}
	return joinSerial(names, ", ", serialAnd, " and ")
}

// localChicago follows Chicago author-date: names, year, title, then the
// place and publisher or the volume and pages.
func localChicago(f citeFields) string {
	head := chicagoNames(citePeople(f.author))
	if f.isArticle {
		vol := f.volume
		if f.number != "" {
			vol = joinNonEmpty(" ", vol, "("+f.number+")")
		}
		if p := articlePages(f); p != "" {
			vol += ": " + p
		}
		return joinNonEmpty(" ", sentence(head), sentence(f.year), "\""+sentence(f.title)+"\"",
			sentence(vol), sentence(doiURL(f.doi)))
	}
	return joinNonEmpty(" ", sentence(head), sentence(f.year), sentence(f.title),
		sentence(editionText(f.edition)), sentence(placePublisher(f)), sentence(doiURL(f.doi)))
}

// localHarvard follows Elsevier's Harvard, the style doi.org is asked for:
// inverted names with run-together initials, then the year after a comma.
func localHarvard(f citeFields) string {
	people := citePeople(f.author)
	names := make([]string, len(people))
	for i, p := range people {
		names[i] = p.withInitials("")
	}
	head := joinNonEmpty(", ", strings.Join(names, ", "), f.year)
	if f.isArticle {
		return joinNonEmpty(" ", sentence(head), sentence(f.title),
			sentence(joinNonEmpty(", ", f.volume, articlePages(f))), doiURL(f.doi))
	}
	return joinNonEmpty(" ", sentence(head), sentence(f.title), sentence(editionText(f.edition)),
		sentence(joinNonEmpty(", ", f.publisher, f.address)), doiURL(f.doi))
}

// vancouverMaxAuthors is how many authors Vancouver lists before "et al.".
const vancouverMaxAuthors = 6

// localVancouver follows the NLM form doi.org's Elsevier Vancouver writes:
// family names with bare initials, six before "et al.", and the year before
// volume and pages.
func localVancouver(f citeFields) string {
	people := citePeople(f.author)
	names := make([]string, 0, vancouverMaxAuthors+1)
	for i, p := range people {
		if i == vancouverMaxAuthors {
			names = append(names, "et al")
			break
		}
		names = append(names, strings.TrimSpace(p.family+" "+p.bareInitials()))
	}
	head := strings.Join(names, ", ")
	if f.isArticle {
		ref := f.year
		if f.volume != "" {
			ref += ";" + volumeIssue(f)
		}
		if p := articlePages(f); p != "" {
			ref += ":" + p
		}
		return joinNonEmpty(" ", sentence(head), sentence(f.title), sentence(ref), doiURL(f.doi))
	}
	return joinNonEmpty(" ", sentence(head), sentence(f.title), sentence(editionText(f.edition)),
		sentence(joinNonEmpty("; ", placePublisher(f), f.year)), doiURL(f.doi))
}

// localIEEE follows IEEE: initials before the family name, "and" before the
// last, the title in quotes for an article.
func localIEEE(f citeFields) string {
	people := citePeople(f.author)
	names := make([]string, len(people))
	for i, p := range people {
		names[i] = strings.TrimSpace(p.initials(" ") + " " + p.family)
	}
	head := joinSerial(names, ", ", serialAnd, " and ")
	if f.isArticle {
		var vol, no, pp, doi string
		if f.volume != "" {
			vol = "vol. " + f.volume
		}
		if f.number != "" {
			no = "no. " + f.number
		}
		if p := articlePages(f); p != "" {
			pp = "pp. " + p
		}
		if f.doi != "" {
			doi = "doi: " + f.doi
		}
		quoted := "\"" + f.title + ",\""
		return sentence(joinNonEmpty(" ", joinNonEmpty(", ", head, quoted), joinNonEmpty(", ", vol, no, pp, f.year, doi)))
	}
	return joinNonEmpty(" ", sentence(joinNonEmpty(", ", head, f.title, editionText(f.edition))),
		sentence(joinNonEmpty(", ", placePublisher(f), f.year)))
}

// localCSL renders the record as CSL-JSON from the fields it holds, with the
// type the catalog's own classification gives it.
func localCSL(f citeFields) string {
	item := libgen.CSLItem{"type": "book", "title": f.title}
	if f.isArticle {
		item["type"] = "article-journal"
	}
	people := citePeople(f.author)
	if len(people) > 0 {
		authors := make([]any, len(people))
		for i, p := range people {
			name := map[string]any{"family": p.family}
			if p.given != "" {
				name["given"] = p.given
			}
			authors[i] = name
		}
		item["author"] = authors
	}
	if y, err := strconv.Atoi(f.year); err == nil {
		item["issued"] = map[string]any{"date-parts": []any{[]any{y}}}
	} else if f.year != "" {
		item["issued"] = map[string]any{"literal": f.year}
	}
	for key, value := range map[string]string{
		"publisher": f.publisher, "publisher-place": f.address, "edition": f.edition,
		"volume": f.volume, "issue": f.number, "page": articlePages(f), "DOI": f.doi,
	} {
		if value != "" {
			item[key] = value
		}
	}
	return item.JSON()
}
