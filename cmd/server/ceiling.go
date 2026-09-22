// ceiling.go bounds how many byte-moving calls one caller may have in flight.

package main

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/v2/internal/mcpotel"
	"github.com/jmrplens/libgen-mcp/v2/internal/toolutil"
)

// heavyTools are the calls that occupy a download slot and move a file's bytes.
//
// They are the only ones worth a ceiling: search and get_details are a page
// fetch each, and the rate bucket already bounds how fast they arrive. What the
// bucket cannot bound is how *long* a call holds something — a download may hold
// its slot for minutes, and a caller who starts four of them has taken every
// slot on the replica while the bucket still shows them well within their rate.
var heavyTools = map[string]bool{"download": true, "read": true}

// maxHeavyPerProcess is the ceiling on byte-moving calls across every caller,
// and it is deliberately not configurable.
//
// The per-client ceiling multiplies by however many clients there are, so it
// bounds one caller and nothing else; only this one bounds the process. A
// deployment that could raise it would be able to configure away the single
// guarantee that a public endpoint cannot be talked into unbounded concurrent
// transfers, and no operator needs that knob to run this server.
const maxHeavyPerProcess = 64

// heavyCeiling is the pair of bounds a byte-moving call has to fit inside.
type heavyCeiling struct {
	// perClient is how many one charged address may have at once, or 0 for no
	// per-client bound at all.
	perClient int
	// perProcess is the bound across every caller.
	perProcess int
}

// resolveHeavyCeiling settles the per-client bound from the flag and the
// configured download concurrency.
//
// The default is derived rather than written down, because the thing it bounds
// is the same semaphore: LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS is 2 in code and 4
// on the hosted deployment, and a literal here would be right for one of them
// and silently wrong for the other. Left at the whole semaphore it changes
// nothing about what a single caller can do today — what it changes is that a
// *second* caller can still get a slot once somebody measures what a download
// costs and tightens it.
//
// An explicit 0 or less turns the per-client bound off; the process-wide one
// stays either way.
func resolveHeavyCeiling(flagValue int, explicit bool, maxConcurrentDownloads int) heavyCeiling {
	perClient := maxConcurrentDownloads
	if explicit {
		perClient = flagValue
	}
	return heavyCeiling{perClient: max(perClient, 0), perProcess: maxHeavyPerProcess}
}

// describe renders the ceiling for the startup line.
func (h heavyCeiling) describe() string {
	if h.perClient <= 0 {
		return fmt.Sprintf("off per caller, %d across the process", h.perProcess)
	}
	return fmt.Sprintf("%d per charged address, %d across the process", h.perClient, h.perProcess)
}

// limitHeavyCalls refuses a download or read that would put a caller — or the
// process — over its ceiling.
//
// It sits INSIDE the rate limiter, so a call that is refused here has already
// spent a token. That is deliberate: a caller sitting at their ceiling and
// retrying in a loop should pay for the retries, and a ceiling refusal that cost
// nothing would be the one free door on the surface.
//
// A request with no record is left alone: stdio, the in-memory session, and any
// deployment keeping no per-caller state. The process-wide bound goes with it,
// which is correct — on stdio there is one caller and the download semaphore is
// already theirs alone.
func (c *clientRecords) limitHeavyCalls(ceiling heavyCeiling) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			rec := recordFrom(ctx)
			if c == nil || rec == nil || method != methodToolsCall || !heavyTools[toolutil.ToolNameOf(req)] {
				return next(ctx, method, req)
			}
			leave, refusal := c.enterHeavy(rec, ceiling)
			if refusal != "" {
				// The span sits outside this layer, so the refusal reaches it
				// through the context the middleware put the holder in. Without
				// this the metric would show a fast successful tools/call, which
				// is what a refusal looks like from the outside.
				mcpotel.RecordRefusal(ctx, mcpotel.ReasonInflightCeiling)
				return toolutil.RefusalResult(refusal), nil
			}
			defer leave()
			return next(ctx, method, req)
		}
	}
}

// methodToolsCall is the one method a heavy tool arrives on.
const methodToolsCall = "tools/call"

// enterHeavy admits one byte-moving call, or explains why it cannot.
//
// The count is taken whether or not a per-client bound is configured, for the
// reason [clientRecord.heavy] gives. The returned function is the caller's to
// defer; it is nil when the call was refused, and the refusal message is
// non-empty in exactly that case.
func (c *clientRecords) enterHeavy(rec *clientRecord, ceiling heavyCeiling) (leave func(), refusal string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch {
	case c.heavyTotal >= ceiling.perProcess:
		return nil, fmt.Sprintf(
			"this server is already moving %d files and will not start another; retry after a short backoff",
			ceiling.perProcess,
		)
	case ceiling.perClient > 0 && rec.heavy >= ceiling.perClient:
		return nil, fmt.Sprintf(
			"you already have %d download or read calls in flight, which is this deployment's limit per caller; let one finish before starting another",
			ceiling.perClient,
		)
	}

	rec.heavy++
	c.heavyTotal++
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if rec.heavy > 0 {
			rec.heavy--
		}
		if c.heavyTotal > 0 {
			c.heavyTotal--
		}
	}, ""
}
