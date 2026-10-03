// What a server start is configured by, how it is checked, and how a start that
// will not happen says why.

package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jmrplens/libgen-mcp/v2/internal/config"
	"github.com/jmrplens/libgen-mcp/v2/internal/logging"
	"github.com/jmrplens/libgen-mcp/v2/internal/transport"
)

// Exit statuses of a start that does not happen.
const (
	// exitRefused is a configuration the server will not run with.
	exitRefused = 1
	// exitUsage is a command line the flag parser cannot read, the status Go's
	// flag package exits with on its own.
	exitUsage = 2
)

// serverFlags is the command line a server start is configured by, beyond the
// variable-backed flags env_flags.go declares.
type serverFlags struct {
	// The invocations that are not a server: see runUtility.
	showVersion, healthcheck, shutdown *bool

	// Which transport, and where it listens.
	httpAddr, transportSelector, socketMode, tlsCert, tlsKey, httpPath *string

	// The streamable HTTP handler.
	stateless, jsonResponse *bool
	maxBody                 *int64

	// Who is calling, from where, and how much each caller may spend.
	trustedOrigins, publicURL, trustedProxyHeader, trustedProxies *string
	rateLimitRPS                                                  *float64
	rateLimitBurst, maxInflight                                   *int

	// How long the listener and its sessions are kept.
	drainDelay, sessionTimeout, httpIdleTimeout *time.Duration
}

// defineServerFlags declares the server's own flags on the process's flag set.
// Call before flag parsing.
func defineServerFlags() *serverFlags {
	return &serverFlags{
		httpAddr:           flag.String("http", "", "serve streamable HTTP here instead of stdio: an address (e.g. :8080) or a unix socket path (e.g. /run/mcp.sock, recognized by the path separator; a bare name like mcp.sock is read as a host)"),
		showVersion:        flag.Bool("version", false, "print version and exit"),
		healthcheck:        flag.Bool("healthcheck", false, "probe the running instance's /health and exit 0 when it answers, 1 when it does not. The listener is read off that instance's own command line — its --http, --http-path, --tls-cert and --transport — so a socket, a moved port, a mount under a prefix and TLS this process terminates are all probed correctly. A target may be given instead: an http(s) URL, unix:<path>, or host:port. This is not cmd/probe, which checks the live mirrors"),
		shutdown:           flag.Bool("shutdown", false, "ask every other instance of this binary on this machine to exit, then kill what is left after "+shutdownGracePeriod.String()+", or, for an instance with a --drain-delay, after its drain plus "+(httpShutdownTimeout+shutdownExitMargin).String()+" (at most "+maxShutdownGrace.String()+"). For an upgrade that swaps the binary while the old process still holds a download slot and a listener"),
		stateless:          flag.Bool("stateless", true, "stateless streamable HTTP (default; required for MCP protocol 2026-07-28): no Mcp-Session-Id, each POST self-contained, GET/DELETE return 405; use -stateless=false for legacy stateful sessions"),
		jsonResponse:       flag.Bool("json-response", false, "return application/json responses instead of text/event-stream (SSE)"),
		maxBody:            flag.Int64("max-request-body-bytes", 0, "maximum streamable HTTP request body size in bytes; 0 uses the SDK default (4 MiB)"),
		socketMode:         flag.String("http-socket-mode", "0660", "permission mode for a unix socket given to --http, as octal with or without a leading 0. Ignored for a TCP address, and refused on platforms without file modes"),
		tlsCert:            flag.String("tls-cert", "", "PEM certificate file; terminate TLS in this process instead of leaving it to a proxy in front. Requires --tls-key"),
		tlsKey:             flag.String("tls-key", "", "PEM private key file for --tls-cert"),
		httpPath:           flag.String("http-path", "/", "URL path the MCP endpoint answers on (e.g. /libgen). Every route — the endpoint, /health and the server card — is mounted under it, and any other path answers 404. Set it when a reverse proxy forwards its prefix instead of rewriting it away; leave it at / when the proxy strips the prefix or the server is reached directly"),
		trustedOrigins:     flag.String("trusted-origins", "", "comma-separated browser origins allowed to call this server cross-origin, as scheme://host[:port] (e.g. https://claude.ai). Empty (default) refuses every cross-origin browser request; \"*\" accepts any. Non-browser clients send no Origin and are unaffected either way"),
		transportSelector:  flag.String("transport", "", "which transport to serve: stdio, http, or auto. Empty (default) keeps the historical rule — a --http value means HTTP, no value means stdio. auto reads it off standard input: a pipe, terminal, file or socket means stdio, and only /dev/null (a container started without -i) means HTTP. --http still supplies the address HTTP binds, defaulting to "+defaultHTTPAddr),
		publicURL:          flag.String("public-url", "", "origin clients reach this deployment at, e.g. https://mcp.example.org/libgen. Its host is the one this server answers to in the Host header, which a reverse proxy forwards from the client; without it, or without --trusted-proxies naming the proxy, a proxied request carrying a public name is refused as a DNS-rebinding attempt"),
		trustedProxyHeader: flag.String("trusted-proxy-header", "", "header a trusted proxy fills with the address it heard the request from (e.g. X-Real-IP or X-Forwarded-For). Read only from a peer listed in --trusted-proxies, and required together with it; without both, every caller is told apart by the address the connection came from"),
		trustedProxies:     flag.String("trusted-proxies", "", "comma-separated addresses and CIDR ranges of the proxies whose --trusted-proxy-header is believed (e.g. 127.0.0.1/32). The literal "+unixPeerEntry+" trusts every peer of a unix-socket listener, and is refused on a TCP address"),
		rateLimitRPS:       flag.Float64("rate-limit-rps", defaultRateLimitRPS, "inbound requests per second allowed from one charged address, for the methods that reach a mirror or spend this process. 0 or less turns the limit off. On a listener whose every peer is this machine — a loopback bind or a unix socket — it is off unless --trusted-proxies names the proxy in front, because otherwise every caller is charged to one address; passing it explicitly there is refused rather than downgraded"),
		rateLimitBurst:     flag.Int("rate-limit-burst", defaultRateLimitBurst, "how many of those requests one charged address may make at once before the refill rate applies"),
		drainDelay:         flag.Duration("drain-delay", 0, "how long GET /health answers 503 draining before the listener is closed on shutdown. 0 (default) closes at once. Set it to at least one probe interval of whatever is in front, or the balancer learns this instance is going by the connection failing — after it has already sent work to it. Capped at "+maxDrainDelay.String()),
		maxInflight:        flag.Int("max-inflight-per-client", 0, "how many download or read calls one charged address may have in flight. Unset means the configured "+config.EnvName("MAX_CONCURRENT_DOWNLOADS")+", so the bound starts at the whole download semaphore; 0 or less turns the per-caller bound off. A ceiling is exactly as real as the identity underneath it, so behind a proxy it needs --trusted-proxy-header and --trusted-proxies too"),
		sessionTimeout:     flag.Duration("session-timeout", defaultSessionTimeout, "close a legacy stateful session that has gone this long without a request from its client. 0 never closes one, which leaves a client that crashed holding its session for the life of the process. Applies to --stateless=false only, and passing it under the default stateless transport is refused rather than ignored. Capped at "+maxSessionTimeout.String()),
		httpIdleTimeout:    flag.Duration("http-idle-timeout", defaultHTTPIdleTimeout, "close a kept-alive connection that has gone this long between requests. 0 (default) never closes one, which is what this server did before the flag existed. It bounds the gap between requests, never a response being written, so an SSE stream is not what it reclaims"),
	}
}

// parseServerFlags parses the process's command line. It reports false, with
// the status to exit with, when the process is not to go on: -h, which has
// printed the usage, or a command line the parser refused.
//
// The parser's own behavior is kept. It prints its complaint and the usage, and
// the statuses are the ones flag.ExitOnError would exit with — 0 for -h, 2 for
// anything else. What is added is the record a refused start owes: the flag set
// main installs is ContinueOnError precisely so the refusal comes back here to
// be logged instead of ending the process inside the parser.
func parseServerFlags() (code int, proceed bool) {
	err := flag.CommandLine.Parse(os.Args[1:])
	switch {
	case err == nil:
		return 0, true
	case errors.Is(err, flag.ErrHelp):
		return 0, false
	default:
		return refuseStart(exitUsage, err), false
	}
}

// refuseStart reports why this process will not start and returns code, the
// status to exit with.
//
// The reason is written at ERROR as a JSON record, and is the last record the
// process writes. Every refusal comes through here, because a refusal written
// any other way went out at INFO: once the JSON handler is installed the log
// package writes through it at that level, so the one line explaining a dead
// server was filed as routine and a filter for errors showed nothing. The
// message is the error's own text, so whatever an operator already searches
// for still matches.
//
// The JSON handler is installed first, at the level a start begins with,
// because a refusal can come before anything else has installed it — a flag
// the parser refuses is read before any configuration. Nothing has wrapped the
// handler at that point, so installing it again replaces nothing.
//
// A refusal from inside run is reported by exitCodeFor instead, at the same
// severity: by then the handler is the configured one and is left alone.
func refuseStart(code int, err error) int {
	logging.Setup(slog.LevelInfo)
	// The text can carry a typed argument, which is the point: it names what
	// was refused. It cannot forge a record, because the handler just installed
	// is the JSON one, which escapes every quote and control byte in a value.
	slog.Error(err.Error()) //nolint:gosec // G706: the JSON handler escapes the value, see above
	return code
}

// startPlan is a checked command line: everything run needs to serve.
type startPlan struct {
	spec     listenSpec
	opts     transport.Options
	decision transportDecision
}

// planStart reads the environment under the flags and checks every setting a
// start depends on, returning the first one it will not run with.
//
// Each check is a refusal rather than a warning, for reasons recorded beside
// the check itself: a deployment that is wrong about what it is serving has
// nothing to look at afterwards. All of them are made before anything is bound
// or served, and none of them reports itself — the caller does, through
// refuseStart, so a refusal added here later is reported like the rest.
//
// typed is the set of flags typed on the command line, asked before the
// environment overlay set others through the same flag set, and shadowed the
// variables a typed flag had already overwritten by then.
func planStart(f *serverFlags, typed map[string]bool, shadowed []shadowedVariable) (startPlan, error) {
	if err := readTheEnvironmentUnderTheFlags(shadowed); err != nil {
		return startPlan{}, err
	}
	// A negative cap disables the SDK limit outright, which must not be reachable
	// from a flag: this server is meant to face untrusted clients.
	if *f.maxBody < 0 {
		return startPlan{}, fmt.Errorf("--max-request-body-bytes must be >= 0, got %d", *f.maxBody)
	}
	// Refused before anything is served, for the same reason the origin list is:
	// a deployment that believes it is serving TLS, or that its socket is
	// group-only, and is wrong about it has nothing to look at afterwards.
	if err := validateTLSFiles(*f.tlsCert, *f.tlsKey); err != nil {
		return startPlan{}, err
	}
	// Resolved before anything reads the listener, because after --transport
	// exists the flag is no longer the answer: `--transport http` with no --http
	// binds an address nobody typed, and `--transport stdio` with one binds
	// nothing at all. Everything downstream — the socket mode, the listen
	// specification, the remote-download mode, the private-address hatch — takes
	// decision.Addr rather than the flag.
	decision, err := resolveTransport(*f.transportSelector, *f.httpAddr)
	if err != nil {
		return startPlan{}, err
	}
	// Named in run, once the configured handler is in place, never refused:
	// see http_only_flags.go.
	decision.Ignored = httpOnlyFlagsIgnored(decision.HTTP, flag.CommandLine, typed)
	mode, err := resolveSocketMode(decision.Addr, *f.socketMode)
	if err != nil {
		return startPlan{}, err
	}
	charge, proxies, err := planCharge(f, decision.Addr)
	if err != nil {
		return startPlan{}, err
	}
	// Refused rather than downgraded when the listener cannot tell two callers
	// apart: an operator who asked for a bound deserves to be told it cannot do
	// what they think it does.
	limit, err := resolveRateLimit(decision.Addr, charge, *f.rateLimitRPS, *f.rateLimitBurst, isFlagPassed("rate-limit-rps"))
	if err != nil {
		return startPlan{}, err
	}
	if decision.HTTP {
		log.Printf("inbound rate limit: %s", limit.describe())
	}
	opts, err := planTransportOptions(f)
	if err != nil {
		return startPlan{}, err
	}
	spec := listenSpec{
		addr:       decision.Addr,
		socketMode: mode,
		tlsCert:    *f.tlsCert,
		tlsKey:     *f.tlsKey,
		// Built from the RESOLVED address, for the reason the private-address
		// hatch reads it too: a `--transport http` deployment with no --http
		// binds an address nobody typed, and a guard built from the flag would
		// declare a host the listener never had.
		guard:  newHostGuard(decision.Addr, *f.publicURL, proxies),
		charge: charge,
		// Nil on stdio, which every layer that reads it treats as "no per-caller
		// state at all" rather than as an empty table.
		records:     newClientRecordsFor(decision.HTTP, limit, charge),
		inflight:    inflightFlag{value: *f.maxInflight, explicit: isFlagPassed("max-inflight-per-client")},
		drainDelay:  *f.drainDelay,
		idleTimeout: *f.httpIdleTimeout,
		publicURL:   strings.TrimSpace(*f.publicURL),
	}
	return startPlan{spec: spec, opts: opts, decision: decision}, nil
}

// planCharge checks how callers are told apart on the listener at addr: the
// trusted proxies and the header they fill, and the public URL the host guard
// answers to.
func planCharge(f *serverFlags, addr string) (chargePolicy, trustedProxies, error) {
	// Refused before anything is served, and for the same reason the origin list
	// and the socket mode are: an operator who believes a forwarded address is
	// being read, and whose callers are all charged to the proxy anyway, has
	// nothing to look at afterwards — and the opposite mistake hands every caller
	// the key their own traffic is counted under.
	proxyEntries := commaSeparated(*f.trustedProxies)
	if err := validateTrustedProxyConfig(proxyEntries, *f.trustedProxyHeader, addr); err != nil {
		return chargePolicy{}, trustedProxies{}, err
	}
	// Already validated above, so the error here is unreachable; parsing again
	// rather than threading the value out of the check keeps the check callable
	// on its own, which is what its own tests do.
	proxies, _ := parseTrustedProxies(proxyEntries)
	// A declaration nobody can act on is worse than none: the guard would keep
	// refusing the very name the operator wrote, and the refusal would keep
	// telling them to pass the flag they already passed.
	if err := validatePublicURL(*f.publicURL); err != nil {
		return chargePolicy{}, trustedProxies{}, err
	}
	charge := chargePolicy{header: strings.TrimSpace(*f.trustedProxyHeader), proxies: proxies}
	if len(proxyEntries) > 0 {
		// Said once at startup because it is the only place it can be seen: the
		// rule decides which address every later per-caller budget is keyed on,
		// and a list that names the wrong hop looks exactly like a correct one
		// from outside — every caller simply shares the proxy's key.
		log.Printf("--trusted-proxy-header %s is read from %s, and from no other peer; every other caller is told apart by the address it connects from",
			charge.header, strings.Join(proxyEntries, ", "))
	}
	return charge, proxies, nil
}

// planTransportOptions checks the settings of the streamable HTTP handler and
// of the listener's lifetime, and builds the handler's options from them.
func planTransportOptions(f *serverFlags) (transport.Options, error) {
	// Refused rather than clamped: past a few minutes a drain delay is not a
	// handover, it is a shutdown that appears to hang — and every supervisor
	// kills the process long before it elapses, so the operator would be waiting
	// for something that never happens.
	if *f.drainDelay < 0 || *f.drainDelay > maxDrainDelay {
		return transport.Options{}, fmt.Errorf("--drain-delay %s must be between 0 and %s", *f.drainDelay, maxDrainDelay)
	}
	// The same refusal the rate limit makes, for the same reason: an operator
	// who set an idle timeout on sessions this transport does not create has
	// configured nothing, and nothing is the one outcome a startup log cannot
	// distinguish from a working setting.
	if err := checkIdleTimeouts(idleTimeouts{
		session:         *f.sessionTimeout,
		sessionExplicit: isFlagPassed("session-timeout"),
		httpIdle:        *f.httpIdleTimeout,
		stateless:       *f.stateless,
	}); err != nil {
		return transport.Options{}, err
	}
	// Refused at startup rather than at the first request: a server mounted on a
	// path it cannot match would answer 404 to everything, which looks like a
	// proxy fault and is the hardest kind of misconfiguration to find.
	if err := validateBasePath(*f.httpPath); err != nil {
		return transport.Options{}, err
	}
	// Parsed before anything is served: a malformed origin fails startup rather
	// than being dropped, because an operator who believes an origin is trusted
	// and whose browser clients are refused anyway has nothing to look at.
	trusted, err := transport.ParseTrustedOrigins(*f.trustedOrigins)
	if err != nil {
		return transport.Options{}, err
	}
	if slices.Contains(trusted, transport.AnyOrigin) {
		log.Printf("--trusted-origins=%s: cross-origin protection is off, every browser origin is accepted", transport.AnyOrigin)
	}
	return transport.Options{
		Stateless:           *f.stateless,
		JSONResponse:        *f.jsonResponse,
		MaxRequestBodyBytes: *f.maxBody,
		TrustedOrigins:      trusted,
		BasePath:            normalizeBasePath(*f.httpPath),
		// Zero in stateless mode, which is what the SDK wants there anyway: the
		// refusal above has already stopped an operator who asked for something
		// else, so this is the mode's own answer rather than a value discarded.
		SessionTimeout: sessionTimeoutFor(*f.sessionTimeout, *f.stateless),
	}, nil
}
