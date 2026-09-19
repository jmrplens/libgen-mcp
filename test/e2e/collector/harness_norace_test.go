//go:build collectore2e && !race

// harness_norace_test.go is the ordinary half of the harness build seam, used by
// every run that is not `go test -race`. See harness_race_test.go for the other
// half and for why the seam exists.
package collectore2e

// raceEnabled is false, so the server is built without the detector.
const raceEnabled = false

// raceEnviron adds nothing when the detector is not in play.
func raceEnviron() []string { return nil }
