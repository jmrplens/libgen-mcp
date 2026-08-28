// Command libgen-mcp is an MCP server for searching and downloading from Library Genesis.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/cachehints"
	"github.com/jmrplens/libgen-mcp/internal/capguard"
	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
	"github.com/jmrplens/libgen-mcp/internal/logging"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
	"github.com/jmrplens/libgen-mcp/internal/prompts"
	"github.com/jmrplens/libgen-mcp/internal/tools"
	"github.com/jmrplens/libgen-mcp/internal/toolutil"
	"github.com/jmrplens/libgen-mcp/internal/transport"
	buildversion "github.com/jmrplens/libgen-mcp/internal/version"
)

// httpShutdownTimeout bounds how long a graceful HTTP shutdown may take before
// in-flight connections are forcibly closed.
const httpShutdownTimeout = 5 * time.Second

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

// serverInstructions is the handshake's Instructions text: the one place that
// tells a connecting model how the four tools chain together, since each
// tool's own Description documents only itself. It goes straight into the
// model's system prompt, so it stays short and names only what a client
// cannot otherwise infer from the tool list — [TestServerInstructionsNameEveryToolAndPrompt]
// guards that every name below still exists on the registered surface.
const serverInstructions = `libgen-mcp searches, retrieves and reads books, papers, comics, magazines and standards — no account or API key needed for any tool.

WORKFLOW — the tools chain by identifier: search returns each record's md5 (books) or doi (articles); carry that identifier into the next call.
1. search — find candidate records across the catalog and, when needed, open-access sources.
2. get_details — full metadata and a ready-to-paste BibTeX/RIS citation for a record you already identified; it does not fetch the file. Use it whenever a citation is requested.
3. download — save the file by md5 (book), doi (article) or isbn (openly licensed book sources); resolve_only=true returns a link without saving.
4. read — extract, paginate, search within (find) or outline a file's text by the same md5/doi (or a local path); it fetches the file itself, so it does not require calling download first.

PROMPTS — acquire_book, research_topic, get_paper and download_troubleshoot wrap these tools into ready-made, step-by-step workflows. Prefer one of them over calling the tools ad hoc when the user's request matches its shape.`

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
	stateless := flag.Bool("stateless", true, "stateless streamable HTTP (default; required for MCP protocol 2026-07-28): no Mcp-Session-Id, each POST self-contained, GET/DELETE return 405; use -stateless=false for legacy stateful sessions")
	jsonResponse := flag.Bool("json-response", false, "return application/json responses instead of text/event-stream (SSE)")
	maxBody := flag.Int64("max-request-body-bytes", 0, "maximum streamable HTTP request body size in bytes; 0 uses the SDK default (4 MiB)")
	socketMode := flag.String("http-socket-mode", "0660", "permission mode for a unix socket given to --http, as octal with or without a leading 0. Ignored for a TCP address, and refused on platforms without file modes")
	tlsCert := flag.String("tls-cert", "", "PEM certificate file; terminate TLS in this process instead of leaving it to a proxy in front. Requires --tls-key")
	tlsKey := flag.String("tls-key", "", "PEM private key file for --tls-cert")
	httpPath := flag.String("http-path", "/", "URL path the MCP endpoint answers on (e.g. /libgen). Every route — the endpoint, /health and the server card — is mounted under it, and any other path answers 404. Set it when a reverse proxy forwards its prefix instead of rewriting it away; leave it at / when the proxy strips the prefix or the server is reached directly")
	trustedOrigins := flag.String("trusted-origins", "", "comma-separated browser origins allowed to call this server cross-origin, as scheme://host[:port] (e.g. https://claude.ai). Empty (default) refuses every cross-origin browser request; \"*\" accepts any. Non-browser clients send no Origin and are unaffected either way")
	flag.Parse()

	if *showVersion {
		fmt.Printf("libgen-mcp %s (commit %s)\n", buildversion.Current(), commit)
		return 0
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
	mode, modeErr := resolveSocketMode(*httpAddr, *socketMode)
	if modeErr != nil {
		log.Print(modeErr)
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
	spec := listenSpec{addr: *httpAddr, socketMode: mode, tlsCert: *tlsCert, tlsKey: *tlsKey}
	if err := run(ctx, spec, opts); err != nil && !isCleanShutdown(err) {
		log.Print(err)
		return 1
	}
	return 0
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
// client: nil, io.EOF (stdin closed) or context.Canceled.
func isCleanShutdown(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled)
}

// newMCPServer builds the bare MCP server with its receiving middleware in
// place; the caller registers the tools and prompts on top.
func newMCPServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:        "libgen-mcp",
		Title:       implementationTitle,
		Description: implementationDescription,
		Version:     buildversion.Current(),
		WebsiteURL:  implementationWebsiteURL,
		Icons:       toolutil.IconBrand,
	}, &mcp.ServerOptions{
		Instructions: serverInstructions,
		// Pinned empty rather than left nil, because nil is not neutral: the
		// SDK fills it with its own default of {"logging":{}}, and MCP logging
		// is Deprecated as of revision 2026-07-28 (SEP-2577), whose prescribed
		// migration is exactly what this server already does — slog to stderr.
		// So the server was advertising a deprecated capability it neither
		// implements nor wants, purely by omission. The SDK still adds tools
		// and prompts on top of a non-nil value, so the advertised set becomes
		// exactly what this server serves.
		Capabilities: &mcp.ServerCapabilities{},
	})
	// The catalog is identical for every client and only changes with a release,
	// so tell clients how long they may hold on to it (SEP-2549).
	server.AddReceivingMiddleware(cachehints.Middleware())
	// Nothing here registers a resource, so the resource methods the SDK wires
	// up regardless must not answer as though something did (see
	// internal/capguard).
	server.AddReceivingMiddleware(capguard.NoResources())
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

// processStartTime marks when this process began serving.
//
// Package-level initialization runs before main, so this is the earliest
// instant the program can observe about itself. Tests do not override it:
// newHealthResponse takes both instants as parameters instead, so uptime is
// deterministic without a mutable package-level clock.
var processStartTime = time.Now()

// healthResponse is the JSON body returned by the /health endpoint. The field
// names match the sibling gitlab-mcp-server so one probe can read both servers.
//
// Liveness is reported two ways on purpose. StartedAt is the stable fact: it
// does not change between probes, so a monitor can cache it, deduplicate it,
// and detect a restart by noticing it moved — the same reason Prometheus
// exposes process_start_time_seconds rather than an uptime counter.
// UptimeSeconds is the derived convenience value, in the unit the IETF health
// check draft uses for it ("observedUnit": "s").
type healthResponse struct {
	// Status is the liveness verdict; this endpoint only ever reports "ok",
	// because a process that cannot answer at all is the failure signal.
	Status string `json:"status"`
	// Version is the release this build reports, stamped or compiled in.
	Version string `json:"version"`
	// Commit is the revision the release ldflags stamped, or "none".
	Commit string `json:"commit"`
	// StartedAt is the process start instant in RFC 3339, matching how this
	// project renders timestamps everywhere else.
	StartedAt string `json:"started_at"`
	// UptimeSeconds is whole seconds since StartedAt. Sub-second precision
	// would be noise on an endpoint polled at probe intervals.
	UptimeSeconds int64 `json:"uptime_seconds"`
}

// newHealthResponse builds the /health body for a start instant observed at
// now. Both instants are parameters so the uptime arithmetic can be tested
// without mutating a package-level clock from concurrent tests.
func newHealthResponse(startedAt, now time.Time) healthResponse {
	// Truncating instead of rounding keeps uptime from reporting a second that
	// has not fully elapsed. The clamp guards a caller that observes an instant
	// before the start; time.Now within one process cannot, because its
	// monotonic reading never goes backwards.
	uptime := int64(now.Sub(startedAt).Seconds())
	uptime = max(uptime, 0)
	return healthResponse{
		Status:        "ok",
		Version:       buildversion.Current(),
		Commit:        commit,
		StartedAt:     startedAt.UTC().Format(time.RFC3339),
		UptimeSeconds: uptime,
	}
}

// healthHandler responds with HTTP 200 and a JSON body for container healthchecks
// and load-balancer probes. It does not require authentication.
//
// Version comes from buildversion rather than the raw ldflags variable: that one
// is empty unless a release stamped it, whereas buildversion falls back to the
// number compiled in from VERSION, so a development build reports what it
// actually is instead of a placeholder.
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set(headerContentType, mediaTypeJSON)
	_ = json.NewEncoder(w).Encode(newHealthResponse(processStartTime, time.Now())) //nolint:errchkjson // healthcheck: client write errors are non-actionable
}

// sseNoBuffering sets X-Accel-Buffering: no on responses to requests that
// negotiate Server-Sent Events, which every streamable HTTP client does. The
// 2026-07-28 transport spec makes it a SHOULD: an nginx-class reverse proxy
// otherwise accumulates events in a buffer instead of forwarding them.
//
// It is not decoration here. The POST response stream is a real stream —
// download and read emit notifications/progress on it (see progressNotifier)
// while a multi-megabyte file is being fetched — so a buffering proxy would
// hold every progress event until the transfer finished, which is precisely
// the failure mode the header exists to prevent. The go-sdk sets Cache-Control
// and Content-Type on the SSE response but not this header.
//
// The header is written on the way in, before the SDK writes any of its own, so
// it is already in the map when the response headers are flushed. Harmless on
// the requests that negotiate down to application/json under --json-response: a
// proxy simply does not buffer a small JSON body either.
func sseNoBuffering(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.Header().Set("X-Accel-Buffering", "no")
		}
		next.ServeHTTP(w, r)
	})
}

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
func endpointPatterns(base string) []string {
	if base == "" {
		return []string{"/{$}"}
	}
	return []string{base, base + "/{$}"}
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

// serverCardGET serves the card bytes under the given media type. Both routes
// close over the same slice, so the two locations cannot answer differently.
func serverCardGET(cardJSON []byte, mediaType string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headerContentType, mediaType)
		w.Header().Set(headerAllowOrigin, "*")
		// The card is fetched by scanners that may hand the bytes to a browser;
		// nosniff keeps it read as the JSON it says it is. It is set by
		// securityHeaders too — repeated here so the route stays correct if it
		// is ever mounted somewhere else.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// The card only changes with a release, so a scanner may hold it. This
		// deliberately overrides the no-store securityHeaders sets for the rest
		// of the surface.
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(cardJSON)
	}
}

// newHTTPHandler mounts every route this server answers under opts.BasePath,
// behind the security headers, and answers 404 for everything else.
//
// A nil card leaves the card routes unmounted, in which case those paths are
// simply not served — unlike before, when an unmounted card fell through to the
// MCP handler and answered 405.
func newHTTPHandler(mcpHandler http.Handler, cardJSON []byte, trusted []string, basePath string, servesTLS bool) http.Handler {
	base := normalizeBasePath(basePath)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+base+"/health", healthHandler)
	if cardJSON != nil {
		for _, c := range []struct {
			path      string
			mediaType string
		}{
			{serverCardPath, mediaTypeJSON},
			{serverCardCurrentPath, serverCardMediaType},
		} {
			mux.HandleFunc("OPTIONS "+base+c.path, serverCardPreflight)
			mux.HandleFunc("GET "+base+c.path, serverCardGET(cardJSON, c.mediaType))
		}
	}
	// CORS outermost so a preflight is answered before the protection sees it,
	// and the protection still guards the POST that follows. The card routes are
	// mounted separately and carry their own permissive CORS: the card is a
	// public document with no per-origin answer to fish out, whereas this
	// endpoint executes tool calls, so its trust is named rather than open.
	endpoint := browserCORS(trusted, crossOriginProtected(trusted, sseNoBuffering(mcpHandler)))
	for _, pattern := range endpointPatterns(base) {
		mux.Handle(pattern, endpoint)
	}
	// The 404 wears the CORS layer too. A browser shown a bare 404 on a
	// cross-origin request reports a CORS failure instead of the status, which
	// hides exactly the mistake — a mistyped path — that the 404 exists to name.
	mux.Handle("/", browserCORS(trusted, notFound(base)))
	return securityHeaders(servesTLS, mux)
}

func run(ctx context.Context, spec listenSpec, opts transport.Options) error {
	httpAddr := spec.addr
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if vErr := cfg.Validate(); vErr != nil {
		return vErr
	}
	// Install the global slog logger before serving so every log line goes to
	// stderr (stdout is reserved for the stdio MCP transport).
	logging.Setup(cfg.LogLevel)

	server, err := newRegisteredServer(cfg, httpAddr)
	if err != nil {
		return err
	}

	if httpAddr != "" {
		return serveHTTP(ctx, server, spec, opts)
	}
	fmt.Fprintf(os.Stderr, "libgen-mcp %s (commit %s) serving on stdio\n", buildversion.Current(), commit)
	return server.Run(ctx, &mcp.StdioTransport{})
}

// newRegisteredServer builds the MCP server for cfg with every tool and
// prompt registered — the same construction run performs, pulled out so a
// test can inspect the live handshake (e.g. that serverInstructions still
// names every registered tool and prompt) without duplicating it.
func newRegisteredServer(cfg *config.Config, httpAddr string) (*mcp.Server, error) {
	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		return nil, err
	}
	client := libgen.New(mgr, cfg)
	server := newMCPServer()
	// When the server can't write to the client's disk, the download tool returns a
	// link to fetch instead of saving a file. That's the case in HTTP mode, and also
	// for a hosted stdio deployment (e.g. behind mcp-proxy) that opts in via
	// LIBGEN_MCP_REMOTE_DOWNLOADS, since its filesystem is unreachable/ephemeral too.
	var regOpts []tools.RegisterOption
	if httpAddr != "" || cfg.RemoteDownloads {
		regOpts = append(regOpts, tools.WithRemoteDownloads())
	}
	tools.Register(server, client, cfg, regOpts...)
	prompts.Register(server, client, cfg)
	return server, nil
}

// serveHTTP runs the streamable HTTP transport and shuts it down gracefully when
// ctx is canceled, tolerating the expected http.ErrServerClosed. Connections
// still streaming after httpShutdownTimeout are closed outright rather than
// waited on.
func serveHTTP(ctx context.Context, server *mcp.Server, spec listenSpec, opts transport.Options) error {
	ln, err := listenHTTP(ctx, spec)
	if err != nil {
		return err
	}
	opts.ServesTLS = spec.servesTLS()
	return serveHTTPOn(ctx, server, ln, opts)
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
func serveHTTPOn(ctx context.Context, server *mcp.Server, ln net.Listener, opts transport.Options) error {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, transport.StreamableHTTP(opts))
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
	cardJSON, cardErr := buildServerCard(ctx, server)
	if cardErr != nil {
		slog.Warn("server card unavailable; "+serverCardPath+" will not be served", "error", cardErr)
	}

	srv := &http.Server{
		Handler:           newHTTPHandler(mcpHandler, cardJSON, opts.TrustedOrigins, opts.BasePath, opts.ServesTLS),
		ReadHeaderTimeout: 10 * time.Second,
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
		// ctx is already canceled here, so derive the shutdown deadline from a
		// cancellation-stripped copy of ctx (preserving its values) rather than
		// the dead parent, keeping graceful shutdown bounded by httpShutdownTimeout.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), httpShutdownTimeout)
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
		log.Printf("graceful shutdown exceeded %s with streams still open; closing remaining connections", httpShutdownTimeout)
		return srv.Close()
	}
}
