package libgen

import (
	"context"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/logging"
)

// sourceCooldownDuration is how long a download source is set aside after a failure
// that showed the SOURCE to be unavailable rather than the item to be missing. It is
// the source-level counterpart of cooldownDuration (the per-mirror window) and is
// deliberately much longer: a mirror is one host inside a family that rotates by the
// second, whereas a source is a whole service, and when one is down it is typically
// down for hours (api.fatcat.wiki has been unreachable for days at a time). The cost
// of a stale cooldown is bounded — the source is only deprioritized, never removed —
// while the cost of retrying too eagerly is a full resolve budget burned on every
// download.
//
// Five minutes is long enough to cover a working session's worth of downloads and
// short enough that a service coming back is picked up without restarting the
// server. It is a constant rather than an environment variable on purpose: this is a
// self-tuning failover detail, not a deployment decision, and every new
// LIBGEN_MCP_* knob is one more thing to document, validate and support.
const sourceCooldownDuration = 5 * time.Minute

// cooldownWindow returns the cooldown length to apply: the injected test override
// when one is set, else sourceCooldownDuration.
func (c *Client) cooldownWindow() time.Duration {
	if c.sourceCooldownWindow > 0 {
		return c.sourceCooldownWindow
	}
	return sourceCooldownDuration
}

// markSourceCooldown sets a source aside for the cooldown window after a failure
// that proved it unavailable. The map is created on demand so a Client built
// directly (as tests do) needs no extra wiring.
func (c *Client) markSourceCooldown(name string) {
	c.sourceMu.Lock()
	if c.sourceCooldown == nil {
		c.sourceCooldown = make(map[string]time.Time)
	}
	c.sourceCooldown[name] = time.Now().Add(c.cooldownWindow())
	c.sourceMu.Unlock()
}

// clearSourceCooldown lifts a source's cooldown after it served an item: whatever
// an earlier failure suggested, a source that just answered is available.
//
// It matters because of the all-cooled-down bypass: that path can let a cooled-down
// source succeed, and leaving its stale entry in place would make the next call skip
// the one source now known to work.
func (c *Client) clearSourceCooldown(name string) {
	c.sourceMu.Lock()
	delete(c.sourceCooldown, name)
	c.sourceMu.Unlock()
}

// noteSourceFailure records what a failed source attempt taught us: an unavailable
// source is put in cooldown so the next download does not pay for it again, while a
// source that answered correctly that it does not hold the item is left alone.
func (c *Client) noteSourceFailure(ctx context.Context, name string, err error) {
	if cooldownWorthy(ctx, err) {
		c.markSourceCooldown(name)
	}
}

// cooledSource is one source passed over for being in cooldown, carried out of the
// locked section so the skip can be logged without holding the mutex.
type cooledSource struct {
	// name is the skipped source's Name().
	name string
	// until is the instant its cooldown expires.
	until time.Time
}

// eligibleSources filters a per-item source list down to those not currently in
// cooldown, preserving chain order, and logs each source it passes over so a
// shortened chain is never a silent one.
//
// When EVERY candidate is in cooldown it returns the full list instead: a cooldown
// deprioritizes a source, it never makes a download impossible — the same "better to
// try than nothing" rule the mirror cooldown follows in candidates. That fallback is
// also what keeps an explicit source: request working: a single-source chain is
// always fully cooled down, so the caller still reaches the source it named.
//
// Skipping was chosen over moving cooled-down sources to the end of the chain
// because the chain grants its LAST source the full start-retry schedule
// (downloadFrom): pushing a dead source to the tail would hand it the most patience
// of all, which is the opposite of the intent.
func (c *Client) eligibleSources(supporting []DownloadSource) []DownloadSource {
	now := time.Now()
	avail := make([]DownloadSource, 0, len(supporting))
	var cooled []cooledSource

	c.sourceMu.Lock()
	for _, s := range supporting {
		until, ok := c.sourceCooldown[s.Name()]
		if !ok || now.After(until) {
			avail = append(avail, s)
			continue
		}
		cooled = append(cooled, cooledSource{name: s.Name(), until: until})
	}
	c.sourceMu.Unlock()

	if len(avail) == 0 {
		logging.SourceCooldownBypassed(cooledNames(cooled))
		return supporting
	}
	for _, s := range cooled {
		logging.SourceSkipped(s.name, s.until)
	}
	return avail
}

// cooledNames reduces the cooled-down sources to their names, for the log line that
// reports a bypass.
func cooledNames(cooled []cooledSource) []string {
	names := make([]string, 0, len(cooled))
	for _, s := range cooled {
		names = append(names, s.name)
	}
	return names
}

// supportingSources returns the sources from chain that can serve item, in chain
// order. Filtering up front is what lets the chain know which source is its last
// resort, and which ones the cooldown may pass over.
func supportingSources(chain []DownloadSource, item Item) []DownloadSource {
	supporting := make([]DownloadSource, 0, len(chain))
	for _, src := range chain {
		if src.Supports(item) {
			supporting = append(supporting, src)
		}
	}
	return supporting
}
