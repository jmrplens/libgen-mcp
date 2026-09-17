package libgen

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// The sentinels below are the values the assertions hunt for. Each appears
// nowhere else in the tree, so a match in an error message or a log record can
// only have come from the request URL the test provoked a failure on.
// The email's local part is asserted on rather than the whole address, and it is
// the half that survives escaping: the endpoint builds the query with
// url.QueryEscape, so the "@" arrives as "%40" and a search for the address as
// written would find nothing whether or not the address leaked. That is a test
// which passes before the fix and after it, which is no test at all.
const (
	annasKeySentinel   = "k3yAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	unpaywallLocalPart = "m41lAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	unpaywallSentinel  = unpaywallLocalPart + "@example.test"
	signedURLSentinel  = "t0k3nAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	redactionTestMD5   = "d41d8cd98f00b204e9800998ecf8427e"
	redactionTestDOI   = "10.1000/redaction"
	redactionTestHost  = "annas.invalid"
	unpaywallTestHost  = "api.unpaywall.invalid"
	signedURLTestHost  = "cdn.invalid"
	redactionSentinels = "the per-call secret"
)

// urlFreeRefusingTransport fails every request without naming the URL it was
// given, which is what refusingTransport does and what makes it unusable here.
//
// The redaction rebuilds the *url.Error around the original cause, and
// (*url.Error).Error renders that cause too, so a transport whose own message
// carries the URL leaks the sentinel out of the inner error no matter how well the
// outer one is redacted — and a test built on it would fail before the fix and
// after it, pinning nothing. A real transport failure is a *net.OpError, a
// *net.DNSError, a TLS error or a deadline, and none of those prints a query
// string, so this double is the faithful one for a leak assertion.
type urlFreeRefusingTransport struct{}

// RoundTrip refuses the request without reporting anything about it.
func (urlFreeRefusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("refused")
}

// urlFreeRefusingClient returns an HTTP client backed by urlFreeRefusingTransport.
func urlFreeRefusingClient() *http.Client {
	return &http.Client{Transport: urlFreeRefusingTransport{}}
}

// assertNoSentinel fails the test when text carries the secret, reporting the
// whole of it so the leak can be read rather than guessed at.
func assertNoSentinel(t *testing.T, text, sentinel, where string) {
	t.Helper()
	if strings.Contains(text, sentinel) {
		t.Errorf("%s leaked %s:\n%s", where, redactionSentinels, text)
	}
}

// assertNames fails the test when text does not name want, which is what keeps a
// redacted diagnostic worth having: dropping the query must not drop the endpoint.
func assertNames(t *testing.T, text, want, where string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Errorf("%s no longer names %q, so the diagnostic lost its subject:\n%s", where, want, text)
	}
}

// TestAnnasMemberFailureDoesNotLeakTheKey is the regression test for the leak this
// file exists for. resolveViaMemberAPI puts the member key in the query string, and
// net/http reports a transport failure as a *url.Error whose message is the whole
// URL, so before the redaction a refused connection wrote the caller's key into
// every sink the error reached.
func TestAnnasMemberFailureDoesNotLeakTheKey(t *testing.T) {
	s := annasSource{
		mirrors: staticMirrors{"https://" + redactionTestHost},
		http:    urlFreeRefusingClient(),
		key:     annasKeySentinel,
	}

	_, err := s.Resolve(context.Background(), Item{MD5: redactionTestMD5})
	if err == nil {
		t.Fatal("every request is refused, so Resolve must fail")
	}
	assertNoSentinel(t, err.Error(), annasKeySentinel, "the Anna's member failure")
	assertNames(t, err.Error(), redactionTestHost, "the Anna's member failure")
}

// TestUnpaywallFailureDoesNotLeakTheEmail covers the second per-call secret: the
// contact address rides in the query string exactly as the member key does.
func TestUnpaywallFailureDoesNotLeakTheEmail(t *testing.T) {
	s := unpaywallSource{
		email:   unpaywallSentinel,
		http:    urlFreeRefusingClient(),
		baseURL: "https://" + unpaywallTestHost,
	}

	_, err := s.Resolve(context.Background(), Item{DOI: redactionTestDOI})
	if err == nil {
		t.Fatal("every request is refused, so Resolve must fail")
	}
	assertNoSentinel(t, err.Error(), unpaywallLocalPart, "the Unpaywall failure")
	assertNames(t, err.Error(), redactionTestDOI, "the Unpaywall failure")
}

// TestUnpaywallPerCallEmailDoesNotLeakEither pins the door the elicitation prompt
// opens: a caller who answers the unpaywall_email prompt supplies the address as a
// tool argument, and that value reaches the same query string as the configured
// one.
func TestUnpaywallPerCallEmailDoesNotLeakEither(t *testing.T) {
	s := unpaywallSource{
		http:    urlFreeRefusingClient(),
		baseURL: "https://" + unpaywallTestHost,
	}

	_, err := s.Resolve(context.Background(), Item{DOI: redactionTestDOI, Email: unpaywallSentinel})
	if err == nil {
		t.Fatal("every request is refused, so Resolve must fail")
	}
	assertNoSentinel(t, err.Error(), unpaywallLocalPart, "the Unpaywall per-call failure")
}

// TestFetchFileFailureDoesNotLeakTheSignedURL covers the third secret, which is the
// URL itself. What the member API hands back is a time-limited presigned URL, so a
// transport failure mid-fetch publishes a working credential rather than a hint of
// one.
func TestFetchFileFailureDoesNotLeakTheSignedURL(t *testing.T) {
	c := newTestClient(staticMirrors{})
	c.dl = urlFreeRefusingClient()

	signed := "https://" + signedURLTestHost + "/f.pdf?token=" + signedURLSentinel
	resp, err := c.fetchFile(context.Background(), signed, 0, nil)
	if err == nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		t.Fatal("every request is refused, so fetchFile must fail")
	}
	assertNoSentinel(t, err.Error(), signedURLSentinel, "the download failure")
	assertNames(t, err.Error(), signedURLTestHost, "the download failure")
}

// TestSourceAttemptLogDoesNotLeakTheKey drives the whole failover chain and reads
// the operator's log stream, which is the sink the source-level tests above cannot
// see. downloadFrom hands every failed attempt to logging.SourceAttempt, which
// writes the error as a structured field, so the key reached stderr on any
// transport failure whether or not anything else rendered it.
func TestSourceAttemptLogDoesNotLeakTheKey(t *testing.T) {
	c := newTestClient(staticMirrors{})
	c.sources = []DownloadSource{annasSource{
		mirrors: staticMirrors{"https://" + redactionTestHost},
		http:    urlFreeRefusingClient(),
		key:     annasKeySentinel,
	}}
	buf := captureLog(t)

	_, err := c.DownloadItem(context.Background(), Item{MD5: redactionTestMD5}, t.TempDir(), "")
	if err == nil {
		t.Fatal("every request is refused, so DownloadItem must fail")
	}

	assertNoSentinel(t, buf.String(), annasKeySentinel, "the source-attempt log record")
	assertNames(t, buf.String(), "source failed, advancing", "the source-attempt log record")
	assertNoSentinel(t, err.Error(), annasKeySentinel, "the joined download failure")
}
