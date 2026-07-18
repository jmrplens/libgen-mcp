package libgen

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// randombookByIDFixture loads the captured randombook by-id JSON response.
func randombookByIDFixture(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("testdata/randombook_byid.json")
	if err != nil {
		t.Fatalf("reading by-id fixture: %v", err)
	}
	return string(body)
}

// randombookAPIServer starts an httptest server standing in for the randombook.org
// HTTP API: /api/search/by-id returns byID and /api/download/links-by-id returns
// links. It returns the server's base URL for use as randombookSource.apiBase.
func randombookAPIServer(t *testing.T, byID, links string) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/search/by-id", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(byID))
	})
	mux.HandleFunc("/api/download/links-by-id", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(links))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestRandombookSupports verifies the source claims md5-keyed items only and names
// itself "randombook".
func TestRandombookSupports(t *testing.T) {
	s := randombookSource{}
	if s.Supports(Item{MD5: ""}) {
		t.Error("Supports(empty MD5) = true, want false")
	}
	if !s.Supports(Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}) {
		t.Error("Supports(non-empty MD5) = false, want true")
	}
	if s.Name() != "randombook" {
		t.Errorf("Name() = %q, want %q", s.Name(), "randombook")
	}
}

// TestRandombookResolvesViaMirror verifies the full discovery flow: by-id yields a
// numeric id, links-by-id yields a mirror list, and the first discovered mirror
// (serving ads.php → get.php → CDN) resolves to a get.php URL with MD5
// verification requested. The resolved URL is then fetched end to end through the
// download pipeline to prove it serves the file and passes integrity.
func TestRandombookResolvesViaMirror(t *testing.T) {
	payload := []byte("%PDF-1.4 randombook fresh-mirror payload")
	want := md5Hex(payload)
	mirror := md5CDNServer(t, want, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="book.pdf"`)
		_, _ = w.Write(payload)
	})
	defer mirror.Close()

	links := fmt.Sprintf(`{"result":{"list":[%q]},"isError":false}`, mirror.URL)
	apiBase := randombookAPIServer(t, randombookByIDFixture(t), links)

	s := randombookSource{apiBase: apiBase, http: http.DefaultClient}
	res, err := s.Resolve(context.Background(), Item{MD5: want})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	wantURL := mirror.URL + "/get.php?md5=" + want + "&key=TESTKEY123"
	if res.FileURL != wantURL {
		t.Errorf("FileURL = %q, want %q", res.FileURL, wantURL)
	}
	if !res.VerifyMD5 {
		t.Error("VerifyMD5 = false, want true (md5-keyed)")
	}

	// Prove the resolved URL is actually downloadable and verifies end to end.
	c := newTestClient(staticMirrors{})
	c.sources = []DownloadSource{s}
	dl, err := c.Download(context.Background(), want, t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("Download() via randombook error = %v", err)
	}
	if dl.Source != "randombook" {
		t.Errorf("Source = %q, want %q", dl.Source, "randombook")
	}
	if !dl.Verified {
		t.Error("Verified = false, want true")
	}
}

// TestRandombookNotIndexed verifies that a null by-id result (md5 unknown to
// randombook) yields an error so the download chain falls through.
func TestRandombookNotIndexed(t *testing.T) {
	apiBase := randombookAPIServer(t, `{"result":null,"isError":false}`, `{"result":{"list":[]},"isError":false}`)
	s := randombookSource{apiBase: apiBase, http: http.DefaultClient}
	if _, err := s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}); err == nil {
		t.Fatal("Resolve() for a non-indexed md5 should return an error")
	}
}

// TestRandombookNoWorkingMirror verifies that when mirrors are discovered but none
// serves a usable get.php key, Resolve returns an error (fall-through) rather than
// a bogus URL.
func TestRandombookNoWorkingMirror(t *testing.T) {
	// A mirror whose ads.php carries no get.php link: ExtractGetLink fails on it.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body>no download link here</body></html>"))
	}))
	defer dead.Close()

	links := fmt.Sprintf(`{"result":{"list":[%q]},"isError":false}`, dead.URL)
	apiBase := randombookAPIServer(t, randombookByIDFixture(t), links)

	s := randombookSource{apiBase: apiBase, http: http.DefaultClient}
	if _, err := s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}); err == nil {
		t.Fatal("Resolve() with no working mirror should return an error")
	}
}

// TestRandombookByIDError verifies the by-id short-circuit: an API response with
// isError:true yields an error even when it carries a result object, so a flagged
// error is never treated as a hit.
func TestRandombookByIDError(t *testing.T) {
	apiBase := randombookAPIServer(t,
		`{"result":{"id":"123"},"isError":true}`,
		`{"result":{"list":[]},"isError":false}`)
	s := randombookSource{apiBase: apiBase, http: http.DefaultClient}
	if _, err := s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}); err == nil {
		t.Fatal("Resolve() must fail when the by-id API reports isError:true")
	}
}

// TestRandombookParsesLinksFixture guards the links response parsing against the
// captured fixture shape: the mirror hostname list is decoded from result.list.
func TestRandombookParsesLinksFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/randombook_links.json")
	if err != nil {
		t.Fatalf("reading links fixture: %v", err)
	}
	apiBase := randombookAPIServer(t, randombookByIDFixture(t), string(body))
	s := randombookSource{apiBase: apiBase, http: http.DefaultClient}
	mirrors, err := s.lookupMirrors(context.Background(), "123")
	if err != nil {
		t.Fatalf("lookupMirrors() error = %v", err)
	}
	if len(mirrors) == 0 || mirrors[0] != "https://libgen.net" {
		t.Errorf("mirrors = %v, want first == %q", mirrors, "https://libgen.net")
	}
}
