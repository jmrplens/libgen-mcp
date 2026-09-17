package netguard

import (
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

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
			err := hook("tcp", hostPort(tc.addr), nil)
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

	err := control(true)("tcp", "["+mapped.String()+"]:80", nil)
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
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("the client's transport is not an *http.Transport")
	}
	if transport.DialContext == nil {
		t.Fatal("the client has no dialer to check")
	}

	// The dial is attempted for real. Nothing answers on a metadata address from
	// a test runner, so the assertion is on which error comes back: the guard's,
	// not the network's.
	_, err := transport.DialContext(t.Context(), "tcp", "169.254.169.254:80")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("err = %v, want ErrBlockedAddress; the test seam lifted the metadata tier", err)
	}
}
