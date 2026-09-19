//go:build httpe2e

package httpe2e

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The card locations are named in routing_test.go, beside the rest of this
// server's routes. Here they are read as what they serve: serverCardLegacyPath
// the enumerating document, serverCardCurrentPath the SEP-2127 discovery card.

// TestServerCard_EachLocationServesItsOwnDocument is the correction, over the
// real binary.
//
// Both paths used to answer the enumerating document and differ only in
// Content-Type, which put the older SEP-1649 shape at the location SEP-2127
// reserves — and a deployment that wanted to be conformant had to shadow the
// route with a static file in its proxy. Each path now answers the document its
// own specification describes.
func TestServerCard_EachLocationServesItsOwnDocument(t *testing.T) {
	s := startServer(t, nil, "--public-url", "https://mcp.example.org/libgen")

	legacy := s.do(t, request{method: http.MethodGet, path: serverCardLegacyPath})
	discovery := s.do(t, request{method: http.MethodGet, path: serverCardCurrentPath})

	for _, tc := range []struct {
		path  string
		reply response
		want  string
	}{
		{path: serverCardLegacyPath, reply: legacy, want: "application/json"},
		{path: serverCardCurrentPath, reply: discovery, want: "application/mcp-server-card+json"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if tc.reply.status != http.StatusOK {
				t.Fatalf("GET %s = %d, want %d", tc.path, tc.reply.status, http.StatusOK)
			}
			if ct := tc.reply.header.Get("Content-Type"); ct != tc.want {
				t.Errorf("GET %s Content-Type = %q, want %q", tc.path, ct, tc.want)
			}
		})
	}

	if legacy.body == discovery.body {
		t.Fatal("both locations served the same bytes; the SEP-2127 path is serving the enumerating document again")
	}

	// The enumerating one lists the surface; the SEP-2127 one deliberately does
	// not, because what a server exposes can vary per session and a card cannot
	// answer that honestly.
	var legacyDoc, discoveryDoc map[string]any
	if err := json.Unmarshal([]byte(legacy.body), &legacyDoc); err != nil {
		t.Fatalf("the legacy card is not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(discovery.body), &discoveryDoc); err != nil {
		t.Fatalf("the discovery card is not JSON: %v", err)
	}
	if _, ok := legacyDoc["tools"]; !ok {
		t.Error("the legacy card lists no tools; that document is the only unauthenticated answer to what this server does")
	}
	if _, ok := legacyDoc["serverInfo"]; !ok {
		t.Errorf("the legacy card carries no serverInfo: %s", truncate(legacy.body))
	}
	if _, ok := discoveryDoc["tools"]; ok {
		t.Error("the SEP-2127 card lists tools, which is the one thing its specification says it must not")
	}
	if discoveryDoc["name"] != "io.github.jmrplens/libgen-mcp" {
		t.Errorf("name = %v, want the registry identity", discoveryDoc["name"])
	}
}

// TestServerCard_BothLocationsAnswerAConditionalRequest is what the validator is
// for: a scanner revalidating after the document's one-hour lifetime pays for a
// 304 rather than for the whole thing again.
func TestServerCard_BothLocationsAnswerAConditionalRequest(t *testing.T) {
	s := startServer(t, nil)

	for _, path := range []string{serverCardLegacyPath, serverCardCurrentPath} {
		t.Run(path, func(t *testing.T) {
			first := s.do(t, request{method: http.MethodGet, path: path})
			if first.status != http.StatusOK {
				t.Fatalf("GET %s = %d, want %d", path, first.status, http.StatusOK)
			}
			etag := first.header.Get("ETag")
			if etag == "" {
				t.Fatalf("GET %s carries no ETag", path)
			}

			again := s.do(t, request{
				method:  http.MethodGet,
				path:    path,
				headers: map[string]string{"If-None-Match": etag},
			})
			if again.status != http.StatusNotModified {
				t.Errorf("GET %s with its own validator = %d, want %d", path, again.status, http.StatusNotModified)
			}
			if again.body != "" {
				t.Errorf("the 304 carried a body of %d bytes", len(again.body))
			}

			// A stale validator gets the document, so the 304 above is the
			// comparison working rather than the route answering 304 to anything.
			stale := s.do(t, request{
				method:  http.MethodGet,
				path:    path,
				headers: map[string]string{"If-None-Match": `"0123456789abcdef0123456789abcdef"`},
			})
			if stale.status != http.StatusOK {
				t.Errorf("GET %s with a stale validator = %d, want %d", path, stale.status, http.StatusOK)
			}
		})
	}
}

// TestServerCard_TwoReplicasPublishTheSameValidator is the property that makes
// the tag worth having on a deployment behind a balancer.
//
// The tag is derived from the document's bytes and from nothing process-local,
// so a client revalidating against whichever replica the balancer picked is
// answered 304. A tag minted per process — a start instant, a random string —
// would miss on every request that changed replica, and the miss would be
// invisible because the response is still correct.
func TestServerCard_TwoReplicasPublishTheSameValidator(t *testing.T) {
	flags := []string{"--public-url", "https://mcp.example.org/libgen"}
	first := startServer(t, nil, flags...)
	second := startServer(t, nil, flags...)

	for _, path := range []string{serverCardLegacyPath, serverCardCurrentPath} {
		t.Run(path, func(t *testing.T) {
			a := first.do(t, request{method: http.MethodGet, path: path})
			b := second.do(t, request{method: http.MethodGet, path: path})

			if a.header.Get("ETag") == "" {
				t.Fatalf("GET %s carries no ETag", path)
			}
			if a.header.Get("ETag") != b.header.Get("ETag") {
				t.Errorf("two identically configured replicas published %q and %q for %s",
					a.header.Get("ETag"), b.header.Get("ETag"), path)
			}
			// And a client holding one replica's copy is answered 304 by the
			// other, which is the thing the equal tags buy.
			cross := second.do(t, request{
				method:  http.MethodGet,
				path:    path,
				headers: map[string]string{"If-None-Match": a.header.Get("ETag")},
			})
			if cross.status != http.StatusNotModified {
				t.Errorf("the second replica answered %d to the first's validator, want %d", cross.status, http.StatusNotModified)
			}
		})
	}
}

// TestServerCard_TheDiscoveryCardOmitsARemoteItCannotName covers the deployment
// that has not said where it is reachable.
//
// A listen address is not an answer: it is frequently loopback or a socket
// behind a proxy, and publishing it would send a client somewhere it cannot go.
// `remotes` is optional, so a card with identity and no connection block is true
// rather than misleading.
func TestServerCard_TheDiscoveryCardOmitsARemoteItCannotName(t *testing.T) {
	s := startServer(t, nil)

	reply := s.do(t, request{method: http.MethodGet, path: serverCardCurrentPath})
	if reply.status != http.StatusOK {
		t.Fatalf("GET %s = %d, want %d", serverCardCurrentPath, reply.status, http.StatusOK)
	}
	var card map[string]any
	if err := json.Unmarshal([]byte(reply.body), &card); err != nil {
		t.Fatalf("the card is not JSON: %v", err)
	}
	if _, ok := card["remotes"]; ok {
		t.Errorf("the card advertises a remote although no --public-url was given: %s", truncate(reply.body))
	}
	if card["name"] == nil {
		t.Error("the card carries no identity either, so it says nothing at all")
	}
}
