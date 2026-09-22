package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBaseEnv_ReplacesTheEnvironmentRatherThanExtendingIt verifies the
// isolation this command's numbers rest on.
//
// A developer with LIBGEN_MIRROR exported would otherwise decide what is being
// measured, and the difference would be invisible on their machine and on
// nobody else's. The dead proxy pair with a loopback exemption is the other
// half: a measured process can reach the stand-in this command started and
// nothing else on the internet.
func TestBaseEnv_ReplacesTheEnvironmentRatherThanExtendingIt(t *testing.T) {
	t.Setenv("LIBGEN_MIRROR", "https://a-developers-mirror.invalid")
	t.Setenv("LIBGEN_MCP_SOURCES", "everything")

	env := baseEnv(targetOptions{mirror: "http://127.0.0.1:9", downloadDir: "/tmp/x"})
	joined := strings.Join(env, "\n")

	for _, want := range []string{
		"LIBGEN_MIRROR=http://127.0.0.1:9",
		"LIBGEN_MCP_SOURCES=libgen",
		"LIBGEN_MCP_ALLOW_PRIVATE_ADDRESSES=true",
		"HTTP_PROXY=http://127.0.0.1:1",
		"NO_PROXY=127.0.0.1,localhost",
	} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(joined, want) {
				t.Errorf("baseEnv() does not carry %q:\n%s", want, joined)
			}
		})
	}
	if strings.Contains(joined, "a-developers-mirror") {
		t.Errorf("baseEnv() carried the ambient mirror into the measurement:\n%s", joined)
	}
	if strings.Contains(joined, "everything") {
		t.Errorf("baseEnv() carried an ambient source list into the measurement:\n%s", joined)
	}
}

// TestBaseEnv_AddsTelemetryOnlyWhenAsked verifies the exporter is configured for
// the scenario that measures it and for no other, since the whole point of that
// scenario is the difference.
func TestBaseEnv_AddsTelemetryOnlyWhenAsked(t *testing.T) {
	without := strings.Join(baseEnv(targetOptions{mirror: "m", downloadDir: "d"}), "\n")
	if strings.Contains(without, "OTEL_EXPORTER_OTLP_ENDPOINT") {
		t.Error("a scenario that did not ask for telemetry got an exporter")
	}

	with := strings.Join(baseEnv(targetOptions{mirror: "m", downloadDir: "d", telemetry: "http://127.0.0.1:4318"}), "\n")
	for _, want := range []string{"LIBGEN_MCP_TELEMETRY=1", "OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318"} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(with, want) {
				t.Errorf("baseEnv() does not carry %q", want)
			}
		})
	}
}

// TestBaseEnv_LetsAScenarioOverrideWhatItNeedsTo verifies the extra environment
// is appended last, which is what makes a per-scenario knob possible at all.
func TestBaseEnv_LetsAScenarioOverrideWhatItNeedsTo(t *testing.T) {
	env := baseEnv(targetOptions{
		mirror:      "m",
		downloadDir: "d",
		extraEnv:    map[string]string{"LIBGEN_MCP_RATE_RPS": "20"},
	})
	last := map[string]string{}
	for _, entry := range env {
		if name, value, found := strings.Cut(entry, "="); found {
			last[name] = value
		}
	}
	if last["LIBGEN_MCP_RATE_RPS"] != "20" {
		t.Errorf("the scenario's rate did not win: %v", last["LIBGEN_MCP_RATE_RPS"])
	}
}

// TestSeedMirrorCache_RemovesTheCallRatherThanMakingItFail verifies the cache
// the measured process reads instead of discovering mirrors over the internet.
//
// Without it the first search of every scenario fetches the family's catalog
// page, which would measure somebody else's site and would fail entirely on a
// machine with no route out — which is the shape this command is meant to have.
func TestSeedMirrorCache_RemovesTheCallRatherThanMakingItFail(t *testing.T) {
	home := t.TempDir()
	if err := seedMirrorCache(home, "http://127.0.0.1:9"); err != nil {
		t.Fatalf("seedMirrorCache: %v", err)
	}

	for _, name := range []string{"mirrors.json", "annas-mirrors.json"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(mirrorCacheDir(home), name)
			body, err := os.ReadFile(path) // #nosec G304 -- a path this test just wrote
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			var doc struct {
				Mirrors []string `json:"mirrors"`
			}
			if uErr := json.Unmarshal(body, &doc); uErr != nil {
				t.Fatalf("decode %s: %v", name, uErr)
			}
			if len(doc.Mirrors) != 1 || doc.Mirrors[0] != "http://127.0.0.1:9" {
				t.Errorf("%s names %v, want only the stand-in", name, doc.Mirrors)
			}
		})
	}

	t.Run("a home it cannot write", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "a-file")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		if err := seedMirrorCache(file, "http://127.0.0.1:9"); err == nil {
			t.Error("expected an error seeding into a path that is a file")
		}
	})
}

// TestMirrorCacheDir_FollowsTheStandardLibrarysRulePerPlatform verifies the
// seeded cache lands where the measured process will look for it.
//
// os.UserCacheDir answers $HOME/.cache on Linux and %LocalAppData% on Windows,
// and the first version of this seeded the Linux path unconditionally — which
// passed on two platforms and failed on the third with a file the process would
// never have read anyway.
func TestMirrorCacheDir_FollowsTheStandardLibrarysRulePerPlatform(t *testing.T) {
	original := runtimeGOOS
	t.Cleanup(func() { runtimeGOOS = original })

	testCases := []struct {
		name, goos, want string
	}{
		{name: "linux", goos: "linux", want: filepath.Join("home", ".cache", "libgen-mcp")},
		{name: "darwin", goos: "darwin", want: filepath.Join("home", "Library", "Caches", "libgen-mcp")},
		{name: "windows", goos: "windows", want: filepath.Join("home", "AppData", "Local", "libgen-mcp")},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			runtimeGOOS = tc.goos
			if got := mirrorCacheDir("home"); got != tc.want {
				t.Errorf("mirrorCacheDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParseServerInfo_TakesTheBuildFromTheBinary verifies the record names the
// build the binary states rather than the one the tree says, and refuses a
// document that names none.
func TestParseServerInfo_TakesTheBuildFromTheBinary(t *testing.T) {
	testCases := []struct {
		name, body  string
		wantVersion string
		wantErr     bool
	}{
		{
			name:        "a health document",
			body:        `{"status":"ok","version":"1.7.3","commit":"abc1234"}`,
			wantVersion: "1.7.3",
		},
		{name: "no version", body: `{"status":"ok"}`, wantErr: true},
		{name: "not a document", body: "ok", wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseServerInfo([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseServerInfo: %v", err)
			}
			if got.Version != tc.wantVersion {
				t.Errorf("Version = %q, want %q", got.Version, tc.wantVersion)
			}
		})
	}
}

// TestServerInfo_ReadsTheRunningBinarysHealth verifies the whole path, against a
// stand-in for the route.
func TestServerInfo_ReadsTheRunningBinarysHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok","version":"9.9.9","commit":"deadbee"}`)
	}))
	t.Cleanup(srv.Close)

	got, err := serverInfo(t.Context(), &target{baseURL: srv.URL})
	if err != nil {
		t.Fatalf("serverInfo: %v", err)
	}
	if got.Version != "9.9.9" || got.Commit != "deadbee" {
		t.Errorf("serverInfo() = %+v, want the health document's build", got)
	}

	t.Run("a server that is not there", func(t *testing.T) {
		gone := httptest.NewServer(http.NotFoundHandler())
		url := gone.URL
		gone.Close()
		if _, err = serverInfo(t.Context(), &target{baseURL: url}); err == nil {
			t.Error("expected an error against a closed server")
		}
	})
}

// TestWaitForHealth_AnswersOrSaysWhatTheProcessWrote verifies a target that
// never comes up fails with the process's own words rather than with a bare
// timeout, which is the difference between a diagnosis and a shrug.
func TestWaitForHealth_AnswersOrSaysWhatTheProcessWrote(t *testing.T) {
	t.Run("a server that answers", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)
		if err := waitForHealth(t.Context(), &target{baseURL: srv.URL, stderr: &strings.Builder{}}); err != nil {
			t.Errorf("waitForHealth: %v", err)
		}
	})

	t.Run("a context that is already done", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		t.Cleanup(srv.Close)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := waitForHealth(ctx, &target{baseURL: srv.URL, stderr: &strings.Builder{}}); err == nil {
			t.Error("expected the context error")
		}
	})
}

// TestTargetAccessors_AnswerBeforeAndAfterAProcess verifies the reads a runner
// makes about a target that has not started, or has already gone.
func TestTargetAccessors_AnswerBeforeAndAfterAProcess(t *testing.T) {
	var empty target
	if got := empty.pid(); got != 0 {
		t.Errorf("pid() = %d on a target with no process, want 0", got)
	}
	empty.stderr = &strings.Builder{}
	if got := empty.stderrText(); got != "" {
		t.Errorf("stderrText() = %q, want empty", got)
	}
	empty.stop()
}

// TestBinarySize_AnswersZeroForAPathThatIsNotThere verifies the record carries a
// size or nothing, rather than a failure.
func TestBinarySize_AnswersZeroForAPathThatIsNotThere(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if got := binarySize(path); got != 10 {
		t.Errorf("binarySize() = %d, want 10", got)
	}
	if got := binarySize(filepath.Join(t.TempDir(), "absent")); got != 0 {
		t.Errorf("binarySize() = %d for a path that is not there, want 0", got)
	}
}

// TestFreePort_ReservesSomethingNothingElseHolds verifies the port a target is
// started on can actually be bound.
func TestFreePort_ReservesSomethingNothingElseHolds(t *testing.T) {
	port, err := freePort(t.Context())
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	if port <= 0 || port > 65535 {
		t.Errorf("freePort() = %d", port)
	}
}

// fakeServerEnv is the switch that turns the test binary into a stand-in for
// the server, so target.go's exec, pipe and readiness paths are exercised
// against a real process rather than against a mock of one.
const fakeServerEnv = "LIBGEN_MCP_BENCH_FAKE_SERVER"

// runFakeServer is the stand-in process. It speaks just enough of each
// transport for the helpers under test to do their whole job: answer a health
// route, hand back a build, and reply to a JSON-RPC frame.
func runFakeServer(mode string, args []string) int {
	fmt.Fprintln(os.Stderr, "fake server starting")
	switch mode {
	case transportHTTP:
		return runFakeHTTP(args)
	case transportStdio:
		return runFakeStdio()
	default:
		fmt.Fprintf(os.Stderr, "fake server: unknown mode %q\n", mode)
		return 2
	}
}

// runFakeHTTP serves the health route and one JSON-RPC endpoint on the address
// the --http flag names, until it is killed.
func runFakeHTTP(args []string) int {
	addr := flagValue(args, "--http")
	if addr == "" {
		fmt.Fprintln(os.Stderr, "fake server: no --http address")
		return 2
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok","version":"0.0.0-fake","commit":"fake"}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)
	})
	// The profiling listener, when the series asked for one. It answers the
	// text heap profile the settled reading parses, so the whole series path
	// including the forced collection is exercised against a real process.
	if pprofAddr := flagValue(args, "--pprof-addr"); pprofAddr != "" {
		go serveFakePprof(pprofAddr)
	}
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "fake server: %v\n", err)
	}
	return 0
}

// serveFakePprof answers the one profiling route the series reads.
func serveFakePprof(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/heap", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "heap profile: 1: 2 [3: 4] @ heap/1048576\n\n# HeapAlloc = 4194304\n# Sys = 8388608\n")
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "fake profiler: %v\n", err)
	}
}

// runFakeStdio answers every framed request on stdin with an empty result.
func runFakeStdio() int {
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0
		}
		if !strings.Contains(line, `"id"`) {
			continue
		}
		fmt.Println(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)
	}
}

// flagValue reads the value that follows a flag in an argument list.
func flagValue(args []string, name string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// fakeTargetOptions points a target helper at the test binary.
func fakeTargetOptions(t *testing.T, mode string) targetOptions {
	t.Helper()
	return targetOptions{
		binary:      os.Args[0],
		mirror:      "http://127.0.0.1:9",
		downloadDir: t.TempDir(),
		extraEnv:    map[string]string{fakeServerEnv: mode},
	}
}

// TestStartHTTP_WaitsForARealProcessToAnswer verifies the whole HTTP start
// path against a process this test actually spawned: the port is reserved, the
// binary is executed, stderr is drained, and the helper returns only once the
// health route answers.
func TestStartHTTP_WaitsForARealProcessToAnswer(t *testing.T) {
	tgt, err := startHTTP(t.Context(), fakeTargetOptions(t, transportHTTP))
	if err != nil {
		t.Fatalf("startHTTP: %v", err)
	}
	t.Cleanup(tgt.stop)

	if tgt.pid() <= 0 {
		t.Error("the target has no process")
	}
	if !strings.HasPrefix(tgt.baseURL, "http://127.0.0.1:") {
		t.Errorf("baseURL = %q, want a loopback address", tgt.baseURL)
	}
	info, err := serverInfo(t.Context(), tgt)
	if err != nil {
		t.Fatalf("serverInfo: %v", err)
	}
	if info.Version != "0.0.0-fake" {
		t.Errorf("Version = %q, want the build the process stated", info.Version)
	}
	if !processAlive(t.Context(), tgt.pid()) && runtimeGOOS == "linux" {
		t.Error("processAlive() = false for a process that just answered")
	}
	waitFor(t, func() bool { return strings.Contains(tgt.stderrText(), "fake server starting") },
		"the process's stderr was never drained")
}

// TestStartStdio_SpeaksOverItsOwnPipes verifies the other transport's start
// path, and the handshake the caller completes before anything is timed.
func TestStartStdio_SpeaksOverItsOwnPipes(t *testing.T) {
	tgt, err := startStdio(t.Context(), fakeTargetOptions(t, transportStdio))
	if err != nil {
		t.Fatalf("startStdio: %v", err)
	}
	t.Cleanup(tgt.stop)

	c, err := newStdioCaller(t.Context(), tgt)
	if err != nil {
		t.Fatalf("newStdioCaller: %v", err)
	}
	got, err := c.call(t.Context(), methodToolsList, map[string]any{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	// The bytes are asserted and the duration is not. A reply from a stand-in
	// on the same machine can arrive inside the monotonic clock's resolution,
	// which on Windows is coarse enough to report exactly zero — a true reading
	// of a call that fast, and not something a test should refuse.
	if got.Bytes == 0 {
		t.Errorf("call() = %+v, want a reply", got)
	}
	if got.Duration < 0 {
		t.Errorf("call() = %+v, want a duration of zero or more", got)
	}
	c.close()
}

// TestStartHTTP_ReportsABinaryItCannotRun verifies a target that will not start
// is an error rather than a wait for a health route nothing is serving.
func TestStartHTTP_ReportsABinaryItCannotRun(t *testing.T) {
	opts := fakeTargetOptions(t, transportHTTP)
	opts.binary = filepath.Join(t.TempDir(), "no-such-binary")
	if _, err := startHTTP(t.Context(), opts); err == nil {
		t.Error("expected an error starting a binary that is not there")
	}
}

// waitFor polls until the condition holds or the test gives up, so an assertion
// about a goroutine's work does not race it.
func waitFor(t *testing.T, ok func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error(what)
}
