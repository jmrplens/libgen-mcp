package netguard

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

// TestRedactURLString verifies a URL named in an error never carries the three
// parts a secret hides in. A resolved file URL can be signed with an account key
// or a one-time token, an endpoint can carry a member key or a contact address in
// its query, and error text travels into logs and into a model's context.
func TestRedactURLString(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"drops a signed query", "https://cdn.example.org/f.pdf?key=s3cret&exp=1", "https://cdn.example.org/f.pdf"},
		{"drops a fragment", "https://e.org/f.pdf#page=2", "https://e.org/f.pdf"},
		{"drops the userinfo", "https://user:s3cret@e.org/f.pdf", "https://e.org/f.pdf"},
		{"drops all three at once", "https://user:s3cret@e.org/f.pdf?key=s3cret#p=2", "https://e.org/f.pdf"},
		{"keeps a plain url", "https://e.org/f.pdf", "https://e.org/f.pdf"},
		{"keeps the port", "https://e.org:8443/f.pdf?key=s3cret", "https://e.org:8443/f.pdf"},
		{"unparseable is still truncated", "://nope?key=s3cret", "://nope"},
		{"a bare path names no host and is truncated", "/local/f.pdf?key=s3cret", "/local/f.pdf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactURLString(tt.in)
			if got != tt.want {
				t.Errorf("RedactURLString(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if strings.Contains(got, "s3cret") && !strings.Contains(tt.want, "s3cret") {
				t.Errorf("RedactURLString(%q) leaked the secret", tt.in)
			}
		})
	}
}

// TestRedactURLHandlesNil pins the nil case, because a *url.Error built by hand
// can carry one and a panic in the redaction path would be a worse failure than
// the leak it exists to stop.
func TestRedactURLHandlesNil(t *testing.T) {
	if got := RedactURL(nil); got != "" {
		t.Errorf("RedactURL(nil) = %q, want empty", got)
	}
}

// redactionSecret is the value every case below plants in the part of the URL it
// is testing, so an assertion can say "this must not come back out".
const redactionSecret = "s3cret"

// redacted runs err through RedactTransportError and returns the *url.Error it
// must produce, failing the test when it produces anything else.
func redacted(t *testing.T, err error) *url.Error {
	t.Helper()
	got := RedactTransportError(err)

	var out *url.Error
	if !errors.As(got, &out) {
		t.Fatalf("RedactTransportError returned %T, want *url.Error", got)
	}
	return out
}

// TestRedactTransportErrorDropsTheThreeSecretParts covers what the helper exists
// for: the userinfo, the query and the fragment go, the scheme, host and path stay,
// and the operation the failure was is still named.
func TestRedactTransportErrorDropsTheThreeSecretParts(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"query", "https://annas.invalid/dyn/api/fast_download.json?md5=abc&key=" + redactionSecret, "https://annas.invalid/dyn/api/fast_download.json"},
		{"fragment", "https://api.unpaywall.invalid/v2/10.1/x#" + redactionSecret, "https://api.unpaywall.invalid/v2/10.1/x"},
		{"userinfo", "https://u:" + redactionSecret + "@cdn.invalid/f.pdf", "https://cdn.invalid/f.pdf"},
		{"all three", "https://u:" + redactionSecret + "@cdn.invalid/f.pdf?key=" + redactionSecret + "#" + redactionSecret, "https://cdn.invalid/f.pdf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := &url.Error{Op: "Get", URL: tt.raw, Err: errors.New("connection refused")}
			out := redacted(t, in)

			if out.URL != tt.want {
				t.Errorf("URL = %q, want %q", out.URL, tt.want)
			}
			if strings.Contains(out.Error(), redactionSecret) {
				t.Errorf("the rendered error still carries the secret: %v", out)
			}
			if out.Op != in.Op {
				t.Errorf("Op = %q, want %q", out.Op, in.Op)
			}
		})
	}
}

// TestRedactTransportErrorPassesOtherErrorsThrough pins that the helper is safe to
// put in front of any error: only a *url.Error is rebuilt, and everything else —
// including nil — comes back as the same value it went in as.
func TestRedactTransportErrorPassesOtherErrorsThrough(t *testing.T) {
	in := errors.New("plain")
	//nolint:errorlint // identity is the assertion: the value must come back unchanged, not merely be findable in a chain.
	if got := RedactTransportError(in); got != in {
		t.Errorf("RedactTransportError(%v) = %v, want the same error value", in, got)
	}
	if got := RedactTransportError(nil); got != nil {
		t.Errorf("RedactTransportError(nil) = %v, want nil", got)
	}
}

// TestRedactTransportErrorKeepsTheCauseMatchable covers the reason the replacement
// is a rebuilt *url.Error rather than a fresh fmt.Errorf: a caller classifying the
// failure must still find what it was.
//
// Timeout matters on the same ground. (*url.Error).Timeout delegates to its cause,
// and the source chain reads a timeout as the source being unavailable rather than
// as a verdict on the item, so losing it would silently reroute the failover.
func TestRedactTransportErrorKeepsTheCauseMatchable(t *testing.T) {
	in := &url.Error{Op: "Get", URL: "https://e.invalid/x?key=" + redactionSecret, Err: context.DeadlineExceeded}
	out := redacted(t, in)

	if !errors.Is(out, context.DeadlineExceeded) {
		t.Error("errors.Is no longer finds the wrapped cause through the replacement")
	}
	if !out.Timeout() {
		t.Error("Timeout() is false after redaction, was true before")
	}
	if strings.Contains(out.Error(), redactionSecret) {
		t.Errorf("the rendered error still carries the secret: %v", out)
	}
}

// TestRedactTransportErrorLeavesTheCauseAlone pins the reason the regression tests
// elsewhere in this repository drive their transports with a URL-free error.
//
// The helper rebuilds the *url.Error around the original cause, and
// (*url.Error).Error renders that cause too, so a cause which itself names the URL
// still shows it. That is deliberate: rewriting the cause would destroy the type a
// caller matches with errors.Is or errors.As, which is the whole reason the
// replacement keeps it. No real transport failure names a query string — a
// *net.OpError, a *net.DNSError, a TLS error and a deadline all name a host and a
// port at most — but a test double can, and a test built on one would pass before
// and after the fix while pinning nothing.
func TestRedactTransportErrorLeavesTheCauseAlone(t *testing.T) {
	cause := errors.New("refused https://e.invalid/x?key=" + redactionSecret)
	in := &url.Error{Op: "Get", URL: "https://e.invalid/x?key=" + redactionSecret, Err: cause}
	out := redacted(t, in)

	if !errors.Is(out, cause) {
		t.Error("the cause was replaced; errors.Is no longer finds it")
	}
	if out.URL != "https://e.invalid/x" {
		t.Errorf("URL = %q, want the redacted form", out.URL)
	}
	if !strings.Contains(out.Error(), redactionSecret) {
		t.Error("a cause naming the URL no longer shows it; the helper is rewriting causes, " +
			"which breaks the type a caller matches on")
	}
}

// TestRedactTransportErrorMatchesTheClientShape pins the shape the helper is
// written for: what (*http.Client).Do returns is a *url.Error whose message is the
// whole request URL, which is the mechanism the redaction exists to defeat.
func TestRedactTransportErrorMatchesTheClientShape(t *testing.T) {
	raw := "https://annas.invalid/dyn/api/fast_download.json?md5=abc&key=" + redactionSecret
	// This is how net/http builds it: the URL string, verbatim, in the message.
	asClientReturnsIt := &url.Error{Op: "Get", URL: raw, Err: errors.New("dial tcp: connection refused")}

	if !strings.Contains(asClientReturnsIt.Error(), redactionSecret) {
		t.Fatal("premise broken: a *url.Error no longer renders its URL, so the helper guards nothing")
	}
	got := RedactTransportError(asClientReturnsIt).Error()
	if strings.Contains(got, redactionSecret) {
		t.Errorf("the redacted error still carries the key: %s", got)
	}
	if !strings.Contains(got, "annas.invalid") {
		t.Errorf("the redacted error no longer names the endpoint that failed: %s", got)
	}
}
