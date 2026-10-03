// Citation styles built locally from a record's own fields.

package tools

import (
	"regexp"
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
// nothing a reader could mistake for a fact the record did not state. A name
// they cannot split with confidence is written as the record has it (see
// citePerson), and an article whose journal is unknown loses its volume and
// pages rather than printing them with nothing to locate.
//
// They are plain text. A style that sets the title in italics is written with
// the title unmarked, since the text lands in a code block and a JSON string,
// where markup would be literal characters.

// citePerson is one author split into the parts the styles arrange, or, when
// the name could not be split with confidence, kept whole as literal.
//
// Splitting a name is a claim about which part is the family name, and every
// style that inverts or abbreviates a name repeats that claim in print. So a
// name is split only in the shapes that say which part is which, and anything
// else is written exactly as the record has it: "World Health Organization",
// "Martin Luther King Jr." and "Ludwig van Beethoven" stay as they are, rather
// than becoming "Organization, W. H.", "Jr., M. L. K." or "Beethoven, L. V.".
type citePerson struct {
	family, given string
	literal       string
}

// citeNameMaxWords is the most words a "Given Family" name may have and
// still be split at its last word. Past it, a run of capitalized words is as
// likely an organization or a compound name as a person.
const citeNameMaxWords = 3

// parseCitePerson splits a name in the three shapes that say which part is the
// family name, and keeps any other name whole:
//
//   - "Knuth, Donald E." is inverted at its one comma.
//   - "Knuth D.E." ends in initials, so what precedes them is the family name.
//   - "Donald E. Knuth", two or three capitalized words with no particle,
//     suffix or organization word among them, takes its last word as the family
//     name.
func parseCitePerson(name string) citePerson {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return citePerson{}
	}
	if family, given, ok := strings.Cut(name, ","); ok {
		family, given = strings.TrimSpace(family), strings.TrimSpace(given)
		// A second comma means a list splitAuthors could not take apart, or a
		// suffix ("King, Martin Luther, Jr."): neither is one family name.
		if family == "" || given == "" || strings.Contains(given, ",") || !plainNameWords(strings.Fields(given)) {
			return citePerson{literal: name}
		}
		return citePerson{family: family, given: given}
	}
	words := strings.Fields(name)
	switch n := trailingInitials(words); {
	case n > 0 && n < len(words):
		return citePerson{family: strings.Join(words[:len(words)-n], " "), given: strings.Join(words[len(words)-n:], " ")}
	case n >= 2 && n == len(words):
		// Every word could be initials ("WU X.", "LI Y"): a short all-caps
		// surname reads the same as two initials, and the catalog puts the
		// surname first in this form, so the first word is taken as it.
		return citePerson{family: words[0], given: strings.Join(words[1:], " ")}
	}
	if len(words) < 2 || len(words) > citeNameMaxWords || !plainNameWords(words) {
		return citePerson{literal: name}
	}
	return citePerson{family: words[len(words)-1], given: strings.Join(words[:len(words)-1], " ")}
}

// trailingInitials counts the words at the end of a name that are initials.
func trailingInitials(words []string) int {
	n := 0
	for i := len(words) - 1; i >= 0 && isInitials(words[i]); i-- {
		n++
	}
	return n
}

// isInitials reports whether w is a run of initials: "D.", "D.E.", "DE" or
// "D.-P.". Only capitals, periods and hyphens, so a capitalized surname such as
// "Li" is not one. A word with a period may hold up to four letters, and one
// without at most two: an all-caps surname ("LEE", "WANG") is three letters or
// more and has no periods, and it must not be read as initials.
func isInitials(w string) bool {
	letters, dotted := 0, false
	for _, r := range w {
		switch {
		case unicode.IsUpper(r):
			letters++
		case r == '.':
			dotted = true
		case r == '-':
		default:
			return false
		}
	}
	if dotted {
		return letters > 0 && letters <= 4
	}
	return letters > 0 && letters <= 2
}

// nameParticles are the lowercase words that join a family name ("van", "de")
// and the suffixes that follow one ("Jr."). A name holding one is kept whole:
// where the particle belongs, and whether the suffix is part of the name, is
// a convention that differs by language and by style.
var nameParticles = map[string]bool{
	"van": true, "von": true, "der": true, "den": true, "de": true, "del": true, "della": true,
	"di": true, "da": true, "du": true, "le": true, "la": true, "bin": true, "ibn": true,
	"al": true, "el": true, "ter": true, "ten": true, "dos": true, "das": true, "y": true,
	"jr": true, "sr": true, "ii": true, "iii": true, "iv": true,
}

// organizationWords mark a name as an organization's. Both spellings are
// listed where British and American English differ, because catalog names
// arrive in either.
var organizationWords = map[string]bool{
	"organization": true, "organisation": true, //nolint:misspell // the British spelling is matched on purpose
	"institute": true, "university": true,
	"association": true, "society": true, "committee": true, "council": true, "group": true,
	"consortium": true, "agency": true, "foundation": true, "ministry": true, "department": true,
	"center": true, "centre": true, //nolint:misspell // the British spelling is matched on purpose
	"board": true, "office": true, "inc": true, "ltd": true,
	"team": true, "project": true, "network": true, "collaboration": true, "commission": true,
	"bureau": true, "service": true, "laboratory": true, "academy": true, "press": true,
}

// plainNameWords reports whether every word looks like part of a personal
// name: capitalized, letters with at most periods, hyphens and apostrophes,
// and neither a particle, a suffix nor an organization word.
func plainNameWords(words []string) bool {
	for _, w := range words {
		key := strings.ToLower(strings.TrimRight(w, "."))
		if nameParticles[key] || organizationWords[key] || !nameShaped(w) {
			return false
		}
	}
	return true
}

// nameShaped reports whether w starts with a capital and holds only letters,
// periods, hyphens and apostrophes.
func nameShaped(w string) bool {
	r := []rune(w)
	if len(r) == 0 || !unicode.IsUpper(r[0]) {
		return false
	}
	for _, c := range r {
		if !unicode.IsLetter(c) && !strings.ContainsRune(".-'’", c) {
			return false
		}
	}
	return true
}

// initials returns the given names as initials, each followed by a period
// and separated by sep ("D. E." with a space, "D.E." without). A word that is
// already initials ("D.E.", "DE") gives each of its letters.
func (p citePerson) initials(sep string) string {
	var parts []string
	for w := range strings.FieldsSeq(p.given) {
		if isInitials(w) {
			for _, r := range w {
				if unicode.IsUpper(r) {
					parts = append(parts, string(r)+".")
				}
			}
			continue
		}
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

// inverted is "Family, Given", the family name alone, or the literal name.
func (p citePerson) inverted() string {
	switch {
	case p.literal != "":
		return p.literal
	case p.given == "":
		return p.family
	}
	return p.family + ", " + p.given
}

// natural is "Given Family", the family name alone, or the literal name.
func (p citePerson) natural() string {
	if p.literal != "" {
		return p.literal
	}
	return strings.TrimSpace(p.given + " " + p.family)
}

// withInitials is "Family, G. G." (sep " ") or "Family, G.G." (sep ""), or the
// literal name.
func (p citePerson) withInitials(sep string) string {
	if p.literal != "" {
		return p.literal
	}
	if ini := p.initials(sep); ini != "" {
		return p.family + ", " + ini
	}
	return p.family
}

// vancouver is "Family GG", or the literal name.
func (p citePerson) vancouver() string {
	if p.literal != "" {
		return p.literal
	}
	return strings.TrimSpace(p.family + " " + p.bareInitials())
}

// initialsFirst is "G. G. Family", the IEEE form, or the literal name.
func (p citePerson) initialsFirst() string {
	if p.literal != "" {
		return p.literal
	}
	return strings.TrimSpace(p.initials(" ") + " " + p.family)
}

// csl is the person as a CSL name: family and given, or a literal.
func (p citePerson) csl() map[string]any {
	if p.literal != "" {
		return map[string]any{"literal": p.literal}
	}
	name := map[string]any{"family": p.family}
	if p.given != "" {
		name["given"] = p.given
	}
	return name
}

// citeAuthors is a record's author field as the styles arrange it: the named
// people, and whether the record cut the list short with "et al.".
type citeAuthors struct {
	people []citePerson
	etAl   bool
}

// etAlTail is what follows the named authors when a style cuts the list
// short, before the style's own closing punctuation.
const etAlTail = ", et al"

// etAlSuffix matches an "et al." that ends a name or stands as one, in the
// spellings catalogs use ("et al.", "et al", "et. al.", "Et Al").
var etAlSuffix = regexp.MustCompile(`(?i)(^|[\s,;]+)et\.?\s*al\.?$`)

// citePeople parses a record's author field into people, in order. An
// "et al." is not a person: it is taken off whichever name carries it and
// recorded, so each style can write its own form of it.
func citePeople(author string) citeAuthors {
	names := splitAuthors(author)
	var out citeAuthors
	for _, n := range names {
		if loc := etAlSuffix.FindStringIndex(strings.TrimSpace(n)); loc != nil {
			out.etAl = true
			n = strings.TrimSpace(n)[:loc[0]]
		}
		if p := parseCitePerson(n); p.family != "" || p.literal != "" {
			out.people = append(out.people, p)
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
	// A volume, an issue and pages locate an article only inside a journal.
	// Without the journal's name they are numbers pointing nowhere, so they
	// are left out rather than printed orphaned.
	if f.isArticle && f.container == "" {
		f.volume, f.number, f.startPg, f.endPg = "", "", "", ""
	}
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
	authors := citePeople(f.author)
	names := make([]string, len(authors.people))
	for i, p := range authors.people {
		names[i] = p.withInitials(" ")
	}
	year := f.year
	if year == "" {
		year = "n.d."
	}
	head := joinSerial(names, ", ", ", & ", ", & ")
	if authors.etAl && len(names) > 0 {
		// The record names only some of the authors, so there is no last one
		// to put "&" before.
		head = strings.Join(names, ", ") + etAlTail + "."
	}
	title := f.title
	if ed := editionText(f.edition); ed != "" && !f.isArticle {
		title += " (" + ed + ")"
	}
	var tail string
	if f.isArticle {
		tail = joinNonEmpty(", ", f.container, volumeIssue(f), articlePages(f))
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
	authors := citePeople(f.author)
	people := authors.people
	var head string
	switch {
	case authors.etAl && len(people) > 0:
		head = people[0].inverted() + etAlTail
	case len(people) == 1:
		head = people[0].inverted()
	case len(people) == 2:
		head = people[0].inverted() + serialAnd + people[1].natural()
	case len(people) > 2:
		head = people[0].inverted() + etAlTail
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
			sentence(joinNonEmpty(", ", f.container, vol, no, f.year, pp)), sentence(doiURL(f.doi)))
	}
	return joinNonEmpty(" ", sentence(head), sentence(f.title),
		sentence(joinNonEmpty(", ", editionText(f.edition), f.publisher, f.year)), sentence(doiURL(f.doi)))
}

// chicagoNames is the Chicago list: the first name inverted, the rest natural,
// "and" before the last, or "et al." after the named ones when the record cut
// the list short.
func chicagoNames(authors citeAuthors) string {
	names := make([]string, len(authors.people))
	for i, p := range authors.people {
		if i == 0 {
			names[i] = p.inverted()
		} else {
			names[i] = p.natural()
		}
	}
	if authors.etAl && len(names) > 0 {
		return strings.Join(names, ", ") + etAlTail
	}
	return joinSerial(names, ", ", serialAnd, " and ")
}

// localChicago follows Chicago author-date: names, year, title, then the
// place and publisher or the volume and pages.
func localChicago(f citeFields) string {
	head := chicagoNames(citePeople(f.author))
	if f.isArticle {
		vol := joinNonEmpty(" ", f.container, f.volume)
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
	authors := citePeople(f.author)
	names := make([]string, 0, len(authors.people)+1)
	for _, p := range authors.people {
		names = append(names, p.withInitials(""))
	}
	if authors.etAl && len(names) > 0 {
		names = append(names, "et al.")
	}
	head := joinNonEmpty(", ", strings.Join(names, ", "), f.year)
	if f.isArticle {
		return joinNonEmpty(" ", sentence(head), sentence(f.title),
			sentence(joinNonEmpty(" ", f.container, joinNonEmpty(", ", f.volume, articlePages(f)))), doiURL(f.doi))
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
	authors := citePeople(f.author)
	names := make([]string, 0, vancouverMaxAuthors+1)
	for i, p := range authors.people {
		if i == vancouverMaxAuthors {
			break
		}
		names = append(names, p.vancouver())
	}
	if len(names) > 0 && (authors.etAl || len(authors.people) > vancouverMaxAuthors) {
		names = append(names, "et al")
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
		return joinNonEmpty(" ", sentence(head), sentence(f.title), sentence(f.container), sentence(ref), doiURL(f.doi))
	}
	return joinNonEmpty(" ", sentence(head), sentence(f.title), sentence(editionText(f.edition)),
		sentence(joinNonEmpty("; ", placePublisher(f), f.year)), doiURL(f.doi))
}

// localIEEE follows IEEE: initials before the family name, "and" before the
// last, the title in quotes for an article.
func localIEEE(f citeFields) string {
	authors := citePeople(f.author)
	names := make([]string, len(authors.people))
	for i, p := range authors.people {
		names[i] = p.initialsFirst()
	}
	head := joinSerial(names, ", ", serialAnd, " and ")
	if authors.etAl && len(names) > 0 {
		head = names[0] + " et al."
	}
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
		return sentence(joinNonEmpty(" ", joinNonEmpty(", ", head, quoted), joinNonEmpty(", ", f.container, vol, no, pp, f.year, doi)))
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
	people := citePeople(f.author).people
	if len(people) > 0 {
		authors := make([]any, len(people))
		for i, p := range people {
			authors[i] = p.csl()
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
		"container-title": f.container,
	} {
		if value != "" {
			item[key] = value
		}
	}
	return item.JSON()
}
