package openalex

import (
	"context"
	"net/http"
	"strings"

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
