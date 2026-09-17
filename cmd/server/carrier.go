// carrier.go ends the work when the POST that asked for it goes away.

package main

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// carrierHeader marks a POST to the MCP endpoint with a token naming the
// request context that POST runs under.
//
// It is stamped by [requestCarriers.middleware] and read back by
// [requestCarriers.bind] out of [mcp.RequestExtra.Header], which the SDK fills
// with the header of the POST an MCP request arrived on. Any value a client
// sends under this name is deleted or overwritten before the SDK sees it, so a
// token that reaches bind was minted here.
//
// The name is deliberately not in the Mcp- family: those are protocol headers a
// client may send, and this one is process-internal.
const carrierHeader = "X-Libgen-Mcp-Carrier"

// errCallerGone is the cause recorded on a handler context canceled because the
// HTTP request carrying it went away.
var errCallerGone = errors.New("the client abandoned the request")

// notificationPrefix is the method-name prefix the protocol gives to
// notifications, which carry no id and therefore no response.
const notificationPrefix = "notifications/"

// requestCarriers ties each in-flight MCP request to the HTTP POST that carries
// it, so abandoning that POST cancels the work it asked for.
//
// # Why this is needed at all
//
// The context an MCP handler runs under does not descend from the HTTP request.
// The SDK publishes an incoming message to the session and says why it ignores
// the carrier's cancellation: "don't select on req.Context().Done() here, since
// we've already received the requests and may have already published a response
// message or notification. The client could resume the stream." Cancellation is
// expected to arrive instead as the protocol's cancellation notification, which
// the SDK's preempter turns into a cancel on the request's own id.
//
// A client older than protocol 2026-07-28 cannot send that notification — which
// is what PropagateRequestCancellation covers and nothing else does. So an
// abandoned tools/call from such a client is not a signal this server drops:
// nothing is signaled at all, and the handler keeps working.
//
// Here that handler is usually a download. It holds one of
// LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS slots and a temp-cache slot, and spends the
// shared outbound bucket against a mirror, for megabytes nobody will read — while
// the callers who are still waiting queue behind it.
//
// # Why binding to the POST is exact rather than a heuristic
//
// This server configures no [mcp.EventStore]. Without one, a response can only
// be written to the stream that carried its request: there is nothing to replay
// from, and in stateless mode there is no second request to replay onto, since
// GET and DELETE are answered 405. When the POST ends, the answer to every call
// it carried has nowhere left to go. Canceling those calls is therefore not a
// guess about the client's intent; it is the truth about what the result can
// still be used for.
//
// # Why the token travels in a header
//
// A per-POST value has to reach the handler somehow, and the header is the only
// per-request channel the SDK exposes on both transports it serves here.
// Context values would work in stateless mode, where the session is connected
// with the POST's own context; they would be actively wrong in stateful mode,
// where the session is connected with the context of the *first* POST (the
// initialize) and every later request would inherit a context that ended seconds
// after startup. The header is the same mechanism in both modes.
type requestCarriers struct {
	// contexts maps a minted token to the request context of the POST that
	// carries it. Entries are removed when that context is done.
	contexts sync.Map
}

// mcpCarriers is the process-wide registry. The two halves of the mechanism sit
// in different layers — an http.Handler in front of the SDK, an mcp.Middleware
// on the server — and a package-level value is what lets them share one map
// without threading it through every constructor in between.
var mcpCarriers requestCarriers

// carriedMCPHandler stamps the MCP handler's requests with a carrier token.
//
// It wraps the SDK handler itself rather than the whole route set, so a token is
// minted only for a POST that reaches the SDK: the refusals in front of it —
// the Host guard, the cross-origin protection, the version check — end the
// request without ever dispatching an MCP call, and a token minted for one of
// those would name a context nothing looks up.
func carriedMCPHandler(charge chargePolicy, next http.Handler) http.Handler {
	return mcpCarriers.middleware(charge, next)
}

// middleware stamps every POST to the MCP endpoint with a fresh carrier token
// and registers that POST's context under it.
//
// The entry is removed when the request context is done rather than when this
// handler returns. net/http cancels a request context when ServeHTTP returns,
// so the entry cannot outlive the request either way; tying the removal to the
// context is what makes "the token is not in the map" mean exactly "the POST
// carrying it is over", which is the invariant [requestCarriers.bind] reads.
//
// It stamps the charged address in the same place and for the same reasons: it
// is the other per-POST value the SDK's middlewares cannot see for themselves,
// and it is decided from the connection and the operator's flags rather than
// from anything a caller wrote. Both headers are server-controlled, so both are
// overwritten here and deleted everywhere else.
//
// Non-POST methods carry no MCP requests: a stateless GET or DELETE is answered
// 405, and a stateful GET opens the standalone SSE stream, which delivers
// server-initiated messages and receives none. They get the headers deleted and
// nothing else, so a client cannot smuggle either value in on a method this
// never mints one for.
func (c *requestCarriers) middleware(charge chargePolicy, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			r.Header.Del(carrierHeader)
			r.Header.Del(clientAddressHeader)
			next.ServeHTTP(w, r)
			return
		}
		token := rand.Text()
		ctx := r.Context()
		c.contexts.Store(token, ctx)
		context.AfterFunc(ctx, func() { c.contexts.Delete(token) })
		r.Header.Set(carrierHeader, token)
		r.Header.Set(clientAddressHeader, charge.charge(r))
		next.ServeHTTP(w, r)
	})
}

// bind runs each MCP request under a context canceled when the POST carrying it
// goes away.
//
// Notifications are left alone. They have no response, so a POST does not wait
// for one to be handled before returning, and their handler can legitimately
// still be running when the carrier's context is already done. Binding those
// would cancel work that is not abandoned at all, notifications/initialized on a
// stateful session being the everyday case.
//
// A call is different: the POST that carried it blocks until its response has
// been written, so the carrier is live when the call is dispatched. A token with
// no entry therefore means the POST has already ended, and the call is canceled
// straight away rather than left to run for an answer that can no longer be
// delivered.
//
// A request with no token at all is left alone. That is stdio, the in-memory
// transport the server card is built over, and any other path that never passed
// through [requestCarriers.middleware]. stdio needs no equivalent: a client that
// goes away closes the pipe, and the transport read failure cancels every
// request in flight.
func (c *requestCarriers) bind(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		token := carrierTokenOf(req)
		if token == "" || strings.HasPrefix(method, notificationPrefix) {
			return next(ctx, method, req)
		}

		bound, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)

		if carrier := c.lookup(token); carrier != nil {
			// The carrier contributes its cancellation and nothing else. It is
			// not made the parent: the handler context has to keep the values
			// the SDK put on it, and the carrier's own values have no business
			// traveling any further than the layer that put them there. That is
			// what contextcheck sees and objects to, and it is the point.
			stop := context.AfterFunc(carrier, func() { cancel(errCallerGone) }) //nolint:contextcheck // the carrier is a cancellation source, not a parent
			defer stop()
			// AfterFunc runs its callback on another goroutine, so a carrier
			// that is already done can still hand a live context to the handler
			// — which is exactly the entry a POST that ended between the lookup
			// and here leaves behind. Checked once, synchronously, so the
			// already-abandoned case cannot start work at all. A carrier that
			// ends after this check is still asynchronous, and that race is the
			// ordinary one: the handler has begun, and cancellation reaches it
			// when it reaches it.
			if carrier.Err() != nil {
				cancel(errCallerGone)
			}
		} else {
			cancel(errCallerGone)
		}

		return next(bound, method, req)
	}
}

// lookup returns the request context registered under token, or nil when there
// is none.
func (c *requestCarriers) lookup(token string) context.Context {
	value, ok := c.contexts.Load(token)
	if !ok {
		return nil
	}
	carrier, _ := value.(context.Context)
	return carrier
}

// carrierTokenOf reads the carrier token an MCP request arrived with, or "" when
// it arrived on a transport that stamps none.
func carrierTokenOf(req mcp.Request) string {
	extra := req.GetExtra()
	if extra == nil || extra.Header == nil {
		return ""
	}
	return extra.Header.Get(carrierHeader)
}
