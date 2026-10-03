package libgen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Answers doi.org gave on 2026-10-03, kept verbatim: DataCite's APA with its
// markup and entity, Crossref's MLA with the empty editor, and Crossref's IEEE
// with its citation number.
const (
	negotiatedDataCiteAPA = `Ollomo, B., Durand, P., Prugnolle, F., Douzery, E. J. P., Arnathau, C., Nkoghe, D., Leroy, E., &amp; Renaud, F. (2011). <i>Data from: A new malaria agent in African hominids.</i> (Version 1) [Dataset]. Dryad. https://doi.org/10.5061/DRYAD.8515`
	negotiatedCrossrefMLA = "Kucsko, G., et al. “Nanometre-Scale Thermometry in a Living Cell.” Nature, edited by , vol. 500, no. 7460, July 2013, pp. 54–58. Crossref, https://doi.org/10.1038/nature12373.\n"
	negotiatedCrossrefIEE = "[1]G. Kucsko et al., “Nanometre-scale thermometry in a living cell,” Nature, vol. 500, no. 7460, pp. 54–58, Jul. 2013, doi: 10.1038/nature12373.\n"
)

// TestCleanNegotiatedCitation pins each repair the registries' answers need,
// and that an answer which is not a reference comes back empty.
func TestCleanNegotiatedCitation(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{
			"DataCite markup and entity", negotiatedDataCiteAPA,
			"Ollomo, B., Durand, P., Prugnolle, F., Douzery, E. J. P., Arnathau, C., Nkoghe, D., Leroy, E., & Renaud, F. (2011). Data from: A new malaria agent in African hominids. (Version 1) [Dataset]. Dryad. https://doi.org/10.5061/DRYAD.8515",
		},
		{
			"Crossref empty editor", negotiatedCrossrefMLA,
			"Kucsko, G., et al. “Nanometre-Scale Thermometry in a Living Cell.” Nature, vol. 500, no. 7460, July 2013, pp. 54–58. Crossref, https://doi.org/10.1038/nature12373.",
		},
		{
			"bracketed number", negotiatedCrossrefIEE,
			"G. Kucsko et al., “Nanometre-scale thermometry in a living cell,” Nature, vol. 500, no. 7460, pp. 54–58, Jul. 2013, doi: 10.1038/nature12373.",
		},
		{"dotted number", "1.Kucsko G. Title. 2013", "Kucsko G. Title. 2013"},
		{"a real editor stays", "Book, edited by Ann Smith, 2001.", "Book, edited by Ann Smith, 2001."},
		{"newlines and a bidi override", "A" + string(rune(0x202e)) + " title\n\twith  breaks", "A title with breaks"},
		{"an error document", `{"code":"style-not-found"}`, ""},
		{"empty", "  \n", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanNegotiatedCitation(tc.in); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestCiteStyleNamesAreAllServed checks every advertised style has a path: a
// CSL style id, or the CSL-JSON record.
func TestCiteStyleNamesAreAllServed(t *testing.T) {
	for _, style := range CiteStyleNames() {
		t.Run(style, func(t *testing.T) {
			if _, ok := citeStyleIDs[style]; !ok && style != CiteStyleCSLJSON {
				t.Errorf("%s has no CSL style id", style)
			}
		})
	}
}

// TestFormatDOI drives the negotiation against a stand-in for doi.org: each
// style is asked for by its CSL id in the Accept header, a style the resolver
// refuses is simply absent, and csl-json is the trimmed record.
func TestFormatDOI(t *testing.T) {
	csl, err := os.ReadFile("testdata/csl_crossref.json")
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu      sync.Mutex
		accepts []string
		paths   []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept := r.Header.Get("Accept")
		mu.Lock()
		accepts = append(accepts, accept)
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch accept {
		case "text/x-bibliography; style=modern-language-association":
			_, _ = w.Write([]byte(negotiatedCrossrefMLA))
		case "text/x-bibliography; style=ieee":
			_, _ = w.Write([]byte(negotiatedCrossrefIEE))
		case "application/vnd.citationstyles.csl+json":
			_, _ = w.Write(csl)
		case "text/x-bibliography; style=apa":
			w.WriteHeader(http.StatusNoContent)
		case "text/x-bibliography; style=elsevier-harvard":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><body>A landing page</body></html>"))
		default:
			http.Error(w, `{"code":"style-not-found"}`, http.StatusNotAcceptable)
		}
	}))
	t.Cleanup(srv.Close)
	c := newVerifyClient(t, srv.URL)
	c.doiOrgBaseOverride = srv.URL

	got := c.FormatDOI(context.Background(), "10.1038/nature12373", []string{"mla", "ieee", "apa", "chicago", "harvard", "csl-json", "nonsense"})
	if !strings.HasPrefix(got["mla"], "Kucsko, G., et al.") || strings.Contains(got["mla"], "edited by") {
		t.Errorf("mla = %q", got["mla"])
	}
	if !strings.HasPrefix(got["ieee"], "G. Kucsko") {
		t.Errorf("ieee = %q", got["ieee"])
	}
	for _, missing := range []string{"apa", "chicago", "harvard", "nonsense"} {
		t.Run(missing, func(t *testing.T) {
			if _, ok := got[missing]; ok {
				t.Errorf("%s answered %q, want it absent", missing, got[missing])
			}
		})
	}
	var record map[string]any
	if err = json.Unmarshal([]byte(got["csl-json"]), &record); err != nil {
		t.Fatalf("csl-json is not JSON: %v", err)
	}
	if _, ok := record["reference"]; ok || record["title"] != "Nanometre-scale thermometry in a living cell" {
		t.Errorf("csl-json not trimmed to the CSL variables: %v", record["title"])
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(accepts, "text/x-bibliography; style=chicago-author-date") {
		t.Errorf("chicago was not asked for as chicago-author-date: %q", accepts)
	}
	if !slices.Contains(paths, "/10.1038/nature12373") {
		t.Errorf("paths = %q", paths)
	}
}

// TestFetchCSL_Registries parses the record each registration agency served
// for one of its DOIs: Crossref's carries a reference list and role
// vocabularies that are dropped, DataCite's a dataset type, and mEDRA's no
// authors at all.
func TestFetchCSL_Registries(t *testing.T) {
	tests := []struct {
		file, title, typ string
		year, authors    int
		article          bool
	}{
		{"csl_crossref.json", "Nanometre-scale thermometry in a living cell", "journal-article", 2013, 8, true},
		{"csl_datacite.json", "Data from: A new malaria agent in African hominids.", "dataset", 2011, 8, false},
		{"csl_medra.json", "Libri ricevuti", "article-journal", 2003, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			body, err := os.ReadFile("testdata/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
			t.Cleanup(srv.Close)
			c := newVerifyClient(t, srv.URL)
			item := c.FetchCSL(context.Background(), "10.1/x")
			if item == nil {
				t.Fatal("no record")
			}
			if item.String("title") != tc.title || item.String("type") != tc.typ || item.Year() != tc.year ||
				len(item.Authors()) != tc.authors || item.IsArticle() != tc.article {
				t.Errorf("title %q type %q year %d authors %d article %v", item.String("title"), item.String("type"),
					item.Year(), len(item.Authors()), item.IsArticle())
			}
			for _, dropped := range []string{"reference", "indexed", "license", "link"} {
				if _, ok := (*item)[dropped]; ok {
					t.Errorf("kept %s", dropped)
				}
			}
			if strings.Contains(item.JSON(), "affiliation") {
				t.Error("author affiliations were kept")
			}
		})
	}
}

// TestFetchCSL_Unusable covers the answers that are no record: a failure, a
// body that is not JSON and a record with no title.
func TestFetchCSL_Unusable(t *testing.T) {
	for _, body := range []string{"<html>", `{"type":"book"}`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			t.Cleanup(srv.Close)
			if item := newVerifyClient(t, srv.URL).FetchCSL(context.Background(), "10.1/x"); item != nil {
				t.Errorf("got %v", *item)
			}
		})
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }))
	t.Cleanup(down.Close)
	if item := newVerifyClient(t, down.URL).FetchCSL(context.Background(), "10.1/x"); item != nil {
		t.Errorf("a 404 gave %v", *item)
	}
}

// TestCSLItemAccessors covers the shapes the accessors read: a title as an
// array, a literal author, and an issued date with no parts.
func TestCSLItemAccessors(t *testing.T) {
	item := CSLItem{
		"title":  []any{"Array Title"},
		"author": []any{map[string]any{"literal": "The Consortium"}, map[string]any{}, "junk"},
		"issued": map[string]any{"date-parts": []any{[]any{}}},
	}
	if item.String("title") != "Array Title" || item.String("missing") != "" {
		t.Errorf("title = %q", item.String("title"))
	}
	if got := item.Authors(); len(got) != 1 || got[0] != "The Consortium" {
		t.Errorf("authors = %q", got)
	}
	if item.Year() != 0 || (CSLItem{}).Year() != 0 {
		t.Error("a date with no parts must give no year")
	}
	if (CSLItem{"title": []any{1}}).String("title") != "" {
		t.Error("a non-string title must read as empty")
	}
	if got := (CSLItem{"bad": make(chan int)}).JSON(); got != "" {
		t.Errorf("an unencodable item gave %q", got)
	}
	if people := cslPeople("not a list"); len(people) != 0 {
		t.Errorf("people = %v", people)
	}
}
