// Process-wide outbound pacing for the discovery providers.

package discovery

import (
	"context"
	"strconv"
	"sync"

	"golang.org/x/time/rate"
)

// pacers holds one rate limiter per upstream for the life of the process.
//
// A limiter kept on the provider paces nothing between searches: ExtraProviders
// builds a fresh provider for every federated search, so each search started
// with a full bucket, and an HTTP deployment answering several searches at once
// sent each upstream as many requests as it had searches in flight. The rates
// the providers declare are per caller (arXiv's API terms, NCBI's E-utilities
// limit, OpenLibrary's published allowance), and this process is one caller, so
// the bucket has to outlive the provider.
//
// It is keyed by the provider, the base URL the request goes to, and the rate,
// so that in tests every httptest server gets a bucket of its own, and an
// OpenLibrary provider built with a contact address does not share the
// anonymous bucket's smaller allowance.
type pacers struct {
	// mu guards byKey: Federate runs providers in their own goroutines.
	mu sync.Mutex
	// byKey maps "<provider>|<base>|<limit>|<burst>" to its limiter.
	byKey map[string]*rate.Limiter
}

// outbound is the process-wide set of discovery pacers.
var outbound pacers

// pace is the rate a provider declares for its upstream. The bucket it names is
// the process-wide one in outbound, looked up at request time against the base
// the request goes to.
type pace struct {
	// limit is the sustained request rate.
	limit rate.Limit
	// burst is how many requests may go back to back.
	burst int
}

// wait blocks until provider may send one request to base, under this pace.
func (p pace) wait(ctx context.Context, provider, base string) error {
	return outbound.limiter(provider, base, p.limit, p.burst).Wait(ctx)
}

// limiter returns the process-wide limiter for provider at base, creating it
// with limit and burst on first use.
func (p *pacers) limiter(provider, base string, limit rate.Limit, burst int) *rate.Limiter {
	key := provider + "|" + base + "|" + strconv.FormatFloat(float64(limit), 'g', -1, 64) + "|" + strconv.Itoa(burst)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byKey == nil {
		p.byKey = map[string]*rate.Limiter{}
	}
	l, ok := p.byKey[key]
	if !ok {
		l = rate.NewLimiter(limit, burst)
		p.byKey[key] = l
	}
	return l
}
