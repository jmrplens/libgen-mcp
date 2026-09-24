//go:build stdioe2e

// harness_test.go starts the real server binary over stdio and drives it the
// way a client does.
//
// The binary is built once and started per test with its own environment,
// because stdio configuration is environment-driven and most of what is under
// test is configuration-dependent. The environment is REPLACED rather than
// extended: a developer with LIBGEN_MIRROR exported would otherwise decide what
// these tests measure, and the failure would be invisible on their machine and
// on nobody else's.
//
// The harness is deliberately a copy of the one under test/e2e/http rather than
// a shared package. Each module drives the binary through a different mouth — a
// socket there, two pipes here — and the shapes that matter are the ones that
// differ, so a helper abstract enough to serve both would hide exactly what
// each is for.

package stdioe2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// protocolVersion is the revision these tests speak. The legacy-era one on
// purpose, as in test/e2e/http: it is what a plain request negotiates without
// the per-request _meta a 2026-07-28 client carries, so a test that is not
// about version negotiation does not have to.
const protocolVersion = "2025-11-25"

// The surface this server registers on a local stdio deployment. Stated as
// numbers because the startup-catalog assertion is about completeness: a
// catalog half-built when it was asked for is the failure, and "some tools came
// back" cannot see it.
const (
	registeredTools   = 4
	registeredPrompts = 4
)

// serverStopGrace is how long a session's cleanup lets the server leave on its
// own before the context is canceled, which kills it. Bounded rather than
// unbounded because a server that will not stop is a defect this module is
// meant to report, not one it should hang on.
const serverStopGrace = 10 * time.Second

var (
	buildOnce   sync.Once
	builtBinary string
	// builtDir is recorded separately from builtBinary because a failed build
	// leaves the directory created and the binary path empty — the one case
	// where cleanup matters most, since the run is about to end.
	builtDir string
	errBuild error
)

// binaryEnv names a server already built, to drive instead of building one.
const binaryEnv = "E2E_SERVER_BINARY"

// serverBinary returns the path of the server these tests drive: the one
// E2E_SERVER_BINARY names, or one built once for the whole package.
//
// Building rather than importing is the point of the module: what is under test
// is the process — its pipes, its streams, its exit — and a test that imported
// package main would be testing its own assembly of it instead.
//
// A path that names nothing is refused rather than built around. Falling back
// to a compile would answer a typo by silently driving a different binary from
// the one whoever ran the tests staged.
func serverBinary(t *testing.T) string {
	t.Helper()
	if prebuilt := os.Getenv(binaryEnv); prebuilt != "" {
		if refusal := prebuiltBinaryRefusal(); refusal != "" {
			t.Fatalf("%s names %s, which cannot be used: %s", binaryEnv, prebuilt, refusal)
		}
		staged, err := stagedBinary(prebuilt)
		if err != nil {
			t.Fatalf("%s names %s, which cannot be used: %v", binaryEnv, prebuilt, err)
		}
		return staged
	}
	bin, err := buildServerBinary()
	if err != nil {
		t.Fatalf("building the server binary: %v", err)
	}
	return bin
}

// stagedBinary resolves what E2E_SERVER_BINARY names to an absolute path this
// harness can execute, or says why it cannot.
//
// Absolute, because the path is executed rather than only read, and a relative
// one names two different files: os.Stat resolves it against this package's
// directory while exec resolves it against the process's working directory.
// Regular and executable, because a stat alone accepts a directory and a file
// nothing can run — and "fork/exec …: permission denied" out of cmd.Start names
// neither the variable nor what is wrong with what it names.
func stagedBinary(prebuilt string) (string, error) {
	abs, err := filepath.Abs(prebuilt)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs) //#nosec G703 -- the binary the person running the suite chose to stage, named by them in its own variable
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("it is not a regular file (mode %s)", info.Mode())
	}
	// Windows has no executable bit — os.Stat reports 0666 or 0444 there, from
	// the read-only attribute — so this half would refuse every staged binary
	// on that platform rather than the ones that cannot run.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("it is not executable (mode %s)", info.Mode())
	}
	return abs, nil
}

// buildServerBinary builds cmd/server once for the whole package and returns
// its path.
//
// It takes no testing.T, and the build directory is not a t.TempDir, for one
// reason: the build is shared by every test in the package, so the first test
// to arrive would own a directory removed when that test ended, leaving every
// later test pointing at nothing. TestMain removes it instead.
func buildServerBinary() (string, error) {
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "libgen-mcp-stdioe2e")
		if err != nil {
			errBuild = err
			return
		}
		builtDir = dir
		out := filepath.Join(dir, "libgen-mcp")
		if runtime.GOOS == "windows" {
			// exec refuses a file with no executable extension there, and
			// go build -o writes exactly the name it is given.
			out += ".exe"
		}
		// The arguments and the bound come from the race seam, so a
		// `go test -race` run builds an instrumented server rather than driving
		// an uninstrumented one (harness_race_test.go).
		ctx, cancel := context.WithTimeout(context.Background(), serverBuildTimeout)
		defer cancel()
		//nolint:gosec // "go" plus arguments this package composes from a path it created; nothing here comes from outside the test binary.
		cmd := exec.CommandContext(ctx, "go", serverBuildArgs(out)...)
		cmd.Dir = repoRoot()
		if output, runErr := cmd.CombinedOutput(); runErr != nil {
			errBuild = fmt.Errorf("building cmd/server: %w\n%s", runErr, output)
			return
		}
		builtBinary = out
	})
	return builtBinary, errBuild
}

// repoRoot walks up to the directory holding go.mod.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// session is a running server process and the two pipes a client talks to it
// through.
type session struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	// exited is closed by the one goroutine that reaps the process. Everything
	// asking whether the server is still there reads it rather than waiting
	// again: a second Wait on a reaped child answers ErrProcessDone, which
	// cannot be told from a real failure.
	exited chan struct{}
	// state is what the reaper found, for the callers that report an exit code.
	state atomic.Pointer[os.ProcessState]
	// expectedExit is set by [session.waitExit], which every helper that asks
	// the server to stop goes through. Without it the cleanup cannot tell an
	// exit a test was about from one the server took by itself.
	expectedExit atomic.Bool

	mu     sync.Mutex
	stderr strings.Builder
	// notifications holds every notification read past while waiting for a
	// response, in arrival order.
	notifications []map[string]any
}

// startSession launches the binary with the given environment and returns a
// live session.
func startSession(t *testing.T, env map[string]string) *session {
	t.Helper()
	return startSessionWithArgs(t, env)
}

// startSessionWithArgs is [startSession] for a case that needs the binary to be
// given flags. stdio configuration is environment-driven, so almost nothing
// here passes arguments.
func startSessionWithArgs(t *testing.T, env map[string]string, args ...string) *session {
	t.Helper()
	return startSessionIn(t, "", env, args...)
}

// startSessionInDir is [startSession] with the process's working directory
// chosen by the caller.
//
// It exists because the working directory is untrusted input on stdio: an MCP
// client sets it to whatever workspace it has open, and neither Claude Desktop
// (which uses "/") nor a client that uses the user's home asks first. Whether
// the server reads anything out of it is a property of the process and can only
// be tested by choosing one.
func startSessionInDir(t *testing.T, dir string, env map[string]string) *session {
	t.Helper()
	return startSessionIn(t, dir, env)
}

// startSessionIn is what the three wrappers above share. An empty dir keeps the
// Go default, which is this package's own directory, so a caller that does not
// care about the working directory is unaffected by one that does.
func startSessionIn(t *testing.T, dir string, env map[string]string, args ...string) *session {
	t.Helper()

	bin := serverBinary(t)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, args...)
	prepareForTermination(cmd)
	cmd.Dir = dir

	// Built from nothing, so an exported LIBGEN_MIRROR or LIBGEN_MCP_SOURCES on
	// the machine running the tests cannot decide what they measure. PATH is
	// kept because the process needs to be executable; HOME because the server
	// resolves its cache and its default download directory through it.
	// A case that chooses the server's home (the containment cases do, since
	// the home directory is what they are about) wins here, and the cache is
	// seeded into the home the server will actually resolve — seeding the other
	// one would leave the session fetching the live catalog page.
	home := t.TempDir()
	if chosen := env["HOME"]; chosen != "" {
		home = chosen
	}
	seedMirrorCache(t, home, env["LIBGEN_MIRROR"])
	environ := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	// Before the caller's own entries, so a test that needs to say something
	// else about GORACE still can.
	environ = append(environ, raceEnviron()...)
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	// os.UserHomeDir reads HOME on Unix and USERPROFILE on Windows, and
	// os.UserCacheDir reads LocalAppData there; the mirror cache and the
	// download directory both hang off those answers. Without this a Windows
	// run configures a home the server never looks at, and fails to start for
	// want of a cache directory.
	environ = withPlatformHome(environ)
	cmd.Env = environ

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatalf("stdout pipe: %v", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		t.Fatalf("stderr pipe: %v", err)
	}
	if startErr := cmd.Start(); startErr != nil {
		cancel()
		t.Fatalf("starting the server: %v", startErr)
	}

	s := &session{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), exited: make(chan struct{})}

	// The one reaper. Started with the process rather than on demand, because
	// alive() has to answer before anybody has asked the process to stop, and a
	// check that has to wait first cannot answer that question.
	go func() {
		state, _ := cmd.Process.Wait()
		s.state.Store(state)
		close(s.exited)
	}()

	// stderr is drained on its own goroutine: a full pipe would block the
	// server, and the contents are an assertion of their own — logs belong here
	// and nowhere near stdout.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := stderrPipe.Read(buf)
			if n > 0 {
				s.mu.Lock()
				s.stderr.Write(buf[:n])
				s.mu.Unlock()
			}
			if readErr != nil {
				return
			}
		}
	}()

	t.Cleanup(func() { s.stop(t, cancel) })
	return s
}

// stop is the session cleanup: it reports an exit no test asked for, then asks
// the server to leave the way a client does and kills it only if it will not.
func (s *session) stop(t *testing.T, cancel context.CancelFunc) {
	t.Helper()
	// Asked before stdin is closed, because closing it is how this harness asks
	// the server to stop and an exit after that says nothing. A process already
	// gone at this point went on its own, which under GORACE=halt_on_error=1 is
	// what a race report looks like: it lands in the captured stderr of a test
	// that made its last assertion and passed, and nothing else would mention it.
	if !s.expectedExit.Load() {
		select {
		case <-s.exited:
			t.Errorf("the server exited on its own during the test (%s), which no test here asks it to do\nstderr: %s",
				s.exitStatus(), s.stderrText())
		default:
		}
	}
	_ = s.stdin.Close()
	select {
	case <-s.exited:
	case <-time.After(serverStopGrace):
	}
	cancel()
	// Not Cmd.Wait: the reaper already collected the child, so this would
	// answer ErrProcessDone and could not be told from a real failure. Waiting
	// on the reaper is the same barrier without the ambiguity.
	select {
	case <-s.exited:
	case <-time.After(serverStopGrace):
	}
}

// send writes one JSON-RPC message.
func (s *session) send(t *testing.T, msg string) {
	t.Helper()
	if _, err := io.WriteString(s.stdin, msg+"\n"); err != nil {
		// A broken pipe here means the process is gone. Saying so is worth the
		// line: the raw error names the syscall and not the cause.
		t.Fatalf("the server is no longer running (%v)\nstderr: %s", err, s.stderrText())
	}
}

// readMessage returns the next line the server writes to stdout, decoded.
//
// It fails rather than blocking forever when the server says nothing, since a
// server that has died mid-handshake is the exact failure this module exists to
// catch and a hung test reports it as a timeout with no detail.
func (s *session) readMessage(t *testing.T, within time.Duration) map[string]any {
	t.Helper()

	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := s.stdout.ReadString('\n')
		ch <- result{line, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil && r.line == "" {
			t.Fatalf("the server closed stdout without answering: %v\nstderr: %s", r.err, s.stderrText())
		}
		var decoded map[string]any
		if unmarshalErr := json.Unmarshal([]byte(r.line), &decoded); unmarshalErr != nil {
			// The most useful failure this module can produce: something that
			// is not JSON-RPC on stdout breaks every client, whatever it is.
			t.Fatalf("stdout carried a line that is not JSON: %q\nstderr: %s", r.line, s.stderrText())
		}
		return decoded
	case <-time.After(within):
		t.Fatalf("the server did not answer within %s\nstderr: %s", within, s.stderrText())
		return nil
	}
}

// call sends a request and returns the response carrying its id.
//
// Matching on the id is what makes this a call rather than "the next line". A
// server that speaks unprompted — a notification, a progress report — would
// otherwise leave every later assertion one message behind, running against
// something that has no error and so never fails, while the real responses pile
// up unread in a pipe that eventually blocks the server's write.
func (s *session) call(t *testing.T, msg string) map[string]any {
	t.Helper()

	want := requestIDOf(t, msg)
	s.send(t, msg)
	for {
		got := s.readMessage(t, 30*time.Second)
		if _, isCall := got["method"]; isCall && got["id"] == nil {
			s.mu.Lock()
			s.notifications = append(s.notifications, got)
			s.mu.Unlock()
			continue
		}
		if !sameID(got["id"], want) {
			s.mu.Lock()
			passed := len(s.notifications)
			s.mu.Unlock()
			t.Fatalf("waiting for the response to id %v, read a message for id %v after passing %d notification(s): %v\nstderr: %s",
				want, got["id"], passed, got, s.stderrText())
		}
		return got
	}
}

// requestIDOf reads the id out of an outgoing request, failing on a message
// that has none: a notification is sent with send, not call.
func requestIDOf(t *testing.T, msg string) any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(msg), &decoded); err != nil {
		t.Fatalf("call was given a message that is not JSON: %q: %v", msg, err)
	}
	id, ok := decoded["id"]
	if !ok || id == nil {
		t.Fatalf("call was given a message without an id; use send for a notification: %q", msg)
	}
	return id
}

// sameID compares two JSON-RPC ids as the decoder produced them, a number or a
// string, without caring which spelling each side used.
func sameID(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

// alive reports whether the server process is still running.
func (s *session) alive() bool {
	select {
	case <-s.exited:
		return false
	default:
		return true
	}
}

// exitStatus describes how the process ended, in a form that makes sense
// whether or not the reaper had recorded it by the time it was asked.
func (s *session) exitStatus() string {
	if state := s.state.Load(); state != nil {
		return state.String()
	}
	return "exit status not recorded"
}

// stderrText returns everything the server has logged so far.
func (s *session) stderrText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stderr.String()
}

// waitForStderr returns the server's stderr once it contains needle, and fails
// the test if it does not within the window.
//
// stderr is copied into the buffer by a goroutine of this harness, so a line
// the server wrote before answering on stdout can still be in the pipe when the
// test reads the buffer: the stdout reply and the stderr copy are two
// goroutines with no ordering between them. Waiting for the line the assertion
// is about removes the race without loosening the assertion.
func (s *session) waitForStderr(t *testing.T, needle string, within time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		text := s.stderrText()
		if strings.Contains(text, needle) {
			return text
		}
		if time.Now().After(deadline) {
			t.Fatalf("stderr did not carry %q within %s\nstderr: %s", needle, within, text)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// terminate asks the server to shut down the way a supervisor does and reports
// whether it went.
//
// Shutdown is a contract: a client that stops its server expects the process to
// go away, and a supervisor that waits on it expects the same. A server that has
// to be killed is one that may not have finished what it was doing.
func (s *session) terminate(t *testing.T, within time.Duration) (code int, exited bool) {
	t.Helper()
	if err := signalTermination(s.cmd.Process); err != nil {
		t.Fatalf("sending %s: %v", terminationSignalName, err)
	}
	return s.waitExit(t, within)
}

// closeStdinAndWait closes the client's end of stdin and returns the exit
// status. This is the shutdown a client that simply goes away produces.
func (s *session) closeStdinAndWait(t *testing.T, within time.Duration) (code int, exited bool) {
	t.Helper()
	if err := s.stdin.Close(); err != nil {
		t.Fatalf("closing stdin: %v", err)
	}
	return s.waitExit(t, within)
}

// waitExit reaps the process and returns its exit code.
//
// It reads the status the reaper recorded rather than calling Cmd.Wait, which
// closes the stdout and stderr pipes once it sees the process exit: a read
// still in flight on either would fail rather than return what it had.
func (s *session) waitExit(t *testing.T, within time.Duration) (code int, exited bool) {
	t.Helper()

	// Every helper that asks the server to stop arrives here, so this is the
	// one place that records that the exit about to happen is the test's own
	// doing and not a finding.
	s.expectedExit.Store(true)

	select {
	case <-s.exited:
		state := s.state.Load()
		if state == nil {
			t.Fatalf("the process could not be reaped\nstderr: %s", s.stderrText())
		}
		return state.ExitCode(), true
	case <-time.After(within):
		return 0, false
	}
}

// request builds a plain JSON-RPC request. This server negotiates the
// legacy-era protocol, so there is no per-request _meta to carry.
func request(id int, method, params string) string {
	if params == "" {
		params = "{}"
	}
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params)
}

// initializeRequest is the handshake exactly as a client sends it.
func initializeRequest(id int) string {
	return request(id, "initialize",
		`{"protocolVersion":"`+protocolVersion+`","capabilities":{},"clientInfo":{"name":"stdio-e2e","version":"1"}}`)
}

// mirror is a stand-in libgen mirror the server can actually reach.
//
// Every tool call this module makes reaches an upstream, and pointing
// LIBGEN_MIRROR at a domain that does not resolve exercises the failure path
// rather than the transport. What is under test here is the pipe, so the
// upstream has to answer.
type mirror struct {
	url string
	// reached is closed by the first request to arrive, so a test can wait for
	// a call to be genuinely in flight rather than sleeping and hoping.
	reached chan struct{}
	// requests counts everything that arrived, for the cases whose claim is that
	// a host was NOT contacted. "The other one answered" is a weaker assertion
	// than "this one was never asked", and only the second one rules out a
	// server that tried both.
	requests atomic.Int64
}

// requestCount reports how many requests reached this mirror.
func (m *mirror) requestCount() int64 { return m.requests.Load() }

// startMirror serves a catalog page, which is enough for a search to come back
// with something rather than with a network error.
func startMirror(t *testing.T) *mirror {
	t.Helper()
	return startMirrorWith(t, nil)
}

// startMirrorWith is [startMirror] with a handler of the test's own, for a case
// about what happens while a request is outstanding.
//
// A nil hold answers immediately. A non-nil one is waited on before the
// response is written, which is how a case produces a tool call that is still
// running when the client goes away — the everyday shape of a client exiting,
// not an exotic one.
func startMirrorWith(t *testing.T, hold <-chan struct{}) *mirror {
	t.Helper()
	m := &mirror{reached: make(chan struct{})}
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.requests.Add(1)
		once.Do(func() { close(m.reached) })
		if hold != nil {
			select {
			case <-hold:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><body><table id="tablelibgen"><tbody></tbody></table></body></html>`)
	}))
	t.Cleanup(srv.Close)
	m.url = srv.URL
	return m
}

// awaitInFlightCall waits until a request has reached the mirror.
//
// Waiting for the request rather than sleeping is what keeps a case about "a
// call in flight" from quietly becoming the idle case: a fixed delay asserts on
// the scheduler, and if the call had not left the server yet the case would
// pass for the opposite of the reason it was written.
func (m *mirror) awaitInFlightCall(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case <-m.reached:
	case <-time.After(within):
		t.Fatalf("no request reached the mirror within %s, so nothing was in flight", within)
	}
}

// seedMirrorCache writes a fresh mirror cache into the session's home so the
// server never fetches the live catalog page.
//
// Mirror discovery is a real request to shadowlibraries.github.io on the first
// call that needs a mirror, and it succeeds on a developer's machine and on a
// CI runner, which is precisely why leaving it would be a mistake: the module
// would pass every day and report that site's outages as this server's
// regressions, and nothing in a green run would say a third party had been
// asked. A cache younger than the 24-hour TTL is preferred over a live fetch,
// so seeding one removes the call rather than merely making it fail.
//
// Both families are written. The libgen one is what the search path reads; the
// Anna's one is read by a source this module does not exercise today, and
// writing it costs a line and means a test that does exercise it later does not
// quietly acquire a network dependency.
func seedMirrorCache(t *testing.T, home, mirrorURL string) {
	t.Helper()
	if mirrorURL == "" {
		return
	}
	dir := filepath.Join(home, ".cache", "libgen-mcp")
	if runtime.GOOS == "windows" {
		dir = filepath.Join(home, "AppData", "Local", "libgen-mcp")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("seeding the mirror cache: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"fetched_at": time.Now().UTC(),
		"mirrors":    []string{mirrorURL},
	})
	if err != nil {
		t.Fatalf("seeding the mirror cache: %v", err)
	}
	for _, name := range []string{"mirrors.json", "annas-mirrors.json"} {
		if writeErr := os.WriteFile(filepath.Join(dir, name), body, 0o600); writeErr != nil {
			t.Fatalf("seeding %s: %v", name, writeErr)
		}
	}
}

// baseEnv is the environment every session starts with.
//
// The private-address allowance is here for the fixture and nowhere else: the
// mirror above listens on loopback, which internal/netguard refuses by design.
// It is right in production and exactly wrong for a fake on 127.0.0.1.
//
// The three proxy variables make this module's isolation structural rather than
// a matter of belief. Every outbound request goes through the cloned
// http.DefaultTransport, which reads them, so a dead proxy plus a loopback
// exemption means a session can reach the fixtures this harness starts and
// nothing else. It costs an ordinary run nothing — the seeded cache and
// extra_sources=never already keep every path local — and it is what stops a
// test added later from quietly acquiring a dependency on a third party's
// uptime, which is the reason the live suite under test/e2e is kept out of CI.
func baseEnv(t *testing.T, m *mirror) map[string]string {
	t.Helper()
	return map[string]string{
		"LIBGEN_MIRROR":                      m.url,
		"LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES": "true",
		"LIBGEN_MCP_SOURCES":                 "libgen",
		"LIBGEN_MCP_TIMEOUT":                 "5s",
		"LIBGEN_MCP_LOG_LEVEL":               "info",
		"LIBGEN_MCP_DOWNLOAD_DIR":            t.TempDir(),
		"HTTP_PROXY":                         "http://127.0.0.1:1",
		"HTTPS_PROXY":                        "http://127.0.0.1:1",
		"NO_PROXY":                           "127.0.0.1,localhost",
	}
}

// TestMain removes the binary this package builds once its tests are done.
//
// serverBinary deliberately cannot use t.TempDir: the binary is built under
// sync.Once for the whole package, and the first test to arrive would own a
// directory removed when that test ends. TestMain is the scope that matches —
// it outlives every test and runs after all of them — so opting out of t.TempDir
// is not a reason to leak a binary per run onto a machine that runs this suite
// all day. The exit code is preserved, so a failing suite still fails.
func TestMain(m *testing.M) {
	code := m.Run()
	removeBuiltBinary()
	os.Exit(code)
}

// removeBuiltBinary deletes the temporary directory serverBinary created, if it
// created one.
//
// Keyed on the directory rather than the binary so a build that failed is
// cleaned up too: MkdirTemp succeeds before the compile does, so the failing
// case leaves a directory and no binary. Failure is ignored: this runs after
// the tests have reported, so there is nobody left to tell.
func removeBuiltBinary() {
	if builtDir == "" {
		return
	}
	_ = os.RemoveAll(builtDir)
}
