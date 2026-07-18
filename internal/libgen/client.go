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
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

const (
	userAgent   = "libgen-mcp/0.1.0 (+https://github.com/jmrplens/libgen-mcp)"
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
	mirrors     MirrorLister
	http        *http.Client // pages: with timeout
	dl          *http.Client // streaming downloads: no global timeout, governed by ctx
	limiter     *rate.Limiter
	retry       int           // maximum number of passes over the mirrors
	backoffBase time.Duration // backoff base; injectable for tests
	// maxDownloadBytes is the download size cap in bytes (0 = no limit).
	maxDownloadBytes int64
	// dlSem is a counting semaphore bounding concurrent downloads: its capacity
	// is MaxConcurrentDownloads. Download acquires a slot before starting and
	// releases it on completion.
	dlSem chan struct{}
	// partialLocks serializes downloads that share the same partial file (the
	// same md5 into the same dir), keyed by the absolute .part path. The .part
	// path is deterministic, so without this two concurrent same-md5 downloads
	// would open/rehash/truncate/append the same file and corrupt it. Entries are
	// refcounted and removed once the last holder releases, so the map does not
	// grow unbounded over the lifetime of a long-running process.
	partialMu    sync.Mutex
	partialLocks map[string]*refLock

	mu       sync.Mutex           // protects cooldown
	cooldown map[string]time.Time // mirror base → instant at which the cooldown expires
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
		c.partialMu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(c.partialLocks, key)
		}
		c.partialMu.Unlock()
	}
}

// partialLockCount reports the number of live partial-lock entries. It exists for
// tests to assert that entries are released rather than leaked.
func (c *Client) partialLockCount() int {
	c.partialMu.Lock()
	defer c.partialMu.Unlock()
	return len(c.partialLocks)
}

// New builds a Client from the configuration: rate limiter (RateRPS/RateBurst),
// number of retries (RetryAttempts) and HTTP timeout.
func New(m MirrorLister, cfg *config.Config) *Client {
	// Size the download semaphore from config; guard against an unvalidated
	// non-positive value so the channel never becomes an unbuffered (deadlocking)
	// zero-capacity semaphore.
	maxConcurrent := max(cfg.MaxConcurrentDownloads, 1)
	return &Client{
		mirrors:          m,
		http:             &http.Client{Timeout: cfg.Timeout},
		dl:               &http.Client{},
		limiter:          rate.NewLimiter(rate.Limit(cfg.RateRPS), cfg.RateBurst),
		retry:            cfg.RetryAttempts,
		backoffBase:      defaultBackoffBase,
		maxDownloadBytes: cfg.MaxDownloadBytes,
		dlSem:            make(chan struct{}, maxConcurrent),
		cooldown:         make(map[string]time.Time),
	}
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
	req.Header.Set("User-Agent", userAgent)
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
