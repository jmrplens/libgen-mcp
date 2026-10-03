package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/v2/internal/config"
	"github.com/jmrplens/libgen-mcp/v2/internal/libgen"
	"github.com/jmrplens/libgen-mcp/v2/internal/openalex"
)

// TestRelatedRequest covers the defaults and every refusal.
func TestRelatedRequest(t *testing.T) {
	tests := []struct {
		name    string
		kind    string
		limit   int
		want    relatedAsk
		wantErr string
	}{
		{name: "nothing asked", want: relatedAsk{}},
		{name: "default limit", kind: " Cited_By ", want: relatedAsk{kind: "cited_by", limit: 10}},
		{name: "explicit limit", kind: "references", limit: 25, want: relatedAsk{kind: "references", limit: 25}},
		{name: "limit alone", limit: 5, wantErr: "only read with related"},
		{name: "unknown kind", kind: "citations", wantErr: `not "citations"`},
		{name: "limit too high", kind: "references", limit: 26, wantErr: "1 to 25"},
		{name: "limit negative", kind: "references", limit: -1, wantErr: "1 to 25"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := relatedRequest(tc.kind, tc.limit)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("got %+v, %v", got, err)
			}
		})
	}
}

// stubLooker records the DOI it was asked about and answers with one work.
type stubLooker struct{ asked *[]string }

// Related implements relatedLooker.
func (s stubLooker) Related(_ context.Context, doi, kind string, limit int) libgen.RelatedWorks {
	*s.asked = append(*s.asked, doi)
	return libgen.RelatedWorks{Kind: kind, Total: limit, Works: []libgen.RelatedWork{{Title: "W"}}}
}

// TestAttachRelated looks the list up only by a DOI that names this work, and
// says why it is not available otherwise.
func TestAttachRelated(t *testing.T) {
	verifier := stubVerifier{byDOI: map[string]libgen.DOICheck{
		"10.1/ok":  {Verdict: libgen.DOIConfirmed},
		"10.1/bad": {Verdict: libgen.DOIMismatch, CrossrefTitle: "Other"},
	}}
	tests := []struct {
		name      string
		edition   map[string]any
		allowed   bool
		wantAsked string
		wantNote  string
	}{
		{"confirmed doi", map[string]any{"title": "T", "doi": "10.1/ok"}, true, "10.1/ok", ""},
		{"no doi", map[string]any{"title": "T"}, true, "", "has no DOI"},
		{"mismatched doi", map[string]any{"title": "T", "doi": "10.1/bad"}, true, "", "not confirmed"},
		{"enrichment off", map[string]any{"title": "T", "doi": "10.1/ok"}, false, "", "LIBGEN_MCP_ENRICH=false"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var asked []string
			out := DetailsOutput{Edition: tc.edition}
			out.Citations = buildCitations(context.Background(), verifier, "", nil, out.Edition)
			attachRelated(context.Background(), tc.allowed, stubLooker{asked: &asked}, &out, relatedAsk{kind: "references", limit: 4})
			if strings.Join(asked, ",") != tc.wantAsked {
				t.Errorf("asked %q, want %q", asked, tc.wantAsked)
			}
			if out.Related == nil || out.Related.Kind != "references" || !strings.Contains(out.Related.Note, tc.wantNote) {
				t.Errorf("related = %+v", out.Related)
			}
		})
	}
	out := DetailsOutput{}
	attachRelated(context.Background(), true, stubLooker{}, &out, relatedAsk{})
	if out.Related != nil {
		t.Error("a call that did not ask got a related block")
	}
}

// TestWriteRelated renders the list with every OpenAlex field escaped for its
// cell, the total as a row, and the note as a quote.
func TestWriteRelated(t *testing.T) {
	var b strings.Builder
	writeRelated(&b, &libgen.RelatedWorks{
		Kind: "cited_by", Total: 1980, Note: "Partial.",
		Works: []libgen.RelatedWork{{Title: "Quantum | sensing\nnew row", Year: 2017, DOI: "10.1103/x", OpenAccess: true, CitedByCount: 4041}},
	})
	md := b.String()
	for _, want := range []string{
		"### Cited by (via OpenAlex)",
		"- **Total**: 1980",
		"| 1 | Quantum \\| sensing new row | 2017 | `10.1103/x` | yes | 4041 |",
		"> Partial.",
	} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(md, want) {
				t.Errorf("lacks %q:\n%s", want, md)
			}
		})
	}
	b.Reset()
	writeRelated(&b, &libgen.RelatedWorks{Kind: "references", Note: "Not available: the record has no DOI."})
	if !strings.Contains(b.String(), "### References (via OpenAlex)") || strings.Contains(b.String(), "| # |") {
		t.Errorf("empty list rendered as:\n%s", b.String())
	}
}

// TestDetailsHandler_Related drives a DOI end to end: the registry record
// confirms the DOI, and OpenAlex lists the citing works under the card.
func TestDetailsHandler_Related(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/json.php", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	mux.HandleFunc("/works/doi:10.1038/nature12373", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"https://openalex.org/W2159974629"}`))
	})
	mux.HandleFunc("/works", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") != "cites:W2159974629" {
			http.Error(w, "unexpected filter", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"meta":{"count":1980},"results":[{"doi":"https://doi.org/10.1103/RevModPhys.89.035002","display_name":"Quantum sensing","publication_year":2017,"cited_by_count":4041,"open_access":{"is_oa":true}}]}`))
	})
	mux.HandleFunc("/works/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":{"title":["Nanometre-scale thermometry in a living cell"],"container-title":["Nature"]}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1, EnrichEnabled: true}
	client := libgen.New(staticMirrors{srv.URL}, cfg, libgen.WithEnrichBaseURLs(srv.URL, srv.URL),
		libgen.WithOpenAlexBase(srv.URL, &openalex.Budget{}))

	res, out, err := detailsHandler(client, cfg, nil)(t.Context(), nil,
		DetailsInput{DOI: "10.1038/nature12373", Related: "cited_by", RelatedLimit: 1})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if out.Related == nil || out.Related.Total != 1980 || len(out.Related.Works) != 1 || out.Related.Works[0].DOI != "10.1103/revmodphys.89.035002" {
		t.Fatalf("related = %+v", out.Related)
	}
	if md := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(md, "### Cited by (via OpenAlex)") {
		t.Errorf("markdown lacks the related section:\n%s", md)
	}
}
