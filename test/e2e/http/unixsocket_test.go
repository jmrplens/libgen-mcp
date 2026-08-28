//go:build httpe2e

package httpe2e

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// assertSocketMode checks that the server created a socket and gave it exactly
// the permission bits asked for.
//
// Both halves matter. The type says the path is a socket rather than whatever
// else may have been there, and the mode is the whole reason the flag exists:
// the kernel creates the inode with 0777 &^ umask — 0755 on an ordinary process
// — so a server that skipped the narrowed umask and the chmod would still serve
// perfectly, on a socket every local account can connect to.
func assertSocketMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("the server did not create %s: %v", path, err)
	}
	if info.Mode()&fs.ModeSocket == 0 {
		t.Fatalf("%s is %s, want a socket", path, info.Mode())
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s has mode %#o, want %#o", path, got, want)
	}
}

// TestUnix_ServesTheWholeSurfaceOnASocket is the case the whole listener exists
// for: a deployment that reaches this server without a network segment between
// the two.
//
// The tool call is the assertion, not the health probe. /health is a two-line
// handler that would answer over anything, whereas a JSON-RPC result proves the
// MCP handler at the end of the chain — CORS, cross-origin protection, the
// no-buffering wrapper — is reachable on a transport it has never seen before.
func TestUnix_ServesTheWholeSurfaceOnASocket(t *testing.T) {
	path := socketPath(t)
	s := startUnixServerAt(t, path, nil)

	// Default mode: owner and group, so a proxy reaches it by sharing the group
	// and nobody else does.
	assertSocketMode(t, path, 0o660)

	if !s.healthy(t) {
		t.Fatalf("GET /health did not answer over the socket. Output:\n%s", s.logs())
	}
	assertToolsListed(t, "over a unix socket", s.do(t, mcpPOST(nil)))

	// The startup line names the socket, because ln.Addr() alone prints a bare
	// path that says nothing about what it is.
	if logs := s.logs(); !strings.Contains(logs, "unix socket "+path) {
		t.Errorf("the startup log does not name the socket:\n%s", logs)
	}
}

// TestUnix_SocketModeIsHonored covers --http-socket-mode over the real binary.
//
// The value without a leading zero is not padding for the table: the flag is a
// string parsed as octal precisely so "600" means what chmod means by it, and
// reading it as decimal — which flag.Uint would have done — would silently
// produce 0o1130 instead. Nothing but the mode on disk can tell the two apart.
func TestUnix_SocketModeIsHonored(t *testing.T) {
	cases := []struct {
		flag string
		want fs.FileMode
	}{
		{flag: "0600", want: 0o600},
		{flag: "600", want: 0o600},
		{flag: "0640", want: 0o640},
	}
	for _, tc := range cases {
		t.Run(tc.flag, func(t *testing.T) {
			path := socketPath(t)
			startUnixServerAt(t, path, nil, "--http-socket-mode", tc.flag)
			assertSocketMode(t, path, tc.want)
		})
	}
}

// TestUnix_HandlerChainIsNotTransportDependent asks the socket the questions
// the TCP cases already ask on a port.
//
// The chain is assembled once and handed to one http.Server, so in principle it
// cannot know what it is listening on. In practice the listener is the thing
// that changed, and a security header or a 404 that turned out to depend on it
// would be the kind of regression nobody looks for — every existing case here
// runs over TCP and would stay green.
func TestUnix_HandlerChainIsNotTransportDependent(t *testing.T) {
	s := startUnixServer(t, nil)

	reply := s.do(t, mcpPOST(nil))
	if reply.status != http.StatusOK {
		t.Fatalf("tools/list = %d, want %d (body: %s)", reply.status, http.StatusOK, truncate(reply.body))
	}
	// Including the absence of HSTS: a socket carries no scheme a browser could
	// upgrade, and this process is not terminating TLS on it.
	assertConstantSecurityHeaders(t, "an MCP POST over a unix socket", reply.header)
	assertNotCacheable(t, "an MCP POST over a unix socket", reply.header)

	assertNotFound(t, s, request{method: http.MethodGet, path: "/nope"}, "/")
}

// TestUnix_StaleSocketIsTakenOver covers the restart after a crash, which is
// the ordinary case and not an edge one.
//
// A killed process never unlinks its socket, so the file is still there when
// systemd or a container runtime starts the replacement. bind fails on an
// existing path whatever left it behind, so a server that did not clear it
// would refuse to start after every unclean stop — and the operator would find
// a stale file and no explanation.
func TestUnix_StaleSocketIsTakenOver(t *testing.T) {
	path := socketPath(t)
	leaveStaleSocket(t, path)

	s := startUnixServerAt(t, path, nil)
	assertToolsListed(t, "after taking over a stale socket", s.do(t, mcpPOST(nil)))

	// Removing it silently would be the wrong shape: an operator looking at why
	// a socket vanished has only the log to look at.
	if logs := s.logs(); !strings.Contains(logs, "stale unix socket") {
		t.Errorf("nothing in the log says the stale socket was removed:\n%s", logs)
	}
	assertSocketMode(t, path, 0o660)
}

// TestUnix_LiveSocketIsRefused is the other half of the case above, and the one
// worth more.
//
// Clearing a stale socket is a remove, and an unconditional remove is how a
// second instance silently steals the path the first is still serving: bind
// succeeds, the original keeps running with a socket no name points at, and
// every client that reconnects lands on the newcomer. The refusal is what makes
// the removal safe, so it is asserted next to it.
func TestUnix_LiveSocketIsRefused(t *testing.T) {
	path := socketPath(t)

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatalf("holding the socket open: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	out, err := runServerExpectingExit(t, "--http", path)
	if err == nil {
		t.Fatalf("the server started on a socket another process is serving. Output:\n%s", out)
	}
	if !strings.Contains(out, "already served by another process") {
		t.Errorf("the refusal does not say the socket is in use:\n%s", out)
	}
	// And the listener it refused to take is still the one on the path.
	if !stillListening(t, ln, path) {
		t.Error("the socket no longer reaches the process that was already serving it")
	}
}

// TestUnix_NonSocketPathIsRefused pins the one case where the file is left
// alone.
//
// A path that exists and is not a socket is something the operator pointed at —
// a typo landing on a config file, a bind mount that did not appear. Removing
// it to make room is not this program's call, and the assertion that matters is
// that the file is still there afterwards.
func TestUnix_NonSocketPathIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-socket")
	const contents = "important\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	out, err := runServerExpectingExit(t, "--http", path)
	if err == nil {
		t.Fatalf("the server started on a path that is not a socket. Output:\n%s", out)
	}
	if !strings.Contains(out, "not a socket") {
		t.Errorf("the refusal does not say what is wrong:\n%s", out)
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		t.Fatalf("the refused server removed the file: %v", statErr)
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(contents)) {
		t.Errorf("the file is now %s of %d bytes, want the regular file of %d it was", info.Mode(), info.Size(), len(contents))
	}
}

// TestUnix_StartupRefusals covers the flag combinations that must not reach a
// listener at all.
//
// Each one is a deployment that believes something about its socket which this
// build cannot deliver: a mode on an address that has no file, a mode that is
// not a mode, a directory that is not there. Accepting any of them quietly is
// worse than refusing, because the operator stops watching once the process
// stays up.
func TestUnix_StartupRefusals(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "no-such-dir", "mcp.sock")

	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "a socket mode with a TCP address",
			args: []string{"--http", "127.0.0.1:0", "--http-socket-mode", "0600"},
			want: "applies to a unix socket",
		},
		{
			name: "a mode that is not octal",
			args: []string{"--http", socketPath(t), "--http-socket-mode", "999"},
			want: "expected an octal mode",
		},
		{
			name: "a mode outside the permission bits",
			args: []string{"--http", socketPath(t), "--http-socket-mode", "07777"},
			want: "between 0001 and 0777",
		},
		{
			name: "a socket whose directory does not exist",
			args: []string{"--http", missingDir},
			want: "its directory is not usable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runServerExpectingExit(t, tc.args...)
			if err == nil {
				t.Fatalf("the server started anyway. Output:\n%s", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("the refusal does not mention %q:\n%s", tc.want, out)
			}
		})
	}
}

// leaveStaleSocket creates a socket file and stops serving it, without removing
// it — which is what a SIGKILLed process leaves behind.
//
// SetUnlinkOnClose is the whole trick: a Go unix listener unlinks its path on
// Close, so closing one normally would leave nothing to take over and the test
// would pass by testing an empty directory.
func leaveStaleSocket(t *testing.T, path string) {
	t.Helper()

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatalf("creating the stale socket: %v", err)
	}
	unixLn, ok := ln.(*net.UnixListener)
	if !ok {
		t.Fatalf("listening on a unix address returned %T, want *net.UnixListener", ln)
	}
	unixLn.SetUnlinkOnClose(false)
	if closeErr := unixLn.Close(); closeErr != nil {
		t.Fatalf("closing the stale listener: %v", closeErr)
	}

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("the stale socket was removed after all: %v", err)
	}
	if info.Mode()&fs.ModeSocket == 0 {
		t.Fatalf("%s is %s, want a socket left behind", path, info.Mode())
	}
}

// stillListening reports whether ln is what answers a connection to path.
//
// The dial alone is not enough: it succeeds against a backlog whoever owns the
// path, so a second instance that stole the socket would look identical.
// Accepting the connection is what identifies the owner.
func stillListening(t *testing.T, ln net.Listener, path string) bool {
	t.Helper()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- conn
	}()

	var d net.Dialer
	conn, err := d.DialContext(t.Context(), "unix", path)
	if err != nil {
		return false
	}
	defer conn.Close()

	server, ok := <-accepted
	if !ok {
		return false
	}
	_ = server.Close()
	return true
}

// TestUnix_CleanShutdownUnlinksTheSocket is the other end of the socket's life,
// and the reason the case above is about a crash rather than a restart.
//
// A listener that stopped unlinking its path would leave every ordinary
// redeploy looking like an unclean one: the replacement would find a socket,
// probe it, remove it and warn — working, but warning on every deploy about a
// crash that never happened, which is how a warning stops being read.
//
// The process is not started through the harness: a context cancel SIGKILLs,
// and a killed process unlinks nothing, so a case about a clean stop has to own
// its own process and signal it.
func TestUnix_CleanShutdownUnlinksTheSocket(t *testing.T) {
	path := socketPath(t)

	cmd := exec.CommandContext(context.Background(), serverBinary(t), "--http", path) //nolint:gosec // the binary this package built, with a path it created
	cmd.Env = append(os.Environ(), "LIBGEN_MCP_LOG_LEVEL=info", "LIBGEN_MCP_DOWNLOAD_DIR="+t.TempDir())
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the server: %v", err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	waitHealthy(t, &server{baseURL: "http://unix", client: unixClient(t, path), logs: func() string { return "" }})

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signaling: %v", err)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("exited with %v after SIGTERM, want a clean exit", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the server did not exit within 15s of SIGTERM")
	}

	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the socket file is still there after a clean shutdown (Lstat: %v)", err)
	}
}
