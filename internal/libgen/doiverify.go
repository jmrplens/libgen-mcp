package libgen

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// doiVerifyTimeout is the hard wall-clock budget for a single DOI corroboration
// lookup. It is tighter than enrichTimeout because this check runs on the default
// get_details path, where enrichment does not: a caller who asked for nothing but
// a catalog record must not wait long for it, and an unanswered check degrades to
// DOIUnverified rather than blocking.
const doiVerifyTimeout = 4 * time.Second

// DOIVerdict is the outcome of corroborating a catalog record's DOI against
// Crossref, the registry that owns the DOI-to-work link. A catalog record states
// that link without any authority to; this is how much we were able to confirm.
type DOIVerdict string

const (
	// DOIUnverified means the link could not be checked at all — no DOI, no title
	// to compare, corroboration disabled, the registry unreachable, or the two
	// titles too close to call apart. It is the safe default: nothing is asserted.
	DOIUnverified DOIVerdict = "unverified"
	// DOIConfirmed means Crossref registers this DOI to a work whose title matches
	// the record's, so the record may state the DOI as its own.
	DOIConfirmed DOIVerdict = "confirmed"
	// DOIMismatch means Crossref registers this DOI to a plainly different work.
	// The record's DOI field is wrong and must never be repeated as fact.
	DOIMismatch DOIVerdict = "mismatch"
)

// DOICheck is the result of a DOI corroboration: the verdict plus, when the
// registry answered, the title it holds for that DOI — which is what makes a
// mismatch explainable to the caller instead of merely asserted.
type DOICheck struct {
	Verdict       DOIVerdict
	CrossrefTitle string
}

// VerifyDOI asks Crossref which work owns doi and compares its title with
// recordTitle, the title the catalog record claims. It NEVER returns an error and
// never blocks past doiVerifyTimeout: every failure — no DOI, no title, a
// transport error, an unknown DOI — degrades to DOIUnverified, so a third-party
// outage can weaken a citation but can never break the call.
func (c *Client) VerifyDOI(ctx context.Context, doi, recordTitle string) DOICheck {
	doi, recordTitle = strings.TrimSpace(doi), strings.TrimSpace(recordTitle)
	if doi == "" || recordTitle == "" {
		return DOICheck{Verdict: DOIUnverified}
	}
	ctx, cancel := context.WithTimeout(ctx, doiVerifyTimeout)
	defer cancel()
	work := c.fetchCrossref(ctx, doi)
	if work == nil {
		return DOICheck{Verdict: DOIUnverified}
	}
	return CheckDOITitle(recordTitle, work.Title)
}

// CheckDOITitle is the pure half of VerifyDOI: it judges a record title against
// the title Crossref holds for the DOI, with no network of its own. It is exported
// so a caller that already fetched the Crossref work (get_details with enrich=true)
// can reach a verdict without asking the registry the same question twice.
func CheckDOITitle(recordTitle, crossrefTitle string) DOICheck {
	check := DOICheck{Verdict: DOIUnverified, CrossrefTitle: strings.TrimSpace(crossrefTitle)}
	overlap, ok := titleOverlap(recordTitle, check.CrossrefTitle)
	switch {
	case !ok:
		// One of the titles carries no comparable words, so there is nothing to
		// judge. Silence is the honest answer, not an accusation.
	case overlap >= titleConfirmRatio:
		check.Verdict = DOIConfirmed
	case overlap <= titleMismatchRatio:
		check.Verdict = DOIMismatch
	}
	return check
}

// titleConfirmRatio and titleMismatchRatio bracket the containment score at which
// two titles are taken to name the same work, or plainly different ones. The band
// between them is deliberately left as DOIUnverified: two papers in one series
// ("Hallmarks of Cancer" / "Hallmarks of Aging") land there, and dropping the DOI
// quietly is right where calling the catalog a liar would not be.
const (
	titleConfirmRatio  = 0.6
	titleMismatchRatio = 0.2
)

// titleMarkup matches the inline JATS/HTML tags Crossref titles carry (<i>, <sub>,
// <mml:math>…), which are formatting rather than words and are dropped whole so
// they cannot dilute the comparison.
var titleMarkup = regexp.MustCompile(`<[^>]*>`)

// titleStopwords are the function words skipped when comparing titles: they say
// nothing about which work a title names, and their presence in both of two
// unrelated titles would otherwise register as agreement.
var titleStopwords = map[string]bool{
	"a": true, "an": true, "and": true, "as": true, "at": true, "by": true,
	"for": true, "from": true, "in": true, "of": true, "on": true, "or": true,
	"the": true, "to": true, "with": true,
}

// titleOverlap scores how far the shorter of two titles is contained in the longer,
// as |A∩B| / min(|A|,|B|) over their content words. Containment rather than
// symmetric similarity, because a catalog record legitimately drops a subtitle the
// registry keeps ("Hallmarks of Cancer" vs "Hallmarks of Cancer: The Next
// Generation") and that is agreement, not disagreement. ok is false when either
// title has no content words left to compare.
func titleOverlap(a, b string) (score float64, ok bool) {
	ta, tb := titleTokens(a), titleTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0, false
	}
	shorter, longer := ta, tb
	if len(longer) < len(shorter) {
		shorter, longer = longer, shorter
	}
	common := 0
	for tok := range shorter {
		if longer[tok] {
			common++
		}
	}
	return float64(common) / float64(len(shorter)), true
}

// titleTokens reduces a title to the set of its content words: markup stripped,
// lowercased, split on everything that is not a letter or digit, and stopwords
// dropped. A set (not a list) because a repeated word should not count twice
// toward agreement.
func titleTokens(s string) map[string]bool {
	cleaned := titleMarkup.ReplaceAllString(s, " ")
	fields := strings.FieldsFunc(strings.ToLower(cleaned), isTitleSeparator)
	out := make(map[string]bool, len(fields))
	for _, f := range fields {
		if !titleStopwords[f] {
			out[f] = true
		}
	}
	return out
}

// isTitleSeparator reports whether r ends a title word. Anything that is not a
// letter or a digit does — including the em dashes and curly quotes a typesetter
// swaps in — so the same title punctuated two ways tokenizes identically. The test
// is Unicode-wide, not ASCII-wide, or an accented or non-Latin title would tokenize
// to nothing and be permanently unverifiable.
func isTitleSeparator(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}
