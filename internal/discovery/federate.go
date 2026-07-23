package discovery

import (
	"context"
	"strings"
	"sync"
	"unicode"
)

// Federate runs every provider concurrently for the query and returns the merged,
// deduped results. It is best-effort: a provider that errors or times out
// contributes nothing and never sinks the others. Order: libgen-facing callers
// present these AFTER their own results.
//
// Results are deduped across providers first by normalized DOI, then by a
// normalized title+year key, keeping the first occurrence in provider order (and
// original order within a provider).
func Federate(ctx context.Context, query string, limit int, providers ...Provider) []DiscoveryResult {
	perProvider := make([][]DiscoveryResult, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func(idx int, prov Provider) {
			defer wg.Done()
			res, err := prov.Search(ctx, query, limit)
			if err != nil {
				return // best-effort: a failing provider contributes nothing.
			}
			perProvider[idx] = res
		}(i, p)
	}
	wg.Wait()
	return dedupResults(perProvider)
}

// dedupResults flattens the per-provider result slices in provider order and drops
// any result whose DOI, or whose title+year key, was already seen. Keeping the
// per-provider layout makes the merge deterministic regardless of goroutine timing.
func dedupResults(perProvider [][]DiscoveryResult) []DiscoveryResult {
	seenDOI := map[string]bool{}
	seenTitle := map[string]bool{}
	merged := []DiscoveryResult{}
	for _, batch := range perProvider {
		for _, r := range batch {
			if isDuplicate(r, seenDOI, seenTitle) {
				continue
			}
			merged = append(merged, r)
		}
	}
	return merged
}

// isDuplicate reports whether r was already seen by DOI or title+year, recording
// its keys as seen when it is not. A result with neither a DOI nor a title+year key
// is always kept (it carries no identity to dedup on).
func isDuplicate(r DiscoveryResult, seenDOI, seenTitle map[string]bool) bool {
	if doi := NormalizeDOI(r.DOI); doi != "" {
		if seenDOI[doi] {
			return true
		}
		seenDOI[doi] = true
	}
	if key := TitleYearKey(r.Title, r.Year); key != "" {
		if seenTitle[key] {
			return true
		}
		seenTitle[key] = true
	}
	return false
}

// DefaultProviders returns the standard keyless open-access providers in the order
// arxiv, crossref, openlibrary. email is the Crossref polite-pool mailto contact
// (typically cfg.UnpaywallEmail); pass "" to omit it.
func DefaultProviders(email string) []Provider {
	return []Provider{NewArxiv(), NewCrossref(email), NewOpenLibrary()}
}

// NormalizeDOI lowercases and trims a DOI so two spellings of the same identifier
// compare equal. An all-whitespace or empty DOI normalizes to "".
func NormalizeDOI(doi string) string {
	return strings.ToLower(strings.TrimSpace(doi))
}

// TitleYearKey builds a dedup key from a title and year: the title lowercased with
// punctuation stripped and internal whitespace collapsed, joined to the trimmed
// year. It returns "" when the title is empty, so a result without a title is never
// deduped by this key.
func TitleYearKey(title, year string) string {
	norm := normalizeTitle(title)
	if norm == "" {
		return ""
	}
	return norm + "|" + strings.TrimSpace(year)
}

// normalizeTitle lowercases a title, replaces every non-alphanumeric rune with a
// space, and collapses runs of whitespace to a single space, so punctuation and
// spacing differences do not defeat title-based dedup.
func normalizeTitle(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteRune(' ')
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
