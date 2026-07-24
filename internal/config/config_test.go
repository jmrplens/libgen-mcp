package config

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLoadDefaults verifies LoadDefaults.
func TestLoadDefaults(t *testing.T) {
	t.Setenv("LIBGEN_MIRROR", "")
	t.Setenv("LIBGEN_MCP_DOWNLOAD_DIR", "")
	t.Setenv("LIBGEN_MCP_TIMEOUT", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Mirror != "" {
		t.Errorf("Mirror = %q, want empty", cfg.Mirror)
	}
	if filepath.Base(cfg.DownloadDir) != "Downloads" {
		t.Errorf("DownloadDir = %q, want ~/Downloads", cfg.DownloadDir)
	}
	if cfg.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", cfg.Timeout)
	}
}

// TestLoadOverrides verifies LoadOverrides.
func TestLoadOverrides(t *testing.T) {
	t.Setenv("LIBGEN_MIRROR", "https://libgen.la/")
	t.Setenv("LIBGEN_MCP_DOWNLOAD_DIR", "/tmp/books")
	t.Setenv("LIBGEN_MCP_TIMEOUT", "5s")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Mirror != "https://libgen.la" {
		t.Errorf("Mirror = %q, want https://libgen.la (no trailing slash)", cfg.Mirror)
	}
	if cfg.DownloadDir != "/tmp/books" {
		t.Errorf("DownloadDir = %q", cfg.DownloadDir)
	}
	if cfg.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want 5s", cfg.Timeout)
	}
}

// TestLoadBadTimeout verifies LoadBadTimeout.
func TestLoadBadTimeout(t *testing.T) {
	t.Setenv("LIBGEN_MCP_TIMEOUT", "banana")
	if _, err := Load(); err == nil {
		t.Fatal("Load() with an invalid timeout should fail")
	}
}

// TestLoadRemoteDownloads checks LIBGEN_MCP_REMOTE_DOWNLOADS: unset defaults to
// false, the truthy forms strconv.ParseBool accepts set it, and a non-boolean
// value is a load error rather than a silent fallback.
func TestLoadRemoteDownloads(t *testing.T) {
	t.Setenv("LIBGEN_MCP_DOWNLOAD_DIR", t.TempDir()) // keep Load() offline/valid

	t.Run("default false", func(t *testing.T) {
		t.Setenv("LIBGEN_MCP_REMOTE_DOWNLOADS", "")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.RemoteDownloads {
			t.Error("RemoteDownloads = true, want false when unset")
		}
	})

	for _, v := range []string{"1", "true", "TRUE", "t"} {
		t.Run("true via "+v, func(t *testing.T) {
			t.Setenv("LIBGEN_MCP_REMOTE_DOWNLOADS", v)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if !cfg.RemoteDownloads {
				t.Errorf("RemoteDownloads = false for %q, want true", v)
			}
		})
	}

	t.Run("false via 0", func(t *testing.T) {
		t.Setenv("LIBGEN_MCP_REMOTE_DOWNLOADS", "0")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.RemoteDownloads {
			t.Error("RemoteDownloads = true for \"0\", want false")
		}
	})

	t.Run("invalid errors", func(t *testing.T) {
		t.Setenv("LIBGEN_MCP_REMOTE_DOWNLOADS", "banana")
		if _, err := Load(); err == nil {
			t.Fatal("Load() with a non-boolean LIBGEN_MCP_REMOTE_DOWNLOADS should fail")
		}
	})
}

// TestLoadEnrichEnabled checks LIBGEN_MCP_ENRICH (parsed via envBool, default
// true): unset leaves enrichment allowed, an explicit false form disables it,
// and a non-boolean value is a load error rather than a silent fallback.
func TestLoadEnrichEnabled(t *testing.T) {
	t.Setenv("LIBGEN_MCP_DOWNLOAD_DIR", t.TempDir()) // keep Load() offline/valid

	t.Run("default true", func(t *testing.T) {
		t.Setenv("LIBGEN_MCP_ENRICH", "")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if !cfg.EnrichEnabled {
			t.Error("EnrichEnabled = false, want true when unset")
		}
	})

	for _, v := range []string{"false", "FALSE", "f", "0"} {
		t.Run("false via "+v, func(t *testing.T) {
			t.Setenv("LIBGEN_MCP_ENRICH", v)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.EnrichEnabled {
				t.Errorf("EnrichEnabled = true for %q, want false", v)
			}
		})
	}

	t.Run("invalid errors", func(t *testing.T) {
		t.Setenv("LIBGEN_MCP_ENRICH", "banana")
		if _, err := Load(); err == nil {
			t.Fatal("Load() with a non-boolean LIBGEN_MCP_ENRICH should fail")
		}
	})
}

// TestLoadNewDefaults verifies LoadNewDefaults.
func TestLoadNewDefaults(t *testing.T) {
	t.Setenv("LIBGEN_MCP_LOG_LEVEL", "")
	t.Setenv("LIBGEN_MCP_RATE_RPS", "")
	t.Setenv("LIBGEN_MCP_RATE_BURST", "")
	t.Setenv("LIBGEN_MCP_MAX_DOWNLOAD_BYTES", "")
	t.Setenv("LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS", "")
	t.Setenv("LIBGEN_MCP_RETRY_ATTEMPTS", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelInfo)
	}
	if cfg.RateRPS != 1 {
		t.Errorf("RateRPS = %v, want 1", cfg.RateRPS)
	}
	if cfg.RateBurst != 1 {
		t.Errorf("RateBurst = %v, want 1", cfg.RateBurst)
	}
	if cfg.MaxDownloadBytes != 0 {
		t.Errorf("MaxDownloadBytes = %v, want 0", cfg.MaxDownloadBytes)
	}
	if cfg.MaxConcurrentDownloads != 2 {
		t.Errorf("MaxConcurrentDownloads = %v, want 2", cfg.MaxConcurrentDownloads)
	}
	if cfg.RetryAttempts != 3 {
		t.Errorf("RetryAttempts = %v, want 3", cfg.RetryAttempts)
	}
}

// TestLoadNewOverrides verifies LoadNewOverrides.
func TestLoadNewOverrides(t *testing.T) {
	t.Setenv("LIBGEN_MCP_LOG_LEVEL", "debug")
	t.Setenv("LIBGEN_MCP_RATE_RPS", "2.5")
	t.Setenv("LIBGEN_MCP_RATE_BURST", "5")
	t.Setenv("LIBGEN_MCP_MAX_DOWNLOAD_BYTES", "1048576")
	t.Setenv("LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS", "8")
	t.Setenv("LIBGEN_MCP_RETRY_ATTEMPTS", "7")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelDebug)
	}
	if cfg.RateRPS != 2.5 {
		t.Errorf("RateRPS = %v, want 2.5", cfg.RateRPS)
	}
	if cfg.RateBurst != 5 {
		t.Errorf("RateBurst = %v, want 5", cfg.RateBurst)
	}
	if cfg.MaxDownloadBytes != 1048576 {
		t.Errorf("MaxDownloadBytes = %v, want 1048576", cfg.MaxDownloadBytes)
	}
	if cfg.MaxConcurrentDownloads != 8 {
		t.Errorf("MaxConcurrentDownloads = %v, want 8", cfg.MaxConcurrentDownloads)
	}
	if cfg.RetryAttempts != 7 {
		t.Errorf("RetryAttempts = %v, want 7", cfg.RetryAttempts)
	}
}

// TestLoadDownloadTuningDefaults verifies the start-retry schedule and stall
// timeout fall back to the spec defaults (3x5s, 3x10s, 1x15s; stall 60s) when
// their environment variables are unset.
func TestLoadDownloadTuningDefaults(t *testing.T) {
	t.Setenv("LIBGEN_MCP_DOWNLOAD_STALL_TIMEOUT", "")
	t.Setenv("LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DownloadStallTimeout != 60*time.Second {
		t.Errorf("DownloadStallTimeout = %v, want 60s", cfg.DownloadStallTimeout)
	}
	want := []time.Duration{5 * time.Second, 5 * time.Second, 5 * time.Second, 10 * time.Second, 10 * time.Second, 10 * time.Second, 15 * time.Second}
	if len(cfg.DownloadStartRetryWaits) != len(want) {
		t.Fatalf("DownloadStartRetryWaits = %v, want %v", cfg.DownloadStartRetryWaits, want)
	}
	for i, w := range want {
		if cfg.DownloadStartRetryWaits[i] != w {
			t.Errorf("DownloadStartRetryWaits[%d] = %v, want %v", i, cfg.DownloadStartRetryWaits[i], w)
		}
	}
}

// TestLoadDownloadTuningOverrides verifies both download-tuning variables parse
// from the environment, including a comma-separated schedule with surrounding
// whitespace and empty entries.
func TestLoadDownloadTuningOverrides(t *testing.T) {
	t.Setenv("LIBGEN_MCP_DOWNLOAD_STALL_TIMEOUT", "90s")
	t.Setenv("LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS", " 1s , 2s ,, 3s ")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DownloadStallTimeout != 90*time.Second {
		t.Errorf("DownloadStallTimeout = %v, want 90s", cfg.DownloadStallTimeout)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 3 * time.Second}
	if len(cfg.DownloadStartRetryWaits) != len(want) {
		t.Fatalf("DownloadStartRetryWaits = %v, want %v", cfg.DownloadStartRetryWaits, want)
	}
	for i, w := range want {
		if cfg.DownloadStartRetryWaits[i] != w {
			t.Errorf("DownloadStartRetryWaits[%d] = %v, want %v", i, cfg.DownloadStartRetryWaits[i], w)
		}
	}
}

// TestLoadBadDownloadTuning verifies a malformed stall timeout or an unparseable
// entry in the start-retry schedule fails Load fast.
func TestLoadBadDownloadTuning(t *testing.T) {
	t.Run("stall", func(t *testing.T) {
		t.Setenv("LIBGEN_MCP_DOWNLOAD_STALL_TIMEOUT", "banana")
		if _, err := Load(); err == nil {
			t.Fatal("Load() with a bad stall timeout should fail")
		}
	})
	t.Run("waits", func(t *testing.T) {
		t.Setenv("LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS", "5s,banana")
		if _, err := Load(); err == nil {
			t.Fatal("Load() with a bad retry-wait entry should fail")
		}
	})
}

// TestLoadUnpaywallEmailDefault verifies unpaywall defaults to disabled (no
// contact email) and becomes enabled once an email is configured.
func TestLoadUnpaywallEmailDefault(t *testing.T) {
	t.Setenv("LIBGEN_MCP_UNPAYWALL_EMAIL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.UnpaywallEmail != "" {
		t.Errorf("UnpaywallEmail = %q, want empty (disabled by default)", cfg.UnpaywallEmail)
	}
	if cfg.SourceEnabled("unpaywall") {
		t.Error("unpaywall should be disabled when no contact email is configured")
	}

	t.Setenv("LIBGEN_MCP_UNPAYWALL_EMAIL", "me@example.com")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.UnpaywallEmail != "me@example.com" {
		t.Errorf("UnpaywallEmail = %q, want %q", cfg.UnpaywallEmail, "me@example.com")
	}
	if !cfg.SourceEnabled("unpaywall") {
		t.Error("unpaywall should be enabled once a contact email is configured")
	}
}

// TestLoadUnpaywallEmailOverride verifies overriding the Unpaywall contact email.
func TestLoadUnpaywallEmailOverride(t *testing.T) {
	t.Setenv("LIBGEN_MCP_UNPAYWALL_EMAIL", "someone@example.org")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.UnpaywallEmail != "someone@example.org" {
		t.Errorf("UnpaywallEmail = %q, want %q", cfg.UnpaywallEmail, "someone@example.org")
	}
}

// TestValidateBadUnpaywallEmail covers rejected Unpaywall contact emails.
func TestValidateBadUnpaywallEmail(t *testing.T) {
	cases := []string{"no-at-sign", "no-dot@localhost", "trailing@dot."}
	for _, bad := range cases {
		t.Run(bad, func(t *testing.T) {
			cfg := validConfig(t)
			cfg.UnpaywallEmail = bad
			if cfg.Validate() == nil {
				t.Fatalf("Validate() with UnpaywallEmail=%q should fail", bad)
			}
		})
	}
}

// TestValidateEmptyUnpaywallEmailOK verifies an empty contact email validates:
// it disables the unpaywall source rather than being a configuration error.
func TestValidateEmptyUnpaywallEmailOK(t *testing.T) {
	cfg := validConfig(t)
	cfg.UnpaywallEmail = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("empty UnpaywallEmail should validate (disabled), got %v", err)
	}
}

// TestSourceEnabledUnpaywallRequiresEmail verifies the unpaywall source is gated
// on a contact email even when it is otherwise enabled via LIBGEN_MCP_SOURCES,
// while the other sources are unaffected by the email.
func TestSourceEnabledUnpaywallRequiresEmail(t *testing.T) {
	if (&Config{UnpaywallEmail: "me@example.com"}).SourceEnabled("unpaywall") != true {
		t.Error("unpaywall should be enabled with an email and an empty Sources list")
	}
	if (&Config{}).SourceEnabled("unpaywall") {
		t.Error("unpaywall should be disabled without a contact email")
	}
	if (&Config{Sources: []string{"unpaywall"}}).SourceEnabled("unpaywall") {
		t.Error("unpaywall listed in Sources but without an email should stay disabled")
	}
	if !(&Config{}).SourceEnabled("scihub") {
		t.Error("scihub should not be gated on the unpaywall email")
	}
}

// TestLoadSources verifies LIBGEN_MCP_SOURCES parses into the enabled list and
// that SourceEnabled reflects it (empty = all enabled; a set = only those named).
func TestLoadSources(t *testing.T) {
	t.Setenv("LIBGEN_MCP_UNPAYWALL_EMAIL", "me@example.com") // unpaywall is email-gated
	t.Setenv("LIBGEN_MCP_SOURCES", " libgen , unpaywall ")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.SourceEnabled("libgen") || !cfg.SourceEnabled("unpaywall") {
		t.Errorf("SourceEnabled(libgen/unpaywall) = false, want true")
	}
	if cfg.SourceEnabled("scihub") || cfg.SourceEnabled("randombook") {
		t.Errorf("SourceEnabled(scihub/randombook) = true, want false")
	}

	empty := &Config{}
	if !empty.SourceEnabled("scihub") {
		t.Error("SourceEnabled on an empty list should enable every source")
	}
}

// TestValidateBadSources verifies an unknown source name is rejected.
func TestValidateBadSources(t *testing.T) {
	cfg := validConfig(t)
	cfg.Sources = []string{"libgen", "bogus"}
	if cfg.Validate() == nil {
		t.Fatal("Validate() with an unknown source should fail")
	}
}

// TestLoadBadNumericEnv verifies LoadBadNumericEnv.
func TestLoadBadNumericEnv(t *testing.T) {
	cases := map[string]string{
		"LIBGEN_MCP_RATE_RPS":                 "fast",
		"LIBGEN_MCP_RATE_BURST":               "many",
		"LIBGEN_MCP_MAX_DOWNLOAD_BYTES":       "big",
		"LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS": "lots",
		"LIBGEN_MCP_RETRY_ATTEMPTS":           "some",
		"LIBGEN_MCP_LOG_LEVEL":                "verbose",
		"LIBGEN_MCP_REMOTE_DOWNLOADS":         "maybe",
		"LIBGEN_MCP_ENRICH":                   "sometimes",
		"LIBGEN_MCP_EXTRA_SOURCES":            "sometimes",
	}
	for envKey, badVal := range cases {
		t.Run(envKey, func(t *testing.T) {
			t.Setenv(envKey, badVal)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() with %s=%q should fail", envKey, badVal)
			}
		})
	}
}

// TestLoadReadDefaults verifies the four read/temp-cache knobs (ReadMaxChars,
// ReadDefaultPages, ReadCacheBytes, ReadCacheTTL) fall back to their spec
// defaults when their environment variables are unset.
func TestLoadReadDefaults(t *testing.T) {
	t.Setenv("LIBGEN_MCP_READ_MAX_CHARS", "")
	t.Setenv("LIBGEN_MCP_READ_DEFAULT_PAGES", "")
	t.Setenv("LIBGEN_MCP_READ_CACHE_BYTES", "")
	t.Setenv("LIBGEN_MCP_READ_CACHE_TTL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ReadMaxChars != 6000 {
		t.Errorf("ReadMaxChars = %d, want 6000", cfg.ReadMaxChars)
	}
	if cfg.ReadDefaultPages != 5 {
		t.Errorf("ReadDefaultPages = %d, want 5", cfg.ReadDefaultPages)
	}
	if cfg.ReadCacheBytes != 512<<20 {
		t.Errorf("ReadCacheBytes = %d, want %d", cfg.ReadCacheBytes, int64(512<<20))
	}
	if cfg.ReadCacheTTL != 10*time.Minute {
		t.Errorf("ReadCacheTTL = %v, want 10m", cfg.ReadCacheTTL)
	}
}

// TestLoadReadOverrides verifies the four read/temp-cache knobs parse from the
// environment when set.
func TestLoadReadOverrides(t *testing.T) {
	t.Setenv("LIBGEN_MCP_READ_MAX_CHARS", "8000")
	t.Setenv("LIBGEN_MCP_READ_DEFAULT_PAGES", "10")
	t.Setenv("LIBGEN_MCP_READ_CACHE_BYTES", "1048576")
	t.Setenv("LIBGEN_MCP_READ_CACHE_TTL", "5m")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ReadMaxChars != 8000 {
		t.Errorf("ReadMaxChars = %d, want 8000", cfg.ReadMaxChars)
	}
	if cfg.ReadDefaultPages != 10 {
		t.Errorf("ReadDefaultPages = %d, want 10", cfg.ReadDefaultPages)
	}
	if cfg.ReadCacheBytes != 1048576 {
		t.Errorf("ReadCacheBytes = %d, want 1048576", cfg.ReadCacheBytes)
	}
	if cfg.ReadCacheTTL != 5*time.Minute {
		t.Errorf("ReadCacheTTL = %v, want 5m", cfg.ReadCacheTTL)
	}
}

// TestLoadBadReadEnv verifies an unparseable value for each of the four
// read/temp-cache knobs fails Load fast instead of silently keeping the default.
func TestLoadBadReadEnv(t *testing.T) {
	cases := map[string]string{
		"LIBGEN_MCP_READ_MAX_CHARS":     "many",
		"LIBGEN_MCP_READ_DEFAULT_PAGES": "some",
		"LIBGEN_MCP_READ_CACHE_BYTES":   "big",
		"LIBGEN_MCP_READ_CACHE_TTL":     "banana",
	}
	for envKey, badVal := range cases {
		t.Run(envKey, func(t *testing.T) {
			t.Setenv(envKey, badVal)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() with %s=%q should fail", envKey, badVal)
			}
		})
	}
}

// validConfig returns a configuration that passes Validate(), with DownloadDir
// escribible bajo t.TempDir().
func validConfig(t *testing.T) *Config {
	t.Helper()
	return &Config{
		Mirror:                  "https://libgen.li",
		DownloadDir:             t.TempDir(),
		Timeout:                 30 * time.Second,
		LogLevel:                slog.LevelInfo,
		RateRPS:                 1,
		RateBurst:               1,
		MaxDownloadBytes:        0,
		MaxConcurrentDownloads:  2,
		RetryAttempts:           3,
		UnpaywallEmail:          "mail@jmrp.io",
		ScihubHosts:             []string{"sci-hub.ee", "sci-hub.se"},
		DownloadStartRetryWaits: defaultStartRetryWaits(),
		DownloadStallTimeout:    60 * time.Second,
		ReadMaxChars:            6000,
		ReadDefaultPages:        5,
		ReadCacheBytes:          512 << 20,
		ReadCacheTTL:            10 * time.Minute,
	}
}

// TestLoadScihubHostsDefault verifies the default ordered Sci-Hub host list.
func TestLoadScihubHostsDefault(t *testing.T) {
	t.Setenv("LIBGEN_MCP_SCIHUB_HOSTS", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []string{"sci-hub.ee", "sci-hub.se", "sci-hub.st", "sci-hub.ru", "sci-hub.wf"}
	if len(cfg.ScihubHosts) != len(want) {
		t.Fatalf("ScihubHosts = %v, want %v", cfg.ScihubHosts, want)
	}
	for i, h := range want {
		if cfg.ScihubHosts[i] != h {
			t.Errorf("ScihubHosts[%d] = %q, want %q", i, cfg.ScihubHosts[i], h)
		}
	}
}

// TestLoadScihubHostsOverride verifies overriding and trimming the host list.
func TestLoadScihubHostsOverride(t *testing.T) {
	t.Setenv("LIBGEN_MCP_SCIHUB_HOSTS", " sci-hub.example , mirror.test ")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []string{"sci-hub.example", "mirror.test"}
	if len(cfg.ScihubHosts) != len(want) {
		t.Fatalf("ScihubHosts = %v, want %v", cfg.ScihubHosts, want)
	}
	for i, h := range want {
		if cfg.ScihubHosts[i] != h {
			t.Errorf("ScihubHosts[%d] = %q, want %q", i, cfg.ScihubHosts[i], h)
		}
	}
}

// TestValidateBadScihubHosts covers rejected Sci-Hub host lists.
func TestValidateBadScihubHosts(t *testing.T) {
	cases := []struct {
		name  string
		hosts []string
	}{
		{"empty", nil},
		{"scheme", []string{"https://sci-hub.ee"}},
		{"slash", []string{"sci-hub.ee/path"}},
		{"blank-entry", []string{"sci-hub.ee", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig(t)
			cfg.ScihubHosts = tc.hosts
			if cfg.Validate() == nil {
				t.Fatalf("Validate() with ScihubHosts=%v should fail", tc.hosts)
			}
		})
	}
}

// TestValidateValid verifies ValidateValid.
func TestValidateValid(t *testing.T) {
	if err := validConfig(t).Validate(); err != nil {
		t.Fatalf("Validate() on a valid config, error = %v", err)
	}
}

// TestValidateEmptyMirrorOK verifies ValidateEmptyMirrorOK.
func TestValidateEmptyMirrorOK(t *testing.T) {
	cfg := validConfig(t)
	cfg.Mirror = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with an empty mirror, error = %v", err)
	}
}

// TestValidateInvalid covers ValidateInvalid with table-driven subtests.
func TestValidateInvalid(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"RateRPSZero", func(c *Config) { c.RateRPS = 0 }},
		{"RateRPSTooHigh", func(c *Config) { c.RateRPS = 21 }},
		{"RateBurstZero", func(c *Config) { c.RateBurst = 0 }},
		{"RateBurstTooHigh", func(c *Config) { c.RateBurst = 101 }},
		{"ConcurrencyZero", func(c *Config) { c.MaxConcurrentDownloads = 0 }},
		{"ConcurrencyTooHigh", func(c *Config) { c.MaxConcurrentDownloads = 17 }},
		{"RetryZero", func(c *Config) { c.RetryAttempts = 0 }},
		{"RetryTooHigh", func(c *Config) { c.RetryAttempts = 11 }},
		{"MaxBytesNegative", func(c *Config) { c.MaxDownloadBytes = -1 }},
		{"MaxBytesTooHigh", func(c *Config) { c.MaxDownloadBytes = 51 * 1024 * 1024 * 1024 }},
		{"TimeoutZero", func(c *Config) { c.Timeout = 0 }},
		{"TimeoutTooHigh", func(c *Config) { c.Timeout = 11 * time.Minute }},
		{"StallTimeoutZero", func(c *Config) { c.DownloadStallTimeout = 0 }},
		{"StallTimeoutTooHigh", func(c *Config) { c.DownloadStallTimeout = 2 * time.Hour }},
		{"StartRetryWaitZero", func(c *Config) { c.DownloadStartRetryWaits = []time.Duration{0} }},
		{"StartRetryWaitTooHigh", func(c *Config) { c.DownloadStartRetryWaits = []time.Duration{11 * time.Minute} }},
		{"TooManyStartRetryWaits", func(c *Config) { c.DownloadStartRetryWaits = make([]time.Duration, maxStartRetries+1) }},
		{"BadMirrorScheme", func(c *Config) { c.Mirror = "ftp://x" }},
		{"BadMirrorNoHost", func(c *Config) { c.Mirror = "https://" }},
		{"MirrorUnparseable", func(c *Config) { c.Mirror = "://nope" }},
		{"ReadMaxCharsTooLow", func(c *Config) { c.ReadMaxChars = 499 }},
		{"ReadMaxCharsTooHigh", func(c *Config) { c.ReadMaxChars = 200001 }},
		{"ReadDefaultPagesTooLow", func(c *Config) { c.ReadDefaultPages = 0 }},
		{"ReadDefaultPagesTooHigh", func(c *Config) { c.ReadDefaultPages = 201 }},
		{"ReadCacheBytesTooLow", func(c *Config) { c.ReadCacheBytes = (1 << 20) - 1 }},
		{"ReadCacheBytesTooHigh", func(c *Config) { c.ReadCacheBytes = maxDownloadBytesLimit + 1 }},
		{"ReadCacheTTLTooLow", func(c *Config) { c.ReadCacheTTL = 500 * time.Millisecond }},
		{"ReadCacheTTLTooHigh", func(c *Config) { c.ReadCacheTTL = 25 * time.Hour }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig(t)
			tc.mutate(cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("Validate() for %s should fail", tc.name)
			}
		})
	}
}

// TestValidateEmptyDownloadDir verifies that an empty download directory is
// rejected by Validate (the empty-string guard in validateDownloadDir).
func TestValidateEmptyDownloadDir(t *testing.T) {
	cfg := validConfig(t)
	cfg.DownloadDir = ""
	if cfg.Validate() == nil {
		t.Fatal("Validate() with an empty DownloadDir should fail")
	}
}

// TestValidateReadOnlyDownloadDir verifies that a directory that exists but is
// not writable is rejected: MkdirAll succeeds on the existing dir, then the
// write-test CreateTemp fails.
func TestValidateReadOnlyDownloadDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // restore write bits so TempDir cleanup can remove the dir
	cfg := validConfig(t)
	cfg.DownloadDir = dir
	if cfg.Validate() == nil {
		t.Fatal("Validate() with a read-only DownloadDir should fail")
	}
}

// TestLoadHomeDirError verifies that when LIBGEN_MCP_DOWNLOAD_DIR is unset and no
// home directory can be resolved, Load fails instead of building an unusable
// default download path.
func TestLoadHomeDirError(t *testing.T) {
	t.Setenv("LIBGEN_MCP_DOWNLOAD_DIR", "")
	t.Setenv("HOME", "")
	if _, err := os.UserHomeDir(); err == nil {
		t.Skip("os.UserHomeDir still resolves a home directory on this platform")
	}
	if _, err := Load(); err == nil {
		t.Fatal("Load() with no home dir and no LIBGEN_MCP_DOWNLOAD_DIR should fail")
	}
}

// TestValidateUnwritableDownloadDir verifies ValidateUnwritableDownloadDir.
func TestValidateUnwritableDownloadDir(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "notadir")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	if closeErr := f.Close(); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}
	cfg := validConfig(t)
	// A subdirectory under a regular file cannot be created (ENOTDIR).
	cfg.DownloadDir = filepath.Join(f.Name(), "sub")
	if cfg.Validate() == nil {
		t.Fatal("Validate() with an unwritable DownloadDir should fail")
	}
}

// TestValidateDownloadDirCloseError covers the write-test Close-error branch of
// validateDownloadDir. Closing a freshly created temp file does not fail on a
// real filesystem, so the failure is injected through the closeWriteTestFile
// seam.
func TestValidateDownloadDirCloseError(t *testing.T) {
	orig := closeWriteTestFile
	t.Cleanup(func() { closeWriteTestFile = orig })
	closeWriteTestFile = func(f *os.File) error {
		_ = f.Close() // close the real fd, then report a synthetic failure
		return errors.New("synthetic close failure")
	}
	if err := validateDownloadDir(t.TempDir()); err == nil {
		t.Fatal("validateDownloadDir() should fail when the write-test file cannot be closed")
	}
}

// TestValidateDownloadDirRemoveError covers the write-test cleanup Remove-error
// branch of validateDownloadDir. Removing the temp file from an already-writable
// directory does not fail on a real filesystem, so the failure is injected
// through the removeWriteTestFile seam.
func TestValidateDownloadDirRemoveError(t *testing.T) {
	orig := removeWriteTestFile
	t.Cleanup(func() { removeWriteTestFile = orig })
	removeWriteTestFile = func(name string) error {
		_ = os.Remove(name) // remove the real temp file, then report a synthetic failure
		return errors.New("synthetic remove failure")
	}
	if err := validateDownloadDir(t.TempDir()); err == nil {
		t.Fatal("validateDownloadDir() should fail when the write-test cleanup cannot remove the file")
	}
}

// TestKnownSourcesIncludesSciDB pins the canonical chain order the download
// pipeline relies on: scidb follows scihub, so it acts as the article fallback
// that covers Sci-Hub's indexing gap, and annas sits last so it is the final
// book rescue after libgen and randombook.
func TestKnownSourcesIncludesSciDB(t *testing.T) {
	want := []string{"unpaywall", "scihub", "scidb", "libgen", "randombook", "annas"}
	if len(KnownSources) != len(want) {
		t.Fatalf("KnownSources = %v, want %v", KnownSources, want)
	}
	for i, w := range want {
		if KnownSources[i] != w {
			t.Fatalf("KnownSources[%d] = %q, want %q (full: %v)", i, KnownSources[i], w, KnownSources)
		}
	}
}

// TestLoadAnnasKey verifies LIBGEN_MCP_ANNAS_KEY populates AnnasKey, and that an
// unset variable leaves it empty so the annas source stays keyless by default.
func TestLoadAnnasKey(t *testing.T) {
	t.Setenv("LIBGEN_MCP_ANNAS_KEY", "test-secret")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AnnasKey != "test-secret" {
		t.Fatalf("AnnasKey = %q, want test-secret", cfg.AnnasKey)
	}

	t.Setenv("LIBGEN_MCP_ANNAS_KEY", "")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AnnasKey != "" {
		t.Fatalf("AnnasKey = %q, want empty (keyless default)", cfg.AnnasKey)
	}
}

// TestLoadExtraSources verifies LIBGEN_MCP_EXTRA_SOURCES accepts each mode,
// defaults to auto, and rejects an unknown value at startup rather than guessing.
func TestLoadExtraSources(t *testing.T) {
	for _, want := range []ExtraSourcesMode{ExtraSourcesAuto, ExtraSourcesAlways, ExtraSourcesNever} {
		t.Setenv("LIBGEN_MCP_EXTRA_SOURCES", string(want))
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load(%s): %v", want, err)
		}
		if cfg.ExtraSources != want {
			t.Fatalf("ExtraSources = %q, want %q", cfg.ExtraSources, want)
		}
	}

	t.Setenv("LIBGEN_MCP_EXTRA_SOURCES", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ExtraSources != ExtraSourcesAuto {
		t.Fatalf("default = %q, want auto", cfg.ExtraSources)
	}

	t.Setenv("LIBGEN_MCP_EXTRA_SOURCES", "sometimes")
	if _, lerr := Load(); lerr == nil {
		t.Fatal("an unknown mode must fail at startup, not fall back silently")
	}
}
