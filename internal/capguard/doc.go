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
