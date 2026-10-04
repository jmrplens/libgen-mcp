// Command audit_binary_vulns holds every binary the release publishes to the
// Go vulnerability database at module grain, which is how every scanner that
// reads a shipped binary or its SBOM judges it.
//
// # Why the source scan is not enough
//
// `make govulncheck` asks whether this module's code calls a vulnerable
// symbol, and that is the question that decides whether a vulnerability is
// real. It is not the question a user's scanner asks. Trivy, Grype,
// osv-scanner, Docker Scout, Dependency-Track and verifymcp.io read the modules
// named in a binary's build information, or in the SBOM generated from it, and
// report every advisory against any version of any of them, called or not.
// `govulncheck -mode binary -scan module` asks the same question the same way.
//
// The two answers disagreed here. The source scan passed, correctly, while
// every binary carried golang.org/x/crypto (for the ocsp package pdfcpu's
// signature code imports), against which GO-2026-5932 is filed for the openpgp
// packages nothing here links, and google.golang.org/grpc v1.84.0, against
// which GO-2026-6443 is filed for a server-side transport the binaries linked
// and never reached. Nothing asked the scanners' question before a release, so
// nothing said so. The first was declared until 2.2.1, which removed pdfcpu
// and with it x/crypto; the second was fixed by taking v1.83.2, and a bump
// back to v1.84.0 now fails here.
//
// # What it scans
//
// By default it builds the binaries itself. The targets are read from
// .goreleaser.yml rather than listed here, so a target added to the release is
// scanned without anybody remembering to add it: every entry of builds, each
// goos crossed with each goarch, built from the entry's main package with its
// env and flags. The operating system decides the module set, and a
// linux-only scan would miss what only the others link: the darwin builds link
// github.com/ebitengine/purego and the windows builds github.com/go-ole/go-ole
// and github.com/yusufpapurcu/wmi, none of which a linux build carries. The
// architecture decides nothing today, and every pair is built anyway, since a
// dependency is free to split on it.
//
// The entry's ldflags are not passed. They carry GoReleaser templates this
// command does not evaluate, and they set strings and strip the symbol table,
// none of which changes which modules a binary links, which is all a
// module-grain scan reads.
//
// Anything else is refused rather than read half right. An entry may carry
// only the keys this command reads or knows to leave the module set alone (id,
// main, env, flags, goos, goarch, binary, ldflags, mod_timestamp and
// overrides), an override only the keys that select its targets and ldflags,
// and a global env is refused, since GoReleaser applies it to every build. So
// is a gomod section that sets anything, since each of its keys changes how
// every build fetches or builds the module, and a before hook other than go mod
// download, which runs ahead of every build and may change the source or
// go.mod. The rest of the configuration (the universal binary, archives,
// signing, the release itself) decides what happens to a binary once it is
// built, and is not read.
//
// With -binaries it scans binaries somebody else built instead, which is how
// the release job holds GoReleaser's own output to the same table. The
// configuration is still read, and the platforms the binaries record in their
// build information must be exactly its targets, each once: a glob that missed
// a binary would otherwise pass a release with that binary unchecked, and the
// staleness rule below would judge a declaration against half the release.
//
// # How a finding is judged
//
// govulncheck runs in this process, through golang.org/x/vuln/scan at the
// version go.mod pins, once per binary, with -format json. Findings are merged
// across the binaries by advisory and module, so one advisory against a module
// every binary links is one finding naming every target.
//
// A finding passes only when the declaration table in declarations.go accepts
// it, keyed by the advisory and the module, with a category and a reason. A
// declaration that matches no finding of the run is itself a failure, since
// every run scans every target the release builds: an entry that outlives what
// it excused is how an allowlist comes to excuse the next advisory against the
// same module unread.
//
// # The VEX document
//
// A not-linked declaration is a claim a scanner can be told: the module is in
// the binary and the advisory's packages are not. .vex/libgen-mcp.openvex.json
// says it to them, as one OpenVEX statement per not-linked declaration
// (not_affected, vulnerable_code_not_present, the reason as the impact
// statement), about every identifier the products go by: the main module's
// purl, which Trivy reports as the root of the binary in an image or on its
// own, the image's pkg:oci purl in each registry, and Docker Scout's
// pkg:docker form. Every one is pinned to a version, since a product with none
// matches every release, ones the gate never saw included, and the OCI purls
// are written only with the index digest. A fix-not-yet-adoptable declaration
// writes nothing, since the code it excuses is in the binary.
//
// The committed document names the version -vex-version gives (the VERSION
// file), is generated from the table and is never edited by hand. -vex-check
// holds it to the table and that version in both directions, and so does a
// test of this package: a statement with no not-linked declaration behind it
// fails, and so does a declaration with no statement, which is what makes
// removing a declaration (an advisory fixed) remove its statement in the same
// change. -vex-write rewrites it, keeping its timestamp and version while the
// statements are unchanged. Neither builds or scans anything.
//
// -vex-out with -vex-release and -vex-index-digest writes the copy a release
// attaches to its image and publishes as an asset, each product pinned to
// that release and its index. It runs the whole audit on -dir first, the
// not-linked check below included, and writes nothing unless it passes: the
// copy is what gets signed.
//
// # Not-linked is checked, not trusted
//
// The scan reads modules, so a not-linked declaration would otherwise be a
// measurement made once by hand. Every run remakes it: the packages the
// advisory names (ecosystem_specific.imports of the record the scan matched)
// must not appear in go list -deps of the main package of any release target,
// listed with that target's GOOS, GOARCH, env and flags. One that does fails
// the run as LINKED. An advisory that names no package covers the whole
// module, which the binaries link, and fails as UNCHECKED.
//
// # Why it is a module of its own
//
// golang.org/x/vuln brings golang.org/x/tools, x/mod and x/telemetry with it,
// and none of them has any business in the server's go.mod: a requirement
// there is one every scanner of the repository reads, one Dependabot proposes
// bumps for in the server's group, and one that would make the claim that this
// module does not depend on x/tools (which cmd/audit_md_escaping's design rests
// on) false. So this directory carries its own go.mod, which `./...` at the
// root does not descend into. The Makefile runs it with `go -C`, lints, vets,
// documents and tests it there, and holds it to its own coverage floor, since
// the root profile cannot carry a package of another module.
//
// # Exit codes
//
// 0 when every finding is declared and every declaration matched; 1 when a
// finding is undeclared, a declaration is stale or malformed, or the VEX
// document disagrees with the table; 2 when the run
// could not be made (a configuration it refuses, a build or a scan that
// failed, arguments that do not parse), because a gate that could not check the
// release must not read as one that passed. The database is vuln.go.dev unless
// -db names another, which is how the tests run offline.
package main
