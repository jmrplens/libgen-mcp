package libgen

import (
	"context"
	"errors"
	"net"
	"net/http"
)

// ErrSourceUnavailable marks a resolve failure that says nothing about the item and
// everything about the source: the host never answered, the request timed out, or
// the service replied with a transient status (5xx/429). Every item offered to that
// source next would fail the same way, so it is the signal the per-source cooldown
// acts on.
//
// It is exported so callers outside the package (notably the live e2e suite) can
// classify a chain failure with errors.Is instead of matching error text.
var ErrSourceUnavailable = errors.New("download source unavailable")

// ErrNotIndexed marks the opposite outcome: the source answered correctly and
// simply does not hold this item — not indexed, not open access, no preserved copy.
// That is a NORMAL, final answer about one item and says nothing about the source's
// health, so it must never put a source in cooldown: a provider that legitimately
// does not carry an item would otherwise be punished for being honest.
var ErrNotIndexed = errors.New("item not held by this download source")

// taggedError attaches one of the taxonomy sentinels to a source's own error
// WITHOUT changing its message: Error returns the underlying text verbatim while
// Unwrap exposes both the original error and the tag, so errors.Is finds either.
// Wrapping with fmt.Errorf would instead splice the sentinel's wording into every
// message, changing text that logs, tests and the e2e suite read.
type taggedError struct {
	// err is the source's own error, whose message and wrapped chain are preserved.
	err error
	// tag is the taxonomy sentinel this failure is being classified as.
	tag error
}

// Error returns the underlying error's message unchanged, so tagging is invisible
// to anything that renders the error.
func (e taggedError) Error() string { return e.err.Error() }

// Unwrap exposes both the underlying error and the taxonomy tag, which is what
// makes errors.Is match either of them.
func (e taggedError) Unwrap() []error { return []error{e.err, e.tag} }

// unavailable tags err as evidence that the source itself could not serve any item
// right now (transport failure, timeout, transient status), keeping its message
// intact.
func unavailable(err error) error { return taggedError{err: err, tag: ErrSourceUnavailable} }

// notIndexed tags err as the source's correct answer that it does not hold this
// particular item, keeping its message intact.
func notIndexed(err error) error { return taggedError{err: err, tag: ErrNotIndexed} }

// unavailableStatus tags err as unavailability when status is a transient HTTP
// status — 5xx (the service is broken) or 429 (it is asking us to back off) — and
// returns it untouched otherwise, since a 4xx is an answer about the request, not a
// verdict on the service.
func unavailableStatus(status int, err error) error {
	if status >= http.StatusInternalServerError || status == http.StatusTooManyRequests {
		return unavailable(err)
	}
	return err
}

// cooldownWorthy reports whether err is evidence that a source is unavailable, and
// therefore worth setting aside for a while, as opposed to a correct answer about
// one item.
//
// ctx is the caller's context: when it is already done, nothing was learned about
// the source (the download was abandoned from this side), so no cooldown is
// recorded. Otherwise a failure counts when it is tagged ErrSourceUnavailable, when
// every libgen mirror was unreachable (ErrAllMirrorsFailed, the same diagnosis one
// layer down), when the per-source resolve budget expired — a source that cannot
// produce a URL inside its whole budget is exactly the case this exists for — or
// when the error is a network timeout. Anything else, including an unclassified
// failure, is deliberately NOT cooldown-worthy: setting a healthy source aside is
// more expensive than retrying an unhealthy one.
func cooldownWorthy(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	// A clean miss wins over every other signal: it is a correct answer about the
	// item, whatever else the error chain happens to carry.
	if errors.Is(err, ErrNotIndexed) {
		return false
	}
	if errors.Is(err, ErrSourceUnavailable) || errors.Is(err, ErrAllMirrorsFailed) ||
		errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
