// telemetry.go brings the OpenTelemetry providers up and takes them down again.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/logging"
	"github.com/jmrplens/libgen-mcp/internal/mcpotel"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
	"github.com/jmrplens/libgen-mcp/internal/netguard"
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

	// Installed unconditionally, before any outbound client is built, and not
	// behind provider.Enabled(): the OpenTelemetry API's no-ops cost a nil check
	// with no SDK installed, and the observer is also what strips baggage and
	// trace context from every outbound request — a rule that must hold whether
	// or not anybody is collecting. Which hosts the metric may name is bounded
	// here for the same reason, since a mirror list is discovered and an
	// open-access index hands back a publisher's own URL.
	mcpotel.SetMetricServerAddresses(outboundMetricHosts(cfg))
	restoreObserver := netguard.SetOutboundObserver(mcpotel.NewTransport)

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
			//
			// The observer is restored here too. It was installed above, before
			// the start that failed, and it is process-global — so leaving it
			// behind on this path outlives what it writes into, which in a test
			// binary is one test's telemetry reaching every later test's
			// clients. That is the same leak the successful path takes care to
			// avoid, reached by the door nobody looks at.
			restoreObserver()
		}, nil
	}

	restoreLogger := func() {
		// Replaced below when the bridge is installed. Until then it is what the
		// stop function calls, so the "no bridge" case needs a body rather than
		// a nil check in the exit path.
	}
	if provider.Enabled() {
		// The logs signal is only real once something writes into it, and this
		// is that something: `telemetry.Start` installs the global logger
		// provider and nothing in this server had ever logged through it. Until
		// this line existed, "logs" was announced at startup and published on
		// the server card while no record was ever exported.
		//
		// It is gated on the signal as well as on the provider: with
		// LIBGEN_MCP_TELEMETRY_SIGNALS=traces the global logger provider is
		// still the no-op one, so bridging would cost every record a trip
		// through a handler that discards it.
		if provider.Signals().Logs {
			restoreLogger = installSlogBridge(cfg.LogLevel)
		}

		// After the bridge, so the announcement itself reaches the collector.
		// Before it, these lines went to stderr alone — which is the one place
		// an operator running several replicas is not looking, and they are the
		// lines that say what this deployment exports about its callers.
		announceTelemetry(ctx, provider, identity)
	}
	return identity, func(shutdownCtx context.Context) {
		if shutdownErr := provider.Shutdown(shutdownCtx); shutdownErr != nil {
			slog.WarnContext(ctx, "telemetry did not shut down cleanly",
				"component", "telemetry", "error", shutdownErr)
		}
		// After the provider, so a record written during shutdown still reaches
		// a collector that is listening, and so the bridge's lifetime matches
		// the provider's: the default logger is a process global, and leaving it
		// installed routes every later record at an exporter that has stopped.
		restoreLogger()
		// After the provider, so a client built during shutdown is not still
		// recording into a stopped exporter. The observer is process-global, so
		// leaving it installed would outlive what it writes into — which in a
		// test binary is one test's telemetry reaching every later test's
		// clients.
		restoreObserver()
	}, nil
}

// installSlogBridge sends every log record to the collector as well as to
// stderr, and returns the function that takes it back out.
//
// The stderr handler is rebuilt here rather than read back from slog.Default():
// the bridge must wrap a handler whose behavior is known, and the default is
// whatever the last caller installed. Reading it would work every time it was
// tried and fail the once it mattered.
func installSlogBridge(level slog.Level) func() {
	return logging.SetupWrapped(level, func(base slog.Handler) slog.Handler {
		return telemetry.NewSlogHandler(base, telemetry.DefaultLogSeverity)
	})
}

// outboundMetricHosts are the hosts the outbound duration metric may name.
//
// The closed set is what this deployment is configured to reach rather than
// everything it might: a mirror the discovery rotates to, and a publisher URL an
// open-access index hands back, are both hosts a third party chose. Anything
// outside the set is recorded as one bucket, so a caller cannot mint a time
// series by causing a fetch.
//
// The span still carries the real host either way. A trace has no series budget;
// a metric does.
func outboundMetricHosts(cfg *config.Config) []string {
	// The built-in mirror families as well as the operator's own settings. They
	// are what this deployment reaches on the ordinary path, and leaving them
	// out does not make the metric safer — it makes it useless, since every
	// catalog and download request then lands in the one bucket reserved for
	// hosts nobody configured.
	//
	// OperatorHosts returns URLs for the families and bare hosts for the
	// Sci-Hub list, and the dimension is compared against a hostname, so both
	// shapes are reduced here rather than at the comparison.
	named := mirrors.OperatorHosts(cfg)
	hosts := make([]string, 0, len(named)+len(cfg.ScihubHosts)+1)
	hosts = appendHostOf(hosts, named...)
	hosts = appendHostOf(hosts, cfg.ScihubHosts...)
	hosts = appendHostOf(hosts, cfg.Mirror)
	return hosts
}

// appendHostOf adds each value's hostname, accepting a URL or a bare host.
func appendHostOf(hosts []string, values ...string) []string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !strings.Contains(value, "//") {
			hosts = append(hosts, value)
			continue
		}
		if parsed, err := url.Parse(value); err == nil && parsed.Hostname() != "" {
			hosts = append(hosts, parsed.Hostname())
		}
	}
	return hosts
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
