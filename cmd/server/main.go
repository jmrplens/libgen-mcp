// Command libgen-mcp is an MCP server for searching and downloading from Library Genesis.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/cachehints"
	"github.com/jmrplens/libgen-mcp/internal/capguard"
	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/logging"
	"github.com/jmrplens/libgen-mcp/internal/mcpotel"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
	"github.com/jmrplens/libgen-mcp/internal/prompts"
	"github.com/jmrplens/libgen-mcp/internal/tools"
	"github.com/jmrplens/libgen-mcp/internal/toolutil"
	"github.com/jmrplens/libgen-mcp/internal/transport"
	buildversion "github.com/jmrplens/libgen-mcp/internal/version"
)

// httpShutdownTimeout bounds how long a graceful HTTP shutdown may take before
// in-flight connections are forcibly closed.
//
// The value is bracketed from both sides and neither side is generous. Below it
// sit the things a request may legitimately still be doing: a resolve inside
// LIBGEN_MCP_RESOLVE_BUDGET, a transfer inside LIBGEN_MCP_DOWNLOAD_STALL_TIMEOUT,
// a stream between two progress notifications. Above it sits the supervisor's
// own grace — ten seconds for `docker stop`, thirty for a Kubernetes pod — after
// which the process is killed and the budget is academic.
//
// Fifteen seconds outlasts the stall timeout and clears docker's grace only
// because [drainAndShutdown] clamps it to whatever the caller's own deadline
// leaves. Raising it without that clamp buys nothing: the supervisor is the real
// limit, and a budget longer than the grace is a promise the process cannot
// keep.
const httpShutdownTimeout = 15 * time.Second

// The listener's own timeouts. Both guard a peer that opens a connection and
// then does nothing with it, which costs a file descriptor either way.
//
// httpWriteTimeout is the one with a catch: it bounds the whole handler, not
// just the write, so it would sever a tool call that is doing exactly what it
// was asked to do. That is why it arrived with [sseAware], which clears it on
// the MCP endpoint; on every other route the response is a small body and a
// minute is already generous.
const (
	httpReadHeaderTimeout = 10 * time.Second
	httpWriteTimeout      = 60 * time.Second
)

// version and commit are injected at release time with
// -ldflags "-X main.version=<v> -X main.commit=<sha>".
//
// version is empty rather than a literal, and deliberately not initialized from
// libgenmcp.Version: -X sets a variable's initial value, but a variable with a
// runtime initializer is overwritten by package init afterwards, which would
// silently discard the tag goreleaser stamps. Empty means "nobody stamped one",
// and buildversion.Set leaves the number compiled in from VERSION in place.
var (
	version = ""
	commit  = "none"
)

func init() {
	commit = resolveCommit(commit, debug.ReadBuildInfo)
}

// resolveCommit fills in an unstamped commit from the module build info Go
// embeds in every binary. `go install github.com/jmrplens/libgen-mcp/cmd/server@version`
// carries no -ldflags, so without this it always reports "none" even though the
// VCS revision that produced the binary is right there in its build info. A
// release build's stamped value always wins.
//
// readBuildInfo is injected so tests can exercise the paths where build info is
// unavailable or carries no usable revision.
func resolveCommit(ldflagsCommit string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	if ldflagsCommit != "none" {
		return ldflagsCommit
	}
	info, ok := readBuildInfo()
	if !ok || info == nil {
		return ldflagsCommit
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			return setting.Value
		}
	}
	return ldflagsCommit
}

// Handshake display metadata. These mirror the title, description and website
// URL server.json and the marketplace manifests already advertise, so the live
// MCP handshake — the first thing any client or registry renders — states the
// same identity instead of the bare {name, version} it sent before.
const (
	implementationTitle       = "Books & Papers MCP Server"
	implementationDescription = "Federated search of books and papers, BibTeX/RIS citations, open-access retrieval and reading."
	// implementationWebsiteURL is the documentation site rather than the
	// repository or the hosted endpoint: a client rendering serverInfo shows
	// this to an end user, for whom the guides are more useful than a source
	// tree or an API base URL. mcp.jmrp.io/libgen is the real endpoint this
	// server answers as — that belongs in server.json's remotes, not here.
	implementationWebsiteURL = "https://jmrp.io/docs/libgen-mcp"
)

// The handshake's Instructions text, in the pieces serverInstructions assembles.
// It is the one place that tells a connecting model how the tools chain
// together, since each tool's own Description documents only itself. It goes
// straight into the model's system prompt, so it stays short and names only what
// a client cannot otherwise infer from the tool list —
// [TestServerInstructionsNameEveryToolAndPrompt] guards that every name below
// still exists on the registered surface, and the tests in serverfetch_test.go
// guard the reverse for a deployment that registers fewer of them.
const (
	instructionsOpening = "libgen-mcp searches, retrieves and reads books, papers, comics, magazines and standards" +
		" — no account or API key needed for any tool."
	// The opening for a deployment that does not fetch files: it still searches
	// and retrieves, but reading a file's text is the client's to do.
	instructionsOpeningNoFetch = "libgen-mcp searches and retrieves books, papers, comics, magazines and standards" +
		" — no account or API key needed for any tool."

	instructionsWorkflow = "WORKFLOW — the tools chain by identifier: search returns each record's md5 (books)" +
		" or doi (articles); carry that identifier into the next call."

	stepSearch  = "search — find candidate records across the catalog and, when needed, open-access sources."
	stepDetails = "get_details — full metadata and a ready-to-paste BibTeX/RIS citation for a record you already identified; it does not fetch the file. Use it whenever a citation is requested."
	// Two download steps, because the two deployments honor different contracts
	// and the numbered step is what a model follows. Telling it a link-only
	// server saves the file is the same defect the tool's own description was
	// rewritten to remove, one layer up.
	stepDownload         = "download — save the file by md5 (book), doi (article) or isbn (openly licensed book sources); resolve_only=true returns a link without saving."
	stepDownloadLinkOnly = "download — resolve a copy by md5 (book), doi (article) or isbn (openly licensed book sources); this deployment always returns a link to fetch yourself, never a saved file."
	stepRead             = "read — extract, paginate, search within (find) or outline a file's text by the same md5/doi (or a local path); it fetches the file itself, so it does not require calling download first."

	// Stated once, where a model that expected to read text will look: this
	// deployment has no read tool, and the way to a file's contents is the link.
	instructionsNoFetch = "THIS DEPLOYMENT DOES NOT FETCH FILES — there is no read tool here. download returns a" +
		" direct link; fetch it yourself to read the file's text."

	instructionsPrompts = "PROMPTS — acquire_book, research_topic, get_paper and download_troubleshoot wrap these" +
		" tools into ready-made, step-by-step workflows. Prefer one of them over calling the tools ad hoc when the" +
		" user's request matches its shape."
)

// serverInstructions renders the handshake Instructions for the surface this
// deployment actually registers. A server that may not fetch file bodies has no
// read tool, so the text neither numbers a step for it nor claims the server
// reads files — instructions naming a tool that is not there cost the model the
// same wasted turn that hiding the tool exists to save.
//
// linkOnly is the download tool's contract, which is not the same question:
// a remote deployment with fetching enabled still serves read and still only
// ever returns links. Not fetching implies link-only; the reverse does not hold.
func serverInstructions(serverFetch, linkOnly bool) string {
	download := stepDownload
	if linkOnly {
		download = stepDownloadLinkOnly
	}
	opening, steps := instructionsOpening, []string{stepSearch, stepDetails, download, stepRead}
	if !serverFetch {
		opening, steps = instructionsOpeningNoFetch, []string{stepSearch, stepDetails, download}
	}
	numbered := make([]string, 0, len(steps))
	for i, step := range steps {
		numbered = append(numbered, fmt.Sprintf("%d. %s", i+1, step))
	}
	workflow := instructionsWorkflow + "\n" + strings.Join(numbered, "\n")

	paragraphs := []string{opening, workflow}
	if !serverFetch {
		paragraphs = append(paragraphs, instructionsNoFetch)
	}
	return strings.Join(append(paragraphs, instructionsPrompts), "\n\n")
}

func main() {
	// Before anything else, and before any request can be made: the release
	// ldflags stamp this package's version, and internal/version is what builds
	// the User-Agent every outbound request carries.
	buildversion.Set(version)
	// Wrap the real logic so deferred cleanup (signal reset) runs before exit;
	// this avoids log.Fatal skipping defers on the error path.
	os.Exit(mainWithExit())
}

// mainWithExit parses flags, wires the signal context and runs the server,
// returning the process exit code.
func mainWithExit() int {
	httpAddr := flag.String("http", "", "serve streamable HTTP here instead of stdio: an address (e.g. :8080) or a unix socket path (e.g. /run/mcp.sock, recognized by the path separator; a bare name like mcp.sock is read as a host)")
	showVersion := flag.Bool("version", false, "print version and exit")
	healthcheck := flag.Bool("healthcheck", false, "probe the running instance's /health and exit 0 when it answers, 1 when it does not. The listener is read off that instance's own command line — its --http, --http-path, --tls-cert and --transport — so a socket, a moved port, a mount under a prefix and TLS this process terminates are all probed correctly. A target may be given instead: an http(s) URL, unix:<path>, or host:port. This is not cmd/probe, which checks the live mirrors")
	shutdown := flag.Bool("shutdown", false, "ask every other instance of this binary on this machine to exit, then kill what is left after "+shutdownGracePeriod.String()+". For an upgrade that swaps the binary while the old process still holds a download slot and a listener")
	stateless := flag.Bool("stateless", true, "stateless streamable HTTP (default; required for MCP protocol 2026-07-28): no Mcp-Session-Id, each POST self-contained, GET/DELETE return 405; use -stateless=false for legacy stateful sessions")
	jsonResponse := flag.Bool("json-response", false, "return application/json responses instead of text/event-stream (SSE)")
	maxBody := flag.Int64("max-request-body-bytes", 0, "maximum streamable HTTP request body size in bytes; 0 uses the SDK default (4 MiB)")
	socketMode := flag.String("http-socket-mode", "0660", "permission mode for a unix socket given to --http, as octal with or without a leading 0. Ignored for a TCP address, and refused on platforms without file modes")
	tlsCert := flag.String("tls-cert", "", "PEM certificate file; terminate TLS in this process instead of leaving it to a proxy in front. Requires --tls-key")
	tlsKey := flag.String("tls-key", "", "PEM private key file for --tls-cert")
	httpPath := flag.String("http-path", "/", "URL path the MCP endpoint answers on (e.g. /libgen). Every route — the endpoint, /health and the server card — is mounted under it, and any other path answers 404. Set it when a reverse proxy forwards its prefix instead of rewriting it away; leave it at / when the proxy strips the prefix or the server is reached directly")
	trustedOrigins := flag.String("trusted-origins", "", "comma-separated browser origins allowed to call this server cross-origin, as scheme://host[:port] (e.g. https://claude.ai). Empty (default) refuses every cross-origin browser request; \"*\" accepts any. Non-browser clients send no Origin and are unaffected either way")
	transportSelector := flag.String("transport", "", "which transport to serve: stdio, http, or auto. Empty (default) keeps the historical rule — a --http value means HTTP, no value means stdio. auto reads it off standard input: a pipe, terminal, file or socket means stdio, and only /dev/null (a container started without -i) means HTTP. --http still supplies the address HTTP binds, defaulting to "+defaultHTTPAddr)
	publicURL := flag.String("public-url", "", "origin clients reach this deployment at, e.g. https://mcp.example.org/libgen. Its host is the one this server answers to in the Host header, which a reverse proxy forwards from the client; without it, or without --trusted-proxies naming the proxy, a proxied request carrying a public name is refused as a DNS-rebinding attempt")
	trustedProxyHeader := flag.String("trusted-proxy-header", "", "header a trusted proxy fills with the address it heard the request from (e.g. X-Real-IP or X-Forwarded-For). Read only from a peer listed in --trusted-proxies, and required together with it; without both, every caller is told apart by the address the connection came from")
	trustedProxies := flag.String("trusted-proxies", "", "comma-separated addresses and CIDR ranges of the proxies whose --trusted-proxy-header is believed (e.g. 127.0.0.1/32). The literal "+unixPeerEntry+" trusts every peer of a unix-socket listener, and is refused on a TCP address")
	rateLimitRPS := flag.Float64("rate-limit-rps", defaultRateLimitRPS, "inbound requests per second allowed from one charged address, for the methods that reach a mirror or spend this process. 0 or less turns the limit off. On a listener whose every peer is this machine — a loopback bind or a unix socket — it is off unless --trusted-proxies names the proxy in front, because otherwise every caller is charged to one address; passing it explicitly there is refused rather than downgraded")
	rateLimitBurst := flag.Int("rate-limit-burst", defaultRateLimitBurst, "how many of those requests one charged address may make at once before the refill rate applies")
	drainDelay := flag.Duration("drain-delay", 0, "how long GET /health answers 503 draining before the listener is closed on shutdown. 0 (default) closes at once. Set it to at least one probe interval of whatever is in front, or the balancer learns this instance is going by the connection failing — after it has already sent work to it. Capped at "+maxDrainDelay.String())
	maxInflight := flag.Int("max-inflight-per-client", 0, "how many download or read calls one charged address may have in flight. Unset means the configured "+config.EnvName("MAX_CONCURRENT_DOWNLOADS")+", so the bound starts at the whole download semaphore; 0 or less turns the per-caller bound off. A ceiling is exactly as real as the identity underneath it, so behind a proxy it needs --trusted-proxy-header and --trusted-proxies too")
	registerEnvBackedFlags()
	flag.Parse()

	// Before anything reads configuration, and before the dotenv loader resolves
	// LIBGEN_MCP_ENV_FILE — which it does once per process, so a write after
	// that point would do nothing and say nothing.
	applyEnvBackedFlags()

	if code, handled := runUtility(utilityFlags{
		version:     *showVersion,
		healthcheck: *healthcheck,
		shutdown:    *shutdown,
		tlsCert:     *tlsCert,
	}); handled {
		return code
	}

	if envErr := readTheEnvironmentUnderTheFlags(); envErr != nil {
		log.Print(envErr)
		return 1
	}

	// A negative cap disables the SDK limit outright, which must not be reachable
	// from a flag: this server is meant to face untrusted clients.
	if *maxBody < 0 {
		log.Printf("--max-request-body-bytes must be >= 0, got %d", *maxBody)
		return 1
	}

	// Cancel the root context on the first SIGINT/SIGTERM so both transports can
	// shut down gracefully; a second signal restores the default behavior.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Both refused before anything is served, for the same reason the origin list
	// is: a deployment that believes it is serving TLS, or that its socket is
	// group-only, and is wrong about it has nothing to look at afterwards.
	if tlsErr := validateTLSFiles(*tlsCert, *tlsKey); tlsErr != nil {
		log.Print(tlsErr)
		return 1
	}
	// Resolved before anything reads the listener, because after --transport
	// exists the flag is no longer the answer: `--transport http` with no --http
	// binds an address nobody typed, and `--transport stdio` with one binds
	// nothing at all. Everything downstream — the socket mode, the listen
	// specification, the remote-download mode, the private-address hatch — takes
	// decision.Addr rather than the flag.
	decision, transportErr := resolveTransport(*transportSelector, *httpAddr)
	if transportErr != nil {
		log.Print(transportErr)
		return 1
	}
	mode, modeErr := resolveSocketMode(decision.Addr, *socketMode)
	if modeErr != nil {
		log.Print(modeErr)
		return 1
	}
	// Refused before anything is served, and for the same reason the origin list
	// and the socket mode are: an operator who believes a forwarded address is
	// being read, and whose callers are all charged to the proxy anyway, has
	// nothing to look at afterwards — and the opposite mistake hands every caller
	// the key their own traffic is counted under.
	proxyEntries := commaSeparated(*trustedProxies)
	if proxyErr := validateTrustedProxyConfig(proxyEntries, *trustedProxyHeader, decision.Addr); proxyErr != nil {
		log.Print(proxyErr)
		return 1
	}
	// Already validated above, so the error here is unreachable; parsing again
	// rather than threading the value out of the check keeps the check callable
	// on its own, which is what its own tests do.
	proxies, _ := parseTrustedProxies(proxyEntries)
	// A declaration nobody can act on is worse than none: the guard would keep
	// refusing the very name the operator wrote, and the refusal would keep
	// telling them to pass the flag they already passed.
	if urlErr := validatePublicURL(*publicURL); urlErr != nil {
		log.Print(urlErr)
		return 1
	}
	charge := chargePolicy{header: strings.TrimSpace(*trustedProxyHeader), proxies: proxies}
	if len(proxyEntries) > 0 {
		// Said once at startup because it is the only place it can be seen: the
		// rule decides which address every later per-caller budget is keyed on,
		// and a list that names the wrong hop looks exactly like a correct one
		// from outside — every caller simply shares the proxy's key.
		log.Printf("--trusted-proxy-header %s is read from %s, and from no other peer; every other caller is told apart by the address it connects from",
			charge.header, strings.Join(proxyEntries, ", "))
	}
	// Refused rather than downgraded when the listener cannot tell two callers
	// apart: an operator who asked for a bound deserves to be told it cannot do
	// what they think it does.
	limit, limitErr := resolveRateLimit(decision.Addr, charge, *rateLimitRPS, *rateLimitBurst, isFlagPassed("rate-limit-rps"))
	if limitErr != nil {
		log.Print(limitErr)
		return 1
	}
	if decision.HTTP {
		log.Printf("inbound rate limit: %s", limit.describe())
	}
	// Refused rather than clamped: past a few minutes a drain delay is not a
	// handover, it is a shutdown that appears to hang — and every supervisor
	// kills the process long before it elapses, so the operator would be waiting
	// for something that never happens.
	if *drainDelay < 0 || *drainDelay > maxDrainDelay {
		log.Printf("--drain-delay %s must be between 0 and %s", *drainDelay, maxDrainDelay)
		return 1
	}

	// Refused at startup rather than at the first request: a server mounted on a
	// path it cannot match would answer 404 to everything, which looks like a
	// proxy fault and is the hardest kind of misconfiguration to find.
	if pathErr := validateBasePath(*httpPath); pathErr != nil {
		log.Print(pathErr)
		return 1
	}

	// Parsed before anything is served: a malformed origin fails startup rather
	// than being dropped, because an operator who believes an origin is trusted
	// and whose browser clients are refused anyway has nothing to look at.
	trusted, originErr := transport.ParseTrustedOrigins(*trustedOrigins)
	if originErr != nil {
		log.Print(originErr)
		return 1
	}
	if slices.Contains(trusted, transport.AnyOrigin) {
		log.Printf("--trusted-origins=%s: cross-origin protection is off, every browser origin is accepted", transport.AnyOrigin)
	}

	opts := transport.Options{
		Stateless:           *stateless,
		JSONResponse:        *jsonResponse,
		MaxRequestBodyBytes: *maxBody,
		TrustedOrigins:      trusted,
		BasePath:            normalizeBasePath(*httpPath),
	}
	spec := listenSpec{
		addr:       decision.Addr,
		socketMode: mode,
		tlsCert:    *tlsCert,
		tlsKey:     *tlsKey,
		// Built from the RESOLVED address, for the reason the private-address
		// hatch reads it too: a `--transport http` deployment with no --http
		// binds an address nobody typed, and a guard built from the flag would
		// declare a host the listener never had.
		guard:  newHostGuard(decision.Addr, *publicURL, proxies),
		charge: charge,
		// Nil on stdio, which every layer that reads it treats as "no per-caller
		// state at all" rather than as an empty table.
		records:    newClientRecordsFor(decision.HTTP, limit, charge),
		inflight:   inflightFlag{value: *maxInflight, explicit: isFlagPassed("max-inflight-per-client")},
		drainDelay: *drainDelay,
		publicURL:  strings.TrimSpace(*publicURL),
	}
	if err := run(ctx, spec, opts, decision); err != nil && !isCleanShutdown(err) {
		log.Print(err)
		return 1
	}
	return 0
}

// utilityFlags are the invocations that are not a server: they say something
// about this build or about a process already running, and exit.
type utilityFlags struct {
	version     bool
	healthcheck bool
	shutdown    bool
	// tlsCert is this invocation's own --tls-cert, which --healthcheck uses as
	// the pin for an https target it was given outright.
	tlsCert string
}

// runUtility answers one of those invocations, reporting whether it was one.
//
// They are answered before anything reads configuration or binds anything:
// --version describes the binary, and the other two are diagnostics about a
// process that is already running. Neither has any use for this process's own
// settings, and --healthcheck in particular must not load a dotenv file to
// decide whether somebody else's listener answers.
func runUtility(f utilityFlags) (code int, handled bool) {
	switch {
	case f.version:
		fmt.Printf("libgen-mcp %s (commit %s)\n", buildversion.Current(), commit)
		return 0, true
	case f.healthcheck:
		return runHealthcheck(context.Background(), flag.Args(), f.tlsCert,
			healthcheckDeps{peers: livePeers, stdinIsNull: peerStdinIsNull, environ: peerEnviron}, os.Stderr), true
	case f.shutdown:
		return runShutdown(os.Stderr), true
	}
	return 0, false
}

// readTheEnvironmentUnderTheFlags loads the dotenv files and fills in every HTTP
// flag the operator did not pass.
//
// It runs in mainWithExit rather than inside run, because every setting below it
// is now backed by a variable and a home file has to be able to set them.
// Reading the files later would give this server two precedences: one for the
// settings config.Load reads, and another for the HTTP half.
//
// The JSON handler goes up first for the reason it does in run: loading
// announces where configuration came from, and those records would otherwise go
// through the standard library's text handler, putting plain lines onto a stream
// that is otherwise JSON. The announcement happens once per process, so run's
// own config.Load finds it already done.
func readTheEnvironmentUnderTheFlags() error {
	logging.Setup(slog.LevelInfo)
	config.LoadEnvFiles()

	overlaid, err := applyHTTPEnvOverlay()
	if err != nil {
		return err
	}
	if len(overlaid) > 0 {
		// At the starting level rather than the configured one, for the reason
		// the dotenv announcement is: this is the only local evidence that the
		// listener was configured by something other than the command line, and
		// the level is one of the settings such a source would like to set.
		slog.Info("HTTP settings taken from the environment", "variables", describeOverlay(overlaid))
	}
	return nil
}

// resolveSocketMode parses --http-socket-mode for the address actually given.
//
// A mode is meaningless for a TCP address and unenforceable on a platform whose
// chmod does not carry permission bits, so an explicitly set value is refused in
// both cases rather than accepted and quietly ignored — the operator asked for a
// guarantee this build cannot give.
func resolveSocketMode(httpAddr, value string) (os.FileMode, error) {
	mode, err := parseSocketMode(value)
	if err != nil {
		return 0, err
	}
	explicit := mode != defaultSocketMode
	if explicit && httpAddr != "" && !isUnixSocketAddr(httpAddr) {
		return 0, fmt.Errorf("--http-socket-mode %q applies to a unix socket, but --http %q is an address", value, httpAddr)
	}
	if explicit && !socketModesEnforced {
		return 0, fmt.Errorf("--http-socket-mode %q cannot be honored on %s, which has no file permission modes", value, runtime.GOOS)
	}
	return mode, nil
}

// isCleanShutdown reports whether err represents a normal shutdown of the MCP
// client: nil, or context.Canceled from the signal handler.
//
// It used to test errors.Is(err, io.EOF) as well, for a client closing stdin.
// That arm was measured to be unreachable: the SDK swallows the EOF and Run
// returns nil, so the first arm already covers it, and the one place an EOF
// could travel outward formats it with %v into an error errors.Is cannot see
// through. An arm that cannot fire is worse than no arm — it reads as a
// guarantee the code does not make. The guarantee itself is pinned where it can
// be observed, in test/e2e/stdio's shutdown cases.
func isCleanShutdown(err error) bool {
	return err == nil || errors.Is(err, context.Canceled)
}

// newMCPServer builds the bare MCP server with its receiving middleware in
// place; the caller registers the tools and prompts on top. instructions is the
// handshake text for the surface the caller is about to register, which is not
// the same on every deployment — see serverInstructions.
//
// records is the per-caller state this deployment keeps, or nil when it keeps
// none — stdio, and any listener where an address cannot tell two callers apart.
// Nil leaves the metering middlewares out entirely rather than installing ones
// that would allow everything.
func newMCPServer(instructions string, records *clientRecords, ceiling heavyCeiling, spans mcpotel.Options) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:        "libgen-mcp",
		Title:       implementationTitle,
		Description: implementationDescription,
		Version:     buildversion.Current(),
		WebsiteURL:  implementationWebsiteURL,
		Icons:       toolutil.IconBrand,
	}, &mcp.ServerOptions{
		Instructions: instructions,
		// Pinned rather than left nil, because nil is not neutral: the SDK
		// fills it with its own default of {"logging":{}}, and MCP logging
		// is Deprecated as of revision 2026-07-28 (SEP-2577), whose prescribed
		// migration is exactly what this server already does — slog to stderr.
		// So the server was advertising a deprecated capability it neither
		// implements nor wants, purely by omission.
		//
		// Tools and Prompts are pinned for the same reason one layer down. The
		// SDK leaves a non-nil capability alone but fills in a nil one with
		// ListChanged: true as soon as anything is registered, and that is a
		// promise this server cannot keep: the catalog is fixed at registration
		// and only changes with a release (the same fact the cachehints
		// middleware below is built on). A client that believes it opens a
		// subscriptions/listen stream, which the SDK then parks on a context
		// nothing here will ever cancel — no list-changed notification is ever
		// sent, and no KeepAlive is configured, so there is never even a failed
		// write to unwind it. Declaring false is the truth, and it stops the
		// stream being opened at all.
		//
		// If this server ever gains a list-changed notification — a hot reload
		// of LIBGEN_MCP_SOURCES, say — the truth changes and this flips back,
		// but a ceiling on concurrent listen streams has to land with it. Size
		// that ceiling for the legacy path, not the modern one: from 2026-07-28
		// the SDK ties the handler to its POST, so the cost is per open
		// connection, while an older request or one with no protocol header
		// leaks a goroutine and a session per request, for the life of the
		// process. The sibling project gitlab-mcp-server carries such a ceiling
		// in cmd/server/subscriptions.go, which is the shape to copy.
		Capabilities: &mcp.ServerCapabilities{
			Tools:   &mcp.ToolCapabilities{ListChanged: false},
			Prompts: &mcp.PromptCapabilities{ListChanged: false},
		},
	})
	// The per-caller trio goes on first, which makes it INNERMOST, and the order
	// within it is not interchangeable. Each layer reads the caller's record off
	// the context, so whatever resolves it has to run before — that is, wrap —
	// both of them; and the ceiling sits inside the limiter so a call refused at
	// the ceiling has already spent a token, which is what keeps a caller from
	// retrying into it for free. Nested, that is
	// meter(limiter(ceiling(handler))). Nothing is installed at all when this
	// deployment keeps no per-caller state.
	if records != nil {
		server.AddReceivingMiddleware(records.limitHeavyCalls(ceiling))
		toolutil.AttachRateLimit(server, limiterFrom)
		server.AddReceivingMiddleware(records.meter)
	}
	// Then the carrier, so a handler runs under the bound context. The layers
	// above it have nothing to cancel — cachehints annotates a result and
	// capguard refuses a method outright — and all of them still run under
	// recoverPanics, which is added last and wraps every one.
	server.AddReceivingMiddleware(mcpCarriers.bind)
	// The catalog is identical for every client and only changes with a release,
	// so tell clients how long they may hold on to it (SEP-2549).
	server.AddReceivingMiddleware(cachehints.Middleware())
	// Nothing here registers a resource, so the resource methods the SDK wires
	// up regardless must not answer as though something did (see
	// internal/capguard).
	server.AddReceivingMiddleware(capguard.NoResources())
	// The span goes on beneath the panic guard, so everything below it — the
	// resource refusal, the cache hints, the per-caller trio and the handler —
	// runs inside one measured request. Above it there is only recoverPanics,
	// which means a panicking call ends its span while unwinding and records no
	// duration: the measurement is of requests that finished, and a panic is
	// reported as the IsError result recoverPanics turns it into.
	server.AddReceivingMiddleware(mcpotel.Middleware(spans))
	// Last, and that is what puts it OUTERMOST — which is not obvious and is the
	// whole of why the order matters here. Each call wraps the handler built so
	// far, so the four nest recoverPanics(mcpotel(capguard(cachehints(handler))))
	// and a panic in any of the others is caught as well as one in a handler.
	// Moving this line up would quietly demote it to covering less.
	server.AddReceivingMiddleware(recoverPanics)
	return server
}

// The content-type header and the media type this server answers most routes
// with, named once because /health, the 404 and the legacy card location all
// write the same pair and a typo in any of them is a body a strict client
// refuses to parse rather than an error anything reports.
const (
	headerContentType = "Content-Type"
	mediaTypeJSON     = "application/json"
)

// The CORS response headers this server writes, named once because three
// separate handlers set the origin header — the card's, its preflight, and the
// MCP endpoint's — and a typo in any one of them is a header a browser silently
// ignores rather than an error anything reports.
const (
	headerAllowOrigin   = "Access-Control-Allow-Origin"
	headerAllowMethods  = "Access-Control-Allow-Methods"
	headerAllowHeaders  = "Access-Control-Allow-Headers"
	headerExposeHeaders = "Access-Control-Expose-Headers"
	headerMaxAge        = "Access-Control-Max-Age"
	headerRequestMethod = "Access-Control-Request-Method"
	headerRequestHeader = "Access-Control-Request-Headers"
)

// browserCORS answers a cross-origin browser request from a trusted origin so
// the browser will actually send it, and leaves every other request untouched.
//
// It exists because validating the Origin header and refusing every origin are
// not the same instruction, and shipping the second as the first made this
// endpoint unusable from a browser: nginx answered the preflight 204, the
// browser believed the request was allowed, and crossOriginProtected then
// refused the POST that followed. The endpoint advertised access it did not
// grant.
//
// The middleware widens nothing. An origin that is not trusted falls straight
// through to the protection, which refuses it exactly as before; all this does
// is make an already-granted trust decision usable by the client it was granted
// for. Three details are load-bearing:
//
//   - The origin is echoed rather than answered with "*", because a browser
//     rejects the wildcard on a credentialed request. Even AnyOrigin echoes
//     whatever asked.
//   - Vary: Origin, because the response now differs by origin and a shared
//     cache that missed that would serve one origin's answer to another.
//   - Access-Control-Expose-Headers names Mcp-Session-Id only, and only because
//     --stateless=false emits it: neither it nor Mcp-Protocol-Version is
//     CORS-safelisted, so a browser cannot read what is not exposed. The
//     default stateless transport emits neither, which is why the list is one
//     header rather than the pair a session-based server needs.
func browserCORS(trusted []string, next http.Handler) http.Handler {
	if len(trusted) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if !transport.Trusts(trusted, origin) {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set(headerAllowOrigin, origin)
		// One Vary header listing every request header the answer is derived
		// from, rather than several: Header.Get reads only the first line, so
		// two Vary headers are a value half of any reader will miss.
		h.Set("Vary", "Origin")
		if r.Method == http.MethodOptions && r.Header.Get(headerRequestMethod) != "" {
			h.Set(headerAllowMethods, "POST, GET, DELETE, OPTIONS")
			// Echo what was asked for rather than guessing: a client may send
			// Mcp-Protocol-Version, Mcp-Session-Id, Last-Event-ID or its own,
			// and a fixed list would refuse whichever one it forgot.
			if want := r.Header.Get(headerRequestHeader); want != "" {
				h.Set(headerAllowHeaders, want)
			}
			// The answer also echoes the requested headers, so it differs by
			// them too: without this a shared cache could replay one header
			// set's preflight for another and refuse a request this server
			// allows.
			h.Set("Vary", "Origin, "+headerRequestHeader)
			h.Set(headerMaxAge, "3600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.Set(headerExposeHeaders, "Mcp-Session-Id")
		next.ServeHTTP(w, r)
	})
}

// crossOriginProtected wraps the MCP handler in the standard library's
// cross-origin protection, which the 2026-07-28 streamable-HTTP transport
// requires: "Servers MUST validate the Origin header on all incoming
// connections to prevent DNS rebinding attacks", answering 403 when the header
// is present and invalid.
//
// An explicit allowlist is validation, not an exemption from it: --trusted-origins
// names the origins this deployment vouches for, and everything else is still
// refused. With no allowlist the behavior is unchanged from before the flag
// existed.
//
// The SDK does not do this for us. StreamableHTTPOptions.CrossOriginProtection
// is nil unless set — "If nil, no cross-origin protection is applied" — and the
// field is deprecated in favor of exactly this wrapping; its only default
// protection is a Host check that never fires on a public bind.
//
// Who this actually stops is narrow, and deliberately so. Safe methods are
// always allowed, so the card and the health probe are untouched. A request
// carrying neither Sec-Fetch-Site nor Origin is allowed too, which is every
// non-browser client — stdio hosts, desktop apps, curl, the SDK's own client.
// What remains is a state-changing POST issued by a browser from another
// origin, which is the attack the requirement names.
func crossOriginProtected(trusted []string, next http.Handler) http.Handler {
	if slices.Contains(trusted, transport.AnyOrigin) {
		// Every origin is trusted, so there is nothing left for the protection
		// to refuse — and it must be removed rather than left in place, since
		// browserCORS hands the request on to it rather than answering for it.
		// The operator asked for this explicitly and was warned at startup.
		return next
	}
	protection := http.NewCrossOriginProtection()
	// The standard library refuses with plain text, which on this route is the
	// body a Streamable HTTP client reads as a pre-negotiation server before
	// downgrading its transport. Every gate in front of the endpoint answers in
	// one shape; see refusal.go. The card routes are mounted outside this
	// wrapper and keep the library's own answer, since nothing there is a
	// JSON-RPC request to correlate with.
	protection.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refusal{
			status:  http.StatusForbidden,
			code:    codeForbidden,
			message: "cross-origin request refused: this deployment vouches for no browser origin, or not for this one. Name it with --trusted-origins.",
		}.write(w, r)
	}))
	for _, origin := range trusted {
		// The value was validated at startup, so an error here is unreachable;
		// ignoring it silently would still be the wrong shape, because a
		// deployment that believes an origin is trusted when it is not is the
		// failure this whole path exists to avoid.
		if err := protection.AddTrustedOrigin(origin); err != nil {
			panic(fmt.Sprintf("trusted origin %q passed validation but AddTrustedOrigin rejected it: %v", origin, err))
		}
	}
	return protection.Handler(next)
}

// securityHeaders sets the response headers this server is willing to state
// about itself on every route, including the ones the layers beneath answer
// themselves — a 403 from the cross-origin protection, a 204 preflight, the
// SDK's 405, the 404 below.
//
// It sits outermost and writes on the way in, which is what makes that work: an
// inner handler that never calls next has already had these headers put in the
// map, and an inner handler that wants a different value simply Sets its own
// over the top (the card's Cache-Control does exactly that). For the same reason
// every header here is Set, never Add — the card also sets nosniff, and Add
// would ship it twice.
//
// It deliberately does not touch Vary or any Access-Control-* header: three
// handlers already own those, and a second writer there is how a browser ends up
// rejecting a response that curl reports as fine.
//
// Strict-Transport-Security is emitted only when this process terminates TLS
// itself. Sent over plain HTTP it is either a claim the server cannot make or,
// on localhost, one that poisons the browser's HSTS cache for that host and port
// for a year — and a proxy in front that does terminate TLS is the one that
// knows its own certificate lifetime, so it sends its own.
func securityHeaders(servesTLS bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// Nothing this server returns is ever a page: no script, no style, no
		// frame, no form. default-src 'none' says so, and frame-ancestors is
		// the modern spelling of the X-Frame-Options above it.
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		// Tool results are per-request and a health probe is a point-in-time
		// reading; neither is safe for a shared cache to replay. The card
		// overrides this with its own lifetime.
		h.Set("Cache-Control", "no-store")
		if servesTLS {
			// One year, no preload directive: preloading is a decision about a
			// whole domain, which a single server behind it has no standing to
			// make on the operator's behalf.
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// normalizeBasePath reduces a --http-path value to the form the route patterns
// are built from: "" for the root, or "/prefix" with no trailing slash.
//
// It is lenient about how the value is written ("libgen", "/libgen", "/libgen/"
// all mean the same mount) and strict about what it produces, so every caller
// concatenates rather than deciding again whether a slash is needed.
func normalizeBasePath(p string) string {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return ""
	}
	return "/" + p
}

// validateBasePath rejects a --http-path this server cannot mount on. A path
// that is empty after normalization is the root, which is valid; anything
// carrying a query, a fragment, a traversal segment or an escape is not, because
// it would either never match a request or would match one the operator did not
// mean.
func validateBasePath(p string) error {
	if strings.ContainsAny(p, "?#") {
		return fmt.Errorf("--http-path %q must be a path, with no query or fragment", p)
	}
	base := normalizeBasePath(p)
	if base == "" {
		return nil
	}
	if base != path.Clean(base) {
		return fmt.Errorf("--http-path %q must be a clean absolute path (got %q after cleaning)", p, path.Clean(base))
	}
	if u, err := url.Parse(base); err != nil || u.Path != base {
		return fmt.Errorf("--http-path %q must not contain percent-escapes or a scheme", p)
	}
	return nil
}

// mcpAliasPath is the second spelling the MCP endpoint answers on, under
// whatever --http-path mounts. It is an alias, not the canonical path.
const mcpAliasPath = "/mcp"

// endpointPatterns lists the ServeMux patterns the MCP endpoint answers on for a
// normalized base path.
//
// At the root that is "/{$}" — the exact-match wildcard — and nothing else,
// which is the whole point of the change: mounting the MCP handler at "/" made
// it a catch-all that answered every path, so an unknown route reported 405
// ("wrong method for a route that exists") when the honest answer was 404.
//
// Under a prefix both "/prefix" and "/prefix/" are accepted, since a client
// given a base URL may or may not keep the trailing slash and neither spelling
// is a different endpoint.
//
// "/mcp" is mounted beside them as an alias, because enough clients and enough
// guides assume that path that a base URL pasted without it — or with it —
// should reach the same endpoint rather than a 404 that reads as "this server
// does not speak MCP". The canonical spelling stays the base path itself: every
// snippet in this repository uses it, and the server card advertises it.
//
// Both the bare and the trailing-slash form are mounted explicitly. Left to
// ServeMux, "/mcp/" would be a subtree pattern that swallows "/mcp/anything",
// and a registered "/mcp/" also makes the mux answer "/mcp" with a 301 to it —
// a redirect a POST does not follow with its body.
func endpointPatterns(base string) []string {
	patterns := []string{base + mcpAliasPath, base + mcpAliasPath + "/{$}"}
	if base == "" {
		return append(patterns, "/{$}")
	}
	return append(patterns, base, base+"/{$}")
}

// notFound answers a path this server does not serve, and names the one it does.
//
// The status is the point. Before this existed every unknown path reached the
// MCP handler and came back 405 with Allow: POST, which asserted two things that
// were not true — that the route exists, and that another method would work.
// Scanners believed both: the hosted deployment answered 405 to OAuth discovery
// probes for endpoints it does not implement, ninety-nine times a day.
func notFound(base string) http.HandlerFunc {
	endpoint := base
	if endpoint == "" {
		endpoint = "/"
	}
	body, err := json.Marshal(map[string]string{
		"error":        "not found",
		"mcp_endpoint": endpoint,
	})
	if err != nil {
		// Two constant strings cannot fail to marshal; degrade rather than
		// refuse to serve a 404 at all.
		body = []byte(`{"error":"not found"}`)
	}
	body = append(body, '\n')
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headerContentType, mediaTypeJSON)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write(body)
	}
}

// serverCardPreflight answers a CORS preflight for either card route.
//
// The card's audience is browser-based registries and scanners, and a browser
// discards a cross-origin response that carries no CORS header however public
// the document is — so without this the card is readable by curl and by nothing
// that would list this server. Allowing every origin gives away nothing: the
// card is served unauthenticated and is byte-identical for every caller, so
// there is no per-origin answer for a page to fish out.
func serverCardPreflight(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(headerAllowOrigin, "*")
	w.Header().Set(headerAllowMethods, "GET, OPTIONS")
	// A plain fetch of the card is a simple request and never preflights, so
	// this branch exists for the caller that adds a header of its own — a
	// scanner stamping a request id, say. Its request is refused unless the
	// preflight names that header back, and the browser sends the list to name:
	// echoing it allows exactly what was asked for and nothing else, which a
	// static list cannot do without guessing, and which "*" cannot do at all for
	// a caller that sends credentials.
	if want := r.Header.Get(headerRequestHeader); want != "" {
		w.Header().Set(headerAllowHeaders, want)
	}
	// Echoed, therefore varying by it. Not by Origin: this answer is the same
	// "*" for every caller.
	w.Header().Set("Vary", headerRequestHeader)
	// Without a lifetime the browser preflights again on every fetch, which for
	// a document that only changes with a release is two round-trips where one
	// would do.
	w.Header().Set(headerMaxAge, "3600")
	w.WriteHeader(http.StatusNoContent)
}

// serverCardGET serves one card document under its own media type.
//
// The two routes no longer close over the same bytes: /server-card answers the
// SEP-2127 card, which carries identity and how to connect, and the .well-known
// path answers the enumerating SEP-1649 document. They were the same document
// with two content types, which put the older shape at the location SEP-2127
// reserves; see discovery_card.go for why enriching the new one would be a
// contradiction rather than a kindness to a scanner.
func serverCardGET(cardJSON []byte, mediaType string) http.HandlerFunc {
	etag := entityTagFor(cardJSON)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerContentType, mediaType)
		w.Header().Set(headerAllowOrigin, "*")
		// The card is fetched by scanners that may hand the bytes to a browser;
		// nosniff keeps it read as the JSON it says it is. It is set by
		// securityHeaders too — repeated here so the route stays correct if it
		// is ever mounted somewhere else.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// The card only changes with a release, so a scanner may hold it. This
		// deliberately overrides the no-store securityHeaders sets for the rest
		// of the surface, and the validator is what makes the revalidation after
		// that hour cost a 304 rather than the whole document again.
		w.Header().Set("Cache-Control", "public, max-age=3600")
		serveCachedDocument(w, r, etag, cardJSON)
	}
}

// newHTTPHandler mounts every route this server answers under opts.BasePath,
// behind the security headers, and answers 404 for everything else.
//
// A nil card leaves the card routes unmounted, in which case those paths are
// simply not served — unlike before, when an unmounted card fell through to the
// MCP handler and answered 405.
func newHTTPHandler(mcpHandler http.Handler, cards serverCards, trusted []string, basePath string, servesTLS bool, health http.Handler) http.Handler {
	base := normalizeBasePath(basePath)
	mux := http.NewServeMux()
	mux.Handle("GET "+base+"/health", health)
	for _, c := range []struct {
		path      string
		mediaType string
		body      []byte
	}{
		{serverCardPath, mediaTypeJSON, cards.enumerating},
		{serverCardCurrentPath, serverCardMediaType, cards.discovery},
	} {
		// Each route is mounted only when its own document exists. The
		// enumerating one is built by listing the live server and can fail; the
		// discovery one is built from constants and flags and cannot. A path
		// left unmounted falls through to the 404, which is the honest answer —
		// unlike before, when it fell through to the MCP handler and answered
		// 405.
		if c.body == nil {
			continue
		}
		mux.HandleFunc("OPTIONS "+base+c.path, serverCardPreflight)
		mux.HandleFunc("GET "+base+c.path, serverCardGET(c.body, c.mediaType))
	}
	// CORS outermost so a preflight is answered before the protection sees it,
	// and the protection still guards the POST that follows. The card routes are
	// mounted separately and carry their own permissive CORS: the card is a
	// public document with no per-origin answer to fish out, whereas this
	// endpoint executes tool calls, so its trust is named rather than open.
	endpoint := browserCORS(trusted, crossOriginProtected(trusted, sseAware(mcpHandler)))
	for _, pattern := range endpointPatterns(base) {
		mux.Handle(pattern, endpoint)
	}
	// The 404 wears the CORS layer too. A browser shown a bare 404 on a
	// cross-origin request reports a CORS failure instead of the status, which
	// hides exactly the mistake — a mistyped path — that the 404 exists to name.
	mux.Handle("/", browserCORS(trusted, notFound(base)))
	// Outermost, and that is the whole placement: a guard that answers instead
	// of forwarding — a Host the deployment never declared, a cross-origin POST,
	// an unknown path — still gets a span, and a refusal is the traffic an
	// operator of a published endpoint most needs to see. The health route is
	// excluded by exact path: a balancer polls it forever, and a span per probe
	// would bury every real request.
	return mcpotel.ServerMiddleware(securityHeaders(servesTLS, mux), base+"/health")
}

// run serves the MCP server on the listener spec it is given.
//
// decision carries only how that spec was arrived at — the warning, the
// inference, and whether a person is at the other end of stdin. Which transport
// to serve is read from spec.addr and nowhere else, so there is one source of
// truth for it rather than two that can drift: mainWithExit resolves the
// transport first and builds the spec from the answer.
func run(ctx context.Context, spec listenSpec, opts transport.Options, decision transportDecision) error {
	// The JSON handler is installed before the configuration is read, and then
	// again at the level the configuration chose.
	//
	// config.Load reads the dotenv files, and that announces where configuration
	// came from. Those records would otherwise go through whatever handler
	// happened to be default — the standard library's text one — putting plain
	// lines onto a stream that is otherwise JSON records, on every start of a
	// deployment that uses a home file. The starting level is not the configured
	// one on purpose either: the announcement is the only local evidence that a
	// file outside the client's configuration was read, and the level is one of
	// the settings such a file would like to set.
	logging.Setup(slog.LevelInfo)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if vErr := cfg.Validate(); vErr != nil {
		return vErr
	}
	if hatchErr := refusePrivateHatchOnOpenListener(spec, cfg); hatchErr != nil {
		return hatchErr
	}
	// Install the global slog logger before serving so every log line goes to
	// stderr (stdout is reserved for the stdio MCP transport). The transport
	// decision is explained here rather than where it was taken, so it arrives
	// as a JSON record on a stream of JSON records.
	logging.Setup(cfg.LogLevel)
	decision.explain()

	// Before anything is served, so a span can cover the startup it is measuring,
	// and after the configured log level so the announcement is filtered the way
	// the operator asked. A collector that cannot be reached is logged and
	// swallowed: a server that can still reach the mirrors keeps doing so.
	// The shutdown context deliberately outlives the signal that triggered it:
	// Shutdown applies its own bound of a few seconds, and a flush canceled by
	// the same SIGTERM that started it would drop the batch describing the
	// shutdown.
	identity, stopTelemetry, err := startTelemetry(ctx, cfg)
	if err != nil {
		return err
	}
	defer stopTelemetry(context.WithoutCancel(ctx))

	// Before the catalog is registered and before either transport, so a profile
	// of startup itself can be taken. Refused rather than warned about when the
	// address is not loopback: a profile listener on a reachable interface hands
	// out copies of this process's memory.
	profiler, err := startPprofListener(ctx, pprofListenAddr())
	if err != nil {
		return err
	}
	defer profiler.stop()

	server, err := newRegisteredServer(cfg, spec.addr, spec.records, spec.inflight, identity)
	if err != nil {
		return err
	}

	if spec.addr != "" {
		// The digest is built here rather than at flag time because it
		// fingerprints the configuration, and the configuration is not read
		// until this point.
		return serveHTTP(ctx, server, spec, opts, httpPolicy{
			guard:      spec.guard,
			charge:     spec.charge,
			digest:     configDigest(cfg, opts.BasePath, opts.Stateless),
			drainDelay: spec.drainDelay,
			publicURL:  spec.publicURL,
		})
	}
	// Said before the first read, because after it the process looks idle and
	// that is exactly what the message is about.
	if decision.Interactive {
		writeTerminalGuidance(os.Stderr, buildversion.Current())
	}
	return serveStdio(ctx, server, cfg)
}

// serveStdio runs the MCP server over stdin and stdout, with input the SDK
// cannot read already answered.
//
// The filter sits in front rather than the bare StdioTransport because the SDK
// treats a message it cannot decode exactly like a closed pipe: the reader
// goroutine ends, the session ends, and on this transport that is the process.
// One malformed line from a buggy client took the server down with nothing
// written to say why (see cmd/server/stdio.go).
//
// MaxLineLength carries the same ceiling the filter enforces, so the SDK is the
// backstop rather than a second, different answer: a message the filter refuses
// never reaches it, and a message it accepts is one the SDK will buffer.
//
// Nothing here translates a closed pipe into a clean exit, because nothing has
// to: measured against SDK v1.8.0, Run returns nil when the client hangs up,
// both while idle and with a tool call in flight. That is not documented API, so
// it is pinned from outside instead — test/e2e/stdio's shutdown cases assert the
// exit status a supervisor reads, and would catch the day it changes.
func serveStdio(ctx context.Context, server *mcp.Server, cfg *config.Config) error {
	reader, writer := resilientStdio(os.Stdin, os.Stdout, cfg.StdioMaxLineBytes)
	fmt.Fprintf(os.Stderr, "libgen-mcp %s (commit %s) serving on stdio\n", buildversion.Current(), commit)
	return server.Run(ctx, &mcp.IOTransport{
		Reader:        reader,
		Writer:        writer,
		MaxLineLength: cfg.StdioMaxLineBytes,
	})
}

// refusePrivateHatchOnOpenListener rejects the private-address hatch on a
// listener somebody other than this machine can open a connection to.
//
// The hatch lets this server dial loopback, the operator's LAN and
// carrier-grade-NAT space. On a stdio server, and on one bound to loopback or a
// unix socket, whoever can ask for that is somebody with an account on this
// machine, which is what the hatch is for. On an open listener it is anyone who
// can POST to the endpoint: a tool call naming a URL is enough to have the
// server fetch an address the caller could not reach, and return it. That is a
// request-forgery proxy into whatever network the server sits in, which is the
// thing internal/netguard exists to prevent — turned on by configuration that
// says nothing about who may reach the listener.
//
// It is a startup refusal rather than a warning, because a warning is read once
// by whoever deployed it and never again, while the exposure lasts for the life
// of the process.
//
// It reads spec.addr — the address the server is about to bind — and never a
// flag value. **Any change that lets the listener be chosen somewhere else must
// resolve it before this point**: a transport default that serves HTTP with no
// --http given, or an address supplied through the environment, would otherwise
// arrive here looking like stdio and leave the hatch open on a wildcard bind,
// which is the exact case this refuses.
func refusePrivateHatchOnOpenListener(spec listenSpec, cfg *config.Config) error {
	if !cfg.AllowPrivateAddresses || spec.addr == "" || listenerIsHostLocal(spec.addr) {
		return nil
	}
	return fmt.Errorf(
		"%s is set and --http %s binds a listener other machines can reach, which would let any caller that can POST to this endpoint aim this server at addresses only this network can reach. "+
			"Bind a loopback address (--http 127.0.0.1:PORT) or a unix socket, or unset the variable: a mirror named in LIBGEN_MIRROR or %s is already exempt from the address guard without it",
		config.EnvName("ALLOW_PRIVATE_ADDRESSES"), spec.addr, config.EnvName("SCIHUB_HOSTS"),
	)
}

// newRegisteredServer builds the MCP server for cfg with every tool and
// prompt registered — the same construction run performs, pulled out so a
// test can inspect the live handshake (e.g. that serverInstructions still
// names every registered tool and prompt) without duplicating it.
func newRegisteredServer(cfg *config.Config, listenAddr string, records *clientRecords, inflight inflightFlag, identity identityChoice) (*mcp.Server, error) {
	// A deployment is remote when its disk is not the caller's: an HTTP listener
	// (a TCP address or a unix socket) or a hosted stdio process that says so with
	// LIBGEN_MCP_REMOTE_DOWNLOADS. That is also what decides, when the operator has
	// not, whether the server may pull a file's body over its own connection —
	// resolved here, before the client is built, because this is the only place
	// that knows the transport.
	//
	// listenAddr is the RESOLVED address, not the --http flag. The difference is
	// silent and it matters: a `--transport http` deployment with no --http would
	// otherwise read as local and start pulling file bodies over an egress IP
	// shared by everyone it serves, which is the thing the file-body decision
	// record exists to prevent.
	remote := listenAddr != "" || cfg.RemoteDownloads
	serverFetch := cfg.ResolveServerFetch(remote)

	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		return nil, err
	}
	client := libgen.New(mgr, cfg)
	// Either trigger puts download in link-only mode: a remote server cannot
	// write to the caller's disk, and a server that may not fetch has no bytes
	// to write. The handshake text states whichever contract results.
	// Resolved here rather than beside the flag, because its default is the
	// configured download concurrency and the configuration is not read until
	// after the flags are parsed.
	ceiling := resolveHeavyCeiling(inflight.value, inflight.explicit, cfg.MaxConcurrentDownloads)
	if records != nil {
		slog.Info("in-flight ceiling on download and read", "ceiling", ceiling.describe())
	}
	server := newMCPServer(serverInstructions(serverFetch, remote || !serverFetch), records, ceiling,
		spanOptions(listenAddr, identity))
	// When the server can't write to the client's disk, the download tool returns a
	// link to fetch instead of saving a file. That's the case in HTTP mode, and also
	// for a hosted stdio deployment (e.g. behind mcp-proxy) that opts in via
	// LIBGEN_MCP_REMOTE_DOWNLOADS, since its filesystem is unreachable/ephemeral too.
	var regOpts []tools.RegisterOption
	if remote {
		regOpts = append(regOpts, tools.WithRemoteDownloads())
	}
	// Fetching off drops read from the surface entirely and puts download in
	// link-only mode, whatever the transport.
	if !serverFetch {
		regOpts = append(regOpts, tools.WithoutServerFetch())
	}
	tools.Register(server, client, cfg, regOpts...)
	prompts.Register(server, client, cfg)
	return server, nil
}

// serveHTTP runs the streamable HTTP transport and shuts it down gracefully when
// ctx is canceled, tolerating the expected http.ErrServerClosed. Connections
// still streaming after httpShutdownTimeout are closed outright rather than
// waited on.
func serveHTTP(ctx context.Context, server *mcp.Server, spec listenSpec, opts transport.Options, policy httpPolicy) error {
	ln, err := listenHTTP(ctx, spec)
	if err != nil {
		return err
	}
	opts.ServesTLS = spec.servesTLS()
	return serveHTTPOn(ctx, server, ln, opts, policy)
}

// serveHTTPOn serves the MCP endpoint on a listener the caller has already
// bound, and closes it on return.
//
// The split exists for the tests. Reserving a port by binding it, reading its
// address and closing it before handing the address on leaves a window in which
// anything else on the machine can take that port — and with the package's HTTP
// tests running in parallel, the window is entered often enough to matter: a
// health probe can even reach a different test's server. Handing over a live
// listener removes the window by construction rather than narrowing it.
//
// Production still calls serveHTTP, which binds and delegates here, so the
// address a deployment configures is bound exactly as before.
func serveHTTPOn(ctx context.Context, server *mcp.Server, ln net.Listener, opts transport.Options, policy httpPolicy) error {
	guard, charge := policy.guard, policy.charge
	// One flag per listener rather than per process: a server that has begun
	// draining does not come back, but the next listener built in the same
	// process — a test, or a restart — starts out serving.
	var draining atomic.Bool
	// The Host check wraps the MCP endpoint and nothing else: /health is what a
	// balancer and a container runtime probe, and the server card is a public
	// document, so both answer whatever Host the prober sends. It is applied
	// here rather than inside newHTTPHandler because this is where the endpoint
	// handler is built, and because the SDK's own copy of the check — which
	// transport.StreamableHTTP turns off — sat in exactly this position.
	// The Host guard is outermost of the two: a request naming a host this
	// deployment does not serve is refused before anything else is done with
	// it, the body read for a JSON-RPC id included. The version guard sits
	// directly in front of the SDK because that is the answer it replaces.
	mcpHandler := hostGuarded(guard, protocolVersionGuarded(opts.Stateless,
		carriedMCPHandler(charge,
			mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, transport.StreamableHTTP(opts)),
		)))
	log.Printf("libgen-mcp %s (commit %s) listening on %s (streamable HTTP, stateless=%t, json-response=%t)",
		buildversion.Current(), commit, describeListener(ln, opts.ServesTLS), opts.Stateless, opts.JSONResponse)
	if !opts.Stateless {
		log.Print("stateless mode is off: legacy compatibility transport, clients negotiate MCP protocol 2025-11-25 or older")
	}
	// ReadHeaderTimeout guards against Slowloris; body/write timeouts stay
	// unset so long-lived streamable HTTP (SSE) sessions are not cut short.
	// Built once here rather than per request: it only changes with a release.
	// A failure is not fatal — the endpoint simply stays unmounted, because a
	// server that serves its tools is more useful than one that refuses to start
	// over a discovery document.
	var cards serverCards
	enumerating, cardErr := buildServerCard(ctx, server)
	if cardErr != nil {
		slog.Warn("server card unavailable; "+serverCardPath+" will not be served", "error", cardErr)
	}
	cards.enumerating = enumerating
	// The SEP-2127 card is built from constants and flags, so unlike the
	// enumerating one it cannot fail for a reason worth serving without — and a
	// failure here would mean a bug in this package rather than a catalog that
	// would not list.
	discovery, discoveryErr := buildDiscoveryCard(policy.publicURL, opts.Stateless)
	if discoveryErr != nil {
		slog.Warn("discovery card unavailable; "+serverCardCurrentPath+" will not be served", "error", discoveryErr)
	}
	cards.discovery = discovery

	srv := &http.Server{
		Handler: newHTTPHandler(mcpHandler, cards, opts.TrustedOrigins, opts.BasePath, opts.ServesTLS,
			healthHandler(policy.digest, &draining)),
		ReadHeaderTimeout: httpReadHeaderTimeout,
		// The slow-reader guard, and it is only safe because sseAware clears it
		// on the MCP endpoint. Everything else this server answers — /health,
		// both card paths, the 404 — is a small body with no business taking a
		// minute to write, so a client that stops reading one is holding a
		// connection for nothing. Before the writer existed there was no
		// deadline at all, which is why this lands with it rather than before.
		WriteTimeout: httpWriteTimeout,
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		return drainAndShutdown(ctx, srv, &draining, policy.drainDelay)
	}
}

// drainAndShutdown announces the drain, waits it out, and then ends the server
// inside whatever budget is left.
//
// The announcement comes first and the close comes last, which is the whole
// point of the delay: without it the close is what a balancer notices, one probe
// later, and every request it sent in that window failed.
func drainAndShutdown(ctx context.Context, srv *http.Server, draining *atomic.Bool, delay time.Duration) error {
	announceDraining(ctx, draining, delay)

	// ctx is already canceled here, so derive the shutdown deadline from a
	// cancellation-stripped copy of it (preserving its values) rather than from
	// the dead parent.
	budget := shutdownBudget(ctx)
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancel()

	err := srv.Shutdown(shutdownCtx)
	if err == nil {
		return nil
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// Shutdown waits for every connection to go idle, and a streamable HTTP
	// (SSE) stream never does on its own: a client attached when the signal
	// arrives holds the graceful phase open until the deadline. That is an
	// expected shutdown, not a failure — cut the remaining connections so the
	// listener is released instead of leaking it and reporting an error.
	log.Printf("graceful shutdown exceeded %s with streams still open; closing remaining connections", budget)
	return srv.Close()
}

// shutdownBudget is the smaller of this server's own budget and whatever the
// caller's deadline leaves.
//
// A caller with a deadline of its own has already been told how long it has —
// by a supervisor, or by a test — and spending longer than that on a graceful
// phase means the process is killed mid-drain instead of closing its listener.
// The clamp is what makes [httpShutdownTimeout] safe to raise: the budget is a
// ceiling, never a floor.
func shutdownBudget(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return httpShutdownTimeout
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		// The caller is already out of time. Something above zero, so Shutdown
		// gets one pass at the idle connections rather than none.
		return time.Millisecond
	}
	return min(remaining, httpShutdownTimeout)
}
