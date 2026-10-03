package openalex

import (
	"net/http"
	"time"
)

// Refusal is what an OpenAlex response says about being let through: nothing, a
// burst of requests to back off from for a moment, or a daily allowance spent.
type Refusal int

// The kinds of refusal [ClassifyRefusal] tells apart.
const (
	// RefusalNone is any response but a 429.
	RefusalNone Refusal = iota
	// RefusalBurst is a 429 for too many requests at once. It clears in seconds
	// and says nothing about the daily allowance.
	RefusalBurst
	// RefusalSpent is a 429 for an allowance that is gone. It clears when the
	// allowance resets, at midnight UTC.
	RefusalSpent
)

// String names the kind, for logs and messages.
func (r Refusal) String() string {
	switch r {
	case RefusalBurst:
		return "burst"
	case RefusalSpent:
		return "spent"
	default:
		return "none"
	}
}

// refusedBackoff is how long a 429 that states no Retry-After keeps the budget
// closed. OpenAlex answers 429 both for a burst and for a spent allowance, and a
// refusal with nothing to go on is treated as the shorter of the two, so one odd
// answer cannot close the day.
const refusedBackoff = time.Minute

// burstRetryLimit is the longest Retry-After still read as a burst. Measured on
// 2026-10-03, a burst refusal asks for one second. A wait longer than a minute
// is one that only the end of the allowance explains.
const burstRetryLimit = time.Minute

// ClassifyRefusal says what kind of refusal a response is and how long to wait
// before asking again. It is the one reading of an OpenAlex 429 in this
// codebase, shared by the budget and by every caller that reports a refusal, so
// the budget and the message a caller writes cannot disagree.
//
// Only the Retry-After header decides. X-RateLimit-Remaining and
// X-RateLimit-Reset are not read: on a burst 429 OpenAlex reports Remaining 0 and
// the seconds to midnight while the allowance still holds hundreds of credits, so
// those two headers on a 429 would make every burst look like a spent day.
//
//   - not a 429: RefusalNone, no wait;
//   - Retry-After of at most a minute: RefusalBurst, for that long;
//   - a longer Retry-After: RefusalSpent, for that long;
//   - no usable Retry-After: RefusalBurst, for [refusedBackoff].
func ClassifyRefusal(status int, header http.Header) (kind Refusal, wait time.Duration) {
	if status != http.StatusTooManyRequests {
		return RefusalNone, 0
	}
	seconds, ok := headerWhole(header, "Retry-After")
	if !ok {
		return RefusalBurst, refusedBackoff
	}
	wait = time.Duration(seconds) * time.Second
	if wait > burstRetryLimit {
		return RefusalSpent, wait
	}
	return RefusalBurst, wait
}
