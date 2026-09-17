// shutdown.go ends the other instances of this binary on this machine.
//
// It exists for the upgrade: swapping the binary under an npm launcher, a .mcpb
// bundle or a plain `cp` leaves the old process running, and this one holds a
// download slot, a temp-cache entry and its TTL, and a listener the new one
// wants. `libgen-mcp --shutdown` asks them to go and, after five seconds, makes
// them.
//
// It shares its process lookup with --healthcheck, which is the reason both
// land together: the dependency on a process list is the cost, and one consumer
// would not have justified it.

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// shutdownGracePeriod is how long runShutdown waits after asking before it
// stops asking.
//
// It is the graceful HTTP budget's neighbor rather than its copy: what is being
// waited for here is the whole process exiting, which on a server with an open
// stream is the drain plus the close. Five seconds is enough for an idle
// instance, and the force-kill is what bounds the rest — an upgrade that waited
// out every straggler would be an upgrade that appears to hang.
const shutdownGracePeriod = 5 * time.Second

// processHandle is one running process as the process list reports it.
//
// An alias rather than a wrapper: it is the gopsutil type, named here so the
// seam below and the code that reads it say what they mean without the
// dependency's package name appearing in every signature.
type processHandle = process.Process

// listProcesses enumerates every process on the machine. A variable because the
// listing fails only when the process table itself is unreadable, which no input
// to this binary produces, and the branch that reports it is otherwise never
// run.
var listProcesses = process.Processes

// runShutdown asks every other instance of this binary to exit, waits, and kills
// what is left. It returns the process exit code.
//
// Finding nothing is success, not failure: the point of the command is that no
// instance is running afterwards, and that is already true.
func runShutdown(stderr io.Writer) int {
	peers, err := findPeers()
	if err != nil {
		fmt.Fprintf(stderr, "shutdown: listing processes: %v\n", err)
		return 1
	}
	if len(peers) == 0 {
		return 0
	}
	fmt.Fprintf(stderr, "shutdown: found %d running instance(s)\n", len(peers))

	for _, p := range peers {
		// SIGTERM on Unix, TerminateProcess on Windows: the same signal the
		// drain path is written for.
		_ = p.Terminate()
	}

	deadline := time.After(shutdownGracePeriod)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			forceKillPeers(peers, stderr)
			return 0
		case <-ticker.C:
			if countAlive(peers) == 0 {
				fmt.Fprintf(stderr, "shutdown: all instances terminated\n")
				return 0
			}
		}
	}
}

// forceKillPeers kills every peer still running once the grace period has
// lapsed, and reports the outcome.
//
// Zero kills is a possible outcome rather than a contradiction: a peer can exit
// between the last poll and the deadline, and then there is nothing left to kill
// and nothing to complain about.
func forceKillPeers(peers []*process.Process, stderr io.Writer) {
	var killed int
	for _, p := range peers {
		if running, _ := p.IsRunning(); running {
			_ = p.Kill()
			killed++
		}
	}
	if killed > 0 {
		fmt.Fprintf(stderr, "shutdown: force-killed %d instance(s)\n", killed)
		return
	}
	fmt.Fprintf(stderr, "shutdown: all instances terminated\n")
}

// findPeers returns every running process whose binary name matches this one's,
// excluding the current process.
func findPeers() ([]*process.Process, error) {
	self := os.Getpid()
	baseName := canonicalBinaryName(filepath.Base(os.Args[0]))

	all, err := listProcesses()
	if err != nil {
		return nil, err
	}

	var peers []*process.Process
	for _, p := range all {
		if int(p.Pid) == self {
			continue
		}
		name, nameErr := p.Name()
		if nameErr != nil {
			continue
		}
		if canonicalBinaryName(name) == baseName {
			peers = append(peers, p)
		}
	}
	return peers, nil
}

// canonicalBinaryName strips the OS/arch suffix and the .exe extension so every
// platform variant of this binary compares equal.
//
// It is what keeps a healthcheck from reporting somebody else's container
// healthy on a shared host, and a shutdown from leaving the one instance it was
// run to replace: a release asset is named libgen-mcp-linux-amd64 and the
// process it becomes is often renamed on the way in.
//
//	"libgen-mcp-linux-amd64" → "libgen-mcp"
//	"libgen-mcp.exe"         → "libgen-mcp"
//	"libgen-mcp"             → "libgen-mcp"
func canonicalBinaryName(name string) string {
	name = strings.TrimSuffix(name, ".exe")
	for _, suffix := range []string{
		"-linux-amd64", "-linux-arm64",
		"-darwin-amd64", "-darwin-arm64",
		"-windows-amd64", "-windows-arm64",
	} {
		name = strings.TrimSuffix(name, suffix)
	}
	return name
}

// countAlive returns how many of these processes are still running.
func countAlive(procs []*process.Process) int {
	var n int
	for _, p := range procs {
		if running, _ := p.IsRunning(); running {
			n++
		}
	}
	return n
}
