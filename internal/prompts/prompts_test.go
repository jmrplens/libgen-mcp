package prompts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/libgen"
)

type staticMirrors []string

func (s staticMirrors) Mirrors(context.Context) []string { return s }

// newFixtureClient builds a libgen client backed by an httptest mirror that
// serves the libgen package's search fixture, mirroring the internal/tools
// test setup.
func newFixtureClient(t *testing.T) *libgen.Client {
	t.Helper()
	searchHTML, err := os.ReadFile("../libgen/testdata/search_books.html")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(searchHTML) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := &config.Config{DownloadDir: t.TempDir(), Timeout: 5 * time.Second, RateRPS: 1000, RateBurst: 100, RetryAttempts: 1}
	return libgen.New(staticMirrors{srv.URL}, cfg)
}

func TestAcquireBook_RequiresTitle(t *testing.T) {
	client := newFixtureClient(t)
	_, err := handleAcquireBook(context.Background(), client, &mcp.GetPromptRequest{
		Params: &mcp.GetPromptParams{Arguments: map[string]string{}},
	})
	if err == nil {
		t.Fatal("expected error when title is missing")
	}
}

func TestAcquireBook_ReturnsUserInstruction(t *testing.T) {
	client := newFixtureClient(t)
	res, err := handleAcquireBook(context.Background(), client, &mcp.GetPromptRequest{
		Params: &mcp.GetPromptParams{Arguments: map[string]string{"title": "linux"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Role != "user" {
		t.Fatalf("want one user-role message, got %+v", res.Messages)
	}
	txt := res.Messages[0].Content.(*mcp.TextContent).Text
	if !strings.Contains(txt, "Next actions") {
		t.Errorf("missing Next actions block:\n%s", txt)
	}
}
