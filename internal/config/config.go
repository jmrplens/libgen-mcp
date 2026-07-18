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
		Mirror:                 strings.TrimRight(os.Getenv("LIBGEN_MIRROR"), "/"),
		Timeout:                30 * time.Second,
		LogLevel:               slog.LevelInfo,
		RateRPS:                1,
		RateBurst:              1,
		MaxDownloadBytes:       0,
		MaxConcurrentDownloads: 2,
		RetryAttempts:          3,
		UnpaywallEmail:         "mail@jmrp.io",
		ScihubHosts:            append([]string(nil), defaultScihubHosts...),
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
	if err := loadNumeric(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loadNumeric fills the numeric fields of cfg from the environment.
func loadNumeric(cfg *Config) error {
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

// Validate checks that the configuration values are within range and that the
// mirror and download directory are usable.
func (c *Config) Validate() error {
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

// validateUnpaywallEmail applies a basic sanity check to the Unpaywall contact
// address: it must be non-empty, contain an "@", and have a "." somewhere after
// that "@" (the Unpaywall API rejects requests without a plausible email).
func validateUnpaywallEmail(email string) error {
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
// names (compared case-insensitively) are.
func (c *Config) SourceEnabled(name string) bool {
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
	if closeErr := f.Close(); closeErr != nil {
		return fmt.Errorf("LIBGEN_MCP_DOWNLOAD_DIR %q write test: %w", dir, closeErr)
	}
	if rmErr := os.Remove(name); rmErr != nil {
		return fmt.Errorf("LIBGEN_MCP_DOWNLOAD_DIR %q write test cleanup: %w", dir, rmErr)
	}
	return nil
}
