// linked.go holds every not-linked declaration to the build graph: none of the
// packages the advisory names may be among the packages a release target
// links.

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// listDeps returns the import paths of every package one target of one build
// links, as `go list -deps` of the build's main package reports them, in the
// environment the build runs in. It is a variable so a test can stand in for
// the go command.
var listDeps = func(ctx context.Context, dir string, b build, t target) ([]string, error) {
	args := append([]string{"list", "-deps"}, b.flags...)
	args = append(args, b.main)
	// #nosec G204 -- the program is the Go toolchain joined out of GOROOT, and the arguments come from this repository's own release configuration
	cmd := exec.CommandContext(ctx, goExecutable(), args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), b.env...), "GOOS="+t.goos, "GOARCH="+t.goarch)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list -deps %s for %s: %w\n%s", b.main, t, err, stderr.String())
	}
	return strings.Fields(stdout.String()), nil
}

// checkNotLinked holds each finding a not-linked declaration accepts to the
// build graph of every release target, and records the verdict on the report.
//
// A not-linked declaration is a claim about which packages are in the
// binaries, and the scan that matched it reads modules only, so without this
// the claim would be a measurement made once by hand and trusted on every run
// after. Here it is remade on every run: the packages the advisory names (its
// ecosystem_specific.imports, from the record the scan matched) must not be
// among the packages any target links. An advisory that names no package is
// filed against the whole module, which the binaries do link, so it can never
// be not-linked and is reported as unchecked.
func checkNotLinked(ctx context.Context, dir string, builds []build, rep *report) error {
	deps := map[string][]string{}
	for _, f := range rep.findings {
		if f.declaration == nil || f.declaration.category != categoryNotLinked {
			continue
		}
		key := declarationKey(f.osv, f.module)
		if len(f.imports) == 0 {
			rep.unchecked = append(rep.unchecked, key+": the advisory names no packages, so it covers the whole module and cannot be not-linked")
			continue
		}
		hits, targets, err := linkedImports(ctx, dir, builds, f.imports, deps)
		if err != nil {
			return err
		}
		if len(hits) != 0 {
			rep.linked = append(rep.linked, key+": "+strings.Join(hits, ", "))
			continue
		}
		rep.verified = append(rep.verified, fmt.Sprintf("%s: none of the %d packages the advisory names (%s) is among the packages %d targets link",
			key, len(f.imports), strings.Join(f.imports, ", "), targets))
	}
	return nil
}

// linkedImports names each of imports some target links, and counts the
// targets it looked at.
func linkedImports(ctx context.Context, dir string, builds []build, imports []string, cache map[string][]string) (hits []string, targets int, err error) {
	for _, b := range builds {
		for _, t := range b.targets {
			var linked []string
			if linked, err = depsOf(ctx, dir, b, t, cache); err != nil {
				return nil, 0, err
			}
			targets++
			for _, path := range imports {
				if slices.Contains(linked, path) {
					hits = append(hits, fmt.Sprintf("%s is linked on %s", path, t))
				}
			}
		}
	}
	return hits, targets, nil
}

// depsOf is listDeps, remembered per build and target for the run.
func depsOf(ctx context.Context, dir string, b build, t target, cache map[string][]string) ([]string, error) {
	key := b.id + " " + t.String()
	if linked, ok := cache[key]; ok {
		return linked, nil
	}
	linked, err := listDeps(ctx, dir, b, t)
	if err != nil {
		return nil, err
	}
	cache[key] = linked
	return linked, nil
}
