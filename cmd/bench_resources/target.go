// target.go builds and runs the thing being measured.
//
// It is the real binary, not an in-process server, and that is the whole point.
// An in-process server shares the test harness's heap, its goroutines and its
// scheduler, so its resident set is not a number an operator can give to a
// container. Everything here therefore goes through exec, and every figure is
// read from the operating system about a process this command started.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// serverPackage is the binary under measurement.
const serverPackage = "github.com/jmrplens/libgen-mcp/cmd/server"

// buildServer compiles the server into dir and returns the binary's path.
//
// CGO is off and no -ldflags are passed, which is what the release builds do
// (see CLAUDE.md on -buildmode=pie): a benchmark of a differently linked binary
// would measure a binary nobody ships.
//
// The package is named by its import path rather than as ./cmd/server so the
// build does not depend on which directory the command was started from. A
// relative path works from the module root and from nowhere else, which is a
// trap for anything that calls this from a test.
func buildServer(ctx context.Context, dir string) (string, error) {
	out := filepath.Join(dir, "libgen-mcp-bench")
	if runtimeGOOS == "windows" {
		out += ".exe"
	}
	// NOSONAR: S4036 asks whether PATH is trustworthy. The command is the Go
	// toolchain this repository is built with, on a maintainer's own shell, and
	// anybody able to put a different `go` on that PATH could have changed the
	// source this is about to compile.
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, serverPackage) // NOSONAR
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if combined, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build ./cmd/server: %w\n%s", err, combined)
	}
	return out, nil
}

// binarySize reports what the measured binary weighs on disk, zero when it
// cannot be stated.
func binarySize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// target is one running server process.
type target struct {
	cmd *exec.Cmd
	// baseURL is empty for a stdio target.
	baseURL string
	// stdin and stdout are the pipes a stdio target speaks over; nil on HTTP.
	stdin  io.WriteCloser
	stdout *bufio.Reader
	// stderr is kept so a process that dies can say why instead of leaving the
	// caller with "connection refused".
	stderr *strings.Builder
	mu     sync.Mutex
	cancel context.CancelFunc
}

// pid reports the process id, zero once it has been reaped.
func (t *target) pid() int {
	if t.cmd == nil || t.cmd.Process == nil {
		return 0
	}
	return t.cmd.Process.Pid
}

// stderrText returns whatever the process has written to stderr so far.
func (t *target) stderrText() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stderr.String()
}

// stop ends the process and waits for it, so the next scenario does not measure
// the previous one's pages.
func (t *target) stop() {
	if t.cancel != nil {
		t.cancel()
	}
	if t.stdin != nil {
		_ = t.stdin.Close()
	}
	if t.cmd != nil {
		_ = t.cmd.Wait()
	}
}

// targetOptions are what differs between one started server and another.
type targetOptions struct {
	binary string
	// mirror is the catalog stand-in every scenario points the server at.
	mirror string
	// downloadDir is where the server is told to save, which it validates at
	// startup by creating it and writing a probe file.
	downloadDir string
	// telemetry, when set, is the OTLP endpoint the server exports to.
	telemetry string
	// extraEnv is added last, so a scenario can override anything above.
	extraEnv map[string]string
	// extraArgs are appended to the command line.
	extraArgs []string
}

// baseEnv is the environment every target starts with.
//
// The environment is replaced rather than extended, for the reason the stdio
// end-to-end module gives: a developer with LIBGEN_MIRROR exported would
// otherwise decide what these numbers measure, and the difference would be
// invisible on their machine and on nobody else's.
//
// The private-address allowance is here for the stand-in catalog and nothing
// else: it listens on loopback, which internal/netguard refuses by design. The
// dead proxy pair with a loopback exemption is what makes the isolation
// structural rather than a matter of belief — a measured process can reach the
// stand-in this command started and nothing else on the internet.
func baseEnv(opts targetOptions) []string {
	env := []string{
		"LIBGEN_MIRROR=" + opts.mirror,
		"LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES=true",
		"LIBGEN_MCP_SOURCES=libgen",
		"LIBGEN_MCP_TIMEOUT=10s",
		"LIBGEN_MCP_LOG_LEVEL=warn",
		"LIBGEN_MCP_DOWNLOAD_DIR=" + opts.downloadDir,
		"HTTP_PROXY=http://127.0.0.1:1",
		"HTTPS_PROXY=http://127.0.0.1:1",
		"NO_PROXY=127.0.0.1,localhost",
		// PATH is carried because the process still has to exec nothing but
		// itself. The other four are how the standard library finds a home and
		// a cache directory, and all four are set because it reads a different
		// one per platform: os.UserHomeDir takes HOME or USERPROFILE, and
		// os.UserCacheDir takes HOME or LocalAppData. Setting only the Unix
		// pair leaves a Windows run resolving its cache to the real profile —
		// where the seeded mirror list is not, so the first search goes to the
		// internet for one, which is the one thing this command must not do.
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + opts.downloadDir,
		"USERPROFILE=" + opts.downloadDir,
		"LocalAppData=" + filepath.Join(opts.downloadDir, "AppData", "Local"),
		"AppData=" + filepath.Join(opts.downloadDir, "AppData", "Roaming"),
	}
	if opts.telemetry != "" {
		env = append(env,
			"LIBGEN_MCP_TELEMETRY=1",
			"OTEL_EXPORTER_OTLP_ENDPOINT="+opts.telemetry,
			"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		)
	}
	for k, v := range opts.extraEnv {
		env = append(env, k+"="+v)
	}
	return env
}

// mirrorCacheDir is where the measured process looks for its cached mirror
// list, given the home it was started with.
//
// All three branches are here because os.UserCacheDir has three answers, and
// seeding the wrong one does not fail: the process simply finds no cache and
// goes to the internet for a mirror list, which is the one thing this command
// must not do. Linux is $HOME/.cache, macOS is $HOME/Library/Caches, and
// Windows is %LocalAppData%, which baseEnv sets for exactly this reason.
func mirrorCacheDir(home string) string {
	switch runtimeGOOS {
	case "windows":
		return filepath.Join(home, "AppData", "Local", cacheSubdir)
	case "darwin":
		return filepath.Join(home, "Library", "Caches", cacheSubdir)
	default:
		return filepath.Join(home, ".cache", cacheSubdir)
	}
}

// cacheSubdir is the directory the server creates inside whatever
// os.UserCacheDir resolves to. It is the server's own name, so it is spelled
// once here rather than at each platform's branch.
const cacheSubdir = "libgen-mcp"

// seedMirrorCache writes a mirror list naming the stand-in catalog into the
// cache directory the measured process will read.
//
// Without it the first search discovers mirrors the way production does: by
// fetching the family's catalog page, which is on the internet. A run that did
// that would measure somebody else's site on the first call of every scenario,
// and would fail entirely on a machine with no route out — which is the shape
// this command is meant to have. A cache younger than the 24-hour TTL is
// preferred over a live fetch, so seeding one removes the call rather than
// merely making it fail.
func seedMirrorCache(home, mirrorURL string) error {
	dir := mirrorCacheDir(home)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("seed the mirror cache: %w", err)
	}
	body, err := json.Marshal(map[string]any{
		"fetched_at": time.Now().UTC(),
		"mirrors":    []string{mirrorURL},
	})
	if err != nil {
		return fmt.Errorf("seed the mirror cache: %w", err)
	}
	for _, name := range []string{"mirrors.json", "annas-mirrors.json"} {
		if wErr := os.WriteFile(filepath.Join(dir, name), body, 0o600); wErr != nil {
			return fmt.Errorf("seed %s: %w", name, wErr)
		}
	}
	return nil
}

// startHTTP starts a server listening on loopback and waits for its health
// route.
//
// The trusted-proxy pair is not decoration. This server charges a caller by the
// address its request arrives from, and every request here arrives from
// loopback, so without it every client in a concurrency series would be one
// caller and the series would measure nothing. Declaring loopback trusted lets
// the driver present a distinct X-Real-IP per client, which is exactly the
// shape the hosted deployment runs in.
func startHTTP(ctx context.Context, opts targetOptions) (*target, error) {
	port, err := freePort(ctx)
	if err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	args := append([]string{
		"--http", addr,
		"--trusted-proxies", "127.0.0.1/32",
		"--trusted-proxy-header", "X-Real-IP",
	}, opts.extraArgs...)

	t, err := spawn(ctx, opts, args)
	if err != nil {
		return nil, err
	}
	// NOSONAR: S5332 asks why this is not HTTPS. The address is a loopback port
	// this command just reserved and handed to a process it just started, so
	// there is no network between the two ends. Measuring through TLS would
	// measure TLS, which is a deployment's choice and not this server's cost.
	t.baseURL = "http://" + addr // NOSONAR
	if waitErr := waitForHealth(ctx, t); waitErr != nil {
		t.stop()
		return nil, waitErr
	}
	return t, nil
}

// startStdio starts a server speaking MCP over its own pipes.
func startStdio(ctx context.Context, opts targetOptions) (*target, error) {
	t, err := spawn(ctx, opts, opts.extraArgs)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// spawn is the shared body of both constructors.
func spawn(ctx context.Context, opts targetOptions, args []string) (*target, error) {
	if err := seedMirrorCache(opts.downloadDir, opts.mirror); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	//#nosec G204 -- the binary is one this command built and the arguments are literals above
	cmd := exec.CommandContext(runCtx, opts.binary, args...)
	cmd.Env = baseEnv(opts)

	t := &target{cmd: cmd, cancel: cancel, stderr: &strings.Builder{}}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if t.stdin, err = cmd.StdinPipe(); err != nil {
		cancel()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	t.stdout = bufio.NewReaderSize(stdout, 1<<20)

	if startErr := cmd.Start(); startErr != nil {
		cancel()
		return nil, fmt.Errorf("start %s: %w", opts.binary, startErr)
	}
	go drainInto(t, stderr)
	return t, nil
}

// drainInto copies the process's stderr where stderrText can read it. It runs
// for the life of the process: a reader that stopped would eventually block the
// process on a full pipe, which would be measured as latency.
func drainInto(t *target, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		t.mu.Lock()
		t.stderr.WriteString(sc.Text())
		t.stderr.WriteByte('\n')
		t.mu.Unlock()
	}
}

// freePort reserves an ephemeral port and releases it, which is how a server
// started with an address of its own gets one nothing else holds.
func freePort(ctx context.Context) (int, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserving a port: %w", err)
	}
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		_ = l.Close()
		return 0, fmt.Errorf("reserved %s, which is not a TCP address", l.Addr())
	}
	if closeErr := l.Close(); closeErr != nil {
		return 0, fmt.Errorf("releasing the reserved port: %w", closeErr)
	}
	return addr.Port, nil
}

// healthTimeout bounds how long a target is given to answer its health route
// before the run gives up and says what the process wrote instead.
const healthTimeout = 30 * time.Second

// waitForHealth polls /health until the server answers or the deadline passes.
//
// The poll is what makes ProcessReadyMs a measurement rather than a guess, so
// the interval is short: a coarse one would round every startup up to itself.
func waitForHealth(ctx context.Context, t *target) error {
	deadline := time.Now().Add(healthTimeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/health", http.NoBody)
		if err != nil {
			return fmt.Errorf("health request: %w", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	return fmt.Errorf("server did not answer %s/health within %s:\n%s", t.baseURL, healthTimeout, t.stderrText())
}

// serverInfo asks the running binary what it is, rather than assuming.
//
// A benchmark records the build it measured, and the build is the binary's to
// state: reading VERSION here would record what the tree says while the binary
// under measurement could have been built from anything.
func serverInfo(ctx context.Context, t *target) (ServerInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/health", http.NoBody)
	if err != nil {
		return ServerInfo{}, fmt.Errorf("health request: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ServerInfo{}, fmt.Errorf("read /health: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return ServerInfo{}, fmt.Errorf("read /health body: %w", err)
	}
	return parseServerInfo(body)
}

// parseServerInfo pulls the version and commit out of a /health document.
func parseServerInfo(body []byte) (ServerInfo, error) {
	var doc struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return ServerInfo{}, fmt.Errorf("decode /health: %w", err)
	}
	if doc.Version == "" {
		return ServerInfo{}, errors.New("/health names no version")
	}
	return ServerInfo{Version: doc.Version, Commit: doc.Commit}, nil
}
