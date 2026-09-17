//go:build collectore2e

// collector_test.go runs a genuine OpenTelemetry Collector and reads back what
// it parsed out of this server's exports.
//
// The receiver is the thing under test, which is why the pipeline ends in a file
// exporter: the collector writes OTLP JSON only after decoding the protobuf,
// routing it through a pipeline and re-encoding it, so a document appearing in
// that file is evidence that a real implementation understood what was sent.
// Reading raw bytes off a socket — what the in-process stub in test/e2e/http
// does — proves delivery and nothing about meaning.
package collectore2e

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// collectorImage pins the receiver under test.
//
// Never latest. A floating tag makes the suite's meaning change without a
// commit, so a collector release that tightened validation would either fail a
// run nobody had changed anything in or, worse, relax one and let a defect
// through unannounced. Bumping this is a deliberate act with a diff.
const collectorImage = "otel/opentelemetry-collector-contrib:0.159.0"

// The container-side ports and the files each pipeline writes.
//
// The receiver binds 0.0.0.0 rather than the image default of localhost: a
// published port reaches the container from outside its own loopback, and a
// receiver on localhost is simply never reached — which presents as a timeout
// with a perfectly healthy collector.
//
// Three file exporters rather than one, because the collector builds a separate
// exporter per pipeline: a single path would have three writers appending to one
// file and the interleaving would be ours to untangle.
const (
	collectorOTLPPort = "4318"
	collectorGRPCPort = "4317"
	tracesFile        = "traces.json"
	metricsFile       = "metrics.json"
	logsFile          = "logs.json"
)

// collectorConfig is the pipeline the container runs.
//
// All three signals are wired. The logs one is not optional here the way it is
// in a server with no bridge: this server does bridge slog to the logs provider,
// so a receiver without that pipeline would answer /v1/logs with 404, the SDK's
// error handler would record the refusal, and the acceptance case would report a
// defect in this file as though the server had emitted something invalid.
//
// The debug exporter is at basic verbosity: one counted line per batch, which
// leaves the container log small enough that "no error line appears in it" is an
// assertion a person can also check by eye.
const collectorConfig = `receivers:
  otlp:
    protocols:
      http:
        endpoint: 0.0.0.0:` + collectorOTLPPort + `
      grpc:
        endpoint: 0.0.0.0:` + collectorGRPCPort + `

exporters:
  file/traces:
    path: /out/` + tracesFile + `
  file/metrics:
    path: /out/` + metricsFile + `
  file/logs:
    path: /out/` + logsFile + `
  debug:
    verbosity: basic

service:
  telemetry:
    logs:
      level: info
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [file/traces, debug]
    metrics:
      receivers: [otlp]
      exporters: [file/metrics, debug]
    logs:
      receivers: [otlp]
      exporters: [file/logs, debug]
`

// collector is a running OpenTelemetry Collector container.
type collector struct {
	// endpoint is what OTEL_EXPORTER_OTLP_ENDPOINT is set to.
	endpoint string
	// grpcEndpoint is the same collector over the other protocol, which nothing
	// exercised until it existed.
	grpcEndpoint string
	// outDir is the host side of the bind mount the file exporters write into.
	outDir string
	name   string
}

// startCollector runs the collector for the duration of the test.
//
// Docker unavailability is a skip and never a failure: somebody without a daemon
// should be told why this suite did not run, not handed a red test they cannot
// act on. A container that starts and then refuses to serve is the opposite, and
// fails — at that point the configuration above is wrong, which is this module's
// own defect rather than the environment's.
func startCollector(t *testing.T) *collector {
	t.Helper()

	requireDocker(t)

	hostPort := freePort(t)
	grpcPort := freePort(t)
	dir := t.TempDir()
	outDir := filepath.Join(dir, "out")
	// World-writable, because a throwaway container writes here as its own
	// user. MkdirAll applies the process umask, which on most machines clears
	// the bits that user needs, so the mode is set again with Chmod, which does
	// not.
	if err := os.MkdirAll(outDir, 0o777); err != nil { //#nosec G301 -- a throwaway container writes here as its own user
		t.Fatalf("creating the collector output directory: %v", err)
	}
	if err := os.Chmod(outDir, 0o777); err != nil { //#nosec G302 -- the same directory, with the bits the umask took back
		t.Fatalf("opening the collector output directory to the container: %v", err)
	}

	confPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(confPath, []byte(collectorConfig), 0o644); err != nil { //#nosec G306 -- read by a throwaway container
		t.Fatalf("writing the collector config: %v", err)
	}

	name := "libgen-mcp-collectore2e-" + strconv.Itoa(hostPort)
	args := []string{
		"run", "-d", "--name", name,
		"-p", "127.0.0.1:" + strconv.Itoa(hostPort) + ":" + collectorOTLPPort,
		"-p", "127.0.0.1:" + strconv.Itoa(grpcPort) + ":" + collectorGRPCPort,
	}
	// Run as the invoking user so the exported files are readable here and
	// removable by t.TempDir's cleanup. Without this the image's own uid owns
	// them, and the cleanup of a run by an ordinary user fails on files it may
	// not delete.
	if uid := os.Getuid(); uid >= 0 {
		args = append(args, "--user", strconv.Itoa(uid)+":"+strconv.Itoa(os.Getgid()))
	}
	args = append(args,
		"-v", confPath+":/etc/otelcol-contrib/config.yaml:ro",
		"-v", outDir+":/out",
		collectorImage,
	)

	// Generous, because this may include pulling the image on a cold machine.
	runCtx, runCancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer runCancel()
	if out, err := exec.CommandContext(runCtx, "docker", args...).CombinedOutput(); err != nil {
		// requireDocker has already established that a daemon answers, so a
		// failure here is usually this module's own: a configuration the
		// collector rejects, a flag it does not know, a bind mount it cannot
		// make. Reporting those as an environmental skip is how a broken
		// harness stays green — the failure this module exists to prevent in
		// the server, and must not commit itself.
		//
		// Reaching the registry is the exception: a machine with a working
		// daemon and no route to the image has nothing to fix in the code.
		if isRegistryFailure(out) {
			t.Skipf("could not pull %s (%v):\n%s", collectorImage, err, out)
		}
		t.Fatalf("could not start %s (%v):\n%s", collectorImage, err, out)
	}

	c := &collector{
		endpoint:     "http://127.0.0.1:" + strconv.Itoa(hostPort),
		grpcEndpoint: "127.0.0.1:" + strconv.Itoa(grpcPort),
		outDir:       outDir,
		name:         name,
	}
	t.Cleanup(func() {
		// Not t.Context: cleanup runs after the test context is canceled, and a
		// container left behind would collide with the next run by name.
		rmCtx, rmCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer rmCancel()
		//#nosec G204 -- the container name is this file's own literal plus a port number
		_ = exec.CommandContext(rmCtx, "docker", "rm", "-f", c.name).Run()
	})

	c.waitReceiving(t)
	return c
}

// requireDocker skips unless a usable daemon is present.
func requireDocker(t *testing.T) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("this module bind-mounts POSIX paths into a Linux container; run it from WSL or a Linux machine")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available; a real collector is the whole point here, so this is skipped rather than modeled")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		t.Skip("docker is installed but not usable; skipping the real-collector suite")
	}
}

// isRegistryFailure reports whether docker could not reach the image.
func isRegistryFailure(out []byte) bool {
	text := strings.ToLower(string(out))
	for _, sign := range []string{
		"pull access denied", "no such host", "connection refused",
		"i/o timeout", "tls handshake timeout", "manifest unknown",
		"error response from daemon: get ",
	} {
		if strings.Contains(text, sign) {
			return true
		}
	}
	return false
}

// waitReceiving polls the OTLP endpoint until it accepts an export.
//
// The probe is an empty but well-formed OTLP JSON document rather than a health
// endpoint, because what the tests need to know is that the receiver is taking
// exports, not that the process is up. Those differ for several seconds while
// the collector builds its pipelines, and a server that exported into that
// window would have its batch refused for reasons that are nobody's defect.
func (c *collector) waitReceiving(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
			c.endpoint+"/v1/traces", strings.NewReader(`{"resourceSpans":[]}`))
		if err != nil {
			t.Fatalf("building the collector readiness probe: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the collector never accepted an export. Container output:\n%s", c.containerLogs(t))
}

// containerLogs returns everything the collector has written to its own log.
func (c *collector) containerLogs(t *testing.T) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	//#nosec G204 -- the container name is this file's own literal plus a port number
	out, err := exec.CommandContext(ctx, "docker", "logs", c.name).CombinedOutput()
	if err != nil {
		return "could not read the container log: " + err.Error()
	}
	return string(out)
}

// documents decodes the OTLP JSON the file exporter has written so far.
//
// A line that will not parse is skipped rather than reported. The exporter
// appends whole documents, so the only way to see a broken one is to read the
// file while a line is mid-write; every caller polls, so the next read has it
// intact. Failing on it would turn a timing artifact into a test failure, and
// nothing is hidden by the tolerance: a document that never becomes parseable is
// one the caller times out waiting for.
func documents[T any](t *testing.T, path string) []T {
	t.Helper()

	raw, err := os.ReadFile(path) //#nosec G304 -- a path this file built inside the test's own temp dir
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var out []T
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var doc T
		if json.Unmarshal([]byte(line), &doc) != nil {
			continue
		}
		out = append(out, doc)
	}
	return out
}

// freePort reserves a port and hands it back, which is as close to atomic as a
// test can get without holding the listener open.
func freePort(t *testing.T) int {
	t.Helper()

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if closeErr := ln.Close(); closeErr != nil {
		t.Fatalf("releasing the reserved port: %v", closeErr)
	}
	return port
}
