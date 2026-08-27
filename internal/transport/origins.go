package transport

import (
	"fmt"
	"net/url"
	"strings"
)

// AnyOrigin is the --trusted-origins value that turns cross-origin protection
// off for browsers: every origin is answered as trusted. It exists because a
// deployment behind its own gateway may already decide this, but it is never
// the default and the server says so at startup.
const AnyOrigin = "*"

// ParseTrustedOrigins splits a comma-separated --trusted-origins value into
// absolute origins ("scheme://host[:port]"), rejecting anything that is not
// one.
//
// A malformed entry fails rather than being dropped, because the failure it
// would otherwise cause is silent and the wrong way round: an operator who
// believes an origin is trusted, and whose browser clients are refused anyway,
// has no signal to look at. Refusing to start is the louder, cheaper error.
//
// The comparison net/http performs is on the literal origin string, so a
// trailing slash or a path is not a harmless typo — it can never match an
// Origin header, and is refused here for that reason rather than trimmed into
// something the caller did not write.
func ParseTrustedOrigins(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if raw == AnyOrigin {
		return []string{AnyOrigin}, nil
	}
	var out []string
	for part := range strings.SplitSeq(raw, ",") {
		origin := strings.TrimSpace(part)
		if origin == "" {
			continue
		}
		if origin == AnyOrigin {
			return nil, fmt.Errorf("trusted origin %q: %s is only valid on its own, not in a list", origin, AnyOrigin)
		}
		if err := validateOrigin(origin); err != nil {
			return nil, err
		}
		out = append(out, origin)
	}
	return out, nil
}

// validateOrigin reports whether origin is an absolute scheme://host[:port]
// with nothing after the host, which is the only shape an Origin header takes.
func validateOrigin(origin string) error {
	u, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("trusted origin %q: %w", origin, err)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Errorf("trusted origin %q: want an http or https scheme", origin)
	case u.Host == "":
		return fmt.Errorf("trusted origin %q: want scheme://host[:port]", origin)
	case u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return fmt.Errorf("trusted origin %q: want scheme://host[:port] with no path, query, fragment or userinfo", origin)
	}
	return nil
}

// Trusts reports whether origins covers the given Origin header value.
func Trusts(origins []string, origin string) bool {
	if origin == "" {
		return false
	}
	for _, o := range origins {
		if o == AnyOrigin || o == origin {
			return true
		}
	}
	return false
}
