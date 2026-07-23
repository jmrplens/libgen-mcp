package discovery

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFirstNonEmpty documents firstNonEmpty's contract: it returns the first
// trimmed non-empty string, treats whitespace-only entries as empty, and yields
// "" for a nil/empty slice or an all-blank one.
func TestFirstNonEmpty(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   string
	}{
		{name: "nil slice", values: nil, want: ""},
		{name: "empty slice", values: []string{}, want: ""},
		{name: "all empty strings", values: []string{"", "", ""}, want: ""},
		{name: "all whitespace", values: []string{" ", "\t", "\n"}, want: ""},
		{name: "first non-empty returned", values: []string{"a", "b"}, want: "a"},
		{name: "skips leading blanks", values: []string{"", "  ", "x"}, want: "x"},
		{name: "trims the returned value", values: []string{"  hit  "}, want: "hit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstNonEmpty(tc.values); got != tc.want {
				t.Errorf("firstNonEmpty(%q) = %q, want %q", tc.values, got, tc.want)
			}
		})
	}
}

// TestNewDiscoveryClient verifies newDiscoveryClient returns a non-nil client
// whose overall timeout is positive, so a stalled connection cannot outlive the
// per-provider budget.
func TestNewDiscoveryClient(t *testing.T) {
	client := newDiscoveryClient()
	if client == nil {
		t.Fatal("newDiscoveryClient() returned nil")
	}
	if client.Timeout <= 0 {
		t.Errorf("client.Timeout = %v, want > 0", client.Timeout)
	}
	if client.Timeout <= discoveryTimeout {
		t.Errorf("client.Timeout = %v, want greater than the per-provider budget %v",
			client.Timeout, discoveryTimeout)
	}
}

// TestBoundedGet_OKReturnsStatusAndBody verifies a 200 response yields status 200
// and the exact body bytes, and that the discovery User-Agent is sent.
func TestBoundedGet_OKReturnsStatusAndBody(t *testing.T) {
	const payload = "hello discovery"
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	status, body, err := boundedGet(context.Background(), newDiscoveryClient(), srv.URL)
	if err != nil {
		t.Fatalf("boundedGet() error = %v, want nil", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want %d", status, http.StatusOK)
	}
	if string(body) != payload {
		t.Errorf("body = %q, want %q", body, payload)
	}
	if gotUA != discoveryUserAgent {
		t.Errorf("User-Agent = %q, want %q", gotUA, discoveryUserAgent)
	}
}

// TestBoundedGet_BodyCappedAtMaxBody verifies a response larger than
// discoveryMaxBody is truncated: the returned body never exceeds the cap.
func TestBoundedGet_BodyCappedAtMaxBody(t *testing.T) {
	oversized := make([]byte, discoveryMaxBody+4096)
	for i := range oversized {
		oversized[i] = 'x'
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(oversized)
	}))
	defer srv.Close()

	status, body, err := boundedGet(context.Background(), newDiscoveryClient(), srv.URL)
	if err != nil {
		t.Fatalf("boundedGet() error = %v, want nil", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want %d", status, http.StatusOK)
	}
	if len(body) > discoveryMaxBody {
		t.Errorf("len(body) = %d, want <= discoveryMaxBody %d", len(body), discoveryMaxBody)
	}
	if len(body) != discoveryMaxBody {
		t.Errorf("len(body) = %d, want exactly discoveryMaxBody %d", len(body), discoveryMaxBody)
	}
}

// TestBoundedGet_NonOKStatusNoError verifies a non-200 status is reported as-is
// with no error: the status itself is not treated as a transport failure.
func TestBoundedGet_NonOKStatusNoError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()

	status, body, err := boundedGet(context.Background(), newDiscoveryClient(), srv.URL)
	if err != nil {
		t.Fatalf("boundedGet() error = %v, want nil for a non-200 status", err)
	}
	if status != http.StatusTeapot {
		t.Errorf("status = %d, want %d", status, http.StatusTeapot)
	}
	if string(body) != "nope" {
		t.Errorf("body = %q, want %q", body, "nope")
	}
}

// TestBoundedGet_UnreachableServerErrors verifies a request to a closed server
// surfaces a transport error with a zero status and nil body.
func TestBoundedGet_UnreachableServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // close immediately so the address refuses connections

	status, body, err := boundedGet(context.Background(), newDiscoveryClient(), url)
	if err == nil {
		t.Fatal("boundedGet() error = nil, want a transport error for a closed server")
	}
	if status != 0 {
		t.Errorf("status = %d, want 0 on transport error", status)
	}
	if body != nil {
		t.Errorf("body = %q, want nil on transport error", body)
	}
}

// TestBoundedGet_CanceledContextErrors verifies an already-canceled context makes
// boundedGet return the context error rather than degrading silently.
func TestBoundedGet_CanceledContextErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("unreached"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the request is issued

	_, _, err := boundedGet(ctx, newDiscoveryClient(), srv.URL)
	if err == nil {
		t.Fatal("boundedGet() error = nil, want the context error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("boundedGet() error = %v, want context.Canceled", err)
	}
}

// TestBoundedGet_BadURLErrors verifies a malformed URL fails at request
// construction, before any network call, with a zero status.
func TestBoundedGet_BadURLErrors(t *testing.T) {
	status, body, err := boundedGet(context.Background(), newDiscoveryClient(), "://not-a-url")
	if err == nil {
		t.Fatal("boundedGet() error = nil, want an error for a malformed URL")
	}
	if status != 0 || body != nil {
		t.Errorf("status,body = %d,%q, want 0,nil for a malformed URL", status, body)
	}
}

// TestBoundedGet_BodyReadErrorSurfaces verifies the read-error branch: when the
// server promises more bytes via Content-Length than it actually sends and then
// closes the connection, io.ReadAll fails with an unexpected EOF, and boundedGet
// surfaces that error along with the response status and a nil body. The handler
// hijacks the connection to write a deliberately truncated response.
func TestBoundedGet_BodyReadErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("ResponseWriter does not support hijacking")
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Errorf("Hijack() error = %v", err)
			return
		}
		// Promise 1024 bytes but send only a handful, then close: the client's
		// io.ReadAll hits an unexpected EOF while the body is still incomplete.
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 1024\r\n\r\n")
		_, _ = buf.WriteString("short")
		_ = buf.Flush()
		_ = conn.Close()
	}))
	defer srv.Close()

	status, body, err := boundedGet(context.Background(), newDiscoveryClient(), srv.URL)
	if err == nil {
		t.Fatal("boundedGet() error = nil, want a body read error")
	}
	// The status came back before the read failed, so it is reported; the body is
	// dropped on a read error.
	if status != http.StatusOK {
		t.Errorf("status = %d, want %d (read failed after headers)", status, http.StatusOK)
	}
	if body != nil {
		t.Errorf("body = %q, want nil on a read error", body)
	}
	// Sanity: the failure is a network read error, not a context error.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Errorf("boundedGet() error = %v, want a read error, not a timeout", err)
	}
}

// TestSetBasesForTest verifies SetBasesForTest overrides every provider base URL
// and that its restore func reinstates the originals exactly.
func TestSetBasesForTest(t *testing.T) {
	origArxiv, origCrossref, origOpenLibrary := arxivBase, crossrefBase, openLibraryBase

	restore := SetBasesForTest("http://a.test", "http://c.test", "http://o.test")
	if arxivBase != "http://a.test" || crossrefBase != "http://c.test" || openLibraryBase != "http://o.test" {
		t.Fatalf("bases not overridden: arxiv=%q crossref=%q openlibrary=%q",
			arxivBase, crossrefBase, openLibraryBase)
	}

	restore()
	if arxivBase != origArxiv || crossrefBase != origCrossref || openLibraryBase != origOpenLibrary {
		t.Errorf("restore did not reinstate originals: arxiv=%q crossref=%q openlibrary=%q",
			arxivBase, crossrefBase, openLibraryBase)
	}
}
