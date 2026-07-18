package config

import (
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

// TestLoadUnpaywallEmailDefault verifies the default Unpaywall contact email.
func TestLoadUnpaywallEmailDefault(t *testing.T) {
	t.Setenv("LIBGEN_MCP_UNPAYWALL_EMAIL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.UnpaywallEmail != "mail@jmrp.io" {
		t.Errorf("UnpaywallEmail = %q, want %q", cfg.UnpaywallEmail, "mail@jmrp.io")
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
	cases := []string{"", "no-at-sign", "no-dot@localhost", "trailing@dot."}
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

// TestLoadSources verifies LIBGEN_MCP_SOURCES parses into the enabled list and
// that SourceEnabled reflects it (empty = all enabled; a set = only those named).
func TestLoadSources(t *testing.T) {
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
		Mirror:                 "https://libgen.li",
		DownloadDir:            t.TempDir(),
		Timeout:                30 * time.Second,
		LogLevel:               slog.LevelInfo,
		RateRPS:                1,
		RateBurst:              1,
		MaxDownloadBytes:       0,
		MaxConcurrentDownloads: 2,
		RetryAttempts:          3,
		UnpaywallEmail:         "mail@jmrp.io",
		ScihubHosts:            []string{"sci-hub.ee", "sci-hub.se"},
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
		{"BadMirrorScheme", func(c *Config) { c.Mirror = "ftp://x" }},
		{"BadMirrorNoHost", func(c *Config) { c.Mirror = "https://" }},
		{"MirrorUnparseable", func(c *Config) { c.Mirror = "://nope" }},
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
