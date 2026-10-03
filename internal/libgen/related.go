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
		out.Note = c.relatedFailureNote(err)
		return out
	}
	if kind == RelatedReferences {
		return c.relatedReferences(ctx, work, limit)
	}
	out.Works, out.Total, err = c.openAlexList(ctx, "cites:"+shortID(work.ID), limit)
	out.Note = c.listNote(err, len(out.Works), "OpenAlex knows no work that cites this one.")
	return out
}

// relatedReferences lists the works a work cites, ranked from the IDs OpenAlex
// names for them. Where the count OpenAlex states and the IDs it names
// disagree, or the IDs are more than one query can carry, the note says what
// the list was ranked from.
func (c *Client) relatedReferences(ctx context.Context, work openAlexEntity, limit int) RelatedWorks {
	out := RelatedWorks{Kind: RelatedReferences, Total: work.ReferencedWorksCount}
	ids := work.ReferencedWorks
	if len(ids) == 0 {
		out.Note = "OpenAlex lists no references for this work."
		if work.ReferencedWorksCount > 0 {
			out.Note = fmt.Sprintf("OpenAlex counts %d references for this work but names none of them.", work.ReferencedWorksCount)
		}
		return out
	}
	var notes []string
	if work.ReferencedWorksCount != len(ids) {
		notes = append(notes, fmt.Sprintf("OpenAlex counts %d references but names %d of them, and only the named ones can be listed.",
			work.ReferencedWorksCount, len(ids)))
	}
	if len(ids) > relatedMaxIDs {
		notes = append(notes, fmt.Sprintf("Ranked from the first %d of the %d it names, the most one query can carry.", relatedMaxIDs, len(ids)))
		ids = ids[:relatedMaxIDs]
	}
	works, _, err := c.openAlexList(ctx, "openalex:"+strings.Join(shortIDs(ids), "|"), limit)
	out.Works = works
	if note := c.listNote(err, len(works), "OpenAlex returned none of the works this one references."); note != "" {
		notes = append(notes, note)
	}
	out.Note = strings.Join(notes, " ")
	return out
}

// listNote is the note for a list query: why it failed, what an empty answer
// means, or nothing for a list that came back.
func (c *Client) listNote(err error, works int, empty string) string {
	switch {
	case err != nil:
		return c.relatedFailureNote(err)
	case works == 0:
		return empty
	}
	return ""
}

// relatedFailureNote says why a lookup came back empty, naming nothing but
// the service: a transport error would print the request URL. A refusal for
// going too fast and a spent daily allowance are told apart, since one passes
// in seconds and the other at midnight UTC, and the key is suggested only to a
// server that has none.
func (c *Client) relatedFailureNote(err error) string {
	switch {
	case errors.Is(err, errOpenAlexBurst):
		return "OpenAlex asked this server to slow down, so the list was not fetched. Try again shortly."
	case errors.Is(err, errOpenAlexBudget):
		note := "The OpenAlex daily allowance shared by this server is spent, so the list was not fetched. It resets at midnight UTC."
		if strings.TrimSpace(c.openAlexKey) == "" {
			note += " LIBGEN_MCP_OPENALEX_KEY raises it."
		}
		return note
	case errors.Is(err, errOpenAlexNotFound):
		return "OpenAlex has no work with this DOI."
	default:
		return "OpenAlex did not answer, so the list is not available now."
	}
}

// errOpenAlexBurst is a refusal for too many requests too fast, which passes
// in seconds, as opposed to errOpenAlexBudget, a spent daily allowance.
var errOpenAlexBurst = errors.New("OpenAlex asked for a slower pace")

// burstRetryCeiling is the longest Retry-After still read as a burst refusal
// rather than a spent day.
const burstRetryCeiling = time.Minute

// refusalKind tells a 429 for pace apart from one for a spent allowance: it is
// a burst when the response still reports credits left, or asks for a wait of
// a minute or less.
func refusalKind(h http.Header) error {
	if n, err := strconv.Atoi(strings.TrimSpace(h.Get("X-RateLimit-Remaining"))); err == nil && n > 0 {
		return errOpenAlexBurst
	}
	if s, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && time.Duration(s)*time.Second <= burstRetryCeiling {
		return errOpenAlexBurst
	}
	return errOpenAlexBudget
}

// spendRefusal says why the shared budget refused a spend: the daily
// allowance is spent when the window it knows has no credits left for it, and
// otherwise the budget is paused by a recent refusal.
func (c *Client) spendRefusal(cost int) error {
	if left, _, ok := c.openAlexBudgetOrShared().Remaining(time.Now()); ok && left < cost {
		return errOpenAlexBudget
	}
	return errOpenAlexBurst
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
	if _, err := c.openAlexGet(ctx, endpoint, &work); err != nil {
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
	budget := c.openAlexBudgetOrShared()
	if !budget.Spend(relatedFilterCost, 0, time.Now()) {
		return nil, 0, c.spendRefusal(relatedFilterCost)
	}
	endpoint := c.openAlexURL() + "/works?" + url.Values{
		"filter":   {filter},
		"sort":     {"cited_by_count:desc"},
		"per_page": {strconv.Itoa(limit)},
		"select":   {relatedSelect},
	}.Encode()
	var page openAlexListPage
	if answered, err := c.openAlexGet(ctx, endpoint, &page); err != nil {
		if !answered {
			budget.Refund(relatedFilterCost, time.Now())
		}
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
//
// answered reports whether OpenAlex answered at all, so a caller that paid for
// the request can take the credit back when it never reached the service.
func (c *Client) openAlexGet(ctx context.Context, endpoint string, into any) (answered bool, err error) {
	req, err := openalex.NewRequest(ctx, endpoint, c.openAlexKey)
	if err != nil {
		return false, netguard.RedactTransportError(err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, netguard.RedactTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	c.openAlexBudgetOrShared().Observe(resp.StatusCode, resp.Header, time.Now())
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return true, errOpenAlexNotFound
	case http.StatusTooManyRequests:
		return true, refusalKind(resp.Header)
	default:
		return true, fmt.Errorf("openalex: HTTP %d", resp.StatusCode)
	}
	return true, json.NewDecoder(io.LimitReader(resp.Body, enrichMaxBody)).Decode(into)
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
