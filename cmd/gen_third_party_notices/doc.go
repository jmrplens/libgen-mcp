// Command gen_third_party_notices writes THIRD_PARTY_NOTICES: the license,
// notice and patent texts of every module built into the release binaries,
// read from the binaries' own build information and from the module cache.
//
// # Why it exists
//
// libgen-mcp is MIT and its LICENSE travels with every package. The binaries
// also carry the Go standard library and the modules the server links, under
// BSD-3-Clause, Apache-2.0 (pdfcpu among them), MIT and MPL-2.0, whose terms
// ask for their license and notice texts to accompany a binary
// redistribution, and until this command no channel shipped them: the SPDX
// SBOMs name each license and carry none of the texts. This file is those
// texts, generated at release time from what the binaries actually link rather
// than from go.mod, which also lists modules only tests, tools or other
// platforms use.
//
// # What it reads
//
// Each argument is a binary or a glob of them. The build information each
// binary records, the same list `go version -m` prints, names the modules
// linked into it and the GOOS and GOARCH it was built for. The modules of
// every binary are merged, and a module one target links and another does not
// says so. For each module the texts are read from its directory in the module
// cache, which GoReleaser's `go mod download` hook fills in the release job and
// the Dockerfile's builder fills in the image build: every regular file at the
// module's root named LICENSE (in either spelling), COPYING, COPYRIGHT, NOTICE
// or PATENTS, alone or with a prefix or suffix (LICENSE.md, LICENSE-APACHE,
// MIT-LICENSE). The standard library's texts are read from GOROOT the same
// way, after its VERSION is held to the toolchain the binaries record, so the
// text is the one of the Go that built them.
//
// A module can also carry a license of its own below its root: pdfcpu, under
// Apache-2.0, vendors pkcs7 in pkg/pdfcpu/pkcs7 under MIT, and that package is
// linked into the server. Build information records modules, not packages, so
// for each binary the packages it links are listed again with
// `go list -deps` on its main package, under the GOOS, GOARCH, CGO_ENABLED
// and build tags it records, and the license files in the directory of every
// linked package, and of each parent of one, are reproduced too, each named
// with the import path of its directory. Walking the whole module instead
// would also reproduce texts covering code no binary carries (test fuzzers,
// internal tools), so it is not done. The listing must name the modules and
// versions the build information names, which holds the working directory to
// the tree the binaries were built from.
//
// # What it refuses
//
// Generation stops, and with it the release, rather than write a file that is
// short of something: a pattern that matches no file, a binary with no build
// information, binaries built from different main modules, by different
// toolchains, twice for one target, or linking one module at two versions, a
// target list other than the one -targets names, a package listing that fails
// or disagrees with the build information, a module the cache does not hold or
// replaced by a local directory, and a module or a GOROOT that publishes no
// license file at all.
//
// It reproduces texts and classifies none: which SPDX identifier a text is
// lives in the release's SBOMs. GOROOT and the module cache are read from
// `go env` unless -goroot and -modcache name them.
//
// # Where it runs
//
// GoReleaser runs it once all six release binaries are built and before
// checksums.txt is computed and signed, through an `sboms` entry with
// `artifacts: any` (.goreleaser.yml), so THIRD_PARTY_NOTICES is a release
// asset listed in the signed checksums.txt like every binary. The npm, PyPI
// and NuGet packages and the Claude Desktop bundle carry that file. The
// image's builder stage runs this command against the image's own binary,
// which is built separately.
//
// # Exit codes
//
// 0 when the file was written, 1 when it could not be generated, and 2 for
// arguments that do not parse.
package main
