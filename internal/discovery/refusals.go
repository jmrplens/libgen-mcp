// Process-wide memory of the upstream hosts that refused this server.

package discovery

import (
	"sync"
	"time"
)

// refusalWindows remembers, for the life of the process, which upstream bases
// refused this server and until when they are left alone.
//
// The memory has to outlive the provider. ExtraProviders builds a fresh set of
// providers for every federated search, so a cooldown kept on the provider value
// is forgotten by the next search, which then asks again and is refused again.
// It is keyed by the base URL the request went to: in production that is one
// host per provider, and in tests every httptest server is its own key, so one
// test's refusal never silences another's server.
type refusalWindows struct {
	// mu guards until: Federate runs providers in their own goroutines and the
	// server answers several searches at once.
	mu sync.Mutex
	// until maps a base URL to the instant before which it is not asked.
	until map[string]time.Time
}

// quiet reports whether key is still inside a refusal window at now.
func (w *refusalWindows) quiet(key string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return now.Before(w.until[key])
}

// open starts, or extends, the window for key so it lasts challengeCooldown from
// now, and reports whether this call opened a fresh one, so the reason is logged
// once per window rather than once per search. The deadline only ever moves
// later: a late writer holding an older clock reading never shortens a window
// another refusal has since extended.
func (w *refusalWindows) open(key string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.until == nil {
		w.until = map[string]time.Time{}
	}
	previous, seen := w.until[key]
	if next := now.Add(challengeCooldown); next.After(previous) {
		w.until[key] = next
	}
	return !seen || !now.Before(previous)
}
