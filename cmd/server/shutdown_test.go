package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// helperEnv marks a re-exec of this test binary as the child a shutdown case
// wants to terminate.
const helperEnv = "LIBGEN_MCP_SHUTDOWN_HELPER"

// TestHelperProcess is not a test. It is the body of the child process the
// shutdown cases spawn: a process that stays alive until something ends it.
//
// Re-executing the test binary is what makes the child findable at all —
// [findPeers] matches on the binary name, and the name here is the test
// binary's. It sleeps rather than blocking forever so a case that fails to
// terminate it does not leave it behind for the rest of the run.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("not the helper child")
	}
	time.Sleep(2 * time.Minute)
	os.Exit(0)
}

// startHelper spawns one child and returns a handle to it.
func startHelper(t *testing.T) *processHandle {
	t.Helper()

	//nolint:gosec // this test binary, re-executed
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=TestHelperProcess")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	// Reaped on a goroutine, so a child that exits on its own does not become a
	// zombie that IsRunning keeps reporting alive.
	go func() { _ = cmd.Wait() }()

	handle, err := process.NewProcess(int32(cmd.Process.Pid)) //nolint:gosec // a pid this test just created
	if err != nil {
		t.Fatalf("looking up the helper: %v", err)
	}
	return handle
}

// withProcessList replaces the process listing for one test.
//
// The real listing is deliberately not used here: it would find every process
// on the machine whose name matches this test binary's, and a second `go test`
// running beside this one would be terminated by it. The cases below own the
// processes they end.
func withProcessList(t *testing.T, list func() ([]*processHandle, error)) {
	t.Helper()
	previous := listProcesses
	t.Cleanup(func() { listProcesses = previous })
	listProcesses = list
}

// TestRunShutdownTerminatesAPeer is the whole command: ask, wait, and report.
func TestRunShutdownTerminatesAPeer(t *testing.T) {
	helper := startHelper(t)
	withProcessList(t, func() ([]*processHandle, error) { return []*processHandle{helper}, nil })

	var stderr bytes.Buffer
	if got := runShutdown(&stderr); got != 0 {
		t.Fatalf("exit = %d, want 0 (%s)", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "found 1 running instance") {
		t.Errorf("stderr = %q, want it to say what it found", stderr.String())
	}
	if !strings.Contains(stderr.String(), "terminated") {
		t.Errorf("stderr = %q, want it to report the outcome", stderr.String())
	}

	if running, _ := helper.IsRunning(); running {
		t.Error("the peer is still running after --shutdown returned")
	}
}

// TestFindPeersMatchesOnTheBinaryName covers the lookup against a real process
// list, which is the half withProcessList replaces everywhere else.
//
// The assertion is deliberately one-sided: whether any peer is found depends on
// what else is running on the machine, and the thing worth pinning is that this
// process never finds itself — a --shutdown that did would terminate the caller.
func TestFindPeersMatchesOnTheBinaryName(t *testing.T) {
	peers, err := findPeers()
	if err != nil {
		t.Skipf("the process list is not readable here: %v", err)
	}
	self := int32(os.Getpid()) //nolint:gosec // a pid fits
	for _, p := range peers {
		if p.Pid == self {
			t.Fatal("findPeers returned this process; --shutdown would terminate its own caller")
		}
	}
}

// TestFindPeersReportsAnUnreadableList covers the one failure the lookup has.
func TestFindPeersReportsAnUnreadableList(t *testing.T) {
	withProcessList(t, func() ([]*processHandle, error) { return nil, os.ErrPermission })

	if _, err := findPeers(); err == nil {
		t.Error("an unreadable process list was reported as no peers")
	}
}

// TestCountAliveAndForceKill covers the two halves of the deadline branch,
// against a process that really is running.
func TestCountAliveAndForceKill(t *testing.T) {
	helper := startHelper(t)
	peers := []*processHandle{helper}

	if got := countAlive(peers); got != 1 {
		t.Fatalf("countAlive = %d, want 1 with a live child", got)
	}

	var stderr bytes.Buffer
	forceKillPeers(peers, &stderr)
	if !strings.Contains(stderr.String(), "force-killed 1") {
		t.Errorf("stderr = %q, want it to report the kill", stderr.String())
	}

	// The kill is asynchronous on some platforms, so this is a poll rather than
	// an immediate read.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if countAlive(peers) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("the child is still running after forceKillPeers")
}

// TestForceKillPeersWithNothingLeftReportsSuccess pins the outcome that reads
// like a contradiction and is not: a peer can exit between the last poll and the
// deadline, and then there is nothing to kill and nothing to complain about.
func TestForceKillPeersWithNothingLeftReportsSuccess(t *testing.T) {
	var stderr bytes.Buffer
	forceKillPeers(nil, &stderr)

	if strings.Contains(stderr.String(), "force-killed") {
		t.Errorf("stderr = %q, want no kill to be claimed", stderr.String())
	}
	if !strings.Contains(stderr.String(), "all instances terminated") {
		t.Errorf("stderr = %q, want the success line", stderr.String())
	}
}
