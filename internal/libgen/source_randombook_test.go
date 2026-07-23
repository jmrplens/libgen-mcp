package libgen

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

// clientToHost returns an http.Client whose transport redirects any dial to
// fakeHost to the given real address, so a test can present a libgen.<tld>-shaped
// mirror hostname (as randombookHostRe requires) while actually talking to a local
// httptest server, which only ever listens on a 127.0.0.1:port address.
func clientToHost(fakeHost, realAddr string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr == fakeHost {
				addr = realAddr
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
}

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

	// Present a libgen.<tld>-shaped hostname (as randombookHostRe now requires of
	// any candidate Resolve attempts) and redirect it to the httptest server.
	const fakeHost = "libgen.test"
	fakeMirror := "http://" + fakeHost
	httpClient := clientToHost(fakeHost+":80", mirror.Listener.Addr().String())

	links := fmt.Sprintf(`{"result":{"list":[%q]},"isError":false}`, fakeMirror)
	apiBase := randombookAPIServer(t, randombookByIDFixture(t), links)

	s := randombookSource{apiBase: apiBase, http: httpClient}
	res, err := s.Resolve(context.Background(), Item{MD5: want})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	wantURL := fakeMirror + "/get.php?md5=" + want + "&key=TESTKEY123"
	if res.FileURL != wantURL {
		t.Errorf("FileURL = %q, want %q", res.FileURL, wantURL)
	}
	if !res.VerifyMD5 {
		t.Error("VerifyMD5 = false, want true (md5-keyed)")
	}

	// Prove the resolved URL is actually downloadable and verifies end to end.
	// The file-streaming path uses c.dl (not c.http), so both need the redirect.
	c := newTestClient(staticMirrors{})
	c.http = httpClient
	c.dl = httpClient
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

// TestRandombookLinksError verifies the links-by-id short-circuit: an API response
// with isError:true yields an error even when it carries a result object, so a
// flagged error is never treated as a hit and the download chain falls through.
func TestRandombookLinksError(t *testing.T) {
	apiBase := randombookAPIServer(t,
		randombookByIDFixture(t),
		`{"result":{"list":["https://libgen.net"]},"isError":true}`)
	s := randombookSource{apiBase: apiBase, http: http.DefaultClient}
	if _, err := s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}); err == nil {
		t.Fatal("Resolve() must fail when the links-by-id API reports isError:true")
	}
}

// TestRandombookByIDNoID verifies that a by-id result object carrying an empty id
// is treated as a layout change (ErrLayoutChanged), not a normal miss.
func TestRandombookByIDNoID(t *testing.T) {
	apiBase := randombookAPIServer(t, `{"result":{"id":""},"isError":false}`, `{"result":{"list":[]},"isError":false}`)
	s := randombookSource{apiBase: apiBase, http: http.DefaultClient}
	_, err := s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"})
	if !errors.Is(err, ErrLayoutChanged) {
		t.Fatalf("err = %v, want ErrLayoutChanged (by-id result with no id)", err)
	}
}

// TestRandombookLinksMissingResult verifies that a links-by-id response with no
// result object is treated as a layout change (ErrLayoutChanged).
func TestRandombookLinksMissingResult(t *testing.T) {
	apiBase := randombookAPIServer(t, randombookByIDFixture(t), `{"isError":false}`)
	s := randombookSource{apiBase: apiBase, http: http.DefaultClient}
	_, err := s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"})
	if !errors.Is(err, ErrLayoutChanged) {
		t.Fatalf("err = %v, want ErrLayoutChanged (links-by-id result missing)", err)
	}
}

// TestRandombookByIDNon200 verifies that a non-200 by-id response is surfaced as
// an error (getJSON's status gate) so the download chain falls through.
func TestRandombookByIDNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	s := randombookSource{apiBase: srv.URL, http: srv.Client()}
	if _, err := s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}); err == nil {
		t.Fatal("Resolve() on a non-200 by-id response should return an error")
	}
}

// TestRandombookByIDBadJSON verifies that a malformed by-id body is wrapped in
// ErrLayoutChanged (the private API may have changed shape).
func TestRandombookByIDBadJSON(t *testing.T) {
	apiBase := randombookAPIServer(t, `{"result": not-json`, `{"result":{"list":[]},"isError":false}`)
	s := randombookSource{apiBase: apiBase, http: http.DefaultClient}
	_, err := s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"})
	if !errors.Is(err, ErrLayoutChanged) {
		t.Fatalf("err = %v, want ErrLayoutChanged (malformed by-id JSON)", err)
	}
}

// TestRandombookMirrorNon200 verifies that a discovered mirror returning a non-200
// status is skipped (resolveViaMirror's status gate), and with no other mirror the
// resolve fails rather than handing back a bogus URL.
func TestRandombookMirrorNon200(t *testing.T) {
	badMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(badMirror.Close)
	links := fmt.Sprintf(`{"result":{"list":[%q]},"isError":false}`, badMirror.URL)
	apiBase := randombookAPIServer(t, randombookByIDFixture(t), links)
	s := randombookSource{apiBase: apiBase, http: http.DefaultClient}
	if _, err := s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}); err == nil {
		t.Fatal("Resolve() should fail when the only discovered mirror returns non-200")
	}
}

// TestRandombookDefaultClientTransportError covers the default-client fallback
// (http is nil) together with getJSON's transport-error branch: a dead API address
// makes the by-id request fail.
func TestRandombookDefaultClientTransportError(t *testing.T) {
	s := randombookSource{apiBase: "http://127.0.0.1:0"} // http nil -> default client
	if _, err := s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}); err == nil {
		t.Error("Resolve should fail when the API request cannot be sent")
	}
}

// TestRandombookRequestBuildError covers getJSON's request-construction failure: a
// base URL carrying a control character cannot be turned into a request.
func TestRandombookRequestBuildError(t *testing.T) {
	s := randombookSource{apiBase: "http://\x7f", http: http.DefaultClient}
	if _, err := s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}); err == nil {
		t.Error("Resolve should fail when the request cannot be built")
	}
}

// TestRandombookEmptyMirrorList covers lookupMirrors' empty-list branch: a valid id
// whose links-by-id response carries no mirrors yields an error.
func TestRandombookEmptyMirrorList(t *testing.T) {
	apiBase := randombookAPIServer(t, randombookByIDFixture(t), `{"result":{"list":[]},"isError":false}`)
	s := randombookSource{apiBase: apiBase, http: http.DefaultClient}
	if _, err := s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}); err == nil {
		t.Error("Resolve should fail when the discovered mirror list is empty")
	}
}

// TestRandombookLinksNon200 covers lookupMirrors' getJSON error branch: a non-200
// links-by-id response (by-id having succeeded) surfaces as an error.
func TestRandombookLinksNon200(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/search/by-id", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(randombookByIDFixture(t)))
	})
	mux.HandleFunc("/api/download/links-by-id", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s := randombookSource{apiBase: srv.URL, http: srv.Client()}
	if _, err := s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}); err == nil {
		t.Error("Resolve should fail when links-by-id returns a non-200")
	}
}

// TestRandombookMirrorRequestBuildError covers resolveViaMirror's
// request-construction failure and normalizeMirrorBase's bare-host branch: a
// scheme-less mirror host with a control character is prefixed with https:// and
// then fails to build a request.
func TestRandombookMirrorRequestBuildError(t *testing.T) {
	// A scheme-less host carrying a DEL (0x7f) control byte: it is prefixed with
	// https:// (the bare-host branch) and then rejected by request construction.
	links := "{\"result\":{\"list\":[\"\x7fbadhost\"]},\"isError\":false}"
	apiBase := randombookAPIServer(t, randombookByIDFixture(t), links)
	s := randombookSource{apiBase: apiBase, http: http.DefaultClient}
	if _, err := s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}); err == nil {
		t.Error("Resolve should fail when a discovered mirror yields an unbuildable request")
	}
}

// TestRandombookMirrorBodyReadError covers resolveViaMirror's body-read failure: a
// mirror that declares more bytes than it sends, then closes, makes reading the
// ads.php body fail.
func TestRandombookMirrorBodyReadError(t *testing.T) {
	badMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\nshort"))
		_ = conn.Close()
	}))
	t.Cleanup(badMirror.Close)
	links := fmt.Sprintf(`{"result":{"list":[%q]},"isError":false}`, badMirror.URL)
	apiBase := randombookAPIServer(t, randombookByIDFixture(t), links)
	s := randombookSource{apiBase: apiBase, http: http.DefaultClient}
	if _, err := s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}); err == nil {
		t.Error("Resolve should fail when a mirror body cannot be fully read")
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

// TestFilterLibgenFamily verifies filterLibgenFamily keeps only bare
// libgen.<tld> hostnames (the shape resolveViaMirror's ads.php scraping
// targets) and drops any candidate outside that shape — in particular the
// annas-archive.* hosts randombook.org has been observed to mix into its
// candidate list, which use an unrelated URL scheme entirely.
func TestFilterLibgenFamily(t *testing.T) {
	in := []string{
		"https://libgen.net",
		"https://libgen.me",
		"https://libgen.xyz",
		"https://annas-archive.gl",
		"not a url",
		"https://evil.example.com",
	}
	got := filterLibgenFamily(in)
	want := []string{"https://libgen.net", "https://libgen.me", "https://libgen.xyz"}
	if len(got) != len(want) {
		t.Fatalf("filterLibgenFamily(%v) = %v, want %v", in, got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("filterLibgenFamily()[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestFilterLibgenFamily_AllFiltered verifies an all-non-family candidate list
// filters down to empty, rather than falling back to trying any of them.
func TestFilterLibgenFamily_AllFiltered(t *testing.T) {
	got := filterLibgenFamily([]string{"https://annas-archive.gl", "https://annas-archive.pk"})
	if len(got) != 0 {
		t.Errorf("filterLibgenFamily() = %v, want empty", got)
	}
}

// TestRandombookSkipsNonFamilyHosts verifies Resolve never attempts a
// non-libgen-family candidate (e.g. annas-archive.gl) at all: with only such
// candidates discovered, it fails fast with the same "no usable mirrors"
// message used for a structurally empty list, and never issues a single request
// to the non-family host — proving the host is filtered out before any network
// call, not merely tried-and-failed.
func TestRandombookSkipsNonFamilyHosts(t *testing.T) {
	var hit atomic.Bool
	nonFamily := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(nonFamily.Close)

	// Present it as a fake annas-archive.gl-shaped candidate via the DialContext
	// redirect trick, so the ONLY way resolveViaMirror could reach it is if the
	// family filter failed to exclude it.
	const fakeHost = "annas-archive.gl"
	httpClient := clientToHost(fakeHost+":80", nonFamily.Listener.Addr().String())

	links := fmt.Sprintf(`{"result":{"list":[%q]},"isError":false}`, "http://"+fakeHost)
	apiBase := randombookAPIServer(t, randombookByIDFixture(t), links)

	s := randombookSource{apiBase: apiBase, http: httpClient}
	if _, err := s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"}); err == nil {
		t.Fatal("Resolve() with only a non-family candidate should fail")
	}
	if hit.Load() {
		t.Error("resolveViaMirror issued a request to a non-family host; it should have been filtered out before any request")
	}
}

// TestRandombookClientRenderedMirror verifies that when a libgen-family mirror
// answers ads.php with a client-rendered application shell (captured live from a
// real migrated mirror, testdata/randombook_spa_shell.html) instead of the
// classic server-rendered ads.php page, resolveViaMirror reports the distinct
// ErrMirrorClientRendered diagnosis rather than the generic ExtractGetLink
// failure — so a site-wide frontend migration is distinguishable in logs/errors
// from an ordinary missing-link parse failure.
func TestRandombookClientRenderedMirror(t *testing.T) {
	shell, err := os.ReadFile("testdata/randombook_spa_shell.html")
	if err != nil {
		t.Fatalf("reading SPA shell fixture: %v", err)
	}
	// The mirror serves no download API, so Resolve falls back to the ads.php
	// chain — the path whose diagnosis this test pins.
	spaMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/download" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(shell)
	}))
	t.Cleanup(spaMirror.Close)

	const fakeHost = "libgen.test"
	httpClient := clientToHost(fakeHost+":80", spaMirror.Listener.Addr().String())
	links := fmt.Sprintf(`{"result":{"list":[%q]},"isError":false}`, "http://"+fakeHost)
	apiBase := randombookAPIServer(t, randombookByIDFixture(t), links)

	s := randombookSource{apiBase: apiBase, http: httpClient}
	_, err = s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"})
	if !errors.Is(err, ErrMirrorClientRendered) {
		t.Fatalf("err = %v, want wrapping ErrMirrorClientRendered (client-rendered SPA shell)", err)
	}
}

// TestRandombookOrdinaryMissingLink verifies that a libgen-family mirror serving
// an ordinary (non-SPA) page with no get.php link still reports the generic
// ExtractGetLink failure, not ErrMirrorClientRendered — the two diagnoses must
// not be conflated.
func TestRandombookOrdinaryMissingLink(t *testing.T) {
	// The mirror serves no download API, so Resolve falls back to the ads.php
	// chain — the path whose diagnosis this test pins.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/download" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("<html><body>no download link here</body></html>"))
	}))
	t.Cleanup(plain.Close)

	const fakeHost = "libgen.test"
	httpClient := clientToHost(fakeHost+":80", plain.Listener.Addr().String())
	links := fmt.Sprintf(`{"result":{"list":[%q]},"isError":false}`, "http://"+fakeHost)
	apiBase := randombookAPIServer(t, randombookByIDFixture(t), links)

	s := randombookSource{apiBase: apiBase, http: httpClient}
	_, err := s.Resolve(context.Background(), Item{MD5: "87a4ebdaf21fa6cc70009a3dd63194ee"})
	if errors.Is(err, ErrMirrorClientRendered) {
		t.Fatal("an ordinary missing-link page must not be misdiagnosed as a client-rendered shell")
	}
	if err == nil {
		t.Fatal("Resolve() with no get.php link should fail")
	}
}

// TestRandombookRealCapturedCandidates guards against a regression in the
// real-world candidate mix: the captured links-by-id fixture carries the exact
// hosts randombook.org has been observed returning (three libgen.* SPA-migrated
// hosts plus one annas-archive.gl entry). filterLibgenFamily must keep exactly
// the three libgen.* hosts, so Resolve knows which are even worth attempting.
func TestRandombookRealCapturedCandidates(t *testing.T) {
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
	got := filterLibgenFamily(mirrors)
	want := []string{"https://libgen.net", "https://libgen.me", "https://libgen.xyz"}
	if len(got) != len(want) {
		t.Fatalf("filterLibgenFamily(lookupMirrors()) = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("filtered mirrors[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestRandombookDownloadAPISkipsDeadMirror verifies the real-world candidate mix
// observed on 2026-07-24, where randombook offers three libgen.<tld> hosts and
// only some are reachable: an unreachable mirror must be skipped in favor of the
// next one rather than failing the whole source.
func TestRandombookDownloadAPISkipsDeadMirror(t *testing.T) {
	payload := []byte("%PDF-1.5 second-mirror payload")
	want := md5Hex(payload)

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/download" || r.URL.Query().Get("id") != "123" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer live.Close()

	// A closed listener's address stands in for a mirror that is down: dialing it
	// fails at the transport level, exactly as libgen.me did when observed.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadAddr := dead.Listener.Addr().String()
	dead.Close()

	const deadHost, liveHost = "libgen.dead", "libgen.live"
	httpClient := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			switch addr {
			case deadHost + ":80":
				addr = deadAddr
			case liveHost + ":80":
				addr = live.Listener.Addr().String()
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}

	links := fmt.Sprintf(`{"result":{"list":["http://%s","http://%s"]},"isError":false}`, deadHost, liveHost)
	apiBase := randombookAPIServer(t, randombookByIDFixture(t), links)

	s := randombookSource{apiBase: apiBase, http: httpClient}
	res, err := s.Resolve(context.Background(), Item{MD5: want})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if wantURL := "http://" + liveHost + "/api/download?id=123"; res.FileURL != wantURL {
		t.Fatalf("FileURL = %q, want the live mirror %q", res.FileURL, wantURL)
	}
}

// TestRandombookResolvesViaDownloadAPI verifies the source resolves an item
// through the mirror's own /api/download?id=<numeric id> endpoint — the
// mechanism the site itself uses (verified live 2026-07-24) — instead of
// scraping ads.php, which the current libgen.<tld> candidates no longer serve.
// A HEAD probe selects a live mirror, and the resolved URL is streamed end to
// end to prove it serves the file and passes md5 verification.
func TestRandombookResolvesViaDownloadAPI(t *testing.T) {
	payload := []byte("%PDF-1.5 randombook download-api payload")
	want := md5Hex(payload)

	var headSeen atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/download", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "123" {
			http.NotFound(w, r)
			return
		}
		// The live endpoint is GET-only; a HEAD reaches it through the mirror's
		// 302 and comes back 405, which still proves the mirror is alive.
		if r.Method == http.MethodHead {
			headSeen.Store(true)
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="book.pdf"`)
		_, _ = w.Write(payload)
	})
	mirror := httptest.NewServer(mux)
	defer mirror.Close()

	const fakeHost = "libgen.test"
	fakeMirror := "http://" + fakeHost
	httpClient := clientToHost(fakeHost+":80", mirror.Listener.Addr().String())

	links := fmt.Sprintf(`{"result":{"list":[%q]},"isError":false}`, fakeMirror)
	apiBase := randombookAPIServer(t, randombookByIDFixture(t), links)

	s := randombookSource{apiBase: apiBase, http: httpClient}
	res, err := s.Resolve(context.Background(), Item{MD5: want})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	wantURL := fakeMirror + "/api/download?id=123"
	if res.FileURL != wantURL {
		t.Fatalf("FileURL = %q, want %q", res.FileURL, wantURL)
	}
	if !res.VerifyMD5 {
		t.Error("VerifyMD5 = false, want true (md5-keyed)")
	}
	if !headSeen.Load() {
		t.Error("no HEAD probe was issued to select a live mirror")
	}

	// The file-streaming path uses c.dl (not c.http), so both need the redirect.
	c := newTestClient(staticMirrors{})
	c.http = httpClient
	c.dl = httpClient
	c.sources = []DownloadSource{s}
	dl, err := c.Download(context.Background(), want, t.TempDir(), "", nil)
	if err != nil {
		t.Fatalf("Download() via randombook error = %v", err)
	}
	if !dl.Verified {
		t.Error("Verified = false, want true")
	}
}
