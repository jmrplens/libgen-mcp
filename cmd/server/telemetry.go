// telemetry.go brings the OpenTelemetry providers up and takes them down again.

package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jmrplens/libgen-mcp/internal/config"
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
func startTelemetry(ctx context.Context, cfg *config.Config) (stop func(context.Context), err error) {
	signals, err := telemetry.ParseSignals(cfg.TelemetrySignals)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", config.EnvName("TELEMETRY_SIGNALS"), err)
	}

	provider, startErr := telemetry.Start(ctx, telemetry.Config{
		Enabled:        cfg.Telemetry,
		ServiceVersion: buildversion.Current(),
		Signals:        signals,
	})
	if startErr != nil {
		slog.ErrorContext(ctx, "telemetry disabled: it could not be started",
			"component", "telemetry", "error", startErr)
		return func(context.Context) {
			// Nothing to shut down: the provider never started, and the caller
			// still defers this unconditionally.
		}, nil
	}

	if provider.Enabled() {
		announceTelemetry(ctx, provider)
	}
	return func(shutdownCtx context.Context) {
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
func announceTelemetry(ctx context.Context, provider *telemetry.Provider) {
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
