package discovery

import (
	"context"
	"fmt"
	"strconv"
)

// The publication years a range may name. They bound what the search tool accepts
// and fill an open side of a range for a provider whose API needs both ends: no
// catalog this server reaches dates a record before the first, and a year past the
// second is a typo, not a request.
const (
	// MinYear is the earliest year a range may start at.
	MinYear = 1000
	// MaxYear is the latest year a range may end at.
	MaxYear = 2100
)

// YearRange bounds a search to records published in From through To, inclusive. A
// zero bound is open on that side, and the zero value bounds nothing.
type YearRange struct {
	// From is the earliest publication year kept, or 0 for no lower bound.
	From int
	// To is the latest publication year kept, or 0 for no upper bound.
	To int
}

// IsZero reports whether the range bounds nothing.
func (y YearRange) IsZero() bool { return y.From == 0 && y.To == 0 }

// Validate refuses a bound outside [MinYear, MaxYear] and a range that ends before
// it starts, naming the argument the search tool takes each bound from.
func (y YearRange) Validate() error {
	for _, b := range []struct {
		name string
		year int
	}{{"year_from", y.From}, {"year_to", y.To}} {
		if b.year != 0 && (b.year < MinYear || b.year > MaxYear) {
			return fmt.Errorf("%s must be a year between %d and %d, got %d", b.name, MinYear, MaxYear, b.year)
		}
	}
	if y.From != 0 && y.To != 0 && y.From > y.To {
		return fmt.Errorf("year_from (%d) is after year_to (%d): swap them, or drop one to leave that side open", y.From, y.To)
	}
	return nil
}

// Bounds returns the range with an open side filled from MinYear or MaxYear, for a
// provider whose query syntax takes a closed interval.
func (y YearRange) Bounds() (from, to int) {
	from, to = y.From, y.To
	if from == 0 {
		from = MinYear
	}
	if to == 0 {
		to = MaxYear
	}
	return from, to
}

// Contains reports whether year falls inside the range. An open side admits
// everything on it.
func (y YearRange) Contains(year int) bool {
	from, to := y.Bounds()
	return year >= from && year <= to
}

// Admits reports whether a record whose year is written as year belongs in a
// search bounded by the range. The zero range admits everything. Otherwise a
// record whose year cannot be read is admitted only when the provider applied the
// range itself (pushed), because then the provider's own date put it there, and
// nothing here can show it is wrong.
func (y YearRange) Admits(year string, pushed bool) bool {
	if y.IsZero() {
		return true
	}
	n, ok := ParseYear(year)
	if !ok {
		return pushed
	}
	return y.Contains(n)
}

// ParseYear reads the first run of four digits in s as a year, which is how every
// catalog here writes one whatever surrounds it: "2019", "2019-05-01", "c1999",
// "[1887?]". It reports false when s holds no such run.
func ParseYear(s string) (int, bool) {
	for i := 0; i+4 <= len(s); i++ {
		if !isFourDigits(s[i:i+4]) || (i > 0 && isDigit(s[i-1])) || (i+4 < len(s) && isDigit(s[i+4])) {
			continue
		}
		n, err := strconv.Atoi(s[i : i+4])
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

// isFourDigits reports whether s is exactly four ASCII digits.
func isFourDigits(s string) bool {
	if len(s) != 4 {
		return false
	}
	for i := range len(s) {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

// isDigit reports whether c is an ASCII digit.
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// RangeSearcher is a Provider whose API can bound a search to publication years
// itself. Federate asks such a provider with the range in its query, and filters
// every other provider's results after they arrive, so a range always holds: the
// difference is only whether the provider's page was spent on records the range
// then drops.
//
// SearchYears is best-effort exactly as Search is: only a context error is
// returned. A zero range must behave as Search.
type RangeSearcher interface {
	Provider
	// SearchYears returns up to limit results for the query published within
	// years.
	SearchYears(ctx context.Context, query string, limit int, years YearRange) ([]DiscoveryResult, error)
}

// searchProvider runs one provider for a federated search, pushing years into the
// provider's own query when it can take them, and filters what comes back to the
// range. pushed reports whether the provider applied the range itself.
func searchProvider(ctx context.Context, p Provider, query string, limit int, years YearRange) ([]DiscoveryResult, error) {
	var (
		res    []DiscoveryResult
		err    error
		pushed bool
	)
	if rs, ok := p.(RangeSearcher); ok && !years.IsZero() {
		res, err = rs.SearchYears(ctx, query, limit, years)
		pushed = true
	} else {
		res, err = p.Search(ctx, query, limit)
	}
	if err != nil || years.IsZero() {
		return res, err
	}
	kept := make([]DiscoveryResult, 0, len(res))
	for _, r := range res {
		if years.Admits(r.Year, pushed) {
			kept = append(kept, r)
		}
	}
	return kept, nil
}
