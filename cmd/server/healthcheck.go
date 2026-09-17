// healthcheck.go answers the container HEALTHCHECK from inside the binary.
//
// A curl or wget probe in the image has to restate the deployment's flags, and
// gets four of the five listener shapes this server supports wrong: another
// port, a unix socket, TLS this process terminates itself, and a mount under
// --http-path all serve perfectly while the probe reports unhealthy — and an
// orchestrator then restarts a container whose restart changes nothing.
//
// --healthcheck reads the listener off the running server's own command line
// instead. It finds the other instances of this binary, takes --http,
// --http-path, --tls-cert and --transport from their arguments, derives where
// /health is served, and asks. A target may also be given outright, for a probe
// run from outside the container or for a deployment whose process list cannot
// be read.
//
// The name is --healthcheck rather than --probe because cmd/probe is already
// something else in this repository: a live mirror diagnostic. `libgen-mcp
// --healthcheck` beside `dist/probe` cannot be confused; `--probe` would be a
// name meaning two things.
//
// One listener it cannot discover is one bound to port 0: the command line then
// says 0, and the port that was actually bound is known only to the kernel and
// to the server's own log. A deployment that asks the kernel for a port has to
// give --healthcheck the target, which it knows from that log.

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// healthcheckTimeout bounds one attempt. The image's HEALTHCHECK allows five
// seconds, and an attempt that has not answered in three is not going to.
const healthcheckTimeout = 3 * time.Second

// healthcheckBudget bounds the whole run, however many instances discovery
// finds. Attempts are sequential, so two unreachable listeners at the per-
// attempt timeout would already exceed the image's five seconds and be killed
// without a verdict; under this deadline the run answers instead, and a caller
// that imposed a shorter one of its own keeps it.
const healthcheckBudget = 4 * time.Second

// Exit codes.
const (
	healthcheckHealthy   = 0
	healthcheckUnhealthy = 1
	healthcheckUsage     = 2
)

// healthPath is the route a probe asks for, unless a given target named one.
const healthPath = "/health"

// healthTarget is where a probe connects.
type healthTarget struct {
	// scheme is "http", "https" or "unix".
	scheme string
	// addr is host:port, or the socket path for a unix target.
	addr string
	// path is the request path, /health under whatever --http-path mounts.
	path string
	// certFile is the PEM certificate an https listener is verified against, as
	// the only trusted root: the server's own --tls-cert. Empty means the
	// system roots.
	certFile string
}

func (t healthTarget) String() string {
	if t.scheme == "unix" {
		return "unix:" + t.addr + t.path
	}
	return t.scheme + "://" + t.addr + t.path
}

// listenerFlags are the flags a probe reads off a peer's command line: the ones
// that decide where, and whether, HTTP is served.
type listenerFlags struct {
	addr      string
	basePath  string
	tlsCert   string
	transport string
	// utility is set when the command line is a healthcheck, a shutdown, a
	// version or a help invocation rather than a server.
	utility bool
}

// parseListenerFlags reads the listener flags out of a command line, argv[0]
// excluded.
//
// It accepts the spellings the flag package accepts — -name=value and -name
// value, with one or two dashes — and ignores every other flag and every
// positional argument rather than refusing them: the command line belongs to a
// process that already parsed it successfully.
//
// A bare -- ends the scan, because it ended the peer's own: everything after it
// was a positional argument to that process, so reading `-- --http` as an HTTP
// listener would send the probe to a port a stdio server never opened.
func parseListenerFlags(args []string) listenerFlags {
	var f listenerFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return f
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			continue
		}
		name, value, hasValue := splitFlag(arg)
		// takeValue reads a string flag's argument, from the same token or the
		// next one.
		takeValue := func() string {
			if hasValue {
				return value
			}
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch name {
		case "http":
			f.addr = takeValue()
		case "http-path":
			f.basePath = takeValue()
		case "tls-cert":
			f.tlsCert = takeValue()
		case "transport":
			f.transport = takeValue()
		case "healthcheck", "shutdown", "version", "h", "help":
			f.utility = true
		}
	}
	return f
}

// splitFlag breaks one argument into its flag name, its inline value and
// whether it had one.
func splitFlag(arg string) (name, value string, hasValue bool) {
	name = strings.TrimLeft(arg, "-")
	if eq := strings.IndexByte(name, '='); eq >= 0 {
		return name[:eq], name[eq+1:], true
	}
	return name, "", false
}

// peerServesHTTP decides whether a peer with these flags has an HTTP listener,
// the way resolveTransport decides it for this process, and returns the address
// it would have bound.
//
// stdinIsNull answers what the peer's --transport auto observed: whether its
// file descriptor 0 is the null device. When that cannot be read, HTTP is
// assumed and the connection settles it — which errs toward reporting a stdio
// instance unhealthy rather than an HTTP instance healthy unprobed.
func peerServesHTTP(f listenerFlags, stdinIsNull func() (bool, error)) (serves bool, addr, reason string) {
	bound := func() string {
		if f.addr == "" {
			return defaultHTTPAddr
		}
		return f.addr
	}
	switch strings.TrimSpace(strings.ToLower(f.transport)) {
	case transportStdio:
		return false, "", "--transport=stdio"
	case transportHTTP:
		return true, bound(), "--transport=http"
	case transportAuto:
		isNull, err := stdinIsNull()
		switch {
		case err != nil:
			return true, bound(), "--transport=auto and stdin could not be examined (" + err.Error() + "), so HTTP is assumed"
		case isNull:
			return true, bound(), "--transport=auto and stdin is " + os.DevNull
		}
		return false, "", "--transport=auto and stdin is not " + os.DevNull
	}
	if f.addr != "" {
		return true, f.addr, "--http"
	}
	return false, "", "no transport flag and no --http, so stdio"
}

// healthTargetFor derives where a listener with these flags serves /health.
//
// An unspecified host — which is what :8080 and 0.0.0.0:8080 mean — is reached
// on loopback: a probe runs beside the process, and the wildcard says nothing
// about which address to dial.
func healthTargetFor(addr, basePath, tlsCert string) healthTarget {
	path := normalizeBasePath(basePath) + healthPath
	if isUnixSocketAddr(addr) {
		return healthTarget{scheme: "unix", addr: addr, path: path}
	}
	scheme := "http"
	if tlsCert != "" {
		scheme = "https"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return healthTarget{scheme: scheme, addr: addr, path: path, certFile: tlsCert}
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return healthTarget{scheme: scheme, addr: net.JoinHostPort(host, port), path: path, certFile: tlsCert}
}

// parseHealthTarget reads a target given on the command line: an http or https
// URL, unix:<path> or a bare path for a socket, or host:port for plain HTTP.
func parseHealthTarget(s string) (healthTarget, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return healthTarget{}, errors.New("empty target")
	case strings.HasPrefix(s, "http://"), strings.HasPrefix(s, "https://"):
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return healthTarget{}, fmt.Errorf("%q is not a URL with a host", s)
		}
		path := u.Path
		if path == "" {
			path = healthPath
		}
		return healthTarget{scheme: u.Scheme, addr: u.Host, path: path}, nil
	case strings.HasPrefix(s, "unix:"):
		return parseHealthTarget(strings.TrimPrefix(s, "unix:"))
	case strings.Contains(s, "://"):
		// Refused before the socket rule below can read it as a path. That rule
		// says "anything containing a separator is a path", which is right for a
		// listen address and wrong here: "ftp://example.org" would become a
		// socket nobody is serving, and the failure would name a file rather
		// than the scheme that was the actual mistake.
		return healthTarget{}, fmt.Errorf("%q names a scheme this probe does not speak; use http, https or unix", s)
	case isUnixSocketAddr(s):
		return healthTarget{scheme: "unix", addr: s, path: healthPath}, nil
	}
	if _, _, err := net.SplitHostPort(s); err != nil {
		return healthTarget{}, fmt.Errorf("%q is not a URL, a socket path or host:port", s)
	}
	return healthTargetFor(s, "", ""), nil
}

// askHealth asks the target for /health and returns nil when it answers 200.
func askHealth(ctx context.Context, target healthTarget) error {
	tlsConfig, err := healthTLSConfig(target)
	if err != nil {
		return err
	}
	rt := &http.Transport{
		// The proxy environment must not redirect a loopback probe.
		Proxy:           nil,
		TLSClientConfig: tlsConfig,
	}
	requestURL := target.scheme + "://" + target.addr + target.path
	if target.scheme == "unix" {
		socketPath := target.addr
		rt.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		}
		requestURL = "http://unix" + target.path
	}
	// The request context carries the run's overall deadline, so this timeout is
	// the per-attempt ceiling and whichever is nearer ends the attempt.
	client := &http.Client{Transport: rt, Timeout: healthcheckTimeout}
	defer client.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", target, resp.Status)
	}
	return nil
}

// healthTLSConfig decides how an https listener is verified.
//
// A listener derived from the server's own flags is verified against the
// certificate --tls-cert names, read from the same file the server read: it is
// the only root the probe trusts, and the name the probe expects is one the
// certificate itself carries. So a self-signed certificate on a loopback address
// verifies the standard way — with no chain it never had and no host name it was
// never issued for — and nothing else is trusted. Without a certificate file, a
// given https target gets the system roots and the host it named.
func healthTLSConfig(target healthTarget) (*tls.Config, error) {
	if target.scheme != "https" || target.certFile == "" {
		return &tls.Config{MinVersion: tls.VersionTLS12}, nil
	}
	cert, err := loadServerCertificate(target.certFile)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
		ServerName: certificateName(cert),
	}, nil
}

// certificateName is a name the certificate was issued for, which is what the
// probe asks the listener to answer to: a DNS name, else an IP address, else the
// common name.
func certificateName(cert *x509.Certificate) string {
	if len(cert.DNSNames) > 0 {
		return cert.DNSNames[0]
	}
	if len(cert.IPAddresses) > 0 {
		return cert.IPAddresses[0].String()
	}
	return cert.Subject.CommonName
}

// loadServerCertificate reads the first certificate of a PEM file.
func loadServerCertificate(path string) (*x509.Certificate, error) {
	rest, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the certificate to trust: %w", err)
	}
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, fmt.Errorf("%s holds no CERTIFICATE block", path)
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, parseErr := x509.ParseCertificate(block.Bytes)
		if parseErr != nil {
			return nil, fmt.Errorf("parsing the certificate in %s: %w", path, parseErr)
		}
		return cert, nil
	}
}

// healthPeer is a running instance of this binary as the probe sees it.
type healthPeer struct {
	pid  int32
	args []string
}

// healthcheckDeps are the things about the machine a discovery run reads, plus
// the budget it is given — injectable so the decision logic is testable without
// live processes and without spending the real budget to watch it run out.
type healthcheckDeps struct {
	// peers lists the other instances of this binary, with their command lines.
	peers func() ([]healthPeer, error)
	// stdinIsNull reports whether a peer's file descriptor 0 is the null device.
	stdinIsNull func(pid int32) (bool, error)
	// budget bounds the whole run. Zero, which is what the binary passes, means
	// healthcheckBudget.
	budget time.Duration
}

// runHealthcheck implements --healthcheck and returns the process exit code: 0
// when a listener answered, or when the only instance serves stdio, which has
// nothing to probe and is alive; 1 when none did; 2 for a target that does not
// parse. certFile is this invocation's own --tls-cert, the pin for a given https
// target.
func runHealthcheck(ctx context.Context, args []string, certFile string, deps healthcheckDeps, stderr io.Writer) int {
	// One deadline for the whole run, so a peer that never answers cannot spend
	// the next peer's time. A caller with an earlier deadline of its own keeps
	// it: the context already carries the tighter one.
	budget := deps.budget
	if budget <= 0 {
		budget = healthcheckBudget
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > budget {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}

	if len(args) > 0 {
		return checkGivenTarget(ctx, args[0], certFile, stderr)
	}
	return checkDiscoveredPeers(ctx, deps, stderr)
}

// checkGivenTarget probes a target the caller named outright.
func checkGivenTarget(ctx context.Context, arg, certFile string, stderr io.Writer) int {
	target, err := parseHealthTarget(arg)
	if err != nil {
		fmt.Fprintf(stderr, "healthcheck: %v\n", err)
		return healthcheckUsage
	}
	target.certFile = certFile
	if probeErr := askHealth(ctx, target); probeErr != nil {
		fmt.Fprintf(stderr, "healthcheck: %s: %v\n", target, probeErr)
		return healthcheckUnhealthy
	}
	fmt.Fprintf(stderr, "healthcheck: %s answered\n", target)
	return healthcheckHealthy
}

// checkDiscoveredPeers probes every running instance until one answers.
func checkDiscoveredPeers(ctx context.Context, deps healthcheckDeps, stderr io.Writer) int {
	peers, err := deps.peers()
	if err != nil {
		fmt.Fprintf(stderr, "healthcheck: listing processes: %v\n", err)
		return healthcheckUnhealthy
	}
	slices.SortFunc(peers, func(a, b healthPeer) int { return int(a.pid - b.pid) })

	var failures []string
	servers := 0
	for _, peer := range peers {
		if len(peer.args) == 0 {
			continue
		}
		flags := parseListenerFlags(peer.args[1:])
		if flags.utility {
			continue
		}
		servers++
		serves, addr, why := peerServesHTTP(flags, func() (bool, error) { return deps.stdinIsNull(peer.pid) })
		if !serves {
			fmt.Fprintf(stderr, "healthcheck: pid %d serves stdio (%s) and is running\n", peer.pid, why)
			return healthcheckHealthy
		}
		target := healthTargetFor(addr, flags.basePath, flags.tlsCert)
		if probeErr := askHealth(ctx, target); probeErr != nil {
			failures = append(failures, fmt.Sprintf("pid %d at %s: %v", peer.pid, target, probeErr))
			continue
		}
		fmt.Fprintf(stderr, "healthcheck: pid %d at %s answered\n", peer.pid, target)
		return healthcheckHealthy
	}
	if servers == 0 {
		fmt.Fprintf(stderr, "healthcheck: no running instance of %s\n", canonicalBinaryName(filepath.Base(os.Args[0])))
		return healthcheckUnhealthy
	}
	fmt.Fprintf(stderr, "healthcheck: %s\n", strings.Join(failures, "; "))
	return healthcheckUnhealthy
}

// livePeers lists the other instances of this binary with their command lines,
// through the same lookup --shutdown uses.
func livePeers() ([]healthPeer, error) {
	found, err := findPeers()
	if err != nil {
		return nil, err
	}
	peers := make([]healthPeer, 0, len(found))
	for _, p := range found {
		args, argsErr := p.CmdlineSlice()
		if argsErr != nil || len(args) == 0 {
			// A process that vanished between the listing and the read, or one
			// whose command line this user may not see.
			continue
		}
		peers = append(peers, healthPeer{pid: p.Pid, args: args})
	}
	return peers, nil
}

// stdinIsNullUnder reports whether the process's file descriptor 0, as published
// under procRoot (/proc on Linux), is the null device.
func stdinIsNullUnder(procRoot string, pid int32) (bool, error) {
	link, err := os.Readlink(filepath.Join(procRoot, strconv.Itoa(int(pid)), "fd", "0"))
	if err != nil {
		return false, err
	}
	return link == os.DevNull, nil
}
