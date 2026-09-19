// Command audit_test_names holds test files and test function names to the
// conventions this repository states.
//
// Two modes, and the second is the gate. Without flags it scans Go test files
// and classifies test *function* names by pattern, writing a CSV report with
// the columns file, current_name, pattern and suggested_name; -apply renames
// them in place and -dry-run prints what that would change.
//
// -check-files is the file-name convention instead: a _test.go file may only
// exist under the name of the module it tests. A theme-named file hides its
// tests from a reader looking beside the module, and can hide them from CI
// too — a coverage_boost_test.go would match a .gitignore coverage_* rule and
// sit untracked indefinitely. Four shapes are exempt, each for a reason the
// rule cannot absorb; files.go states them beside the code that decides.
//
// Usage:
//
//	go run ./cmd/audit_test_names/ <dir>...
//	go run ./cmd/audit_test_names/ -apply <dir>...
//	go run ./cmd/audit_test_names/ -apply -dry-run <dir>...
//	go run ./cmd/audit_test_names/ -check-files cmd internal test
package main
