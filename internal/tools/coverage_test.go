package tools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
)

// TestResultIdentifier covers the doi and empty arms of resultIdentifier that the
// md5-keyed search fixtures never reach.
func TestResultIdentifier(t *testing.T) {
	if got := resultIdentifier(libgen.Result{MD5: "abc"}); got != "md5:abc" {
		t.Errorf("md5 identifier = %q, want %q", got, "md5:abc")
	}
	if got := resultIdentifier(libgen.Result{DOI: "10.1/x"}); got != "doi:10.1/x" {
		t.Errorf("doi identifier = %q, want %q", got, "doi:10.1/x")
	}
	if got := resultIdentifier(libgen.Result{}); got != "" {
		t.Errorf("empty identifier = %q, want empty", got)
	}
}

// TestResultLinks covers the skip-empty-URL and default-label arms of resultLinks.
func TestResultLinks(t *testing.T) {
	// An entry with no URL is skipped; an entry with no label renders as "download".
	r := libgen.Result{Downloads: []libgen.DownloadOption{
		{Label: "GET", URL: ""},            // skipped: empty URL
		{Label: "", URL: "https://m/dl/2"}, // default label "download"
	}}
	if got := resultLinks(r); got != "[download](https://m/dl/2)" {
		t.Errorf("resultLinks = %q, want %q", got, "[download](https://m/dl/2)")
	}
	// No downloads at all → empty string.
	if got := resultLinks(libgen.Result{}); got != "" {
		t.Errorf("resultLinks(no downloads) = %q, want empty", got)
	}
}

// TestDownloadInputSchemaEmptyEnabled covers the branch where no sources are
// enabled: the schema is returned unconstrained (no enum) rather than restricted.
func TestDownloadInputSchemaEmptyEnabled(t *testing.T) {
	schema := downloadInputSchema(nil)
	if schema == nil {
		t.Fatal("downloadInputSchema(nil) returned nil")
	}
	if src := schema.Properties["source"]; src != nil && len(src.Enum) != 0 {
		t.Errorf("empty enabled should leave source enum unset; got %v", src.Enum)
	}
}

// TestDownloadInputSchemaInferenceError covers the defensive guard that returns a
// nil schema when jsonschema inference fails. Real inference of the static
// DownloadInput struct never errors, so the seam is overridden to force the path.
func TestDownloadInputSchemaInferenceError(t *testing.T) {
	orig := downloadSchemaFor
	t.Cleanup(func() { downloadSchemaFor = orig })
	downloadSchemaFor = func(*jsonschema.ForOptions) (*jsonschema.Schema, error) {
		return nil, errors.New("inference failed")
	}
	if got := downloadInputSchema([]string{"libgen"}); got != nil {
		t.Errorf("schema inference error should yield a nil schema; got %v", got)
	}
}

// TestValidateDownloadInputUnknownSource covers the unknown-source rejection arm
// of validateDownloadInput. This branch is unreachable through the registered tool
// (the input schema's source enum rejects unknown values before the handler runs),
// so it is exercised directly.
func TestValidateDownloadInputUnknownSource(t *testing.T) {
	_, _, _, err := validateDownloadInput(DownloadInput{
		MD5:    "87a4ebdaf21fa6cc70009a3dd63194ee",
		Source: "definitelynotasource",
	})
	if err == nil {
		t.Fatal("an unknown source should be rejected")
	}
	if !strings.Contains(err.Error(), "definitelynotasource") {
		t.Errorf("error should name the bad source; got %v", err)
	}
}

// emptyJSONClient builds a libgen client whose json.php always returns an empty
// object, so DetailsByMD5/DetailsByID surface their "no record found" errors.
func emptyJSONClient(t *testing.T) *libgen.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/json.php", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	return libgen.New(staticMirrors{srv.URL}, cfg)
}

// TestDetailsByMD5LookupError covers detailsByMD5's error return when the client's
// lookup fails (valid md5 syntax, but no record).
func TestDetailsByMD5LookupError(t *testing.T) {
	client := emptyJSONClient(t)
	if _, err := detailsByMD5(context.Background(), client, "87a4ebdaf21fa6cc70009a3dd63194ee"); err == nil {
		t.Fatal("detailsByMD5 should surface the lookup error when no record is found")
	}
}

// TestDetailsByIDLookupError covers detailsByID's error return when the client's
// lookup fails, for both the edition (default) and file objects.
func TestDetailsByIDLookupError(t *testing.T) {
	client := emptyJSONClient(t)
	if _, err := detailsByID(context.Background(), client, "", "138281637"); err == nil {
		t.Fatal("detailsByID (edition) should surface the lookup error")
	}
	if _, err := detailsByID(context.Background(), client, "file", "93485370"); err == nil {
		t.Fatal("detailsByID (file) should surface the lookup error")
	}
}

// TestHeaderMapAllEmptyValues covers headerMap's post-filter nil return: a header
// whose only entries have empty values flattens to no usable keys.
func TestHeaderMapAllEmptyValues(t *testing.T) {
	if got := headerMap(http.Header{"Empty": {""}, "AlsoEmpty": {""}}); got != nil {
		t.Errorf("headerMap with only empty values should return nil; got %v", got)
	}
}

// TestDownloadResolveOnlyResolveError covers resolveDownload's error path: on the
// resolve_only route, a source that fails to resolve surfaces as a tool error.
func TestDownloadResolveOnlyResolveError(t *testing.T) {
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	session := newDownloadSession(t, cfg, staticMirrors{}, libgen.WithSources(md5ErrSource{}))
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "download",
		Arguments: map[string]any{"md5": "87a4ebdaf21fa6cc70009a3dd63194ee", "resolve_only": true},
	})
	if err != nil {
		t.Fatalf("CallTool(download) transport error = %v", err)
	}
	if !res.IsError {
		t.Fatal("resolve_only whose only source fails to resolve should be a tool error")
	}
}
