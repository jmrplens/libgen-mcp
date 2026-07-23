// Package config loads the server configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/logging"
)

// maxDownloadBytesLimit is the allowed ceiling for MaxDownloadBytes (50 GiB).
const maxDownloadBytesLimit int64 = 50 * 1024 * 1024 * 1024

// maxTimeout is the allowed ceiling for Timeout.
const maxTimeout = 10 * time.Minute

// maxStallTimeout is the allowed ceiling for DownloadStallTimeout.
const maxStallTimeout = time.Hour

// maxStartRetryWait is the allowed ceiling for a single start-retry wait, and
// maxStartRetries caps how many waits the schedule may list.
const (
	maxStartRetryWait = 10 * time.Minute
	maxStartRetries   = 20
)

// Config groups the server configuration read from the environment.
type Config struct {
	Mirror                 string        // LIBGEN_MIRROR: forced mirror, e.g. https://libgen.li
	DownloadDir            string        // LIBGEN_MCP_DOWNLOAD_DIR: download destination
	Timeout                time.Duration // LIBGEN_MCP_TIMEOUT: timeout per HTTP request
	LogLevel               slog.Level    // LIBGEN_MCP_LOG_LEVEL: log level (debug/info/warn/error)
	RateRPS                float64       // LIBGEN_MCP_RATE_RPS: allowed requests per second
	RateBurst              int           // LIBGEN_MCP_RATE_BURST: maximum limiter burst
	MaxDownloadBytes       int64         // LIBGEN_MCP_MAX_DOWNLOAD_BYTES: maximum download size in bytes (0 = no limit)
	MaxConcurrentDownloads int           // LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS: simultaneous downloads
	RetryAttempts          int           // LIBGEN_MCP_RETRY_ATTEMPTS: retries per request
	UnpaywallEmail         string        // LIBGEN_MCP_UNPAYWALL_EMAIL: contact email required by the Unpaywall API
	ScihubHosts            []string      // LIBGEN_MCP_SCIHUB_HOSTS: ordered Sci-Hub mirror hosts (comma-separated, bare host, no scheme)
	Sources                []string      // LIBGEN_MCP_SOURCES: enabled download sources (comma-separated names; empty = all enabled)
	// RemoteDownloads forces the download tool to always return a direct link (a
	// resource_link + resolved object) instead of saving a file, regardless of
	// transport. LIBGEN_MCP_REMOTE_DOWNLOADS, a bool. HTTP (`--http`) implies it;
	// set it for a hosted stdio deployment (e.g. behind mcp-proxy) whose disk the
	// client cannot reach, so downloads are delivered as links the client fetches.
	RemoteDownloads bool
	// DownloadStartRetryWaits is the staged wait schedule between attempts to get a
	// download to BEGIN (resolve + connect + first bytes). LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS,
	// a comma-separated list of Go durations. len(waits) waits means len(waits)+1
	// attempts. Default: 5s,5s,5s,10s,10s,10s,15s (8 attempts over ~60s).
	DownloadStartRetryWaits []time.Duration
	// DownloadStallTimeout is the progress-resetting stall window while streaming: a
	// download is aborted only when NO bytes arrive for this long, never merely for
	// being slow. LIBGEN_MCP_DOWNLOAD_STALL_TIMEOUT, a Go duration. Default: 60s.
	DownloadStallTimeout time.Duration
	// ReadMaxChars is the max characters the read tool returns per call by
	// default, used when a call omits max_chars. LIBGEN_MCP_READ_MAX_CHARS.
	ReadMaxChars int
	// ReadDefaultPages is the default max PDF pages per read call, used when a
	// call omits max_pages. LIBGEN_MCP_READ_DEFAULT_PAGES.
	ReadDefaultPages int
	// ReadCacheBytes is the total-size cap of the FetchToTemp temp cache:
	// downloaded read files past this aggregate size are evicted (least-recently
	// used first, never while a read holds a reference). LIBGEN_MCP_READ_CACHE_BYTES.
	ReadCacheBytes int64
	// ReadCacheTTL is how long an unreferenced FetchToTemp temp file lingers
	// before eviction, so successive pages of one read reuse a single fetch while
	// idle files are reclaimed. LIBGEN_MCP_READ_CACHE_TTL.
	ReadCacheTTL time.Duration
	// EnrichEnabled is the deployment kill-switch for get_details' opt-in metadata
	// enrichment (Crossref/OpenLibrary). LIBGEN_MCP_ENRICH, default true: a
	// deployment sets it false to forbid enrichment entirely, regardless of the
	// per-call enrich flag.
	EnrichEnabled bool
	// OpenAccessEnabled is the deployment default for the search tool's open-access
	// discovery (arXiv/Crossref/OpenLibrary). LIBGEN_MCP_OPEN_ACCESS, default false:
	// OA discovery is off unless a caller opts in per call; a deployment sets it true
	// to make OA on by default while each call can still override it.
	OpenAccessEnabled bool
}

// defaultStartRetryWaits returns the built-in start-retry schedule: three waits
// of 5s, then three of 10s, then one of 15s — eight attempts spread over ~60s
// before a source is considered unable to start.
func defaultStartRetryWaits() []time.Duration {
	return []time.Duration{
		5 * time.Second, 5 * time.Second, 5 * time.Second,
		10 * time.Second, 10 * time.Second, 10 * time.Second,
		15 * time.Second,
	}
}

// KnownSources lists the download-source names recognized by LIBGEN_MCP_SOURCES,
// in their natural chain order (DOI-based first, then md5-based). It is the
// authority both for validating the configured list and for building the chain.
var KnownSources = []string{"unpaywall", "scihub", "libgen", "randombook"}

// defaultScihubHosts is the ordered list of Sci-Hub mirror hosts tried when
// LIBGEN_MCP_SCIHUB_HOSTS is unset. Mirrors rotate, so the source falls through
// the list until one serves an article page.
var defaultScihubHosts = []string{"sci-hub.ee", "sci-hub.se", "sci-hub.st", "sci-hub.ru", "sci-hub.wf"}

// Load builds the configuration from environment variables.
//
// Every new variable is optional; an empty string uses the default value. A
// numeric value that is present but invalid produces an error instead of
// silently falling back to the default.
func Load() (*Config, error) {
	cfg := &Config{
		Mirror:                  strings.TrimRight(os.Getenv("LIBGEN_MIRROR"), "/"),
		Timeout:                 30 * time.Second,
		LogLevel:                slog.LevelInfo,
		RateRPS:                 1,
		RateBurst:               1,
		MaxDownloadBytes:        0,
		MaxConcurrentDownloads:  2,
		RetryAttempts:           3,
		UnpaywallEmail:          "", // empty disables the unpaywall source; each deployment sets its own contact email via LIBGEN_MCP_UNPAYWALL_EMAIL
		ScihubHosts:             append([]string(nil), defaultScihubHosts...),
		DownloadStartRetryWaits: defaultStartRetryWaits(),
		DownloadStallTimeout:    60 * time.Second,
		ReadMaxChars:            6000,
		ReadDefaultPages:        5,
		ReadCacheBytes:          512 << 20, // 512 MiB
		ReadCacheTTL:            10 * time.Minute,
		EnrichEnabled:           true,
		OpenAccessEnabled:       false,
	}
	if v := os.Getenv("LIBGEN_MCP_UNPAYWALL_EMAIL"); v != "" {
		cfg.UnpaywallEmail = v
	}
	if v := os.Getenv("LIBGEN_MCP_SCIHUB_HOSTS"); v != "" {
		cfg.ScihubHosts = splitHosts(v)
	}
	if v := os.Getenv("LIBGEN_MCP_SOURCES"); v != "" {
		cfg.Sources = splitHosts(v)
	}
	if dir := os.Getenv("LIBGEN_MCP_DOWNLOAD_DIR"); dir != "" {
		cfg.DownloadDir = dir
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolving home dir: %w", err)
		}
		cfg.DownloadDir = filepath.Join(home, "Downloads")
	}
	if v := os.Getenv("LIBGEN_MCP_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("LIBGEN_MCP_TIMEOUT: %w", err)
		}
		cfg.Timeout = d
	}
	if v := os.Getenv("LIBGEN_MCP_LOG_LEVEL"); v != "" {
		level, err := logging.ParseLevel(v)
		if err != nil {
			return nil, fmt.Errorf("LIBGEN_MCP_LOG_LEVEL: %w", err)
		}
		cfg.LogLevel = level
	}
	if err := loadDownloadTuning(cfg); err != nil {
		return nil, err
	}
	if err := loadNumeric(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loadDownloadTuning fills the download-tuning fields (the stall timeout and the
// start-retry schedule) from the environment, leaving the spec defaults in place
// when the variables are unset.
func loadDownloadTuning(cfg *Config) error {
	if v := os.Getenv("LIBGEN_MCP_DOWNLOAD_STALL_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("LIBGEN_MCP_DOWNLOAD_STALL_TIMEOUT: %w", err)
		}
		cfg.DownloadStallTimeout = d
	}
	if v := os.Getenv("LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS"); v != "" {
		waits, err := parseDurations(v)
		if err != nil {
			return fmt.Errorf("LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS: %w", err)
		}
		cfg.DownloadStartRetryWaits = waits
	}
	return nil
}

// parseDurations parses a comma-separated list of Go durations (e.g.
// "5s,5s,10s"), trimming whitespace and dropping empty entries. An unparseable
// entry is an error so a malformed schedule fails fast rather than being
// silently ignored.
func parseDurations(v string) ([]time.Duration, error) {
	parts := strings.Split(v, ",")
	waits := make([]time.Duration, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		d, err := time.ParseDuration(p)
		if err != nil {
			return nil, fmt.Errorf("invalid duration %q: %w", p, err)
		}
		waits = append(waits, d)
	}
	return waits, nil
}

// loadNumeric fills the numeric and boolean scalar fields of cfg from the
// environment.
func loadNumeric(cfg *Config) error {
	if err := envBool("LIBGEN_MCP_REMOTE_DOWNLOADS", &cfg.RemoteDownloads); err != nil {
		return err
	}
	if err := envBool("LIBGEN_MCP_ENRICH", &cfg.EnrichEnabled); err != nil {
		return err
	}
	if err := envBool("LIBGEN_MCP_OPEN_ACCESS", &cfg.OpenAccessEnabled); err != nil {
		return err
	}
	if err := envFloat("LIBGEN_MCP_RATE_RPS", &cfg.RateRPS); err != nil {
		return err
	}
	if err := envInt("LIBGEN_MCP_RATE_BURST", &cfg.RateBurst); err != nil {
		return err
	}
	if err := envInt64("LIBGEN_MCP_MAX_DOWNLOAD_BYTES", &cfg.MaxDownloadBytes); err != nil {
		return err
	}
	if err := envInt("LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS", &cfg.MaxConcurrentDownloads); err != nil {
		return err
	}
	if err := envInt("LIBGEN_MCP_RETRY_ATTEMPTS", &cfg.RetryAttempts); err != nil {
		return err
	}
	if err := envInt("LIBGEN_MCP_READ_MAX_CHARS", &cfg.ReadMaxChars); err != nil {
		return err
	}
	if err := envInt("LIBGEN_MCP_READ_DEFAULT_PAGES", &cfg.ReadDefaultPages); err != nil {
		return err
	}
	if err := envInt64("LIBGEN_MCP_READ_CACHE_BYTES", &cfg.ReadCacheBytes); err != nil {
		return err
	}
	if err := envDuration("LIBGEN_MCP_READ_CACHE_TTL", &cfg.ReadCacheTTL); err != nil {
		return err
	}
	return nil
}

// splitHosts parses a comma-separated host list, trimming surrounding whitespace
// from each entry and dropping empties, so "a , b ," yields ["a", "b"].
func splitHosts(v string) []string {
	parts := strings.Split(v, ",")
	hosts := make([]string, 0, len(parts))
	for _, p := range parts {
		if h := strings.TrimSpace(p); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// envBool overwrites *dst with the boolean read from the variable key if present.
// It accepts the forms strconv.ParseBool understands (1/0, t/f, true/false).
func envBool(key string, dst *bool) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dst = b
	return nil
}

// envInt overwrites *dst with the integer read from the variable key if present.
func envInt(key string, dst *int) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dst = n
	return nil
}

// envInt64 overwrites *dst with the int64 read from the variable key if present.
func envInt64(key string, dst *int64) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dst = n
	return nil
}

// envFloat overwrites *dst with the float64 read from the variable key if present.
func envFloat(key string, dst *float64) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dst = n
	return nil
}

// envDuration overwrites *dst with the Go duration read from the variable key if
// present.
func envDuration(key string, dst *time.Duration) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dst = d
	return nil
}

// Validate checks that the configuration values are within range and that the
// mirror and download directory are usable.
func (c *Config) Validate() error {
	if err := c.validateRanges(); err != nil {
		return err
	}
	if err := c.validateReadRanges(); err != nil {
		return err
	}
	if err := c.validateDownloadTuning(); err != nil {
		return err
	}
	if err := validateUnpaywallEmail(c.UnpaywallEmail); err != nil {
		return err
	}
	if err := validateScihubHosts(c.ScihubHosts); err != nil {
		return err
	}
	if err := validateSources(c.Sources); err != nil {
		return err
	}
	if err := validateMirror(c.Mirror); err != nil {
		return err
	}
	return validateDownloadDir(c.DownloadDir)
}

// validateRanges checks that the numeric configuration fields fall within their
// allowed bounds, reporting the first out-of-range field in declaration order.
func (c *Config) validateRanges() error {
	if c.RateRPS <= 0 || c.RateRPS > 20 {
		return fmt.Errorf("LIBGEN_MCP_RATE_RPS must be in (0, 20], got %v", c.RateRPS)
	}
	if c.RateBurst < 1 || c.RateBurst > 100 {
		return fmt.Errorf("LIBGEN_MCP_RATE_BURST must be in [1, 100], got %d", c.RateBurst)
	}
	if c.MaxDownloadBytes < 0 || c.MaxDownloadBytes > maxDownloadBytesLimit {
		return fmt.Errorf("LIBGEN_MCP_MAX_DOWNLOAD_BYTES must be in [0, %d], got %d", maxDownloadBytesLimit, c.MaxDownloadBytes)
	}
	if c.MaxConcurrentDownloads < 1 || c.MaxConcurrentDownloads > 16 {
		return fmt.Errorf("LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS must be in [1, 16], got %d", c.MaxConcurrentDownloads)
	}
	if c.RetryAttempts < 1 || c.RetryAttempts > 10 {
		return fmt.Errorf("LIBGEN_MCP_RETRY_ATTEMPTS must be in [1, 10], got %d", c.RetryAttempts)
	}
	if c.Timeout <= 0 || c.Timeout > maxTimeout {
		return fmt.Errorf("LIBGEN_MCP_TIMEOUT must be in (0, %v], got %v", maxTimeout, c.Timeout)
	}
	return nil
}

// validateReadRanges checks that the read-tool and temp-cache tuning fields
// (ReadMaxChars, ReadDefaultPages, ReadCacheBytes, ReadCacheTTL) fall within
// their allowed bounds, reporting the first out-of-range field in declaration
// order.
func (c *Config) validateReadRanges() error {
	if c.ReadMaxChars < 500 || c.ReadMaxChars > 200000 {
		return fmt.Errorf("LIBGEN_MCP_READ_MAX_CHARS must be in [500, 200000], got %d", c.ReadMaxChars)
	}
	if c.ReadDefaultPages < 1 || c.ReadDefaultPages > 200 {
		return fmt.Errorf("LIBGEN_MCP_READ_DEFAULT_PAGES must be in [1, 200], got %d", c.ReadDefaultPages)
	}
	if c.ReadCacheBytes < 1<<20 || c.ReadCacheBytes > maxDownloadBytesLimit {
		return fmt.Errorf("LIBGEN_MCP_READ_CACHE_BYTES must be in [%d, %d], got %d", int64(1<<20), maxDownloadBytesLimit, c.ReadCacheBytes)
	}
	if c.ReadCacheTTL < time.Second || c.ReadCacheTTL > 24*time.Hour {
		return fmt.Errorf("LIBGEN_MCP_READ_CACHE_TTL must be in [%v, %v], got %v", time.Second, 24*time.Hour, c.ReadCacheTTL)
	}
	return nil
}

// validateDownloadTuning checks the start-retry schedule and the stall timeout:
// the stall window must be positive and within its ceiling, the schedule must not
// list more than maxStartRetries waits, and each wait must be positive and within
// its ceiling.
func (c *Config) validateDownloadTuning() error {
	if c.DownloadStallTimeout <= 0 || c.DownloadStallTimeout > maxStallTimeout {
		return fmt.Errorf("LIBGEN_MCP_DOWNLOAD_STALL_TIMEOUT must be in (0, %v], got %v", maxStallTimeout, c.DownloadStallTimeout)
	}
	if len(c.DownloadStartRetryWaits) > maxStartRetries {
		return fmt.Errorf("LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS must list at most %d waits, got %d", maxStartRetries, len(c.DownloadStartRetryWaits))
	}
	for _, w := range c.DownloadStartRetryWaits {
		if w <= 0 || w > maxStartRetryWait {
			return fmt.Errorf("LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS entries must be in (0, %v], got %v", maxStartRetryWait, w)
		}
	}
	return nil
}

// validateUnpaywallEmail applies a basic sanity check to the Unpaywall contact
// address. An empty value is valid and disables the unpaywall source (each
// deployment opts in by setting its own email); a non-empty value must contain
// an "@" and have a "." somewhere after it (the Unpaywall API rejects requests
// without a plausible email).
func validateUnpaywallEmail(email string) error {
	if email == "" {
		return nil
	}
	at := strings.Index(email, "@")
	if at <= 0 {
		return fmt.Errorf("LIBGEN_MCP_UNPAYWALL_EMAIL must contain %q, got %q", "@", email)
	}
	dot := strings.Index(email[at+1:], ".")
	if dot <= 0 || at+1+dot == len(email)-1 {
		return fmt.Errorf("LIBGEN_MCP_UNPAYWALL_EMAIL must have a domain with a dot, got %q", email)
	}
	return nil
}

// validateScihubHosts checks that the Sci-Hub host list is non-empty and that
// each entry is a bare host: no scheme and no slash (the source builds
// https://<host>/<doi> itself, so a scheme or path here would corrupt the URL).
func validateScihubHosts(hosts []string) error {
	if len(hosts) == 0 {
		return errors.New("LIBGEN_MCP_SCIHUB_HOSTS must list at least one host")
	}
	for _, h := range hosts {
		if h == "" {
			return errors.New("LIBGEN_MCP_SCIHUB_HOSTS must not contain empty hosts")
		}
		if strings.Contains(h, "/") || strings.Contains(h, "://") {
			return fmt.Errorf("LIBGEN_MCP_SCIHUB_HOSTS entries must be bare hosts, got %q", h)
		}
	}
	return nil
}

// validateSources checks that every entry in the enabled-sources list names a
// known download source (case-insensitive). An empty list is valid and means
// "all sources enabled".
func validateSources(sources []string) error {
	for _, s := range sources {
		if !slices.Contains(KnownSources, strings.ToLower(strings.TrimSpace(s))) {
			return fmt.Errorf("LIBGEN_MCP_SOURCES has unknown source %q (allowed: %s)", s, strings.Join(KnownSources, ", "))
		}
	}
	return nil
}

// SourceEnabled reports whether the named download source should be part of the
// chain. When Sources is empty every source is enabled; otherwise only the listed
// names (compared case-insensitively) are. The unpaywall source is additionally
// gated on a configured contact email: its API rejects requests without one, so
// an empty LIBGEN_MCP_UNPAYWALL_EMAIL disables it regardless of the Sources list.
func (c *Config) SourceEnabled(name string) bool {
	if strings.EqualFold(strings.TrimSpace(name), "unpaywall") && strings.TrimSpace(c.UnpaywallEmail) == "" {
		return false
	}
	if len(c.Sources) == 0 {
		return true
	}
	for _, s := range c.Sources {
		if strings.EqualFold(strings.TrimSpace(s), name) {
			return true
		}
	}
	return false
}

// validateMirror checks that a non-empty mirror is an http/https URL with a host.
func validateMirror(mirror string) error {
	if mirror == "" {
		return nil
	}
	u, err := url.Parse(mirror)
	if err != nil {
		return fmt.Errorf("LIBGEN_MIRROR is not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("LIBGEN_MIRROR must use http or https, got scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("LIBGEN_MIRROR must include a host")
	}
	return nil
}

// Test seams for the write-test cleanup steps. Closing a freshly created temp
// file and removing it from an already-verified-writable directory cannot fail
// deterministically on a real filesystem, so these are overridden in tests to
// exercise the defensive error branches below.
var (
	closeWriteTestFile  = (*os.File).Close
	removeWriteTestFile = os.Remove
)

// validateDownloadDir creates the download directory if missing and checks that
// it is writable using a temporary file.
func validateDownloadDir(dir string) error {
	if dir == "" {
		return errors.New("LIBGEN_MCP_DOWNLOAD_DIR must not be empty")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("LIBGEN_MCP_DOWNLOAD_DIR %q is not usable: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".libgen-mcp-write-test-*")
	if err != nil {
		return fmt.Errorf("LIBGEN_MCP_DOWNLOAD_DIR %q is not writable: %w", dir, err)
	}
	name := f.Name()
	if closeErr := closeWriteTestFile(f); closeErr != nil {
		return fmt.Errorf("LIBGEN_MCP_DOWNLOAD_DIR %q write test: %w", dir, closeErr)
	}
	if rmErr := removeWriteTestFile(name); rmErr != nil {
		return fmt.Errorf("LIBGEN_MCP_DOWNLOAD_DIR %q write test cleanup: %w", dir, rmErr)
	}
	return nil
}
