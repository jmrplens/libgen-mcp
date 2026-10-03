package openalex

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/jmrplens/libgen-mcp/v2/internal/config"
	"github.com/jmrplens/libgen-mcp/v2/internal/version"
)

// APIBase is the root of the OpenAlex REST API. Callers keep their own copy in a
// variable a test can point at an httptest server; this is the value it starts as.
const APIBase = "https://api.openalex.org"

// NewRequest builds a GET for an OpenAlex endpoint carrying the server's
// User-Agent, a JSON Accept header and, when key is not blank, the API key.
//
// The key rides in the Authorization header as a bearer token, which OpenAlex
// documents as equivalent to its api_key query parameter. The header is chosen
// so the secret is never part of a URL: net/http prints the whole request URL in
// a transport error, and an error here reaches the operator's log and, through
// the download tool, the model's transcript.
//
// A request built here with no key is keyless and draws on the per-address
// budget; with one it draws on the key's, ten times larger.
func NewRequest(ctx context.Context, rawURL, key string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Accept", "application/json")
	if k := strings.TrimSpace(key); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	return req, nil
}

// keyRejectedOnce guards the one warning a rejected key earns per process.
var keyRejectedOnce sync.Once

// NoteKeyRejected warns, once per process, when OpenAlex refused a request sent
// with a key. A key OpenAlex does not accept (mistyped, rotated, revoked) is
// answered 401 or 403 on every request, which silently empties the search
// provider and takes the download source out of every DOI it would have served,
// so the operator is told which variable to fix. The warning names the variable,
// never the key.
func NoteKeyRejected(status int, key string) {
	if strings.TrimSpace(key) == "" || (status != http.StatusUnauthorized && status != http.StatusForbidden) {
		return
	}
	keyRejectedOnce.Do(func() {
		slog.Warn("OpenAlex rejected the configured API key, so OpenAlex search and the openalex download source fail until it is fixed or unset",
			"variable", config.EnvName("OPENALEX_KEY"), "status", status)
	})
}
