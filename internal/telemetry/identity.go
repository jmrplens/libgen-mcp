package telemetry

import (
	"fmt"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel/attribute"
)

// IdentityPolicy decides what a signal leaving this process may say about who
// made a call.
//
// It is deliberately not a log level. Verbosity is how much is recorded;
// identity is what each record contains, and fusing them fails both ways: at
// WARN the identity would disappear from exactly the records where it matters
// most — a refusal, a throttle, a failure — and at DEBUG it would arrive
// alongside a flood nobody asked for.
//
// It is also not scoped to logs. A span attribute and a metric label carry an
// address just as easily, so a policy that governed only the log signal would
// leave identity flowing through traces while claiming to be off.
//
// # The rule
//
// What leaves the process is redacted unless the operator says otherwise; what
// stays on the operator's own stderr is not. That boundary is what keeps this
// from quietly changing the existing logs: stderr is unchanged for every
// deployment, and nothing about a caller crosses to a collector until somebody
// asks for it.
//
// # There is no user here
//
// This server authenticates nobody. A caller is an address, and on the hosted
// endpoint it is a public one — so the identity being governed is not "which of
// our employees" but "which stranger", which is a different question with a
// different answer. It is also personal data under the GDPR (the Breyer line
// covers a dynamic IP held by someone who can have it attributed), which is why
// [IdentityFull] is a decision with a legal shape and why the default is
// [IdentityNone].
type IdentityPolicy string

const (
	// IdentityNone exports nothing about who made a call. The default, because
	// it is the only value that is safe for an operator who has not thought
	// about the question.
	IdentityNone IdentityPolicy = "none"
	// IdentityPseudonymous exports a keyed per-caller digest and no readable
	// identity. It keeps the one thing a shared endpoint genuinely needs —
	// telling one caller's traffic from another's, so a burst or an abuse
	// pattern can be seen — while naming nobody.
	IdentityPseudonymous IdentityPolicy = "pseudonymous"
	// IdentityFull exports the caller's address and the client it connected
	// with. What an operator running this for a known group of people may
	// legitimately want; not what a public endpoint should default to.
	IdentityFull IdentityPolicy = "full"
)

// The policy arrives as a parsed value rather than being read here.
// LIBGEN_MCP_TELEMETRY_IDENTITY is read in internal/config with every other
// variable this server defines, so there is one reader per setting and the name
// is in one list.

// DefaultIdentityPolicy is what an operator gets without deciding.
const DefaultIdentityPolicy = IdentityNone

// PolicyDescription says in plain words what a policy exports, for the startup
// log.
//
// It exists so the one line an operator reads says what will actually leave the
// process, rather than a mode name they would have to look up. "pseudonymous"
// means nothing on its own; "a keyed digest of the caller's address, with no
// readable identity" is the sentence that lets somebody notice a mistake.
func PolicyDescription(policy IdentityPolicy) string {
	switch policy {
	case IdentityFull:
		return "the caller's address and the client it connected with"
	case IdentityPseudonymous:
		return "a keyed digest of the caller's address, with no readable identity"
	case IdentityNone:
		return "nothing about who made a call"
	default:
		return "nothing about who made a call"
	}
}

// ParseIdentityPolicy validates an operator-supplied value.
func ParseIdentityPolicy(value string) (IdentityPolicy, error) {
	switch IdentityPolicy(strings.TrimSpace(strings.ToLower(value))) {
	case "":
		return DefaultIdentityPolicy, nil
	case IdentityNone:
		return IdentityNone, nil
	case IdentityPseudonymous:
		return IdentityPseudonymous, nil
	case IdentityFull:
		return IdentityFull, nil
	default:
		return "", fmt.Errorf("unknown identity policy %q: use %q, %q or %q",
			value, IdentityNone, IdentityPseudonymous, IdentityFull)
	}
}

// The slog field names a record may carry on the stderr leg that the exported
// copy must not.
//
// Declared here rather than where they are written, because the export-side
// redactor has to find them by name, and two spellings of the same field would
// mean the rule silently applies to neither.
const (
	// LogFieldQuery is the search terms a caller sent. It is the single most
	// sensitive value this server handles: what somebody looked for says more
	// about them than which address they used. It is stripped rather than
	// policy-governed, because there is no policy under which a collector
	// should receive it.
	LogFieldQuery = "query"
	// LogFieldTitle is a record's title, which is the same disclosure arriving
	// by the other direction: the answer rather than the question.
	LogFieldTitle = "title"
	// LogFieldAnnasKey and LogFieldUnpaywallEmail name the two credentials a
	// caller may supply per call. They are used once and never stored, and
	// nothing deliberately logs them — the list exists for the record that
	// starts carrying one later, which is exactly how this class of defect
	// arrives.
	//
	// The names of fields, never credentials.
	LogFieldAnnasKey       = "annas_key"
	LogFieldUnpaywallEmail = "unpaywall_email"
)

// ExportStrippedFields are the log fields removed from the exported copy at any
// depth, by name.
//
// **A named list rather than a memory.** The defect this prevents is a field
// added to a log line that the export-side redactor had no reason to know about
// — it survives every policy untouched, because no policy has heard of it. A
// field belongs here when it discloses something no operator would knowingly
// send to a collector, whether or not it identifies anyone.
//
// The call sites keep writing them: stderr is the operator's own terminal, and
// a search this server ran is theirs to see.
//
// # What a name-based list cannot do
//
// It cannot help when the value is *inside* something else — a credential in a
// query string inside a URL inside a `*url.Error`'s message, which is what
// `net/http` produces and what would otherwise reach two sinks at once. That
// shape is handled where the error is made, by `netguard.RedactTransportError`
// and `netguard.RedactURLString`, and not here. A reader who finds this list
// will otherwise assume it covers that case, so: it does not.
var ExportStrippedFields = map[string]bool{
	LogFieldQuery:          true,
	LogFieldTitle:          true,
	LogFieldAnnasKey:       true,
	LogFieldUnpaywallEmail: true,
}

// StripExported removes every field in [ExportStrippedFields] from a set of log
// attributes, at any depth.
//
// It is the one place that decides, so the rule cannot be applied on one path
// and skipped on another. Both suppliers of exported attributes go through it:
// a record's own attributes, and the attributes a derived logger attaches with
// slog.With — having two paths was the sibling project's hole, since a component
// logger built with With bypassed the policy for whatever it attached.
//
// # Two things it does that the obvious version does not
//
// **It descends into groups.** slog.Group is an ordinary value any caller can
// pass, and a group was enough to smuggle a governed field past a flat name
// check.
//
// **It resolves before it inspects.** A [slog.LogValuer] is opaque until asked,
// so judging the unresolved value would wave through whatever it later resolves
// to — which is to say, exactly the lazily-built values a hot path uses to avoid
// formatting a query it may not log.
func StripExported(attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		attr.Value = attr.Value.Resolve()
		if ExportStrippedFields[attr.Key] {
			continue
		}
		if attr.Value.Kind() == slog.KindGroup {
			attr.Value = slog.GroupValue(StripExported(attr.Value.Group())...)
		}
		out = append(out, attr)
	}
	return out
}

// identitySaltBytes sizes the pseudonymisation key.
const identitySaltBytes = 32

// Caller is what this process knows about who made a call.
//
// Every field is optional and a zero value yields nothing under every policy,
// which is the honest answer on stdio: there is one caller, it is the person who
// started the process, and there is nothing to tell apart.
type Caller struct {
	// Address is the charged client address — the peer, or the address a
	// trusted proxy forwarded. It is the same value every per-caller budget is
	// keyed on, so a pseudonym and a rate-limit bucket name the same caller.
	Address string
	// Session is the Mcp-Session-Id, which exists only under --stateless=false.
	// Under the default stateless transport there is none, and the address is
	// the whole of the identity.
	Session string
	// ClientName and ClientVersion are what the client called itself in
	// initialize. They name software rather than a person, which is why they
	// are in the full policy and not the pseudonymous one: a digest of
	// "claude-code/1.4" would correlate nothing while hiding nothing.
	ClientName    string
	ClientVersion string
}

// empty reports whether there is anything to say about this caller at all.
func (c Caller) empty() bool {
	return strings.TrimSpace(c.Address) == "" && strings.TrimSpace(c.Session) == ""
}

// pseudonymInput is what the digest is computed over.
//
// The session joins the address when there is one, so two sessions from one
// address are told apart under --stateless=false — which is the only mode where
// a session exists at all. Under the stateless default this is the address and
// nothing else.
func (c Caller) pseudonymInput() string {
	address := strings.TrimSpace(c.Address)
	session := strings.TrimSpace(c.Session)
	if session == "" {
		return address
	}
	return address + "\x00" + session
}

// Redactor turns a caller into the attributes a signal may carry.
//
// The zero value redacts everything, so a caller that never configured one
// cannot leak by forgetting.
type Redactor struct {
	policy IdentityPolicy
	// keys holds the secret the digest is computed under, and decides how long
	// it lives. Shared with every other pseudonym this process emits, because
	// two keyrings would give one caller two digests that no single signal
	// could show to be the same caller. See [Keyring].
	keys *Keyring
}

// NewRedactor builds the redactor for a policy, over the process's keyring.
//
// A nil keyring is accepted and yields no digest, which is the same answer the
// zero value gives: a caller that never wired one records nothing rather than
// emitting something that looks like a pseudonym and is not. Nothing here can
// fail: the policy was validated where it was parsed, and the keyring is
// whatever the caller built, nil included.
func NewRedactor(policy IdentityPolicy, keys *Keyring) *Redactor {
	return &Redactor{policy: policy, keys: keys}
}

// Policy reports what this redactor was built for.
func (r *Redactor) Policy() IdentityPolicy {
	if r == nil || r.policy == "" {
		return DefaultIdentityPolicy
	}
	return r.policy
}

// Attributes returns what may be recorded about a caller.
//
// An empty caller yields nothing under every policy, which is what stdio
// produces: there is no address, no session, and no identity to redact or
// publish.
func (r *Redactor) Attributes(caller Caller) []attribute.KeyValue {
	if caller.empty() {
		return nil
	}
	switch r.Policy() {
	case IdentityFull:
		return fullAttributes(caller)
	case IdentityPseudonymous:
		digest := r.keys.IdentityPseudonym(caller.pseudonymInput())
		if digest == "" {
			// A keyring that failed to build yields no digest, and an empty
			// user.hash would read as one anonymous caller while meaning "the
			// keyring is broken". Absence is the honest value.
			return nil
		}
		return []attribute.KeyValue{attribute.String(AttrUserHash, digest)}
	default:
		return nil
	}
}

// fullAttributes is what the full policy records: the address, and the client
// that connected.
func fullAttributes(caller Caller) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 3)
	if address := strings.TrimSpace(caller.Address); address != "" {
		attrs = append(attrs, attribute.String(AttrClientAddress, address))
	}
	if name := strings.TrimSpace(caller.ClientName); name != "" {
		attrs = append(attrs, attribute.String(AttrMCPClientName, name))
	}
	if version := strings.TrimSpace(caller.ClientVersion); version != "" {
		attrs = append(attrs, attribute.String(AttrMCPClientVersion, version))
	}
	if len(attrs) == 0 {
		return nil
	}
	return attrs
}

// The registry-defined keys this policy emits.
//
// Not user.id and user.name, which is where the sibling project puts its
// equivalents: those describe a person a server authenticated, and this server
// authenticates nobody. Recording an IP address under user.id would tell a
// backend that a person was identified when an address was observed, which is a
// claim in the schema rather than in prose and a worse one for being machine
// readable.
const (
	// AttrUserHash is "Unique user hash to correlate information for a user in
	// anonymized form", to be used "when user.id or user.name contain
	// confidential data". That is this policy's pseudonymous mode exactly, and
	// the key is kept even though no user is named: it is what a backend already
	// knows how to group by, and inventing a key in a namespace OpenTelemetry
	// owns is what the naming guidance rules out.
	//
	// One objection deserves an answer rather than a dismissal: a hash over IPv4
	// is reversible by enumeration, because the input space is four billion and
	// a laptop can walk it. That is true of a plain digest and false of what
	// [Keyring.IdentityPseudonym] computes, which is an HMAC under a 32-byte key
	// that is either generated per process and never written down or supplied by
	// the operator and kept as the secret it is. Without the key there is
	// nothing to enumerate against. What remains is inherent to pseudonymity:
	// somebody who can correlate a known caller's activity with a digest can
	// link the two, which is why the mode is called pseudonymous and not
	// anonymous.
	AttrUserHash = "user.hash"

	// AttrClientAddress is the registry's "Client address - domain name if
	// available without reverse DNS lookup; otherwise, IP address or Unix domain
	// socket name". The address this server charged the call to.
	AttrClientAddress = "client.address"

	// AttrMCPClientName and AttrMCPClientVersion are the MCP convention's names
	// for what a client called itself in initialize. They name software, not a
	// person.
	AttrMCPClientName    = "mcp.client.name"
	AttrMCPClientVersion = "mcp.client.version"
)
