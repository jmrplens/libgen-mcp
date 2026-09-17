// telemetry.go brings the OpenTelemetry providers up and takes them down again.

package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/mcpotel"
	"github.com/jmrplens/libgen-mcp/internal/telemetry"
	buildversion "github.com/jmrplens/libgen-mcp/internal/version"
)

// startTelemetry brings up the OpenTelemetry providers and returns the function
// that retires them.
//
// A failure to start is logged and swallowed. The specification permits either
// answer ("The API or SDK MAY fail fast and cause the application to fail on
// initialization... but MUST NOT cause the application to fail later at
// runtime"), so this is a decision rather than a rule — and the decision is that
// a server which can still reach the mirrors must keep doing so when it cannot
// reach a collector. The official Go examples model the opposite, with a dozen
// calls to panic on one page, and copying them would let a telemetry
// misconfiguration take down a working server.
//
// A selection that does not parse is the exception, and it is refused before
// anything starts: LIBGEN_MCP_TELEMETRY_SIGNALS is this server's own variable,
// and a value that is set but unparseable is an error here the same as it is
// everywhere else on this surface.
//
// The returned stop function is always usable — with telemetry off, after a
// failed start, and after a successful one — so the caller defers it
// unconditionally rather than carrying a nil check into the exit path, which is
// the worst place to put one because it runs after the work succeeded.
func startTelemetry(ctx context.Context, cfg *config.Config) (identity identityChoice, stop func(context.Context), err error) {
	signals, err := telemetry.ParseSignals(cfg.TelemetrySignals)
	if err != nil {
		return identityChoice{}, nil, fmt.Errorf("%s: %w", config.EnvName("TELEMETRY_SIGNALS"), err)
	}
	identity, err = resolveIdentity(cfg)
	if err != nil {
		return identityChoice{}, nil, err
	}

	provider, startErr := telemetry.Start(ctx, telemetry.Config{
		Enabled:        cfg.Telemetry,
		ServiceVersion: buildversion.Current(),
		Signals:        signals,
	})
	if startErr != nil {
		slog.ErrorContext(ctx, "telemetry disabled: it could not be started",
			"component", "telemetry", "error", startErr)
		return identity, func(context.Context) {
			// Nothing to shut down: the provider never started, and the caller
			// still defers this unconditionally. The identity choice is returned
			// anyway: the span middleware is installed whether or not an
			// exporter is, because the OpenTelemetry API's no-ops cost nothing,
			// and it must redact the same way either way.
		}, nil
	}

	if provider.Enabled() {
		announceTelemetry(ctx, provider, identity)
	}
	return identity, func(shutdownCtx context.Context) {
		if shutdownErr := provider.Shutdown(shutdownCtx); shutdownErr != nil {
			slog.WarnContext(ctx, "telemetry did not shut down cleanly",
				"component", "telemetry", "error", shutdownErr)
		}
	}, nil
}

// announceTelemetry says where the batches are going, and warns when a
// credential is going with them in the clear.
//
// At WARN rather than INFO, both of them. This is the only local evidence that
// anything about this deployment leaves the machine, and LOG_LEVEL is free to
// suppress everything below it — so a deployment running at warn would export
// every call with nothing on its own stderr naming the collector, and the
// exported copy is no help to somebody looking for the destination.
func announceTelemetry(ctx context.Context, provider *telemetry.Provider, identity identityChoice) {
	// The policy in the same breath as the destination, in words rather than as
	// a mode name: "pseudonymous" means nothing on its own, and an operator who
	// misconfigured this deserves to notice at startup rather than from a
	// backend three weeks later.
	slog.WarnContext(ctx, "telemetry identity policy",
		"component", "telemetry",
		"policy", string(identity.policy),
		"exports", telemetry.PolicyDescription(identity.policy),
		"key", identity.keySource(),
		"rotation", identity.rotationDescription())

	// Said once, at the moment it applies. The documentation carries the same
	// warning in prose, which reaches an operator who read that section; this
	// reaches the one who did not.
	if affected := telemetry.InsecureCredentialSignals(provider.Signals()); len(affected) > 0 {
		slog.WarnContext(ctx, "a collector credential is configured against a plaintext endpoint on another host; it crosses the network in the clear on every export",
			"component", "telemetry", "signals", affected)
	}

	// Per signal as well as in summary. The two scalars are empty when the
	// enabled signals disagree, which is the honest answer for a summary and a
	// useless one for an operator looking at their own deployment: they want to
	// know which collector each signal will actually reach, and that is exactly
	// the case where no single answer exists.
	snapshot := provider.Snapshot()
	slog.WarnContext(ctx, "telemetry enabled",
		"component", "telemetry",
		"protocol", snapshot.Protocol,
		"endpoint", snapshot.Endpoint,
		"protocols", snapshot.SignalProtocols,
		"endpoints", snapshot.SignalEndpoints,
		"signals", snapshot.Signals)
}

// identityChoice is the identity half of the telemetry configuration, resolved
// once so the startup line and the redactors agree about it.
type identityChoice struct {
	policy telemetry.IdentityPolicy
	keys   *telemetry.Keyring
	// caller redacts who made a call; item redacts what the call was about. Both
	// are built over the one keyring, so an operator's secret governs both and
	// neither can outlive the other.
	caller *telemetry.Redactor
	item   *telemetry.ItemRedactor
}

// keySource says where the pseudonymisation secret came from, for the startup
// line.
//
// Whether the key is configured is the single most consequential thing about a
// multi-replica deployment's telemetry — it decides whether one caller has one
// digest across the fleet or one per replica — and it is invisible from the
// outside, since both produce digests that look identical.
func (c identityChoice) keySource() string {
	switch {
	case c.keys == nil:
		return "none (no pseudonym is emitted)"
	case c.keys.Configured():
		return "configured (" + config.EnvName("TELEMETRY_IDENTITY_KEY") + "), shared across replicas and never rotated here"
	default:
		return "generated at startup, written nowhere, not shared across replicas"
	}
}

// rotationDescription renders the lifetime of a generated key.
func (c identityChoice) rotationDescription() string {
	if c.keys == nil || c.keys.Configured() || c.keys.Rotation() <= 0 {
		return "none (the key lives as long as the process)"
	}
	return c.keys.Rotation().String()
}

// resolveIdentity parses the identity policy and builds the keyring the
// pseudonyms are computed under.
//
// The keyring is built even under `none`, and deliberately: the item redactor
// digests an identifier under every policy, so a deployment that records nothing
// about its callers still tells one failing book from a failing mirror.
//
// Both refusals are startup errors rather than warnings, because both are this
// server's own variables: a policy nobody can parse and a rotation interval out
// of range are deployments that do not match their own configuration.
func resolveIdentity(cfg *config.Config) (identityChoice, error) {
	policy, err := telemetry.ParseIdentityPolicy(cfg.TelemetryIdentity)
	if err != nil {
		return identityChoice{}, fmt.Errorf("%s: %w", config.EnvName("TELEMETRY_IDENTITY"), err)
	}

	keys, err := telemetry.NewKeyring(cfg.TelemetryIdentityKey, cfg.TelemetryIdentityRotation)
	if err != nil {
		return identityChoice{}, fmt.Errorf("%s: %w", config.EnvName("TELEMETRY_IDENTITY_ROTATION"), err)
	}
	if keys.Configured() && cfg.TelemetryIdentityRotation > 0 {
		// Said rather than silently obeyed or silently ignored. An operator who
		// set both has a mental model where one of them does nothing, and which
		// one is not guessable from the outside.
		slog.Warn("the identity key is configured, so the rotation interval is ignored",
			"component", "telemetry",
			"key", config.EnvName("TELEMETRY_IDENTITY_KEY"),
			"rotation", config.EnvName("TELEMETRY_IDENTITY_ROTATION"),
			"reason", "rotating a key the operator supplied would destroy the correlation they configured it for")
	}

	return identityChoice{
		policy: policy,
		keys:   keys,
		caller: telemetry.NewRedactor(policy, keys),
		item:   telemetry.NewItemRedactor(policy, keys),
	}, nil
}

// spanOptions builds what the MCP span middleware needs from this deployment.
//
// It is here rather than in newMCPServer because every input is a telemetry
// decision: which transport the convention's vocabulary calls this, which
// protocol revisions may reach a metric dimension, and how much a span may say
// about who called.
func spanOptions(listenAddr string, identity identityChoice) mcpotel.Options {
	transport := mcpotel.TransportPipe
	if listenAddr != "" {
		transport = mcpotel.TransportTCP
	}
	return mcpotel.Options{
		Callers: callerAttributer(identity),
		// The convention's own vocabulary, not names of our choosing: "pipe"
		// for stdio and "tcp" for streamable HTTP, a unix socket included —
		// the attribute describes the shape of the transport, and a socket is
		// a network transport that happens not to cross a wire.
		Transport: transport,
		// Only a revision this server admits is ever recorded. The value
		// arrives from the caller and lands on a metric dimension, so an
		// unbounded one would let a client mint time series by typing.
		ProtocolVersions: mcp.SupportedProtocolVersions(),
	}
}

// callerAttributer turns the charged client address into the attributes the
// identity policy allows on a span.
//
// The address is the one every per-caller budget is already keyed on, read back
// from the header the HTTP layer stamped, so a pseudonym on a span and a bucket
// in the rate limiter name the same caller. On stdio there is no header and no
// address, and every policy yields nothing — which is right: one process, one
// person, nothing to tell apart.
//
// The client's own name and version come from the session's initialize
// parameters, which exist on both transports, and are recorded only under the
// full policy: they name software rather than a person.
func callerAttributer(identity identityChoice) mcpotel.CallerAttributer {
	return mcpotel.CallerAttributerFunc(func(_ context.Context, req mcp.Request) []attribute.KeyValue {
		caller := telemetry.Caller{Address: chargedAddressOf(req)}
		if session, ok := req.GetSession().(*mcp.ServerSession); ok && session != nil {
			caller.Session = session.ID()
			if params := session.InitializeParams(); params != nil && params.ClientInfo != nil {
				caller.ClientName = params.ClientInfo.Name
				caller.ClientVersion = params.ClientInfo.Version
			}
		}
		return identity.caller.Attributes(caller)
	})
}
