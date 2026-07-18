// Package libgen implementa el cliente HTTP contra la familia de mirrors libgen.li:
// búsqueda (HTML), detalles (json.php) y descarga (ads.php → get.php → CDN).
package libgen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/time/rate"
)

const (
	userAgent   = "libgen-mcp/0.1.0 (+https://github.com/jmrplens/libgen-mcp)"
	maxBodySize = 20 << 20 // 20 MiB para páginas HTML/JSON (no descargas)
)

// ErrAllMirrorsFailed indica que ningún mirror respondió correctamente.
var ErrAllMirrorsFailed = errors.New("all libgen mirrors unreachable (network block? try a VPN or different DNS)")

// MirrorLister provides candidate base URLs, preferred first.
type MirrorLister interface {
	// Mirrors returns candidate base URLs, preferred first.
	Mirrors(ctx context.Context) []string
}

type Client struct {
	mirrors MirrorLister
	http    *http.Client // páginas: con timeout
	dl      *http.Client // descargas en streaming: sin timeout global, gobierna ctx
	limiter *rate.Limiter
}

func New(m MirrorLister, timeout time.Duration) *Client {
	return &Client{
		mirrors: m,
		http:    &http.Client{Timeout: timeout},
		dl:      &http.Client{},
		limiter: rate.NewLimiter(rate.Every(time.Second), 1),
	}
}

// get prueba path?q en cada mirror hasta obtener un 200. Devuelve el cuerpo y
// la URL base del mirror que respondió.
func (c *Client) get(ctx context.Context, path string, q url.Values) (content []byte, baseURL string, resErr error) {
	var errs []error
	for _, base := range c.mirrors.Mirrors(ctx) {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, "", err
		}
		u := base + path
		if len(q) > 0 {
			u += "?" + q.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := c.http.Do(req)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", base, err))
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			errs = append(errs, fmt.Errorf("%s: status %d", base, resp.StatusCode))
			continue
		}
		if readErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", base, readErr))
			continue
		}
		return body, base, nil
	}
	return nil, "", fmt.Errorf("%w: %w", ErrAllMirrorsFailed, errors.Join(errs...))
}
