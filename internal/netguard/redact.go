package netguard

import (
	"errors"
	"net/url"
	"strings"
)

// RedactURL renders u with the three parts a secret can hide in removed: the
// userinfo, the query and the fragment. What survives is the scheme, the host
// and the path, which is what identifies the destination.
//
// Each of the three dropped parts is somewhere a credential is routinely
// written: a proxy login in the userinfo, a signed token or an account key in
// the query, a fragment carried over from a browser. None of them identifies
// the destination, so none of them is worth echoing into a log line or a
// model's context.
//
// The path survives because it names the endpoint that failed, which is the
// useful half of the diagnostic. A nil URL renders empty.
func RedactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

// RedactURLString parses raw and renders it through [RedactURL], so a URL can be
// named in an error without carrying whatever credential it was signed with.
//
// A URL that will not parse, or that names no host, is truncated at the first
// "?" or "#" textually rather than echoed: falling back to the raw string would
// defeat the whole point on exactly the inputs least likely to be well formed.
func RedactURLString(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.SplitN(strings.SplitN(raw, "?", 2)[0], "#", 2)[0]
	}
	return RedactURL(u)
}

// RedactTransportError replaces the URL inside a *url.Error with the origin and
// path [RedactURL] keeps, dropping the userinfo, the query and the fragment.
// Anything that is not a *url.Error is returned unchanged.
//
// This exists because net/http returns every transport-level failure from
// (*http.Client).Do as a *url.Error whose Error() prints the full request URL,
// query string included. Any caller that wraps such an error with %w and later
// renders it publishes whatever the query carried — and this server writes those
// errors to two sinks at once, the operator's log stream and the model's
// transcript. A source that must put a secret in the query string (Anna's member
// key, the Unpaywall address) therefore routes the failure through here before
// wrapping it.
//
// The replacement is a rebuilt *url.Error carrying the original Op and cause, so
// Unwrap, Timeout and Temporary keep working and a caller matching the cause with
// errors.Is or errors.As still finds it. The cause is left untouched: rewriting
// it would destroy the very type such a caller matches on, so an inner error that
// names the URL itself still shows it. Real transport failures do not — a
// *net.OpError, a *net.DNSError, a TLS error or a deadline name a host and a
// port, never a query string — but a test double can, which is why the regression
// tests use a transport whose error names no URL.
//
// Apply it to the error as the client returned it, before any wrapping: the
// value handed back is the *url.Error itself, so an outer wrapper would be lost.
func RedactTransportError(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	return &url.Error{Op: ue.Op, URL: RedactURLString(ue.URL), Err: ue.Err}
}
