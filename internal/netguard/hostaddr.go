// Which address, if any, a URL's host spells.

package netguard

import (
	"errors"
	"net/netip"
	"strconv"
	"strings"
)

// errNumericHost is the reason a host that looks numeric but is no address
// this package can parse is refused. It never stands alone: the refusal wraps
// [ErrBlockedAddress] beside it.
var errNumericHost = errors.New("a numeric host this server cannot classify")

// addressLiteral reports the address host spells, whether it spells one at all,
// and an error for a host that looks numeric without parsing as an address.
//
// A host that is a name is nobody's decision to make before the dialer: what it
// resolves to is the only thing worth judging. But "is a name" has to be decided
// the way the resolver on the far side decides it, not the way netip does.
// Behind a proxy, or anywhere a C resolver sees the host, getaddrinfo reads it
// through inet_aton, which accepts one to four dot-separated parts in decimal,
// octal or hex. 2852039166, 0xa9fea9fe, 0251.0376.0251.0376 and 169.254.43518
// are each 169.254.169.254 to that resolver, and netip rejects all four, so a
// check that asked netip alone passed them on as names.
//
// Two shapes are refused rather than parsed. A host whose last label is all
// digits, or 0x and hex digits, and that is still not a valid inet_aton form
// (a part out of range, five parts, an empty part): no registered name ends in
// such a label, and a resolver that disagrees with this function about it is
// the one case that matters. And a host carrying a colon that netip does not
// accept as IPv6, which is not a hostname either.
func addressLiteral(host string) (netip.Addr, bool, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr, true, nil
	}
	if strings.Contains(host, ":") {
		return netip.Addr{}, false, errNumericHost
	}
	// A trailing dot makes a name absolute, and a resolver that strips it reads
	// what is left: the address is judged without it.
	trimmed := strings.TrimSuffix(host, ".")
	if addr, ok := inetAton(trimmed); ok {
		return addr, true, nil
	}
	if numericLabel(trimmed[strings.LastIndex(trimmed, ".")+1:]) {
		return netip.Addr{}, false, errNumericHost
	}
	return netip.Addr{}, false, nil
}

// inetAton parses s the way the C library's inet_aton does: one to four parts
// separated by dots, each decimal, octal (a leading 0) or hex (a leading 0x),
// with the last part filling every byte the earlier ones left. So a.b.c.d is
// four bytes, a.b.c puts c in the low sixteen bits, a.b puts b in the low
// twenty-four, and a single part is the whole address.
func inetAton(s string) (netip.Addr, bool) {
	parts := strings.Split(s, ".")
	if len(parts) > 4 {
		return netip.Addr{}, false
	}
	var value uint32
	for i, part := range parts {
		n, ok := atonPart(part)
		if !ok {
			return netip.Addr{}, false
		}
		if i < len(parts)-1 {
			if n > 0xff {
				return netip.Addr{}, false
			}
			value |= uint32(n) << (24 - 8*i)
			continue
		}
		if remaining := 32 - 8*i; remaining < 32 && n >= 1<<remaining {
			return netip.Addr{}, false
		}
		value |= uint32(n) //nolint:gosec // atonPart parses with bitSize 32, so n always fits.
	}
	return netip.AddrFrom4([4]byte{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value)}), true
}

// atonPart parses one inet_aton part, which fits in 32 bits whatever its base.
func atonPart(part string) (uint64, bool) {
	digits, base := part, 10
	switch {
	case len(part) > 1 && (part[:2] == "0x" || part[:2] == "0X"):
		digits, base = part[2:], 16
	case len(part) > 1 && part[0] == '0':
		digits, base = part[1:], 8
	}
	if digits == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(digits, base, 32)
	return n, err == nil
}

// numericLabel reports whether a DNS label is all decimal digits, or 0x
// followed by hex digits, which is the shape of an inet_aton part and of no
// top-level domain.
func numericLabel(label string) bool {
	if label == "" {
		return false
	}
	digits, isDigit := label, func(r rune) bool { return r >= '0' && r <= '9' }
	if len(label) > 1 && (label[:2] == "0x" || label[:2] == "0X") {
		digits = label[2:]
		isDigit = func(r rune) bool {
			return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		}
	}
	for _, r := range digits {
		if !isDigit(r) {
			return false
		}
	}
	return true
}
