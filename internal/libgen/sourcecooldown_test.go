package libgen

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingSource is a DownloadSource that counts how often it was asked to resolve
// and always fails with a fixed error, so a test can prove which sources the chain
// actually reached across successive calls.
type countingSource struct {
	name     string
	err      error
	attempts *atomic.Int32
}

func (s countingSource) Name() string     { return s.name }
func (countingSource) Supports(Item) bool { return true }
func (s countingSource) Resolve(context.Context, Item) (Resolved, error) {
	s.attempts.Add(1)
	return Resolved{}, s.err
}

// cooldownChainClient builds a client whose article chain is the two counting
// sources given, with the short start-retry schedule the unit tests use.
func cooldownChainClient(sources ...DownloadSource) *Client {
	c := newTestClient(staticMirrors{})
	c.sources = sources
	return c
}

// captureLog redirects slog to a buffer for the duration of the test and returns it.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return &buf
}

// TestUnavailableSourceIsSkippedOnTheNextCall is the feature's core promise: a
// source that proved unreachable is remembered and passed over on the next
// download, while a source that merely answered "I do not hold this item" keeps its
// place in the chain. Without this, a globally dead source burns its resolve budget
// on every single call.
func TestUnavailableSourceIsSkippedOnTheNextCall(t *testing.T) {
	var dead, missing atomic.Int32
	c := cooldownChainClient(
		countingSource{name: "dead", err: unavailable(errors.New("dial tcp: no answer")), attempts: &dead},
		countingSource{name: "missing", err: notIndexed(errors.New("not in my index")), attempts: &missing},
	)

	for range 2 {
		if _, err := c.DownloadItem(context.Background(), Item{DOI: "10.1/x"}, t.TempDir(), ""); err == nil {
			t.Fatal("every source fails, so DownloadItem must return an error")
		}
	}

	if got := dead.Load(); got != 1 {
		t.Errorf("the unavailable source resolved %d times, want 1 (the second call must skip it)", got)
	}
	if got := missing.Load(); got != 2 {
		t.Errorf(`the "not indexed" source resolved %d times, want 2 (a clean miss must not cost it its place)`, got)
	}
}

// TestSourceCooldownExpires verifies the cooldown is a delay, not a removal: once
// the window passes the source is offered to the chain again.
func TestSourceCooldownExpires(t *testing.T) {
	c := newTestClient(staticMirrors{})
	c.sourceCooldownWindow = 20 * time.Millisecond
	chain := []DownloadSource{
		stubSource{name: "fatcat", supports: true},
		stubSource{name: "scidb", supports: true},
	}

	c.markSourceCooldown("fatcat")
	if got := srcNames(c.eligibleSources(chain)); len(got) != 1 || got[0] != "scidb" {
		t.Fatalf("eligible sources = %v, want [scidb] while fatcat is in cooldown", got)
	}

	time.Sleep(50 * time.Millisecond)
	if got := srcNames(c.eligibleSources(chain)); len(got) != 2 {
		t.Errorf("eligible sources = %v, want both once the cooldown expired", got)
	}
}

// TestEverySourceCooledDownIsStillAttempted verifies a cooldown never makes a
// download impossible: when every source able to serve the item is set aside, the
// chain tries them all anyway — better to try than nothing.
func TestEverySourceCooledDownIsStillAttempted(t *testing.T) {
	var first, second atomic.Int32
	c := cooldownChainClient(
		countingSource{name: "first", err: unavailable(errors.New("dial tcp")), attempts: &first},
		countingSource{name: "second", err: unavailable(errors.New("dial tcp")), attempts: &second},
	)
	c.markSourceCooldown("first")
	c.markSourceCooldown("second")

	if _, err := c.DownloadItem(context.Background(), Item{DOI: "10.1/x"}, t.TempDir(), ""); err == nil {
		t.Fatal("both sources fail, so DownloadItem must return an error")
	}
	if first.Load() != 1 || second.Load() != 1 {
		t.Errorf("attempts = (%d, %d), want (1, 1): a cooldown must deprioritize, never disable",
			first.Load(), second.Load())
	}
}

// TestSourceCooldownIsVisibleInTheLog verifies a skipped source is reported rather
// than silently absent. A source that vanishes from the chain log with no
// explanation is the debugging dead end this feature must not create.
func TestSourceCooldownIsVisibleInTheLog(t *testing.T) {
	buf := captureLog(t)
	var dead, missing atomic.Int32
	c := cooldownChainClient(
		countingSource{name: "dead", err: unavailable(errors.New("dial tcp")), attempts: &dead},
		countingSource{name: "missing", err: notIndexed(errors.New("not indexed")), attempts: &missing},
	)

	for range 2 {
		_, _ = c.DownloadItem(context.Background(), Item{DOI: "10.1/x"}, t.TempDir(), "")
	}

	log := buf.String()
	if !strings.Contains(log, "cooldown") || !strings.Contains(log, `"source":"dead"`) {
		t.Errorf("the log never explains that dead was skipped for being in cooldown; log=%s", log)
	}
}

// TestEverySourceCooledDownIsLogged verifies the bypass is reported too: trying
// sources that are all in cooldown is a deliberate decision, so the log says so.
func TestEverySourceCooledDownIsLogged(t *testing.T) {
	buf := captureLog(t)
	var only atomic.Int32
	c := cooldownChainClient(countingSource{name: "only", err: unavailable(errors.New("dial tcp")), attempts: &only})
	c.markSourceCooldown("only")

	_, _ = c.DownloadItem(context.Background(), Item{DOI: "10.1/x"}, t.TempDir(), "")

	if log := buf.String(); !strings.Contains(log, "in cooldown, trying them anyway") {
		t.Errorf("the log never explains why a cooled-down source was tried anyway; log=%s", log)
	}
}

// TestResolveLinkHonorsTheSourceCooldown verifies the remote (resolve-only) path
// shares the cooldown: it runs the same chain, so it must both record and respect
// an unavailable source.
func TestResolveLinkHonorsTheSourceCooldown(t *testing.T) {
	var dead, missing atomic.Int32
	c := cooldownChainClient(
		countingSource{name: "dead", err: unavailable(errors.New("dial tcp")), attempts: &dead},
		countingSource{name: "missing", err: notIndexed(errors.New("not indexed")), attempts: &missing},
	)

	for range 2 {
		if _, err := c.ResolveLink(context.Background(), Item{DOI: "10.1/x"}); err == nil {
			t.Fatal("every source fails, so ResolveLink must return an error")
		}
	}
	if got := dead.Load(); got != 1 {
		t.Errorf("the unavailable source resolved %d times through ResolveLink, want 1", got)
	}
	if got := missing.Load(); got != 2 {
		t.Errorf(`the "not indexed" source resolved %d times through ResolveLink, want 2`, got)
	}
}

// TestPinnedSourceIgnoresItsOwnCooldown verifies an explicit source: argument still
// reaches that source. The caller asked for it by name, and the all-cooled fallback
// is what keeps that promise.
func TestPinnedSourceIgnoresItsOwnCooldown(t *testing.T) {
	var pinned atomic.Int32
	c := cooldownChainClient(
		countingSource{name: "dead", err: unavailable(errors.New("dial tcp")), attempts: &pinned},
		stubSource{name: "other", supports: true},
	)
	c.markSourceCooldown("dead")

	if _, err := c.DownloadItem(context.Background(), Item{DOI: "10.1/x", Source: "dead"}, t.TempDir(), ""); err == nil {
		t.Fatal("the pinned source fails, so DownloadItem must return an error")
	}
	if got := pinned.Load(); got != 1 {
		t.Errorf("the pinned source resolved %d times, want 1: a cooldown must not override an explicit request", got)
	}
}

// TestSourceCooldownIsLiftedOnSuccess verifies a source that serves an item leaves
// its cooldown behind. The all-cooled-down bypass can let a cooled source succeed,
// and a stale entry would then make the next call skip the one source known to work.
func TestSourceCooldownIsLiftedOnSuccess(t *testing.T) {
	good := stubSource{name: "good", supports: true, resolved: Resolved{FileURL: "https://cdn/x.pdf", Ext: "pdf"}}
	other := stubSource{name: "other", supports: true}
	c := cooldownChainClient(good)
	c.markSourceCooldown("good")
	c.markSourceCooldown("other")

	if _, err := c.ResolveLink(context.Background(), Item{DOI: "10.1/x"}); err != nil {
		t.Fatalf("ResolveLink: %v", err)
	}

	// "other" stays in cooldown, so there is no bypass to mask the result.
	if got := srcNames(c.eligibleSources([]DownloadSource{good, other})); len(got) != 1 || got[0] != "good" {
		t.Errorf("eligible sources = %v, want [good]: a source that just served must not stay in cooldown", got)
	}
}

// TestSourceCooldownIsRaceFree exercises the cooldown map from several goroutines
// at once, so `go test -race` proves the shared state is properly guarded.
func TestSourceCooldownIsRaceFree(t *testing.T) {
	c := newTestClient(staticMirrors{})
	chain := []DownloadSource{
		stubSource{name: "a", supports: true},
		stubSource{name: "b", supports: true},
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for range 50 {
				c.noteSourceFailure(context.Background(), chain[i%2].Name(), unavailable(errors.New("dial tcp")))
				_ = c.eligibleSources(chain)
			}
		}(i)
	}
	wg.Wait()
}
