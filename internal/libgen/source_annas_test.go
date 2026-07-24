package libgen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
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

	// The member URL must be reachable: the source probes it before committing.
	fileSrv := gatewayServing(http.StatusPartialContent)
	defer fileSrv.Close()
	memberURL := fileSrv.URL + "/dl/book.pdf"

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

// TestAnnasMemberAPIReportsAccountQuota verifies the member response's account
// block is surfaced on the Resolved, so a caller can see how much of the metered
// daily allowance remains. The API is the only place this data exists and each
// call consumes a download, so it is captured from a real resolve rather than
// fetched separately.
func TestAnnasMemberAPIReportsAccountQuota(t *testing.T) {
	fileSrv := gatewayServing(http.StatusPartialContent)
	defer fileSrv.Close()

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dyn/api/fast_download.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"download_url":"` + fileSrv.URL + `/b.pdf",
			"account_fast_download_info":{"downloads_left":49,"downloads_per_day":50,"downloads_done_today":1}}`))
	}))
	defer site.Close()

	s := annasSource{mirrors: staticMirrors{site.URL}, http: site.Client(), key: "k"}
	got, err := s.Resolve(context.Background(), Item{MD5: "d64efd386ed7227592499460aca2044b"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.Account == nil {
		t.Fatal("Account = nil, want the member quota")
	}
	if got.Account.DownloadsLeft != 49 || got.Account.DownloadsPerDay != 50 || got.Account.DownloadsDoneToday != 1 {
		t.Fatalf("Account = %+v, want 49/50 with 1 done today", *got.Account)
	}
	if got.Account.Source != "annas" {
		t.Errorf("Account.Source = %q, want annas", got.Account.Source)
	}
}

// TestAnnasKeylessPathReportsNoQuota verifies the keyless IPFS path leaves the
// account block nil, since no account is involved.
func TestAnnasKeylessPathReportsNoQuota(t *testing.T) {
	const cidV1 = "bafyquotanonecidzzz234567"
	gw := gatewayServing(http.StatusPartialContent)
	defer gw.Close()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(annasBookPage("QmV0", cidV1)))
	}))
	defer site.Close()

	s := annasSource{mirrors: staticMirrors{site.URL}, http: site.Client(), gateways: []string{gw.URL + "/ipfs/"}}
	got, err := s.Resolve(context.Background(), Item{MD5: "abc"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.Account != nil {
		t.Fatalf("Account = %+v, want nil on the keyless path", *got.Account)
	}
}

// TestPerCallAnnasKeyPullsSourceIn verifies a per-call Anna's key pulls an annas
// source using that key into the chain even when the server configured none, and
// that it is prepended so it is tried first — mirroring the per-call Unpaywall
// email behavior.
func TestPerCallAnnasKeyPullsSourceIn(t *testing.T) {
	c := newTestClient(staticMirrors{})
	c.sources = []DownloadSource{libgenSource{c: c}}

	got := c.withPerCallAnnas(Item{MD5: "d64efd386ed7227592499460aca2044b", AnnasKey: "runtime-key"}, c.sources)
	if len(got) != 2 || got[0].Name() != "annas" {
		names := make([]string, len(got))
		for i, s := range got {
			names[i] = s.Name()
		}
		t.Fatalf("chain = %v, want annas prepended", names)
	}
	if s, ok := got[0].(annasSource); !ok || s.key != "runtime-key" {
		t.Fatalf("prepended source = %+v, want an annasSource carrying the per-call key", got[0])
	}
}

// TestPerCallAnnasKeyIgnoredWhenNotApplicable verifies the per-call key is a no-op
// without a key, without an md5, when a specific source was requested, or when an
// annas source is already in the chain (the configured key wins).
func TestPerCallAnnasKeyIgnoredWhenNotApplicable(t *testing.T) {
	c := newTestClient(staticMirrors{})
	base := []DownloadSource{libgenSource{c: c}}

	cases := map[string]Item{
		"no key":          {MD5: "abc"},
		"no md5":          {DOI: "10.1/x", AnnasKey: "k"},
		"explicit source": {MD5: "abc", AnnasKey: "k", Source: "libgen"},
	}
	for name, it := range cases {
		if got := c.withPerCallAnnas(it, base); len(got) != len(base) {
			t.Errorf("%s: chain grew to %d, want unchanged", name, len(got))
		}
	}

	withAnnas := []DownloadSource{libgenSource{c: c}, annasSource{key: "configured"}}
	got := c.withPerCallAnnas(Item{MD5: "abc", AnnasKey: "runtime"}, withAnnas)
	if len(got) != len(withAnnas) {
		t.Errorf("chain grew to %d, want unchanged when annas is already configured", len(got))
	}
}

// TestAnnasUnreachableMemberURLFallsBackToIPFS verifies that a member URL the
// client cannot actually reach falls back to the keyless IPFS path. This is a real
// live failure mode: Anna's hands out rotating file hosts, and some resolve to
// 0.0.0.0 on networks that null-route them. The allowance is spent when the API is
// called, whether or not its URL is used, so falling back costs no extra quota and
// still delivers the file.
func TestAnnasUnreachableMemberURLFallsBackToIPFS(t *testing.T) {
	const cidV1 = "bafyunreachablecidzz234567"
	const md5 = "d64efd386ed7227592499460aca2044b"

	// A closed listener's address stands in for a host that cannot be dialed.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	gw := gatewayServing(http.StatusPartialContent)
	defer gw.Close()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dyn/api/fast_download.json" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"download_url":"` + deadURL + `/f.pdf","account_fast_download_info":{"downloads_left":49}}`))
			return
		}
		_, _ = w.Write([]byte(annasBookPage("QmV0", cidV1)))
	}))
	defer site.Close()

	s := annasSource{
		mirrors:  staticMirrors{site.URL},
		http:     site.Client(),
		key:      "k",
		gateways: []string{gw.URL + "/ipfs/"},
	}
	got, err := s.Resolve(context.Background(), Item{MD5: md5})
	if err != nil {
		t.Fatalf("Resolve() must fall back to IPFS, got: %v", err)
	}
	if want := gw.URL + "/ipfs/" + cidV1; got.FileURL != want {
		t.Fatalf("FileURL = %q, want the IPFS fallback %q", got.FileURL, want)
	}
}

// TestAnnasMemberAPICalledOnceAcrossMirrors verifies the member fast-download API
// is invoked at most once even when multiple mirrors are configured, because each
// call consumes the account's metered allowance — calling it on every mirror would
// burn one unit per mirror for the same answer.
func TestAnnasMemberAPICalledOnceAcrossMirrors(t *testing.T) {
	const md5 = "d64efd386ed7227592499460aca2044b"

	var apiCalls int
	// Both mirrors' member API endpoints reject the key, forcing the loop to
	// advance past mirror 1 to mirror 2's IPFS path. The assertion is that the
	// member API is hit exactly once (on mirror 1), not once per mirror.
	reject := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dyn/api/fast_download.json" {
			apiCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"Not a member"}`))
			return
		}
		http.NotFound(w, r)
	}
	mirror1 := httptest.NewServer(http.HandlerFunc(reject))
	defer mirror1.Close()
	mirror2 := httptest.NewServer(http.HandlerFunc(reject))
	defer mirror2.Close()

	s := annasSource{mirrors: staticMirrors{mirror1.URL, mirror2.URL}, http: mirror1.Client(), key: "k"}
	_, _ = s.Resolve(context.Background(), Item{MD5: md5})
	if apiCalls != 1 {
		t.Fatalf("member API called %d times across mirrors, want exactly 1 (allowance is account-level)", apiCalls)
	}
}

// TestPerCallAnnasKeyReplacesPinnedSource verifies that when source: "annas" is
// pinned and a per-call key is supplied, the selected annas source is replaced
// with one carrying the per-call key — so the member tier takes effect even
// against a server that configured no key.
func TestPerCallAnnasKeyReplacesPinnedSource(t *testing.T) {
	c := newTestClient(staticMirrors{})
	configured := annasSource{key: ""} // keyless configured source
	sources := []DownloadSource{configured}

	got := c.withPerCallAnnas(Item{MD5: "abc", AnnasKey: "runtime", Source: "annas"}, sources)
	if len(got) != 1 {
		t.Fatalf("chain = %d sources, want 1 (replacement not append)", len(got))
	}
	s, ok := got[0].(annasSource)
	if !ok {
		t.Fatalf("source = %T, want annasSource", got[0])
	}
	if s.key != "runtime" {
		t.Fatalf("key = %q, want the per-call key %q", s.key, "runtime")
	}
}

// TestPerCallAnnasKeyIgnoredForNonAnnasSource verifies a per-call key is dropped
// when a non-annas source is explicitly pinned (e.g. source: "libgen").
func TestPerCallAnnasKeyIgnoredForNonAnnasSource(t *testing.T) {
	c := newTestClient(staticMirrors{})
	base := []DownloadSource{libgenSource{c: c}}
	got := c.withPerCallAnnas(Item{MD5: "abc", AnnasKey: "k", Source: "libgen"}, base)
	if len(got) != len(base) {
		t.Errorf("chain grew to %d for a non-annas pinned source, want unchanged", len(got))
	}
}

// TestExtractAnnasExtFromRecordPage verifies the file extension is read off the
// record page. Anna's serves the bytes over IPFS under a content address that
// carries no filename, so without this the saved file is extensionless and the
// read tool cannot pick an extractor for it — a real PDF becomes unreadable.
func TestExtractAnnasExtFromRecordPage(t *testing.T) {
	page, err := os.ReadFile("../discovery/testdata/annas_md5_zlib.html")
	if err != nil {
		t.Fatal(err)
	}
	if got := extractAnnasExt(page); got != "pdf" {
		t.Errorf("extractAnnasExt() = %q, want pdf", got)
	}
	if got := extractAnnasExt([]byte(`<html><body>no record here</body></html>`)); got != "" {
		t.Errorf("extractAnnasExt() on a non-record page = %q, want empty", got)
	}
}

// TestAnnasResolveCarriesTheExtension verifies the resolved item announces the
// file type. The IPFS gateway URL ends in a content address with no name, so
// without this the saved file has no extension and read cannot extract it.
func TestAnnasResolveCarriesTheExtension(t *testing.T) {
	gw := gatewayServing(http.StatusPartialContent)
	defer gw.Close()
	page := annasBookPage("QmTest0000000000000000000000000000000000000000",
		"bafyworkingcidzzzz234567") +
		`<span class="x">Filepath</span><span class="y">zlib/no-category/Author/Some Book.pdf</span>`
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(page))
	}))
	defer mirror.Close()

	s := annasSource{mirrors: staticMirrors{mirror.URL}, gateways: []string{gw.URL}}
	got, err := s.Resolve(context.Background(), Item{MD5: "d64efd386ed7227592499460aca2044b"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.Ext != "pdf" {
		t.Errorf("Resolved.Ext = %q, want pdf — an extensionless save makes the file unreadable", got.Ext)
	}
}

// TestAnnasMemberResolveCarriesTheExtension verifies the member tier also announces
// the file type. It never fetches the record page — that is the point of the fast
// path — so the extension has to come from the URL the API hands back, or a keyed
// deployment would save extensionless files that read cannot open.
func TestAnnasMemberResolveCarriesTheExtension(t *testing.T) {
	file := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
	}))
	defer file.Close()
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "fast_download") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"download_url":"` + file.URL + `/dl/Some%20Book.pdf"}`))
	}))
	defer mirror.Close()

	s := annasSource{mirrors: staticMirrors{mirror.URL}, key: "secret"}
	got, err := s.Resolve(context.Background(), Item{MD5: "d64efd386ed7227592499460aca2044b"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.Ext != "pdf" {
		t.Errorf("Resolved.Ext = %q, want pdf", got.Ext)
	}
}
