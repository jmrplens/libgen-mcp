// stop_signal.go turns SIGINT and SIGTERM into the root context's
// cancellation, and decides what a repeated one means.
//
// The first signal starts the graceful shutdown: both transports unwind, and an
// HTTP server announces the drain for --drain-delay before it closes the
// listener. A later signal is the operator's escape hatch — somebody who has
// decided not to wait out the drain says so by pressing Ctrl+C again, and the
// process ends at once.
//
// A second signal is not always a second request, though. One stop often
// arrives twice within milliseconds: a terminal's Ctrl+C reaches the whole
// foreground process group, systemd's default KillMode=control-group signals
// every process in the unit, and the npm launcher relays the stop it received
// as well. Read as the escape hatch, each of those skipped the drain it was
// asking for — measured, a group SIGINT through the launcher ended a server
// with a three-second drain after 15 ms, and `systemctl stop` took 0.03 s
// instead of 3.03 s. So a repeat inside repeatedStopSignalWindow of the first
// is the same request and is ignored, and only one after it forces the exit.

package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// repeatedStopSignalWindow is how long after the first stop signal a repeat is
// taken to be a copy of the same request rather than a new one.
//
// One second, because the two things it separates are an order of magnitude
// apart on either side. Every fan-out it absorbs — a process group, a cgroup,
// a launcher's relay — delivers its copies within milliseconds of each other,
// and well under a hundred even on a loaded machine. A person who saw the
// shutdown begin, judged it too slow and pressed again takes longer than a
// second to do that, and one who pressed twice faster than that presses a
// third time and is heard. Longer would make the escape hatch feel broken,
// shorter would start to let a slow relay through.
const repeatedStopSignalWindow = time.Second

// forceExitFallbackDelay is how long forceExit waits for the re-raised signal
// to end the process before it exits by itself.
const forceExitFallbackDelay = time.Second

// stopSignals are the signals that ask this process to stop.
var stopSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// stopSignalWatch is the state one watcher of the stop signals runs on. Every
// field is a seam, so the policy can be driven without signaling the test
// binary or waiting on a real clock.
type stopSignalWatch struct {
	// signals delivers the stop signals. Closing it ends the watch.
	signals <-chan os.Signal
	// cancel cancels the root context, which is the graceful shutdown.
	cancel context.CancelFunc
	// window is how long a repeat is ignored for.
	window time.Duration
	// after starts the window. time.After outside tests.
	after func(time.Duration) <-chan time.Time
	// force ends the process without the drain. forceExit outside tests.
	force func(os.Signal)
}

// watchStopSignals returns a context canceled by the first SIGINT or SIGTERM,
// and the function that stops watching.
//
// It replaces signal.NotifyContext, whose stop the caller had to call at the
// first signal so a second could kill the process: that made the copy of one
// request a group delivers as fatal as a deliberate second press.
func watchStopSignals(parent context.Context) (ctx context.Context, stop func()) {
	ctx, cancel := context.WithCancel(parent)
	// Buffered so a burst of copies is not dropped before the watch has read
	// the first: signal.Notify never blocks on a full channel.
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, stopSignals...)
	watch := stopSignalWatch{
		signals: signals,
		cancel:  cancel,
		window:  repeatedStopSignalWindow,
		after:   time.After,
		force:   forceExit,
	}
	go watch.run()

	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			// Once Stop returns nothing more is sent on the channel, so closing
			// it is safe, and it is what ends the watch's goroutine.
			signal.Stop(signals)
			close(signals)
			cancel()
		})
	}
}

// run is the watch: the first signal cancels, repeats inside the window are
// ignored, and the first one after it forces the exit.
func (w stopSignalWatch) run() {
	first, ok := <-w.signals
	if !ok {
		return
	}
	slog.Info("stop signal received, shutting down gracefully",
		"signal", first.String(),
		"repeat_ignored_for", w.window.String())
	w.cancel()

	if !w.ignoreRepeats() {
		return
	}
	sig, ok := <-w.signals
	if !ok {
		return
	}
	slog.Warn("stop signal received again, exiting at once without finishing the graceful shutdown",
		"signal", sig.String())
	w.force(sig)
}

// ignoreRepeats drops every stop signal that arrives inside the window, and
// reports false when the watch ended before the window did.
func (w stopSignalWatch) ignoreRepeats() bool {
	closed := w.after(w.window)
	for {
		select {
		case sig, ok := <-w.signals:
			if !ok {
				return false
			}
			slog.Debug("stop signal repeated within the window, treated as the same request",
				"signal", sig.String(),
				"window", w.window.String())
		case <-closed:
			return true
		}
	}
}

// forceExit ends the process the way the signal's default action would, so a
// supervisor reading the exit status sees the signal rather than a number this
// server chose.
//
// The default action is restored and the signal sent again. Where that cannot
// be done — Windows has no way for a process to signal itself — or does not
// end the process, it exits with the status a shell reports for a death by
// that signal.
func forceExit(sig os.Signal) {
	signal.Reset(stopSignals...)
	if self, err := os.FindProcess(os.Getpid()); err == nil && self.Signal(sig) == nil {
		time.Sleep(forceExitFallbackDelay)
	}
	os.Exit(signalExitCode(sig))
}

// signalExitCode is the status a POSIX shell reports for a process the signal
// killed: 128 plus its number.
func signalExitCode(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok {
		return 128 + int(s)
	}
	return 1
}
