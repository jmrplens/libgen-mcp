package tools

import (
	"context"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newLiveSession connects an in-memory client to server and returns the
// server-side session, which is what downloadConsent keys on.
func newLiveSession(t *testing.T, server *mcp.Server) (*mcp.ServerSession, *mcp.ClientSession) {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	return ss, cs
}

// TestDownloadConsent_RemembersPerSession is the core promise: opting out in one
// session silences the prompt there and nowhere else.
func TestDownloadConsent_RemembersPerSession(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	a, ca := newLiveSession(t, server)
	b, cb := newLiveSession(t, server)
	t.Cleanup(func() { ca.Close(); cb.Close() })

	c := &downloadConsent{server: server}
	if c.remembered(a) {
		t.Fatal("a fresh session must not be remembered")
	}
	c.remember(a)
	if !c.remembered(a) {
		t.Fatal("session a opted out and must be remembered")
	}
	if c.remembered(b) {
		t.Fatal("session b never opted out; one session's choice must not leak into another")
	}
}

// TestDownloadConsent_NilSafe covers the paths a caller should not have to
// guard: no store wired up, or a request carrying no session.
func TestDownloadConsent_NilSafe(t *testing.T) {
	var nilStore *downloadConsent
	if nilStore.remembered(nil) {
		t.Fatal("a nil store must report not-remembered")
	}
	nilStore.remember(nil) // must not panic

	c := &downloadConsent{}
	if c.remembered(nil) {
		t.Fatal("a nil session must report not-remembered")
	}
	c.remember(nil)
	if len(c.sessions) != 0 {
		t.Fatalf("a nil session must not be stored, got %d entries", len(c.sessions))
	}
}

// TestDownloadConsent_PrunesDisconnectedSessions checks the memory story: the
// map keys are *ServerSession pointers, so entries left behind by closed
// sessions would keep those sessions alive forever.
func TestDownloadConsent_PrunesDisconnectedSessions(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	c := &downloadConsent{server: server}

	// Fill past the prune threshold with sessions that are all closed again, so
	// the next write has something to collect.
	for range consentPruneThreshold + 1 {
		ss, cs := newLiveSession(t, server)
		c.remember(ss)
		cs.Close()
		ss.Wait()
	}

	live, cl := newLiveSession(t, server)
	t.Cleanup(func() { cl.Close() })
	c.remember(live)

	if !c.remembered(live) {
		t.Fatal("the still-connected session must survive pruning")
	}
	c.mu.Lock()
	n := len(c.sessions)
	c.mu.Unlock()
	if n > consentPruneThreshold {
		t.Fatalf("closed sessions were not pruned: %d entries retained", n)
	}
}

// TestDownloadConsent_ConcurrentAccess exercises the mutex under the race
// detector; the store is reached from every concurrent download.
func TestDownloadConsent_ConcurrentAccess(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	ss, cs := newLiveSession(t, server)
	t.Cleanup(func() { cs.Close() })
	c := &downloadConsent{server: server}

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(2)
		go func() { defer wg.Done(); c.remember(ss) }()
		go func() { defer wg.Done(); _ = c.remembered(ss) }()
	}
	wg.Wait()
	if !c.remembered(ss) {
		t.Fatal("the session should be remembered after concurrent writes")
	}
}
