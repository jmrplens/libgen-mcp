package libgen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveStaticBytes returns an httptest server that serves content at any path.
func serveStaticBytes(t *testing.T, content []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(content)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestDownloadItemRestrictToSource verifies that Item.Source restricts the
// download to a single named source instead of walking the whole chain, and
// that an unknown/disabled source name is a clear error.
func TestDownloadItemRestrictToSource(t *testing.T) {
	srvA := serveStaticBytes(t, []byte("%PDF-1.4 AAAA "+strings.Repeat("a", 600)))
	srvB := serveStaticBytes(t, []byte("%PDF-1.7 BBBB "+strings.Repeat("b", 600)))

	newClient := func() *Client {
		c := newTestClient(staticMirrors{})
		c.sources = []DownloadSource{
			stubSource{name: "srca", supports: true, resolved: Resolved{FileURL: srvA.URL + "/f", VerifyMD5: false}},
			stubSource{name: "srcb", supports: true, resolved: Resolved{FileURL: srvB.URL + "/f", VerifyMD5: false}},
		}
		return c
	}

	// Restrict to srcb even though srca is first and would serve on its own.
	res, err := newClient().DownloadItem(context.Background(), Item{DOI: "10.1/x", Source: "srcb"}, t.TempDir(), "o.pdf")
	if err != nil {
		t.Fatalf("restrict to srcb: %v", err)
	}
	if res.Source != "srcb" {
		t.Errorf("Source = %q, want srcb", res.Source)
	}

	// Restrict to srca.
	res, err = newClient().DownloadItem(context.Background(), Item{DOI: "10.1/x", Source: "srca"}, t.TempDir(), "o.pdf")
	if err != nil {
		t.Fatalf("restrict to srca: %v", err)
	}
	if res.Source != "srca" {
		t.Errorf("Source = %q, want srca", res.Source)
	}

	// An unknown / disabled source name is a clear error, not a silent fallback.
	_, uerr := newClient().DownloadItem(context.Background(), Item{DOI: "10.1/x", Source: "nope"}, t.TempDir(), "o.pdf")
	if uerr == nil || !strings.Contains(uerr.Error(), "not enabled") {
		t.Errorf("unknown source error = %v, want it to mention 'not enabled'", uerr)
	}
}

// TestDownloadItemRestrictIncompatibleSource verifies that restricting to a
// source that does not support the item type returns a targeted error rather
// than the generic "no source supports" message.
func TestDownloadItemRestrictIncompatibleSource(t *testing.T) {
	c := newTestClient(staticMirrors{})
	c.sources = []DownloadSource{stubSource{name: "onlydoi", supports: false}}
	_, err := c.DownloadItem(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee", Source: "onlydoi"}, t.TempDir(), "")
	if err == nil || !strings.Contains(err.Error(), "cannot serve") {
		t.Errorf("incompatible source error = %v, want it to mention 'cannot serve'", err)
	}
}
