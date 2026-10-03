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
	// spends. It is left for the lookups that cannot go elsewhere: the download
	// chain's openalex source and the filtered queries a record lookup makes, which
	// cost one credit or none and would be refused with the rest once the
	// allowance is gone. A hundred credits is a tenth of the day, and a hundred
	// filtered lookups.
	KeylessReserve = 100
)

// refusedBackoff is how long a refusal (429) that states no reset of its own keeps
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
// Within one window the remaining count only ever goes down: responses can
// arrive out of order, and the smaller figure is the more recent one. A spend
// granted by [Budget.Spend] is deducted at once, so concurrent searches cannot
// all pass the same check before the first of them reports back.
type Budget struct {
	mu        sync.Mutex
	known     bool
	remaining int
	resetAt   time.Time
}

// shared is the process-wide budget every OpenAlex caller reports to.
var shared Budget

// Shared returns the process-wide budget. OpenAlex meters by address, so every
// request this process makes draws on one allowance, and every caller observes
// into, and spends from, this one value.
func Shared() *Budget { return &shared }

// Observe records the rate-limit state a response reports, given its status code
// and headers. A response without the headers changes nothing, except a 429, which
// closes the budget until the reset it states, its Retry-After, or
// [refusedBackoff], in that order.
func (b *Budget) Observe(status int, header http.Header, now time.Time) {
	remaining, haveRemaining := headerWhole(header, "X-RateLimit-Remaining")
	reset, haveReset := headerWhole(header, "X-RateLimit-Reset")
	if status == http.StatusTooManyRequests {
		if !haveRemaining {
			remaining, haveRemaining = 0, true
		}
		if !haveReset {
			reset, haveReset = refusalWait(header), true
		}
	}
	if !haveRemaining || !haveReset {
		return
	}
	b.record(remaining, now.Add(time.Duration(reset)*time.Second), now)
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

// Spend reports whether a request costing cost credits may go ahead while
// leaving reserve credits untouched, and deducts the cost when it may. An unknown
// budget, or one whose window has ended, allows the request: the response it
// brings back is what makes the budget known again.
func (b *Budget) Spend(cost, reserve int, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.known || !now.Before(b.resetAt) {
		return true
	}
	if b.remaining-cost < reserve {
		return false
	}
	b.remaining -= cost
	return true
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

// refusalWait is how many seconds a 429 with no reset header keeps the budget
// closed: its Retry-After when that is a whole number of seconds, otherwise
// [refusedBackoff].
func refusalWait(h http.Header) int {
	if s, ok := headerWhole(h, "Retry-After"); ok {
		return s
	}
	return int(refusedBackoff / time.Second)
}

// headerWhole reads a header holding a non-negative whole number.
func headerWhole(h http.Header, name string) (int, bool) {
	v, err := strconv.Atoi(strings.TrimSpace(h.Get(name)))
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}
