// Server card: the vendor document served at /.well-known/mcp/server-card.json.
//
// Its shape descends from SEP-1649, which is closed and superseded by SEP-2127 —
// that one relocates the card to GET <url>/server-card behind
// /.well-known/ai-catalog.json and explicitly rejects .well-known/mcp paths. So
// this is a vendor document with a SEP-1649 lineage, not an implementation of a
// live specification; the standard pre-connection channel is server/discover,
// which the SDK already answers. Migrate when SEP-2127 lands.

package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/v2/internal/telemetry"
)

// serverCardPath is the legacy card location, kept because scanners and
// directories written against the SEP-1649 draft still look there.
const serverCardPath = "/.well-known/mcp/server-card.json"

// serverCardCurrentPath is where the card belongs today. The ext-server-card
// extension moved it here on 2026-06-08 (commit 10e958fa, "Server Cards:
// recommend /server-card over .well-known") on the grounds that /.well-known is
// for site-wide metadata whereas a server's card is application-level metadata.
// Both are served: retiring the old one would break every scanner that already
// fetches it, and the two answer with the same bytes.
const serverCardCurrentPath = "/server-card"

// serverCardMediaType is the content type the ext-server-card extension gives
// the document. It is used on serverCardCurrentPath only; serverCardPath keeps
// application/json, which is what the scanners already fetching it expect and
// what a client comparing the header literally will accept.
const serverCardMediaType = "application/mcp-server-card+json"

// serverCardTool is one tool as the card presents it: the same fields tools/list
// returns, so a reader gets the full surface without opening a session.
type serverCardTool struct {
	Name         string               `json:"name"`
	Title        string               `json:"title,omitempty"`
	Description  string               `json:"description,omitempty"`
	InputSchema  any                  `json:"inputSchema,omitempty"`
	OutputSchema any                  `json:"outputSchema,omitempty"`
	Annotations  *mcp.ToolAnnotations `json:"annotations,omitempty"`
	// Icons carries whatever the tool declares: three entries per tool — the
	// scalable SVG plus the light/dark WebP fallbacks internal/toolutil builds
	// (see docs/architecture.md § Icons). Omitted when a tool declares none, so
	// a surface that drops them does not leave a null key behind.
	Icons []mcp.Icon `json:"icons,omitempty"`
}

// serverCardPromptArgument is one argument of a prompt as the card presents it.
type serverCardPromptArgument struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// serverCardPrompt is one prompt as the card presents it. Prompts are the reason
// this endpoint earns its place here: they are what distinguishes this server
// from the other Library Genesis MCP servers, and nothing else advertises them
// without a client first connecting.
type serverCardPrompt struct {
	Name        string                     `json:"name"`
	Title       string                     `json:"title,omitempty"`
	Description string                     `json:"description,omitempty"`
	Arguments   []serverCardPromptArgument `json:"arguments,omitempty"`
	// Icons carries whatever the prompt declares; see serverCardTool.Icons.
	Icons []mcp.Icon `json:"icons,omitempty"`
}

// serverCard is the document itself, mirroring what initialize plus the list
// calls would return. Field names and shape follow the sibling
// gitlab-mcp-server so one scanner reads both.
type serverCard struct {
	// ServerInfo is taken verbatim from the handshake (see buildServerCard)
	// rather than restated here, so the card cannot advertise less than the
	// server does: it previously hardcoded name and version and silently
	// dropped Title, Description, WebsiteURL and Icons — exactly the fields a
	// registry listing renders.
	ServerInfo *mcp.Implementation `json:"serverInfo"`
	// Capabilities is taken from the same handshake, for the same reason: a
	// directory reading the card could not otherwise learn what this server
	// negotiates — that it advertises tools and prompts, and, since the
	// Capabilities pin in newMCPServer, no deprecated logging capability and no
	// list-changed notification it cannot send — except by grepping English
	// prose. The card follows the original
	// SEP-1649 shape, which required this key; its successor SEP-2127
	// deliberately carries neither capabilities nor primitives, but this card
	// already enumerates tools and prompts, so it is a SEP-1649-lineage
	// document and states its capabilities structurally too.
	Capabilities   *mcp.ServerCapabilities `json:"capabilities"`
	Authentication struct {
		Required bool     `json:"required"`
		Schemes  []string `json:"schemes"`
	} `json:"authentication"`
	Tools             []serverCardTool   `json:"tools"`
	Prompts           []serverCardPrompt `json:"prompts"`
	Resources         []any              `json:"resources"`
	ResourceTemplates []any              `json:"resourceTemplates"`
	// Observability says what this deployment records about the calls it
	// serves, and is absent when it records nothing.
	Observability *serverCardObservability `json:"observability,omitempty"`
}

// serverCardObservability is what a caller may know about being instrumented.
//
// # Why it is published at all
//
// Telemetry is off by default here for privacy rather than cost, and a privacy
// default nobody can observe is worth less than one they can. This matters more
// on this server than on the sibling it is modeled after: that one is deployed by
// an organization for its own people, who have somebody to ask. A stranger
// reaching a public MCP endpoint has no relationship with the operator through
// which to ask whether their searches are traced.
//
// # Absent rather than "enabled": false
//
// A consumer should not have to parse a negation to learn that nothing is
// recorded, and a block that is always present invites one written by hand that
// says the wrong thing.
//
// # What it deliberately does not carry
//
// The collector's address. It names the operator's own infrastructure, and the
// card is fetched by every client that asks. What a caller needs is whether their
// calls are recorded and in what form; where the records land is not theirs to
// know. The startup log has it, for the operator who is looking at their own
// deployment.
type serverCardObservability struct {
	Enabled bool `json:"enabled"`
	// Signals and Protocol come from the live snapshot. Protocol is empty when
	// the enabled signals do not agree on one, which is an honest absence
	// rather than one signal's answer published as the process's.
	Signals  []string `json:"signals,omitempty"`
	Protocol string   `json:"protocol,omitempty"`
	// Conventions names the vocabulary, so a reader knows the records follow a
	// published schema rather than one invented here.
	Conventions string `json:"conventions"`
	// Identity is the policy's name, and Discloses is the same thing in words.
	// The name alone means nothing to somebody who did not read the
	// documentation, which is most of the people this block is for.
	Identity  string `json:"identity"`
	Discloses string `json:"discloses"`
	// Recorded and NotRecorded are the two halves a caller actually wants, and
	// the second is the one worth writing down: it is a commitment, and a
	// commitment in a machine-readable document is one somebody can check.
	//
	// Recorded is per signal rather than one sentence for the process, because
	// one sentence is false for every partial configuration: a metrics-only
	// deployment records no mirror host, and a logs-only one records no MCP
	// method. A caller reading a claim that covers what is not exported has been
	// told something untrue about their own call.
	Recorded    map[string]string `json:"recorded"`
	NotRecorded string            `json:"not_recorded"`
}

// recordedBySignal is what each signal carries, written per signal because the
// three differ.
//
// Traces are the detailed leg; metrics drop the mirror host on purpose, since a
// discovered host is a dimension a third party chooses and a metric is where
// cardinality costs; logs are this server's own records, which name the source
// chain and never the protocol method.
var recordedBySignal = map[string]string{
	"traces": "the method called, the tool named, the outcome, the duration, " +
		"which download source served a file, and the mirror host it came from",
	"metrics": "counts and durations by method, tool, outcome and download source — " +
		"never the mirror host, which a third party chooses",
	"logs": "this server's own operational records: the tool named, the outcome, " +
		"the duration, which download source served and the mirror host it came from",
}

// recordedFor describes only the signals this deployment actually exports.
//
// A signal that is off says nothing, rather than saying nothing is recorded:
// absence here means "this leg does not exist", which is what the signals list
// beside it already states.
func recordedFor(signals []string) map[string]string {
	recorded := make(map[string]string, len(signals))
	for _, signal := range signals {
		if description, known := recordedBySignal[signal]; known {
			recorded[signal] = description
		}
	}
	return recorded
}

// cardObservability describes this deployment's instrumentation, or nothing when
// it has none.
func cardObservability(identity telemetry.IdentityPolicy) *serverCardObservability {
	snapshot := telemetry.CurrentSnapshot()
	if !snapshot.Enabled {
		return nil
	}
	policy := identity
	if policy == "" {
		policy = telemetry.DefaultIdentityPolicy
	}
	return &serverCardObservability{
		Enabled:     true,
		Signals:     snapshot.Signals,
		Protocol:    snapshot.Protocol,
		Conventions: "OpenTelemetry, following the MCP semantic conventions",
		Identity:    string(policy),
		Discloses:   telemetry.PolicyDescription(policy),
		Recorded:    recordedFor(snapshot.Signals),
		NotRecorded: "search queries, record titles, tool arguments, tool results, " +
			"and any credential supplied for a single call",
	}
}

// buildServerCard serializes the card once, by listing the live server's own
// tools and prompts over an in-memory session.
//
// It reads the registered server rather than standing up an ephemeral copy the
// way the sibling does: that copy exists there only because its server needs
// credentials to register, and this one needs none. Listing the real server is
// both simpler and impossible to drift from what a client actually sees.
//
// Resources and resource templates are always empty: this server registers
// none. The keys are still emitted, because the card's shape is the contract a
// scanner reads and an absent key is a different statement from an empty list.
func buildServerCard(ctx context.Context, server *mcp.Server, identity telemetry.IdentityPolicy) ([]byte, error) {
	st, ct := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, st, nil)
	if err != nil {
		return nil, fmt.Errorf("server connect: %w", err)
	}
	defer func() { _ = serverSession.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "server-card-builder", Version: "0"}, nil)
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("client connect: %w", err)
	}
	defer func() { _ = session.Close() }()

	var card serverCard
	card.ServerInfo = session.InitializeResult().ServerInfo
	card.Capabilities = session.InitializeResult().Capabilities
	// Keyless by design: no account, no API key, nothing to present. Saying so
	// explicitly is more useful to a scanner than omitting the block.
	card.Authentication.Required = false
	card.Authentication.Schemes = []string{}
	// The paginated iterators rather than a single ListTools/ListPrompts call:
	// one call returns one page, so a surface that outgrew the page size would
	// publish a card silently missing its tail. Four tools and four prompts fit
	// today; the card should not depend on that staying true.
	card.Tools = []serverCardTool{}
	for t, tErr := range session.Tools(ctx, nil) {
		if tErr != nil {
			return nil, fmt.Errorf("list tools: %w", tErr)
		}
		card.Tools = append(card.Tools, serverCardTool{
			Name:         t.Name,
			Title:        t.Title,
			Description:  t.Description,
			InputSchema:  t.InputSchema,
			OutputSchema: t.OutputSchema,
			Annotations:  t.Annotations,
			Icons:        t.Icons,
		})
	}
	card.Prompts = []serverCardPrompt{}
	for p, pErr := range session.Prompts(ctx, nil) {
		if pErr != nil {
			return nil, fmt.Errorf("list prompts: %w", pErr)
		}
		args := make([]serverCardPromptArgument, 0, len(p.Arguments))
		for _, a := range p.Arguments {
			args = append(args, serverCardPromptArgument{
				Name:        a.Name,
				Title:       a.Title,
				Description: a.Description,
				Required:    a.Required,
			})
		}
		card.Prompts = append(card.Prompts, serverCardPrompt{
			Name:        p.Name,
			Title:       p.Title,
			Description: p.Description,
			Arguments:   args,
			Icons:       p.Icons,
		})
	}
	card.Resources = []any{}
	card.ResourceTemplates = []any{}
	card.Observability = cardObservability(identity)

	return json.Marshal(card)
}
