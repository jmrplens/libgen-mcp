// shutdown.go ends the other instances of this binary on this machine.
//
// It exists for the upgrade: swapping the binary under an npm launcher, a .mcpb
// bundle or a plain `cp` leaves the old process running, and this one holds a
// download slot, a temp-cache entry and its TTL, and a listener the new one
// wants. `libgen-mcp --shutdown` asks them to go and, once they have had the
// time they asked for, makes them: five seconds for an instance that does not
// drain, and its own drain plus the shutdown phase for one that does.
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

	"github.com/jmrplens/libgen-mcp/v2/internal/config"
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

// shutdownExitMargin is what a draining peer is given beyond its drain and its
// shutdown phase, for closing the remaining connections and exiting.
const shutdownExitMargin = 2 * time.Second

// maxShutdownGrace is the longest runShutdown ever waits: the longest drain
// the server accepts, its shutdown phase and the margin. A peer whose command
// line names a longer drain was refused at startup, so it is not running.
const maxShutdownGrace = maxDrainDelay + httpShutdownTimeout + shutdownExitMargin

// peerGrace is how long a peer started with args and env is waited for after
// it is asked to exit.
//
// A peer that drains sleeps out --drain-delay with /health answering 503
// before it begins closing, and the close itself may take httpShutdownTimeout
// with a stream open. Killing it at five seconds, which is what this command
// used to do, ended a systemd service mid-drain — and systemd, seeing a kill
// rather than an exit, restarted it. So such a peer gets its whole budget. One
// that does not drain keeps the five seconds it always had: the wait ends the
// moment the last peer exits, so the grace only matters for one that is stuck.
//
// The drain is read the way the peer read it: the flag on its command line
// first, then LIBGEN_MCP_DRAIN_DELAY from its environment. A value that does
// not parse is one the peer refused, so it is not draining on it.
func peerGrace(args []string, env map[string]string) time.Duration {
	value := parseListenerFlags(args).drainDelay
	if value == "" {
		value = env[config.EnvName("DRAIN_DELAY")]
	}
	delay, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || delay <= 0 {
		return shutdownGracePeriod
	}
	return min(delay+httpShutdownTimeout+shutdownExitMargin, maxShutdownGrace)
}

// shutdownGrace is the longest grace any of peers needs, read through
// cmdline and environ. A peer whose command line cannot be read gets the
// default, since nothing says it drains.
func shutdownGrace(peers []*processHandle, cmdline func(*processHandle) ([]string, error), environ func(int32) (map[string]string, error)) time.Duration {
	grace := shutdownGracePeriod
	for _, p := range peers {
		args, err := cmdline(p)
		if err != nil || len(args) == 0 {
			continue
		}
		// An unreadable environment is an empty one: the command line is still
		// read, and the default stands for what it does not say.
		env, _ := environ(p.Pid)
		grace = max(grace, peerGrace(args[1:], env))
	}
	return grace
}

// peerCmdline reads a peer's command line, argv[0] included.
func peerCmdline(p *processHandle) ([]string, error) {
	return p.CmdlineSlice()
}

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

	// Read before the signal, while every peer is still there to be read.
	grace := shutdownGrace(peers, peerCmdline, peerEnviron)
	if grace > shutdownGracePeriod {
		fmt.Fprintf(stderr, "shutdown: waiting up to %s for a draining instance to exit\n", grace)
	}

	for _, p := range peers {
		// SIGTERM on Unix, TerminateProcess on Windows: the same signal the
		// drain path is written for.
		_ = p.Terminate()
	}

	deadline := time.After(grace)
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
