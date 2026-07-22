package tools

import "strings"

// Citations holds ready-to-paste bibliographic exports built from a record's
// metadata. A field is empty when the record lacks the data to build it.
type Citations struct {
	BibTeX string `json:"bibtex,omitempty" jsonschema:"a BibTeX @book/@article entry built from this record's metadata"`
	RIS    string `json:"ris,omitempty" jsonschema:"an RIS (TY..ER) entry built from this record's metadata"`
}

type citeFields struct {
	author, title, year, publisher, address, edition, series, pages string
	volume, number, startPg, endPg, doi, md5                        string
	isArticle                                                       bool
}

// buildCitations returns BibTeX+RIS exports for a details record, or nil when
// the record has no title (the minimum for a usable citation). Bibliographic
// fields come from the edition record; md5 from the file record.
func buildCitations(file, edition map[string]any) *Citations {
	get := func(key string) string {
		if v := stringField(edition, key); v != "" {
			return v
		}
		return stringField(file, key)
	}
	title := get("title")
	if title == "" {
		return nil
	}
	f := citeFields{
		author: get("author"), title: title, year: get("year"),
		publisher: get("publisher"), address: get("city"),
		edition: get("edition"), series: get("series_name"),
		pages:  get("pages"),
		volume: get("issue_volume"), number: get("issue_number"),
		startPg: get("issue_first_page"), endPg: get("issue_last_page"),
		doi: get("doi"), md5: stringField(file, "md5"),
	}
	f.isArticle = f.doi != "" || get("type") == "a" || get("libgen_topic") == "a"
	return &Citations{BibTeX: renderBibTeX(f), RIS: renderRIS(f)}
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
