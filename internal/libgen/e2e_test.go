//go:build e2e

package libgen

import (
	"context"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/mirrors"
)

// TestE2ESearchRealSite validates against the real network that the site's HTML is
// still parseable. Run with: go test -tags e2e ./internal/libgen/ -run E2E -v
func TestE2ESearchRealSite(t *testing.T) {
	cfg := &config.Config{Timeout: 30 * time.Second, RateRPS: 1, RateBurst: 1, RetryAttempts: 3}
	mgr, err := mirrors.NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := New(mgr, cfg)
	page, mirror, err := c.Search(context.Background(), SearchParams{Query: "golang", Topics: []string{"nonfiction"}})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	t.Logf("mirror=%s results=%d total=%s", mirror, len(page.Results), page.TotalFiles)
	if len(page.Results) == 0 {
		t.Fatal("0 results on the real site: HTML changed or blocked")
	}
}
