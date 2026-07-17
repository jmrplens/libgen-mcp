// Package config carga la configuración del servidor desde variables de entorno.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	Mirror      string        // LIBGEN_MIRROR: mirror forzado, p. ej. https://libgen.li
	DownloadDir string        // LIBGEN_MCP_DOWNLOAD_DIR: destino de descargas
	Timeout     time.Duration // LIBGEN_MCP_TIMEOUT: timeout por petición HTTP
}

func Load() (*Config, error) {
	cfg := &Config{
		Mirror:  strings.TrimRight(os.Getenv("LIBGEN_MIRROR"), "/"),
		Timeout: 30 * time.Second,
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
	return cfg, nil
}
