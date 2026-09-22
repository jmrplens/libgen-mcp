package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestTicksFromGetconf_FallsBackRatherThanScalingEveryFigure verifies the CPU
// divisor. A wrong USER_HZ would silently multiply or divide every processor
// figure this command publishes, so every answer that is not a positive number
// falls back to the value every Linux this runs on has.
func TestTicksFromGetconf_FallsBackRatherThanScalingEveryFigure(t *testing.T) {
	testCases := []struct {
		name string
		out  string
		err  error
		want float64
	}{
		{name: "a normal answer", out: "100\n", want: 100},
		{name: "an unusual but valid answer", out: "1000\n", want: 1000},
		{name: "getconf is missing", err: errors.New("no getconf"), want: 100},
		{name: "getconf said nothing", out: "", want: 100},
		{name: "getconf said something else", out: "many", want: 100},
		{name: "a value that would divide by zero", out: "0", want: 100},
		{name: "a negative value", out: "-100", want: 100},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ticksFromGetconf([]byte(tc.out), tc.err); got != tc.want {
				t.Errorf("ticksFromGetconf(%q, %v) = %v, want %v", tc.out, tc.err, got, tc.want)
			}
		})
	}
}

// TestParseProcStatusRSS_ReadsVmRSSInBytes verifies the resident set is read
// from the line that carries it and converted out of kibibytes.
func TestParseProcStatusRSS_ReadsVmRSSInBytes(t *testing.T) {
	testCases := []struct {
		name, status string
		want         uint64
		wantErr      bool
	}{
		{
			name:   "a normal status",
			status: "Name:\tlibgen-mcp\nVmPeak:\t  100000 kB\nVmRSS:\t   24576 kB\n",
			want:   24576 * 1024,
		},
		{name: "no VmRSS line", status: "Name:\tlibgen-mcp\n", wantErr: true},
		{name: "a VmRSS that is not a number", status: "VmRSS:\t lots kB\n", wantErr: true},
		{name: "an empty file", status: "", wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseProcStatusRSS(tc.status)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseProcStatusRSS: %v", err)
			}
			if got != tc.want {
				t.Errorf("parseProcStatusRSS() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestParseProcStatCPU_CountsFromTheClosingParenthesis verifies the one thing
// this parser exists for: a command name may contain spaces and parentheses, so
// the fields are counted from the last ")" rather than from the start of the
// line. Splitting the whole line on whitespace is the classic way to read the
// wrong two numbers, and the second case below is what it reads them from.
func TestParseProcStatCPU_CountsFromTheClosingParenthesis(t *testing.T) {
	// utime is the 14th field of the line and stime the 15th: after the command
	// name there is the state, then eleven fields, then the two.
	tail := "S 1 1 1 0 -1 4194304 100 0 0 0 250 150 0 0 20 0 5 0 100"

	testCases := []struct {
		name, stat string
		want       float64
		wantErr    bool
	}{
		{name: "a plain command name", stat: "42 (libgen-mcp) " + tail, want: 4},
		{name: "a command name with a space and a parenthesis", stat: "42 (lib gen (mcp)) " + tail, want: 4},
		{name: "no closing parenthesis", stat: "42 libgen-mcp " + tail, wantErr: true},
		{name: "too few fields", stat: "42 (libgen-mcp) S 1 1", wantErr: true},
		{name: "utime is not a number", stat: "42 (x) S 1 1 1 0 -1 4194304 100 0 0 0 x 150 0", wantErr: true},
		{name: "stime is not a number", stat: "42 (x) S 1 1 1 0 -1 4194304 100 0 0 0 250 y 0", wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseProcStatCPU(tc.stat)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseProcStatCPU: %v", err)
			}
			if want := tc.want * 100 / clockTicks; got != want {
				t.Errorf("parseProcStatCPU() = %v, want %v", got, want)
			}
		})
	}
}

// TestParsePSStat_ReadsTheMacOSFallback verifies the shape ps prints on the
// platforms with no /proc, including the day-prefixed time nothing on a short
// run produces and everything on a long-lived process does.
func TestParsePSStat_ReadsTheMacOSFallback(t *testing.T) {
	testCases := []struct {
		name, out string
		wantRSS   uint64
		wantCPU   float64
		wantErr   bool
	}{
		{name: "minutes and seconds", out: " 24576 01:30\n", wantRSS: 24576 * 1024, wantCPU: 90},
		{name: "hours too", out: " 24576 02:01:30\n", wantRSS: 24576 * 1024, wantCPU: 7290},
		{name: "days too", out: " 24576 1-00:00:01\n", wantRSS: 24576 * 1024, wantCPU: 86401},
		{name: "one field", out: "24576\n", wantErr: true},
		{name: "an rss that is not a number", out: "lots 01:30\n", wantErr: true},
		{name: "a time that is not a time", out: "24576 soon\n", wantErr: true},
		{name: "a day count that is not a number", out: "24576 x-01:30\n", wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePSStat(tc.out)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePSStat: %v", err)
			}
			if got.rssBytes != tc.wantRSS || got.cpuSeconds != tc.wantCPU {
				t.Errorf("parsePSStat() = %+v, want rss %d cpu %v", got, tc.wantRSS, tc.wantCPU)
			}
		})
	}
}

// fakeProcess lays out one process directory the way the kernel does, and
// returns the pid to read it by.
//
// The point is the shape a live kernel never produces: a readable status beside
// an unreadable stat. Every branch of readProcStat has to be reachable from a
// test, and on a real /proc the two files appear and disappear together.
func fakeProcess(t *testing.T, pid int, status, stat string) {
	t.Helper()
	dir := filepath.Join(procRoot, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("laying out a fake process: %v", err)
	}
	if status != "" {
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o600); err != nil {
			t.Fatalf("writing status: %v", err)
		}
	}
	if stat != "" {
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
			t.Fatalf("writing stat: %v", err)
		}
	}
}

// withFakeProc points the reader at a tree of this test's own for the duration.
func withFakeProc(t *testing.T) {
	t.Helper()
	original, originalOS := procRoot, runtimeGOOS
	procRoot, runtimeGOOS = t.TempDir(), "linux"
	t.Cleanup(func() { procRoot, runtimeGOOS = original, originalOS })
}

// TestReadProcStat_ReportsWhatTheKernelWouldNot verifies each way a process
// read can fail is an error the sampler records rather than a zero it would
// publish.
func TestReadProcStat_ReportsWhatTheKernelWouldNot(t *testing.T) {
	withFakeProc(t)
	const goodStat = "1 (x) S 1 1 1 0 -1 4194304 100 0 0 0 100 100 0 0 20 0 5 0 100"

	fakeProcess(t, 1, "VmRSS:\t   24576 kB\n", goodStat)
	fakeProcess(t, 2, "", goodStat)
	fakeProcess(t, 3, "VmRSS:\t   24576 kB\n", "")

	t.Run("a process the kernel describes", func(t *testing.T) {
		got, err := readProcStat(t.Context(), 1)
		if err != nil {
			t.Fatalf("readProcStat: %v", err)
		}
		if got.rssBytes != 24576*1024 {
			t.Errorf("rssBytes = %d, want %d", got.rssBytes, 24576*1024)
		}
	})
	t.Run("no status", func(t *testing.T) {
		if _, err := readProcStat(t.Context(), 2); err == nil {
			t.Error("expected an error with no status file")
		}
	})
	t.Run("a status with no stat beside it", func(t *testing.T) {
		if _, err := readProcStat(t.Context(), 3); err == nil {
			t.Error("expected an error with no stat file")
		}
	})
	t.Run("a process that is not there", func(t *testing.T) {
		if processAlive(t.Context(), 999) {
			t.Error("processAlive() = true for a pid with no directory")
		}
		if !processAlive(t.Context(), 1) {
			t.Error("processAlive() = false for a pid the tree describes")
		}
	})
}

// TestSampler_RemembersThePeakAndSurvivesADeadProcess verifies the three things
// a sampler has to get right: it keeps the worst it saw rather than the last, a
// process that has exited is skipped rather than recorded as a failure, and a
// window that is reset starts again from nothing.
func TestSampler_RemembersThePeakAndSurvivesADeadProcess(t *testing.T) {
	withFakeProc(t)
	const stat = "1 (x) S 1 1 1 0 -1 4194304 100 0 0 0 100 100 0 0 20 0 5 0 100"
	fakeProcess(t, 1, "VmRSS:\t   10240 kB\n", stat)
	fakeProcess(t, 2, "VmRSS:\t   20480 kB\n", stat)

	pids := []int{1, 2}
	sam := newSampler(t.Context(), time.Millisecond, func() []int { return pids })

	if got := mibOf(sam.peakRSS()); got != mibOf(30720*1024) {
		t.Errorf("peak over two processes = %v MiB, want their sum", got)
	}
	if mean := sam.meanRSS(); mean == 0 {
		t.Error("meanRSS() = 0 after a sample was taken")
	}
	if seconds, ok := sam.cpuSeconds(); !ok || seconds <= 0 {
		t.Errorf("cpuSeconds() = %v, %t; want a positive reading", seconds, ok)
	}

	t.Run("a smaller reading does not lower the peak", func(t *testing.T) {
		pids = []int{1}
		if got := mibOf(sam.peakRSS()); got != mibOf(30720*1024) {
			t.Errorf("peak = %v MiB after a smaller sample, want the earlier peak", got)
		}
	})

	// The settled reading asks a different question from the peak — what the
	// process weighs now that it is idle — so it has to read now rather than
	// returning the worst of the window. Reporting the peak there put the same
	// number in two columns of the published table and called one of them
	// settled.
	t.Run("the current reading is now, not the peak", func(t *testing.T) {
		pids = []int{1}
		if got := mibOf(sam.currentRSS()); got != mibOf(10240*1024) {
			t.Errorf("currentRSS() = %v MiB, want what is resident now", got)
		}
		pids = nil
		if got := sam.currentRSS(); got != 0 {
			t.Errorf("currentRSS() = %d with nothing to read, want 0", got)
		}
		// Put the set back: the subtests below share this sampler, and one that
		// left it empty would be deciding what the next one measures.
		pids = []int{1}
	})

	t.Run("reset starts the window again", func(t *testing.T) {
		sam.reset()
		if got := mibOf(sam.peakRSS()); got != mibOf(10240*1024) {
			t.Errorf("peak after reset = %v MiB, want only what has been sampled since", got)
		}
	})

	t.Run("every process gone is a failure, not a zero", func(t *testing.T) {
		pids = nil
		before := sam.sampleFailures()
		sam.observe()
		if sam.sampleFailures() <= before {
			t.Error("a sample with nothing to read should count as a failure")
		}
		if _, ok := sam.cpuSeconds(); ok {
			t.Error("cpuSeconds() reported a reading with no processes to read")
		}
	})
}

// TestSampler_StopIsIdempotent verifies stop can be called twice, which is what
// a scenario does: once before it kills the process, and again from the defer
// that guarantees the poller dies on an early return.
func TestSampler_StopIsIdempotent(t *testing.T) {
	sam := newSampler(context.Background(), time.Millisecond, func() []int { return nil })
	sam.start()
	sam.stop()
	sam.stop()
}

// TestMibOf_ConvertsAtTheRecordsPrecision verifies the one conversion every
// memory figure passes through.
func TestMibOf_ConvertsAtTheRecordsPrecision(t *testing.T) {
	if got := mibOf(1024 * 1024); got != 1 {
		t.Errorf("mibOf(1 MiB) = %v, want 1", got)
	}
	if got := mibOf(0); got != 0 {
		t.Errorf("mibOf(0) = %v, want 0", got)
	}
}
