package netguard

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// forwardProxy is an HTTP forward proxy on loopback that answers every request
// itself rather than forwarding it, and records which destinations it was asked
// to reach. Loopback is a private address, which is the whole point: it stands
// in for the corporate proxy on 10.x that HTTP_PROXY names in an ordinary
// deployment.
type forwardProxy struct {
	srv *httptest.Server
	// mu guards asked, which the handler writes on the server's goroutine.
	mu    sync.Mutex
	asked []string
}

// newForwardProxy starts a forwardProxy and closes it with the test.
func newForwardProxy(t *testing.T) *forwardProxy {
	t.Helper()
	p := &forwardProxy{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.asked = append(p.asked, r.URL.Host)
		p.mu.Unlock()
		_, _ = io.WriteString(w, "via proxy")
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// destinations returns the hosts the proxy was asked to reach so far.
func (p *forwardProxy) destinations() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.asked)
}

// proxiedClient builds the client ClientFor ships for policy, sending every
// request through proxy the way HTTP_PROXY would.
//
// The Proxy field is set on the transport rather than through the environment,
// because http.ProxyFromEnvironment reads the environment once per process and
// a test that set HTTP_PROXY would change every other test in the binary.
func proxiedClient(t *testing.T, policy *Policy, proxy *forwardProxy) *http.Client {
	t.Helper()
	c := ClientFor(5*time.Second, policy)
	base := guardedBase(t, c)
	proxyURL, err := url.Parse(proxy.srv.URL)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", proxy.srv.URL, err)
	}
	base.Proxy = http.ProxyURL(proxyURL)
	t.Cleanup(base.CloseIdleConnections)
	return c
}

// guardedBase returns the *http.Transport inside a client ClientFor built.
func guardedBase(t *testing.T, c *http.Client) *http.Transport {
	t.Helper()
	stamping, ok := c.Transport.(*policyTransport)
	if !ok {
		t.Fatalf("client transport is %T, want *policyTransport", c.Transport)
	}
	base, ok := stamping.base.(*http.Transport)
	if !ok {
		t.Fatalf("guarded transport is %T, want *http.Transport", stamping.base)
	}
	return base
}

// newConnectProxy starts a forwardProxy that answers CONNECT, the way an https
// destination is reached through an http proxy, and tunnels every one to
// target whatever host it names, recording the host it was asked for.
func newConnectProxy(t *testing.T, target string) *forwardProxy {
	t.Helper()
	p := &forwardProxy{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "this proxy only tunnels", http.StatusMethodNotAllowed)
			return
		}
		p.mu.Lock()
		p.asked = append(p.asked, r.Host)
		p.mu.Unlock()
		tunnel(w, r, target)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// tunnel connects the hijacked client connection to target in both directions
// until either side closes. It runs on the proxy's handler goroutine, so every
// failure is answered on the wire rather than reported to a test.
func tunnel(w http.ResponseWriter, r *http.Request, target string) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "connection cannot be hijacked", http.StatusInternalServerError)
		return
	}
	upstream, err := (&net.Dialer{}).DialContext(r.Context(), "tcp", target)
	if err != nil {
		http.Error(w, "dialing the origin failed", http.StatusBadGateway)
		return
	}
	conn, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
	go func() {
		_, _ = io.Copy(upstream, buffered)
		_ = upstream.Close()
	}()
	_, _ = io.Copy(conn, upstream)
	_ = conn.Close()
}

// TestAnHTTPSRequestTunnelsThroughAPrivateProxy covers the https path, where
// net/http opens a CONNECT tunnel through the proxy instead of forwarding the
// request. The tunnel is dialed to the proxy's address like any proxied
// request, so it gets the same answer: a private proxy is not refused for its
// own address, and a metadata destination is refused before a CONNECT is sent.
func TestAnHTTPSRequestTunnelsThroughAPrivateProxy(t *testing.T) {
	stubResolver(t, map[string][]string{"example.com": {"93.184.215.14"}})
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "tunneled")
	}))
	t.Cleanup(origin.Close)
	proxy := newConnectProxy(t, origin.Listener.Addr().String())
	client := proxiedClient(t, NewPolicy(nil, false), proxy)
	originTransport, ok := origin.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("origin client transport is %T, want *http.Transport", origin.Client().Transport)
	}
	// The test certificate names example.com, so that is the host asked for,
	// and the proxy tunnels it to the origin on loopback.
	guardedBase(t, client).TLSClientConfig = originTransport.TLSClientConfig.Clone()
	destination := "example.com:" + mustParse(t, origin.URL).Port()

	resp, err := get(t, client, "https://"+destination+"/")
	if err != nil {
		t.Fatalf("Get() through a CONNECT tunnel error = %v, want the origin reached", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "tunneled" {
		t.Errorf("body = %q, want the origin's answer", body)
	}

	resp, err = get(t, client, "https://0xa9fea9fe/latest/")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a metadata destination was tunneled through the proxy")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("err = %v, want ErrBlockedAddress", err)
	}
	if got := proxy.destinations(); !slices.Equal(got, []string{destination}) {
		t.Errorf("proxy was asked to tunnel to %v, want only [%s]", got, destination)
	}
}

// TestAPrivateProxyIsNotJudgedAsTheDestination is the regression test for a
// deployment behind a corporate proxy.
//
// With HTTP_PROXY set the dialer is handed the proxy's address, not the
// destination's, and a proxy on a private address used to be refused under the
// destination's rule. That refused every host the operator did not name, which
// is Crossref, arXiv, Unpaywall and every download URL, whatever they were going
// to reach. The proxy is the operator's own configuration, so its dial answers
// to tier A alone.
func TestAPrivateProxyIsNotJudgedAsTheDestination(t *testing.T) {
	stubResolver(t, map[string][]string{"api.crossref.test": {"93.184.215.14"}})
	proxy := newForwardProxy(t)
	client := proxiedClient(t, NewPolicy(nil, false), proxy)

	resp, err := get(t, client, "http://api.crossref.test/works")
	if err != nil {
		t.Fatalf("Get() through a private proxy error = %v, want the public destination reached", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "via proxy" {
		t.Errorf("body = %q, want the proxy's answer", body)
	}
	if got := proxy.destinations(); !slices.Equal(got, []string{"api.crossref.test"}) {
		t.Errorf("proxy was asked for %v, want [api.crossref.test]", got)
	}
}

// TestTheDestinationBehindAProxyIsStillJudged is the other half: letting the
// proxy's own dial through must not let a request reach, through the proxy,
// what the dialer would refuse it directly.
//
// Behind a proxy the dialer only ever sees the proxy, so a destination the URL
// spells as an address is judged per request, before anything is sent, under
// both tiers. A name is resolved by the proxy and stays its decision.
func TestTheDestinationBehindAProxyIsStillJudged(t *testing.T) {
	for _, tc := range []struct {
		name    string
		policy  *Policy
		rawURL  string
		refused bool
	}{
		{name: "private literal, strict", policy: NewPolicy(nil, false), rawURL: "http://10.1.2.3/file.pdf", refused: true},
		{name: "loopback literal, strict", policy: NewPolicy(nil, false), rawURL: "http://127.0.0.1:9/file.pdf", refused: true},
		{name: "metadata under the allowance", policy: NewPolicy(nil, true), rawURL: "http://169.254.169.254/latest/meta-data/", refused: true},
		{name: "metadata named by the operator", policy: NewPolicy([]string{"169.254.169.254"}, true), rawURL: "http://169.254.169.254/latest/meta-data/", refused: true},
		{name: "private literal under the allowance", policy: NewPolicy(nil, true), rawURL: "http://10.1.2.3/file.pdf", refused: false},
		{name: "private literal the operator named", policy: NewPolicy([]string{"10.1.2.3"}, false), rawURL: "http://10.1.2.3/file.pdf", refused: false},
		{name: "public literal, strict", policy: NewPolicy(nil, false), rawURL: "http://93.184.215.14/file.pdf", refused: false},
		// Every spelling getaddrinfo reads as 169.254.169.254 through inet_aton,
		// none of which netip accepts, plus the two IPv6 coats it can wear.
		{name: "metadata as one decimal part", policy: NewPolicy(nil, true), rawURL: "http://2852039166/latest/", refused: true},
		{name: "metadata as one hex part", policy: NewPolicy(nil, true), rawURL: "http://0xa9fea9fe/latest/", refused: true},
		{name: "metadata in octal", policy: NewPolicy(nil, true), rawURL: "http://0251.0376.0251.0376/latest/", refused: true},
		{name: "metadata as three parts", policy: NewPolicy(nil, true), rawURL: "http://169.254.43518/latest/", refused: true},
		{name: "metadata with a trailing dot", policy: NewPolicy(nil, true), rawURL: "http://2852039166./latest/", refused: true},
		{name: "metadata mapped into IPv6", policy: NewPolicy(nil, true), rawURL: "http://[::ffff:169.254.169.254]/latest/", refused: true},
		{name: "metadata mapped into IPv6 in hex", policy: NewPolicy(nil, true), rawURL: "http://[::ffff:a9fe:a9fe]/latest/", refused: true},
		{name: "a numeric host that is no address", policy: NewPolicy(nil, true), rawURL: "http://1.2.3.4.5/latest/", refused: true},
		{name: "loopback as one decimal part, strict", policy: NewPolicy(nil, false), rawURL: "http://2130706433/", refused: true},
		{name: "public as one decimal part", policy: NewPolicy(nil, false), rawURL: "http://1572394766/file.pdf", refused: false},
		// A name gets tier A from a local lookup, and nothing else.
		{name: "a name resolving to metadata", policy: NewPolicy(nil, true), rawURL: "http://169.254.169.254.nip.test/latest/", refused: true},
		{name: "a name the operator named resolving to metadata", policy: NewPolicy([]string{"meta.operator.test"}, true), rawURL: "http://meta.operator.test/latest/", refused: true},
		{name: "a name resolving private", policy: NewPolicy(nil, false), rawURL: "http://intranet.corp.test/file.pdf", refused: false},
		{name: "a name this machine cannot resolve", policy: NewPolicy(nil, false), rawURL: "http://only.the-proxy-knows.test/file.pdf", refused: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubResolver(t, map[string][]string{
				"169.254.169.254.nip.test": {"93.184.215.14", "169.254.169.254"},
				"meta.operator.test":       {"169.254.169.254"},
				"intranet.corp.test":       {"10.20.30.40"},
			})
			proxy := newForwardProxy(t)
			resp, err := get(t, proxiedClient(t, tc.policy, proxy), tc.rawURL)
			if err == nil {
				_ = resp.Body.Close()
			}
			if !tc.refused {
				if err != nil {
					t.Fatalf("Get(%q) error = %v, want it sent through the proxy", tc.rawURL, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Get(%q) reached the proxy, want it refused before sending", tc.rawURL)
			}
			if !errors.Is(err, ErrBlockedAddress) {
				t.Errorf("err = %v, want ErrBlockedAddress", err)
			}
			if got := proxy.destinations(); len(got) != 0 {
				t.Errorf("proxy was asked for %v, want nothing sent", got)
			}
		})
	}
}

// TestProxyDialAddressIsWhatNetHTTPDials holds the stamp to the address
// net/http actually hands the dialer, for every proxy scheme it supports.
//
// guardedDial compares the two as strings, so a stamp spelled differently from
// the dial matches nothing and the proxy is judged as the destination again.
// That fails safe, but it would reintroduce the refusal this exists to remove,
// silently, for whichever scheme drifted.
func TestProxyDialAddressIsWhatNetHTTPDials(t *testing.T) {
	for _, proxyURL := range []string{
		"http://proxy.corp.test",
		"http://proxy.corp.test:3128",
		"https://proxy.corp.test",
		"socks5://proxy.corp.test",
		"socks5h://proxy.corp.test:9050",
		"http://10.0.0.8:8080",
		"http://[fd00::8]",
	} {
		t.Run(proxyURL, func(t *testing.T) {
			parsed, err := url.Parse(proxyURL)
			if err != nil {
				t.Fatalf("url.Parse(%q) error = %v", proxyURL, err)
			}
			// The dial runs on the transport's own goroutine, so what it saw is
			// recorded under a lock and read after the round trip returns.
			var mu sync.Mutex
			var dialed string
			transport := &http.Transport{
				Proxy: http.ProxyURL(parsed),
				DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
					mu.Lock()
					dialed = address
					mu.Unlock()
					return nil, errors.New("recorded, not dialed")
				},
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://books.example.test/file", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			want, err := proxyDialAddress(transport, req)
			if err != nil {
				t.Fatalf("proxyDialAddress() error = %v", err)
			}
			if resp, rerr := transport.RoundTrip(req); rerr == nil {
				_ = resp.Body.Close()
			}
			mu.Lock()
			defer mu.Unlock()
			if dialed != want {
				t.Errorf("net/http dialed %q, the stamp says %q", dialed, want)
			}
		})
	}
}

// TestProxyDialAddressWithNoProxy covers the shapes that dial the destination
// directly, where no proxy address may be stamped, and the one that fails.
func TestProxyDialAddressWithNoProxy(t *testing.T) {
	proxyErr := errors.New("proxy configuration is broken")
	for _, tc := range []struct {
		name    string
		base    http.RoundTripper
		wantErr error
	}{
		{name: "not an http.Transport", base: &recordingTransport{}},
		{name: "no Proxy function", base: &http.Transport{}},
		// A nil URL with a nil error is how net/http's own Proxy contract says
		// "direct", so the double has to answer in exactly that shape.
		{name: "Proxy answers direct", base: &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return nil, nil }}}, //nolint:nilnil // the Proxy contract's own spelling of "no proxy".
		{name: "Proxy fails", base: &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return nil, proxyErr }}, wantErr: proxyErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://books.example.test/", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			got, err := proxyDialAddress(tc.base, req)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if got != "" {
				t.Errorf("address = %q, want none", got)
			}
		})
	}
}

// closeRecorder is a request body that records whether it was closed.
type closeRecorder struct {
	io.Reader
	closed bool
}

// Close records the call.
func (c *closeRecorder) Close() error {
	c.closed = true
	return nil
}

// TestARefusalBeforeSendingClosesTheBody pins the RoundTripper contract on the
// two refusals policyTransport makes itself. net/http closed the body when its
// own call of the Proxy function failed, and a transport that now refuses
// before net/http is reached has to do the same, or a streamed body leaks.
func TestARefusalBeforeSendingClosesTheBody(t *testing.T) {
	proxyErr := errors.New("proxy configuration is broken")
	for _, tc := range []struct {
		name    string
		proxy   func(*http.Request) (*url.URL, error)
		rawURL  string
		wantErr error
	}{
		{
			name:    "Proxy fails",
			proxy:   func(*http.Request) (*url.URL, error) { return nil, proxyErr },
			rawURL:  "http://books.example.test/",
			wantErr: proxyErr,
		},
		{
			name:    "destination refused",
			proxy:   http.ProxyURL(&url.URL{Scheme: "http", Host: "10.0.0.8:3128"}),
			rawURL:  "http://169.254.169.254/latest/",
			wantErr: ErrBlockedAddress,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &closeRecorder{Reader: strings.NewReader("payload")}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, tc.rawURL, body)
			if err != nil {
				t.Fatal(err)
			}
			transport := &policyTransport{base: &http.Transport{Proxy: tc.proxy}, policy: NewPolicy(nil, true), allowPrivate: true}
			resp, err := transport.RoundTrip(req)
			if err == nil {
				_ = resp.Body.Close()
				t.Fatal("RoundTrip() sent the request, want it refused")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if !body.closed {
				t.Error("the refused request's body was left open")
			}
		})
	}
	t.Run("no body", func(t *testing.T) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://books.example.test/", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := refuseUnsent(req, proxyErr); !errors.Is(got, proxyErr) {
			t.Errorf("err = %v, want %v", got, proxyErr)
		}
	})
}

// TestGuardedDialRestampsOnlyTheProxy drives the wrapper against a dialer whose
// Control hook records the decision it was handed, so what is asserted is what
// the guard would read.
//
// A dial to the stamped proxy address is re-stamped as the proxy's own, and any
// other address keeps the request's decision: that is the fallback that makes
// a spelling the two disagree about refuse more rather than permit more.
func TestGuardedDialRestampsOnlyTheProxy(t *testing.T) {
	policy := NewPolicy(nil, false)
	stamped := dialDecision{policy: policy, proxy: "127.0.0.1:1"}
	for _, tc := range []struct {
		name      string
		decision  dialDecision
		address   string
		proxyDial bool
	}{
		{name: "the proxy", decision: stamped, address: "127.0.0.1:1", proxyDial: true},
		{name: "another address", decision: stamped, address: "127.0.0.1:2", proxyDial: false},
		{name: "a direct request", decision: dialDecision{policy: policy}, address: "127.0.0.1:1", proxyDial: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var seen dialDecision
			refused := errors.New("recorded, not connected")
			dialer := &net.Dialer{ControlContext: func(ctx context.Context, _, _ string, _ syscall.RawConn) error {
				mu.Lock()
				seen, _ = dialDecisionFrom(ctx)
				mu.Unlock()
				return refused
			}}
			ctx := withDialDecision(t.Context(), tc.decision)
			conn, err := guardedDial(dialer)(ctx, "tcp", tc.address)
			if err == nil {
				_ = conn.Close()
				t.Fatal("the dial connected, so the Control hook never ran")
			}
			mu.Lock()
			defer mu.Unlock()
			if seen.proxyDial != tc.proxyDial {
				t.Errorf("proxyDial = %v, want %v", seen.proxyDial, tc.proxyDial)
			}
			if !tc.proxyDial && seen.policy != policy {
				t.Error("a dial that is not the proxy lost the request's policy")
			}
		})
	}
}

// TestTheProxyDialAnswersToTierAOnly is what the re-stamp is worth at the
// guard: a private proxy is let through with nothing opened, and a proxy on a
// metadata address is still refused.
func TestTheProxyDialAnswersToTierAOnly(t *testing.T) {
	ctx := withDialDecision(t.Context(), dialDecision{proxyDial: true})
	if err := control(false)(ctx, "tcp", "10.0.0.8:3128", nil); err != nil {
		t.Errorf("the dial to a private proxy was refused: %v", err)
	}
	if err := control(false)(ctx, "tcp", "169.254.169.254:80", nil); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("err = %v, want a proxy on the metadata address refused", err)
	}
}

// TestAPooledProxyConnectionDoesNotCarryTheNextRequestPastTheGuard is the form
// connection reuse takes here.
//
// net/http keys a plain-HTTP request sent through an http proxy on the proxy
// alone, so one idle connection carries requests to many destinations, and a
// request served from it is never dialed. A decision made only at the dial
// would let the first request's answer serve the second: a request to the
// operator's own mirror opens the connection, and a request to a private
// address rides it. The destination is therefore judged per request, never by
// the connection.
//
// The mirror resolves public, so the private-sibling concession does not apply
// and the second request has nothing in the configuration answering for it.
func TestAPooledProxyConnectionDoesNotCarryTheNextRequestPastTheGuard(t *testing.T) {
	stubResolver(t, map[string][]string{"mirror.operator.test": {"93.184.215.14"}})
	proxy := newForwardProxy(t)
	client := proxiedClient(t, NewPolicy([]string{"mirror.operator.test"}, false), proxy)

	resp, err := get(t, client, "http://mirror.operator.test/mirror")
	if err != nil {
		t.Fatalf("Get() of the operator's mirror error = %v, want it reached", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	resp, err = get(t, client, "http://10.9.9.9/elsewhere")
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a private address the operator did not name was reached over the pooled proxy connection")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("err = %v, want ErrBlockedAddress", err)
	}
	if got := proxy.destinations(); !slices.Equal(got, []string{"mirror.operator.test"}) {
		t.Errorf("proxy was asked for %v, want only [mirror.operator.test]", got)
	}
}
