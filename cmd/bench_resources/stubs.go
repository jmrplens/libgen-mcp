// stubs.go stands in for everything outside this process.
//
// A benchmark that reached the real Library Genesis would measure that site's
// weather. Worse, it would measure a different thing on every machine, which is
// the one property a published number must not have. So the catalog is an
// in-process HTTP server on loopback serving a captured page, and the run is
// offline: the only thing varying between two machines is the machine.

package main

import (
	// embed is imported for its side effect: the //go:embed directive below
	// needs it in scope, and nothing here calls it.
	_ "embed"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
)

// catalogPage is a real capture of a Library Genesis results page, copied from
// internal/libgen/testdata so the parse the server does under measurement is
// the parse it does in production.
//
// Its size is the point as much as its shape. An empty table would make every
// search cost nothing to read and would publish a number no deployment will
// ever see; this is eighty kilobytes of the markup the parser actually meets.
//
//go:embed testdata/search_books.html
var catalogPage string

// catalog is the stand-in for a Library Genesis mirror.
type catalog struct {
	url string
	// requests counts what arrived, which is what tells a scenario that served
	// its calls from the server's own cache from one that reached the mirror
	// every time.
	requests atomic.Int64
}

// requestCount reports how many requests reached the catalog.
func (c *catalog) requestCount() int64 { return c.requests.Load() }

// startCatalog serves the captured page to anything asked of it.
//
// Every path answers the same body on purpose: what is being measured is this
// server's cost to fetch, parse and render a result, and a stub that routed
// would be measuring a router nobody deploys.
func startCatalog() (stub *catalog, stop func()) {
	c := &catalog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		c.requests.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(catalogPage))
	}))
	c.url = srv.URL
	return c, srv.Close
}

// collector is the stand-in for an OpenTelemetry collector.
//
// It decodes nothing. What the telemetry scenario measures is what exporting
// costs the server — the batching, the encoding, the sixteen modules' worth of
// machinery — and an endpoint that accepts and forgets is the cheapest possible
// receiver, which is the honest floor for that cost. Whether the payload is a
// valid export is a different question, asked by test/e2e/collector against a
// real collector.
type collector struct {
	url      string
	requests atomic.Int64
}

// requestCount reports how many exports arrived.
func (c *collector) requestCount() int64 { return c.requests.Load() }

// startCollector accepts every OTLP export and answers success.
func startCollector() (stub *collector, stop func()) {
	c := &collector{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	c.url = srv.URL
	return c, srv.Close
}
