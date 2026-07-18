// Package config carga la configuración del servidor desde variables de entorno.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/logging"
)

// maxDownloadBytesLimit es el techo permitido para MaxDownloadBytes (50 GiB).
const maxDownloadBytesLimit int64 = 50 * 1024 * 1024 * 1024

// maxTimeout es el techo permitido para Timeout.
const maxTimeout = 10 * time.Minute

// Config agrupa la configuración del servidor leída del entorno.
type Config struct {
	Mirror                 string        // LIBGEN_MIRROR: mirror forzado, p. ej. https://libgen.li
	DownloadDir            string        // LIBGEN_MCP_DOWNLOAD_DIR: destino de descargas
	Timeout                time.Duration // LIBGEN_MCP_TIMEOUT: timeout por petición HTTP
	LogLevel               slog.Level    // LIBGEN_MCP_LOG_LEVEL: nivel de log (debug/info/warn/error)
	RateRPS                float64       // LIBGEN_MCP_RATE_RPS: peticiones por segundo permitidas
	RateBurst              int           // LIBGEN_MCP_RATE_BURST: ráfaga máxima del limitador
	MaxDownloadBytes       int64         // LIBGEN_MCP_MAX_DOWNLOAD_BYTES: tamaño máximo de descarga (0 = sin límite)
	MaxConcurrentDownloads int           // LIBGEN_MCP_MAX_CONCURRENT_DOWNLOADS: descargas simultáneas
	RetryAttempts          int           // LIBGEN_MCP_RETRY_ATTEMPTS: reintentos por petición
}

// Load construye la configuración a partir de las variables de entorno.
//
// Todas las variables nuevas son opcionales; una cadena vacía usa el valor por
// defecto. Un valor numérico presente pero inválido produce un error en lugar de
// caer silenciosamente al valor por defecto.
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

// loadNumeric rellena los campos numéricos del cfg a partir del entorno.
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

// envInt sobrescribe *dst con el entero leído de la variable key si está presente.
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

// envInt64 sobrescribe *dst con el int64 leído de la variable key si está presente.
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

// envFloat sobrescribe *dst con el float64 leído de la variable key si está presente.
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

// Validate comprueba que los valores de la configuración están dentro de rango
// y que el mirror y el directorio de descargas son utilizables.
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
	if err := validateMirror(c.Mirror); err != nil {
		return err
	}
	return validateDownloadDir(c.DownloadDir)
}

// validateMirror comprueba que un mirror no vacío es una URL http/https con host.
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

// validateDownloadDir crea el directorio de descargas si falta y comprueba que
// es escribible mediante un fichero temporal.
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
