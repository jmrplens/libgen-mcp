package libgen

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// taxonomyDOI carries the bioRxiv preprint prefix so every prefix-agnostic DOI
// source — plus the prefix-restricted biorxiv one — claims the item under test.
const taxonomyDOI = "10.1101/2020.12.30.424878"

// taxonomyDOIFor returns the DOI to offer a named source, which is taxonomyDOI for
// every source that claims any DOI and the source's own registrant otherwise. A
// prefix-restricted source declines taxonomyDOI outright, so offering it one would
// test the prefix check rather than the error taxonomy the suite is about.
func taxonomyDOIFor(name string) string {
	switch name {
	case "dagstuhl":
		return "10.4230/LIPIcs.ICALP.2023.1"
	case "zenodo":
		return "10.5281/zenodo.3233986"
	default:
		return taxonomyDOI
	}
}

// doiSourceFor builds the named DOI source with every upstream it talks to pointed
// at srv, so one local server decides the whole outcome of a Resolve. It fails the
// test for an unknown name rather than silently testing nothing.
func doiSourceFor(t *testing.T, name string, srv *httptest.Server) DownloadSource {
	t.Helper()
	cl := srv.Client()
	switch name {
	case "unpaywall":
		return unpaywallSource{email: "mail@jmrp.io", http: cl, baseURL: srv.URL}
	case "europepmc":
		return europePMCSource{http: cl, searchBase: srv.URL, renderBase: srv.URL}
	case "biorxiv":
		return biorxivSource{http: cl, apiBase: srv.URL, contentBase: srv.URL}
	case "dagstuhl":
		return dagstuhlSource{http: cl, baseURL: srv.URL}
	case "zenodo":
		return zenodoSource{http: cl, baseURL: srv.URL}
	case "fatcat":
		return fatcatSource{http: cl, baseURL: srv.URL}
	case "core":
		return coreSource{http: cl, key: "test-key", apiBase: srv.URL}
	case "scihub":
		return scihubSource{http: cl, hosts: []string{strings.TrimPrefix(srv.URL, "http://")}, scheme: "http"}
	case "scidb":
		return scidbSource{http: cl, mirrors: staticMirrors{srv.URL}}
	default:
		t.Fatalf("no DOI source named %q", name)
		return nil
	}
}

// fixedResponse serves status and body for every request, so a source's whole
// lookup path sees one deliberate upstream answer.
func fixedResponse(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// doiSourceNames lists every DOI source that talks to an upstream, in chain order,
// so the taxonomy tests cover the whole article chain rather than a sample of it.
//
// rfc, nist and acl are deliberately absent: each derives its file URL from the DOI
// by string manipulation and issues no request at all, so there is no upstream whose
// failure could be misclassified. Their "not my registrant" refusal is covered in
// their own tests.
var doiSourceNames = []string{
	"unpaywall", "europepmc", "biorxiv", "dagstuhl", "zenodo", "fatcat", "core", "scihub", "scidb",
}

// TestUnavailableUpstreamIsDiagnosedAsSuch verifies every DOI source reports a
// server that answers 503 as ErrSourceUnavailable: the failure says nothing about
// the item, so the source itself is what went wrong.
func TestUnavailableUpstreamIsDiagnosedAsSuch(t *testing.T) {
	for _, name := range doiSourceNames {
		t.Run(name, func(t *testing.T) {
			srv := fixedResponse(http.StatusServiceUnavailable, "down")
			defer srv.Close()

			item := Item{DOI: taxonomyDOIFor(name)}
			_, err := doiSourceFor(t, name, srv).Resolve(context.Background(), item)
			if err == nil {
				t.Fatal("Resolve should fail against a 503 upstream")
			}
			if !errors.Is(err, ErrSourceUnavailable) {
				t.Errorf("error %v is not ErrSourceUnavailable; a 503 upstream must be diagnosable as unavailability", err)
			}
			if !cooldownWorthy(context.Background(), err) {
				t.Errorf("error %v should be cooldown-worthy", err)
			}
		})
	}
}

// TestCleanMissIsNotDiagnosedAsUnavailability verifies a source that answers
// correctly that it does not hold the item reports ErrNotIndexed and is NOT
// cooldown-worthy: a source must never be punished for a normal miss.
func TestCleanMissIsNotDiagnosedAsUnavailability(t *testing.T) {
	misses := map[string]string{
		"unpaywall": `{"is_oa":false}`,
		"europepmc": `{"hitCount":0,"resultList":{"result":[]}}`,
		"biorxiv":   `{"collection":[]}`,
		"dagstuhl":  "<html>not found</html>",
		"zenodo":    "",
		"fatcat":    "",
		"core":      `{"results":[]}`,
		"scihub":    "<html><body>article not found</body></html>",
		"scidb":     "<html><body>nothing here</body></html>",
	}
	for _, name := range doiSourceNames {
		t.Run(name, func(t *testing.T) {
			status := http.StatusOK
			switch name {
			case "fatcat", "dagstuhl":
				status = http.StatusNotFound // their clean miss is a 404 on the lookup
			case "zenodo":
				// The file listing 404s and so does the record page the version hop
				// then asks, which together mean the record does not exist.
				status = http.StatusNotFound
			}
			srv := fixedResponse(status, misses[name])
			defer srv.Close()

			item := Item{DOI: taxonomyDOIFor(name)}
			_, err := doiSourceFor(t, name, srv).Resolve(context.Background(), item)
			if err == nil {
				t.Fatal("Resolve should fail when the source does not hold the item")
			}
			if !errors.Is(err, ErrNotIndexed) {
				t.Errorf("error %v is not ErrNotIndexed; a clean miss must be diagnosable as one", err)
			}
			if errors.Is(err, ErrSourceUnavailable) {
				t.Errorf("error %v is tagged unavailable; a clean miss is a correct answer, not a failure", err)
			}
			if cooldownWorthy(context.Background(), err) {
				t.Errorf("a clean miss (%v) must not put the source in cooldown", err)
			}
		})
	}
}

// TestTaggedErrorKeepsItsMessage verifies the taxonomy tags are invisible in the
// rendered message: the diagnosis rides alongside the source's own wording so
// error text (and everything that reads it) is unchanged.
func TestTaggedErrorKeepsItsMessage(t *testing.T) {
	inner := errors.New(`fatcat: requesting "10.1/x": dial tcp: i/o timeout`)
	tagged := unavailable(inner)
	if tagged.Error() != inner.Error() {
		t.Errorf("tagged message = %q, want the original %q", tagged.Error(), inner.Error())
	}
	if !errors.Is(tagged, inner) {
		t.Error("the tagged error should still unwrap to the source's own error")
	}
	if !errors.Is(tagged, ErrSourceUnavailable) {
		t.Error("the tagged error should carry ErrSourceUnavailable")
	}
}

// TestUnavailableStatusLeavesNonTransientErrorsUntouched verifies the pass-through
// branch returns the SAME error value, not a copy and not a rewrapping.
//
// Every other test observes that branch only through cooldownWorthy reporting
// false, which a wrapped error would also satisfy. The doc comment promises the
// error is "returned untouched", and sources rely on it: a 4xx error is matched by
// message and by sentinel further up (randombook's ErrLayoutChanged, the tools
// layer's catalog wording), so quietly wrapping it here would change text that is
// asserted several layers away.
func TestUnavailableStatusLeavesNonTransientErrorsUntouched(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, 451, 499} {
		in := errors.New("source: some diagnosis")
		got := unavailableStatus(status, in)
		if got != in { //nolint:errorlint // identity is exactly what is being asserted
			t.Errorf("unavailableStatus(%d) returned %v, want the original error value unchanged", status, got)
		}
	}
}

// TestNotIndexedKeepsItsMessage is the counterpart of TestTaggedErrorKeepsItsMessage
// for the other tag. Both go through the same taggedError, but the doc comment makes
// the message-preservation promise for each, and a clean miss's wording is what the
// download tool shows the user when no source holds an item.
func TestNotIndexedKeepsItsMessage(t *testing.T) {
	inner := errors.New(`zenodo: record 123 has no files`)
	tagged := notIndexed(inner)
	if tagged.Error() != inner.Error() {
		t.Errorf("tagged message = %q, want the original %q", tagged.Error(), inner.Error())
	}
	if !errors.Is(tagged, inner) {
		t.Error("the tagged error should still unwrap to the source's own error")
	}
	if !errors.Is(tagged, ErrNotIndexed) {
		t.Error("the tagged error should carry ErrNotIndexed")
	}
	if errors.Is(tagged, ErrSourceUnavailable) {
		t.Error("a clean miss must not also read as the source being unavailable")
	}
}

// TestCooldownWorthy pins the classification that decides whether a failure sets a
// source aside. It is the heart of the feature: an unreachable source must be
// remembered, and a correct "I do not have this" must not be.
func TestCooldownWorthy(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	timeout := &net.DNSError{Err: "i/o timeout", IsTimeout: true}

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{name: "no failure at all", ctx: context.Background(), err: nil},
		{name: "tagged unavailable", ctx: context.Background(), err: unavailable(errors.New("dial tcp")), want: true},
		{name: "tagged not indexed", ctx: context.Background(), err: notIndexed(errors.New("not in index"))},
		{name: "every mirror unreachable", ctx: context.Background(), err: fmt.Errorf("%w: x", ErrAllMirrorsFailed), want: true},
		{name: "every mirror rejected the request", ctx: context.Background(), err: fmt.Errorf("%w: 404", ErrRequestRejected)},
		{name: "resolve budget expired", ctx: context.Background(), err: fmt.Errorf("fatcat: %w", context.DeadlineExceeded), want: true},
		{name: "network timeout", ctx: context.Background(), err: fmt.Errorf("scidb: %w", timeout), want: true},
		{name: "transient status", ctx: context.Background(), err: unavailableStatus(http.StatusTooManyRequests, errors.New("rate limited")), want: true},
		{name: "server error status", ctx: context.Background(), err: unavailableStatus(http.StatusBadGateway, errors.New("bad gateway")), want: true},
		{name: "client error status", ctx: context.Background(), err: unavailableStatus(http.StatusNotFound, errors.New("missing"))},
		// 500 itself and the status immediately below the 5xx range. Without this
		// pair the >= comparison in unavailableStatus can be weakened to > and every
		// other row still passes, because 502 and 503 satisfy both.
		{name: "the first server error status", ctx: context.Background(), err: unavailableStatus(http.StatusInternalServerError, errors.New("boom")), want: true},
		{name: "the status just below the server range", ctx: context.Background(), err: unavailableStatus(499, errors.New("client closed request"))},
		{name: "the status just below the rate limit", ctx: context.Background(), err: unavailableStatus(428, errors.New("precondition required"))},
		{name: "unclassified failure", ctx: context.Background(), err: errors.New("something else")},
		{name: "caller canceled the download", ctx: canceled, err: unavailable(errors.New("dial tcp"))},
		{
			name: "unavailability wrapped by the download chain",
			ctx:  context.Background(),
			err:  fmt.Errorf("%w: %w", errDownloadCouldNotStart, startErr(unavailable(errors.New("dial tcp")))),
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cooldownWorthy(tt.ctx, tt.err); got != tt.want {
				t.Errorf("cooldownWorthy(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
