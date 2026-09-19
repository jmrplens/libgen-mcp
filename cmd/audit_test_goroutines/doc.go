// Command audit_test_goroutines detects testing.T abort calls made from
// goroutines other than the one running the test.
//
// The testing package documents that FailNow — and therefore t.Fatal and
// t.Fatalf — must be called from the test goroutine. Inside an HTTP mock
// handler, a go statement, an errgroup task or an MCP tool handler, the call
// instead terminates only that goroutine: the response is truncated or never
// written, the client observes a transport error, and the test carries on
// against the wreckage, reporting a failure that has nothing to do with the
// assertion that actually failed.
//
// This matters here more than the shape suggests. Almost every test in this
// repository stands up an httptest server for a mirror or a provider, so the
// handler literal is the most common place an assertion is written, and it is
// exactly the place the abort does not work. go vet's testinggoroutine
// analyzer flags a bare `go func() { t.Fatal() }()` but cannot see that a
// literal passed to http.HandlerFunc crosses a goroutine boundary, which is
// how such sites accumulate unnoticed.
//
// The tool reports every t.Fatal/t.Fatalf/t.FailNow call inside such a
// literal, classifies each as category A (the abort is in tail position:
// nothing else would have run) or category B (the handler still had work to
// do, so the response is observably truncated), and separately reports
// t.Error/t.Errorf calls whose enclosing block does not return afterwards. The
// conversion from Fatal to Error needs that explicit return, or the code below
// it runs on state the assertion just declared broken.
//
// Usage:
//
//	go run ./cmd/audit_test_goroutines [-json out.json] [-check] [dirs...]
//
// With no directories the module's test files under ./cmd, ./internal and
// ./test are scanned. -check gates the abort sites and leaves the
// errorf-without-return findings advisory: the first class is a test that
// reports the wrong failure, the second is a style the conversion should
// follow, and a gate that failed on both would have to be turned off.
package main
