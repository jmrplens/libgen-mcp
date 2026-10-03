package openalex

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// TestNewRequest_SendsTheKeyAsABearerTokenAndNeverInTheURL pins where the key
// travels: in the Authorization header, with the URL left exactly as given, and
// not at all when the key is blank.
func TestNewRequest_SendsTheKeyAsABearerTokenAndNeverInTheURL(t *testing.T) {
	const rawURL = "https://api.openalex.org/works?search=dna"
	cases := []struct {
		name string
		key  string
		want string
	}{
		{name: "a key", key: "s3cret", want: "Bearer s3cret"},
		{name: "a key with surrounding space", key: "  s3cret \n", want: "Bearer s3cret"},
		{name: "no key", key: "", want: ""},
		{name: "a blank key", key: "   ", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := NewRequest(context.Background(), rawURL, tc.key)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			if got := req.Header.Get("Authorization"); got != tc.want {
				t.Errorf("Authorization = %q, want %q", got, tc.want)
			}
			if got := req.URL.String(); got != rawURL {
				t.Errorf("URL = %q, want it unchanged as %q", got, rawURL)
			}
			if req.Header.Get("User-Agent") == "" || req.Header.Get("Accept") != "application/json" {
				t.Errorf("headers = %v, want a User-Agent and a JSON Accept", req.Header)
			}
		})
	}
}

// TestNoteKeyRejected_WarnsOnceNamingTheVariable checks a refused key is reported
// once per process, names the variable and never the key, and that a refusal
// without a key, or a status that is not a refusal, says nothing.
func TestNoteKeyRejected_WarnsOnceNamingTheVariable(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old); keyRejectedOnce = sync.Once{} })
	keyRejectedOnce = sync.Once{}

	NoteKeyRejected(http.StatusUnauthorized, "")
	NoteKeyRejected(http.StatusTooManyRequests, "oa-secret")
	if buf.Len() != 0 {
		t.Fatalf("warned without a rejected key: %s", buf.String())
	}
	NoteKeyRejected(http.StatusUnauthorized, "oa-secret")
	NoteKeyRejected(http.StatusForbidden, "oa-secret")
	out := buf.String()
	if strings.Count(out, "rejected the configured API key") != 1 {
		t.Errorf("want exactly one warning, got: %s", out)
	}
	if !strings.Contains(out, "LIBGEN_MCP_OPENALEX_KEY") || strings.Contains(out, "oa-secret") {
		t.Errorf("warning must name the variable and not the key: %s", out)
	}
}

// TestNewRequest_RefusesAMalformedURL checks a URL net/http cannot parse is an
// error rather than a request.
func TestNewRequest_RefusesAMalformedURL(t *testing.T) {
	if _, err := NewRequest(context.Background(), "http://[::1", ""); err == nil {
		t.Fatal("NewRequest accepted a malformed URL")
	}
}
