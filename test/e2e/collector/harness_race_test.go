//go:build collectore2e && race

// harness_race_test.go is the race-detector half of the harness build seam. The
// go tool sets the `race` build tag when -race is used, so this file is what is
// compiled by `go test -race -tags collectore2e ./test/e2e/collector/` and
// harness_norace_test.go is what is compiled otherwise.
package collectore2e

// raceEnabled tells the harness to pass the detector on to the server build.
//
// The seam exists because `go test -race` instruments the test binary and
// nothing else. The server is a separate process built by this harness, so
// without passing the flag on, a race run would watch the harness's own
// goroutines and say nothing about the server's — which are the ones this
// module reaches: the telemetry pipeline is assembled in package main and
// exports from goroutines of its own, on a batch schedule nothing here drives.
const raceEnabled = true

// raceEnviron is the extra environment an instrumented server is started with.
//
// Without halt_on_error the race runtime prints its report to stderr and lets
// the process continue, setting the exit status only at a clean exit. A server
// that is canceled at the end of a test — which is every one of them here —
// would then take the report to the log with it and the run would stay green.
// Halting fails whichever test was talking to it, with the report in the
// captured output.
func raceEnviron() []string { return []string{"GORACE=halt_on_error=1"} }
