// Tests for the process-wide discovery pacing.

package discovery

import (
	"context"
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
	// A deadline far below arXiv's three seconds: Wait refuses at once when the
	// token cannot arrive in time, so a full bucket would pass and a shared one
	// fails.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := NewArxiv().pace.wait(ctx, "arxiv", base); err == nil {
		t.Error("a fresh provider sent at once: the pacing did not outlive the provider")
	}
}
