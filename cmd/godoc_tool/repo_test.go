package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// failingWriter always fails, exercising report write-error paths.
type failingWriter struct{}

// Write reports a synthetic failure for every call.
func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

// setupModule writes a temporary Go module, changes into it, and returns its
// root directory. Files may include subdirectory paths.
func setupModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o750); mkErr != nil {
			t.Fatalf("MkdirAll(%s) error = %v", name, mkErr)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	t.Chdir(dir)
	return dir
}

const documentedModule = "module example.com/tm\n\ngo 1.26\n"

// TestGoExecutable_ReturnsRuntimeGo verifies goExecutable resolves to the Go
// tool inside the active runtime installation.
func TestGoExecutable_ReturnsRuntimeGo(t *testing.T) {
	t.Parallel()

	got := goExecutable()
	want := "go"
	if runtime.GOOS == "windows" {
		want = "go.exe"
	}
	if filepath.Base(got) != want {
		t.Fatalf("goExecutable() = %q, want base %q", got, want)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("goExecutable() = %q is not an existing file: %v", got, err)
	}
}

// TestListPackages_ReturnsModulePackages verifies listPackages reports the
// packages of the enclosing module.
func TestListPackages_ReturnsModulePackages(t *testing.T) {
	setupModule(t, map[string]string{
		"go.mod":   documentedModule,
		"tm.go":    "// Package tm is a fixture.\npackage tm\n\n// Widget is documented.\nfunc Widget() {}\n",
		"sub/s.go": "// Package sub is a fixture.\npackage sub\n",
	})

	packages, err := listPackages()
	if err != nil {
		t.Fatalf("listPackages() error = %v", err)
	}
	if len(packages) < 2 {
		t.Fatalf("listPackages() = %#v, want at least 2 packages", packages)
	}
	found := false
	for _, pkg := range packages {
		if pkg.ImportPath == "example.com/tm" && pkg.Name == "tm" {
			found = true
		}
	}
	if !found {
		t.Fatalf("listPackages() missing root package: %#v", packages)
	}
}

// TestListPackages_OutsideModuleErrors verifies listPackages fails when go list
// cannot resolve a module.
func TestListPackages_OutsideModuleErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := listPackages(); err == nil {
		t.Fatal("listPackages() outside a module expected an error")
	}
}

// TestAuditRepository_IgnoreInternal verifies auditRepository skips internal
// packages when requested.
func TestAuditRepository_IgnoreInternal(t *testing.T) {
	setupModule(t, map[string]string{
		"go.mod":                    documentedModule,
		"tm.go":                     "// Package tm is a fixture.\npackage tm\n",
		"internal/helper/helper.go": "// Package helper is a fixture.\npackage helper\n",
	})

	withInternal, err := auditRepository(options{format: formatMarkdown})
	if err != nil {
		t.Fatalf("auditRepository(include) error = %v", err)
	}
	withoutInternal, err := auditRepository(options{format: formatMarkdown, ignoreInternal: true})
	if err != nil {
		t.Fatalf("auditRepository(ignore) error = %v", err)
	}
	if withoutInternal.Packages >= withInternal.Packages {
		t.Fatalf("ignoreInternal did not skip packages: %d >= %d", withoutInternal.Packages, withInternal.Packages)
	}
	if _, ok := withoutInternal.ByPackage["example.com/tm/internal/helper"]; ok {
		t.Fatalf("ignoreInternal kept an internal package: %#v", withoutInternal.ByPackage)
	}
}

// TestRun_AuditModes verifies run writes reports to stdout and files, honors
// fail-on-findings, and reports scan and write errors.
func TestRun_AuditModes(t *testing.T) {
	t.Run("stdout success", func(t *testing.T) {
		setupModule(t, map[string]string{
			"go.mod": documentedModule,
			"tm.go":  "// Package tm is a fixture.\npackage tm\n\n// Widget is documented.\nfunc Widget() {}\n",
		})
		out, err := runForTest([]string{"--format=markdown"})
		if err != nil {
			t.Fatalf("run() error = %v", err)
		}
		if !strings.Contains(out, "# Godoc Audit Report") {
			t.Fatalf("run() stdout = %q", out)
		}
	})

	t.Run("file output json", func(t *testing.T) {
		dir := setupModule(t, map[string]string{
			"go.mod": documentedModule,
			"tm.go":  "// Package tm is a fixture.\npackage tm\n",
		})
		outPath := filepath.Join(dir, "report.json")
		if err := run([]string{"--format=json", "--output=" + outPath}, &failingWriter{}); err != nil {
			t.Fatalf("run(file output) error = %v", err)
		}
		data, err := os.ReadFile(outPath) //#nosec G304 -- test fixture path from t.TempDir.
		if err != nil {
			t.Fatalf("ReadFile(report) error = %v", err)
		}
		if !strings.Contains(string(data), "\"packages\"") {
			t.Fatalf("run(file output) content = %q", data)
		}
	})

	t.Run("fail on findings", func(t *testing.T) {
		setupModule(t, map[string]string{
			"go.mod": documentedModule,
			"tm.go":  "// Package tm is a fixture.\npackage tm\n\nfunc Undocumented() {}\n",
		})
		if _, err := runForTest([]string{"--fail-on-findings"}); err == nil {
			t.Fatal("run(--fail-on-findings) expected an error")
		}
	})

	t.Run("write error", func(t *testing.T) {
		setupModule(t, map[string]string{
			"go.mod": documentedModule,
			"tm.go":  "// Package tm is a fixture.\npackage tm\n",
		})
		badPath := filepath.Join(t.TempDir(), "missing-dir", "report.md")
		if err := run([]string{"--output=" + badPath}, &failingWriter{}); err == nil {
			t.Fatal("run(bad output path) expected a write error")
		}
	})

	t.Run("stdout write error", func(t *testing.T) {
		setupModule(t, map[string]string{
			"go.mod": documentedModule,
			"tm.go":  "// Package tm is a fixture.\npackage tm\n",
		})
		if err := run([]string{"--format=markdown"}, &failingWriter{}); err == nil {
			t.Fatal("run(failing stdout) expected a write error")
		}
	})

	t.Run("scan error", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if _, err := runForTest([]string{"--format=markdown"}); err == nil {
			t.Fatal("run() outside a module expected an error")
		}
	})
}
