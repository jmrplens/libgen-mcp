// The works a DOI cites and the works citing it, from OpenAlex.

package libgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jmrplens/libgen-mcp/v2/internal/netguard"
	"github.com/jmrplens/libgen-mcp/v2/internal/openalex"
)

// The two kinds of related works a lookup can ask for.
const (
	RelatedReferences = "references"
	RelatedCitedBy    = "cited_by"
)

// RelatedKinds lists the values get_details accepts in related.
func RelatedKinds() []string { return []string{RelatedReferences, RelatedCitedBy} }

// Bounds on how many related works one lookup returns. The default is a list
// a reader takes in at a glance, and the ceiling keeps the answer a list rather
// than a bibliography: the whole set is one filtered query either way.
const (
	RelatedDefaultLimit = 10
	RelatedMaxLimit     = 25
)

// relatedMaxIDs is how many referenced works one filtered query names.
// OpenAlex accepts at most a hundred values in one OR filter, and a
// reference list longer than that is ranked from its first hundred entries.
const relatedMaxIDs = 100

// relatedTimeout bounds the whole lookup, both requests included. OpenAlex
// answered each in under half a second when measured, so this is a ceiling
// for a stalled API rather than a budget the normal case comes near.
const relatedTimeout = 8 * time.Second

// relatedFilterCost is what one filtered list query costs, in OpenAlex credits.
// The single-entity lookup that precedes it costs none.
const relatedFilterCost = 1

// relatedSelect is the field list each related work is fetched with.
const relatedSelect = "id,doi,display_name,publication_year,open_access,cited_by_count"

// RelatedWork is one work in a related-works list.
type RelatedWork struct {
	Title        string `json:"title,omitempty" jsonschema:"title OpenAlex holds for the work"`
	Year         int    `json:"year,omitempty" jsonschema:"publication year"`
	DOI          string `json:"doi,omitempty" jsonschema:"DOI, absent when OpenAlex has none. get_details and download accept it"`
	OpenAccess   bool   `json:"open_access" jsonschema:"true when OpenAlex knows a free-to-read copy"`
	CitedByCount int    `json:"cited_by_count,omitempty" jsonschema:"how many works OpenAlex counts citing it"`
}

// RelatedWorks is the answer to a related-works lookup.
type RelatedWorks struct {
	Kind  string        `json:"kind" jsonschema:"references (works this one cites) or cited_by (works citing this one)"`
	Total int           `json:"total,omitempty" jsonschema:"how many there are in all, as OpenAlex counts them"`
	Works []RelatedWork `json:"works,omitempty" jsonschema:"the most cited of them first, at most related_limit"`
	Note  string        `json:"note,omitempty" jsonschema:"why the list is empty or partial, when it is"`
}

// errOpenAlexBudget is a lookup the shared credit budget refused.
var errOpenAlexBudget = errors.New("the OpenAlex daily allowance is spent")

// openAlexURL returns the OpenAlex API root for this client.
func (c *Client) openAlexURL() string {
	if c.openAlexBaseOverride != "" {
		return c.openAlexBaseOverride
	}
	return openalex.APIBase
}

// openAlexBudgetOrShared returns the credit budget this client reports to.
func (c *Client) openAlexBudgetOrShared() *openalex.Budget {
	if c.openAlexBudget != nil {
		return c.openAlexBudget
	}
	return openalex.Shared()
}

// Related looks a DOI up in OpenAlex and lists the works it cites or the works
// citing it, the most cited first, at most limit of them. It costs one credit
// of the shared allowance: the work itself is a single-entity lookup, which
// OpenAlex does not charge, and the list is one filtered query.
//
// It never fails the call that asked: every failure, an unknown DOI included,
// is a RelatedWorks with a note saying what happened, because the related
// works are an addition to a record that was already found.
func (c *Client) Related(ctx context.Context, doi, kind string, limit int) RelatedWorks {
	out := RelatedWorks{Kind: kind}
	ctx, cancel := context.WithTimeout(ctx, relatedTimeout)
	defer cancel()
	work, err := c.openAlexWork(ctx, doi)
	if err != nil {
		out.Note = relatedFailureNote(err)
		return out
	}
	switch kind {
	case RelatedReferences:
		out.Total = work.ReferencedWorksCount
		if len(work.ReferencedWorks) == 0 {
			out.Note = "OpenAlex lists no references for this work."
			return out
		}
		ids := work.ReferencedWorks
		if len(ids) > relatedMaxIDs {
			out.Note = fmt.Sprintf("Ranked from the first %d of its %d references.", relatedMaxIDs, len(ids))
			ids = ids[:relatedMaxIDs]
		}
		out.Works, _, err = c.openAlexList(ctx, "openalex:"+strings.Join(shortIDs(ids), "|"), limit)
	default:
		out.Works, out.Total, err = c.openAlexList(ctx, "cites:"+shortID(work.ID), limit)
	}
	switch {
	case err != nil:
		out.Note = relatedFailureNote(err)
	case len(out.Works) > 0 || out.Note != "":
	case kind == RelatedCitedBy:
		out.Note = "OpenAlex knows no work that cites this one."
	default:
		out.Note = "OpenAlex returned none of the works this one references."
	}
	return out
}

// relatedFailureNote says why a lookup came back empty, naming nothing but
// the service: a transport error would print the request URL.
func relatedFailureNote(err error) string {
	switch {
	case errors.Is(err, errOpenAlexBudget):
		return "The OpenAlex daily allowance shared by this server is spent, so the list was not fetched. It resets at midnight UTC, and LIBGEN_MCP_OPENALEX_KEY raises it."
	case errors.Is(err, errOpenAlexNotFound):
		return "OpenAlex has no work with this DOI."
	default:
		return "OpenAlex did not answer, so the list is not available now."
	}
}

// errOpenAlexNotFound is a DOI OpenAlex does not know.
var errOpenAlexNotFound = errors.New("openalex: no such work")

// openAlexEntity is the part of a single-entity work lookup Related reads.
type openAlexEntity struct {
	ID                   string   `json:"id"`
	ReferencedWorks      []string `json:"referenced_works"`
	ReferencedWorksCount int      `json:"referenced_works_count"`
}

// openAlexWork fetches the work a DOI names, at no credit cost.
func (c *Client) openAlexWork(ctx context.Context, doi string) (openAlexEntity, error) {
	endpoint := c.openAlexURL() + "/works/doi:" + escapeDOIPath(doi) + "?" +
		url.Values{"select": {"id,referenced_works,referenced_works_count"}}.Encode()
	var work openAlexEntity
	if err := c.openAlexGet(ctx, endpoint, &work); err != nil {
		return openAlexEntity{}, err
	}
	if work.ID == "" {
		return openAlexEntity{}, errOpenAlexNotFound
	}
	return work, nil
}

// openAlexListItem is one work of a filtered list.
type openAlexListItem struct {
	DOI             string `json:"doi"`
	DisplayName     string `json:"display_name"`
	PublicationYear int    `json:"publication_year"`
	CitedByCount    int    `json:"cited_by_count"`
	OpenAccess      struct {
		IsOA bool `json:"is_oa"`
	} `json:"open_access"`
}

// openAlexListPage is a filtered list response.
type openAlexListPage struct {
	Meta struct {
		Count int `json:"count"`
	} `json:"meta"`
	Results []openAlexListItem `json:"results"`
}

// openAlexList runs one filtered works query, the most cited first, and
// returns the works and the total OpenAlex counted. It spends one credit from
// the shared budget, with no reserve held back: the reserve exists to keep
// searches from starving exactly this kind of lookup.
func (c *Client) openAlexList(ctx context.Context, filter string, limit int) ([]RelatedWork, int, error) {
	if !c.openAlexBudgetOrShared().Spend(relatedFilterCost, 0, time.Now()) {
		return nil, 0, errOpenAlexBudget
	}
	endpoint := c.openAlexURL() + "/works?" + url.Values{
		"filter":   {filter},
		"sort":     {"cited_by_count:desc"},
		"per_page": {strconv.Itoa(limit)},
		"select":   {relatedSelect},
	}.Encode()
	var page openAlexListPage
	if err := c.openAlexGet(ctx, endpoint, &page); err != nil {
		return nil, 0, err
	}
	works := make([]RelatedWork, 0, len(page.Results))
	for _, r := range page.Results {
		works = append(works, RelatedWork{
			Title:        strings.TrimSpace(r.DisplayName),
			Year:         r.PublicationYear,
			DOI:          bareDOI(r.DOI),
			OpenAccess:   r.OpenAccess.IsOA,
			CitedByCount: r.CitedByCount,
		})
	}
	return works, page.Meta.Count, nil
}

// openAlexGet issues one OpenAlex request with the optional key, reports the
// response's rate-limit headers to the shared budget, and decodes a 200 into
// into. A 404 is errOpenAlexNotFound, a 429 errOpenAlexBudget.
func (c *Client) openAlexGet(ctx context.Context, endpoint string, into any) error {
	req, err := openalex.NewRequest(ctx, endpoint, c.openAlexKey)
	if err != nil {
		return netguard.RedactTransportError(err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return netguard.RedactTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	c.openAlexBudgetOrShared().Observe(resp.StatusCode, resp.Header, time.Now())
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return errOpenAlexNotFound
	case http.StatusTooManyRequests:
		return errOpenAlexBudget
	default:
		return fmt.Errorf("openalex: HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, enrichMaxBody)).Decode(into)
}

// shortID reduces an OpenAlex work URL to its W-number.
func shortID(id string) string {
	return id[strings.LastIndex(id, "/")+1:]
}

// shortIDs reduces each OpenAlex work URL to its W-number.
func shortIDs(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = shortID(id)
	}
	return out
}
