// Package libgen implements the HTTP client for the libgen.li family of mirrors:
// search (HTML), details (json.php) and download (ads.php → get.php → CDN).
package libgen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
	"github.com/jmrplens/libgen-mcp/internal/netguard"
	"github.com/jmrplens/libgen-mcp/internal/version"
)

// userAgent is the User-Agent every request from this package carries. It is a
// function rather than a constant because the version it reports is stamped into
// the binary and handed to internal/version during startup, after this package's
// variables are already initialized.
func userAgent() string { return version.UserAgent() }

const (
	maxBodySize = 20 << 20 // 20 MiB for HTML/JSON pages (not downloads)

	// cooldownDuration is how long a mirror is set aside after failing.
	cooldownDuration = 45 * time.Second
	// defaultBackoffBase is the base of the backoff (grows per attempt) between retries.
	defaultBackoffBase = 200 * time.Millisecond
	// maxBackoff caps the duration of a single backoff wait.
	maxBackoff = 30 * time.Second
)

// ErrAllMirrorsFailed indicates that no mirror responded successfully because of
// a transient failure (network/timeout/5xx/429): a genuine connectivity problem.
var ErrAllMirrorsFailed = errors.New("all libgen mirrors unreachable (network block? try a VPN or different DNS)")

// ErrRequestRejected indicates that every mirror rejected the request with a
// permanent error (e.g. 404/403): not a connectivity problem, but a resource that
// does not exist or was rejected. It is distinguished from ErrAllMirrorsFailed so
// a normal "not found" is not surfaced as a network alarm.
var ErrRequestRejected = errors.New("request rejected by all mirrors")

// MirrorLister provides candidate base URLs, preferred first.
type MirrorLister interface {
	// Mirrors returns candidate base URLs, preferred first.
	Mirrors(ctx context.Context) []string
}

// Client talks to the libgen family of mirrors with failover, rate limiting,
// retries with growing backoff and a per-mirror cooldown after failures.
type Client struct {
	mirrors MirrorLister
	http    *http.Client // pages: with timeout
	dl      *http.Client // streaming downloads: no global timeout, governed by ctx
	limiter *rate.Limiter
	// enrichLimiter governs the keyless metadata-enrichment APIs (Crossref,
	// OpenLibrary). It is deliberately SEPARATE from limiter: the mirror limiter is
	// throttled to ~1 rps for the libgen family, whereas the public enrichment APIs
	// tolerate a higher rate, so enrichment must never be starved by (or starve) the
	// mirror budget.
	enrichLimiter *rate.Limiter
	// olLimiter governs the OpenLibrary enrichment hops specifically. OpenLibrary
	// asks callers to stay within 1 req/s unidentified or 3 req/s with a contact
	// email (openLibraryEnrichAnonRPS / openLibraryEnrichRPS), a tighter budget than
	// Crossref's, so it gets its own limiter rather than sharing enrichLimiter.
	olLimiter *rate.Limiter
	// enrichEmail is the contact address advertised to Crossref's polite pool via
	// the User-Agent mailto. It reuses cfg.UnpaywallEmail and may be empty.
	enrichEmail string
	retry       int           // maximum number of passes over the mirrors
	backoffBase time.Duration // backoff base; injectable for tests
	// maxDownloadBytes is the download size cap in bytes (0 = no limit).
	maxDownloadBytes int64
	// retryEverySource gives every source the full start-retry schedule rather than
	// only the last one that can serve an item. Its zero value is the restrained
	// behavior, so a client built without configuration gets it.
	retryEverySource bool
	// sourceAllowed reports whether the deployment permits a named source at all.
	// The per-call credential paths consult it before adding a source to a chain:
	// LIBGEN_MCP_SOURCES is the operator's decision, and a caller supplying a key
	// or an email must not be able to lift it.
	sourceAllowed func(string) bool
	// startRetryWaits is the staged wait schedule between attempts to get a
	// download to BEGIN (resolve + connect + first bytes). len(waits)+1 attempts
	// are made before a source is deemed unable to start; an empty schedule means a
	// single attempt with no start-retries. Injectable so tests use tiny waits.
	startRetryWaits []time.Duration
	// resolveBudget bounds how long ONE source may spend in Resolve before the
	// chain moves on. Resolution is serial and every source in front of the one
	// that can actually serve an item spends its own failure time first, so without
	// a per-source bound a DOI chain of seven sources — several of which make more
	// than one sequential request — can burn minutes before the last-resort source
	// is even tried. It is derived from cfg.Timeout, i.e. each source gets one
	// request's worth of time to resolve however many hops it needs; a
	// non-positive value disables the bound (a Client built directly by a test).
	resolveBudget time.Duration
	// stallTimeout is the progress-resetting stall window while streaming: a
	// transfer is aborted only when no bytes arrive within it, never for being
	// merely slow. A non-positive value disables the stall guard. Injectable so
	// tests use tiny windows.
	stallTimeout time.Duration
	// dlSem is a counting semaphore bounding concurrent downloads: its capacity
	// is MaxConcurrentDownloads. Download acquires a slot before starting and
	// releases it on completion.
	dlSem chan struct{}
	// unpaywallBase overrides the base URL of the on-demand Unpaywall source built
	// by withPerCallUnpaywall when an Item carries a per-call contact email. Empty
	// means the ad-hoc source uses the documented public API (unpaywallAPIBase); it
	// is a test seam so the prepended source can target an httptest server.
	unpaywallBase string
	// crossrefBase and openLibraryBase override the enrichment API roots. Empty
	// means the package defaults (crossrefBase/openLibraryBase vars) are used; they
	// are test seams so Enrich can target httptest servers.
	crossrefBaseOverride    string
	openLibraryBaseOverride string
	// annasMirrors is the Anna's Archive mirror lister shared by the sources that
	// fetch from that family, including any ad-hoc source built for a per-call key.
	annasMirrors MirrorLister
	// sources is the ordered download-source chain Download tries for each Item,
	// advancing to the next when one fails to resolve or stream. It is built from
	// config by buildSourceChain in config.KnownSources order, then filtered per
	// item by Supports so books try the md5 sources (libgen, randombook, annas) and
	// articles try the doi sources (unpaywall, openalex, europepmc, biorxiv, rfc, nist,
	// dagstuhl, acl, zenodo, scielo, fao, fatcat, core, crossref, oapen, scihub, scidb) — oapen
	// supports a
	// DOI as well as an ISBN, since monographs carry one.
	sources []DownloadSource
	// partialLocks serializes downloads that share the same partial file (the
	// same md5 into the same dir), keyed by the absolute .part path. The .part
	// path is deterministic, so without this two concurrent same-md5 downloads
	// would open/rehash/truncate/append the same file and corrupt it. Entries are
	// refcounted and removed once the last holder releases, so the map does not
	// grow unbounded over the lifetime of a long-running process.
	partialMu    sync.Mutex
	partialLocks map[string]*refLock

	// tempCache holds server-side temp files fetched by FetchToTemp so a paginated
	// read can fetch a file once and reuse it across page requests. It is bounded
	// by a total-size cap and a per-entry TTL and refcounts in-progress reads.
	tempCache *tempCache

	mu       sync.Mutex           // protects cooldown
	cooldown map[string]time.Time // mirror base → instant at which the cooldown expires

	// sourceMu guards sourceCooldown. It is deliberately separate from mu: that one
	// guards the per-mirror cooldown, whose keys are mirror base URLs, and conflating
	// the two key spaces would let a failing mirror influence which download sources
	// the chain is willing to try.
	sourceMu sync.Mutex
	// sourceCooldown records download sources set aside after proving unavailable:
	// source name → instant at which the cooldown expires. It lives on the Client
	// (which outlives every individual download) and is never persisted, so a fresh
	// process always starts by trying every source.
	sourceCooldown map[string]time.Time
	// sourceCooldownWindow overrides sourceCooldownDuration. A non-positive value
	// selects the constant; it is injectable so tests can observe an expiry without
	// waiting minutes for one.
	sourceCooldownWindow time.Duration
}

// refLock is a per-key serialization lock with a reference count. refs tracks how
// many callers currently hold or are waiting on the lock; the entry is deleted
// from the map when refs drops back to zero, so keys never accumulate.
type refLock struct {
	mu   sync.Mutex
	refs int
}

// acquirePartialLock serializes callers on key and returns a release closure. It
// increments the key's refcount under partialMu, acquires the per-key mutex, and
// returns a closure that releases the mutex and drops the refcount, deleting the
// entry when the last holder releases. Two callers with the same key run one
// after another; distinct keys never block each other and leave nothing behind.
func (c *Client) acquirePartialLock(key string) func() {
	c.partialMu.Lock()
	if c.partialLocks == nil {
		c.partialLocks = make(map[string]*refLock)
	}
	entry, ok := c.partialLocks[key]
	if !ok {
		entry = &refLock{}
		c.partialLocks[key] = entry
	}
	entry.refs++
	c.partialMu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		c.releasePartialRef(key, entry)
	}
}

// tryAcquirePartialLock is acquirePartialLock for a caller that has something
// better to do than wait: it returns the release closure and true when the key
// was free, or (nil, false) immediately when another caller holds it. The sweep
// uses it so a partial another download is actively streaming to is left alone
// rather than unlinked from under it.
func (c *Client) tryAcquirePartialLock(key string) (func(), bool) {
	c.partialMu.Lock()
	if c.partialLocks == nil {
		c.partialLocks = make(map[string]*refLock)
	}
	entry, ok := c.partialLocks[key]
	if !ok {
		entry = &refLock{}
		c.partialLocks[key] = entry
	}
	entry.refs++
	c.partialMu.Unlock()

	if !entry.mu.TryLock() {
		c.releasePartialRef(key, entry)
		return nil, false
	}
	return func() {
		entry.mu.Unlock()
		c.releasePartialRef(key, entry)
	}, true
}

// releasePartialRef drops one reference to key's lock entry, deleting the entry
// when the last holder lets go so the map never accumulates keys.
func (c *Client) releasePartialRef(key string, entry *refLock) {
	c.partialMu.Lock()
	entry.refs--
	if entry.refs == 0 {
		delete(c.partialLocks, key)
	}
	c.partialMu.Unlock()
}

// partialLockCount reports the number of live partial-lock entries. It exists for
// tests to assert that entries are released rather than leaked.
func (c *Client) partialLockCount() int {
	c.partialMu.Lock()
	defer c.partialMu.Unlock()
	return len(c.partialLocks)
}

// Option customizes a Client built by New. It exists so callers (chiefly tests
// that inject a DownloadSource pointing at an httptest server) can override
// pieces of the Client that are otherwise derived from config.
type Option func(*Client)

// WithSources overrides the download-source chain New would build from config.
// The supplied sources are used verbatim and in order; Download still filters
// them per item via Supports. It is primarily a test seam for injecting a source
// backed by a local server without reaching the live providers.
func WithSources(sources ...DownloadSource) Option {
	return func(c *Client) { c.sources = sources }
}

// WithUnpaywallBaseURL overrides the base URL of the on-demand Unpaywall source
// that withPerCallUnpaywall prepends when an Item supplies a per-call contact
// email. It exists so tests can point that ad-hoc source at an httptest server
// instead of the live Unpaywall API; production leaves it unset.
func WithUnpaywallBaseURL(base string) Option {
	return func(c *Client) { c.unpaywallBase = base }
}

// WithEnrichBaseURLs overrides the Crossref and OpenLibrary base URLs used by
// Enrich. It exists so tests (including callers in other packages) can point the
// keyless enrichment lookups at httptest servers; production leaves them unset and
// the package defaults apply.
func WithEnrichBaseURLs(crossref, openLibrary string) Option {
	return func(c *Client) {
		c.crossrefBaseOverride = crossref
		c.openLibraryBaseOverride = openLibrary
	}
}

// New builds a Client from the configuration: rate limiter (RateRPS/RateBurst),
// number of retries (RetryAttempts), HTTP timeout and the download-source chain.
// Options are applied last, so WithSources can replace the config-built chain.
func New(m MirrorLister, cfg *config.Config, opts ...Option) *Client {
	// Size the download semaphore from config; guard against an unvalidated
	// non-positive value so the channel never becomes an unbuffered (deadlocking)
	// zero-capacity semaphore.
	maxConcurrent := max(cfg.MaxConcurrentDownloads, 1)
	// OpenLibrary's etiquette grants a higher rate to identified callers; pick the
	// enrichment OL rate from whether a contact email is configured.
	olRPS := openLibraryEnrichAnonRPS
	if strings.TrimSpace(cfg.UnpaywallEmail) != "" {
		olRPS = openLibraryEnrichRPS
	}
	c := &Client{
		mirrors: m,
		// Both clients screen their destinations (internal/netguard): every source in
		// the chain fetches a URL some third party supplied, so the address a URL
		// resolves to is not this server's to trust. dl carries no timeout — a
		// streaming download's lifetime is its context's.
		http:             netguard.Client(cfg.Timeout, cfg.AllowPrivateAddresses),
		dl:               netguard.Client(0, cfg.AllowPrivateAddresses),
		limiter:          rate.NewLimiter(rate.Limit(cfg.RateRPS), cfg.RateBurst),
		enrichLimiter:    rate.NewLimiter(5, 5),
		olLimiter:        rate.NewLimiter(rate.Limit(olRPS), olRPS),
		enrichEmail:      cfg.UnpaywallEmail,
		retry:            cfg.RetryAttempts,
		backoffBase:      defaultBackoffBase,
		maxDownloadBytes: cfg.MaxDownloadBytes,
		startRetryWaits:  cfg.DownloadStartRetryWaits,
		resolveBudget:    cfg.Timeout,
		stallTimeout:     cfg.DownloadStallTimeout,
		dlSem:            make(chan struct{}, maxConcurrent),
		cooldown:         make(map[string]time.Time),
		sourceCooldown:   make(map[string]time.Time),
		tempCache:        newTempCache(cfg.ReadCacheBytes, cfg.ReadCacheTTL),
	}
	c.retryEverySource = cfg.RetryEverySource
	c.sourceAllowed = allowedByOperator(cfg.Sources)
	c.sources = c.buildSourceChain(cfg)
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// fixedMirrors is a MirrorLister over a hardcoded list, used as the offline
// fallback when a mirror Manager cannot be built.
type fixedMirrors []string

// Mirrors returns the fixed base URLs.
func (f fixedMirrors) Mirrors(context.Context) []string { return f }

// annasMirrorsFor builds the Anna's Archive mirror lister used by the sources
// that fetch from that family. It is a package-level variable so tests can
// substitute a lister that touches neither the network nor the on-disk cache.
//
// A Manager can only fail to build when the OS cache dir is unresolvable; in
// that case the family's hardcoded fallback list keeps the sources usable
// instead of dropping them from the chain.
var annasMirrorsFor = func(cfg *config.Config) MirrorLister {
	m, err := mirrors.NewManagerFor(mirrors.AnnasFamily, cfg)
	if err != nil {
		return fixedMirrors(mirrors.AnnasFamily.Fallback)
	}
	return m
}

// AnnasMirrorLister builds the Anna's Archive mirror lister, exported so the
// tools layer's discovery integration can share the same discovered mirrors
// without importing internal/mirrors or rebuilding the manager.
func AnnasMirrorLister(cfg *config.Config) MirrorLister {
	return annasMirrorsFor(cfg)
}

// allowedByOperator turns LIBGEN_MCP_SOURCES into a predicate. An empty list
// permits every source; otherwise only the listed names, compared the same way the
// configuration compares them.
func allowedByOperator(configured []string) func(string) bool {
	return func(name string) bool {
		if len(configured) == 0 {
			return true
		}
		return slices.ContainsFunc(configured, func(s string) bool {
			return strings.EqualFold(strings.TrimSpace(s), name)
		})
	}
}

// buildSourceChain assembles the ordered download-source chain from config in
// config.KnownSources order; because Download filters each source by
// Supports(item), this single ordered slice yields the right per-item order: an
// article (DOI-keyed) item is offered to the doi sources (unpaywall, openalex, europepmc,
// biorxiv, rfc, nist, dagstuhl, acl, zenodo, scielo, fao, fatcat, core, crossref, oapen,
// scihub, scidb),
// an ISBN-keyed book to the
// open-access book sources (oapen, archive) and an md5-keyed book to the shadow
// libraries (libgen, randombook, annas). Sources omitted from LIBGEN_MCP_SOURCES —
// or gated off, like core without a key — are left out. Each non-LibGen source uses
// the client's page HTTP client (with timeout) for its resolution lookups;
// libgenSource holds c so it can reuse the mirror failover in ResolveGetURL.
func (c *Client) buildSourceChain(cfg *config.Config) []DownloadSource {
	// Discovered once and shared by every Anna's-backed source, so one discovery
	// and one cache serve them all. Built unconditionally (not only when scidb or
	// annas are enabled) because withPerCallAnnas needs a non-nil lister for an
	// ad-hoc source even when neither named source is in the default chain.
	if c.annasMirrors == nil {
		c.annasMirrors = annasMirrorsFor(cfg)
	}
	annasLister := func() MirrorLister { return c.annasMirrors }
	factories := map[string]func() DownloadSource{
		"unpaywall": func() DownloadSource { return unpaywallSource{email: cfg.UnpaywallEmail, http: c.http} },
		"openalex":  func() DownloadSource { return openalexSource{http: c.http} },
		"europepmc": func() DownloadSource { return europePMCSource{http: c.http} },
		"biorxiv":   func() DownloadSource { return biorxivSource{http: c.http} },
		"rfc":       func() DownloadSource { return rfcSource{} },
		"nist":      func() DownloadSource { return nistSource{} },
		"dagstuhl":  func() DownloadSource { return dagstuhlSource{http: c.http} },
		"acl":       func() DownloadSource { return aclSource{} },
		"zenodo":    func() DownloadSource { return zenodoSource{http: c.http} },
		"scielo":    func() DownloadSource { return scieloSource{http: c.http} },
		"fao":       func() DownloadSource { return faoSource{http: c.http} },
		"fatcat":    func() DownloadSource { return fatcatSource{http: c.http} },
		"core":      func() DownloadSource { return coreSource{http: c.http, key: cfg.CoreKey} },
		"crossref": func() DownloadSource {
			return crossrefSource{http: c.http, limiter: c.enrichLimiter, email: c.enrichEmail}
		},
		"oapen":      func() DownloadSource { return oapenSource{http: c.http} },
		"archive":    func() DownloadSource { return archiveSource{http: c.http} },
		"scihub":     func() DownloadSource { return scihubSource{hosts: cfg.ScihubHosts, http: c.http} },
		"scidb":      func() DownloadSource { return scidbSource{mirrors: annasLister(), http: c.http} },
		"libgen":     func() DownloadSource { return libgenSource{c: c} },
		"randombook": func() DownloadSource { return randombookSource{http: c.http} },
		"annas": func() DownloadSource {
			return annasSource{mirrors: annasLister(), http: c.http, key: cfg.AnnasKey}
		},
	}
	chain := make([]DownloadSource, 0, len(config.KnownSources))
	for _, name := range config.KnownSources {
		if cfg.SourceEnabled(name) {
			chain = append(chain, factories[name]())
		}
	}
	return chain
}

// EnabledSourceNames returns the names of the enabled download sources in
// canonical chain order, split by the identifier each resolves: book sources
// (keyed by md5) and article sources (keyed by doi). It reflects
// LIBGEN_MCP_SOURCES and derives the split from each source's own Supports, so
// callers advertise only usable sources (e.g. in the download tool's schema)
// without duplicating the book/article categorization.
//
// A source is counted as an article source when it supports ANY of articleProbes,
// so the prefix-restricted ones are advertised alongside the prefix-agnostic ones.
func (c *Client) EnabledSourceNames() (book, article []string) {
	bookProbe := Item{MD5: "0"}
	for _, s := range c.sources {
		if s.Supports(bookProbe) {
			book = append(book, s.Name())
		}
		if supportsSomeArticle(s) {
			article = append(article, s.Name())
		}
	}
	return book, article
}

// articleProbes are the DOIs offered to Supports to find the sources that resolve
// an article. One probe is not enough: several sources claim only their own
// registrant prefix, and a probe carrying one of them makes every other
// prefix-restricted source look unusable. There is therefore one probe per
// restricted prefix in the chain — bioRxiv/medRxiv preprints, the RFC Editor,
// NIST, Schloss Dagstuhl, the ACL Anthology, Zenodo, SciELO Brazil and the FAO —
// and each is a valid DOI that the prefix-agnostic sources accept too, so the union
// is the full "can serve some article" set.
//
// Each probe has to satisfy its source's own well-formedness check, not merely
// carry the right prefix: acl declines a DOI without the "/v1/" segment, zenodo one
// whose suffix is not a record number and fao one whose suffix could not be a
// handle, so a bare-prefix probe would leave any of them absent from the schema
// exactly as having no probe at all would.
//
// A source added with a new prefix restriction must add its probe here, or it will
// resolve correctly but never be advertised in the download tool's schema.
var articleProbes = []Item{
	{DOI: biorxivDOIPrefix + "0"},
	{DOI: rfcDOIPrefix + rfcDOIToken + "1"},
	{DOI: nistDOIPrefix + "0"},
	{DOI: dagstuhlDOIPrefix + "0"},
	{DOI: aclDOIPrefixes[0] + aclVersionSegment + "P00-0000"},
	{DOI: zenodoDOIPrefix + "1"},
	{DOI: scieloDOIPrefix + "0"},
	{DOI: faoDOIPrefix + "aa0000en"},
}

// supportsSomeArticle reports whether the source resolves at least one of the
// probe DOIs, i.e. whether it can serve some article.
func supportsSomeArticle(s DownloadSource) bool {
	return slices.ContainsFunc(articleProbes, s.Supports)
}

// isbnProbe is the well-formed ISBN offered to Supports to find the sources that
// resolve a book by its publisher identifier. It must pass NormalizeISBN — the
// ISBN sources reject a malformed one — so it is a real thirteen-digit Bookland
// number rather than the "0" the md5 probe can get away with.
const isbnProbe = "9780000000002"

// EnabledISBNSources returns the names of the enabled download sources that resolve
// a book by ISBN, in canonical chain order. It is separate from EnabledSourceNames
// because ISBN is a third key alongside md5 and doi: the open-access book sources
// (OAPEN, the Internet Archive) hold books by publisher identifier and know nothing
// of LibGen digests, so advertising them under the md5 chain would invite the model
// to pin a source that cannot serve an md5.
func (c *Client) EnabledISBNSources() []string {
	probe := Item{ISBN: isbnProbe}
	var names []string
	for _, s := range c.sources {
		if s.Supports(probe) {
			names = append(names, s.Name())
		}
	}
	return names
}

// get tries path?q across the mirrors until it gets a 200. On a transient
// failure (timeout, network error, status 5xx/429) it puts the mirror in cooldown
// and retries with growing backoff. On a permanent error (e.g. 404/403) it does
// not retry that mirror or apply backoff, but fails over to the next candidate
// mirror within the same pass. Only if no mirror returns a 200 does it return
// ErrAllMirrorsFailed chaining the per-mirror errors. It returns the body and the
// base URL that responded.
func (c *Client) get(ctx context.Context, path string, q url.Values) (content []byte, baseURL string, resErr error) {
	mirrorList := c.mirrors.Mirrors(ctx)
	var errs []error
	permFailed := make(map[string]bool) // mirrors with a permanent error: do not retry
	attempts := max(c.retry, 1)
	sawTransient := false // was there any transient (connectivity) failure across the whole get?
	for attempt := range attempts {
		if attempt > 0 {
			if err := c.sleepBackoff(ctx, attempt); err != nil {
				return nil, "", err
			}
		}
		body, base, done, retriable, err := c.sweep(ctx, mirrorList, path, q, &errs, permFailed)
		if done {
			return body, base, err
		}
		sawTransient = sawTransient || retriable
		if !retriable {
			break // no pending transient failure: retrying would not help
		}
	}
	joined := errors.Join(errs...)
	if sawTransient {
		// At least one transient failure: genuine connectivity trouble.
		slog.Error("all mirror attempts exhausted", "path", path, "attempts", attempts)
		return nil, "", fmt.Errorf("%w: %w", ErrAllMirrorsFailed, joined)
	}
	// Every candidate error was permanent (e.g. 404/403): a normal rejection, not
	// a connectivity problem. Surface it as such and log at a lower severity.
	slog.Warn("all mirrors rejected the request", "path", path, "attempts", attempts)
	return nil, "", fmt.Errorf("%w: %w", ErrRequestRejected, joined)
}

// sweep makes one pass over the candidate mirrors, failing over to the next on
// any failure. It returns done=true only to stop entirely: success (err=nil) or a
// hard ctx/limiter error (err!=nil). Per-request errors do not stop the pass: a
// transient failure puts the mirror in cooldown and sets retriable=true; a
// permanent error removes the mirror from future passes via permFailed (no
// cooldown or backoff). retriable reports whether another pass is worthwhile
// (there was at least one recoverable transient failure).
func (c *Client) sweep(ctx context.Context, mirrorList []string, path string, q url.Values, errs *[]error, permFailed map[string]bool) (body []byte, base string, done, retriable bool, err error) {
	for _, m := range c.candidates(mirrorList, permFailed) {
		if werr := c.limiter.Wait(ctx); werr != nil {
			return nil, "", true, false, werr
		}
		slog.Debug("mirror attempt", "mirror", m, "path", path)
		b, transient, reqErr := c.doRequest(ctx, m, path, q)
		if reqErr == nil {
			return b, m, true, false, nil
		}
		*errs = append(*errs, reqErr)
		if transient {
			retriable = true
			c.markCooldown(m)
			slog.Warn("mirror failed transiently, trying next", "mirror", m, "error", reqErr)
			continue
		}
		permFailed[m] = true
		slog.Warn("mirror permanent error, failing over", "mirror", m, "error", reqErr)
	}
	return nil, "", false, retriable, nil
}

// doRequest executes a request against a mirror and classifies the result. It
// returns transient=true for network/timeout errors and status 5xx/429; 4xx other
// than 429 are treated as permanent. A readable 200 returns the body.
func (c *Client) doRequest(ctx context.Context, base, path string, q url.Values) (body []byte, transient bool, err error) {
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", base, err)
	}
	req.Header.Set("User-Agent", userAgent())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, true, fmt.Errorf("%s: %w", base, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if resp.StatusCode == http.StatusOK {
		if readErr != nil {
			return nil, true, fmt.Errorf("%s: %w", base, readErr)
		}
		return data, false, nil
	}
	transient = resp.StatusCode >= http.StatusInternalServerError || resp.StatusCode == http.StatusTooManyRequests
	return nil, transient, fmt.Errorf("%s: status %d", base, resp.StatusCode)
}

// candidates returns the eligible mirrors that are out of cooldown in order of
// preference, excluding those that already failed permanently (permFailed). If
// every eligible mirror is in cooldown, it returns the full eligible list (better
// to try than nothing), but never reintroduces the permanent ones.
func (c *Client) candidates(mirrorList []string, permFailed map[string]bool) []string {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	allowed := make([]string, 0, len(mirrorList))
	avail := make([]string, 0, len(mirrorList))
	for _, m := range mirrorList {
		if permFailed[m] {
			continue
		}
		allowed = append(allowed, m)
		if until, ok := c.cooldown[m]; !ok || now.After(until) {
			avail = append(avail, m)
		}
	}
	if len(avail) == 0 {
		return allowed
	}
	return avail
}

// markCooldown sets a mirror aside for cooldownDuration after a transient failure.
func (c *Client) markCooldown(base string) {
	c.mu.Lock()
	c.cooldown[base] = time.Now().Add(cooldownDuration)
	c.mu.Unlock()
}

// sleepBackoff waits a growing backoff with jitter before the next attempt,
// honoring context cancellation.
func (c *Client) sleepBackoff(ctx context.Context, attempt int) error {
	base := min(c.backoffBase<<(attempt-1), maxBackoff) // cap a single backoff wait
	//nolint:gosec // G404: backoff jitter, not security-sensitive.
	jitter := time.Duration(mrand.Int64N(int64(c.backoffBase) + 1))
	timer := time.NewTimer(base + jitter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
