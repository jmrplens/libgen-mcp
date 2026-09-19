package netguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubResolver replaces the package's resolver for one test, answering from a
// table. A host the table does not list fails to resolve, which is the shape a
// real resolver takes when it has nothing to say and is the case the memo must
// not remember.
func stubResolver(t *testing.T, answers map[string][]string) {
	t.Helper()
	previous := lookupHostAddrs
	t.Cleanup(func() { lookupHostAddrs = previous })
	lookupHostAddrs = func(_ context.Context, host string) ([]netip.Addr, error) {
		raw, ok := answers[host]
		if !ok {
			return nil, fmt.Errorf("no answer for %q", host)
		}
		addrs := make([]netip.Addr, 0, len(raw))
		for _, r := range raw {
			addrs = append(addrs, netip.MustParseAddr(r))
		}
		return addrs, nil
	}
}

// dialWith runs the dialer's Control hook for one destination under a stamped
// decision, which is what every request this server makes arrives at the dialer
// carrying. It returns the hook's verdict without connecting to anything.
func dialWith(t *testing.T, policy *Policy, requestHost, address string) error {
	t.Helper()
	decision := dialDecision{policy: policy, operatorNamed: policy.Names(requestHost)}
	ctx := withDialDecision(t.Context(), decision)
	return control(policy.AllowsPrivate())(ctx, "tcp", address, nil)
}

// redirectTo runs the redirect policy for a hop to rawURL, coming from previous.
func redirectTo(t *testing.T, policy *Policy, previous, rawURL string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		t.Fatalf("NewRequestWithContext(%q) error = %v", rawURL, err)
	}
	var via []*http.Request
	if previous != "" {
		first, ferr := http.NewRequestWithContext(t.Context(), http.MethodGet, previous, http.NoBody)
		if ferr != nil {
			t.Fatalf("NewRequestWithContext(%q) error = %v", previous, ferr)
		}
		via = []*http.Request{first}
	}
	return checkRedirectFor(policy.AllowsPrivate(), policy)(req, via)
}

// TestNewPolicyNormalizesWhatTheOperatorWrote pins the shapes a configured host
// arrives in. LIBGEN_MIRROR is a URL, LIBGEN_MCP_SCIHUB_HOSTS are bare hosts and
// the family constants are URLs, so one set has to accept all three — and a
// value that normalized to something no destination can equal would be a member
// that matches nothing and looks like it might.
func TestNewPolicyNormalizesWhatTheOperatorWrote(t *testing.T) {
	p := NewPolicy([]string{
		"https://libgen.li",
		"http://192.168.1.5:8080/",
		"  sci-hub.ee  ",
		"MIRROR.EXAMPLE.TEST:8443",
		"[fd00::1]:443",
		"",
		"   ",
		// The same host twice, which happens the moment LIBGEN_MIRROR names one
		// of the family constants — the ordinary way to pin a mirror.
		"libgen.li",
	}, false)

	for _, host := range []string{"libgen.li", "192.168.1.5", "sci-hub.ee", "mirror.example.test", "fd00::1"} {
		t.Run(host, func(t *testing.T) {
			if !p.Names(host) {
				t.Errorf("Names(%q) = false, want the configured host recognized; got %v", host, p.Hosts())
			}
		})
	}
	if got := len(p.Hosts()); got != 5 {
		t.Errorf("len(Hosts()) = %d, want 5; an empty entry became a member: %v", got, p.Hosts())
	}
	if p.Names("libgen.la") {
		t.Error("Names(libgen.la) = true for a host nobody configured")
	}
}

// TestThirdPartyPrivateDestinationIsRefusedAndTheFlagOpensIt is the tier the
// guard exists for, stated at the dialer where the address is finally known.
//
// Both halves matter. Refusing is the point; admitting under the flag is the one
// legitimate deployment — an operator whose mirror is on their own network —
// and a guard that refuses it too would be a guard everybody turns off.
func TestThirdPartyPrivateDestinationIsRefusedAndTheFlagOpensIt(t *testing.T) {
	const thirdParty = "cdn.somebody-elses.test"

	refused := dialWith(t, NewPolicy([]string{"libgen.li"}, false), thirdParty, "10.0.0.1:80")
	if !errors.Is(refused, ErrBlockedAddress) {
		t.Errorf("err = %v, want ErrBlockedAddress for a third-party URL resolving into RFC 1918", refused)
	}

	if err := dialWith(t, NewPolicy([]string{"libgen.li"}, true), thirdParty, "10.0.0.1:80"); err != nil {
		t.Errorf("err = %v, want the flag to admit it; that is what the flag is for", err)
	}
}

// TestOperatorNamedHostIsExemptThoughItResolvesPrivate is the axis S08 adds,
// isolated from everything else that could admit the same dial.
//
// The named mirror resolves to a public address, so the sibling concession has
// nothing to say and the flag is off: being the host the operator wrote down is
// the only thing left that can permit this connection. The address dialed is
// private anyway — a public name with an RFC 1918 record, which is the shape
// that defeats a URL-level check and the reason the guard runs in the dialer.
//
// The negative is the same address reached from a host nobody named, which is
// the class the guard exists for and must stay refused.
func TestOperatorNamedHostIsExemptThoughItResolvesPrivate(t *testing.T) {
	const mirror = "mirror.operator.test"
	stubResolver(t, map[string][]string{mirror: {"93.184.216.34"}})
	p := NewPolicy([]string{"https://" + mirror}, false)

	if p.NamesPrivateHost(t.Context()) {
		t.Fatal("the fixture's mirror resolves private, so the concession could admit the dial and this test would pin nothing")
	}
	if err := dialWith(t, p, mirror, "10.0.0.1:80"); err != nil {
		t.Errorf("err = %v, want the operator's own host dialed whatever it resolves to", err)
	}
	if err := dialWith(t, p, "cdn.somebody-elses.test", "10.0.0.1:80"); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("err = %v, want the same address refused when nobody named the host it came from", err)
	}
}

// TestOperatorNamedHostIsReachedEndToEnd is the wiring, asserted against a real
// server on an address [Blocked] covers.
//
// It is deliberately not the pin for the exemption above — a mirror configured
// as a loopback literal is admitted by the concession too, and a test that
// cannot tell its two reasons apart pins neither. What it does show is that
// [ClientFor] assembles both halves into a client that actually reaches the
// operator's own mirror with no flag set, which is the deployment S08 exists to
// stop asking for LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES.
func TestOperatorNamedHostIsReachedEndToEnd(t *testing.T) {
	var reached atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	named := ClientFor(5*time.Second, NewPolicy([]string{srv.URL}, false))
	resp, err := get(t, named, srv.URL)
	if err != nil {
		t.Fatalf("Get() error = %v, want the operator's own mirror reached with no flag set", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
	if !reached.Load() {
		t.Error("the handler never ran, so nothing was actually dialed")
	}

	anonymous := ClientFor(5*time.Second, NewPolicy(nil, false))
	resp, err = get(t, anonymous, srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("the same address was reached with no operator-named host, so naming one decides nothing")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("err = %v, want ErrBlockedAddress", err)
	}
}

// TestNamingAMetadataAddressDoesNotOpenIt is the strongest form of tier A: an
// operator who names a metadata endpoint outright, with the private-address flag
// on as well, still cannot reach it.
//
// Tier A is checked before any policy is read, and this is what that ordering is
// worth. The exemption exists so a deployment can reach a machine it owns, and
// nothing in that intent covers handing out the cloud credentials of the machine
// this server runs on — which is what a metadata endpoint answers with, to
// whoever asks.
func TestNamingAMetadataAddressDoesNotOpenIt(t *testing.T) {
	p := NewPolicy([]string{"http://169.254.169.254"}, true)
	if !p.Names("169.254.169.254") {
		t.Fatal("the fixture did not name the metadata address, so this test checks nothing")
	}

	if err := dialWith(t, p, "169.254.169.254", "169.254.169.254:80"); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("dialer err = %v, want ErrBlockedAddress; naming the endpoint opened it", err)
	}
	if err := redirectTo(t, p, "https://libgen.li/get", "http://169.254.169.254/latest/meta-data/"); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("redirect err = %v, want ErrBlockedAddress; naming the endpoint opened the redirect path", err)
	}
}

// TestPrivateSiblingIsAdmittedOnlyWhenTheOperatorHostIsPrivate covers the one
// shape left between the two tiers: an operator's own mirror redirecting to a
// sibling host on the same private network.
//
// The concession is conditional on purpose. A deployment whose named mirror is
// itself inside a private network is already inside that network, so the hop
// reaches nowhere the operator could not. A deployment whose mirrors are all
// public has no such claim, and a redirect from one of them into RFC 1918 is
// precisely the attack: a third party's URL bouncing the server somewhere the
// depositor could never reach.
//
// Both halves of the client are asserted. The dialer is the authority and the
// redirect check only speaks earlier, so the two disagreeing would mean a hop
// refused with a legible message that would have been dialed, or worse.
func TestPrivateSiblingIsAdmittedOnlyWhenTheOperatorHostIsPrivate(t *testing.T) {
	const (
		mirror  = "mirror.operator.test"
		sibling = "http://192.168.1.9/file.pdf"
	)
	cases := []struct {
		name     string
		resolves []string
		admitted bool
	}{
		{name: "the named mirror is itself private", resolves: []string{"192.168.1.5"}, admitted: true},
		{name: "the named mirror is public", resolves: []string{"93.184.216.34"}, admitted: false},
		{
			// A name answering with one private and one public address is not a
			// private deployment. Taking the first would make the answer depend
			// on resolver ordering, which is not a decision anyone chose.
			name:     "the named mirror answers with both",
			resolves: []string{"192.168.1.5", "93.184.216.34"},
			admitted: false,
		},
		{
			// A resolver that answered with nothing has said nothing. It is the
			// same silence as a failure and gets the same verdict.
			name:     "the named mirror answers with nothing",
			resolves: []string{},
			admitted: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubResolver(t, map[string][]string{mirror: tc.resolves})
			p := NewPolicy([]string{"https://" + mirror}, false)

			dialErr := dialWith(t, p, "192.168.1.9", "192.168.1.9:80")
			redirectErr := redirectTo(t, p, "https://"+mirror+"/get", sibling)

			if tc.admitted {
				if dialErr != nil {
					t.Errorf("dialer err = %v, want the sibling admitted", dialErr)
				}
				if redirectErr != nil {
					t.Errorf("redirect err = %v, want the sibling admitted", redirectErr)
				}
				return
			}
			if !errors.Is(dialErr, ErrBlockedAddress) {
				t.Errorf("dialer err = %v, want ErrBlockedAddress", dialErr)
			}
			if !errors.Is(redirectErr, ErrBlockedAddress) {
				t.Errorf("redirect err = %v, want ErrBlockedAddress", redirectErr)
			}
		})
	}
}

// TestNamesPrivateHostDoesNotRememberSilence pins what the per-host memo may
// keep. A resolver that failed has said nothing about the host, and storing that
// as "not private" would turn one timeout into a permanent answer — for a
// deployment whose mirror IS private, which is the deployment the concession
// exists for.
func TestNamesPrivateHostDoesNotRememberSilence(t *testing.T) {
	const mirror = "mirror.operator.test"
	var answers atomic.Int64
	previous := lookupHostAddrs
	t.Cleanup(func() { lookupHostAddrs = previous })
	lookupHostAddrs = func(_ context.Context, host string) ([]netip.Addr, error) {
		if answers.Add(1) == 1 {
			return nil, errors.New("resolver unavailable")
		}
		return []netip.Addr{netip.MustParseAddr("192.168.1.5")}, nil
	}

	p := NewPolicy([]string{mirror}, false)
	if p.NamesPrivateHost(t.Context()) {
		t.Fatal("a resolver that could not answer left the host permitted; not knowing must be a no")
	}
	if !p.NamesPrivateHost(t.Context()) {
		t.Error("the failed lookup was memoized, so the host stays refused for the life of the process")
	}
}

// TestNamesPrivateHostAnswersALiteralWithoutAResolver covers the shape a mirror
// on a LAN usually takes — LIBGEN_MIRROR=http://192.168.1.5:8080 — which must be
// decided at construction. Reaching a resolver for it would make the answer
// depend on a lookup that cannot succeed, and would spend the budget that the
// names in the same set need.
func TestNamesPrivateHostAnswersALiteralWithoutAResolver(t *testing.T) {
	var asked atomic.Bool
	previous := lookupHostAddrs
	t.Cleanup(func() { lookupHostAddrs = previous })
	lookupHostAddrs = func(context.Context, string) ([]netip.Addr, error) {
		asked.Store(true)
		return nil, errors.New("no resolver in this test")
	}

	if !NewPolicy([]string{"http://192.168.1.5:8080"}, false).NamesPrivateHost(t.Context()) {
		t.Error("a mirror configured as a private address literal was not recognized as private")
	}
	if asked.Load() {
		t.Error("a resolver was consulted about an address literal")
	}
	if NewPolicy([]string{"https://libgen.li:443"}, false).privateLiteral {
		t.Error("a name was treated as an address literal")
	}
}

// TestPolicyTransportStampsEveryRequest is what makes the whole design work
// across a redirect chain.
//
// net/http hands each hop to the transport as a request of its own, so the stamp
// is remade per hop from that hop's URL: a request to the operator's mirror
// carries the exemption and a hop that leaves it does not, without anything
// having to track the chain. Both requests carry the same policy, so the client
// decides by the same rules all the way down.
func TestPolicyTransportStampsEveryRequest(t *testing.T) {
	policy := NewPolicy([]string{"https://mirror.operator.test"}, false)
	recorder := &recordingTransport{}
	transport := &policyTransport{base: recorder, policy: policy}

	for _, tc := range []struct {
		rawURL string
		named  bool
	}{
		{rawURL: "https://mirror.operator.test/get?md5=abc", named: true},
		{rawURL: "https://cdn.somebody-elses.test/file.pdf", named: false},
	} {
		t.Run(tc.rawURL, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, tc.rawURL, http.NoBody)
			if err != nil {
				t.Fatalf("NewRequestWithContext(%q) error = %v", tc.rawURL, err)
			}
			resp, rerr := transport.RoundTrip(req)
			if rerr != nil {
				t.Fatalf("RoundTrip(%q) error = %v", tc.rawURL, rerr)
			}
			_ = resp.Body.Close()
			decision, stamped := dialDecisionFrom(recorder.seen)
			if !stamped {
				t.Fatalf("%s reached the dialer with no decision stamped on it", tc.rawURL)
			}
			if decision.operatorNamed != tc.named {
				t.Errorf("%s: operatorNamed = %v, want %v", tc.rawURL, decision.operatorNamed, tc.named)
			}
			if decision.policy != policy {
				t.Errorf("%s: the request carries a different policy than the client was built with", tc.rawURL)
			}
		})
	}
}

// recordingTransport keeps the context of the last request it was handed and
// answers 204, so a round trip can be inspected without a server.
type recordingTransport struct {
	seen context.Context //nolint:containedctx // the context IS what this double exists to capture.
}

// RoundTrip records the request's context and returns an empty response.
func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.seen = req.Context()
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Body:       http.NoBody,
		Request:    req,
	}, nil
}

// TestOperatorPolicyFollowsARedirectChain is the end-to-end form of the stamp:
// a real client, a real server on a private address, and a redirect it actually
// follows.
//
// The hop is dialed a second time, from a second stamp, so a design that decided
// once per client — or once per chain — would fail here rather than in review.
func TestOperatorPolicyFollowsARedirectChain(t *testing.T) {
	var hops atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hops.Add(1) == 1 {
			http.Redirect(w, r, "/next", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	client := ClientFor(5*time.Second, NewPolicy([]string{srv.URL}, false))
	resp, err := get(t, client, srv.URL+"/start")
	if err != nil {
		t.Fatalf("Get() error = %v, want the redirect followed within the operator's own host", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
	if got := hops.Load(); got != 2 {
		t.Errorf("hops = %d, want 2; the redirect was not actually followed", got)
	}
}

// TestClientForKeepsBothHalvesUnderTheTestSeam pins the interaction the suites in
// every other package depend on. SetAllowPrivateForTest lifts the private tier
// for clients built afterwards, and a policy that carries no flag must not
// override it — which it would if the dialer read the policy's own setting
// instead of the client's effective one.
func TestClientForKeepsBothHalvesUnderTheTestSeam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	restore := SetAllowPrivateForTest(true)
	t.Cleanup(restore)

	resp, err := get(t, ClientFor(5*time.Second, NewPolicy(nil, false)), srv.URL)
	if err != nil {
		t.Fatalf("Get() error = %v, want the test seam to admit a loopback fixture", err)
	}
	_ = resp.Body.Close()
}

// TestOperatorHostsAreNotConsultedForAPublicDestination is the cheap negative:
// naming hosts must not change what happens to an ordinary public address, which
// is every request this server makes in practice.
func TestOperatorHostsAreNotConsultedForAPublicDestination(t *testing.T) {
	p := NewPolicy([]string{"https://libgen.li"}, false)
	if err := dialWith(t, p, "openlibrary.org", "104.18.32.7:443"); err != nil {
		t.Errorf("err = %v, want a public destination dialed", err)
	}
	if err := redirectTo(t, p, "https://libgen.li/get", "https://cdn.example.org/file.pdf"); err != nil {
		t.Errorf("err = %v, want a public redirect followed", err)
	}
}

// TestHostsIsACopyOfTheSet keeps the diagnostic accessor from handing out
// something a caller could edit the policy through.
func TestHostsIsACopyOfTheSet(t *testing.T) {
	p := NewPolicy([]string{"libgen.li", "sci-hub.ee"}, false)
	got := p.Hosts()
	slices.Sort(got)
	if want := []string{"libgen.li", "sci-hub.ee"}; !slices.Equal(got, want) {
		t.Fatalf("Hosts() = %v, want %v", got, want)
	}
	got[0] = "tampered.test"
	if p.Names("tampered.test") {
		t.Error("editing the returned slice changed the policy")
	}
}

// TestNilPolicyIsTheStrictOne states the zero value's answer. A nil policy
// reaches the guard from CheckRedirect's exported form and from any caller that
// has no set to hand in, and every question it is asked must resolve the safe
// way rather than panic.
func TestNilPolicyIsTheStrictOne(t *testing.T) {
	var p *Policy
	if p.Names("libgen.li") || p.AllowsPrivate() || p.NamesPrivateHost(t.Context()) || p.Hosts() != nil {
		t.Error("a nil policy answered as though it permitted something")
	}
	err := redirectTo(t, nil, "", "http://10.0.0.7/f.pdf")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("err = %v, want ErrBlockedAddress from the policy-free redirect check", err)
	}
	if !strings.Contains(err.Error(), "10.0.0.7") {
		t.Errorf("err = %v, want it to name the destination", err)
	}
}

// metadataCases are the four addresses tier A refuses, each with what it is.
// They are listed here rather than read out of metadataAddresses so the test
// fails when an entry is dropped from production rather than following it.
var metadataCases = []struct {
	name string
	addr string
}{
	{"the cloud instance metadata address", "169.254.169.254"},
	{"the AWS container credentials address", "169.254.170.2"},
	{"the AWS instance metadata address over IPv6", "fd00:ec2::254"},
	{"the Alibaba Cloud instance metadata address", "100.100.100.200"},
}

// hostPort renders an address for a dialer, bracketing IPv6.
func hostPort(addr string) string {
	if strings.Contains(addr, ":") {
		return "[" + addr + "]:80"
	}
	return addr + ":80"
}

// TestMetadataAddressesAreRefusedUnderTheAllowance is the regression test, and
// the allowance is what makes it one.
//
// LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES exists so an operator can point this server
// at a mirror on their own network. Before this, setting it also opened
// 169.254.169.254 to every third-party URL in the download chain, because the
// dialer hook was not installed at all when the flag was set — so a URL
// deposited in an open-access index could make the server fetch the cloud
// credentials of the machine it runs on and hand them back as a file.
//
// Asserting the refusal with the flag OFF proves nothing: those addresses are
// link-local, shared-space or ULA, so Blocked already covered every one of them.
// The flag has to be on for this to discriminate.
func TestMetadataAddressesAreRefusedUnderTheAllowance(t *testing.T) {
	hook := control(true)
	if hook == nil {
		t.Fatal("control(true) returned no hook, so nothing can be refused")
	}

	for _, tc := range metadataCases {
		t.Run(tc.addr, func(t *testing.T) {
			err := hook(t.Context(), "tcp", hostPort(tc.addr), nil)
			if err == nil {
				t.Fatalf("dialing %s was permitted under the private-address allowance", tc.addr)
			}
			if !errors.Is(err, ErrBlockedAddress) {
				t.Errorf("err = %v, want it to wrap ErrBlockedAddress so a caller can tell this from a dial failure", err)
			}
			if !strings.Contains(err.Error(), tc.name) {
				t.Errorf("err = %v, want it to say what the address is (%q)", err, tc.name)
			}
		})
	}
}

// TestMetadataAddressesAreRefusedWhenMappedIntoIPv6 covers the standard way past
// a filter that only reasons about one address family: ::ffff:169.254.169.254 is
// the same endpoint wearing an IPv6 coat.
func TestMetadataAddressesAreRefusedWhenMappedIntoIPv6(t *testing.T) {
	mapped := netip.AddrFrom16(netip.MustParseAddr("169.254.169.254").As16())
	if mapped.Unmap().Is4() != true {
		t.Fatal("the fixture is not a v4-mapped address, so this test checks nothing")
	}

	err := control(true)(t.Context(), "tcp", "["+mapped.String()+"]:80", nil)
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("err = %v, want the mapped form refused as the address it is", err)
	}
}

// TestMetadataRedirectIsRefusedUnderTheAllowance covers the cheaper half of the
// same attack. A URL in an index need only redirect once, and the redirect check
// short-circuited on the same flag the dialer did, so the two halves of one
// client have to agree about which tier is unconditional.
func TestMetadataRedirectIsRefusedUnderTheAllowance(t *testing.T) {
	check := CheckRedirect(true)

	for _, tc := range metadataCases {
		t.Run(tc.addr, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "http://"+hostPort(tc.addr)+"/latest/meta-data/", nil) //nolint:noctx // the request is never sent; only its URL is inspected.
			if err != nil {
				t.Fatal(err)
			}
			rerr := check(req, nil)
			if rerr == nil {
				t.Fatalf("a redirect to %s was followed under the private-address allowance", tc.addr)
			}
			if !errors.Is(rerr, ErrBlockedAddress) {
				t.Errorf("err = %v, want it to wrap ErrBlockedAddress", rerr)
			}
		})
	}
}

// TestPrivateRedirectsStillFollowedUnderTheAllowance is the other side of the
// pin: widening the refusal must not close the case the flag exists for. An
// operator's own mirror redirecting within their own network still works.
func TestPrivateRedirectsStillFollowedUnderTheAllowance(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://10.0.0.7/file.pdf", nil) //nolint:noctx // the request is never sent; only its URL is inspected.
	if err != nil {
		t.Fatal(err)
	}
	if rerr := CheckRedirect(true)(req, nil); rerr != nil {
		t.Errorf("a redirect to a private address was refused (%v); that is what the flag permits", rerr)
	}
}

// TestSetAllowPrivateForTestDoesNotLiftTheMetadataTier pins the seam itself. The
// unit suites flip it once per binary to reach their loopback fixtures, and a
// seam that could also switch off the tier which holds unconditionally would be
// a way to reach production with that tier off.
func TestSetAllowPrivateForTestDoesNotLiftTheMetadataTier(t *testing.T) {
	restore := SetAllowPrivateForTest(true)
	t.Cleanup(restore)

	client := Client(0, false)

	// The round trip is attempted for real, through the whole transport the
	// client ships with rather than the dialer alone, so the per-request stamp is
	// on the path too. Nothing answers on a metadata address from a test runner,
	// so the assertion is on which error comes back: the guard's, not the
	// network's.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://169.254.169.254/latest/meta-data/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Transport.RoundTrip(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("the metadata address was reached under the test seam")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("err = %v, want ErrBlockedAddress; the test seam lifted the metadata tier", err)
	}
}
