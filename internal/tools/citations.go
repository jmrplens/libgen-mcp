package tools

import (
	"context"
	"strings"

	"github.com/jmrplens/libgen-mcp/internal/libgen"
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
	DOIStatus  string `json:"doi_status,omitempty" jsonschema:"Crossref check on the DOI: confirmed (same title, entries state it), unverified (not checked) or mismatch (other work); the last two omit the DOI"`
	Provenance string `json:"provenance,omitempty" jsonschema:"field sources and what was verified; relay it, do not present the citation as authoritative"`
}

type citeFields struct {
	author, title, year, publisher, address, edition, series, pages string
	volume, number, startPg, endPg, doi, md5                        string
	isArticle                                                       bool
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
	check := corroborateDOI(ctx, v, knownCrossrefTitle, claimedDOI, title)
	f := citeFields{
		author: get("author"), title: title, year: get("year"),
		publisher: get("publisher"), address: get("city"),
		edition: get("edition"), series: get("series_name"),
		pages:  get("pages"),
		volume: get("issue_volume"), number: get("issue_number"),
		startPg: get("issue_first_page"), endPg: get("issue_last_page"),
		doi: doiForCitation(claimedDOI, check), md5: oneLine(stringField(file, "md5")),
	}
	// The entry type is decided by the catalog's own classification, never by the
	// bare presence of a DOI: an uncorroborated DOI would otherwise re-typeset a
	// 544-page Random House book as a journal article on the strength of the same
	// bad field the citation must not repeat.
	f.isArticle = get("type") == "a" || get("libgen_topic") == "a" || f.doi != ""
	return &Citations{
		BibTeX:     renderBibTeX(f),
		RIS:        renderRIS(f),
		DOIStatus:  doiStatus(claimedDOI, check),
		Provenance: citationProvenance(claimedDOI, check),
	}
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

// citationProvenance explains, in one line the caller can relay verbatim, where
// the citation's fields came from and what happened to the record's DOI. The
// claimed DOI and the registry's title are untrusted text, so both are collapsed
// to a single line and bounded in length before being quoted back.
func citationProvenance(claimed string, check libgen.DOICheck) string {
	if claimed == "" {
		return catalogProvenance
	}
	doi := truncateRunes(oneLine(claimed), 128)
	switch check.Verdict {
	case libgen.DOIConfirmed:
		return catalogProvenance + " DOI " + doi + " was corroborated against Crossref, which registers it to this same title."
	case libgen.DOIMismatch:
		return catalogProvenance + " The catalog lists DOI " + doi + ", but Crossref registers that DOI to a different work (" +
			truncateRunes(oneLine(check.CrossrefTitle), 160) + "), so this record's DOI is wrong and has been left out of the entries above. " +
			"Do not cite this record for that DOI."
	default:
		return catalogProvenance + " The catalog lists DOI " + doi + ", but it could not be corroborated against Crossref, so it has been left out of the entries above."
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
	fields := []kv{
		{"author", f.author},
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
			{"author", f.author},
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

func splitAuthors(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	sep := func(r rune) bool { return r == ';' }
	if strings.Contains(s, " and ") {
		return trimAll(strings.Split(s, " and "))
	}
	return trimAll(strings.FieldsFunc(s, sep))
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
		parts := strings.Fields(auths[0])
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
	if len(s) < n {
		return s
	}
	return s[:n]
}
