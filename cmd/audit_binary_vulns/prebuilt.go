// prebuilt.go finds binaries somebody else built, GoReleaser in the release
// job, and holds them to the targets the configuration declares.

package main

import (
	"debug/buildinfo"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// findPrebuilt returns the binaries pattern matches, each named by the
// platform its own build information records.
//
// The set must be exactly the configuration's targets, each once. A pattern
// that matched one binary fewer would pass a release with that binary
// unchecked, one that matched an extra file (the universal darwin binary, a
// copy staged under another name) would scan something twice or something the
// gate was not written for, and either would make the staleness rule judge a
// declaration against a release other than the one being published.
func findPrebuilt(pattern string, builds []build) ([]binary, error) {
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("-binaries %q: %w", pattern, err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("-binaries %q matches no file", pattern)
	}
	sort.Strings(paths)

	want := map[target]string{}
	for _, b := range builds {
		for _, t := range b.targets {
			want[t] = b.id
		}
	}
	found := make([]binary, 0, len(paths))
	seen := map[target]string{}
	for _, path := range paths {
		t, readErr := recordedTarget(path)
		if readErr != nil {
			return nil, readErr
		}
		id, declared := want[t]
		if !declared {
			return nil, fmt.Errorf("%s was built for %s, which the configuration does not release", path, t)
		}
		if previous, twice := seen[t]; twice {
			return nil, fmt.Errorf("%s and %s were both built for %s; -binaries must match one binary per target", previous, path, t)
		}
		seen[t] = path
		found = append(found, binary{build: id, target: t, path: path})
	}
	var missing []string
	for t := range want {
		if seen[t] == "" {
			missing = append(missing, t.String())
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("-binaries %q has no binary for %s", pattern, strings.Join(missing, ", "))
	}
	return found, nil
}

// recordedTarget reads the platform a Go binary records in its build
// information. A binary recording neither reads as the target "/", which no
// configuration releases, so it is refused by the caller with the rest.
func recordedTarget(path string) (target, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return target{}, fmt.Errorf("reading the build information of %s: %w", path, err)
	}
	var t target
	for _, s := range info.Settings {
		switch s.Key {
		case "GOOS":
			t.goos = s.Value
		case "GOARCH":
			t.goarch = s.Value
		}
	}
	return t, nil
}
