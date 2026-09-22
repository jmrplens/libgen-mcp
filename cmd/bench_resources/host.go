// host.go records the machine a measurement came from.
//
// Everything here is best-effort by design: a missing CPU model makes the
// published record vaguer, and refusing to benchmark over it would help nobody.
// What must never happen is a number recorded with no machine attached, which
// is why the fields are gathered here rather than typed in by hand.

package main

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// runtimeGOOS is the operating system the platform branches below key on. A
// variable so a test on one platform can walk the branches of another: the
// sysctl and ps fallbacks are only ever taken on macOS, and a test that could
// not reach them would leave them as the one part of this command nobody had
// run.
var runtimeGOOS = runtime.GOOS

// hostInfo collects what the running machine will admit to.
func hostInfo(ctx context.Context) HostInfo {
	return HostInfo{
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		CPUs:        runtime.NumCPU(),
		GoVersion:   runtime.Version(),
		CPUModel:    cpuModel(ctx),
		MemTotalGiB: totalMemoryGiB(ctx),
		Kernel:      kernelRelease(ctx),
	}
}

// cpuModel reads the processor name, from /proc on Linux and from sysctl on
// macOS.
func cpuModel(ctx context.Context) string {
	if runtimeGOOS == "linux" {
		if model := parseCPUModel(readFileString("/proc/cpuinfo")); model != "" {
			return model
		}
	}
	if runtimeGOOS == "darwin" {
		if out, err := probe(ctx, "sysctl", "-n", "machdep.cpu.brand_string"); err == nil {
			return out
		}
	}
	return unknownFact
}

// unknownFact is what a host fact reads as when nothing could answer it. It is
// a word rather than an empty string so a record never carries a blank where a
// reader would look for a machine.
const unknownFact = "unknown"

// probe runs a short informational command and returns its trimmed output.
// Every caller treats a failure as an unknown fact, so the timeout only exists
// to keep a wedged tool from wedging the benchmark.
func probe(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output() // #nosec G204 -- fixed command names, constant arguments
	if err != nil {
		return "", fmt.Errorf("run %s: %w", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// parseCPUModel picks the first model name out of /proc/cpuinfo content.
//
// Three spellings exist: x86 reports "model name", arm64 reports "Model",
// "Processor" or nothing at all, in which case the caller keeps "unknown"
// rather than inventing a processor.
func parseCPUModel(cpuinfo string) string {
	scanner := bufio.NewScanner(strings.NewReader(cpuinfo))
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "model name", "Model", "Processor":
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// totalMemoryGiB reports installed memory, which bounds what any measurement
// here could possibly have needed.
func totalMemoryGiB(ctx context.Context) float64 {
	if runtimeGOOS == "linux" {
		return round(parseMeminfoKiB(readFileString("/proc/meminfo"), "MemTotal:") / (1024 * 1024))
	}
	if runtimeGOOS == "darwin" {
		if out, err := probe(ctx, "sysctl", "-n", "hw.memsize"); err == nil {
			if bytes, parseErr := strconv.ParseFloat(out, 64); parseErr == nil {
				return round(bytes / (1024 * 1024 * 1024))
			}
		}
	}
	return 0
}

// availableMemoryMiB reports what the kernel says could be given to a new
// process without swapping, which is what the concurrency series budgets
// against.
//
// Linux only, from MemAvailable, which accounts for reclaimable cache; the free
// figure alone would under-report by whatever the page cache holds. Elsewhere
// this answers zero, and the series then runs with no budget and says so in its
// notes rather than inventing one from installed memory, which says nothing
// about what is in use.
func availableMemoryMiB() float64 {
	if runtimeGOOS != "linux" {
		return 0
	}
	return round(parseMeminfoKiB(readFileString("/proc/meminfo"), "MemAvailable:") / 1024)
}

// parseMeminfoKiB extracts one field of /proc/meminfo content, in kibibytes,
// zero when the field is absent or unreadable.
func parseMeminfoKiB(meminfo, field string) float64 {
	scanner := bufio.NewScanner(strings.NewReader(meminfo))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == field {
			if value, err := strconv.ParseFloat(fields[1], 64); err == nil {
				return value
			}
		}
	}
	return 0
}

// kernelRelease names the kernel, which is the layer that reports the resident
// set this command trusts.
func kernelRelease(ctx context.Context) string {
	if out, err := probe(ctx, "uname", "-r"); err == nil {
		return out
	}
	return unknownFact
}

// readFileString reads a file, returning empty on any failure: every caller
// here treats an unreadable source as an unknown fact.
func readFileString(path string) string {
	data, err := os.ReadFile(path) // #nosec G304 -- fixed /proc paths only
	if err != nil {
		return ""
	}
	return string(data)
}

// round trims a measurement to two decimals, which is the precision the record
// publishes. Anything finer is noise between two runs on the same machine, and
// printing it invites a reader to compare digits that mean nothing.
func round(value float64) float64 {
	return math.Round(value*100) / 100
}

// describe renders the host as one sentence, for the record's own header and
// for the terminal.
func (h HostInfo) describe() string {
	parts := []string{h.CPUModel}
	if h.CPUs > 0 {
		parts = append(parts, strconv.Itoa(h.CPUs)+" logical CPUs")
	}
	if h.MemTotalGiB > 0 {
		parts = append(parts, strconv.FormatFloat(h.MemTotalGiB, 'f', 0, 64)+" GiB RAM")
	}
	parts = append(parts, h.OS+"/"+h.Arch)
	if h.Kernel != "" && h.Kernel != unknownFact {
		parts = append(parts, "kernel "+h.Kernel)
	}
	return strings.Join(append(parts, h.GoVersion), ", ")
}
