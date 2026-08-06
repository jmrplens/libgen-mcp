package netguard

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestBlocked covers the address predicate, which is the whole policy: every
// range here is one an attacker would aim a deposited URL at, and every allowed
// case is one a legitimate publisher link actually uses.
func TestBlocked(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		blocked bool
	}{
		{"loopback v4", "127.0.0.1", true},
		{"loopback v4, whole /8", "127.9.9.9", true},
		{"loopback v6", "::1", true},
		{"unspecified v4", "0.0.0.0", true},
		{"this-network /8", "0.1.2.3", true},
		{"unspecified v6", "::", true},
		{"broadcast", "255.255.255.255", true},
		{"cloud instance metadata", "169.254.169.254", true},
		{"link-local v4", "169.254.1.1", true},
		{"link-local v6", "fe80::1", true},
		{"private 10/8", "10.0.0.1", true},
		{"private 172.16/12", "172.16.5.4", true},
		{"private 192.168/16", "192.168.1.1", true},
		{"IPv6 unique-local", "fd00::1", true},
		{"carrier-grade NAT", "100.64.0.1", true},
		{"multicast v4", "224.0.0.1", true},
		{"multicast v6", "ff02::1", true},
		// The IPv4-mapped forms are the standard way past a filter that reasons
		// about one address family only, so each blocked family is checked in its
		// disguise as well.
		{"IPv4-mapped loopback", "::ffff:127.0.0.1", true},
		{"IPv4-mapped metadata", "::ffff:169.254.169.254", true},
		{"IPv4-mapped private", "::ffff:192.168.1.1", true},
		// Public addresses a real publisher link resolves to.
		{"public v4", "104.18.32.7", false},
		{"public v4 just outside CGNAT", "100.128.0.1", false},
		{"public v4 just outside 172.16/12", "172.32.0.1", false},
		{"public v6", "2606:4700::1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, err := netip.ParseAddr(tt.addr)
			if err != nil {
				t.Fatalf("ParseAddr(%q) error = %v", tt.addr, err)
			}
			if got := Blocked(addr); got != tt.blocked {
				t.Errorf("Blocked(%s) = %v, want %v", tt.addr, got, tt.blocked)
			}
		})
	}
}

// TestBlockedRejectsInvalidAddress verifies the zero Addr is blocked: an
// unparseable destination is not one to take a chance on.
func TestBlockedRejectsInvalidAddress(t *testing.T) {
	if !Blocked(netip.Addr{}) {
		t.Error("Blocked(invalid) = false, want true")
	}
}

// TestClientRefusesLoopbackServer is the test that matters: a guarded client is
// pointed at a real listening server on loopback and must refuse to connect. The
// server records whether it was ever reached, so a policy that let the request
// through would be caught even if the response were discarded.
//
// This is where the policy is exercised, because every other package's suite runs
// with it lifted (see SetAllowPrivateForTest) — their fixtures are all on
// loopback.
func TestClientRefusesLoopbackServer(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = io.WriteString(w, "%PDF-1.7")
	}))
	defer srv.Close()

	resp, err := get(t, Client(5*time.Second, false), srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("Get() error = nil, want the loopback destination refused")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("error %v is not ErrBlockedAddress", err)
	}
	if reached {
		t.Error("the request reached the server; the dialer let a loopback connection through")
	}
}

// TestClientAllowsLoopbackWhenPermitted verifies the escape hatch actually opens:
// an operator pointing the server at a mirror on their own network, and every
// unit suite in the repo, depend on it.
func TestClientAllowsLoopbackWhenPermitted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	resp, err := get(t, Client(5*time.Second, true), srv.URL)
	if err != nil {
		t.Fatalf("Get() error = %v, want the request permitted", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
}

// TestSetAllowPrivateForTestRestores verifies the seam is symmetric: a package
// that lifts the policy for its own suite must be able to put it back, or a later
// test in the same binary would silently run unguarded.
func TestSetAllowPrivateForTestRestores(t *testing.T) {
	restore := SetAllowPrivateForTest(true)
	if !privateAllowedForTest.Load() {
		t.Fatal("SetAllowPrivateForTest(true) did not take effect")
	}
	restore()
	if privateAllowedForTest.Load() {
		t.Error("restore() did not put the policy back")
	}
}

// TestCheckRedirectRefusesPrivateTarget covers the hop the dialer alone would
// report obscurely: a public-looking URL that 302s into private space. The
// redirect policy names the target instead of leaving an opaque dial failure.
func TestCheckRedirectRefusesPrivateTarget(t *testing.T) {
	req := &http.Request{URL: mustParse(t, "http://169.254.169.254/latest/meta-data/")}
	err := CheckRedirect(false)(req, []*http.Request{{URL: mustParse(t, "https://publisher.example.org/a.pdf")}})
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("error %v is not ErrBlockedAddress", err)
	}
	if !strings.Contains(err.Error(), "169.254.169.254") {
		t.Errorf("error = %q, want it to name the redirect target", err)
	}
}

// TestCheckRedirectBoundsTheChain verifies a redirect loop is cut rather than
// followed to Go's own higher default.
func TestCheckRedirectBoundsTheChain(t *testing.T) {
	via := make([]*http.Request, maxRedirects)
	for i := range via {
		via[i] = &http.Request{URL: mustParse(t, "https://publisher.example.org/")}
	}
	err := CheckRedirect(false)(&http.Request{URL: mustParse(t, "https://publisher.example.org/next")}, via)
	if !errors.Is(err, ErrTooManyRedirects) {
		t.Errorf("error %v is not ErrTooManyRedirects", err)
	}
}

// TestCheckRedirectStripsCredentialsAcrossHosts pins the one protection the
// dialer cannot give. net/http keeps the Authorization header when a redirect
// lands on a SUBDOMAIN of the original host, so a publisher API that redirects to
// its own internal host would forward the caller's key; any host change is
// treated as reason enough here.
func TestCheckRedirectStripsCredentialsAcrossHosts(t *testing.T) {
	tests := []struct {
		name       string
		from, to   string
		wantHeader bool
	}{
		{"same host keeps it", "https://api.example.org/a", "https://api.example.org/b", true},
		{"subdomain loses it", "https://example.org/a", "https://internal.example.org/b", false},
		{"different host loses it", "https://api.example.org/a", "https://elsewhere.test/b", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{URL: mustParse(t, tt.to), Header: http.Header{}}
			req.Header.Set("Authorization", "Bearer s3cret")
			req.Header.Set("Cookie", "session=1")
			if err := CheckRedirect(false)(req, []*http.Request{{URL: mustParse(t, tt.from)}}); err != nil {
				t.Fatalf("CheckRedirect() error = %v", err)
			}
			if got := req.Header.Get("Authorization") != ""; got != tt.wantHeader {
				t.Errorf("Authorization present = %v, want %v", got, tt.wantHeader)
			}
			if got := req.Header.Get("Cookie") != ""; got != tt.wantHeader {
				t.Errorf("Cookie present = %v, want %v", got, tt.wantHeader)
			}
		})
	}
}

// TestTransportKeepsStandardTuning verifies the guarded Transport is a clone of
// the standard one rather than a bare struct: dropping the standard library's
// pooling and timeouts while adding a dialer would trade a security fix for a
// performance regression nobody asked for.
func TestTransportKeepsStandardTuning(t *testing.T) {
	std, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skip("http.DefaultTransport is not an *http.Transport on this platform")
	}
	got := Transport(false)
	if got.DialContext == nil {
		t.Error("DialContext is nil; the guarded dialer was not installed")
	}
	if got.MaxIdleConns != std.MaxIdleConns {
		t.Errorf("MaxIdleConns = %d, want the standard %d", got.MaxIdleConns, std.MaxIdleConns)
	}
	if got.TLSHandshakeTimeout != std.TLSHandshakeTimeout {
		t.Errorf("TLSHandshakeTimeout = %v, want the standard %v", got.TLSHandshakeTimeout, std.TLSHandshakeTimeout)
	}
}

// TestControlIsAbsentWhenPrivateIsAllowed verifies the allowance removes the hook
// entirely rather than making it a no-op check on every dial.
func TestControlIsAbsentWhenPrivateIsAllowed(t *testing.T) {
	if control(true) != nil {
		t.Error("control(true) returned a hook, want nil")
	}
	if control(false) == nil {
		t.Fatal("control(false) returned nil, want the guard")
	}
	if err := control(false)("tcp", "10.0.0.1:80", nil); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("control error %v is not ErrBlockedAddress", err)
	}
	if err := control(false)("tcp", "not-an-address", nil); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("an unparseable destination must be refused, got %v", err)
	}
	if err := control(false)("tcp", "104.18.32.7:443", nil); err != nil {
		t.Errorf("control error = %v, want a public address permitted", err)
	}
}

// TestClientTimeout verifies the streaming client is built without a wall-clock
// timeout, whose lifetime is its context's, while the page client keeps one.
func TestClientTimeout(t *testing.T) {
	if got := Client(0, true).Timeout; got != 0 {
		t.Errorf("Client(0).Timeout = %v, want none", got)
	}
	if got := Client(3*time.Second, true).Timeout; got != 3*time.Second {
		t.Errorf("Client(3s).Timeout = %v, want 3s", got)
	}
}

// TestGuardedClientHonorsContext verifies the guarded Transport still respects
// cancellation, i.e. that replacing DialContext did not break the plumbing every
// caller in this repo relies on for its per-source budget.
func TestGuardedClientHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.org/", http.NoBody)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	_, doErr := Client(5*time.Second, false).Do(req) //nolint:bodyclose // never succeeds
	if !errors.Is(doErr, context.Canceled) {
		t.Errorf("error %v is not context.Canceled", doErr)
	}
}

// mustParse parses a URL for a test table, failing the test rather than the
// assertion when a literal is malformed.
func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", raw, err)
	}
	return u
}

// TestCheckRedirectHonorsTheAllowance pins the defect the escape hatch had while
// this policy was package-level: it refused redirects into private space even for
// a client explicitly built to permit them, so an operator pointing the server at
// a mirror on their own network would have had every redirect that mirror issues
// rejected. The hop cap and the credential strip are not address policy and must
// still apply.
func TestCheckRedirectHonorsTheAllowance(t *testing.T) {
	req := &http.Request{URL: mustParse(t, "http://192.168.1.50/file.pdf"), Header: http.Header{}}
	if err := CheckRedirect(true)(req, []*http.Request{{URL: mustParse(t, "http://192.168.1.50/ads.php")}}); err != nil {
		t.Errorf("CheckRedirect(true) error = %v, want the redirect permitted", err)
	}

	req.Header.Set("Authorization", "Bearer s3cret")
	if err := CheckRedirect(true)(req, []*http.Request{{URL: mustParse(t, "http://other.example.org/")}}); err != nil {
		t.Fatalf("CheckRedirect(true) error = %v", err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("Authorization survived a host change; the credential strip is not address policy and must always apply")
	}
}

// TestClientAllowsPrivateRedirectWhenPermitted proves the same end to end: a
// permitted client must follow a redirect between loopback endpoints, which is
// exactly the shape of the libgen ads.php -> get.php -> CDN chain every download
// test exercises.
func TestClientAllowsPrivateRedirectWhenPermitted(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/file", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "bytes")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/file", http.StatusFound)
	})

	resp, err := get(t, Client(5*time.Second, true), srv.URL+"/start")
	if err != nil {
		t.Fatalf("Get() error = %v, want the redirect followed", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "bytes" {
		t.Errorf("body = %q, want bytes", body)
	}
}

// get issues a context-carrying GET, which is what the linter asks for and what
// every caller in this repo does: a request with no context cannot be canceled
// by the per-source budget.
func get(t *testing.T, c *http.Client, rawURL string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		t.Fatalf("NewRequestWithContext(%q) error = %v", rawURL, err)
	}
	return c.Do(req)
}
