// Package testsource answers the three questions every command that reads
// _test.go files in this repository would otherwise answer for itself: whether
// a function name is a Go test entry point, which naming bucket that name falls
// in, and which files a scan of the tree may look at.
//
// It is shared rather than copied because the copies drift, and the drift is
// invisible: two readers that disagree about whether TestMain_Something is a
// test produce two different corpora and both report a clean run. Go's own rule
// (testing.isTest) is what IsTestFunction implements, and it decides for all of
// them — the prefix "Test", the next rune not lower case, and exactly "TestMain"
// excluded as the framework entry point rather than a test.
//
// The walk is here for the same reason. SkipDir is the one answer to "is
// testdata part of the corpus": generated and vendored trees (node_modules,
// dist), a tool's own fixtures (testdata, whose Go files are inputs to a test
// rather than source this repository holds to its conventions) and every
// dot-directory. Stating it once is what lets a gate and a report agree about
// what they looked at.
//
// One predicate deliberately stays where it is. cmd/godoc_tool asks which
// functions need a test-form doc comment, not which functions the testing
// package runs, so it keeps TestMain and the lower-case Test-prefixed helpers
// that IsTestFunction excludes; routing it through here would drop those
// findings from the documentation audit.
package testsource
