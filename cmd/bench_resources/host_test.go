package main

import (
	"runtime"
	"strings"
	"testing"
)

// TestParseCPUModel_ReadsEverySpellingOrNothing verifies the three keys a
// processor name arrives under, and that a /proc/cpuinfo with none of them
// yields no model rather than a guess: the caller turns an empty answer into
// "unknown", which is a fact, and a wrong processor name is not.
func TestParseCPUModel_ReadsEverySpellingOrNothing(t *testing.T) {
	testCases := []struct {
		name    string
		cpuinfo string
		want    string
	}{
		{
			name:    "x86 spelling",
			cpuinfo: "processor\t: 0\nmodel name\t: AMD Ryzen 5 3550H\nflags\t\t: fpu\n",
			want:    "AMD Ryzen 5 3550H",
		},
		{name: "arm64 spelling", cpuinfo: "Model\t: Raspberry Pi 5\n", want: "Raspberry Pi 5"},
		{name: "older arm spelling", cpuinfo: "Processor\t: ARMv7 rev 3\n", want: "ARMv7 rev 3"},
		{name: "the key with an empty value", cpuinfo: "model name\t:   \n", want: ""},
		{name: "a line with no colon", cpuinfo: "model name\n", want: ""},
		{name: "nothing at all", cpuinfo: "", want: ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCPUModel(tc.cpuinfo); got != tc.want {
				t.Errorf("parseCPUModel() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParseMeminfoKiB_ReadsTheNamedFieldOnly verifies the two fields the host
// facts are built from are told apart, and that an absent or unparseable one
// reads as zero rather than as the other one.
func TestParseMeminfoKiB_ReadsTheNamedFieldOnly(t *testing.T) {
	const meminfo = "MemTotal:       65536000 kB\nMemFree:         1000000 kB\nMemAvailable:   32768000 kB\n"

	testCases := []struct {
		name, field string
		want        float64
	}{
		{name: "total", field: "MemTotal:", want: 65536000},
		{name: "available", field: "MemAvailable:", want: 32768000},
		{name: "a field that is not there", field: "MemNonsense:", want: 0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseMeminfoKiB(meminfo, tc.field); got != tc.want {
				t.Errorf("parseMeminfoKiB(%q) = %v, want %v", tc.field, got, tc.want)
			}
		})
	}

	t.Run("an unparseable value", func(t *testing.T) {
		if got := parseMeminfoKiB("MemTotal:  many kB\n", "MemTotal:"); got != 0 {
			t.Errorf("parseMeminfoKiB() = %v, want 0 for a value that is not a number", got)
		}
	})
}

// TestRound_TrimsToThePrecisionTheRecordPublishes verifies measurements are cut
// to two decimals, which is the precision two runs on one machine can agree on.
func TestRound_TrimsToThePrecisionTheRecordPublishes(t *testing.T) {
	testCases := []struct {
		name string
		in   float64
		want float64
	}{
		{name: "rounds up", in: 1.005999, want: 1.01},
		{name: "rounds down", in: 1.004, want: 1.0},
		{name: "leaves a whole number alone", in: 42, want: 42},
		{name: "keeps a sign", in: -1.239, want: -1.24},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := round(tc.in); got != tc.want {
				t.Errorf("round(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestHostInfo_NamesTheMachineItRanOn verifies the facts that cannot fail are
// always present: a record with no machine attached is the one thing this
// struct exists to prevent.
func TestHostInfo_NamesTheMachineItRanOn(t *testing.T) {
	info := hostInfo(t.Context())
	if info.OS != runtime.GOOS || info.Arch != runtime.GOARCH {
		t.Errorf("hostInfo() = %+v, want the running platform", info)
	}
	if info.CPUs < 1 {
		t.Errorf("CPUs = %d, want at least one", info.CPUs)
	}
	if info.GoVersion == "" {
		t.Error("GoVersion is empty")
	}
	if info.CPUModel == "" {
		t.Error("CPUModel is empty; it should be the word unknown rather than nothing")
	}
}

// TestDescribe_ReadsAsOneSentence verifies the host line carries what it can and
// leaves out what it could not learn, rather than printing "kernel unknown".
func TestDescribe_ReadsAsOneSentence(t *testing.T) {
	full := HostInfo{
		OS: "linux", Arch: "amd64", CPUModel: "Test CPU", CPUs: 8,
		MemTotalGiB: 61, Kernel: "6.1.0", GoVersion: "go1.27.0",
	}
	got := full.describe()
	for _, want := range []string{"Test CPU", "8 logical CPUs", "61 GiB RAM", "linux/amd64", "kernel 6.1.0", "go1.27.0"} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(got, want) {
				t.Errorf("describe() = %q, want it to carry %q", got, want)
			}
		})
	}

	t.Run("an unknown kernel is left out", func(t *testing.T) {
		sparse := HostInfo{OS: "windows", Arch: "amd64", CPUModel: unknownFact, Kernel: unknownFact, GoVersion: "go1.27.0"}
		if strings.Contains(sparse.describe(), "kernel") {
			t.Errorf("describe() = %q, want no kernel clause when nothing could read one", sparse.describe())
		}
	})
}

// TestProbe_ReportsAFailureRatherThanGuessing verifies a command that is not
// there is an error the caller turns into "unknown", not an empty fact that
// reads like a measurement.
func TestProbe_ReportsAFailureRatherThanGuessing(t *testing.T) {
	if _, err := probe(t.Context(), "libgen-mcp-no-such-command"); err == nil {
		t.Error("expected an error for a command that does not exist")
	}
}

// TestReadFileString_TreatsAnUnreadablePathAsAnUnknownFact verifies the reader
// every host fact goes through answers empty rather than failing the run.
func TestReadFileString_TreatsAnUnreadablePathAsAnUnknownFact(t *testing.T) {
	if got := readFileString("/proc/there-is-no-such-file"); got != "" {
		t.Errorf("readFileString() = %q, want empty", got)
	}
}

// TestPlatformBranches_AnswerOnEveryOS walks the per-platform branches from
// whichever platform the suite happens to run on.
//
// The sysctl and ps paths are only ever taken on macOS, and a test that could
// not reach them would leave them as the one part of this command nobody had
// run. Nothing is asserted about the values — they are the machine's — only
// that each branch answers instead of panicking or hanging.
func TestPlatformBranches_AnswerOnEveryOS(t *testing.T) {
	original := runtimeGOOS
	t.Cleanup(func() { runtimeGOOS = original })

	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			runtimeGOOS = goos
			if model := cpuModel(t.Context()); model == "" {
				t.Error("cpuModel() is empty; it should fall back to the word unknown")
			}
			if mem := totalMemoryGiB(t.Context()); mem < 0 {
				t.Errorf("totalMemoryGiB() = %v, want zero or more", mem)
			}
			if kernel := kernelRelease(t.Context()); kernel == "" {
				t.Error("kernelRelease() is empty")
			}
		})
	}
}
