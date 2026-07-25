package tools

import (
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// consentPruneThreshold is the number of remembered sessions past which a write
// sweeps out the ones that have since disconnected. Pruning on every write would
// walk the server's live sessions on a path that runs once per download; leaving
// it unbounded would retain a *ServerSession per session that ever opted out, and
// keeping the pointer as a map key is exactly what stops it being collected. A
// small threshold keeps both costs negligible: a stdio server holds one session
// and never reaches it, and an HTTP server pays one sweep per few dozen opt-outs.
const consentPruneThreshold = 32

// downloadConsent remembers, per client session, that the user asked not to be
// prompted again before a file is written to disk.
//
// The scope is deliberately a session and not the process: "stop asking" is an
// answer about the conversation in front of the user, so a later client
// connecting to the same server starts out being asked again. Making it survive
// reconnects is what LIBGEN_MCP_CONFIRM_DOWNLOADS=false is for.
//
// Sessions are keyed by pointer rather than by ServerSession.ID(), because ID()
// is empty over stdio: keying by it would file every stdio session under "",
// which happens to be harmless today (one session per process) but would silently
// share one user's opt-out with another the moment that stopped being true.
type downloadConsent struct {
	mu sync.Mutex
	// sessions holds the sessions that opted out. A nil map is a valid empty one;
	// it is created on first write.
	sessions map[*mcp.ServerSession]struct{}
	// server is consulted to drop sessions that have disconnected. When nil,
	// pruning is skipped and the map grows — the tests construct it that way, and
	// Register always supplies it.
	server *mcp.Server
}

// remembered reports whether this session has already opted out of download
// confirmations. A nil receiver or session reports false, so a caller never has
// to guard the lookup.
func (d *downloadConsent) remembered(ss *mcp.ServerSession) bool {
	if d == nil || ss == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.sessions[ss]
	return ok
}

// remember records that this session no longer wants to be asked. A nil receiver
// or session is a no-op.
func (d *downloadConsent) remember(ss *mcp.ServerSession) {
	if d == nil || ss == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sessions == nil {
		d.sessions = make(map[*mcp.ServerSession]struct{})
	}
	d.sessions[ss] = struct{}{}
	if len(d.sessions) > consentPruneThreshold {
		d.pruneLocked()
	}
}

// pruneLocked drops every remembered session the server no longer lists as
// connected. The caller must hold d.mu.
func (d *downloadConsent) pruneLocked() {
	if d.server == nil {
		return
	}
	live := make(map[*mcp.ServerSession]struct{})
	for ss := range d.server.Sessions() {
		live[ss] = struct{}{}
	}
	for ss := range d.sessions {
		if _, ok := live[ss]; !ok {
			delete(d.sessions, ss)
		}
	}
}
