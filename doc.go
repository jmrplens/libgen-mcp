// Package libgenmcp exists for one reason: it is the only package that can read
// the repository's VERSION file at compile time.
//
// A go:embed directive cannot reach outside its own package directory, and VERSION
// lives at the module root because the release process, the five JSON manifests
// and `make check-manifests` all treat it as the single source of truth. A package
// at the root can embed it; nothing under cmd/ or internal/ can.
//
// Before this, every binary carried a hardcoded default — the server reported
// 1.0.0 through three releases, and so did the User-Agent on every outbound
// request, because the real number only arrived through release ldflags and
// nobody notices a plausible wrong version. Now the number is compiled in from the
// same file the manifests are checked against, so an unstamped `go run`, a test
// and a released binary all report the same thing.
package libgenmcp
