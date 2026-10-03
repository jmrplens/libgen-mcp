package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// lockedBuffer is a bytes.Buffer a log handler on another goroutine may write
// while the test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends p under the lock.
func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns everything written so far.
func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureDebugLogs routes the default logger, at every level, into a buffer
// for one test.
func captureDebugLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	logs := &lockedBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return logs
}

// fakeStopWatch is a stopSignalWatch whose signals, window and exit are all
// driven by the test.
type fakeStopWatch struct {
	signals chan os.Signal
	window  chan time.Time
	asked   atomic.Int64
	forced  chan os.Signal
	ctx     context.Context
	done    chan struct{}
}

// startFakeStopWatch runs a watch on unbuffered channels, so every send
// returns only once the watch has taken it, and the test knows which select
// case did.
func startFakeStopWatch(t *testing.T) *fakeStopWatch {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f := &fakeStopWatch{
		signals: make(chan os.Signal),
		window:  make(chan time.Time),
		forced:  make(chan os.Signal, 1),
		ctx:     ctx,
		done:    make(chan struct{}),
	}
	w := stopSignalWatch{
		signals: f.signals,
		cancel:  cancel,
		window:  repeatedStopSignalWindow,
		after: func(d time.Duration) <-chan time.Time {
			f.asked.Store(int64(d))
			return f.window
		},
		force: func(sig os.Signal) { f.forced <- sig },
	}
	go func() {
		defer close(f.done)
		w.run()
	}()
	return f
}

// send delivers one signal and fails the test if the watch does not take it.
func (f *fakeStopWatch) send(t *testing.T, sig os.Signal) {
	t.Helper()
	select {
	case f.signals <- sig:
	case <-time.After(5 * time.Second):
		t.Fatalf("the watch did not take %v", sig)
	}
}

// closeWindow ends the window, and returns once the watch has seen it end.
//
// A send rather than a close: with both a closed window and a pending signal
// ready, select would pick either, and the test could not say which.
func (f *fakeStopWatch) closeWindow(t *testing.T) {
	t.Helper()
	select {
	case f.window <- time.Time{}:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch was not waiting on the window")
	}
}

// wait returns once the watch has ended.
func (f *fakeStopWatch) wait(t *testing.T) {
	t.Helper()
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch did not end")
	}
}

// assertNotForced fails the test when the watch has asked for the exit.
func (f *fakeStopWatch) assertNotForced(t *testing.T, when string) {
	t.Helper()
	select {
	case sig := <-f.forced:
		t.Fatalf("%s forced the exit with %v", when, sig)
	default:
	}
}

// TestStopSignalWatch_RepeatInsideTheWindowIsTheSameRequest is the fix: the
// copies a process group, a cgroup or a launcher's relay deliver within
// milliseconds of the first signal must not end the drain the first one began,
// and a signal after the window still ends the process at once.
func TestStopSignalWatch_RepeatInsideTheWindowIsTheSameRequest(t *testing.T) {
	logs := captureDebugLogs(t)
	f := startFakeStopWatch(t)

	f.send(t, syscall.SIGTERM)
	select {
	case <-f.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the first signal did not cancel the root context")
	}

	// Each send returns once the watch has taken it, and until the window ends
	// the only place it takes one is the loop that ignores repeats — so by the
	// time these return, a watch that acted on either has already asked.
	f.send(t, syscall.SIGTERM)
	f.send(t, os.Interrupt)
	f.assertNotForced(t, "a repeat inside the window")
	if got := time.Duration(f.asked.Load()); got != repeatedStopSignalWindow {
		t.Errorf("window started for %v, want %v", got, repeatedStopSignalWindow)
	}

	f.closeWindow(t)
	f.send(t, os.Interrupt)
	select {
	case sig := <-f.forced:
		if sig != os.Interrupt {
			t.Errorf("forced with %v, want the signal that asked for it (%v)", sig, os.Interrupt)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a signal after the window did not force the exit")
	}
	f.wait(t)

	out := logs.String()
	if n := strings.Count(out, "level=DEBUG msg=\"stop signal repeated within the window"); n != 2 {
		t.Errorf("logged %d ignored repeats at DEBUG, want 2:\n%s", n, out)
	}
	if !strings.Contains(out, "level=INFO msg=\"stop signal received, shutting down gracefully\"") {
		t.Errorf("the first signal was not logged at INFO:\n%s", out)
	}
	if !strings.Contains(out, "level=WARN msg=\"stop signal received again, exiting at once") {
		t.Errorf("the forced exit was not logged at WARN:\n%s", out)
	}
}

// TestStopSignalWatch_EndsWithoutForcingWhenStopped pins that closing the
// signal channel ends the watch quietly at each of its three waits, so the
// deferred stop at the end of main never turns into an exit.
func TestStopSignalWatch_EndsWithoutForcingWhenStopped(t *testing.T) {
	tests := []struct {
		name string
		// before drives the watch to the wait under test.
		before   func(t *testing.T, f *fakeStopWatch)
		canceled bool
	}{
		{name: "before any signal", before: func(*testing.T, *fakeStopWatch) {}},
		{
			name:     "inside the window",
			before:   func(t *testing.T, f *fakeStopWatch) { t.Helper(); f.send(t, syscall.SIGTERM) },
			canceled: true,
		},
		{
			name: "after the window",
			before: func(t *testing.T, f *fakeStopWatch) {
				t.Helper()
				f.send(t, syscall.SIGTERM)
				f.closeWindow(t)
			},
			canceled: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := startFakeStopWatch(t)
			tt.before(t, f)
			close(f.signals)
			f.wait(t)
			f.assertNotForced(t, "stopping the watch")
			if got := f.ctx.Err() != nil; got != tt.canceled {
				t.Errorf("root context canceled = %v, want %v", got, tt.canceled)
			}
		})
	}
}

// TestWatchStopSignals_RealSignalCancelsAndStopIsIdempotent drives the
// production wiring with a real signal to this process: the context it
// returns is canceled, and the stop function can run twice, which the
// deferred call and an early return may both do.
func TestWatchStopSignals_RealSignalCancelsAndStopIsIdempotent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a process cannot send itself a signal on Windows")
	}
	ctx, stop := watchStopSignals(context.Background())
	t.Cleanup(stop)

	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("finding this process: %v", err)
	}
	if sigErr := self.Signal(syscall.SIGTERM); sigErr != nil {
		t.Fatalf("signaling this process: %v", sigErr)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM did not cancel the context")
	}
	stop()
	stop()
}

// TestWatchStopSignals_StopCancelsTheContext pins that stopping the watch
// releases the context too, so nothing derived from it outlives main.
func TestWatchStopSignals_StopCancelsTheContext(t *testing.T) {
	ctx, stop := watchStopSignals(context.Background())
	stop()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("context error after stop = %v, want canceled", ctx.Err())
	}
}

// forceExitHelperEnv marks a re-exec of this test binary as the child
// TestForceExit_DiesByTheSignal ends.
const forceExitHelperEnv = "LIBGEN_MCP_FORCE_EXIT_HELPER"

// TestForceExitHelperProcess is not a test. It is the body of the child
// TestForceExit_DiesByTheSignal spawns, which forces its own exit.
func TestForceExitHelperProcess(t *testing.T) {
	if os.Getenv(forceExitHelperEnv) != "1" {
		t.Skip("not the helper child")
	}
	forceExit(syscall.SIGTERM)
}

// TestForceExit_DiesByTheSignal pins that a forced exit reaches the parent as
// a death by the signal, the status the default action gave before the
// window existed and the one the npm launcher mirrors outward. It runs in a
// child, because the process it ends is its own.
func TestForceExit_DiesByTheSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a process cannot send itself a signal on Windows")
	}
	//nolint:gosec // this test binary, re-executed
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestForceExitHelperProcess$")
	cmd.Env = append(os.Environ(), forceExitHelperEnv+"=1")
	start := time.Now()
	err := cmd.Run()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("the child ended with %v, want a death by SIGTERM", err)
	}
	if code := exitErr.ExitCode(); code != -1 {
		t.Errorf("the child exited with status %d, want a death by signal (-1)", code)
	}
	if !strings.Contains(exitErr.String(), "terminated") {
		t.Errorf("the child ended with %q, want a death by SIGTERM", exitErr.String())
	}
	if elapsed := time.Since(start); elapsed >= forceExitFallbackDelay+5*time.Second {
		t.Errorf("the forced exit took %v, so the fallback ended it rather than the signal", elapsed)
	}
}

// TestSignalExitCode pins the status a shell reports for each stop signal.
func TestSignalExitCode(t *testing.T) {
	tests := []struct {
		name string
		sig  os.Signal
		want int
	}{
		{name: "SIGINT", sig: os.Interrupt, want: 130},
		{name: "SIGTERM", sig: syscall.SIGTERM, want: 143},
		{name: "not a syscall signal", sig: otherSignal{}, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := signalExitCode(tt.sig); got != tt.want {
				t.Errorf("signalExitCode(%v) = %d, want %d", tt.sig, got, tt.want)
			}
		})
	}
}

// otherSignal is an os.Signal that is not a syscall.Signal.
type otherSignal struct{}

// String names the signal.
func (otherSignal) String() string { return "other" }

// Signal marks the type as an os.Signal.
func (otherSignal) Signal() {}
