package libgen

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubSource is a test DownloadSource whose behavior (support decision, resolve
// outcome) is fully controlled by its fields, letting tests assemble arbitrary
// source chains without any network resolution.
type stubSource struct {
	name       string
	supports   bool
	resolveErr error
	resolved   Resolved
}

func (s stubSource) Name() string       { return s.name }
func (s stubSource) Supports(Item) bool { return s.supports }
func (s stubSource) Resolve(context.Context, Item) (Resolved, error) {
	if s.resolveErr != nil {
		return Resolved{}, s.resolveErr
	}
	return s.resolved, nil
}

// fileCDN builds a bare httptest server that serves payload as an octet-stream at
// /file, with the given Content-Disposition (empty to omit it). Unlike
// md5CDNServer it has no ads.php/get.php: sources resolve straight to its /file.
func fileCDN(t *testing.T, payload []byte, disposition string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/file", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		if disposition != "" {
			w.Header().Set("Content-Disposition", disposition)
		}
		_, _ = w.Write(payload)
	})
	return httptest.NewServer(mux)
}

// TestDownloadSourceChainFallback verifies the source chain advances past a source
// whose Resolve fails and completes via the next source, tagging the result with
// the serving source's Name().
func TestDownloadSourceChainFallback(t *testing.T) {
	payload := []byte("%PDF-1.4 chain fallback payload")
	want := md5Hex(payload)
	cdn := fileCDN(t, payload, `attachment; filename="fb.pdf"`)
	defer cdn.Close()

	c := newTestClient(staticMirrors{})
	bad := stubSource{name: "bad", supports: true, resolveErr: errors.New("resolve boom")}
	good := stubSource{name: "good", supports: true, resolved: Resolved{FileURL: cdn.URL + "/file", VerifyMD5: true}}
	c.sources = []DownloadSource{bad, good}

	dir := t.TempDir()
	res, err := c.Download(context.Background(), want, dir, "", nil)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if res.Source != "good" {
		t.Errorf("Source = %q, want %q", res.Source, "good")
	}
	if !res.Verified {
		t.Error("Verified = false, want true")
	}
}

// TestVerifyMD5Conditional verifies that MD5 verification is gated by
// Resolved.VerifyMD5: a mismatch is tolerated when false (verification skipped)
// and rejected when true.
func TestVerifyMD5Conditional(t *testing.T) {
	payload := []byte("%PDF-1.4 conditional verify payload")
	wrongMD5 := md5Hex([]byte("some other content entirely"))
	if wrongMD5 == md5Hex(payload) {
		t.Fatal("test setup: md5s unexpectedly collide")
	}

	t.Run("skip verification", func(t *testing.T) {
		cdn := fileCDN(t, payload, `attachment; filename="nv.pdf"`)
		defer cdn.Close()
		c := newTestClient(staticMirrors{})
		c.sources = []DownloadSource{stubSource{name: "noverify", supports: true, resolved: Resolved{FileURL: cdn.URL + "/file", VerifyMD5: false}}}
		res, err := c.Download(context.Background(), wrongMD5, t.TempDir(), "", nil)
		if err != nil {
			t.Fatalf("Download() error = %v, want nil (verification skipped)", err)
		}
		if res.Verified {
			t.Error("Verified = true, want false (verification was skipped)")
		}
	})

	t.Run("enforce verification", func(t *testing.T) {
		cdn := fileCDN(t, payload, `attachment; filename="v.pdf"`)
		defer cdn.Close()
		c := newTestClient(staticMirrors{})
		c.sources = []DownloadSource{stubSource{name: "verify", supports: true, resolved: Resolved{FileURL: cdn.URL + "/file", VerifyMD5: true}}}
		if _, err := c.Download(context.Background(), wrongMD5, t.TempDir(), "", nil); err == nil {
			t.Fatal("Download() error = nil, want a verification failure (md5 mismatch)")
		}
	})
}
