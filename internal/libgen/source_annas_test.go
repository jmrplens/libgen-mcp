package libgen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// annasBookPage renders an Anna's Archive book page embedding the IPFS CIDs the
// way the live site does.
func annasBookPage(cidV0, cidV1 string) string {
	return `<html><body>` +
		`<a href="ipfs://` + cidV0 + `">ipfs_cid:` + cidV0 + `</a>` +
		`<a href="ipfs://` + cidV1 + `">ipfs_cid:` + cidV1 + `</a>` +
		`</body></html>`
}

// gatewayServing returns a server standing in for a public IPFS gateway that
// answers a Range probe with the given status.
func gatewayServing(status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.WriteHeader(status)
	}))
}

// TestAnnasSupports verifies the source claims md5-keyed items only and names
// itself "annas".
func TestAnnasSupports(t *testing.T) {
	s := annasSource{}
	if !s.Supports(Item{MD5: "d64efd386ed7227592499460aca2044b"}) {
		t.Error("Supports(md5) = false, want true")
	}
	if s.Supports(Item{DOI: "10.1/x"}) {
		t.Error("Supports(DOI-only) = true, want false")
	}
	if s.Name() != "annas" {
		t.Errorf("Name() = %q, want %q", s.Name(), "annas")
	}
}

// TestAnnasResolveUsesIPFSGateway verifies the keyless default path: the md5 page
// yields an IPFS CID and the first gateway that serves it wins, with MD5
// verification on (the item is md5-keyed).
func TestAnnasResolveUsesIPFSGateway(t *testing.T) {
	const cidV1 = "bafykbzaceb75yjslp3fcwdbkgoqlymevekymecpqxyzpbwgoh2cmewc6k2wc4"
	const md5 = "d64efd386ed7227592499460aca2044b"

	gw := gatewayServing(http.StatusPartialContent)
	defer gw.Close()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/md5/"+md5 {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(annasBookPage("QmeFQDjVrNKRrc94a3iMjzsGWKEQff9Wq1mXxdCrdnwvRU", cidV1)))
	}))
	defer site.Close()

	s := annasSource{
		mirrors:  staticMirrors{site.URL},
		http:     site.Client(),
		gateways: []string{gw.URL + "/ipfs/"},
	}
	got, err := s.Resolve(context.Background(), Item{MD5: md5})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := gw.URL + "/ipfs/" + cidV1; got.FileURL != want {
		t.Errorf("FileURL = %q, want %q", got.FileURL, want)
	}
	if !got.VerifyMD5 {
		t.Error("VerifyMD5 = false, want true (md5-keyed)")
	}
}

// TestAnnasResolveSkipsDeadGateway verifies a gateway failing the probe is
// skipped in favor of the next, since public gateway availability varies.
func TestAnnasResolveSkipsDeadGateway(t *testing.T) {
	const cidV1 = "bafyworkingcidzzzz234567"

	dead := gatewayServing(http.StatusInternalServerError)
	defer dead.Close()
	live := gatewayServing(http.StatusOK)
	defer live.Close()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(annasBookPage("QmV0", cidV1)))
	}))
	defer site.Close()

	s := annasSource{
		mirrors:  staticMirrors{site.URL},
		http:     site.Client(),
		gateways: []string{dead.URL + "/ipfs/", live.URL + "/ipfs/"},
	}
	got, err := s.Resolve(context.Background(), Item{MD5: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !strings.HasPrefix(got.FileURL, live.URL) {
		t.Errorf("FileURL = %q, want the live gateway %q", got.FileURL, live.URL)
	}
}

// TestAnnasResolveNoCIDErrors verifies a page with no CID yields an error so the
// chain advances, rather than an empty Resolved.
func TestAnnasResolveNoCIDErrors(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body>no cid</body></html>`))
	}))
	defer site.Close()

	s := annasSource{mirrors: staticMirrors{site.URL}, http: site.Client()}
	if _, err := s.Resolve(context.Background(), Item{MD5: "abc"}); err == nil {
		t.Fatal("Resolve() should fail when the page embeds no IPFS CID")
	}
}

// TestAnnasResolveNoMirrors verifies an empty mirror list errors rather than panics.
func TestAnnasResolveNoMirrors(t *testing.T) {
	s := annasSource{mirrors: staticMirrors{}}
	if _, err := s.Resolve(context.Background(), Item{MD5: "abc"}); err == nil {
		t.Fatal("Resolve() with no mirrors should fail")
	}
}

// TestAnnasMemberAPIPreferredWhenKeySet verifies a configured key routes through
// the member fast-download API, whose URL is used directly, and that the md5 and
// key both reach the endpoint.
func TestAnnasMemberAPIPreferredWhenKeySet(t *testing.T) {
	const md5 = "d64efd386ed7227592499460aca2044b"
	const memberURL = "https://fast.example.net/dl/book.pdf"

	var gotKey, gotMD5 string
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dyn/api/fast_download.json" {
			http.NotFound(w, r)
			return
		}
		gotKey = r.URL.Query().Get("key")
		gotMD5 = r.URL.Query().Get("md5")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"download_url":"` + memberURL + `",` +
			`"account_fast_download_info":{"downloads_left":49,"downloads_per_day":50}}`))
	}))
	defer site.Close()

	s := annasSource{mirrors: staticMirrors{site.URL}, http: site.Client(), key: "secret-key"}
	got, err := s.Resolve(context.Background(), Item{MD5: md5})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.FileURL != memberURL {
		t.Errorf("FileURL = %q, want %q", got.FileURL, memberURL)
	}
	if gotKey != "secret-key" || gotMD5 != md5 {
		t.Errorf("member API called with key=%q md5=%q", gotKey, gotMD5)
	}
	if !got.VerifyMD5 {
		t.Error("VerifyMD5 = false, want true on the member path too")
	}
}

// TestAnnasMemberAPIErrorFallsBackToIPFS verifies a rejected key (the API reports
// its reason in an "error" field) degrades to the keyless IPFS path instead of
// failing the source outright.
func TestAnnasMemberAPIErrorFallsBackToIPFS(t *testing.T) {
	const cidV1 = "bafyfallbackcidzzz234567"
	const md5 = "d64efd386ed7227592499460aca2044b"

	gw := gatewayServing(http.StatusPartialContent)
	defer gw.Close()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dyn/api/fast_download.json" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"Not a member"}`))
			return
		}
		_, _ = w.Write([]byte(annasBookPage("QmV0", cidV1)))
	}))
	defer site.Close()

	s := annasSource{
		mirrors:  staticMirrors{site.URL},
		http:     site.Client(),
		key:      "expired-key",
		gateways: []string{gw.URL + "/ipfs/"},
	}
	got, err := s.Resolve(context.Background(), Item{MD5: md5})
	if err != nil {
		t.Fatalf("Resolve() must fall back to IPFS, got: %v", err)
	}
	if want := gw.URL + "/ipfs/" + cidV1; got.FileURL != want {
		t.Errorf("FileURL = %q, want the IPFS fallback %q", got.FileURL, want)
	}
}

// TestAnnasNoKeySkipsMemberAPI verifies the member endpoint is never contacted
// without a key, keeping the default path fully keyless.
func TestAnnasNoKeySkipsMemberAPI(t *testing.T) {
	const cidV1 = "bafykeylesscidzzzz234567"
	gw := gatewayServing(http.StatusPartialContent)
	defer gw.Close()

	var apiCalls int
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "fast_download") {
			apiCalls++
		}
		_, _ = w.Write([]byte(annasBookPage("QmV0", cidV1)))
	}))
	defer site.Close()

	s := annasSource{
		mirrors:  staticMirrors{site.URL},
		http:     site.Client(),
		gateways: []string{gw.URL + "/ipfs/"},
	}
	if _, err := s.Resolve(context.Background(), Item{MD5: "abc"}); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if apiCalls != 0 {
		t.Errorf("member API contacted %d times with no key configured", apiCalls)
	}
}

// TestExtractIPFSCIDPrefersV1 verifies the v1 (bafy…) CID wins over v0, since
// modern public gateways resolve v1 most reliably.
func TestExtractIPFSCIDPrefersV1(t *testing.T) {
	body := []byte(annasBookPage("QmeFQDjVrNKRrc94a3iMjzsGWKEQff9Wq1mXxdCrdnwvRU", "bafykbzaceb75yjsl"))
	got, ok := extractIPFSCID(body)
	if !ok || got != "bafykbzaceb75yjsl" {
		t.Fatalf("extractIPFSCID() = %q, %v; want the v1 CID", got, ok)
	}
}

// TestExtractIPFSCIDFallsBackToV0 verifies a page carrying only a v0 CID resolves.
func TestExtractIPFSCIDFallsBackToV0(t *testing.T) {
	const want = "QmeFQDjVrNKRrc94a3iMjzsGWKEQff9Wq1mXxdCrdnwvRU"
	got, ok := extractIPFSCID([]byte(`<a href="ipfs://` + want + `">x</a>`))
	if !ok || got != want {
		t.Fatalf("extractIPFSCID() = %q, %v; want %q", got, ok, want)
	}
}

// TestExtractIPFSCIDNone verifies a page with no CID reports a miss.
func TestExtractIPFSCIDNone(t *testing.T) {
	if got, ok := extractIPFSCID([]byte(`<html><body>nothing</body></html>`)); ok {
		t.Fatalf("extractIPFSCID() = %q, true; want a miss", got)
	}
}
