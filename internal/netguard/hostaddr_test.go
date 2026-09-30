package netguard

import (
	"errors"
	"net/netip"
	"testing"
)

// TestAddressLiteralReadsHostsAsACResolverDoes pins the parse to inet_aton,
// which is what getaddrinfo applies to a host before it asks DNS. Every
// spelling here that is an address to the C library must be one here too, or
// it is forwarded to a proxy as a name and resolved there to the address this
// package would have refused.
func TestAddressLiteralReadsHostsAsACResolverDoes(t *testing.T) {
	for _, tc := range []struct {
		host    string
		want    string // "" for a name
		refused bool
	}{
		{host: "169.254.169.254", want: "169.254.169.254"},
		{host: "2852039166", want: "169.254.169.254"},
		{host: "0xa9fea9fe", want: "169.254.169.254"},
		{host: "0XA9FEA9FE", want: "169.254.169.254"},
		{host: "0251.0376.0251.0376", want: "169.254.169.254"},
		{host: "169.254.43518", want: "169.254.169.254"},
		{host: "169.16689662", want: "169.254.169.254"},
		{host: "0xa9.0xfe.0xa9.0xfe", want: "169.254.169.254"},
		{host: "2852039166.", want: "169.254.169.254"},
		{host: "0", want: "0.0.0.0"},
		{host: "127.1", want: "127.0.0.1"},
		{host: "::ffff:a9fe:a9fe", want: "::ffff:169.254.169.254"},
		{host: "fd00:ec2::254", want: "fd00:ec2::254"},
		{host: "books.example.test"},
		{host: "169.254.169.254.nip.test"},
		{host: "0x.example.test"},
		{host: "example.com."},
		{host: "4294967296", refused: true},
		{host: "1.2.3.4.5", refused: true},
		{host: "1.2.3.256", refused: true},
		{host: "1.2.65536", refused: true},
		{host: "256.1", refused: true},
		{host: "08.1.1.1", refused: true},
		{host: "1..1", refused: true},
		{host: "0x", refused: true},
		{host: "example.0x1f", refused: true},
		{host: "::ffff:0251.0376.0251.0376", refused: true},
	} {
		t.Run(tc.host, func(t *testing.T) {
			addr, isAddress, err := addressLiteral(tc.host)
			if tc.refused {
				if !errors.Is(err, errNumericHost) {
					t.Errorf("addressLiteral(%q) = %v, %v, %v; want it refused as numeric", tc.host, addr, isAddress, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("addressLiteral(%q) error = %v", tc.host, err)
			}
			if tc.want == "" {
				if isAddress {
					t.Errorf("addressLiteral(%q) = %v, want it read as a name", tc.host, addr)
				}
				return
			}
			if !isAddress || addr != netip.MustParseAddr(tc.want) {
				t.Errorf("addressLiteral(%q) = %v, %v; want %s", tc.host, addr, isAddress, tc.want)
			}
		})
	}
}

// TestNumericLabelWithoutDigits covers the one shape the table cannot reach
// through addressLiteral: an empty label, which a host never ends in once its
// trailing dot is trimmed, and which is not numeric.
func TestNumericLabelWithoutDigits(t *testing.T) {
	if numericLabel("") {
		t.Error("an empty label was read as numeric")
	}
}
