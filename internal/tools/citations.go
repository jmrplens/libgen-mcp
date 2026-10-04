package tools

import (
	"context"
	"strings"

	"github.com/jmrplens/libgen-mcp/v2/internal/config"
	"github.com/jmrplens/libgen-mcp/v2/internal/libgen"
)

// Citations holds ready-to-paste bibliographic exports built from a record's
// metadata. A field is empty when the record lacks the data to build it.
//
// A formatted citation is the one output of this server that a person pastes
// into a bibliography and never looks at again, so it states only what the
// server could stand behind: the catalog's DOI appears in the entries only once
// corroborated (see doiForCitation), and Provenance always says where the rest
// of the fields came from.
type Citations struct {
	BibTeX string `json:"bibtex,omitempty" jsonschema:"@book/@article entry"`
	RIS    string `json:"ris,omitempty" jsonschema:"TY..ER entry"`
	// DOIStatus is machine-readable so a caller can branch on it without parsing
	// Provenance; it is empty when the record carries no DOI at all.
	DOIStatus  string `json:"doi_status,omitempty" jsonschema:"Crossref check on the DOI: confirmed (same title, entries state it), unverified (not checked) or mismatch (other work). The last two omit the DOI"`
	Provenance string `json:"provenance,omitempty" jsonschema:"field sources and what was verified. Relay it, and do not present the citation as authoritative"`
	// Formatted holds the styles cite_as asked for, in the order asked, and is
	// absent when it asked for none.
	Formatted []FormattedCitation `json:"formatted,omitempty" jsonschema:"the styles requested in cite_as, each with the path that produced it"`

	// fields is what the BibTeX and RIS entries were built from, kept so the
	// requested styles are built from exactly the same fields and DOI.
	fields *citeFields
}

// The values of FormattedCitation.Source.
const (
	formatSourceRegistry    = "doi.org"
	formatSourceLocal       = "local"
	formatSourceUnavailable = "unavailable"
)

// FormattedCitation is one requested citation style.
type FormattedCitation struct {
	Style  string `json:"style" jsonschema:"the cite_as value this answers"`
	Text   string `json:"text,omitempty" jsonschema:"the reference in that style, or CSL-JSON for csl-json. Plain text, with no italics"`
	Source string `json:"source" jsonschema:"doi.org (formatted by the DOI's registration agency), local (built here from the record's fields) or unavailable"`
	Note   string `json:"note,omitempty" jsonschema:"why the registry was not used, or why nothing could be built"`
}

type citeFields struct {
	author, title, year, publisher, address, edition, series, pages string
	volume, number, startPg, endPg, doi, md5                        string
	// container is the journal or book an article appeared in. The catalog
	// does not state it, so it comes from a record that does (one built from
	// the DOI's registry) or from Crossref once the DOI is confirmed. Only the
	// cite_as styles write it: the BibTeX and RIS entries are unchanged.
	container string
	isArticle bool
}

// doiVerifier corroborates that a DOI really names the work a record claims it
// does. It is an interface rather than *libgen.Client so the citation builder can
// be exercised offline, and so the handler can pass nil to mean "no corroboration
// is available here" — which is a verdict, not an outage.
type doiVerifier interface {
	VerifyDOI(ctx context.Context, doi, recordTitle string) libgen.DOICheck
}

// buildCitations returns BibTeX+RIS exports for a details record, or nil when
// the record has no title (the minimum for a usable citation). Bibliographic
// fields come from the edition record; md5 from the file record.
//
// The record's DOI is treated as a claim, not a fact. LibGen records exist whose
// DOI belongs to an entirely different work — 10.1371/journal.pmed.0020124
// (Ioannidis) sits on the catalog's copy of Taleb's Antifragile — and emitting
// that pairing as a formatted citation manufactures a reference that looks
// authoritative and is false. So the DOI reaches the entries only through
// doiForCitation, and only when corroborated.
//
// knownCrossrefTitle is the title Crossref already returned for this DOI when the
// caller has one in hand; it spares a second lookup. v may be nil.
func buildCitations(ctx context.Context, v doiVerifier, knownCrossrefTitle string, file, edition map[string]any) *Citations {
	get := func(key string) string {
		if v := stringField(edition, key); v != "" {
			return oneLine(v)
		}
		return oneLine(stringField(file, key))
	}
	title := get("title")
	if title == "" {
		return nil
	}
	claimedDOI := get("doi")
	fromRegistry := stringField(file, "origin") == originRegistry
	var check libgen.DOICheck
	if fromRegistry {
		// The fields are the registry's own record of this DOI, fetched by it,
		// so there is no third-party claim to corroborate.
		check = libgen.DOICheck{Verdict: libgen.DOIConfirmed}
	} else {
		check = corroborateDOI(ctx, v, knownCrossrefTitle, claimedDOI, title)
	}
	f := citeFields{
		author: get("author"), title: title, year: get("year"),
		publisher: get("publisher"), address: get("city"),
		edition: get("edition"), series: get("series_name"),
		pages:  get("pages"),
		volume: get("issue_volume"), number: get("issue_number"),
		startPg: get("issue_first_page"), endPg: get("issue_last_page"),
		doi: doiForCitation(claimedDOI, check), md5: oneLine(stringField(file, "md5")),
		container: get("container_title"),
	}
	if f.container == "" && check.Verdict == libgen.DOIConfirmed {
		f.container = oneLine(check.CrossrefContainer)
	}
	// The entry type is decided by the catalog's own classification, never by the
	// bare presence of a DOI: an uncorroborated DOI would otherwise re-typeset a
	// 544-page Random House book as a journal article on the strength of the same
	// bad field the citation must not repeat.
	// A registry's own record states its type, and a dataset with a DOI is
	// still a dataset, so there the DOI is no evidence of an article.
	f.isArticle = get("type") == "a" || get("libgen_topic") == "a" || (f.doi != "" && !fromRegistry)
	provenance := citationProvenance(fieldsProvenance(file), claimedDOI, check)
	if fromRegistry {
		provenance = registryProvenance
	}
	return &Citations{
		BibTeX:     renderBibTeX(f),
		RIS:        renderRIS(f),
		DOIStatus:  doiStatus(claimedDOI, check),
		Provenance: provenance,
		fields:     &f,
	}
}

// originRegistry labels a record built from the DOI's own registration agency
// through doi.org, for a DOI neither the catalog nor Crossref knows.
const originRegistry = "doi.org"

// registryProvenance is the caveat on a citation built from such a record.
const registryProvenance = "Bibliographic fields come from the DOI's own registration agency, through doi.org. They are the registrant's metadata, so check them against the work before publishing this citation."

// styleFormatter formats a DOI in the requested styles through doi.org. It is
// an interface so the assembly below can be exercised offline, and so the
// handler can pass nil for a server that sends nothing to doi.org.
type styleFormatter interface {
	FormatDOI(ctx context.Context, doi string, styles []string) map[string]string
}

// attachFormatted adds the styles cite_as asked for to the record's citations,
// each through the best path it has: the DOI's registry when the record has a
// DOI it may send there, and otherwise the record's own fields, built by the
// same rules as the BibTeX entry. A style neither path can produce is listed
// as unavailable with the reason, so a caller never mistakes silence for a
// style it did not ask for.
//
// Only a DOI that names this work is sent: one Crossref confirmed, one whose
// fields came from its own registry, or the DOI of a record that is nothing
// but Crossref's answer for it. A catalog DOI that failed corroboration is
// never sent, because the registry would format the work it really belongs to
// and the reference would describe a different work under this record.
func attachFormatted(ctx context.Context, fmtr styleFormatter, out *DetailsOutput, styles []string) {
	if len(styles) == 0 {
		return
	}
	doi := negotiableDOI(*out)
	var registry map[string]string
	var note string
	// The switch is read first: with it off nothing corroborates a catalog DOI,
	// so the record's DOI reads as unconfirmed, and blaming the DOI would tell
	// an operator who turned lookups off that the record is suspect.
	switch {
	case fmtr == nil:
		note = "This server does not ask doi.org (" + config.EnvName("ENRICH") + "=false), so the style was built from the record's fields."
	case doi == "":
		note = unsentDOINote(out.Citations)
	default:
		registry = fmtr.FormatDOI(ctx, doi, styles)
		note = "doi.org gave no usable answer for this style, so it was built from the record's fields."
	}
	formatted := make([]FormattedCitation, 0, len(styles))
	for _, style := range styles {
		formatted = append(formatted, formatOne(style, registry, out.Citations, note))
	}
	if out.Citations == nil {
		out.Citations = &Citations{Provenance: "The record has no title of its own, so only the registry's formats are given."}
	}
	out.Citations.Formatted = formatted
}

// formatOne picks the path for one style: the registry's text, else the
// record's fields, else unavailable.
func formatOne(style string, registry map[string]string, c *Citations, note string) FormattedCitation {
	if text := registry[style]; text != "" {
		return FormattedCitation{Style: style, Text: text, Source: formatSourceRegistry}
	}
	if c != nil && c.fields != nil {
		if text := formatLocal(style, *c.fields); text != "" {
			return FormattedCitation{Style: style, Text: text, Source: formatSourceLocal, Note: note}
		}
	}
	return FormattedCitation{
		Style: style, Source: formatSourceUnavailable,
		Note: "Neither doi.org nor the record's own fields could produce it: the record has no title.",
	}
}

// negotiableDOI is the DOI the record may send to doi.org, or "".
func negotiableDOI(out DetailsOutput) string {
	if c := out.Citations; c != nil && c.fields != nil && c.fields.doi != "" {
		return c.fields.doi
	}
	if stringField(out.File, "origin") == "crossref" {
		return stringField(out.File, "doi")
	}
	return ""
}

// unsentDOINote says why a record's styles were built locally without asking
// the registry.
func unsentDOINote(c *Citations) string {
	if c != nil && c.DOIStatus != "" && c.DOIStatus != string(libgen.DOIConfirmed) {
		return "The record's DOI was not confirmed to name this work, so it was not sent to doi.org and the style was built from the record's fields."
	}
	return "The record has no DOI, so the style was built from its fields."
}

// fieldsProvenance names where a record's bibliographic fields came from. An md5
// the catalog does not carry is answered from Anna's Archive's own record of the
// file (detailsFromAnnas labels it origin=annas), and a caveat naming the
// Library Genesis catalog on that record sends the reader to check the wrong
// source.
func fieldsProvenance(file map[string]any) string {
	if stringField(file, "origin") == "annas" {
		return annasProvenance
	}
	return catalogProvenance
}

// corroborateDOI resolves the verdict for a record's DOI without ever failing:
// a record with no DOI, an absent verifier and a dead registry all land on
// DOIUnverified, which omits the DOI rather than asserting it. A Crossref title
// already fetched for this DOI is judged in-process, so enrichment and
// corroboration never ask the registry the same question twice.
func corroborateDOI(ctx context.Context, v doiVerifier, knownCrossrefTitle, doi, title string) libgen.DOICheck {
	unverified := libgen.DOICheck{Verdict: libgen.DOIUnverified}
	switch {
	case doi == "":
		return unverified
	case strings.TrimSpace(knownCrossrefTitle) != "":
		return libgen.CheckDOITitle(title, knownCrossrefTitle)
	case v == nil:
		return unverified
	default:
		return v.VerifyDOI(ctx, doi, title)
	}
}

// doiForCitation returns the DOI to write into the BibTeX and RIS entries: the
// claimed one only when Crossref confirmed it names this work, and otherwise
// nothing. Omitting a real DOI costs a reader one lookup; asserting a wrong one
// puts a fabricated reference into their bibliography.
func doiForCitation(claimed string, check libgen.DOICheck) string {
	if check.Verdict == libgen.DOIConfirmed {
		return claimed
	}
	return ""
}

// doiStatus reports the verdict as the machine-readable doi_status value, empty
// when the record claimed no DOI and there was accordingly nothing to check.
func doiStatus(claimed string, check libgen.DOICheck) string {
	if claimed == "" {
		return ""
	}
	return string(check.Verdict)
}

// catalogProvenance is the standing caveat on every citation this server builds:
// the fields are third-party catalog data that nobody has checked against the
// work itself.
const catalogProvenance = "Bibliographic fields come from the Library Genesis catalog, an unverified third-party source; check them against the work before publishing this citation."

// annasProvenance is the same caveat for a record Anna's Archive answered in the
// catalog's place.
const annasProvenance = "Bibliographic fields come from Anna's Archive's record of this file, an unverified third-party source; check them against the work before publishing this citation."

// citationProvenance explains, in one line the caller can relay verbatim, where
// the citation's fields came from and what happened to the record's DOI. The
// claimed DOI and the registry's title are untrusted text, so both are collapsed
// to a single line and bounded in length before being quoted back.
func citationProvenance(fields, claimed string, check libgen.DOICheck) string {
	if claimed == "" {
		return fields
	}
	doi := truncateRunes(oneLine(claimed), 128)
	switch check.Verdict {
	case libgen.DOIConfirmed:
		return fields + " DOI " + doi + " was corroborated against Crossref, which registers it to this same title."
	case libgen.DOIMismatch:
		return fields + " The record lists DOI " + doi + ", but Crossref registers that DOI to a different work (" +
			truncateRunes(oneLine(check.CrossrefTitle), 160) + "), so this record's DOI is wrong and has been left out of the entries above. " +
			"Do not cite this record for that DOI."
	default:
		return fields + " The record lists DOI " + doi + ", but it could not be corroborated against Crossref, so it has been left out of the entries above."
	}
}

// truncateRunes shortens s to at most n runes, appending an ellipsis when it cut
// anything. It counts runes rather than bytes so a multi-byte character is never
// split into invalid UTF-8 on the way into a citation or a Markdown block.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// oneLine collapses any CR/LF/tab whitespace in a metadata value to a single
// space. BibTeX and RIS field values are single-line by nature, so a raw
// newline is malformed anyway; collapsing it here hardens both the structured
// citations JSON and any Markdown that later wraps these values, since an
// embedded newline could otherwise forge instruction lines or help break out
// of a rendered code fence.
func oneLine(s string) string {
	replacer := strings.NewReplacer(
		"\r\n", " ",
		"\n", " ",
		"\r", " ",
		"\t", " ",
	)
	return strings.TrimSpace(replacer.Replace(s))
}

type kv struct{ k, v string }

func renderBibTeX(f citeFields) string {
	entry, key := "book", citeKey(f)
	// BibTeX separates names at "and" and nowhere else: the catalog's own "A; B"
	// or "A, B" reaches a bibliography as one author with a very long name.
	author := strings.Join(splitAuthors(f.author), " and ")
	fields := []kv{
		{"author", author},
		{"title", f.title},
		{"year", f.year},
		{"publisher", f.publisher},
		{"edition", f.edition},
		{"series", f.series},
		{"address", f.address},
		{"pages", f.pages},
		{"doi", f.doi},
	}
	if f.isArticle {
		entry = "article"
		fields = []kv{
			{"author", author},
			{"title", f.title},
			{"year", f.year},
			{"volume", f.volume},
			{"number", f.number},
			{"pages", pageRange(f)},
			{"doi", f.doi},
		}
	}
	var b strings.Builder
	b.WriteString("@" + entry + "{" + key + ",\n")
	for _, kvp := range fields {
		if strings.TrimSpace(kvp.v) != "" {
			b.WriteString("  " + kvp.k + " = {" + kvp.v + "},\n")
		}
	}
	if f.md5 != "" {
		b.WriteString("  note = {libgen md5: " + f.md5 + "}\n")
	}
	b.WriteString("}")
	return b.String()
}

func renderRIS(f citeFields) string {
	ty := "BOOK"
	if f.isArticle {
		ty = "JOUR"
	}
	lines := []kv{{"TY", ty}}
	for _, a := range splitAuthors(f.author) {
		lines = append(lines, kv{"AU", a})
	}
	lines = append(lines,
		kv{"TI", f.title}, kv{"PY", f.year}, kv{"PB", f.publisher},
		kv{"VL", f.volume}, kv{"IS", f.number}, kv{"SP", f.startPg}, kv{"EP", f.endPg},
		kv{"DO", f.doi})
	if f.md5 != "" {
		lines = append(lines, kv{"L1", "libgen md5: " + f.md5})
	}
	var b strings.Builder
	for _, l := range lines {
		if strings.TrimSpace(l.v) != "" {
			b.WriteString(l.k + "  - " + l.v + "\n")
		}
	}
	b.WriteString("ER  - ")
	return b.String()
}

func pageRange(f citeFields) string {
	switch {
	case f.startPg != "" && f.endPg != "":
		return f.startPg + "--" + f.endPg
	case f.pages != "":
		return f.pages
	default:
		return ""
	}
}

// splitAuthors returns a record's authors, one name each.
//
// The catalog separates names with " and ", with semicolons or with commas, and
// a comma is also how one inverted name is written ("Knuth, Donald E."). So a
// comma-separated field is a list only when every part is a full name of two or
// more words ("Thomas H. Cormen, Charles E. Leiserson"), and parts that
// alternate a one-word surname with given names ("Knuth, D. E., Graham, R. L.")
// are paired back into inverted names. Anything else stays one name: splitting
// a name that was never a list invents authors.
func splitAuthors(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	if strings.Contains(s, " and ") {
		return trimAll(strings.Split(s, " and "))
	}
	if strings.Contains(s, ";") {
		return trimAll(strings.Split(s, ";"))
	}
	return splitCommaAuthors(trimAll(strings.Split(s, ",")))
}

// splitCommaAuthors reads the parts of a field split at its commas.
func splitCommaAuthors(parts []string) []string {
	if len(parts) < 2 {
		return parts
	}
	if allFullNames(parts) {
		return parts
	}
	if len(parts)%2 == 0 && alternatesSurnames(parts) {
		var names []string
		for i := 0; i < len(parts); i += 2 {
			names = append(names, parts[i]+", "+parts[i+1])
		}
		return names
	}
	return []string{strings.Join(parts, ", ")}
}

// allFullNames reports whether every part is a name of two or more words.
func allFullNames(parts []string) bool {
	for _, p := range parts {
		if len(strings.Fields(p)) < 2 {
			return false
		}
	}
	return true
}

// alternatesSurnames reports whether every even-indexed part is a single word,
// the surname half of an inverted name.
func alternatesSurnames(parts []string) bool {
	for i := 0; i < len(parts); i += 2 {
		if len(strings.Fields(parts[i])) != 1 {
			return false
		}
	}
	return true
}

func trimAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if t := strings.TrimSpace(v); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// citeKey builds an alnum key: firstAuthorSurname+year, else firstTitleWord+year,
// else "libgen"+md5[:8].
func citeKey(f citeFields) string {
	base := ""
	if auths := splitAuthors(f.author); len(auths) > 0 {
		// The surname is the last word before any comma: "Knuth, Donald E." and
		// "Donald E. Knuth, Jr." both key as Knuth, where the last word of the
		// whole name gave E and Jr.
		before, _, _ := strings.Cut(auths[0], ",")
		parts := strings.Fields(before)
		if len(parts) > 0 {
			base = parts[len(parts)-1]
		}
	}
	if base == "" {
		if w := strings.Fields(f.title); len(w) > 0 {
			base = w[0]
		}
	}
	key := alnum(base) + alnum(f.year)
	if key == "" {
		key = "libgen" + firstN(alnum(f.md5), 8)
	}
	return key
}

func alnum(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func firstN(s string, n int) string {
	return s[:min(len(s), n)]
}
