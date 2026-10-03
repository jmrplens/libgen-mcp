// held.go bounds the calls the whole process holds open at once, sized from the
// descriptors it may open.

package main

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/v2/internal/discovery"
	"github.com/jmrplens/libgen-mcp/v2/internal/mcpotel"
	"github.com/jmrplens/libgen-mcp/v2/internal/toolutil"
	"github.com/jmrplens/libgen-mcp/v2/internal/transport"
)

// The ceiling on the calls the process holds open at once, across every caller.
//
// A tools/call holds its POST for as long as the call runs, and on this server
// that is mostly time spent queued rather than working: every catalog request
// waits for a token of the one outbound bucket (LIBGEN_MCP_RATE_RPS, one a
// second by default), so sixteen searches in flight take fifteen seconds to
// drain and a thousand take over a quarter of an hour. The inbound rate limit
// bounds how often a caller asks, not how many of its calls are still waiting,
// and the ceiling on download and read bounds those two tools and nothing else.
// So nothing bounded how many search and get_details calls one caller, or all
// of them, could keep open, and a process that runs out of descriptors stops
// accepting connections at all, /health among them.
//
// So the ceiling is sized from the descriptors the process may open, read once
// at startup, rather than written as a number. A Go program does not keep the
// soft limit it was started with: the runtime raises it to the hard one before
// main, so a host whose default soft limit is 1024 gives the process its hard
// limit, which is 524288 for a default systemd service. A fixed ceiling sized
// for 1024 would cut such a process to a fraction of what it can hold, and
// sized from the limit it bounds exactly what runs out.
const (
	// descriptorSpareDivisor leaves an eighth of the limit spare, for the idle
	// process, /health, the connections being refused and the idle upstream
	// connections the HTTP client keeps.
	descriptorSpareDivisor = 8
	// fallbackDescriptorLimit is what the ceiling is sized from where the
	// platform has no limit to read. See descriptor_limit_other.go.
	fallbackDescriptorLimit = 1024
)

// heldCallDescriptors is what one held call can cost at once, which is the
// widest fan-out a call has: an escalated search.
//
// Such a search holds the caller's connection, the catalog request, and one
// connection per searcher beyond the catalog, all at the same moment:
// discovery.Federate runs every provider in its own goroutine with its own
// client, and none of them waits on the outbound bucket, so none of them is
// paced behind another. Under extra_sources=always the catalog and the
// federation run together too. Counted from the provider list rather than
// written down, so a provider added there moves the ceiling with it: 2 + 10 is
// 12 today.
//
// Most held calls cost far less (a search still queued for its catalog token
// holds only the caller's connection), so the figure is the bound rather than
// the typical cost, which is what a ceiling that must not be outrun needs.
// Every provider connection closes once its response is read (see
// internal/discovery's newDiscoveryClient), so none of them outlives the call
// that opened it.
var heldCallDescriptors = uint64(2 + discovery.ExtraProviderCount())

// heldCallsFor is the ceiling for a process that may open descriptors file
// descriptors.
//
// An eighth of the limit is left spare, and one descriptor is reserved for each
// byte-moving call the process allows ([maxHeavyPerProcess]): a download writes
// the file it fetches and a read opens the one it extracts from, a descriptor
// on top of what every held call costs. What is left is divided by what one
// held call costs ([heldCallDescriptors]). Under a limit of 1024 that is
// (1024 - 128 - 64) / 12 = 69.
//
// A limit too small to leave room for one held call after the reservation still
// serves one at a time: a ceiling of zero would refuse every call a process
// under such a limit could have served.
func heldCallsFor(descriptors uint64) int64 {
	reserved := min(descriptors, descriptors/descriptorSpareDivisor+maxHeavyPerProcess)
	return max(1, int64((descriptors-reserved)/heldCallDescriptors)) //nolint:gosec // G115: heldCallDescriptors is at least 2, so the quotient fits an int64
}

// processSlots counts what the process holds against a ceiling of its own.
type processSlots struct {
	open  atomic.Int64
	limit int64
	// contend, when set, runs between acquire's read of the count and the swap
	// that takes a slot, the one moment another acquire can change the count
	// under it. Only the tests set it, so the retry that follows is taken on
	// purpose rather than by the scheduler's luck.
	contend func()
}

// acquire takes a slot, or reports that every slot is taken.
//
// It never takes more than the limit, even for a moment: a count that went over
// and came back would refuse a call that arrived while it was over, for a slot
// that was never used. So the count is read, compared and swapped, and read
// again when another acquire or release changed it in between.
func (s *processSlots) acquire() bool {
	for {
		held := s.open.Load()
		if held >= s.limit {
			return false
		}
		if s.contend != nil {
			s.contend()
		}
		if s.open.CompareAndSwap(held, held+1) {
			return true
		}
	}
}

// release gives back a slot acquire took.
func (s *processSlots) release() {
	s.open.Add(-1)
}

// processCeilings is the pair of process-wide ceilings, and what they were
// sized from.
type processCeilings struct {
	// held is the count of calls the process holds open.
	held *processSlots
	// sessions is the count of stateful sessions the process keeps. It is
	// counted only on a --stateless=false listener.
	sessions *processSlots
	// descriptors is the limit both were sized from, and measured whether the
	// platform said it or it is the fallback.
	descriptors uint64
	measured    bool
}

// newProcessCeilings sizes both ceilings from the descriptor limit limit reads,
// or from the fallback where the platform has none to read.
func newProcessCeilings(limit func() (uint64, bool)) *processCeilings {
	descriptors, measured := limit()
	if !measured {
		descriptors = fallbackDescriptorLimit
	}
	held := heldCallsFor(descriptors)
	return &processCeilings{
		held:        &processSlots{limit: held},
		sessions:    &processSlots{limit: statefulSessionsFor(held)},
		descriptors: descriptors,
		measured:    measured,
	}
}

// processWide is the one pair every listener of this process shares.
//
// It is keyed on the process because only a ceiling keyed on the process bounds
// the process: a per-caller number multiplies by however many callers there
// are, and an address is something a caller can have many of. And it is not
// configurable, for the reason [maxHeavyPerProcess] is not: an operator who
// could raise it could undo the one bound that keeps the process answering.
// Raising the descriptor limit the process runs under raises it, which is the
// one lever that also raises what it protects.
var processWide = newProcessCeilings(descriptorLimit)

// announceProcessCeilings writes the startup line that says what this listener
// will hold at most, and what that was sized from.
//
// The session ceiling is named only where sessions are kept: on the default
// stateless transport nothing counts them, and a figure for a bound that
// never applies would be read as one that does. A stateful deployment that
// never closes an idle session is warned, because there a session nobody
// deletes keeps its slot for the life of the process, and once every slot is
// held so, every new client is refused until a restart.
func announceProcessCeilings(c *processCeilings, opts transport.Options) {
	source := "RLIMIT_NOFILE"
	if !c.measured {
		source = "fallback, this platform has no descriptor limit to read"
	}
	attrs := []any{
		"held_calls_per_process", c.held.limit,
		"descriptor_limit", c.descriptors,
		"descriptor_limit_source", source,
	}
	if !opts.Stateless {
		attrs = append(attrs, "stateful_sessions_per_process", c.sessions.limit)
	}
	slog.Info("process ceilings", attrs...)
	if !opts.Stateless && opts.SessionTimeout == 0 {
		slog.Warn("--session-timeout=0 never closes an idle session, so one nobody deletes keeps its slot until the process restarts, and once every slot is held so every new session is refused",
			"stateful_sessions_per_process", c.sessions.limit)
	}
}

// holdsOpen reports whether a call of method is one the held ceiling counts: a
// call that can reach a mirror or a provider, and so wait on the outbound bucket
// for as long as the queue in front of it takes.
//
// tools/call and prompts/get are the two: three of the prompts run a catalog
// search before they answer. Everything else answers from memory (initialize,
// the listings, ping) or has no response for a POST to wait on (a
// notification).
func holdsOpen(method string) bool {
	return method == methodToolsCall || method == methodPromptsGet
}

// methodPromptsGet is the other method that can wait on an upstream.
const methodPromptsGet = "prompts/get"

// processBusyText is what every refusal of a process ceiling says.
//
// It says what the next action is and no more. The ceilings belong to the
// process, so a caller refused by one learns that the process is full and
// nothing else: no bound, no count, no other caller.
const processBusyText = "This server is busy. Retry later."

// busyRetryAfter is the pause a gate refusal asks for. When a slot frees
// depends on calls the caller cannot see, and a fixed half-minute spreads the
// retries a full process would otherwise meet all at once.
const busyRetryAfter = 30 * time.Second

// codeServiceUnavailable is the JSON-RPC code of a refusal for a full process,
// mirroring HTTP 503 the way the other refusal codes mirror theirs.
const codeServiceUnavailable = -50300

// processBusyRefusal is the gate's answer to a POST a full process ceiling
// refuses: 503, before the SDK has read anything.
//
// The connection is closed with the answer. A refused caller that kept it would
// hold a descriptor of the very limit the ceiling protects, and this server
// keeps an idle connection open for as long as --http-idle-timeout says, which
// is forever by default.
func processBusyRefusal() refusal {
	return refusal{
		status:  http.StatusServiceUnavailable,
		code:    codeServiceUnavailable,
		message: processBusyText,
		header: newHeader(
			"Retry-After", strconv.Itoa(int(busyRetryAfter.Seconds())),
			"Connection", "close",
		),
	}
}

// busyLogWindow is how often a refusal of a process ceiling is logged: a full
// process refuses at the rate it is asked, and a line per refusal would bury
// everything else on the stream.
const busyLogWindow = 10 * time.Second

// busyLog throttles the operator's line for a refusal to one per window per
// ceiling.
type busyLog struct {
	last atomic.Int64
}

// log writes the line unless one was written within the window.
func (b *busyLog) log(ctx context.Context, msg string, args ...any) {
	now := time.Now().UnixNano()
	last := b.last.Load()
	if last != 0 && now-last < int64(busyLogWindow) {
		return
	}
	if !b.last.CompareAndSwap(last, now) {
		return
	}
	slog.WarnContext(ctx, msg, args...)
}

// heldBusyLog and sessionBusyLog are the two throttles, one per ceiling, so a
// flood refused at one does not hide the other.
var heldBusyLog, sessionBusyLog busyLog

// logHeldRefusal writes the operator's line for a refusal of the held ceiling.
// It names the scope and the figure, so an operator reading it knows which
// bound refused and that no flag raises it.
func logHeldRefusal(ctx context.Context, limit int64) {
	heldBusyLog.log(ctx, "request refused: too many calls held across the process",
		"scope", "process", "limit_held_calls", limit)
}

// postClaim is what the gate took for one POST, carried to the calls on it
// through the POST's context, which the carrier registry keeps.
type postClaim struct {
	ceilings *processCeilings
	// gateHeld is set when the gate took a held slot for this POST's one call,
	// which the first call claims rather than taking a second.
	gateHeld atomic.Bool
	// session is the slot the gate took for the stateful session this POST
	// opens, or nil when it opens none.
	session *sessionSlot
	// refused is set when a call on this POST was refused in-band, so the
	// response closes the connection it travels on.
	refused atomic.Bool
}

// postClaimKey is the context key a POST's claim travels under.
type postClaimKey struct{}

// claimFor returns the claim of the POST the carrier token names, or nil when
// that POST is gone or never passed through the gate.
func claimFor(token string) *postClaim {
	carrier := mcpCarriers.lookup(token)
	if carrier == nil {
		return nil
	}
	claim, _ := carrier.Value(postClaimKey{}).(*postClaim)
	return claim
}

// gateCountsCall reports whether the gate can take a POST's held slot itself,
// before the SDK reads a byte of it.
//
// Only where the header is the method: on protocol 2026-07-28 or later a POST
// carries one message, and the SDK refuses one whose Mcp-Method does not name
// it. Anywhere else a POST can carry a batch of calls, a notification, or a
// response to a request of the server's own, and only the SDK's reading of the
// body says which, so the calls on it are counted one by one where the SDK
// dispatches them ([processCeilingsMiddleware]).
func gateCountsCall(r *http.Request) bool {
	return r.Header.Get(protocolVersionHeader) >= protocolVersionStatelessOnly &&
		holdsOpen(r.Header.Get("Mcp-Method"))
}

// processGate takes what a POST to the MCP endpoint holds against the process
// ceilings, or refuses it with 503 before the SDK has read anything.
//
// It sits behind the Host and protocol-version guards, which refuse for free,
// and in front of the carrier, whose registry is how the claim reaches the
// calls: the carrier registers the POST's context, and the claim is on it.
// GET and DELETE carry no call and pass untouched, so the standalone stream of
// a stateful session takes no slot of its own; the POST that opened the session
// already took it.
func processGate(ceilings *processCeilings, stateless bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		claim := &postClaim{ceilings: ceilings}
		if opensSession(r, stateless) {
			claim.session = takeSessionSlot(w, r, ceilings)
			if claim.session == nil {
				return
			}
			defer claim.session.releaseUnkept()
		}
		if gateCountsCall(r) {
			if !ceilings.held.acquire() {
				logHeldRefusal(r.Context(), ceilings.held.limit)
				processBusyRefusal().write(w, r)
				return
			}
			defer ceilings.held.release()
			claim.gateHeld.Store(true)
		}
		ctx := context.WithValue(r.Context(), postClaimKey{}, claim)
		next.ServeHTTP(&busyAwareWriter{ResponseWriter: w, claim: claim}, r.WithContext(ctx))
	})
}

// busyAwareWriter closes the connection of a POST one of whose calls was
// refused in-band, which is the one thing the middleware cannot do itself.
//
// The refusal is decided before the SDK writes the response, so the header is
// still open when it is: setting Connection: close then is what net/http reads
// as "close after this reply". A refused caller then does not keep, idle, a
// descriptor of the limit that refused it.
type busyAwareWriter struct {
	http.ResponseWriter
	claim       *postClaim
	wroteHeader bool
}

// WriteHeader adds Connection: close when a call on this POST was refused.
func (b *busyAwareWriter) WriteHeader(status int) {
	if !b.wroteHeader {
		b.wroteHeader = true
		if b.claim.refused.Load() {
			b.Header().Set("Connection", "close")
		}
	}
	b.ResponseWriter.WriteHeader(status)
}

// Write sends the header first through WriteHeader, which the embedded writer
// would otherwise do implicitly and past this wrapper.
func (b *busyAwareWriter) Write(p []byte) (int, error) {
	if !b.wroteHeader {
		b.WriteHeader(http.StatusOK)
	}
	return b.ResponseWriter.Write(p)
}

// Flush sends the header the same way before flushing, for the SSE responses
// the SDK flushes event by event.
func (b *busyAwareWriter) Flush() {
	if !b.wroteHeader {
		b.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(b.ResponseWriter).Flush()
}

// Unwrap exposes the writer underneath to http.ResponseController, so the
// write deadline the SSE layer clears still reaches the connection.
func (b *busyAwareWriter) Unwrap() http.ResponseWriter {
	return b.ResponseWriter
}

// processCeilingsMiddleware counts every held call that arrived on a POST the
// gate saw, as the SDK dispatches it, and hands a new stateful session the slot
// the gate took for it.
//
// This is where the held ceiling is exact: it sees each call of a batch, never
// sees a response the client sent to a request of the server's own, and names
// the method the SDK read out of the body rather than one a header claimed. A
// call on a POST whose slot the gate already took claims that slot instead of
// taking another. A request that arrived on no gated POST, which is stdio and
// every in-memory client, is not counted: the ceiling bounds what a listener
// holds open, and a stdio process serves one caller over one pipe.
//
// It sits outside the per-caller rate limit, so a call it refuses spends none
// of its caller's bucket: the process being full is nothing the caller did.
func processCeilingsMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		token := carrierTokenOf(req)
		if token == "" {
			return next(ctx, method, req)
		}
		claim := claimFor(token)
		if claim == nil {
			return next(ctx, method, req)
		}
		claim.keepSession(req)
		if !holdsOpen(method) || claim.gateHeld.CompareAndSwap(true, false) {
			return next(ctx, method, req)
		}
		held := claim.ceilings.held
		if !held.acquire() {
			claim.refused.Store(true)
			return refuseHeldCall(ctx, method, req, held.limit)
		}
		defer held.release()
		return next(ctx, method, req)
	}
}

// refuseHeldCall refuses a call the middleware counted, carried the way the
// rate limit carries its own refusal of the same method: a tools/call as a
// result flagged isError, so the model reads a retryable diagnostic, and a
// prompts/get as a JSON-RPC error, since a prompt result has no error flag.
func refuseHeldCall(ctx context.Context, method string, req mcp.Request, limit int64) (mcp.Result, error) {
	logHeldRefusal(ctx, limit)
	mcpotel.RecordRefusal(ctx, mcpotel.ReasonInflightCeiling)
	if method == methodToolsCall {
		return toolutil.RefusalResult(req, processBusyText), nil
	}
	return nil, &jsonrpc.Error{Code: codeServiceUnavailable, Message: processBusyText}
}
