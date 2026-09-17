// discovery_card.go builds the SEP-2127 Server Card.
//
// It is a different document from the one buildServerCard builds, even though
// both are called a server card, and the difference is the whole point of this
// file.
//
// SEP-2127 cards deliberately carry NO primitives. The SEP says so in writing:
// it "intentionally omits primitive definitions (tools, resources, and
// prompts)", because what a server exposes "can vary by authenticated user,
// session, configuration, feature flags, deployment state", leaving "no viable
// substitute for runtime listing via the protocol's standard operations". A card
// is identity and connection metadata, nothing more.
//
// Until this existed, both card routes answered the enumerating SEP-1649
// document and differed only in Content-Type, which put the older shape at
// /server-card — the location SEP-2127 reserves. Now the binary serves the right
// shape at each path:
//
//	/server-card                          this document, SEP-2127
//	/.well-known/mcp/server-card.json     the enumerating SEP-1649 document
//
// The legacy path keeps the enumerating document byte for byte, because scanners
// written against that draft already fetch it and because it is the only
// unauthenticated answer to "what can this server do" that this project
// publishes. **Nothing should be tempted to enrich the SEP-2127 card with a tool
// list to help such a scanner**: a conformant card answering that question is a
// contradiction, and the scanner has the legacy URL.

package main

import (
	"encoding/json"
	"fmt"

	buildversion "github.com/jmrplens/libgen-mcp/internal/version"
)

const (
	// discoveryCardSchema is required, and the wire format pins it to this exact
	// string: schema.ts declares
	// `@pattern ^https://static\.modelcontextprotocol\.io/schemas/v1/server-card\.schema\.json$`.
	//
	// It answers 404 today. That is upstream and not ours to work around: the
	// schema family is versioned by the `vN` segment rather than by date, so
	// there is no dated sibling to point at instead, and naming a private mirror
	// would still be wrong once the canonical URL goes live. The trigger for
	// revisiting is that URL starting to answer 200.
	discoveryCardSchema = "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json"

	// discoveryCardName is the reverse-DNS identity the MCP Registry knows this
	// server by, matching `name` in server.json. It is NOT the
	// Implementation.Name of the handshake ("libgen-mcp"), which carries no
	// namespace and would fail the card's pattern.
	discoveryCardName = "io.github.jmrplens/libgen-mcp"

	// discoveryCardRepositorySource is the hosting service identifier a registry
	// uses to decide how to validate and reach the repository.
	discoveryCardRepositorySource = "github"

	// discoveryCardRepositoryURL is the source tree, which is deliberately not
	// [implementationWebsiteURL]: the card carries both, and they answer
	// different questions — `repository` is for inspection, `websiteUrl` for a
	// reader.
	discoveryCardRepositoryURL = "https://github.com/jmrplens/libgen-mcp"
)

// serverCards are the two documents the card routes serve.
//
// They travel together because the routes are mounted together and because
// naming them apart is the whole correction: they used to be one slice under two
// content types. A nil field leaves that route unmounted.
type serverCards struct {
	// discovery is the SEP-2127 card, served at /server-card.
	discovery []byte
	// enumerating is the SEP-1649 document, served at the .well-known path.
	enumerating []byte
}

// buildDiscoveryCard renders the SEP-2127 Server Card for this deployment.
//
// publicURL is --public-url; stateless is the transport mode. Both decide the
// `remotes` block and nothing else.
//
// It is cheap and deterministic: identity constants, the running binary's own
// version, and whatever the deployment's flags say about how to reach it.
// Nothing here registers a catalog or opens a session, which is why it needs
// none of the machinery [buildServerCard] is wrapped in.
func buildDiscoveryCard(publicURL string, stateless bool) ([]byte, error) {
	card := map[string]any{
		"$schema":     discoveryCardSchema,
		"name":        discoveryCardName,
		"version":     buildversion.Current(),
		"description": implementationDescription,
		"title":       implementationTitle,
		"websiteUrl":  implementationWebsiteURL,
		"repository": map[string]any{
			"url":    discoveryCardRepositoryURL,
			"source": discoveryCardRepositorySource,
		},
	}
	if remote := discoveryCardRemote(publicURL, stateless); remote != nil {
		card["remotes"] = []any{remote}
	}

	out, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling the server card: %w", err)
	}
	return out, nil
}

// discoveryCardRemote describes how to connect to THIS deployment, or nil when
// the deployment cannot say.
//
// A card describes a remote server, and the only address this process knows to
// be reachable from outside is the one --public-url states. A listen address is
// not that: it is frequently a loopback address or a unix socket behind a proxy,
// and publishing it would send a client somewhere it cannot go. `remotes` is
// optional in the schema, so a deployment that names no public URL publishes a
// card with identity and no connection block, which is true rather than
// misleading.
//
// There is no `headers` entry, and its absence is a statement: this server takes
// no credential. Every tool works keyless, and a client that sent one would be
// sending it for nothing.
//
// The protocol versions come from [negotiableProtocolVersions], the same helper
// the version guard refuses requests with. The extension asks for exactly that
// consistency — a card "SHOULD accurately reflect the server's runtime
// behavior", and the versions it declares "SHOULD NOT contradict the equivalent
// values" a client observes once connected. A card carrying its own copy of the
// list would satisfy that only by accident, and would advertise a version the
// deployment refuses the moment somebody passed --stateless=false.
func discoveryCardRemote(publicURL string, stateless bool) map[string]any {
	if publicURL == "" {
		return nil
	}
	supported := negotiableProtocolVersions(stateless)
	versions := make([]any, 0, len(supported))
	for _, v := range supported {
		versions = append(versions, v)
	}
	return map[string]any{
		"type":                      "streamable-http",
		"url":                       publicURL,
		"supportedProtocolVersions": versions,
	}
}
