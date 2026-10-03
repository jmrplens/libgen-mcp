// Resolving a pasted, free-text reference to the DOI of the work it names.

package libgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CitationResolved and CitationUnresolved are the two values of
// [CitationMatch.Status].
const (
	CitationResolved   = "resolved"
	CitationUnresolved = "unresolved"
)

// citationRows is how many Crossref candidates a citation lookup asks for. The
// judgement needs two (the best and its nearest rival), and an unresolved lookup
// hands the rest back to the caller to choose from, so a handful is enough
// without turning the answer into a search page.
const citationRows = 5

// citationTimeout bounds a whole citation lookup. The 30 references of the
// corpus each answered in 0.2 to 0.7 seconds on 2026-10-03, but a search is
// what a busy registry slows first, so it gets more than doiVerifyTimeout. A
// caller who pasted a reference is still waiting on one answer and must not
// wait on a stalled registry.
const citationTimeout = 8 * time.Second

// CitationMaxRunes caps the reference a caller may paste. A real reference,
// even one listing every author of a consortium paper, fits well inside it, and
// Crossref only reads the leading part of a long query anyway.
const CitationMaxRunes = 1000

// The two thresholds a candidate must clear to be taken as the work a citation
// names. Both were chosen from 30 real references, good and bad, measured
// against the live API on 2026-10-03 and kept as
// testdata/citation_corpus.json, which TestJudgeCitation_Corpus replays.
//
// citationMinScoreRatio is the margin: the best candidate's Crossref score
// divided by the runner-up's. Every reference whose top hit was the right work
// scored at least 1.24 (AlphaFold, against its own companion paper), and every
// reference with no right answer in Crossref (a NeurIPS paper, an arXiv
// preprint, a book, two fabricated references) scored at most 1.11. A ratio is
// used rather than a difference because Crossref's scores grow with the length
// of the query, so a fixed gap would mean something different for a terse
// reference and a full one.
//
// citationMinTitleCoverage is how much of the best candidate's title must
// appear in the citation. The margin alone is not enough: "The FAIR Guiding
// Principles ..." put a paper whose title merely quotes it ahead by 1.30, and a
// book review of Antifragile led by 1.11. Every right answer in the corpus
// covered its title in full, and the wrong leaders covered at most 0.73, so
// 0.9 leaves room for one lost word in a long title and for nothing more.
const (
	citationMinScoreRatio    = 1.2
	citationMinTitleCoverage = 0.9
)

// errCitationSearch is the failure of the Crossref query itself: a transport
// error, a non-200 answer or a body that does not decode. Its text names the
// registry and nothing else, since the request URL carries the caller's text
// and, in the polite pool, the operator's contact address in the User-Agent.
var errCitationSearch = errors.New("the Crossref citation search did not answer")

// CitationCandidate is one Crossref work a citation lookup considered.
type CitationCandidate struct {
	DOI       string  `json:"doi" jsonschema:"the candidate's DOI, which get_details accepts as doi"`
	Title     string  `json:"title,omitempty" jsonschema:"title Crossref registers for the DOI"`
	Authors   string  `json:"authors,omitempty" jsonschema:"first authors, with et al. when there are more"`
	Year      int     `json:"year,omitempty" jsonschema:"year of issue"`
	Container string  `json:"container_title,omitempty" jsonschema:"journal, proceedings or book it appeared in"`
	Score     float64 `json:"score" jsonschema:"Crossref relevance score for this citation"`
}

// CitationMatch is the outcome of a citation lookup: either one DOI the
// citation was resolved to, or the candidates Crossref offered and why none of
// them was chosen. It never carries both a DOI and a refusal.
type CitationMatch struct {
	Status     string              `json:"status" jsonschema:"resolved (one work clearly matched) or unresolved (none was chosen)"`
	DOI        string              `json:"doi,omitempty" jsonschema:"DOI the citation resolved to, only when resolved"`
	Title      string              `json:"title,omitempty" jsonschema:"title Crossref registers for that DOI, only when resolved"`
	Reason     string              `json:"reason,omitempty" jsonschema:"why no candidate was chosen, only when unresolved"`
	Candidates []CitationCandidate `json:"candidates,omitempty" jsonschema:"what Crossref offered, best first, only when unresolved. Call get_details with the doi of the right one"`
}

// IsResolved reports whether the lookup settled on one DOI.
func (m CitationMatch) IsResolved() bool { return m.Status == CitationResolved }

// ResolveCitation asks Crossref which work a free-text reference names, and
// accepts its best candidate only when [JudgeCitation] does. The query goes to
// the keyless polite pool exactly as enrichment does, through the same limiter.
//
// It returns an error only when Crossref could not be asked at all. A
// reference with no convincing match is not an error: it is an unresolved
// match carrying the candidates, so the caller can choose among them.
func (c *Client) ResolveCitation(ctx context.Context, citation string) (CitationMatch, error) {
	ctx, cancel := context.WithTimeout(ctx, citationTimeout)
	defer cancel()
	candidates, err := c.searchCitation(ctx, citation)
	if err != nil {
		return CitationMatch{}, err
	}
	return JudgeCitation(citation, candidates), nil
}

// searchCitation runs the Crossref bibliographic query and decodes its
// candidates, best first.
func (c *Client) searchCitation(ctx context.Context, citation string) ([]CitationCandidate, error) {
	q := url.Values{}
	q.Set("query.bibliographic", citation)
	q.Set("rows", strconv.Itoa(citationRows))
	q.Set("select", "DOI,title,author,issued,container-title,score")
	resp := c.enrichGet(ctx, c.crossrefURL()+"/works?"+q.Encode(), c.enrichLimiter)
	if resp == nil {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%w: %w", errCitationSearch, err)
		}
		return nil, errCitationSearch
	}
	defer func() { _ = resp.Body.Close() }()
	candidates, err := parseCitationCandidates(io.LimitReader(resp.Body, enrichMaxBody))
	if err != nil {
		return nil, errCitationSearch
	}
	return candidates, nil
}

// crossrefSearchItem is the subset of a Crossref works-search item a citation
// lookup reads.
type crossrefSearchItem struct {
	DOI            string            `json:"DOI"`
	Title          []string          `json:"title"`
	Author         []crossrefAuthor  `json:"author"`
	Issued         crossrefPublished `json:"issued"`
	ContainerTitle []string          `json:"container-title"`
	Score          float64           `json:"score"`
}

// crossrefAuthor is one Crossref contributor: a person carries given and
// family names, an organization only a name.
type crossrefAuthor struct {
	Given  string `json:"given"`
	Family string `json:"family"`
	Name   string `json:"name"`
}

// crossrefSearchEnvelope is the `{"message":{"items":[...]}}` wrapper of a
// works-search response.
type crossrefSearchEnvelope struct {
	Message struct {
		Items []crossrefSearchItem `json:"items"`
	} `json:"message"`
}

// parseCitationCandidates decodes a works-search response into candidates,
// sorted by score with the best first. An item with no DOI is dropped, since
// it is nothing a caller could look up.
func parseCitationCandidates(r io.Reader) ([]CitationCandidate, error) {
	var env crossrefSearchEnvelope
	if err := json.NewDecoder(r).Decode(&env); err != nil {
		return nil, err
	}
	out := make([]CitationCandidate, 0, len(env.Message.Items))
	for _, it := range env.Message.Items {
		if strings.TrimSpace(it.DOI) == "" {
			continue
		}
		cand := CitationCandidate{
			DOI:     strings.TrimSpace(it.DOI),
			Authors: candidateAuthors(it.Author),
			Score:   it.Score,
		}
		if len(it.Title) > 0 {
			cand.Title = strings.TrimSpace(titleMarkup.ReplaceAllString(it.Title[0], ""))
		}
		if len(it.ContainerTitle) > 0 {
			cand.Container = strings.TrimSpace(it.ContainerTitle[0])
		}
		if dp := it.Issued.DateParts; len(dp) > 0 && len(dp[0]) > 0 {
			cand.Year = dp[0][0]
		}
		out = append(out, cand)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}

// candidateAuthorsShown is how many names a candidate lists before "et al.".
const candidateAuthorsShown = 3

// candidateAuthors renders a candidate's first authors as one line, so a
// caller choosing among candidates can tell two papers of one title apart
// without the list growing with a consortium's membership.
func candidateAuthors(authors []crossrefAuthor) string {
	names := make([]string, 0, candidateAuthorsShown)
	for _, a := range authors {
		name := strings.TrimSpace(strings.TrimSpace(a.Given) + " " + strings.TrimSpace(a.Family))
		if name == "" {
			name = strings.TrimSpace(a.Name)
		}
		if name == "" {
			continue
		}
		if len(names) == candidateAuthorsShown {
			return strings.Join(names, ", ") + " et al."
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// JudgeCitation decides whether the best of a citation's Crossref candidates is
// the work the citation names. It is the pure half of [Client.ResolveCitation],
// exported so the thresholds can be replayed against recorded answers.
//
// It accepts only when the best candidate clears both thresholds: it stands
// out from the runner-up by citationMinScoreRatio, and its title is in the
// citation to citationMinTitleCoverage. Anything else is unresolved, with every
// candidate handed back, because a DOI asserted on a near miss puts the wrong
// work into somebody's bibliography under a confident label.
func JudgeCitation(citation string, candidates []CitationCandidate) CitationMatch {
	unresolved := CitationMatch{Status: CitationUnresolved, Candidates: candidates}
	if len(candidates) == 0 {
		unresolved.Reason = "Crossref offered no candidate for this citation."
		return unresolved
	}
	best := candidates[0]
	if ratio := scoreRatio(candidates); ratio < citationMinScoreRatio {
		unresolved.Reason = fmt.Sprintf("No candidate stands out: the best scores %.1f against %.1f for the next, "+
			"and a match needs a lead of %.0f%%.", best.Score, candidates[1].Score, (citationMinScoreRatio-1)*100)
		return unresolved
	}
	if coverage := CitationTitleCoverage(best.Title, citation); coverage < citationMinTitleCoverage {
		unresolved.Reason = fmt.Sprintf("The best candidate leads, but only %.0f%% of its title appears in the citation, "+
			"so it may be a different work that shares some of its words.", coverage*100)
		return unresolved
	}
	return CitationMatch{Status: CitationResolved, DOI: best.DOI, Title: best.Title}
}

// scoreRatio is the best candidate's score over the runner-up's. A lone
// candidate, or a scored best against a runner-up Crossref scored at zero, has
// no rival to be compared with, so the margin is unbounded and the title check
// decides alone. Two candidates both at zero are a tie, not a lead.
func scoreRatio(candidates []CitationCandidate) float64 {
	switch {
	case len(candidates) < 2:
		return math.Inf(1)
	case candidates[1].Score > 0:
		return candidates[0].Score / candidates[1].Score
	case candidates[0].Score > 0:
		return math.Inf(1)
	default:
		return 1
	}
}

// CitationTitleCoverage is the share of a candidate title's content words that
// the citation carries, from 0 to 1. Words are compared as [CheckDOITitle]
// compares them (markup, case, punctuation and stopwords dropped), and a word of
// five letters or more also matches one edit away, so a typo or a dropped
// diacritic in a pasted reference ("cancr", "Korper") does not cost the match.
//
// It is directional on purpose. A citation carries authors, a journal and pages
// besides the title, so the question is whether the title is inside it, never
// whether the two are alike. A title with no content words scores zero.
func CitationTitleCoverage(title, citation string) float64 {
	titleWords, citationWords := titleTokens(title), titleTokens(citation)
	if len(titleWords) == 0 {
		return 0
	}
	found := 0
	for word := range titleWords {
		if citationHasWord(citationWords, word) {
			found++
		}
	}
	return float64(found) / float64(len(titleWords))
}

// fuzzyWordMinRunes is the shortest word allowed to match one edit away.
// Below it a single edit is too large a share of the word: "cell" and "call"
// are one edit apart and nothing alike.
const fuzzyWordMinRunes = 5

// citationHasWord reports whether words holds word itself or, for a long
// enough word, one a single edit away from it.
func citationHasWord(words map[string]bool, word string) bool {
	if words[word] {
		return true
	}
	if len([]rune(word)) < fuzzyWordMinRunes {
		return false
	}
	for w := range words {
		if len([]rune(w)) >= fuzzyWordMinRunes && withinOneEdit([]rune(word), []rune(w)) {
			return true
		}
	}
	return false
}

// withinOneEdit reports whether a and b differ by at most one insertion,
// deletion or substitution.
func withinOneEdit(a, b []rune) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(b)-len(a) > 1 {
		return false
	}
	i := 0
	for i < len(a) && a[i] == b[i] {
		i++
	}
	if len(a) == len(b) {
		// One substitution at i, then the rest must agree.
		return i == len(a) || string(a[i+1:]) == string(b[i+1:])
	}
	// One insertion into a at i.
	return string(a[i:]) == string(b[i+1:])
}
