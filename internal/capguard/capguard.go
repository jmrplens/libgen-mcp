// Package capguard keeps the methods this server answers in step with the
// capabilities it declares.
//
// The go-sdk wires a handler for every method in the protocol, whether or not
// the server opted into the feature behind it, so resources/list and
// resources/templates/list answer with a successful, empty, typed result even
// though the handshake declares no resources capability. That pair has no
// honest reading. The specification requires a server that supports resources
// to declare the resources capability, so a server declaring none is saying it
// does not support them — and then answering "here are my resources, there are
// none" contradicts that instead of completing it. The mismatch is not only
// cosmetic: an empty listing is the same answer a resource-bearing server gives
// when its list happens to be empty right now, so a client is invited to keep
// asking, while -32601 says once and for all that there is nothing to ask for.
//
// The SDK already draws this line wherever it can. resources/subscribe,
// resources/unsubscribe and completion/complete return -32601 when their
// handler is unset, because those features need explicit wiring. The three
// methods below need none, so the SDK cannot tell an intentionally
// resource-free server from an unconfigured one; this middleware tells it.
package capguard

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resourceMethods are the methods only a server that serves resources can
// answer. resources/read joins the two listings deliberately: the SDK answers
// it with -32602 "Resource not found", which is the code the specification
// prescribes for a resource-bearing server asked for a URI it does not have,
// and here no URI could ever be held for exactly the reason the listings are
// empty.
var resourceMethods = map[string]bool{
	"resources/list":           true,
	"resources/templates/list": true,
	"resources/read":           true,
}

// NoResources returns receiving middleware that answers the resource methods
// with JSON-RPC -32601, the code JSON-RPC 2.0 reserves for a method that "does
// not exist / is not available" and the one the SDK itself returns for an
// unwired resources/subscribe. Every other method passes through untouched.
//
// The message set here never reaches the client: jsonrpc2 recognizes the code
// and rewrites the message to method not found: "<method>", so the answer is
// byte-identical to the one the SDK produces for the resource methods it does
// gate.
//
// Delete this middleware if the server ever registers a resource. The SDK
// infers the resources capability from the first AddResource call, at which
// point this would advertise resources and then refuse to list them — the same
// contradiction as today, pointing the other way. TestNoResourcesIsConsistent
// in cmd/server asserts the pair so that cannot ship unnoticed.
func NoResources() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if resourceMethods[method] {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found"}
			}
			return next(ctx, method, req)
		}
	}
}
