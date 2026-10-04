// Tests for the process-wide discovery pacing.

package discovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// TestPacersShareOneBucketPerUpstream pins what makes the pacing hold across
// searches: two providers built for two searches draw from one bucket, while a
// different base, a different provider or a different rate gets its own.
func TestPacersShareOneBucketPerUpstream(t *testing.T) {
	var p pacers
	first := p.limiter("arxiv", "https://a.invalid", rate.Every(3*time.Second), 1)
	if again := p.limiter("arxiv", "https://a.invalid", rate.Every(3*time.Second), 1); again != first {
		t.Error("a second provider for the same upstream got a bucket of its own")
	}
	others := []struct {
		name    string
		limiter *rate.Limiter
	}{
		{name: "another base", limiter: p.limiter("arxiv", "https://b.invalid", rate.Every(3*time.Second), 1)},
		{name: "another provider", limiter: p.limiter("dblp", "https://a.invalid", rate.Every(3*time.Second), 1)},
		{name: "another rate", limiter: p.limiter("arxiv", "https://a.invalid", rate.Limit(3), 3)},
	}
	for _, o := range others {
		t.Run(o.name, func(t *testing.T) {
			if o.limiter == first {
				t.Error("shared the bucket it must not share")
			}
		})
	}
}

// TestPaceHoldsAcrossProviderInstances drives the bug the shared bucket fixes:
// a second provider, built the way the next search builds it, must wait for the
// token the first one spent instead of starting with a full bucket of its own.
func TestPaceHoldsAcrossProviderInstances(t *testing.T) {
	const base = "https://pace-across-instances.invalid"
	if err := NewArxiv().pace.wait(context.Background(), "arxiv", base); err != nil {
		t.Fatalf("the first request had to wait: %v", err)
	}
	// arXiv's next token is three seconds away, past maxPaceWait, so a shared
	// bucket refuses at once and a full bucket of the provider's own would pass.
	if err := NewArxiv().pace.wait(context.Background(), "arxiv", base); !errors.Is(err, errPaced) {
		t.Errorf("a fresh provider got %v, want errPaced: the pacing did not outlive the provider", err)
	}
}

// TestMaxPaceWait pins how long a search waits for a token: one full interval of
// a provider paced at one request per second.
func TestMaxPaceWait(t *testing.T) {
	if got := maxPaceWait(); got != time.Second {
		t.Errorf("maxPaceWait() = %v, want 1s", got)
	}
}

// TestPaceSkipsATokenTooFarAwayWithoutSpendingIt drives the slow-provider case:
// a token further away than maxPaceWait is refused at once rather than slept for,
// and the refusal hands the reservation back, so the refused search does not push
// the next one's token further out.
func TestPaceSkipsATokenTooFarAwayWithoutSpendingIt(t *testing.T) {
	const base = "https://pace-skip.invalid"
	p := pace{limit: rate.Every(10 * time.Second), burst: 1}
	if err := p.wait(context.Background(), "slow", base); err != nil {
		t.Fatalf("the first request had to wait: %v", err)
	}
	for i := range 3 {
		start := time.Now()
		if err := p.wait(context.Background(), "slow", base); !errors.Is(err, errPaced) {
			t.Fatalf("attempt %d = %v, want errPaced", i+1, err)
		}
		if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
			t.Errorf("attempt %d took %v, want an immediate refusal", i+1, elapsed)
		}
	}
	l := outbound.limiter("slow", base, p.limit, p.burst)
	if tokens := l.Tokens(); tokens < -0.01 {
		t.Errorf("the bucket holds %.2f tokens, want the refused reservations handed back", tokens)
	}
}

// TestPaceWaitsForATokenWithinReach verifies a token due inside maxPaceWait is
// waited for rather than skipped, which is how a provider paced at one request
// per second still serves back-to-back searches.
func TestPaceWaitsForATokenWithinReach(t *testing.T) {
	const base = "https://pace-within-reach.invalid"
	p := pace{limit: rate.Every(200 * time.Millisecond), burst: 1}
	if err := p.wait(context.Background(), "near", base); err != nil {
		t.Fatalf("the first request had to wait: %v", err)
	}
	start := time.Now()
	if err := p.wait(context.Background(), "near", base); err != nil {
		t.Fatalf("second wait = %v, want nil", err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("second wait took %v, want it to wait for the token", elapsed)
	}
}

// TestPaceAContextEndingWhileWaitingHandsTheTokenBack verifies a search that is
// canceled while it waits returns the context's error and does not keep the
// token it reserved.
func TestPaceAContextEndingWhileWaitingHandsTheTokenBack(t *testing.T) {
	const base = "https://pace-cancelled.invalid"
	p := pace{limit: rate.Every(500 * time.Millisecond), burst: 1}
	if err := p.wait(context.Background(), "cancel", base); err != nil {
		t.Fatalf("the first request had to wait: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := p.wait(ctx, "cancel", base); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait = %v, want the deadline", err)
	}
	if tokens := outbound.limiter("cancel", base, p.limit, p.burst).Tokens(); tokens < -0.1 {
		t.Errorf("the bucket holds %.2f tokens, want the canceled reservation handed back", tokens)
	}
}

// TestPaceAnEndedContextReservesNothing verifies a context that has already ended
// returns its error before any token is taken.
func TestPaceAnEndedContextReservesNothing(t *testing.T) {
	const base = "https://pace-ended.invalid"
	p := pace{limit: rate.Every(time.Hour), burst: 1}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.wait(ctx, "ended", base); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait = %v, want context.Canceled", err)
	}
	if err := p.wait(context.Background(), "ended", base); err != nil {
		t.Errorf("the next wait = %v, want the untouched token", err)
	}
}

// TestPaceAnUnreservableBurstIsPaced verifies a pace whose burst admits nothing is
// refused rather than waited on forever.
func TestPaceAnUnreservableBurstIsPaced(t *testing.T) {
	p := pace{limit: rate.Every(time.Second), burst: 0}
	if err := p.wait(context.Background(), "zero", "https://pace-zero.invalid"); !errors.Is(err, errPaced) {
		t.Errorf("wait = %v, want errPaced", err)
	}
}
