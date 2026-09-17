// etag.go gives the documents this server renders once and then serves
// unchanged a validator, so a client that already holds a copy is answered 304
// rather than handed a second one.
//
// Two routes qualify: the SEP-2127 card and the enumerating card at the
// .well-known path. Both are public, both answer the same bytes until the
// process restarts with different flags, and both carry a `Cache-Control` of an
// hour — so a scanner revalidating after that hour downloads the whole thing
// again for a document that has not moved since startup.
//
// /health deliberately does not qualify. Its body carries uptime_seconds and so
// differs on every probe: that is a document with no validator, not one whose
// validator is merely expensive to compute.

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"
)

// entityTagFor derives a strong entity tag from a document's own bytes.
//
// Deriving it from the bytes and from nothing else is the property that matters,
// and it is what makes the tag worth having on a deployment running several
// replicas behind one balancer: two processes that rendered the same document
// publish the same tag, so a client revalidating against whichever replica the
// balancer happened to pick is answered 304. A tag minted per process — a start
// instant, a random string, even the build digest — would miss on every request
// that changed replica, which on a round-robin balancer is most of them, and the
// miss would be invisible because the response is still correct.
//
// SHA-256 truncated to 128 bits keeps the header short. The tag has to tell
// apart the handful of documents one deployment serves over its lifetime, not
// resist a collision search: anyone able to choose the body here could choose
// the response, so there is nothing further to protect.
func entityTagFor(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// serveCachedDocument writes body under the validator etag, answering a
// conditional request the way RFC 9110 § 13 says to.
//
// [http.ServeContent] does the conditional handling rather than a comparison
// written here, because that comparison has four ways to be wrong and every one
// of them fails silently, by serving a correct 200 nobody needed: If-None-Match
// is a list; `*` is a value of its own matching any current representation; the
// comparison a GET uses is the **weak** one; and a proxy that compresses this
// response rewrites the tag it forwards as `W/"…"`, so a strong string equality
// would stop matching the moment the document crossed a CDN — precisely where it
// was supposed to help. ServeContent also answers HEAD and a Range request on the
// same terms and sets Content-Length, none of which a bare Write does.
//
// The caller sets Content-Type and Cache-Control before calling: ServeContent
// sniffs a type only when none is set, and this package chooses both per route.
func serveCachedDocument(w http.ResponseWriter, r *http.Request, etag string, body []byte) {
	w.Header().Set("ETag", etag)
	// The zero instant leaves Last-Modified unsent, which is the honest answer:
	// these documents are rendered at startup out of configuration and have no
	// modification time a client could reason about. Sending the process start
	// instant instead would invite a client to compare two replicas by it and
	// conclude the document had changed when only the process had.
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
}
