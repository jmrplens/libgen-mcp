// build.go builds every release target the configuration declares, the way
// GoReleaser would as far as the module set is concerned.

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// binary is one release target's binary, and where it is.
type binary struct {
	build  string
	target target
	path   string
}

// goExecutable is the go command of the toolchain running this one, joined
// out of GOROOT rather than looked up on PATH: a lookup in a directory list
// the environment controls is what Sonar's go:S4036 refuses, and the toolchain
// that runs this command is the one go.mod selected.
func goExecutable() string {
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(runtime.GOROOT(), "bin", name) //nolint:staticcheck // deprecated GOROOT accepted: avoids a PATH lookup (Sonar go:S4036), and this command runs under the toolchain that built it.
}

// buildInto builds every target into a temporary directory and returns the
// binaries with the function that removes them.
func buildInto(ctx context.Context, dir string, builds []build) ([]binary, func(), error) {
	outDir, err := os.MkdirTemp("", toolName+"-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(outDir) }
	built, err := buildAll(ctx, dir, builds, outDir)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return built, cleanup, nil
}

// buildAll builds every target of every build into outDir, from dir.
//
// The environment is the process's own with the entry's env and the target's
// GOOS and GOARCH after it, which is the order GoReleaser applies them in, so a
// value the entry sets wins over one the caller's shell happened to export.
func buildAll(ctx context.Context, dir string, builds []build, outDir string) ([]binary, error) {
	var built []binary
	for _, b := range builds {
		for _, t := range b.targets {
			out := filepath.Join(outDir, b.id+"_"+t.goos+"_"+t.goarch)
			args := append([]string{"build", "-o", out}, b.flags...)
			args = append(args, b.main)
			// #nosec G204 -- the program is the Go toolchain joined out of GOROOT, and the arguments come from this repository's own release configuration
			cmd := exec.CommandContext(ctx, goExecutable(), args...)
			cmd.Dir = dir
			cmd.Env = append(append(os.Environ(), b.env...), "GOOS="+t.goos, "GOARCH="+t.goarch)
			if output, err := cmd.CombinedOutput(); err != nil {
				return nil, fmt.Errorf("building %s for %s: %w\n%s", b.id, t, err, output)
			}
			built = append(built, binary{build: b.id, target: t, path: out})
		}
	}
	return built, nil
}
