// Package libgen implementa el cliente HTTP contra la familia de mirrors libgen.li:
// búsqueda (HTML), detalles (json.php) y descarga (ads.php → get.php → CDN).
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
	maxBodySize = 20 << 20 // 20 MiB para páginas HTML/JSON (no descargas)

	// cooldownDuration es el tiempo que un mirror queda apartado tras fallar.
	cooldownDuration = 45 * time.Second
	// defaultBackoffBase es la base del backoff (crece por intento) entre reintentos.
	defaultBackoffBase = 200 * time.Millisecond
)

// ErrAllMirrorsFailed indica que ningún mirror respondió correctamente.
var ErrAllMirrorsFailed = errors.New("all libgen mirrors unreachable (network block? try a VPN or different DNS)")

// MirrorLister provides candidate base URLs, preferred first.
type MirrorLister interface {
	// Mirrors returns candidate base URLs, preferred first.
	Mirrors(ctx context.Context) []string
}

// Client habla con la familia de mirrors libgen con failover, límite de tasa,
// reintentos con backoff creciente y cooldown por mirror tras fallos.
type Client struct {
	mirrors     MirrorLister
	http        *http.Client // páginas: con timeout
	dl          *http.Client // descargas en streaming: sin timeout global, gobierna ctx
	limiter     *rate.Limiter
	retry       int           // número máximo de pasadas sobre los mirrors
	backoffBase time.Duration // base del backoff; inyectable para tests

	mu       sync.Mutex           // protege cooldown
	cooldown map[string]time.Time // mirror base → instante en que expira el cooldown
}

// New construye un Client a partir de la configuración: rate limiter
// (RateRPS/RateBurst), número de reintentos (RetryAttempts) y timeout HTTP.
func New(m MirrorLister, cfg *config.Config) *Client {
	return &Client{
		mirrors:     m,
		http:        &http.Client{Timeout: cfg.Timeout},
		dl:          &http.Client{},
		limiter:     rate.NewLimiter(rate.Limit(cfg.RateRPS), cfg.RateBurst),
		retry:       cfg.RetryAttempts,
		backoffBase: defaultBackoffBase,
		cooldown:    make(map[string]time.Time),
	}
}

// get prueba path?q en los mirrors hasta obtener un 200. Reintenta ante fallos
// transitorios (timeout, error de red, status 5xx/429) con backoff creciente,
// apartando en cooldown los mirrors que fallan; los errores permanentes (p. ej.
// 404) se propagan sin reintentar. Devuelve el cuerpo y la URL base que respondió.
func (c *Client) get(ctx context.Context, path string, q url.Values) (content []byte, baseURL string, resErr error) {
	mirrorList := c.mirrors.Mirrors(ctx)
	var errs []error
	attempts := max(c.retry, 1)
	for attempt := range attempts {
		if attempt > 0 {
			if err := c.sleepBackoff(ctx, attempt); err != nil {
				return nil, "", err
			}
		}
		body, base, done, err := c.sweep(ctx, mirrorList, path, q, &errs)
		if done {
			return body, base, err
		}
	}
	slog.Error("all mirror attempts exhausted", "path", path, "attempts", attempts)
	return nil, "", fmt.Errorf("%w: %w", ErrAllMirrorsFailed, errors.Join(errs...))
}

// sweep realiza una pasada sobre los mirrors candidatos. Devuelve done=true si
// hay que parar: éxito (err=nil) o error permanente (err!=nil). done=false indica
// que todos los mirrors fallaron de forma transitoria y se puede reintentar.
func (c *Client) sweep(ctx context.Context, mirrorList []string, path string, q url.Values, errs *[]error) (body []byte, base string, done bool, err error) {
	for _, m := range c.candidates(mirrorList) {
		if werr := c.limiter.Wait(ctx); werr != nil {
			return nil, "", true, werr
		}
		slog.Debug("mirror attempt", "mirror", m, "path", path)
		b, transient, reqErr := c.doRequest(ctx, m, path, q)
		if reqErr == nil {
			return b, m, true, nil
		}
		*errs = append(*errs, reqErr)
		if !transient {
			return nil, "", true, reqErr
		}
		c.markCooldown(m)
		slog.Warn("mirror failed, trying next", "mirror", m, "error", reqErr)
	}
	return nil, "", false, nil
}

// doRequest ejecuta una petición contra un mirror y clasifica el resultado.
// Devuelve transient=true para errores de red/timeout y status 5xx/429; los 4xx
// distintos de 429 se consideran permanentes. Un 200 legible devuelve el cuerpo.
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

// candidates devuelve los mirrors fuera de cooldown en orden de preferencia. Si
// todos están en cooldown, devuelve la lista completa (mejor intentar que nada).
func (c *Client) candidates(mirrorList []string) []string {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	avail := make([]string, 0, len(mirrorList))
	for _, m := range mirrorList {
		if until, ok := c.cooldown[m]; !ok || now.After(until) {
			avail = append(avail, m)
		}
	}
	if len(avail) == 0 {
		return mirrorList
	}
	return avail
}

// markCooldown aparta un mirror durante cooldownDuration tras un fallo transitorio.
func (c *Client) markCooldown(base string) {
	c.mu.Lock()
	c.cooldown[base] = time.Now().Add(cooldownDuration)
	c.mu.Unlock()
}

// sleepBackoff espera un backoff creciente con jitter antes del siguiente
// intento, respetando la cancelación del contexto.
func (c *Client) sleepBackoff(ctx context.Context, attempt int) error {
	base := c.backoffBase << (attempt - 1)
	//nolint:gosec // G404: jitter de backoff, no es sensible a seguridad.
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
