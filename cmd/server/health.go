// health.go answers /health: whether this process is serving or draining, which
// build it is, and a fingerprint of the configuration that shapes what it
// serves.
//
// The endpoint needs no credential, and what it answers is chosen so that it
// cannot need one. A balancer, an orchestrator and a person with curl ask it the
// same two questions — is it up, and which one is it — so the body carries no
// counters, no configuration values and no upstream round-trip.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
	buildversion "github.com/jmrplens/libgen-mcp/internal/version"
)

// The two verdicts this endpoint reports.
const (
	healthStatusOK       = "ok"
	healthStatusDraining = "draining"
)

// maxDrainDelay bounds --drain-delay. Past a few minutes the delay is no longer
// a handover, it is a shutdown that appears to hang — and every supervisor kills
// the process long before it elapses.
const maxDrainDelay = 5 * time.Minute

// processStartTime marks when this process began serving.
//
// Package-level initialization runs before main, so this is the earliest instant
// the program can observe about itself. Tests do not override it:
// newHealthResponse takes both instants as parameters instead, so uptime is
// deterministic without a mutable package-level clock.
var processStartTime = time.Now()

// announceDraining flips /health to draining and, when a delay is configured,
// holds the listener open that long before the caller closes it.
//
// Without the delay the close is what a balancer notices, one probe later, and
// every request it sent in that window failed. With a delay of at least one
// probe interval the balancer sees the 503 first and stops sending work before
// the listener goes.
//
// The flag belongs to one listener rather than to the process, and is never
// cleared: a server that has begun draining does not come back to serving, but
// the next listener built in the same process starts out serving.
func announceDraining(ctx context.Context, draining *atomic.Bool, delay time.Duration) {
	draining.Store(true)
	if delay <= 0 {
		slog.InfoContext(ctx, "HTTP server shutdown requested")
		return
	}
	slog.InfoContext(ctx, "HTTP server shutdown requested, announcing draining before closing the listener",
		"drain_delay", delay)
	time.Sleep(delay)
}

// healthResponse is the JSON body returned by the /health endpoint. The field
// names match the sibling gitlab-mcp-server so one probe can read both servers.
//
// Liveness is reported two ways on purpose. StartedAt is the stable fact: it
// does not change between probes, so a monitor can cache it, deduplicate it, and
// detect a restart by noticing it moved — the same reason Prometheus exposes
// process_start_time_seconds rather than an uptime counter. UptimeSeconds is the
// derived convenience value, in the unit the IETF health check draft uses for it
// ("observedUnit": "s").
type healthResponse struct {
	// Status is ok while the process is serving and draining once shutdown was
	// requested; the HTTP status carries the same verdict, 200 or 503.
	Status string `json:"status"`
	// Version is the release this build reports, stamped or compiled in.
	Version string `json:"version"`
	// Commit is the revision the release ldflags stamped, or "none".
	Commit string `json:"commit"`
	// Build is the one string a display wants: the release this build is closest
	// to plus the short commit it was built from. A tag build and a build from
	// main report comparable shapes, where Version alone gives one a plain
	// number and the other a Go pseudo-version with a timestamp in it.
	Build string `json:"build"`
	// ConfigDigest fingerprints the settings that decide what a client sees, so
	// a monitor can tell whether the replicas behind one balancer are configured
	// alike without any of them publishing its configuration.
	ConfigDigest string `json:"config_digest,omitempty"`
	// StartedAt is the process start instant in RFC 3339, matching how this
	// project renders timestamps everywhere else.
	StartedAt string `json:"started_at"`
	// UptimeSeconds is whole seconds since StartedAt. Sub-second precision would
	// be noise on an endpoint polled at probe intervals.
	UptimeSeconds int64 `json:"uptime_seconds"`
}

// newHealthResponse builds the /health body for a start instant observed at now.
// Both instants are parameters so the uptime arithmetic can be tested without
// mutating a package-level clock from concurrent tests.
//
// Version comes from buildversion rather than the raw ldflags variable: that one
// is empty unless a release stamped it, whereas buildversion falls back to the
// number compiled in from VERSION, so a development build reports what it
// actually is instead of a placeholder.
func newHealthResponse(startedAt, now time.Time, digest string, isDraining bool) healthResponse {
	// Truncating instead of rounding keeps uptime from reporting a second that
	// has not fully elapsed. The clamp guards a caller that observes an instant
	// before the start; time.Now within one process cannot, because its
	// monotonic reading never goes backwards.
	uptime := int64(now.Sub(startedAt).Seconds())
	uptime = max(uptime, 0)
	status := healthStatusOK
	if isDraining {
		status = healthStatusDraining
	}
	return healthResponse{
		Status:        status,
		Version:       buildversion.Current(),
		Commit:        commit,
		Build:         buildIdentifier(buildversion.Current(), commit),
		ConfigDigest:  digest,
		StartedAt:     startedAt.UTC().Format(time.RFC3339),
		UptimeSeconds: uptime,
	}
}

// healthHandler returns the /health handler for a listener whose configuration
// fingerprint is digest and whose draining flag is draining. It requires no
// authentication.
func healthHandler(digest string, draining *atomic.Bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		body := newHealthResponse(processStartTime, time.Now(), digest, draining != nil && draining.Load())
		w.Header().Set(headerContentType, mediaTypeJSON)
		if body.Status == healthStatusDraining {
			// A balancer must not serve the last 200 out of a cache across the
			// flip; the whole point of the flip is that it is noticed.
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		// A client that went away mid-write is not something this handler can
		// act on.
		_ = json.NewEncoder(w).Encode(body)
	}
}

// pseudoVersion matches what the Go toolchain records for a build from a working
// tree that is not a tag: the next patch version, a timestamp and a
// twelve-character commit, with +dirty when the tree had changes.
var pseudoVersion = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)-0\.\d{14}-([0-9a-f]{12})(\+dirty)?$`)

// buildIdentifier renders a build as one displayable string: the release it is
// closest to, plus the short commit it was built from, plus ".dirty" when the
// tree had uncommitted changes.
//
// A release binary is stamped with its version, so it reads "1.7.2+404e367". A
// build from main carries a pseudo-version such as
// "1.7.3-0.20260903061404-6e6ff5beb20e+dirty" — correct provenance and unusable
// as a label, because it encodes the patch that does not exist yet. The release
// it is closest to is the one before: "1.7.2+6e6ff5b.dirty". With no commit
// recorded at all, the version is all there is.
func buildIdentifier(version, commit string) string {
	base := strings.TrimSuffix(version, "+dirty")
	dirty := strings.HasSuffix(version, "+dirty")
	short := shortCommit(commit)
	if m := pseudoVersion.FindStringSubmatch(version); m != nil {
		base = previousPatch(m)
		if short == "" {
			short = m[4][:7]
		}
	}
	out := base
	if short != "" {
		out += "+" + short
	}
	if dirty {
		out += ".dirty"
	}
	return out
}

// previousPatch renders the release a pseudo-version is closest to: the patch
// below the one the toolchain invented, or that patch itself when it is already
// zero and there is nothing below it.
func previousPatch(m []string) string {
	patch, err := strconv.Atoi(m[3])
	if err != nil || patch <= 0 {
		return m[1] + "." + m[2] + "." + m[3]
	}
	return m[1] + "." + m[2] + "." + strconv.Itoa(patch-1)
}

// shortCommit is the seven-character form of a commit hash, or empty when none
// was recorded.
func shortCommit(commit string) string {
	commit = strings.TrimSpace(commit)
	if commit == "" || commit == "none" {
		return ""
	}
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// configDigest fingerprints the settings that decide the surface a client sees
// and the answers it can give.
//
// Replicas behind one balancer must agree on every one of them, or a client gets
// a different catalog depending on which node it reaches — and nothing else
// detects that. server_fetch is in it because it decides whether the read tool
// is registered at all; remote_downloads because it decides whether download
// saves a file or returns a link; the sources list because it decides which
// download chain exists; the base path and statelessness because they decide
// what the endpoint is and what a GET answers.
//
// What is deliberately out: counters, configuration values themselves, and
// anything needing an upstream round-trip. **The digest is a fingerprint for
// comparison, not a secret.** The settings it covers are few and public, so
// whoever reads it can work out which combination produced it; nothing here is a
// credential, and treating it as one would be a mistake in the other direction.
//
// Order-free wherever the setting is a set, so two replicas that list the same
// sources in a different order agree.
func configDigest(cfg *config.Config, basePath string, stateless bool) string {
	if cfg == nil {
		return ""
	}
	sources := slices.Clone(cfg.Sources)
	slices.Sort(sources)
	fields := []string{
		"sources=" + strings.Join(sources, ","),
		"extra_sources=" + string(cfg.ExtraSources),
		"server_fetch=" + tristate(cfg.ServerFetch),
		"remote_downloads=" + strconv.FormatBool(cfg.RemoteDownloads),
		"enrich=" + strconv.FormatBool(cfg.EnrichEnabled),
		"confirm_downloads=" + strconv.FormatBool(cfg.ConfirmDownloads),
		"base_path=" + basePath,
		"stateless=" + strconv.FormatBool(stateless),
		// The three limits that decide what an identical call gets back.
		// MaxDownloadBytes refuses a file outright, and the read limits change
		// the answer whenever a caller leaves its own limits out — which is
		// most calls. Two replicas that differ on any of them serve different
		// results, and a digest that matched would say they did not.
		"max_download_bytes=" + strconv.FormatInt(cfg.MaxDownloadBytes, 10),
		"read_max_chars=" + strconv.Itoa(cfg.ReadMaxChars),
		"read_default_pages=" + strconv.Itoa(cfg.ReadDefaultPages),
	}
	sum := sha256.Sum256([]byte(strings.Join(fields, "\n")))
	return hex.EncodeToString(sum[:])[:12]
}

// tristate renders an optional boolean, keeping "the operator did not say"
// distinct from either answer.
//
// LIBGEN_MCP_SERVER_FETCH is a *bool precisely because unset is a third state —
// it resolves from the transport — so folding it into false here would give a
// stdio replica and an HTTP replica the same digest while they serve different
// tool lists.
func tristate(v *bool) string {
	if v == nil {
		return "unset"
	}
	return strconv.FormatBool(*v)
}
