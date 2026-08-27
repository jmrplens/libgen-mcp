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
	"os"
	"os/signal"
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
	httpAddr := flag.String("http", "", "serve streamable HTTP on this address (e.g. :8080) instead of stdio")
	showVersion := flag.Bool("version", false, "print version and exit")
	stateless := flag.Bool("stateless", true, "stateless streamable HTTP (default; required for MCP protocol 2026-07-28): no Mcp-Session-Id, each POST self-contained, GET/DELETE return 405; use -stateless=false for legacy stateful sessions")
	jsonResponse := flag.Bool("json-response", false, "return application/json responses instead of text/event-stream (SSE)")
	maxBody := flag.Int64("max-request-body-bytes", 0, "maximum streamable HTTP request body size in bytes; 0 uses the SDK default (4 MiB)")
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
	}
	if err := run(ctx, *httpAddr, opts); err != nil && !isCleanShutdown(err) {
		log.Print(err)
		return 1
	}
	return 0
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
	w.Header().Set("Content-Type", "application/json")
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
		h.Add("Vary", "Origin")
		if r.Method == http.MethodOptions && r.Header.Get(headerRequestMethod) != "" {
			h.Set(headerAllowMethods, "POST, GET, DELETE, OPTIONS")
			// Echo what was asked for rather than guessing: a client may send
			// Mcp-Protocol-Version, Mcp-Session-Id, Last-Event-ID or its own,
			// and a fixed list would refuse whichever one it forgot.
			if want := r.Header.Get(headerRequestHeader); want != "" {
				h.Set(headerAllowHeaders, want)
			}
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

// newHTTPHandler mounts the MCP handler at / behind cross-origin protection and
// sseNoBuffering, exposes GET /health, and serves the server card when one was
// built. A nil card leaves both card routes unmounted, so the path falls
// through to the MCP handler exactly as it did before.
func newHTTPHandler(mcpHandler http.Handler, cardJSON []byte, trusted []string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler)
	if cardJSON != nil {
		// The card's audience is browser-based registries and scanners, and a
		// browser discards a cross-origin response that carries no CORS header
		// however public the document is — so without this the card is
		// readable by curl and by nothing that would list this server.
		// Allowing every origin gives away nothing: the card is served
		// unauthenticated and is byte-identical for every caller, so there is
		// no per-origin answer for a page to fish out.
		mux.HandleFunc("OPTIONS "+serverCardPath, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(headerAllowOrigin, "*")
			w.Header().Set(headerAllowMethods, "GET, OPTIONS")
			// A plain fetch of the card is a simple request and never
			// preflights, so this branch exists for the caller that adds a
			// header of its own — a scanner stamping a request id, say. Its
			// request is refused unless the preflight names that header back,
			// and the browser sends the list to name: echoing it allows
			// exactly what was asked for and nothing else, which a static list
			// cannot do without guessing, and which "*" cannot do at all for a
			// caller that sends credentials.
			if want := r.Header.Get(headerRequestHeader); want != "" {
				w.Header().Set(headerAllowHeaders, want)
			}
			// Without a lifetime the browser preflights again on every fetch,
			// which for a document that only changes with a release is two
			// round-trips where one would do.
			w.Header().Set(headerMaxAge, "3600")
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("GET "+serverCardPath, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set(headerAllowOrigin, "*")
			// The card is fetched by scanners that may hand the bytes to a
			// browser; nosniff keeps it read as the JSON it says it is. The
			// rest of the usual hardening set — CSP, X-Frame-Options — guards
			// against rendering and framing, neither of which happens to a
			// document that is never a page.
			w.Header().Set("X-Content-Type-Options", "nosniff")
			// The card only changes with a release, so a scanner may hold it.
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write(cardJSON)
		})
	}
	// CORS outermost so a preflight is answered before the protection sees it,
	// and the protection still guards the POST that follows. The card routes
	// are mounted separately and carry their own permissive CORS: the card is
	// a public document with no per-origin answer to fish out, whereas this
	// endpoint executes tool calls, so its trust is named rather than open.
	mux.Handle("/", browserCORS(trusted, crossOriginProtected(trusted, sseNoBuffering(mcpHandler))))
	return mux
}

func run(ctx context.Context, httpAddr string, opts transport.Options) error {
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
		return serveHTTP(ctx, server, httpAddr, opts)
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
func serveHTTP(ctx context.Context, server *mcp.Server, httpAddr string, opts transport.Options) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", httpAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", httpAddr, err)
	}
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
	httpAddr := ln.Addr().String()
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, transport.StreamableHTTP(opts))
	log.Printf("libgen-mcp %s (commit %s) listening on %s (streamable HTTP, stateless=%t, json-response=%t)",
		buildversion.Current(), commit, httpAddr, opts.Stateless, opts.JSONResponse)
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
		Handler:           newHTTPHandler(mcpHandler, cardJSON, opts.TrustedOrigins),
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
