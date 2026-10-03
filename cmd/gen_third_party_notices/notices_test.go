// notices_test.go holds the generator to what it reads and refuses: the build
// information merged across binaries, the module cache spelling, the files
// that count as license texts, and the layout of the notices.

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"testing/fstest"
)

// mainModule is the main module every fixture binary is built from.
const mainModule = "github.com/jmrplens/libgen-mcp/v2"

// fixtureGo is the toolchain the fixture binaries and GOROOT agree on.
const fixtureGo = "go1.27.1"

// writeFiles writes each relative path of files under root with its text.
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// fixtureGOROOT is a GOROOT holding a VERSION, the two texts Go publishes and
// files that are not licenses.
func fixtureGOROOT(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"VERSION":         version + "\ntime 2026-09-01T00:00:00Z\n",
		"LICENSE":         "Copyright 2009 The Go Authors.\n",
		"PATENTS":         "Additional IP Rights Grant (Patents)\n",
		"README.md":       "# The Go Programming Language\n",
		"CONTRIBUTING.md": "Go is an open source project.\n",
	})
	return root
}

// fixtureModcache is a module cache holding the modules the fixture binaries
// link, spelled the way the go command spells them on disk.
func fixtureModcache(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		// Upper-case letters in a path and a version are escaped with '!'.
		"github.com/!burnt!sushi/toml@v1.0.0-!r!c1/COPYING": "The MIT License (MIT)\r\nCopyright Toml\r\n\r\n",
		// Suffixed and prefixed names count; a Go file, a README and a
		// directory named like a license do not.
		"example.com/alpha@v1.2.0/LICENSE.md":         "Apache License\nVersion 2.0   \n\n\n",
		"example.com/alpha@v1.2.0/NOTICE":             "Alpha\nCopyright 2026 Alpha Authors\n",
		"example.com/alpha@v1.2.0/license.go":         "package alpha\n",
		"example.com/alpha@v1.2.0/README.md":          "# alpha\n",
		"example.com/alpha@v1.2.0/LICENSE.d/MIT.txt":  "nested, never read\n",
		"example.com/only-windows@v0.3.0/MIT-LICENSE": "MIT License\nCopyright Windows\n",
		// A license of its own in a linked package's directory, the shape of
		// pdfcpu's vendored pkcs7, counts; one in a directory holding only
		// packages no binary links does not.
		"example.com/alpha@v1.2.0/pkg/vendored/LICENSE":  "Vendored MIT\nCopyright Vendored\n",
		"example.com/alpha@v1.2.0/pkg/vendored/doc.go":   "package vendored\n",
		"example.com/alpha@v1.2.0/internal/fuzz/LICENSE": "never linked, never read\n",
		"example.com/only-windows@v0.3.0/winapi/COPYING": "winapi terms\n",
		"example.com/fork@v1.1.0/sub/sub.go":             "package sub\n",
		// The fork a replace directive points at holds the texts.
		"example.com/fork@v1.1.0/LICENSE": "BSD 3-Clause License\nCopyright Fork\n",
		// A module whose cache directory holds no license at all.
		"example.com/unlicensed@v1.0.0/README.md": "# no license here\n",
	})
	return root
}

// buildInfo is the build information of one fixture binary.
func buildInfo(goos, goarch string, deps ...*debug.Module) *debug.BuildInfo {
	return &debug.BuildInfo{
		GoVersion: fixtureGo,
		Main:      debug.Module{Path: mainModule, Version: "v2.0.1"},
		Deps:      deps,
		Settings: []debug.BuildSetting{
			{Key: "-trimpath", Value: "true"},
			{Key: "GOARCH", Value: goarch},
			{Key: "GOOS", Value: goos},
		},
	}
}

// fixtureDeps are the modules every fixture binary links.
func fixtureDeps() []*debug.Module {
	return []*debug.Module{
		{Path: "example.com/alpha", Version: "v1.2.0"},
		{Path: "github.com/BurntSushi/toml", Version: "v1.0.0-RC1"},
		{Path: "example.com/beta", Version: "v1.0.0", Replace: &debug.Module{Path: "example.com/fork", Version: "v1.1.0"}},
	}
}

// fixtureBinaries writes one empty file per named binary and returns a reader
// that answers each with the build information given for it.
func fixtureBinaries(t *testing.T, infos map[string]*debug.BuildInfo) (dir string, read infoReader) {
	t.Helper()
	dir = t.TempDir()
	byPath := map[string]*debug.BuildInfo{}
	for name, info := range infos {
		path := filepath.Join(dir, name)
		writeFiles(t, dir, map[string]string{name: ""})
		byPath[path] = info
	}
	return dir, func(path string) (*debug.BuildInfo, error) {
		info, ok := byPath[path]
		if !ok {
			return nil, errors.New("not a Go binary")
		}
		return info, nil
	}
}

// fixtureLister answers for the fixture binaries the way `go list -deps`
// would: the main module's own package, packages of alpha at its root and in
// a nested directory, one of the replaced beta, and on windows one of
// only-windows. toml contributes a module and no listed package.
func fixtureLister(info *debug.BuildInfo) ([]listedPackage, error) {
	packages := []listedPackage{
		{importPath: mainModule + "/cmd/server", modulePath: mainModule},
		{importPath: "example.com/alpha", modulePath: "example.com/alpha", moduleVersion: "v1.2.0"},
		{importPath: "example.com/alpha/pkg/vendored", modulePath: "example.com/alpha", moduleVersion: "v1.2.0"},
		{importPath: "example.com/beta/sub", modulePath: "example.com/beta", moduleVersion: "v1.0.0"},
	}
	if target, _ := targetOf(info); target == "windows/amd64" {
		packages = append(packages, listedPackage{importPath: "example.com/only-windows/winapi", modulePath: "example.com/only-windows", moduleVersion: "v0.3.0"})
	}
	return packages, nil
}

// releaseFixture is the usual case: a linux and a windows binary, the windows
// one linking one module more.
func releaseFixture(t *testing.T) (dir string, read infoReader) {
	t.Helper()
	windowsDeps := append(fixtureDeps(), &debug.Module{Path: "example.com/only-windows", Version: "v0.3.0"})
	return fixtureBinaries(t, map[string]*debug.BuildInfo{
		"server-linux-amd64":       buildInfo("linux", "amd64", fixtureDeps()...),
		"server-windows-amd64.exe": buildInfo("windows", "amd64", windowsDeps...),
	})
}

// TestRender_ReproducesEveryTextInLayout pins the whole document for the usual
// case: the header naming the module, toolchain and builds, the contents, the
// standard library first, the modules in path order, every text normalized, a
// replacement and a module linked into one target said so, and a license in a
// linked package's directory reproduced under its import path while one in a
// directory no binary links code from is not.
func TestRender_ReproducesEveryTextInLayout(t *testing.T) {
	t.Parallel()

	dir, read := releaseFixture(t)
	set, err := collect([]string{filepath.Join(dir, "server-linux-amd64"), filepath.Join(dir, "server-windows-amd64.exe")}, read)
	if err != nil {
		t.Fatal(err)
	}
	if err = set.addPackages(fixtureLister); err != nil {
		t.Fatal(err)
	}
	doc, err := render(set, goEnv{goroot: fixtureGOROOT(t, fixtureGo), modcache: fixtureModcache(t)})
	if err != nil {
		t.Fatal(err)
	}
	want := "Third-party notices for libgen-mcp\n\n" +
		"Module:    " + mainModule + " v2.0.1\n" +
		"Toolchain: go1.27.1\n" +
		"Builds:    linux/amd64, windows/amd64\n\n" +
		"libgen-mcp is distributed under the MIT License, in the LICENSE file\n" +
		"that accompanies this one. Its binaries also contain the Go standard\n" +
		"library and the Go modules listed below, each distributed under its own\n" +
		"license. Every license, notice and patent file each of them publishes,\n" +
		"at the module's root and in the directory of each package the binaries\n" +
		"link or of a parent of one, is reproduced below in full, read from the\n" +
		"module at the version the binaries link. cmd/gen_third_party_notices\n" +
		"writes this file from the build information the binaries record and the\n" +
		"packages the toolchain lists for each of them.\n\n" +
		"The source code of each module is available at the version listed, or\n" +
		"at the replacement's where one is named, from the Go module proxy: the\n" +
		"command go mod download -json <module>@<version> fetches it, from\n" +
		"https://proxy.golang.org unless GOPROXY names another. The source code\n" +
		"of the Go standard library is part of the Go release named under\n" +
		"Toolchain, published at https://go.dev/dl/.\n\n" +
		"Contents\n\n" +
		"  Go standard library go1.27.1\n" +
		"  example.com/alpha v1.2.0\n" +
		"  example.com/beta v1.0.0\n" +
		"  example.com/only-windows v0.3.0\n" +
		"  github.com/BurntSushi/toml v1.0.0-RC1\n" +
		"\n" + rule + "\nGo standard library go1.27.1\n" +
		"\n--- LICENSE ---\n\nCopyright 2009 The Go Authors.\n" +
		"\n--- PATENTS ---\n\nAdditional IP Rights Grant (Patents)\n" +
		"\n" + rule + "\nexample.com/alpha v1.2.0\n" +
		"\n--- LICENSE.md ---\n\nApache License\nVersion 2.0\n" +
		"\n--- NOTICE ---\n\nAlpha\nCopyright 2026 Alpha Authors\n" +
		"\n--- LICENSE in example.com/alpha/pkg/vendored ---\n\nVendored MIT\nCopyright Vendored\n" +
		"\n" + rule + "\nexample.com/beta v1.0.0\nReplaced by: example.com/fork v1.1.0\n" +
		"\n--- LICENSE ---\n\nBSD 3-Clause License\nCopyright Fork\n" +
		"\n" + rule + "\nexample.com/only-windows v0.3.0\nLinked into: windows/amd64 only\n" +
		"\n--- MIT-LICENSE ---\n\nMIT License\nCopyright Windows\n" +
		"\n--- COPYING in example.com/only-windows/winapi ---\n\nwinapi terms\n" +
		"\n" + rule + "\ngithub.com/BurntSushi/toml v1.0.0-RC1\n" +
		"\n--- COPYING ---\n\nThe MIT License (MIT)\nCopyright Toml\n"
	if got := string(doc); got != want {
		t.Errorf("notices differ\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestCollect_RefusesBinariesFromDifferentBuilds holds each refusal that keeps
// one release's notices from mixing in another build.
func TestCollect_RefusesBinariesFromDifferentBuilds(t *testing.T) {
	t.Parallel()

	otherMain := buildInfo("darwin", "arm64", fixtureDeps()...)
	otherMain.Main.Path = "example.com/other"
	otherVersion := buildInfo("darwin", "arm64", fixtureDeps()...)
	otherVersion.Main.Version = "v2.0.0"
	otherGo := buildInfo("darwin", "arm64", fixtureDeps()...)
	otherGo.GoVersion = "go1.26.4"
	noGOOS := buildInfo("darwin", "arm64")
	noGOOS.Settings = noGOOS.Settings[:2]
	noGOARCH := buildInfo("darwin", "arm64")
	noGOARCH.Settings = []debug.BuildSetting{{Key: "GOOS", Value: "darwin"}}
	alphaV2 := fixtureDeps()
	alphaV2[0] = &debug.Module{Path: "example.com/alpha", Version: "v2.0.0"}
	betaUnreplaced := fixtureDeps()
	betaUnreplaced[2] = &debug.Module{Path: "example.com/beta", Version: "v1.0.0"}

	for _, tc := range []struct {
		name   string
		second *debug.BuildInfo
		want   string
	}{
		{"another main module", otherMain, "is built from example.com/other v2.0.1, the others from " + mainModule + " v2.0.1"},
		{"another main version", otherVersion, "is built from " + mainModule + " v2.0.0"},
		{"another toolchain", otherGo, "is built with go1.26.4, the others with go1.27.1"},
		{"no GOOS", noGOOS, "records no GOOS or no GOARCH"},
		{"no GOARCH", noGOARCH, "records no GOOS or no GOARCH"},
		{"the same target twice", buildInfo("linux", "amd64", fixtureDeps()...), "are both built for linux/amd64"},
		{"a module at two versions", buildInfo("darwin", "arm64", alphaV2...), "links example.com/alpha at v2.0.0, another binary at v1.2.0"},
		{"a module replaced in one only", buildInfo("darwin", "arm64", betaUnreplaced...), "links example.com/beta at v1.0.0, another binary at v1.0.0 => example.com/fork v1.1.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, read := fixtureBinaries(t, map[string]*debug.BuildInfo{
				"a": buildInfo("linux", "amd64", fixtureDeps()...),
				"b": tc.second,
			})
			_, err := collect([]string{filepath.Join(dir, "a"), filepath.Join(dir, "b")}, read)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("collect = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// TestCollect_ReportsAnUnreadableBinary names the file whose build information
// could not be read.
func TestCollect_ReportsAnUnreadableBinary(t *testing.T) {
	t.Parallel()

	dir, read := fixtureBinaries(t, nil)
	path := filepath.Join(dir, "not-go")
	_, err := collect([]string{path}, read)
	if err == nil || err.Error() != path+": not a Go binary" {
		t.Fatalf("collect = %v, want the path and the reader's error", err)
	}
}

// TestRequireTargets_HoldsTheBinariesToTheNamedSet covers the release's guard
// against a glob that matched one binary short.
func TestRequireTargets_HoldsTheBinariesToTheNamedSet(t *testing.T) {
	t.Parallel()

	set := &linkSet{targets: map[string]string{"linux/amd64": "a", "windows/amd64": "b"}}
	for _, tc := range []struct {
		name string
		want []string
		err  string
	}{
		{"none named", nil, ""},
		{"the same set in another order", []string{"windows/amd64", "linux/amd64"}, ""},
		{"named twice", []string{"windows/amd64", "linux/amd64", "linux/amd64"}, ""},
		{"one short", []string{"linux/amd64"}, "the binaries are built for linux/amd64, windows/amd64, but -targets names linux/amd64"},
		{"one more", []string{"linux/amd64", "linux/arm64", "windows/amd64"}, "but -targets names linux/amd64, linux/arm64, windows/amd64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := set.requireTargets(tc.want)
			if tc.err == "" {
				if err != nil {
					t.Fatalf("requireTargets = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("requireTargets = %v, want %q", err, tc.err)
			}
		})
	}
}

// TestExpand_ReadsGlobsAndRefusesOneMatchingNothing covers the arguments: a
// glob, a path named twice, and a pattern that names nothing.
func TestExpand_ReadsGlobsAndRefusesOneMatchingNothing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"b-linux": "", "a-darwin": "", "other": ""})
	got, err := expand([]string{filepath.Join(dir, "*-*"), filepath.Join(dir, "a-darwin")})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "a-darwin"), filepath.Join(dir, "b-linux")}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("expand = %v, want %v", got, want)
	}

	if _, err = expand([]string{filepath.Join(dir, "missing-*")}); err == nil || !strings.HasSuffix(err.Error(), "missing-* matches no file") {
		t.Errorf("expand of a pattern matching nothing = %v", err)
	}
	if _, err = expand([]string{"["}); err == nil || !strings.HasPrefix(err.Error(), "[: ") {
		t.Errorf("expand of a malformed pattern = %v", err)
	}
}

// TestResolveGoEnv_AsksOnlyForWhatIsNotNamed covers each way GOROOT and the
// module cache are found, and each way `go env` can fail to say.
func TestResolveGoEnv_AsksOnlyForWhatIsNotNamed(t *testing.T) {
	t.Parallel()

	answer := func(out string, err error) func() ([]byte, error) {
		return func() ([]byte, error) { return []byte(out), err }
	}
	never := func() ([]byte, error) {
		t.Error("go env was asked although both were named")
		return nil, nil
	}
	reported := answer(`{"GOROOT": "/go/root", "GOMODCACHE": "/go/mod"}`, nil)
	for _, tc := range []struct {
		name             string
		goroot, modcache string
		ask              func() ([]byte, error)
		want             goEnv
		err              string
	}{
		{"both named", "/r", "/m", never, goEnv{goroot: "/r", modcache: "/m"}, ""},
		{"only GOROOT named", "/r", "", reported, goEnv{goroot: "/r", modcache: "/go/mod"}, ""},
		{"only the cache named", "", "/m", reported, goEnv{goroot: "/go/root", modcache: "/m"}, ""},
		{"neither named", "", "", reported, goEnv{goroot: "/go/root", modcache: "/go/mod"}, ""},
		{"go env fails", "", "", answer("", errors.New("exit status 1")), goEnv{}, "go env: exit status 1"},
		{"go env says nonsense", "", "", answer("not json", nil), goEnv{}, "go env: invalid character"},
		{"go env names no GOROOT", "", "/m", answer(`{"GOMODCACHE": "/go/mod"}`, nil), goEnv{}, "go env reports no GOROOT or no GOMODCACHE"},
		{"go env names no cache", "/r", "", answer(`{"GOROOT": "/go/root"}`, nil), goEnv{}, "go env reports no GOROOT or no GOMODCACHE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveGoEnv(tc.goroot, tc.modcache, tc.ask)
			if tc.err != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tc.err) {
					t.Fatalf("resolveGoEnv = %v, want an error starting %q", err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("resolveGoEnv = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

// TestRunGoEnv_AsksTheRunningToolchain runs the real `go env`, which is what
// production does, and holds it to naming a GOROOT holding a toolchain.
func TestRunGoEnv_AsksTheRunningToolchain(t *testing.T) {
	t.Parallel()

	env, err := resolveGoEnv("", "", runGoEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(env.goroot, "VERSION")); statErr != nil {
		t.Errorf("go env names GOROOT %s, which holds no VERSION: %v", env.goroot, statErr)
	}
}

// TestGoExecutable_NamesTheToolchainsGoCommand pins where the go command is
// looked for: inside the GOROOT given, with the extension Windows needs, and
// on PATH when there is no GOROOT to join.
func TestGoExecutable_NamesTheToolchainsGoCommand(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, goroot, goos, want string
	}{
		{"linux", "/usr/local/go", "linux", filepath.Join("/usr/local/go", "bin", "go")},
		{"windows", "/usr/local/go", "windows", filepath.Join("/usr/local/go", "bin", "go.exe")},
		{"no GOROOT", "", "linux", "go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := goExecutable(tc.goroot, tc.goos); got != tc.want {
				t.Errorf("goExecutable(%q, %q) = %q, want %q", tc.goroot, tc.goos, got, tc.want)
			}
		})
	}
}

// TestRender_RefusesWhatItCannotShip covers every way rendering stops: a
// GOROOT of another toolchain or without its VERSION or license, a module the
// cache does not hold or that publishes no license, and one replaced by a
// local directory.
func TestRender_RefusesWhatItCannotShip(t *testing.T) {
	t.Parallel()

	modcache := fixtureModcache(t)
	noLicenseRoot := t.TempDir()
	writeFiles(t, noLicenseRoot, map[string]string{"VERSION": fixtureGo + "\n"})
	noVersionRoot := t.TempDir()
	writeFiles(t, noVersionRoot, map[string]string{"LICENSE": "Go\n"})
	module := func(path, version string, replace *debug.Module) map[string]*linkedModule {
		return map[string]*linkedModule{path: {path: path, version: version, replace: replace, targets: map[string]bool{"linux/amd64": true}}}
	}

	for _, tc := range []struct {
		name    string
		goroot  string
		modules map[string]*linkedModule
		want    string
	}{
		{"GOROOT of another toolchain", fixtureGOROOT(t, "go1.26.4"), nil, "is go1.26.4, but the binaries were built with go1.27.1"},
		{"GOROOT without VERSION", noVersionRoot, nil, "GOROOT " + noVersionRoot + ": "},
		{"GOROOT without a license", noLicenseRoot, nil, "the Go standard library publishes no license file in " + noLicenseRoot},
		{"a module the cache lacks", fixtureGOROOT(t, fixtureGo), module("example.com/missing", "v1.0.0", nil), "example.com/missing v1.0.0: "},
		{"a module with no license", fixtureGOROOT(t, fixtureGo), module("example.com/unlicensed", "v1.0.0", nil), "example.com/unlicensed v1.0.0 publishes no license file in "},
		{"a local replacement", fixtureGOROOT(t, fixtureGo), module("example.com/beta", "v1.0.0", &debug.Module{Path: "../beta"}), "example.com/beta is replaced by the local directory ../beta, which no module cache holds"},
		{
			"a linked package directory the cache lacks", fixtureGOROOT(t, fixtureGo),
			map[string]*linkedModule{"example.com/alpha": {path: "example.com/alpha", version: "v1.2.0", targets: map[string]bool{"linux/amd64": true}, dirs: map[string]bool{"gone": true}}},
			filepath.Join("example.com", "alpha@v1.2.0", "gone"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			set := &linkSet{goVersion: fixtureGo + " X:none", targets: map[string]string{"linux/amd64": "a"}, modules: tc.modules}
			_, err := render(set, goEnv{goroot: tc.goroot, modcache: modcache})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("render = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// refusingFS lists its files like any other and refuses to read them, which is
// the one failure a directory listing cannot show and a file system that
// refuses root nothing cannot produce.
type refusingFS struct{ fstest.MapFS }

// ReadFile refuses every read, whatever the listing showed.
func (refusingFS) ReadFile(string) ([]byte, error) { return nil, errors.New("read refused") }

// TestReadLicenses_ReportsAFileItCannotRead covers a license the directory
// lists and the read refuses, and a symbolic link named like a license, which
// is never read as one.
func TestReadLicenses_ReportsAFileItCannotRead(t *testing.T) {
	t.Parallel()

	fsys := refusingFS{fstest.MapFS{"LICENSE": {Data: []byte("text\n")}}}
	if _, err := readLicenses(fsys, "/cache/m@v1", "m v1"); err == nil || err.Error() != "m v1: /cache/m@v1: read refused" {
		t.Fatalf("readLicenses = %v, want the module, the directory and the refusal", err)
	}

	linked := fstest.MapFS{
		"COPYING": {Data: []byte("text\n"), Mode: fs.ModeSymlink},
		"NOTICE":  {Data: []byte("notice\n")},
	}
	texts, err := readLicenses(linked, "/cache/m@v1", "m v1")
	if err != nil || len(texts) != 1 || texts[0].name != "NOTICE" {
		t.Fatalf("readLicenses = %+v, %v; want only the regular NOTICE", texts, err)
	}
}

// TestLicenseName_MatchesTheFilesModulesPublish pins which names count.
func TestLicenseName_MatchesTheFilesModulesPublish(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]bool{
		// The British spelling, a file name modules do publish, is written in
		// two halves because the linter's US dictionary flags it whole.
		"LICENSE": true, "LICEN" + "CE": true, "license": true, "License.md": true, "LICENSE.txt": true,
		"LICENSE-APACHE": true, "LICENSE_MIT": true, "MIT-LICENSE": true, "UNLICENSE": true,
		"COPYING": true, "COPYING.LESSER": true, "COPYRIGHT": true, "NOTICE": true, "NOTICE.txt": true,
		"NOTICES": true, "THIRD-PARTY-NOTICES": true, "PATENTS": true,
		"LICENSES": false, "README.md": false, "licensed.go": false, "LICENSEE": false, "XNOTICE": false,
		"CONTRIBUTING.md": false, ".LICENSE": false,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := licenseName.MatchString(name); got != want {
				t.Errorf("licenseName.MatchString(%q) = %v, want %v", name, got, want)
			}
		})
	}
}

// TestEscapeModule_SpellsTheCacheDirectory pins the module cache's case
// escaping, which is what makes a module with an upper-case path findable.
func TestEscapeModule_SpellsTheCacheDirectory(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"github.com/BurntSushi/toml": "github.com/!burnt!sushi/toml",
		"v1.0.0-RC1":                 "v1.0.0-!r!c1",
		"golang.org/x/net":           "golang.org/x/net",
		"AZ":                         "!a!z",
	} {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			if got := escapeModule(in); got != want {
				t.Errorf("escapeModule(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

// TestNormalize_GivesUnixLinesAndOneNewline pins the text clean-up.
func TestNormalize_GivesUnixLinesAndOneNewline(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"a\r\nb\r\n\r\n": "a\nb\n",
		"a\n\n\n":        "a\n",
		"a  \t":          "a\n",
		"a\rb":           "a\rb\n",
	} {
		t.Run(fmt.Sprintf("%q", in), func(t *testing.T) {
			t.Parallel()
			if got := normalize(in); got != want {
				t.Errorf("normalize(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

// TestAddPackages_RefusesAListingOfAnotherTree covers each way the listed
// packages can disagree with the binaries: a listing that fails, a module the
// build information does not name, and a module at another version.
func TestAddPackages_RefusesAListingOfAnotherTree(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		packages []listedPackage
		err      error
		want     string
	}{
		{"a listing that fails", nil, errors.New("exit status 1"), "a: exit status 1"},
		{
			"a module no binary names",
			[]listedPackage{{importPath: "example.com/stray/x", modulePath: "example.com/stray", moduleVersion: "v1.0.0"}},
			nil,
			"go list reports example.com/stray/x from example.com/stray, which the build information of ",
		},
		{
			"a module at another version",
			[]listedPackage{{importPath: "example.com/alpha", modulePath: "example.com/alpha", moduleVersion: "v1.3.0"}},
			nil,
			"go list reports example.com/alpha at v1.3.0, but the binaries link v1.2.0",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, read := fixtureBinaries(t, map[string]*debug.BuildInfo{"a": buildInfo("linux", "amd64", fixtureDeps()...)})
			set, err := collect([]string{filepath.Join(dir, "a")}, read)
			if err != nil {
				t.Fatal(err)
			}
			err = set.addPackages(func(*debug.BuildInfo) ([]listedPackage, error) { return tc.packages, tc.err })
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("addPackages = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// TestWithParents_ClimbsToTheModuleRoot pins which directories a linked
// package makes count: its own and each parent short of the module root.
func TestWithParents_ClimbsToTheModuleRoot(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"":                 "[]",
		"pkg":              "[pkg]",
		"pkg/pdfcpu/pkcs7": "[pkg/pdfcpu/pkcs7 pkg/pdfcpu pkg]",
	} {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			if got := fmt.Sprint(withParents(in)); got != want {
				t.Errorf("withParents(%q) = %s, want %s", in, got, want)
			}
		})
	}
}

// TestParseListed_ReadsTheListFormat covers the lines go list prints: one
// per package, blank ones skipped, and a line that is not three fields
// refused.
func TestParseListed_ReadsTheListFormat(t *testing.T) {
	t.Parallel()

	got, err := parseListed([]byte("example.com/a/x\texample.com/a\tv1.0.0\n\nexample.com/m\texample.com/m\t\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []listedPackage{
		{importPath: "example.com/a/x", modulePath: "example.com/a", moduleVersion: "v1.0.0"},
		{importPath: "example.com/m", modulePath: "example.com/m"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("parseListed = %v, want %v", got, want)
	}
	if _, err = parseListed([]byte("example.com/a/x\texample.com/a\n")); err == nil || !strings.Contains(err.Error(), "not a package, a module and a version") {
		t.Errorf("parseListed of a short line = %v", err)
	}
}

// TestGoListPackages_ReportsWhatTheToolchainRefused runs the real go list for
// a main package that does not exist, under the settings a binary records,
// and holds the error to naming the package and what go list said.
func TestGoListPackages_ReportsWhatTheToolchainRefused(t *testing.T) {
	t.Parallel()

	info := buildInfo("linux", "amd64")
	info.Path = "example.com/does/not/exist"
	info.Settings = append(info.Settings, debug.BuildSetting{Key: "-tags", Value: "netgo"}, debug.BuildSetting{Key: "CGO_ENABLED", Value: "0"})
	_, err := goListPackages(info)
	if err == nil || !strings.HasPrefix(err.Error(), "go list -deps example.com/does/not/exist: ") {
		t.Fatalf("goListPackages = %v, want an error naming the package", err)
	}
}

// TestNestedLicenses_ReportsADirectoryTheCacheLacks covers a linked package
// whose directory the module cache does not hold.
func TestNestedLicenses_ReportsADirectoryTheCacheLacks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	module := &linkedModule{path: "example.com/m", version: "v1.0.0", dirs: map[string]bool{"gone": true}}
	if _, err := nestedLicenses(root, module); err == nil || !strings.HasPrefix(err.Error(), "example.com/m v1.0.0: "+filepath.Join(root, "gone")) {
		t.Fatalf("nestedLicenses = %v, want the module and the missing directory", err)
	}
}

// TestWrite_LeavesOutADevelVersion covers the image's build, from a tree with
// no VCS metadata, whose main module records (devel) rather than a version.
func TestWrite_LeavesOutADevelVersion(t *testing.T) {
	t.Parallel()

	set := &linkSet{mainPath: mainModule, mainVersion: "(devel)", goVersion: fixtureGo, targets: map[string]string{"linux/amd64": "a"}}
	if doc := string(write(set, nil)); !strings.Contains(doc, "\nModule:    "+mainModule+"\nToolchain:") {
		t.Errorf("the header names a version it does not have:\n%s", doc)
	}
}
