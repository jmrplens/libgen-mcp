package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEntityTagIsDerivedFromTheBytesAndNothingElse is the property the whole
// validator rests on.
//
// Two replicas behind one balancer render the same document and must publish the
// same tag, or a client revalidating against whichever replica the balancer
// picked is answered 200 with a copy it already holds — on a round-robin
// balancer, most of the time. A tag minted per process would do exactly that,
// and the miss would be invisible because the response is still correct.
func TestEntityTagIsDerivedFromTheBytesAndNothingElse(t *testing.T) {
	body := []byte(`{"name":"io.github.jmrplens/libgen-mcp"}`)

	first, second := entityTagFor(body), entityTagFor(body)
	if first != second {
		t.Errorf("two tags for the same bytes differ: %s and %s", first, second)
	}
	// Byte-for-byte equal, not merely equal-looking: this is what a second
	// process computing the tag over its own copy produces.
	if same := entityTagFor([]byte(string(body))); same != first {
		t.Errorf("a separately allocated copy of the same bytes tagged %s, want %s", same, first)
	}
	if changed := entityTagFor([]byte(`{"name":"something-else"}`)); changed == first {
		t.Error("two different documents produced the same tag")
	}
}

// TestEntityTagIsAQuotedStrongValidator pins the wire shape. An unquoted value
// is not an entity tag, and a client that parses the header strictly ignores it.
func TestEntityTagIsAQuotedStrongValidator(t *testing.T) {
	tag := entityTagFor([]byte("anything"))

	if !strings.HasPrefix(tag, `"`) || !strings.HasSuffix(tag, `"`) {
		t.Errorf("tag = %s, want it quoted", tag)
	}
	if strings.HasPrefix(tag, "W/") {
		t.Errorf("tag = %s, want a strong validator: the bytes are exactly what is served", tag)
	}
	// 128 bits of hex plus the two quotes. Long enough to tell apart the handful
	// of documents one deployment serves, short enough not to bloat a header.
	if len(tag) != 34 {
		t.Errorf("tag = %s is %d characters, want 34", tag, len(tag))
	}
}

// TestServeCachedDocumentAnswersAConditionalRequest covers the four ways a
// hand-written comparison gets this wrong, each of which fails silently by
// serving a correct 200 nobody needed.
func TestServeCachedDocumentAnswersAConditionalRequest(t *testing.T) {
	body := []byte(`{"served":true}`)
	tag := entityTagFor(body)

	serve := func(t *testing.T, ifNoneMatch string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/server-card", nil)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		rec := httptest.NewRecorder()
		serveCachedDocument(rec, req, tag, body)
		return rec
	}

	t.Run("no validator sent serves the document", func(t *testing.T) {
		rec := serve(t, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if rec.Body.String() != string(body) {
			t.Errorf("body = %q, want the document", rec.Body.String())
		}
		if rec.Header().Get("ETag") != tag {
			t.Errorf("ETag = %q, want %q", rec.Header().Get("ETag"), tag)
		}
	})

	for _, tc := range []struct {
		name        string
		ifNoneMatch string
	}{
		{name: "the matching tag", ifNoneMatch: tag},
		// If-None-Match is a list, not one value.
		{name: "a list carrying it", ifNoneMatch: `"other", ` + tag},
		// "*" matches any current representation.
		{name: "the wildcard", ifNoneMatch: "*"},
		// A GET uses the WEAK comparison, and a proxy that compresses the
		// response rewrites the tag it forwards as W/"…" — so a strong string
		// equality would stop matching the moment the document crossed a CDN,
		// which is precisely where it was meant to help.
		{name: "the same tag weakened by a proxy", ifNoneMatch: "W/" + tag},
	} {
		t.Run(tc.name+" answers 304", func(t *testing.T) {
			rec := serve(t, tc.ifNoneMatch)
			if rec.Code != http.StatusNotModified {
				t.Errorf("status = %d, want %d for If-None-Match %s", rec.Code, http.StatusNotModified, tc.ifNoneMatch)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("a 304 carried %d bytes of body", rec.Body.Len())
			}
		})
	}

	t.Run("a stale validator serves the document", func(t *testing.T) {
		rec := serve(t, `"0123456789abcdef0123456789abcdef"`)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d: the client holds a different document", rec.Code, http.StatusOK)
		}
	})
}

// TestServeCachedDocumentSendsNoLastModified is the honest answer for a document
// rendered at startup out of configuration.
//
// Sending the process start instant instead would invite a client to compare two
// replicas by it and conclude the document had changed when only the process
// had.
func TestServeCachedDocumentSendsNoLastModified(t *testing.T) {
	body := []byte("{}")
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/server-card", nil)
	rec := httptest.NewRecorder()
	serveCachedDocument(rec, req, entityTagFor(body), body)

	if got := rec.Header().Get("Last-Modified"); got != "" {
		t.Errorf("Last-Modified = %q, want it unsent", got)
	}
	// ServeContent sets this, and a bare Write does not.
	if got := rec.Header().Get("Content-Length"); got == "" {
		t.Error("no Content-Length; ServeContent is what sets it, so this is not going through it")
	}
}

// TestServeCachedDocumentAnswersHead covers the other method a scanner uses to
// ask "has it changed" without downloading it.
func TestServeCachedDocumentAnswersHead(t *testing.T) {
	body := []byte(`{"served":true}`)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodHead, "/server-card", nil)
	rec := httptest.NewRecorder()
	serveCachedDocument(rec, req, entityTagFor(body), body)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Length"); got == "" {
		t.Error("a HEAD answered with no Content-Length, so a client cannot size the document")
	}
}
