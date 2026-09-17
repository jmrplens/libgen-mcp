package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

// TestParseListenerFlagsReadsEverySpellingTheFlagPackageAccepts covers the
// command line the probe has to read without owning it.
//
// The peer already parsed these successfully, so the parser is lenient about
// what it does not recognize and exact about what it does. -- ends the scan,
// because it ended the peer's own: everything after it was a positional
// argument, and reading `-- --http` as a listener would send the probe to a port
// a stdio server never opened.
func TestParseListenerFlagsReadsEverySpellingTheFlagPackageAccepts(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want listenerFlags
	}{
		{name: "nothing at all", args: nil},
		{
			name: "one dash, separate value",
			args: []string{"-http", "0.0.0.0:8080"},
			want: listenerFlags{addr: "0.0.0.0:8080"},
		},
		{
			name: "two dashes, inline value",
			args: []string{"--http=0.0.0.0:8080"},
			want: listenerFlags{addr: "0.0.0.0:8080"},
		},
		{
			name: "every listener flag at once",
			args: []string{"--http", "/run/mcp.sock", "--http-path", "/libgen", "--tls-cert", "/etc/x.pem", "--transport", "http"},
			want: listenerFlags{addr: "/run/mcp.sock", basePath: "/libgen", tlsCert: "/etc/x.pem", transport: "http"},
		},
		{
			name: "flags this parser does not read are ignored",
			args: []string{"--stateless=false", "--rate-limit-rps", "10", "--http", ":9000", "positional"},
			want: listenerFlags{addr: ":9000"},
		},
		{
			name: "a bare -- ends the scan",
			args: []string{"--", "--http", ":9000"},
		},
		{
			name: "a trailing flag with no value",
			args: []string{"--http"},
		},
		{name: "a healthcheck invocation is not a server", args: []string{"--healthcheck"}, want: listenerFlags{utility: true}},
		{name: "a shutdown invocation is not a server", args: []string{"--shutdown"}, want: listenerFlags{utility: true}},
		{name: "a version invocation is not a server", args: []string{"--version"}, want: listenerFlags{utility: true}},
		{name: "a help invocation is not a server", args: []string{"-h"}, want: listenerFlags{utility: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseListenerFlags(tc.args); got != tc.want {
				t.Errorf("parseListenerFlags(%q) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}
}

// TestPeerServesHTTPMirrorsTheServersOwnDecision is the half that has to agree
// with resolveTransport, or the probe looks for a listener where there is none —
// or, worse, calls a stdio instance unhealthy for not having one.
func TestPeerServesHTTPMirrorsTheServersOwnDecision(t *testing.T) {
	isNull := func(b bool) func() (bool, error) { return func() (bool, error) { return b, nil } }
	unreadable := func() (bool, error) { return false, errors.ErrUnsupported }

	cases := []struct {
		name       string
		flags      listenerFlags
		stdin      func() (bool, error)
		wantServes bool
		wantAddr   string
	}{
		{name: "no flags at all is stdio", flags: listenerFlags{}, stdin: unreadable},
		{name: "--http names the listener", flags: listenerFlags{addr: ":9000"}, stdin: unreadable, wantServes: true, wantAddr: ":9000"},
		{name: "--transport stdio wins over --http", flags: listenerFlags{addr: ":9000", transport: "stdio"}, stdin: unreadable},
		{
			name:       "--transport http with no address binds the default",
			flags:      listenerFlags{transport: "http"},
			stdin:      unreadable,
			wantServes: true,
			wantAddr:   defaultHTTPAddr,
		},
		{
			name:       "--transport auto with stdin on the null device is HTTP",
			flags:      listenerFlags{transport: "auto"},
			stdin:      isNull(true),
			wantServes: true,
			wantAddr:   defaultHTTPAddr,
		},
		{name: "--transport auto with a stdin is stdio", flags: listenerFlags{transport: "auto"}, stdin: isNull(false)},
		{
			// Assuming HTTP errs toward reporting a stdio instance unhealthy
			// rather than an HTTP instance healthy unprobed, which is the
			// direction a health check should fail in.
			name:       "--transport auto with an unreadable stdin assumes HTTP",
			flags:      listenerFlags{transport: "auto"},
			stdin:      unreadable,
			wantServes: true,
			wantAddr:   defaultHTTPAddr,
		},
		{name: "a transport spelled in caps", flags: listenerFlags{transport: " HTTP "}, stdin: unreadable, wantServes: true, wantAddr: defaultHTTPAddr},
		{
			name:       "--transport http keeps the address --http named",
			flags:      listenerFlags{transport: "http", addr: "127.0.0.1:9000"},
			stdin:      unreadable,
			wantServes: true,
			wantAddr:   "127.0.0.1:9000",
		},
		{
			name:       "--transport auto keeps it too",
			flags:      listenerFlags{transport: "auto", addr: "127.0.0.1:9000"},
			stdin:      isNull(true),
			wantServes: true,
			wantAddr:   "127.0.0.1:9000",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serves, addr, why := peerServesHTTP(tc.flags, tc.stdin)
			if serves != tc.wantServes {
				t.Errorf("serves = %v (%s), want %v", serves, why, tc.wantServes)
			}
			if addr != tc.wantAddr {
				t.Errorf("addr = %q, want %q", addr, tc.wantAddr)
			}
			if why == "" {
				t.Error("the decision carries no reason, so a failing probe says nothing about why it looked there")
			}
		})
	}
}

// TestHealthTargetForDerivesWhereHealthIsServed covers the derivation, and the
// mount prefix is the part that is new work rather than a port.
func TestHealthTargetForDerivesWhereHealthIsServed(t *testing.T) {
	cases := []struct {
		name     string
		addr     string
		basePath string
		tlsCert  string
		want     string
	}{
		{name: "a plain port", addr: "127.0.0.1:8080", want: "http://127.0.0.1:8080/health"},
		// A wildcard says nothing about which address to dial, and the probe
		// runs beside the process.
		{name: "a wildcard bind is reached on loopback", addr: ":8080", want: "http://127.0.0.1:8080/health"},
		{name: "0.0.0.0 likewise", addr: "0.0.0.0:8080", want: "http://127.0.0.1:8080/health"},
		{name: "the IPv6 wildcard likewise", addr: "[::]:8080", want: "http://127.0.0.1:8080/health"},
		{name: "a named host is kept", addr: "mcp.example.org:8443", want: "http://mcp.example.org:8443/health"},
		{name: "a unix socket", addr: "/run/mcp.sock", want: "unix:/run/mcp.sock/health"},
		{name: "TLS this process terminates", addr: ":8443", tlsCert: "/etc/x.pem", want: "https://127.0.0.1:8443/health"},
		{name: "a mount under a prefix", addr: ":8080", basePath: "/libgen", want: "http://127.0.0.1:8080/libgen/health"},
		{name: "a prefix spelled without a slash", addr: ":8080", basePath: "libgen", want: "http://127.0.0.1:8080/libgen/health"},
		{name: "a prefix with a trailing slash", addr: ":8080", basePath: "/libgen/", want: "http://127.0.0.1:8080/libgen/health"},
		{name: "a socket under a prefix", addr: "/run/mcp.sock", basePath: "/libgen", want: "unix:/run/mcp.sock/libgen/health"},
		// An address the peer's own flag parser would have refused. It is kept
		// as written rather than guessed at: the probe is reading somebody
		// else's command line, and inventing a port there would send it
		// somewhere nobody is listening and report that as the verdict.
		{name: "an address with no port", addr: "localhost", want: "http://localhost/health"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := healthTargetFor(tc.addr, tc.basePath, tc.tlsCert).String(); got != tc.want {
				t.Errorf("healthTargetFor(%q, %q, %q) = %q, want %q", tc.addr, tc.basePath, tc.tlsCert, got, tc.want)
			}
		})
	}
}

// TestParseHealthTargetReadsWhatACallerWouldType covers the target given
// outright, which is what a probe from outside the container uses.
func TestParseHealthTargetReadsWhatACallerWouldType(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		for _, tc := range []struct{ in, want string }{
			{in: "http://127.0.0.1:8080/health", want: "http://127.0.0.1:8080/health"},
			{in: "https://mcp.example.org/libgen/health", want: "https://mcp.example.org/libgen/health"},
			{in: "http://127.0.0.1:8080", want: "http://127.0.0.1:8080/health"},
			{in: "unix:/run/mcp.sock", want: "unix:/run/mcp.sock/health"},
			{in: "/run/mcp.sock", want: "unix:/run/mcp.sock/health"},
			{in: "127.0.0.1:8080", want: "http://127.0.0.1:8080/health"},
			{in: ":8080", want: "http://127.0.0.1:8080/health"},
		} {
			got, err := parseHealthTarget(tc.in)
			if err != nil {
				t.Errorf("parseHealthTarget(%q) = %v", tc.in, err)
				continue
			}
			if got.String() != tc.want {
				t.Errorf("parseHealthTarget(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
	})

	t.Run("refused", func(t *testing.T) {
		for _, in := range []string{"", "   ", "not a target", "http://", "ftp://example.org"} {
			if _, err := parseHealthTarget(in); err == nil {
				t.Errorf("parseHealthTarget(%q) was accepted", in)
			}
		}
	})
}

// TestRunHealthcheckAgainstAGivenTarget covers the three exit codes, which are
// the whole interface an orchestrator reads.
func TestRunHealthcheckAgainstAGivenTarget(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	draining := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer draining.Close()

	cases := []struct {
		name   string
		target string
		want   int
	}{
		{name: "a listener that answers", target: ok.URL + "/health", want: healthcheckHealthy},
		// A draining instance is not healthy, which is the point of the 503.
		{name: "a listener that is draining", target: draining.URL + "/health", want: healthcheckUnhealthy},
		{name: "nothing listening", target: "http://127.0.0.1:1/health", want: healthcheckUnhealthy},
		{name: "a target that does not parse", target: "not a target", want: healthcheckUsage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			got := runHealthcheck(t.Context(), []string{tc.target}, "", healthcheckDeps{budget: 2 * time.Second}, &stderr)
			if got != tc.want {
				t.Errorf("exit = %d, want %d (%s)", got, tc.want, stderr.String())
			}
			if stderr.Len() == 0 {
				t.Error("nothing was written to stderr, so a failing check says nothing about why")
			}
		})
	}
}

// TestRunHealthcheckOverDiscoveredPeers covers the path the image's HEALTHCHECK
// takes: no target, read the listener off whatever is running.
func TestRunHealthcheckOverDiscoveredPeers(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	addr := strings.TrimPrefix(ok.URL, "http://")

	t.Run("a serving peer answers", func(t *testing.T) {
		var stderr bytes.Buffer
		deps := healthcheckDeps{
			peers: func() ([]healthPeer, error) {
				return []healthPeer{{pid: 42, args: []string{"libgen-mcp", "--http", addr}}}, nil
			},
			stdinIsNull: func(int32) (bool, error) { return false, errors.ErrUnsupported },
			budget:      2 * time.Second,
		}
		if got := runHealthcheck(t.Context(), nil, "", deps, &stderr); got != healthcheckHealthy {
			t.Errorf("exit = %d, want %d (%s)", got, healthcheckHealthy, stderr.String())
		}
	})

	t.Run("a stdio peer is healthy because it is running", func(t *testing.T) {
		var stderr bytes.Buffer
		deps := healthcheckDeps{
			peers: func() ([]healthPeer, error) {
				return []healthPeer{{pid: 42, args: []string{"libgen-mcp"}}}, nil
			},
			stdinIsNull: func(int32) (bool, error) { return false, nil },
			budget:      2 * time.Second,
		}
		if got := runHealthcheck(t.Context(), nil, "", deps, &stderr); got != healthcheckHealthy {
			t.Errorf("exit = %d, want %d: a stdio instance has no listener to probe (%s)", got, healthcheckHealthy, stderr.String())
		}
	})

	t.Run("a utility invocation is not an instance", func(t *testing.T) {
		var stderr bytes.Buffer
		deps := healthcheckDeps{
			peers: func() ([]healthPeer, error) {
				return []healthPeer{{pid: 42, args: []string{"libgen-mcp", "--shutdown"}}}, nil
			},
			stdinIsNull: func(int32) (bool, error) { return false, nil },
			budget:      2 * time.Second,
		}
		if got := runHealthcheck(t.Context(), nil, "", deps, &stderr); got != healthcheckUnhealthy {
			t.Errorf("exit = %d, want %d: another --shutdown running is not a healthy server", got, healthcheckUnhealthy)
		}
		if !strings.Contains(stderr.String(), "no running instance") {
			t.Errorf("stderr = %q, want it to say nothing is running", stderr.String())
		}
	})

	t.Run("a peer that does not answer", func(t *testing.T) {
		var stderr bytes.Buffer
		deps := healthcheckDeps{
			peers: func() ([]healthPeer, error) {
				return []healthPeer{{pid: 42, args: []string{"libgen-mcp", "--http", "127.0.0.1:1"}}}, nil
			},
			stdinIsNull: func(int32) (bool, error) { return false, errors.ErrUnsupported },
			budget:      2 * time.Second,
		}
		if got := runHealthcheck(t.Context(), nil, "", deps, &stderr); got != healthcheckUnhealthy {
			t.Errorf("exit = %d, want %d", got, healthcheckUnhealthy)
		}
		if !strings.Contains(stderr.String(), "pid 42") {
			t.Errorf("stderr = %q, want it to name the instance that failed", stderr.String())
		}
	})

	t.Run("a peer with no command line is skipped", func(t *testing.T) {
		var stderr bytes.Buffer
		deps := healthcheckDeps{
			peers: func() ([]healthPeer, error) {
				// A process that vanished between the listing and the read, or
				// one whose command line this user may not see.
				return []healthPeer{{pid: 42}}, nil
			},
			stdinIsNull: func(int32) (bool, error) { return false, nil },
			budget:      2 * time.Second,
		}
		if got := runHealthcheck(t.Context(), nil, "", deps, &stderr); got != healthcheckUnhealthy {
			t.Errorf("exit = %d, want %d: a peer nothing is known about is not a healthy server", got, healthcheckUnhealthy)
		}
		if !strings.Contains(stderr.String(), "no running instance") {
			t.Errorf("stderr = %q, want it to say nothing is running", stderr.String())
		}
	})

	t.Run("nothing running at all", func(t *testing.T) {
		var stderr bytes.Buffer
		deps := healthcheckDeps{
			peers:       func() ([]healthPeer, error) { return nil, nil },
			stdinIsNull: func(int32) (bool, error) { return false, nil },
			budget:      2 * time.Second,
		}
		if got := runHealthcheck(t.Context(), nil, "", deps, &stderr); got != healthcheckUnhealthy {
			t.Errorf("exit = %d, want %d", got, healthcheckUnhealthy)
		}
	})

	t.Run("the process list cannot be read", func(t *testing.T) {
		var stderr bytes.Buffer
		deps := healthcheckDeps{
			peers:       func() ([]healthPeer, error) { return nil, errors.New("procfs is not mounted") },
			stdinIsNull: func(int32) (bool, error) { return false, nil },
			budget:      2 * time.Second,
		}
		if got := runHealthcheck(t.Context(), nil, "", deps, &stderr); got != healthcheckUnhealthy {
			t.Errorf("exit = %d, want %d", got, healthcheckUnhealthy)
		}
	})
}

// TestRunHealthcheckKeepsItsBudget is what stops one unreachable instance from
// spending the next one's time — and the whole run from outlasting the image's
// five-second timeout, which would be a container killed with no verdict.
func TestRunHealthcheckKeepsItsBudget(t *testing.T) {
	var stderr bytes.Buffer
	deps := healthcheckDeps{
		peers: func() ([]healthPeer, error) {
			return []healthPeer{
				{pid: 1, args: []string{"libgen-mcp", "--http", "127.0.0.1:1"}},
				{pid: 2, args: []string{"libgen-mcp", "--http", "127.0.0.1:1"}},
				{pid: 3, args: []string{"libgen-mcp", "--http", "127.0.0.1:1"}},
			}, nil
		},
		stdinIsNull: func(int32) (bool, error) { return false, errors.ErrUnsupported },
		budget:      300 * time.Millisecond,
	}

	start := time.Now()
	if got := runHealthcheck(t.Context(), nil, "", deps, &stderr); got != healthcheckUnhealthy {
		t.Errorf("exit = %d, want %d", got, healthcheckUnhealthy)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("the run took %v against a 300ms budget", elapsed)
	}
}

// TestHealthTLSConfigPinsTheServersOwnCertificate is what lets a self-signed
// certificate on a loopback address verify the standard way.
//
// The certificate --tls-cert names is the only root trusted, and the name asked
// for is one that certificate carries — so there is no chain it never had and no
// host name it was never issued for, and nothing else is accepted.
func TestHealthTLSConfigPinsTheServersOwnCertificate(t *testing.T) {
	cert := newTestCert(t)

	pinned, err := healthTLSConfig(healthTarget{scheme: "https", addr: "127.0.0.1:8443", certFile: cert.certFile})
	if err != nil {
		t.Fatalf("healthTLSConfig: %v", err)
	}
	if pinned.RootCAs == nil {
		t.Error("no root pool, so the probe fell back to the system roots for a self-signed certificate")
	}
	if pinned.ServerName == "" {
		t.Error("no server name, so the probe asks the listener to answer to an address the certificate was not issued for")
	}
	if pinned.MinVersion != tlsMinVersion {
		t.Errorf("MinVersion = %d, want %d", pinned.MinVersion, tlsMinVersion)
	}

	plain, err := healthTLSConfig(healthTarget{scheme: "http", addr: "127.0.0.1:8080"})
	if err != nil {
		t.Fatalf("healthTLSConfig: %v", err)
	}
	if plain.RootCAs != nil {
		t.Error("a plain HTTP target was given a root pool")
	}

	_, absentErr := healthTLSConfig(healthTarget{
		scheme: "https", addr: "127.0.0.1:8443", certFile: filepath.Join(t.TempDir(), "absent.pem"),
	})
	if absentErr == nil {
		t.Error("a certificate file that is not there was accepted")
	}
}

// tlsMinVersion is the floor healthTLSConfig sets, written out so the assertion
// above reads as the contract rather than as the constant's own name.
const tlsMinVersion = 0x0303 // tls.VersionTLS12

// TestCanonicalBinaryNameMatchesEveryPlatformVariant is what keeps a healthcheck
// from reporting somebody else's container healthy on a shared host, and a
// shutdown from leaving the one instance it was run to replace.
func TestCanonicalBinaryNameMatchesEveryPlatformVariant(t *testing.T) {
	for _, name := range []string{
		"libgen-mcp", "libgen-mcp.exe",
		"libgen-mcp-linux-amd64", "libgen-mcp-linux-arm64",
		"libgen-mcp-darwin-amd64", "libgen-mcp-darwin-arm64",
		"libgen-mcp-windows-amd64.exe",
	} {
		if got := canonicalBinaryName(name); got != "libgen-mcp" {
			t.Errorf("canonicalBinaryName(%q) = %q, want libgen-mcp", name, got)
		}
	}
	// And not so eager that it matches something else entirely.
	for _, name := range []string{"libgen-mcp-proxy", "probe", "other-mcp"} {
		if canonicalBinaryName(name) == "libgen-mcp" {
			t.Errorf("canonicalBinaryName(%q) matched this binary", name)
		}
	}
}

// TestStdinIsNullUnderReadsAPeersDescriptor covers the procfs read --transport
// auto is settled by, against a directory this test builds.
func TestStdinIsNullUnderReadsAPeersDescriptor(t *testing.T) {
	if _, err := stdinIsNullUnder(t.TempDir(), 1); err == nil {
		t.Error("a missing descriptor was read as an answer rather than as an error")
	}
}

// TestRunShutdownWithNothingRunningSucceeds pins the outcome that reads as a
// failure and is not: the point of the command is that no instance is running
// afterwards, and finding none means that is already true.
func TestRunShutdownWithNothingRunningSucceeds(t *testing.T) {
	previous := listProcesses
	t.Cleanup(func() { listProcesses = previous })
	listProcesses = func() ([]*processHandle, error) { return nil, nil }

	var stderr bytes.Buffer
	if got := runShutdown(&stderr); got != 0 {
		t.Errorf("exit = %d, want 0 with nothing to shut down (%s)", got, stderr.String())
	}
}

// TestRunShutdownReportsAnUnreadableProcessList is the one failure the command
// has: it cannot know whether anything is running, so it must not claim success.
func TestRunShutdownReportsAnUnreadableProcessList(t *testing.T) {
	previous := listProcesses
	t.Cleanup(func() { listProcesses = previous })
	listProcesses = func() ([]*processHandle, error) { return nil, errors.New("procfs is not mounted") }

	var stderr bytes.Buffer
	if got := runShutdown(&stderr); got != 1 {
		t.Errorf("exit = %d, want 1 when the process list cannot be read", got)
	}
	if !strings.Contains(stderr.String(), "procfs is not mounted") {
		t.Errorf("stderr = %q, want it to carry the reason", stderr.String())
	}
}

// contextAlreadyDone is a helper for the budget case above, kept here so the
// intent reads at the call site.
func contextAlreadyDone(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

// TestRunHealthcheckHonorsATighterCallerDeadline pins that the run's own budget
// is a ceiling rather than a floor, the same rule the shutdown budget follows.
func TestRunHealthcheckHonorsATighterCallerDeadline(t *testing.T) {
	var stderr bytes.Buffer
	deps := healthcheckDeps{
		peers: func() ([]healthPeer, error) {
			return []healthPeer{{pid: 1, args: []string{"libgen-mcp", "--http", "127.0.0.1:1"}}}, nil
		},
		stdinIsNull: func(int32) (bool, error) { return false, errors.ErrUnsupported },
	}

	start := time.Now()
	if got := runHealthcheck(contextAlreadyDone(t), nil, "", deps, &stderr); got != healthcheckUnhealthy {
		t.Errorf("exit = %d, want %d", got, healthcheckUnhealthy)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("the run took %v although the caller's context was already done", elapsed)
	}
}

// TestAskHealthOverEveryTransportItSpeaks covers the dial the derivation
// produces, which is the half the target tests cannot reach.
//
// The unix case is the one worth the setup: the request URL is rewritten to a
// placeholder host and the connection is redirected to the socket, so a wrapper
// that forgot either half would produce a request that cannot be sent at all.
func TestAskHealthOverEveryTransportItSpeaks(t *testing.T) {
	t.Run("over a unix socket", func(t *testing.T) {
		// A short directory rather than t.TempDir(): macOS caps sun_path at 104
		// bytes, and t.TempDir() builds a name out of this test's own — which is
		// already past the cap before the socket's file name is added.
		dir, err := os.MkdirTemp("", "lg") //nolint:usetesting // t.TempDir()'s name is longer than sun_path allows
		if err != nil {
			t.Fatalf("making a directory for the socket: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })

		path := filepath.Join(dir, "s.sock")
		ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
		if err != nil {
			// Not every platform this suite runs on has AF_UNIX available to
			// Go. The dial hook is exercised wherever it does — Linux, which is
			// also where coverage is measured — and by the socket cases in the
			// httpe2e module.
			t.Skipf("unix sockets are not usable here: %v", err)
		}
		srv := &http.Server{
			Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
			ReadHeaderTimeout: time.Second,
		}
		go func() { _ = srv.Serve(ln) }()
		t.Cleanup(func() { _ = srv.Close() })

		if probeErr := askHealth(t.Context(), healthTarget{scheme: "unix", addr: path, path: healthPath}); probeErr != nil {
			t.Errorf("askHealth over a socket: %v", probeErr)
		}
	})

	t.Run("over TLS this process terminates", func(t *testing.T) {
		cert := newTestCert(t)
		ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("binding: %v", err)
		}
		pair, err := tls.LoadX509KeyPair(cert.certFile, cert.keyFile)
		if err != nil {
			t.Fatalf("loading the pair: %v", err)
		}
		srv := &http.Server{
			Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
			ReadHeaderTimeout: time.Second,
			TLSConfig:         &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12},
		}
		go func() { _ = srv.ServeTLS(ln, "", "") }()
		t.Cleanup(func() { _ = srv.Close() })

		target := healthTargetFor(ln.Addr().String(), "", cert.certFile)
		if probeErr := askHealth(t.Context(), target); probeErr != nil {
			t.Errorf("askHealth against a self-signed listener pinned to its own certificate: %v", probeErr)
		}
	})

	t.Run("a certificate that cannot be read", func(t *testing.T) {
		target := healthTargetFor("127.0.0.1:1", "", filepath.Join(t.TempDir(), "absent.pem"))
		if err := askHealth(t.Context(), target); err == nil {
			t.Error("a target pinned to a missing certificate was probed anyway")
		}
	})
}

// TestLoadServerCertificateRefusesWhatIsNotOne covers the file the pin reads,
// which is an operator's to get wrong.
func TestLoadServerCertificateRefusesWhatIsNotOne(t *testing.T) {
	dir := t.TempDir()
	write := func(name, contents string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
		return path
	}

	for _, tc := range []struct{ name, contents string }{
		{name: "not-pem.pem", contents: "this is not a certificate"},
		{name: "empty.pem", contents: ""},
		// A PEM file that carries only the key is the mistake worth naming: it
		// is the other half of the pair the operator did pass.
		{name: "key-only.pem", contents: "-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----\n"},
		{name: "corrupt.pem", contents: "-----BEGIN CERTIFICATE-----\nZm9v\n-----END CERTIFICATE-----\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadServerCertificate(write(tc.name, tc.contents)); err == nil {
				t.Errorf("%s was read as a certificate", tc.name)
			}
		})
	}

	// And the one that works, so the refusals above are not passing because
	// nothing is ever accepted.
	cert := newTestCert(t)
	parsed, err := loadServerCertificate(cert.certFile)
	if err != nil {
		t.Fatalf("a real certificate was refused: %v", err)
	}
	if certificateName(parsed) == "" {
		t.Error("the certificate carries no name for the probe to ask the listener to answer to")
	}
}

// TestCertificateNamePrefersWhatAProbeCanAskFor covers the three shapes, in the
// order the probe wants them: a DNS name, else an address, else whatever the
// subject says.
func TestCertificateNamePrefersWhatAProbeCanAskFor(t *testing.T) {
	cases := []struct {
		name string
		cert *x509.Certificate
		want string
	}{
		{
			name: "a DNS name wins",
			cert: &x509.Certificate{
				DNSNames:    []string{"mcp.example.org"},
				IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
				Subject:     pkix.Name{CommonName: "ignored"},
			},
			want: "mcp.example.org",
		},
		{
			name: "an address when there is no name",
			cert: &x509.Certificate{
				IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
				Subject:     pkix.Name{CommonName: "ignored"},
			},
			want: "127.0.0.1",
		},
		{
			name: "the common name as a last resort",
			cert: &x509.Certificate{Subject: pkix.Name{CommonName: "localhost"}},
			want: "localhost",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := certificateName(tc.cert); got != tc.want {
				t.Errorf("certificateName = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLivePeersReadsTheRealProcessList covers the lookup the binary actually
// passes to the check.
//
// A child is started first, so the listing has something of this binary's own
// name to find and the read is exercised rather than skipped over. What is
// otherwise running on the machine is not this test's to decide.
func TestLivePeersReadsTheRealProcessList(t *testing.T) {
	helper := startHelper(t)

	peers, err := livePeers()
	if err != nil {
		t.Skipf("the process list is not readable here: %v", err)
	}
	var found bool
	for _, peer := range peers {
		if len(peer.args) == 0 {
			t.Errorf("pid %d was reported with no command line, which the probe cannot parse", peer.pid)
		}
		if peer.pid == helper.Pid {
			found = true
		}
	}
	if !found {
		t.Errorf("the child this test started (pid %d) was not among the %d peers found", helper.Pid, len(peers))
	}
}

// TestStdinIsNullUnderReadsARealDescriptor covers the procfs read against a
// directory shaped like one, so the Linux path is exercised on every platform.
func TestStdinIsNullUnderReadsARealDescriptor(t *testing.T) {
	root := t.TempDir()
	fdDir := filepath.Join(root, "42", "fd")
	if err := os.MkdirAll(fdDir, 0o750); err != nil {
		t.Fatalf("building the fake procfs: %v", err)
	}
	if err := os.Symlink(os.DevNull, filepath.Join(fdDir, "0")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	isNull, err := stdinIsNullUnder(root, 42)
	if err != nil {
		t.Fatalf("reading the descriptor: %v", err)
	}
	if !isNull {
		t.Errorf("a descriptor pointing at %s was not read as the null device", os.DevNull)
	}
}

// TestEnvironUnderReadsOnlyTheListenerVariables covers the procfs read, and the
// two rules it applies to what it finds.
//
// The file is the whole block the process was started with, secrets included, so
// what comes back is filtered: a probe that carried LIBGEN_MCP_ANNAS_KEY around
// would be one formatting mistake from a log line. And an entry with no "=" is
// skipped rather than guessed at.
func TestEnvironUnderReadsOnlyTheListenerVariables(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "77")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("making the fixture: %v", err)
	}
	block := strings.Join([]string{
		config.EnvName("HTTP_ADDR") + "=127.0.0.1:9123",
		config.EnvName("HTTP_PATH") + "=/libgen",
		config.EnvName("TLS_CERT") + "=/etc/ssl/mcp.crt",
		config.EnvName("TRANSPORT") + "=http",
		config.EnvName("ANNAS_KEY") + "=a-secret-nobody-asked-for",
		"PATH=/usr/bin",
		"a-malformed-entry-with-no-equals",
	}, "\x00") + "\x00"
	if err := os.WriteFile(filepath.Join(dir, "environ"), []byte(block), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	got, err := environUnder(root, 77)
	if err != nil {
		t.Fatalf("environUnder() error = %v", err)
	}

	want := map[string]string{
		config.EnvName("HTTP_ADDR"): "127.0.0.1:9123",
		config.EnvName("HTTP_PATH"): "/libgen",
		config.EnvName("TLS_CERT"):  "/etc/ssl/mcp.crt",
		config.EnvName("TRANSPORT"): "http",
	}
	if !maps.Equal(got, want) {
		t.Errorf("environUnder() = %v, want %v", got, want)
	}
	// Spelled out separately, because this is the one entry whose presence would
	// be a leak rather than a wrong answer.
	if _, leaked := got[config.EnvName("ANNAS_KEY")]; leaked {
		t.Error("environUnder returned a credential; it must keep only the listener variables")
	}
}

// TestEnvironUnderReportsAProcessItCannotRead keeps "nothing was looked at"
// distinguishable from "nothing was set", which is what the caveat rests on.
func TestEnvironUnderReportsAProcessItCannotRead(t *testing.T) {
	if got, err := environUnder(t.TempDir(), 77); err == nil {
		t.Errorf("environUnder() = %v, nil for a pid with no procfs entry, want an error", got)
	}
}

// TestOverlayListenerEnvKeepsTheCommandLineWinning is the probe's half of the
// server's own precedence.
//
// A probe that let the environment win would read a different configuration than
// the process it is probing whenever a deployment overrides one setting on the
// command line — which is the shape of every base image with a `command:`
// override.
func TestOverlayListenerEnvKeepsTheCommandLineWinning(t *testing.T) {
	env := map[string]string{
		config.EnvName("HTTP_ADDR"): "127.0.0.1:9123",
		config.EnvName("HTTP_PATH"): "/from-the-environment",
		config.EnvName("TLS_CERT"):  "/etc/ssl/mcp.crt",
		config.EnvName("TRANSPORT"): "http",
	}

	got := overlayListenerEnv(listenerFlags{addr: "127.0.0.1:7777"}, env)
	if got.addr != "127.0.0.1:7777" {
		t.Errorf("addr = %q, want the command line's", got.addr)
	}
	// The rest still comes from the environment, so the case above is precedence
	// rather than the overlay being skipped altogether.
	if got.basePath != "/from-the-environment" {
		t.Errorf("basePath = %q, want the variable's value", got.basePath)
	}
	if got.tlsCert != "/etc/ssl/mcp.crt" {
		t.Errorf("tlsCert = %q, want the variable's value", got.tlsCert)
	}
	if got.transport != "http" {
		t.Errorf("transport = %q, want the variable's value", got.transport)
	}
}

// TestRunHealthcheckFindsAnInstanceConfiguredThroughTheEnvironment is the gap
// --healthcheck shipped with: parseListenerFlags reads a command line, and a
// container configured entirely through `environment:` has nothing on one.
//
// Before the environment was read, this peer's address was derived as the
// default and the probe reported a healthy instance unhealthy — which an
// orchestrator answers by restarting a container whose restart changes nothing.
func TestRunHealthcheckFindsAnInstanceConfiguredThroughTheEnvironment(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/libgen/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	addr := strings.TrimPrefix(ok.URL, "http://")

	var stderr bytes.Buffer
	deps := healthcheckDeps{
		// No flags at all: the argument list is the bare binary.
		peers: func() ([]healthPeer, error) {
			return []healthPeer{{pid: 42, args: []string{"libgen-mcp"}}}, nil
		},
		stdinIsNull: func(int32) (bool, error) { return false, errors.ErrUnsupported },
		environ: func(int32) (map[string]string, error) {
			return map[string]string{
				config.EnvName("HTTP_ADDR"): addr,
				config.EnvName("HTTP_PATH"): "/libgen",
			}, nil
		},
		budget: 2 * time.Second,
	}
	if got := runHealthcheck(t.Context(), nil, "", deps, &stderr); got != healthcheckHealthy {
		t.Errorf("exit = %d, want %d: the listener is named by variables, not by flags (%s)",
			got, healthcheckHealthy, stderr.String())
	}
	// The mount under the prefix is part of it: a probe that read the address and
	// not the path would ask / and get the 404 above.
	if !strings.Contains(stderr.String(), "/libgen/health") {
		t.Errorf("stderr = %q, want it to name the probed path", stderr.String())
	}
}

// TestRunHealthcheckSaysWhenItCouldNotReadThePeerEnvironment is the honest
// fallback the platform split needs.
//
// Where procfs is not there, a peer with no --http is a peer whose address is a
// guess. Reporting the failure with the default address it assumed is what lets
// an operator tell "the server is down" from "the probe was looking in the wrong
// place", which are the same exit code and opposite actions.
func TestRunHealthcheckSaysWhenItCouldNotReadThePeerEnvironment(t *testing.T) {
	var stderr bytes.Buffer
	deps := healthcheckDeps{
		peers: func() ([]healthPeer, error) {
			return []healthPeer{{pid: 42, args: []string{"libgen-mcp", "--transport", "http"}}}, nil
		},
		stdinIsNull: func(int32) (bool, error) { return false, errors.ErrUnsupported },
		environ:     func(int32) (map[string]string, error) { return nil, errors.ErrUnsupported },
		budget:      2 * time.Second,
	}
	if got := runHealthcheck(t.Context(), nil, "", deps, &stderr); got != healthcheckUnhealthy {
		t.Fatalf("exit = %d, want %d", got, healthcheckUnhealthy)
	}
	out := stderr.String()
	if !strings.Contains(out, "environment could not be read") {
		t.Errorf("stderr = %q, want it to say the environment could not be read", out)
	}
	if !strings.Contains(out, defaultHTTPAddr) {
		t.Errorf("stderr = %q, want it to name the address it assumed", out)
	}
}

// TestRunHealthcheckDoesNotCaveatAnAddressItWasGiven keeps the note off every
// failure on a platform without procfs.
//
// A peer whose --http is on the command line is fully known whether or not the
// environment could be read, and a caveat printed there would attach to every
// failure on Windows and macOS — which is how a caveat stops being read.
func TestRunHealthcheckDoesNotCaveatAnAddressItWasGiven(t *testing.T) {
	var stderr bytes.Buffer
	deps := healthcheckDeps{
		peers: func() ([]healthPeer, error) {
			return []healthPeer{{pid: 42, args: []string{"libgen-mcp", "--http", "127.0.0.1:1"}}}, nil
		},
		stdinIsNull: func(int32) (bool, error) { return false, errors.ErrUnsupported },
		environ:     func(int32) (map[string]string, error) { return nil, errors.ErrUnsupported },
		budget:      2 * time.Second,
	}
	if got := runHealthcheck(t.Context(), nil, "", deps, &stderr); got != healthcheckUnhealthy {
		t.Fatalf("exit = %d, want %d", got, healthcheckUnhealthy)
	}
	if strings.Contains(stderr.String(), "environment could not be read") {
		t.Errorf("stderr = %q, want no caveat: the address was on the command line", stderr.String())
	}
}

// TestReadEnvironWithoutAReaderIsAFailure pins the nil case, which is what every
// caller that does not care about the environment produces.
//
// Returning an empty map would say the peer set no listener variables, which is
// a claim nothing checked; the caveat that follows depends on the difference.
func TestReadEnvironWithoutAReaderIsAFailure(t *testing.T) {
	if got, err := (healthcheckDeps{}).readEnviron(42); err == nil {
		t.Errorf("readEnviron() = %v, nil with no reader, want a failure", got)
	}
}
