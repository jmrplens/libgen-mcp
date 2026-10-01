// sessions.go bounds the stateful sessions the process keeps on
// --stateless=false, and reserves each one's standalone stream when it opens.

package main

import (
	"net/http"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// sessionHeldDivisor makes the session ceiling half the held-call ceiling.
//
// On --stateless=false the SDK keeps every session a client opens until the
// client deletes it or it has sat idle for --session-timeout, and initialize is
// metered to no bucket, so nothing stopped a caller opening sessions as fast as
// it could post. An idle session holds no connection. The one it can hold open
// is its standalone stream, a GET the SDK serves for as long as the session
// lives, and enough of those take every descriptor the process has.
//
// So each session the process keeps holds one held-call slot for its stream,
// from the POST that opens it until it ends, whether the stream is open or not.
// With sessions at half the held slots, the descriptor budget the held ceiling
// was sized from holds as it was, a call on an open session is still served
// when every session slot is taken, and a session the process keeps is never
// refused its stream. That last one matters: a client refused its standalone
// stream does not ask again, and loses every message the server sends outside
// a response while the session carries on as if nothing were missing.
const sessionHeldDivisor = 2

// statefulSessionsFor is the session ceiling of a process that may hold held
// calls open at once. A process that may hold one call still keeps one session:
// a ceiling of zero would refuse every client of the transport.
func statefulSessionsFor(held int64) int64 {
	return max(1, held/sessionHeldDivisor)
}

// opensSession reports whether a POST would open a stateful session that
// outlives it: one carrying no session id, on a deployment that keeps sessions.
//
// The SDK creates a session for every such POST, whatever it carries, and keeps
// it past the POST only once its initialize has completed. A POST on protocol
// 2026-07-28 or later never reaches here on this transport: the version guard
// answers it with the revisions a stateful listener serves.
func opensSession(r *http.Request, stateless bool) bool {
	return !stateless && r.Header.Get("Mcp-Session-Id") == ""
}

// sessionSlot is what the gate took for the session a POST opens: one of the
// process's sessions, and one held slot for the session's standalone stream.
// The session keeps it from the first request dispatched on it until it ends;
// a POST whose session never took it gives it back when it ends.
type sessionSlot struct {
	ceilings *processCeilings
	kept     atomic.Bool
}

// takeSessionSlot takes what a POST that would open a session holds if its
// session is kept, or writes the refusal and returns nil.
//
// The stream's slot is taken here rather than when the stream's GET arrives,
// because a GET refused at a full held ceiling cannot be told to come back. A
// refused initialize is a failure the client sees.
func takeSessionSlot(w http.ResponseWriter, r *http.Request, ceilings *processCeilings) *sessionSlot {
	if !ceilings.sessions.acquire() {
		sessionBusyLog.log(r.Context(), "request refused: too many stateful sessions across the process",
			"scope", "process", "limit_stateful_sessions", ceilings.sessions.limit)
		processBusyRefusal().write(w, r)
		return nil
	}
	if !ceilings.held.acquire() {
		ceilings.sessions.release()
		logHeldRefusal(r.Context(), ceilings.held.limit)
		processBusyRefusal().write(w, r)
		return nil
	}
	return &sessionSlot{ceilings: ceilings}
}

// release gives back everything the slot holds.
func (s *sessionSlot) release() {
	s.ceilings.sessions.release()
	s.ceilings.held.release()
}

// releaseUnkept gives the slot back when no session kept it: the POST opened a
// session the SDK closed with it, or dispatched no request at all. Taking the
// flag here too is what makes the two ends exclusive, so a slot is released
// once.
func (s *sessionSlot) releaseUnkept() {
	if s.kept.CompareAndSwap(false, true) {
		s.release()
	}
}

// keepSession hands the slot the gate took for this POST's session to that
// session, as the first request on it is dispatched, and gives it back when the
// session ends.
//
// The first request is where the session can be reached: the SDK creates it
// before it reads the body, and hands it to every request it dispatches. The
// slot is kept whatever the request is and however it ends, because the SDK
// closes a session whose initialize did not complete when the POST that opened
// it ends, and that ending gives the slot back the same way any other does: a
// DELETE, --session-timeout, or shutdown.
func (c *postClaim) keepSession(req mcp.Request) {
	if c.session == nil {
		return
	}
	session, ok := req.GetSession().(*mcp.ServerSession)
	if !ok || session == nil || !c.session.kept.CompareAndSwap(false, true) {
		return
	}
	slot := c.session
	go func() {
		_ = session.Wait()
		slot.release()
	}()
}
