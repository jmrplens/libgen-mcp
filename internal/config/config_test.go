package config

import (
	"path/filepath"
	"testing"
	"time"
)

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

func TestLoadOverrides(t *testing.T) {
	t.Setenv("LIBGEN_MIRROR", "https://libgen.la/")
	t.Setenv("LIBGEN_MCP_DOWNLOAD_DIR", "/tmp/books")
	t.Setenv("LIBGEN_MCP_TIMEOUT", "5s")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Mirror != "https://libgen.la" {
		t.Errorf("Mirror = %q, want https://libgen.la (sin barra final)", cfg.Mirror)
	}
	if cfg.DownloadDir != "/tmp/books" {
		t.Errorf("DownloadDir = %q", cfg.DownloadDir)
	}
	if cfg.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want 5s", cfg.Timeout)
	}
}

func TestLoadBadTimeout(t *testing.T) {
	t.Setenv("LIBGEN_MCP_TIMEOUT", "banana")
	if _, err := Load(); err == nil {
		t.Fatal("Load() con timeout inválido debería fallar")
	}
}
