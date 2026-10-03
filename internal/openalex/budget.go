package openalex

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Credit costs and the keyless reserve, as OpenAlex meters them. The figures were
// read off the X-RateLimit-Credits-Used header of live responses on 2026-10-03:
// a works search (search=) costs ten credits, a filtered list (filter=doi:...,
// filter=cites:...) one, and a single-entity lookup (works/doi:...) none, against a
// keyless allowance of a thousand credits per address per day, reset at midnight
// UTC.
const (
	// SearchCost is what one works search costs.
	SearchCost = 10
	// KeylessReserve is the part of a keyless daily allowance a search never
	// spends. It is kept for the one-credit filtered queries a record lookup makes
	// (get_details' citing and cited works), which would be refused with the rest
	// once the allowance is gone. The download chain's single-entity lookup does
	// not need it: OpenAlex documents that lookup as free and unlimited whatever
	// the budget ("Get a single entity: Unlimited", Example costs, read
	// 2026-10-03). A hundred credits is a tenth of the day, and a hundred filtered
	// queries.
	KeylessReserve = 100
)

// refusedBackoff is how long a refusal (429) that states no wait of its own keeps
// the budget closed. OpenAlex answers 429 both for an exhausted allowance and for
// more than a hundred requests a second, so a refusal with nothing to go on is
// treated as the shorter of the two rather than as the end of the day.
const refusedBackoff = time.Minute

// newWindowSlack is how far a reported reset may move before it is read as a new
// window. The header counts whole seconds to a fixed instant, so two responses in
// the same window disagree by rounding only, while the next day's is a day later.
const newWindowSlack = time.Minute

// Budget is what this process knows about its OpenAlex credit allowance, learned
// from the rate-limit headers of the responses it has read. It is safe for
// concurrent use, and its zero value is an empty budget that allows everything
// until a response says otherwise.
//
// It holds two separate things. The daily window is the remaining count and the
// instant it resets, and within one window the count only ever goes down:
// responses can arrive out of order, and the smaller figure is the more recent
// one. A spend granted by [Budget.Spend] is deducted at once, so concurrent
// searches cannot all pass the same check before the first of them reports back.
// The refusal deadline is when a 429 stops applying. It is kept apart from the
// window because a 429 for a burst asks for seconds, and folding it into a window
// that ends at midnight would keep the budget closed for the rest of the day.
type Budget struct {
	mu          sync.Mutex
	known       bool
	remaining   int
	resetAt     time.Time
	closedUntil time.Time
}

// shared is the process-wide budget every OpenAlex caller reports to.
var shared Budget

// Shared returns the process-wide budget. OpenAlex meters by address, so every
// request this process makes draws on one allowance, and every caller observes
// into, and spends from, this one value.
func Shared() *Budget { return &shared }

// Observe records the rate-limit state a response reports, given its status code
// and headers. A response carrying both rate-limit headers updates the daily
// window. A 429 also closes the budget, for its Retry-After, else until the reset
// it states, else for [refusedBackoff]: an exhausted allowance reports a
// remaining count of zero with its reset, and that closes the window on its own.
func (b *Budget) Observe(status int, header http.Header, now time.Time) {
	remaining, haveRemaining := headerWhole(header, "X-RateLimit-Remaining")
	reset, haveReset := headerWhole(header, "X-RateLimit-Reset")
	if haveRemaining && haveReset {
		b.record(remaining, now.Add(time.Duration(reset)*time.Second), now)
	}
	if status == http.StatusTooManyRequests {
		b.close(now.Add(refusalWait(header)))
	}
}

// record stores an observation, starting a new window when the old one has ended
// or the reported reset has moved to a later one, and otherwise keeping the
// smaller remaining figure.
func (b *Budget) record(remaining int, resetAt, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.known || !now.Before(b.resetAt) || resetAt.Sub(b.resetAt) > newWindowSlack {
		b.known, b.remaining, b.resetAt = true, remaining, resetAt
		return
	}
	b.remaining = min(b.remaining, remaining)
}

// close keeps the budget refusing until the given instant, extending an earlier
// deadline but never shortening one.
func (b *Budget) close(until time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if until.After(b.closedUntil) {
		b.closedUntil = until
	}
}

// Spend reports whether a request costing cost credits may go ahead while
// leaving reserve credits untouched, and deducts the cost when it may. A budget
// closed by a refusal refuses until its deadline. An unknown window, or one that
// has ended, allows the request: the response it brings back is what makes the
// budget known again.
func (b *Budget) Spend(cost, reserve int, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if now.Before(b.closedUntil) {
		return false
	}
	if !b.known || !now.Before(b.resetAt) {
		return true
	}
	if b.remaining-cost < reserve {
		return false
	}
	b.remaining -= cost
	return true
}

// Refund gives back credits a granted spend did not use, because the request it
// paid for was never sent. It changes nothing once the window has ended.
func (b *Budget) Refund(cost int, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.known && now.Before(b.resetAt) {
		b.remaining += cost
	}
}

// Remaining returns the credits this process believes are left and when the
// window ends. ok is false when no response has reported them yet or the window
// has ended.
func (b *Budget) Remaining(now time.Time) (credits int, resetAt time.Time, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.known || !now.Before(b.resetAt) {
		return 0, time.Time{}, false
	}
	return b.remaining, b.resetAt, true
}

// refusalWait is how long a 429 keeps the budget closed: its Retry-After when that
// is a whole number of seconds, else the X-RateLimit-Reset it states, else
// [refusedBackoff].
func refusalWait(h http.Header) time.Duration {
	for _, name := range []string{"Retry-After", "X-RateLimit-Reset"} {
		if s, ok := headerWhole(h, name); ok {
			return time.Duration(s) * time.Second
		}
	}
	return refusedBackoff
}

// headerWhole reads a header holding a non-negative whole number.
func headerWhole(h http.Header, name string) (int, bool) {
	v, err := strconv.Atoi(strings.TrimSpace(h.Get(name)))
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}
