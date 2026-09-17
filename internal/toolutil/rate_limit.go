// rate_limit.go bounds what one caller may ask of a server everyone shares.

package toolutil

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/time/rate"
)

// RateLimitedErrorCode is the JSON-RPC code a refused request carries.
//
// It mirrors HTTP 429 the way the transport gates' codes mirror their statuses,
// so a client meets one number for "come back later" whichever layer said it.
// -32768 to -32000 is reserved by JSON-RPC and by the MCP specification, and
// this sits well below.
const RateLimitedErrorCode = -42900

// RateLimitRefusalPrefix and rateLimitRetrySuffix are how every refusal this
// limiter writes begins and ends, whichever of the two wire shapes carries it.
//
// The prefix is exported because a refused tools/call arrives as a *successful*
// JSON-RPC result whose only distinguishing mark is this text: that shape has no
// code and no _meta, so anything that has to tell a refusal from a handler
// failure matches the wording. Reading the constant rather than a copy of the
// sentence makes a change of wording a compile-visible edit here rather than a
// silent reclassification somewhere else.
const (
	RateLimitRefusalPrefix = "rate limit exceeded for "
	rateLimitRetrySuffix   = "; retry after a short backoff"
)

// defaultThrottleWindow is how often a refusal is reported.
//
// Long enough that a sustained flood costs six lines a minute, short enough that
// a burst is visible while it is happening.
const defaultThrottleWindow = 10 * time.Second

// The methods this limiter meters, and the bucket each draws on.
//
// tools/call and prompts/get reach a mirror or an open-access provider with the
// deployment's own egress; subscriptions/listen reaches nothing, but it is a
// request a caller can issue in a loop and it is not refused anywhere else, so
// it is charged on the same bucket rather than left as the one unmetered door.
//
// The resource methods are absent on purpose: internal/capguard answers them
// -32601, and so does the SDK for completion/complete, whose handler this server
// never wires. Metering a method that is already refused would be a bucket
// nothing can ever draw from.
const (
	methodToolsCall           = "tools/call"
	methodPromptsGet          = "prompts/get"
	methodSubscriptionsListen = "subscriptions/listen"
	methodToolsList           = "tools/list"
)

// IsMetered reports whether this limiter charges method a token.
//
// Exported because the layer that resolves a caller's bucket has to know which
// methods are worth resolving one for, and a second copy of the list there is a
// list that drifts.
func IsMetered(method string) bool {
	switch method {
	case methodToolsCall, methodPromptsGet, methodSubscriptionsListen, methodToolsList:
		return true
	}
	return false
}

// RateLimiter is a token bucket, plus the second bucket a catalog listing draws
// on and the self-suppressing report of what it refused.
//
// A nil *RateLimiter is the disabled limiter: every method on it is safe to call
// and allows everything, which is what lets the middleware be written without a
// branch per bucket.
//
// [rate.Limiter] is safe for concurrent use, so nothing here synchronizes the
// bucket itself.
type RateLimiter struct {
	limiter *rate.Limiter

	// Reporting state. A refusal used to be entirely silent on the sibling
	// project, and an operator could not tell a limited deployment from an idle
	// one. Self-suppressed because refusals are unbounded: their rate is the
	// arrival rate minus the limit, so one line per event would replace a silent
	// limiter with a log flood. One line per window carries the count of
	// everything it stands for.
	reportMu       sync.Mutex
	windowStart    time.Time
	windowRefusals int
	throttleWindow time.Duration

	// catalogOnce guards the bucket tools/list draws on. It is derived once and
	// kept: a bucket rebuilt per request would arrive full every time, which is
	// not a second limit but no limit at all.
	catalogOnce sync.Once
	catalog     *RateLimiter
}

// NewRateLimiter builds a limiter refilling at rps with the given burst, or nil
// when rps is not positive — which every method here treats as disabled. A burst
// below one is raised to one, since a zero-burst bucket refuses everything
// forever.
func NewRateLimiter(rps float64, burst int) *RateLimiter {
	if rps <= 0 {
		return nil
	}
	burst = max(burst, 1)
	return &RateLimiter{
		limiter:        rate.NewLimiter(rate.Limit(rps), burst),
		throttleWindow: defaultThrottleWindow,
	}
}

// allow reports whether a token was available. A nil limiter allows everything.
func (r *RateLimiter) allow() bool {
	if r == nil || r.limiter == nil {
		return true
	}
	return r.limiter.Allow()
}

// forCatalog returns the bucket tools/list draws on: the same rate and the same
// burst, in a bucket of its own.
//
// A separate bucket rather than a slower one. What it has to guarantee is that a
// caller who drained the call bucket can still discover the surface — a refused
// listing is worse than a refused call, because no model is in the loop to read
// the message and back off. The sibling project also refills its listing bucket
// ten times more slowly, because one listing there marshals megabytes; this
// server lists four tools and four prompts, so there is nothing to slow down.
//
// Memoized for the reason the bucket exists at all: derived per request it would
// arrive full every time and meter nothing.
func (r *RateLimiter) forCatalog() *RateLimiter {
	if r == nil || r.limiter == nil {
		return nil
	}
	r.catalogOnce.Do(func() {
		r.catalog = &RateLimiter{
			limiter:        rate.NewLimiter(r.limiter.Limit(), r.limiter.Burst()),
			throttleWindow: r.throttleWindow,
		}
	})
	return r.catalog
}

// reportRefusal writes at most one line per window, naming what was refused and
// how many refusals the line stands for.
//
// WARN rather than INFO: unlike every other refusal on this surface, this one is
// not the caller's doing. The request was well formed and would have succeeded a
// moment earlier or later, and an operator seeing it may need to raise the
// limit — or may be watching the abuse it was raised against.
func (r *RateLimiter) reportRefusal(ctx context.Context, what string) {
	if r == nil || r.limiter == nil {
		return
	}

	r.reportMu.Lock()
	window := r.throttleWindow
	if window <= 0 {
		window = defaultThrottleWindow
	}
	now := time.Now()
	if !r.windowStart.IsZero() && now.Sub(r.windowStart) < window {
		r.windowRefusals++
		r.reportMu.Unlock()
		return
	}
	alsoRefused := r.windowRefusals
	r.windowStart = now
	r.windowRefusals = 0
	r.reportMu.Unlock()

	if what == "" {
		what = methodToolsCall
	}
	slog.WarnContext(ctx, "request refused: rate limit exceeded",
		"what", what,
		"limit_rps", float64(r.limiter.Limit()),
		"burst", r.limiter.Burst(),
		"also_refused_since_last_report", alsoRefused,
	)
}

// AttachRateLimit registers the metering middleware on server, resolving the
// bucket per request.
//
// Per request rather than per server because one server answers every caller
// here: a bucket captured at registration would be one budget shared by all of
// them, so the noisiest caller would refuse everybody else's requests — which is
// worse than no limit, since no limit is at least honest about what it does.
// resolve returning nil means this request is not metered, which is what stdio
// and an unconfigured deployment both want.
//
// The refusal shapes differ because the results differ. A refused tools/call is
// an MCP tool error result (IsError) rather than a JSON-RPC error, so the model
// receives a structured, retryable diagnostic and the agent loop can back off. A
// refused prompts/get, subscriptions/listen or tools/list is a JSON-RPC error
// carrying [RateLimitedErrorCode], because those results have no error flag of
// their own.
//
// Every other method passes through: initialize, prompts/list and
// notifications/* reach nothing and cost nothing to answer, and the resource
// methods are already -32601.
func AttachRateLimit(server *mcp.Server, resolve func(context.Context) *RateLimiter) {
	if server == nil || resolve == nil {
		return
	}
	server.AddReceivingMiddleware(rateLimitMiddleware(resolve))
}

// rateLimitMiddleware is the middleware itself, split out so a test can drive
// one request through the decision without standing up a server, a session and a
// transport — none of which change the answer.
func rateLimitMiddleware(resolve func(context.Context) *RateLimiter) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			// Resolved once. Twice would charge a token to whichever bucket the
			// second call returned, and on a per-request resolver that need not
			// even be the same bucket.
			limiter := resolve(ctx)
			if refusal, refused := refuse(ctx, limiter, method, req); refused {
				return refusal, nil
			}
			if err := refuseWithError(ctx, limiter, method); err != nil {
				return nil, err
			}
			return next(ctx, method, req)
		}
	}
}

// refuse answers a tools/call that has no token left, in the shape a model can
// act on. The boolean is what distinguishes "refused" from "allowed", since a
// refusal here is a successful result.
func refuse(ctx context.Context, limiter *RateLimiter, method string, req mcp.Request) (mcp.Result, bool) {
	if method != methodToolsCall || limiter.allow() {
		return nil, false
	}
	name := toolNameOf(req)
	limiter.reportRefusal(ctx, name)
	what := name
	if what == "" {
		what = methodToolsCall
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: RateLimitRefusalPrefix + what + rateLimitRetrySuffix}},
	}, true
}

// refuseWithError answers the methods whose result carries no error flag, and
// returns nil when the request may proceed.
func refuseWithError(ctx context.Context, limiter *RateLimiter, method string) error {
	switch method {
	case methodPromptsGet, methodSubscriptionsListen:
		if limiter.allow() {
			return nil
		}
		limiter.reportRefusal(ctx, method)
	case methodToolsList:
		catalog := limiter.forCatalog()
		if catalog.allow() {
			return nil
		}
		// Reported on the bucket that refused, so the line carries the rate that
		// actually applied.
		catalog.reportRefusal(ctx, method)
	default:
		return nil
	}
	return &jsonrpc.Error{
		Code:    RateLimitedErrorCode,
		Message: RateLimitRefusalPrefix + method + rateLimitRetrySuffix,
	}
}

// toolNameOf returns the tool a tools/call names, when it can be read.
//
// The SDK delivers tools/call params as *mcp.CallToolParamsRaw to receiving
// middleware; the typed form arrives later, once the handler decodes Arguments.
// Both are read so this keeps working if that changes.
func toolNameOf(req mcp.Request) string {
	if req == nil {
		return ""
	}
	switch p := req.GetParams().(type) {
	case *mcp.CallToolParamsRaw:
		if p != nil {
			return strings.TrimSpace(p.Name)
		}
	case *mcp.CallToolParams:
		if p != nil {
			return strings.TrimSpace(p.Name)
		}
	}
	return ""
}

// Describe renders the limiter for a startup line, so an operator can read what
// the deployment settled on rather than infer it from the flags they passed.
func (r *RateLimiter) Describe() string {
	if r == nil || r.limiter == nil {
		return "off"
	}
	return fmt.Sprintf("%g rps, burst %d", float64(r.limiter.Limit()), r.limiter.Burst())
}
