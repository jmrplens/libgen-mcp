// proc_sample.go reads what the operating system says about the server
// processes under measurement.
//
// The resident set is taken from the kernel rather than from inside the program
// on purpose. Go's own MemStats describe the heap the runtime manages, and an
// operator sizing a container limit is not billed for the heap: they are billed
// for the resident set, which includes the runtime's own arenas, the stacks,
// the binary's mapped text and everything the scavenger has not returned yet.
// Those numbers differ by a factor of two or more, and only one of them gets a
// process OOM-killed.

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// clockTicks is the kernel's USER_HZ, the unit /proc/<pid>/stat reports CPU
// time in. It is 100 on every Linux this runs on; getconf is consulted anyway
// because a wrong divisor would silently scale every CPU figure recorded.
var clockTicks = func() float64 {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// NOSONAR: S4036 asks whether PATH is trustworthy. This is a maintainer's
	// benchmark run from a developer's own shell, the command is a fixed
	// literal, and an answer that is missing or unparseable falls back to the
	// value every Linux this runs on has. Naming an absolute path instead would
	// be wrong on the platforms that have it somewhere else.
	out, err := exec.CommandContext(ctx, "getconf", "CLK_TCK").Output() // NOSONAR
	return ticksFromGetconf(out, err)
}()

// ticksFromGetconf reads USER_HZ out of getconf's answer, falling back to the
// value every Linux this runs on has when getconf is missing, silent or
// unparseable.
func ticksFromGetconf(out []byte, err error) float64 {
	if err != nil {
		return 100
	}
	value, parseErr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if parseErr != nil || value <= 0 {
		return 100
	}
	return value
}

// procRoot is where the kernel's process files are read from. A variable so a
// test can lay out a process directory of its own, including the one shape a
// live kernel never produces: a readable status beside an unreadable stat.
var procRoot = "/proc"

// procStat is one observation of one process.
type procStat struct {
	rssBytes   uint64
	cpuSeconds float64
}

// readProcStat asks the operating system about a process.
//
// Linux is read from /proc directly; everything else goes through ps, which
// macOS answers and Windows does not. A platform that cannot answer returns an
// error the caller records as a note rather than a failure, because a benchmark
// that refuses to run is worse than one that records latency without memory.
func readProcStat(ctx context.Context, pid int) (procStat, error) {
	if runtimeGOOS == "linux" {
		rss, err := parseProcStatusRSS(readFileString(fmt.Sprintf("%s/%d/status", procRoot, pid)))
		if err != nil {
			return procStat{}, err
		}
		cpu, cpuErr := parseProcStatCPU(readFileString(fmt.Sprintf("%s/%d/stat", procRoot, pid)))
		if cpuErr != nil {
			return procStat{}, cpuErr
		}
		return procStat{rssBytes: rss, cpuSeconds: cpu}, nil
	}
	psCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// NOSONAR: S4036, as above — a fixed command name on a maintainer's own
	// shell, whose failure is recorded as an unreadable sample rather than
	// acted on.
	//#nosec G204 -- the only argument is a process id this command started
	out, err := exec.CommandContext(psCtx, "ps", "-o", "rss=,time=", "-p", strconv.Itoa(pid)).Output() // NOSONAR
	if err != nil {
		return procStat{}, fmt.Errorf("ps for pid %d: %w", pid, err)
	}
	return parsePSStat(string(out))
}

// parseProcStatusRSS pulls VmRSS, in bytes, out of /proc/<pid>/status.
func parseProcStatusRSS(status string) (uint64, error) {
	scanner := bufio.NewScanner(strings.NewReader(status))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "VmRSS:" {
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse VmRSS %q: %w", fields[1], err)
			}
			return value * 1024, nil
		}
	}
	return 0, errors.New("no VmRSS line")
}

// parseProcStatCPU sums utime and stime from /proc/<pid>/stat, in seconds.
//
// The command name in field 2 is parenthesized and may contain spaces, so the
// fields are counted from the closing parenthesis rather than from the start of
// the line. Splitting the whole line on whitespace is the classic way to read
// the wrong two numbers.
func parseProcStatCPU(stat string) (float64, error) {
	_, afterName, found := strings.CutLast(stat, ")")
	if !found {
		return 0, errors.New("malformed stat line")
	}
	fields := strings.Fields(afterName)
	// After the closing parenthesis, field 1 is state, so utime (the 14th field
	// of the whole line) is index 11 here and stime is index 12.
	const utimeIndex, stimeIndex = 11, 12
	if len(fields) <= stimeIndex {
		return 0, fmt.Errorf("stat line has %d fields after the command name", len(fields))
	}
	utime, err := strconv.ParseFloat(fields[utimeIndex], 64)
	if err != nil {
		return 0, fmt.Errorf("parse utime: %w", err)
	}
	stime, err := strconv.ParseFloat(fields[stimeIndex], 64)
	if err != nil {
		return 0, fmt.Errorf("parse stime: %w", err)
	}
	return (utime + stime) / clockTicks, nil
}

// parsePSStat reads one "rss= time=" line as ps prints it: kibibytes and
// [[dd-]hh:]mm:ss.
func parsePSStat(out string) (procStat, error) {
	fields := strings.Fields(out)
	if len(fields) < 2 {
		return procStat{}, fmt.Errorf("ps output %q has %d fields", strings.TrimSpace(out), len(fields))
	}
	rss, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return procStat{}, fmt.Errorf("parse ps rss %q: %w", fields[0], err)
	}
	seconds, err := parsePSTime(fields[1])
	if err != nil {
		return procStat{}, err
	}
	return procStat{rssBytes: rss * 1024, cpuSeconds: seconds}, nil
}

// parsePSTime converts ps's CPU time to seconds.
func parsePSTime(value string) (float64, error) {
	days := 0.0
	if before, after, found := strings.Cut(value, "-"); found {
		parsed, err := strconv.ParseFloat(before, 64)
		if err != nil {
			return 0, fmt.Errorf("parse ps days %q: %w", before, err)
		}
		days = parsed
		value = after
	}
	total := 0.0
	for part := range strings.SplitSeq(value, ":") {
		number, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return 0, fmt.Errorf("parse ps time %q: %w", value, err)
		}
		total = total*60 + number
	}
	return days*86400 + total, nil
}

// sampler polls a set of processes and remembers the worst it saw.
//
// Peak matters more than the endpoint: the resident set a container limit has
// to survive is the one reached while every client was calling at once, not the
// one left over after the garbage collector caught up.
type sampler struct {
	interval time.Duration
	// ctx bounds the reads a sample makes. It matters only where the statistics
	// come from a subprocess rather than from /proc, but carrying it means a
	// canceled run stops sampling with everything else.
	//
	// It is stored rather than passed per method, which the usual guidance
	// argues against, because that guidance is written for request-scoped
	// calls: they have a caller to take a context from. This one is a worker.
	// start launches a ticker loop that nobody calls again, so the loop's own
	// reads have no parameter to arrive through.
	ctx context.Context // NOSONAR: a worker's context, for the reason above
	// pidsFn is asked for the set to sample on every tick rather than being
	// told once, because a stdio scenario grows a process per client while the
	// sampler is already running.
	pidsFn func() []int

	mu       sync.Mutex
	peak     uint64
	failures int
	// sum and count accumulate the samples of the current window, for the mean
	// recorded beside the peak: a step's mean is what the process weighs while
	// serving, its peak what a limit has to survive.
	sum   uint64
	count int

	stopCh   chan struct{}
	doneCh   chan struct{}
	stopping sync.Once
}

// newSampler creates a sampler that polls the processes pidsFn names, at the
// given interval.
func newSampler(ctx context.Context, interval time.Duration, pidsFn func() []int) *sampler {
	return &sampler{
		interval: interval,
		ctx:      ctx,
		pidsFn:   pidsFn,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// start begins polling until stop is called.
func (s *sampler) start() {
	go func() {
		defer close(s.doneCh)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-ticker.C:
				s.observe()
			}
		}
	}()
}

// stop ends polling and waits for the poller to finish, so a caller that reads
// the peak afterwards cannot race the last sample.
//
// Idempotent because a scenario stops sampling before it kills the process,
// while the deferred stop that guarantees the poller dies on an early return is
// still armed.
func (s *sampler) stop() {
	s.stopping.Do(func() {
		close(s.stopCh)
		<-s.doneCh
	})
}

// observe takes one sample and folds it into the peak.
func (s *sampler) observe() {
	current, err := s.current()
	if err != nil {
		s.mu.Lock()
		s.failures++
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	if current.rssBytes > s.peak {
		s.peak = current.rssBytes
	}
	s.sum += current.rssBytes
	s.count++
	s.mu.Unlock()
}

// meanRSS reports the average resident set over the samples of the current
// window, zero when nothing was sampled.
func (s *sampler) meanRSS() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.count == 0 {
		return 0
	}
	return s.sum / uint64(s.count) //#nosec G115 -- count is a positive sample count
}

// current sums every tracked process right now.
//
// A process that has already exited is skipped rather than failing the sample:
// on stdio a scenario ends by killing the processes one at a time, and a sample
// landing in the middle of that must not be recorded as a measurement failure.
func (s *sampler) current() (procStat, error) {
	var total procStat
	var seen int
	var lastErr error
	for _, pid := range s.pidsFn() {
		stat, err := readProcStat(s.ctx, pid)
		if err != nil {
			lastErr = err
			continue
		}
		total.rssBytes += stat.rssBytes
		total.cpuSeconds += stat.cpuSeconds
		seen++
	}
	if seen == 0 {
		if lastErr != nil {
			return procStat{}, lastErr
		}
		return procStat{}, errors.New("no processes tracked")
	}
	return total, nil
}

// peakRSS reports the largest resident set observed since the last reset,
// folding in a sample taken now so a short phase cannot end between ticks
// without ever being looked at.
func (s *sampler) peakRSS() uint64 {
	s.observe()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak
}

// currentRSS reports the resident set right now, zero when it cannot be read.
//
// It is not peakRSS: the peak is the worst of a window, and a reading taken
// after a phase has stopped is a different question — what the process weighs
// now that it is idle. Reporting the peak there would put the same number in
// two columns and call one of them settled.
func (s *sampler) currentRSS() uint64 {
	stat, err := s.current()
	if err != nil {
		return 0
	}
	return stat.rssBytes
}

// reset forgets the current window, so the next phase is measured on its own
// rather than inheriting the peak of the one before it.
func (s *sampler) reset() {
	s.mu.Lock()
	s.peak, s.sum, s.count = 0, 0, 0
	s.mu.Unlock()
}

// sampleFailures reports how many samples could not be taken, which is what
// turns "no memory figure" into a note naming the platform rather than a zero
// a reader would take for a measurement.
func (s *sampler) sampleFailures() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures
}

// cpuSeconds reports the processor time the tracked processes have consumed so
// far, and whether it could be read at all.
func (s *sampler) cpuSeconds() (float64, bool) {
	stat, err := s.current()
	if err != nil {
		return 0, false
	}
	return stat.cpuSeconds, true
}

// mibOf converts a byte count to mebibytes at the record's precision.
func mibOf(bytes uint64) float64 {
	return round(float64(bytes) / (1024 * 1024))
}

// processAlive reports whether the operating system still knows the process,
// which is how a scenario tells "the server exited" from "the server is slow".
func processAlive(ctx context.Context, pid int) bool {
	_, err := readProcStat(ctx, pid)
	return err == nil
}
