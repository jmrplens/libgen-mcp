// transport.go settles which transport this process serves, which is not always
// something the command line says.
//
// One image has to do two jobs. A container published on a port must serve HTTP
// with no arguments, or the port is pointless; an MCP client that runs the same
// image with `docker run -i` must get stdio, or it waits at initialize forever
// with no output at all. Both are the default invocation, so the decision has to
// be read off the process rather than declared.

package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// The transport selectors --transport accepts.
const (
	transportStdio = "stdio"
	transportHTTP  = "http"
	transportAuto  = "auto"
)

// defaultHTTPAddr is where the HTTP transport binds when the transport was
// chosen without an address — `--transport http` on its own, or `--transport
// auto` resolving to HTTP inside a container whose port is published.
//
// A wildcard bind, because the deployment it exists for is a container: nothing
// inside one can reach a loopback listener from outside it. That also means it
// is a listener anyone on the network can reach, which is exactly the shape
// refusePrivateHatchOnOpenListener judges — and judges on this resolved value,
// not on whether --http was typed.
const defaultHTTPAddr = ":8080"

// stdinStat is the stat of file descriptor 0, replaced in tests. It is a
// variable rather than a parameter because the thing under test is a property of
// the process, and threading it through main's signature would put a seam in
// production code that only tests would ever use.
var stdinStat = func() (os.FileInfo, error) { return os.Stdin.Stat() }

// devNullStat is the stat of the null device, replaced in tests alongside
// stdinStat so a case can make the two the same file or not.
var devNullStat = func() (os.FileInfo, error) { return os.Stat(os.DevNull) }

// transportDecision is how the transport was settled, carried as data so it can
// be logged once the real handler is installed.
//
// The decision itself cannot wait for that handler: the listener is bound and
// the download mode is chosen from it, and both happen before config.Load has
// read LIBGEN_MCP_LOG_LEVEL. Logging through whatever handler is in place at
// that moment would put a plain-text line onto a stream that is otherwise JSON
// records.
type transportDecision struct {
	// HTTP is the resolved transport.
	HTTP bool
	// Addr is the address the HTTP transport binds, and is empty on stdio. It
	// is the resolved value: every later decision that used to read the --http
	// flag reads this instead.
	Addr string
	// Override is what to tell an operator whose two selectors disagreed, or
	// empty.
	Override string
	// Inference is the observation auto rested on, or empty when the operator
	// stated the transport rather than leaving it to be read.
	Inference string
	// Interactive records that this is a stdio session with a person at the
	// other end, which is the one case that gets a word of explanation.
	Interactive bool
}

// explain writes the decision to the log.
//
// Called once the real handler is in place. A run that exits before then never
// started a transport, so there is nothing to explain.
func (d transportDecision) explain() {
	if d.Override != "" {
		slog.Warn(d.Override)
	}
	if d.Inference != "" {
		slog.Info("transport inferred from stdin", "transport", transportName(d.HTTP), "reason", d.Inference)
	}
}

// resolveTransport turns the two transport selectors into one decision, along
// with the explanation of how it got there when it inferred rather than obeyed.
//
// --http keeps exactly the meaning it had, so every existing invocation still
// works: a value means HTTP on that address, and no value means stdio.
// --transport is the newer, three-valued spelling, and it decides the transport
// while --http supplies the address — which is why `--transport auto --http
// 127.0.0.1:8080` is not a contradiction but an answer to two different
// questions.
func resolveTransport(selector, httpAddr string) (transportDecision, error) {
	switch strings.TrimSpace(strings.ToLower(selector)) {
	case "":
		return decide(httpAddr != "", httpAddr, ""), nil
	case transportStdio:
		return decide(false, httpAddr, ""), nil
	case transportHTTP:
		return decide(true, httpAddr, ""), nil
	case transportAuto:
		useHTTP, why := inferTransport()
		return decide(useHTTP, httpAddr, why), nil
	default:
		return transportDecision{}, fmt.Errorf("--transport %q is not one of %s, %s, %s",
			selector, transportStdio, transportHTTP, transportAuto)
	}
}

// decide assembles the decision, filling in the address HTTP will bind and the
// warning a dropped address earns.
//
// An address given to a run that serves stdio is warned about rather than
// ignored: it is the one combination where the operator asked for something the
// process is not going to do, and a listener that never appears is the hardest
// kind of thing to notice is missing.
func decide(useHTTP bool, httpAddr, inference string) transportDecision {
	if !useHTTP {
		d := transportDecision{Inference: inference, Interactive: stdinIsTerminal()}
		if httpAddr != "" {
			d.Override = "--http " + httpAddr + " was given but this process is serving stdio; remove one of them to say plainly which transport you meant"
		}
		return d
	}
	if httpAddr == "" {
		httpAddr = defaultHTTPAddr
	}
	return transportDecision{HTTP: true, Addr: httpAddr, Inference: inference}
}

// inferTransport reads the transport off file descriptor 0, and reports the
// observation it decided on so the choice is never silent.
//
// The question is not "is this a terminal", which cannot separate the two cases
// that matter: a terminal and /dev/null are both character devices. It is "did
// anybody hand this process a stdin at all". Docker answers that precisely.
// `docker run` without -i, and Compose without stdin_open, connect file
// descriptor 0 to /dev/null; `docker run -i`, which is what every MCP client
// configuration uses, connects a pipe. Nothing speaks JSON-RPC down /dev/null,
// so that is the one shape that means HTTP.
//
// Everything else means stdio: a pipe is a client, a terminal is a person trying
// it by hand, a regular file is a shell redirect replaying a session, and a
// socket is a supervisor. Choosing stdio for an unrecognized shape is also the
// safer error, because a stdio server that should have been HTTP is obvious
// within seconds, while an HTTP listener that should have been stdio is a client
// hanging with no output at all, which is the defect this closes.
func inferTransport() (useHTTP bool, reason string) {
	info, err := stdinStat()
	if err != nil {
		// Not a reason to refuse to start: a stdin that cannot be stat'ed is
		// not a stdin anybody is speaking to.
		return false, "stdin could not be examined (" + err.Error() + ")"
	}
	mode := info.Mode()
	switch {
	case mode&os.ModeNamedPipe != 0:
		return false, "stdin is a pipe"
	case mode.IsRegular():
		return false, "stdin is a regular file"
	}
	devNull, err := devNullStat()
	if err != nil {
		return false, "the null device could not be examined (" + err.Error() + ")"
	}
	if os.SameFile(info, devNull) {
		return true, "stdin is " + os.DevNull + ", so no client is speaking to this process"
	}
	return false, "stdin is " + mode.String()
}

// stdinIsTerminal reports whether a person is at the other end of stdin.
//
// It is the same two stats inferTransport uses rather than a terminal-detection
// dependency, and it has to be: a terminal and /dev/null are both character
// devices, so "is a character device" alone would call a container's null stdin
// a person and print guidance into its logs on every start.
func stdinIsTerminal() bool {
	info, err := stdinStat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	devNull, err := devNullStat()
	if err != nil {
		return false
	}
	return !os.SameFile(info, devNull)
}

// transportName spells the resolved transport for a log line.
func transportName(useHTTP bool) string {
	if useHTTP {
		return transportHTTP
	}
	return transportStdio
}

// writeTerminalGuidance explains what this program is to somebody who started it
// by hand, and then gets out of the way.
//
// A hint rather than a refusal, which is where this diverges from the sibling
// project it is adapted from: there is no unconfigured state to detect here —
// this server needs no account and no key — and a terminal on stdin is a
// legitimate way to hand-drive the protocol. So it says what the silence means
// and serves anyway.
//
// It writes to stderr, and that is not cosmetic. On this transport stdout
// carries JSON-RPC and nothing else, and a single stray line ends the session.
// Putting the one message written to be read by a human onto the one stream
// reserved for a machine would be a defect waiting for somebody to widen the
// guard above it.
func writeTerminalGuidance(out io.Writer, version string) {
	fmt.Fprintf(out, `libgen-mcp %s is a Model Context Protocol server, not an interactive program.
It is waiting for JSON-RPC on standard input, which is what an MCP client sends;
started from a terminal it will simply sit here. To serve over HTTP instead, pass
--http (for example --http 127.0.0.1:8080). Setup for each client is at
https://jmrp.io/docs/libgen-mcp/getting-started/
`, version)
}
