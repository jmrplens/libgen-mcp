package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// rotatingPair writes a certificate and key into dir under fixed names, and
// returns the paths. The serial number identifies which generation it is.
//
// The two files are always rewritten in place, because that is what a rotation
// does: certbot, a Kubernetes secret projection and Vault's agent all replace
// the contents behind the paths the server was started with, and a reloader that
// only noticed a new path would notice none of them.
//
// The modification times are set explicitly rather than left to the write. Two
// certificates of the same shape can have the same byte count, and a filesystem
// with coarse timestamps could stamp two writes a few milliseconds apart with
// the same time — which would make the test depend on where it runs rather than
// on the code.
func rotatingPair(t *testing.T, dir string, serial int64, when time.Time) (certPath, keyPath string) {
	t.Helper()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	certPEM, keyPEM := mintPair(t, serial)
	writeStamped(t, certPath, certPEM, when)
	writeStamped(t, keyPath, keyPEM, when)
	return certPath, keyPath
}

// mintPair returns a self-signed certificate for the loopback address and its
// key, both PEM encoded, carrying the serial number the caller asked for.
func mintPair(t *testing.T, serial int64) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "libgen-mcp rotation test"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating a certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshaling the key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// writeStamped writes one PEM file and stamps it with the given time.
//
// It is not [writePEM], which encodes a DER block and leaves the timestamp to
// the write: here the timestamp is the thing under test.
func writeStamped(t *testing.T, path string, body []byte, when time.Time) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o600); err != nil { //nolint:gosec // path is built by the caller from the test's own temp dir and two literal names.
		t.Fatalf("writing %s: %v", path, err)
	}
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("stamping %s: %v", path, err)
	}
}

// servedSerial returns the serial number of the leaf certificate the reloader
// would present to a client right now.
func servedSerial(t *testing.T, r *certReloader) int64 {
	t.Helper()
	cert, err := r.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if cert == nil || len(cert.Certificate) == 0 {
		t.Fatal("GetCertificate returned no certificate")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parsing the served leaf: %v", err)
	}
	return leaf.SerialNumber.Int64()
}

// TestCertReloaderPresentsTheCertificateThatIsOnDiskNow is the reason this type
// exists: a certificate replaced under a running server is the one the next
// handshake gets, with no restart.
//
// Loading the pair once at startup made rotation a restart, and a restart here
// cuts every download in flight and takes the temp cache that would have served
// the retry with it. This asserts the whole promise at the seam a handshake
// reaches.
func TestCertReloaderPresentsTheCertificateThatIsOnDiskNow(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	certPath, keyPath := rotatingPair(t, dir, 1, base)

	reloader, err := newCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}
	if got := servedSerial(t, reloader); got != 1 {
		t.Fatalf("served serial before the rotation = %d, want 1", got)
	}

	rotatingPair(t, dir, 2, base.Add(time.Minute))

	if got := servedSerial(t, reloader); got != 2 {
		t.Errorf("served serial after the rotation = %d, want 2: the new pair on disk was not picked up", got)
	}
}

// TestCertReloaderReadsUnchangedFilesOnce keeps the check off the hot path.
//
// The staleness test is two stats; the load is a parse of two PEM files and a
// key-pair match. Doing the second on every handshake would put a measurable
// cost on every new connection for a file that changes twice a year, so the
// stamp has to be what decides, and this is what says it does.
func TestCertReloaderReadsUnchangedFilesOnce(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := rotatingPair(t, dir, 7, time.Now().Add(-time.Hour))

	loads := 0
	original := loadTLSKeyPair
	stubLoadTLSKeyPair(t, func(certFile, keyFile string) (tls.Certificate, error) {
		loads++
		return original(certFile, keyFile)
	})

	reloader, err := newCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}
	for range 5 {
		servedSerial(t, reloader)
	}

	if loads != 1 {
		t.Errorf("the pair was loaded %d times across five handshakes with no rotation, want 1", loads)
	}
}

// TestCertReloaderKeepsThePreviousCertificateThroughAHalfWrittenRotation covers
// the window every rotation has, and the log line it must not flood.
//
// Writing a certificate and its key is two writes, and between them the pair on
// disk does not match. A reloader that failed the handshake there would turn a
// routine renewal into an outage lasting as long as one file write, on whichever
// instance happened to be asked in that window. The previous certificate is
// still valid, so it is what gets served until the pair is whole again — and
// because the state persists across every handshake in that window, the warning
// is written once for it rather than once per client.
func TestCertReloaderKeepsThePreviousCertificateThroughAHalfWrittenRotation(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	certPath, keyPath := rotatingPair(t, dir, 1, base)

	reloader, err := newCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}
	servedSerial(t, reloader)

	warnings := captureReloadWarnings(t)

	// The certificate of the new generation lands first; its key has not been
	// written yet, so the pair on disk does not match.
	newCert, newKey := mintPair(t, 2)
	writeStamped(t, certPath, newCert, base.Add(time.Minute))

	for range 4 {
		if got := servedSerial(t, reloader); got != 1 {
			t.Fatalf("served serial mid-rotation = %d, want the previous 1", got)
		}
	}
	if got := warnings(); got != 1 {
		t.Errorf("%d warnings across four handshakes in one broken state, want 1: a client retrying would bury the line that says what is wrong", got)
	}

	// The key completes the pair, and the next handshake moves on.
	writeStamped(t, keyPath, newKey, base.Add(2*time.Minute))

	if got := servedSerial(t, reloader); got != 2 {
		t.Errorf("served serial after the key landed = %d, want 2", got)
	}
}

// TestCertReloaderWarnsAgainWhenTheBrokenStateChanges is the other half of
// reporting once.
//
// Silencing a repeated state must not silence a new one: a rotation that fails,
// is retried and fails differently is two things an operator needs to see. The
// stamp is what tells them apart, which is also why a failed load deliberately
// does not become the recorded source.
func TestCertReloaderWarnsAgainWhenTheBrokenStateChanges(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	certPath, keyPath := rotatingPair(t, dir, 1, base)

	reloader, err := newCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}
	warnings := captureReloadWarnings(t)

	// Each broken state is the new certificate landing without its key, which
	// is the shape a rotation is caught in; only the file's timestamp tells the
	// second one from the first.

	firstBroken, _ := mintPair(t, 2)
	writeStamped(t, certPath, firstBroken, base.Add(time.Minute))
	servedSerial(t, reloader)
	servedSerial(t, reloader)

	secondBroken, _ := mintPair(t, 3)
	writeStamped(t, certPath, secondBroken, base.Add(2*time.Minute))
	servedSerial(t, reloader)

	if got := warnings(); got != 2 {
		t.Errorf("%d warnings for two distinct broken states, want 2", got)
	}
	if got := servedSerial(t, reloader); got != 1 {
		t.Errorf("served serial = %d, want the original 1 throughout: neither broken pair should have been adopted", got)
	}
}

// TestCertReloaderKeepsThePreviousCertificateWhenTheFilesAreGone covers the
// other way a rotation can be caught in the middle: the files absent rather than
// mismatched.
//
// A renewal that unlinks before it writes, or a mount that is briefly gone, must
// not take the listener down. The certificate already in memory is unaffected by
// anything happening to the files it was read from.
func TestCertReloaderKeepsThePreviousCertificateWhenTheFilesAreGone(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := rotatingPair(t, dir, 3, time.Now().Add(-time.Hour))

	reloader, err := newCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}
	servedSerial(t, reloader)

	if removeErr := os.Remove(certPath); removeErr != nil {
		t.Fatalf("removing the certificate: %v", removeErr)
	}

	if got := servedSerial(t, reloader); got != 3 {
		t.Errorf("served serial with the certificate file gone = %d, want the loaded 3", got)
	}
}

// TestNewCertReloaderRefusesAnUnloadablePair keeps the first load strict.
//
// Startup is the one moment there is nothing to fall back to and an operator is
// watching, so a path that does not exist or a key that does not match its
// certificate has to be a named error there rather than a handshake failure
// reported later by a client. It is the promise validateTLSFiles already makes,
// and moving the load behind a callback must not quietly drop it.
func TestNewCertReloaderRefusesAnUnloadablePair(t *testing.T) {
	dir := t.TempDir()
	certPath, _ := rotatingPair(t, dir, 1, time.Now())
	otherDir := t.TempDir()
	_, foreignKey := rotatingPair(t, otherDir, 2, time.Now())

	for _, tc := range []struct {
		name     string
		certFile string
		keyFile  string
	}{
		{name: "missing files", certFile: filepath.Join(dir, "absent.pem"), keyFile: filepath.Join(dir, "absent.key")},
		{name: "key does not match the certificate", certFile: certPath, keyFile: foreignKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reloader, err := newCertReloader(tc.certFile, tc.keyFile)
			if err == nil {
				t.Fatal("newCertReloader() = nil error, want the pair refused")
			}
			if reloader != nil {
				t.Errorf("newCertReloader() returned %+v alongside %v, want nil", reloader, err)
			}
		})
	}
}

// stampedPair writes a pair, returns the paths and their stamp, and fails the
// test if the stamp cannot be read.
func stampedPair(t *testing.T, base time.Time) (certPath, keyPath string, stamp certStamp) {
	t.Helper()

	certPath, keyPath = rotatingPair(t, t.TempDir(), 1, base)
	stamp, err := stampOf(certPath, keyPath)
	if err != nil {
		t.Fatalf("stampOf: %v", err)
	}
	return certPath, keyPath, stamp
}

// mustStamp reads the stamp of a pair that is expected to be readable.
func mustStamp(t *testing.T, certPath, keyPath string) certStamp {
	t.Helper()

	stamp, err := stampOf(certPath, keyPath)
	if err != nil {
		t.Fatalf("stampOf: %v", err)
	}
	return stamp
}

// TestCertStampIsStableForFilesNobodyTouched is the half that keeps the reload
// off the hot path: an unchanged pair has to compare equal, or every handshake
// re-reads and parses two PEM files for nothing.
func TestCertStampIsStableForFilesNobodyTouched(t *testing.T) {
	certPath, keyPath, first := stampedPair(t, time.Now().Add(-time.Hour).Truncate(time.Second))

	if again := mustStamp(t, certPath, keyPath); again != first {
		t.Errorf("stampOf() = %+v then %+v for files nobody touched", first, again)
	}
}

// TestCertStampNoticesAWriteThatKeptTheLength pins the modification time.
//
// Size alone would miss the common renewal outright: two certificates from the
// same issuer for the same names differ in their serial and their signature, not
// usually in their byte count.
func TestCertStampNoticesAWriteThatKeptTheLength(t *testing.T) {
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	certPath, keyPath, first := stampedPair(t, base)

	// Rewriting the same bytes keeps every size identical, so the modification
	// time is the only thing left to notice it by.
	body, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("reading the certificate: %v", err)
	}
	writeStamped(t, certPath, body, base.Add(time.Minute))

	moved := mustStamp(t, certPath, keyPath)
	if moved.certSize != first.certSize {
		t.Fatalf("the case did not hold the size still: %d then %d", first.certSize, moved.certSize)
	}
	if moved == first {
		t.Error("the stamp did not change when the certificate was rewritten at the same length")
	}
}

// TestCertStampNoticesAWriteThatKeptTheTime pins the size.
//
// Modification time alone would miss a write a filesystem with coarse
// timestamps stamped identically, and a container image's layer can hand two
// files the same mtime outright.
func TestCertStampNoticesAWriteThatKeptTheTime(t *testing.T) {
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	certPath, keyPath, first := stampedPair(t, base)

	body, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("reading the key: %v", err)
	}
	writeStamped(t, keyPath, append(body, '\n'), base)

	grown := mustStamp(t, certPath, keyPath)
	if grown.keyTime != first.keyTime {
		t.Fatalf("the case did not hold the time still: %d then %d", first.keyTime, grown.keyTime)
	}
	if grown == first {
		t.Error("the stamp did not change when the key grew by a byte under the same modification time")
	}
}

// TestCertStampReportsAPairItCannotStat keeps an unreadable file distinguishable
// from a readable one.
//
// A zero stamp returned without an error would compare unequal to the recorded
// source and send the handshake down the reload path, where the load fails for
// the same reason — the same outcome by a longer road, with the warning naming
// the wrong step.
func TestCertStampReportsAPairItCannotStat(t *testing.T) {
	certPath, keyPath, _ := stampedPair(t, time.Now().Add(-time.Hour))
	absent := filepath.Join(filepath.Dir(certPath), "absent.pem")

	for _, pair := range [][2]string{{absent, keyPath}, {certPath, absent}} {
		if stamp, err := stampOf(pair[0], pair[1]); err == nil {
			t.Errorf("stampOf(%q, %q) = %+v, nil, want an error", pair[0], pair[1], stamp)
		}
	}
}

// TestTLSConfigForServesThroughTheReloader pins the wiring: the certificate is
// reached through the callback rather than frozen into Certificates, which is
// the whole of what makes a rotation possible, and the version floor and the
// protocol list survive the change.
func TestTLSConfigForServesThroughTheReloader(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	certPath, keyPath := rotatingPair(t, dir, 5, base)

	cfg, err := tlsConfigFor(certPath, keyPath)
	if err != nil {
		t.Fatalf("tlsConfigFor() error = %v", err)
	}
	if cfg.GetCertificate == nil {
		t.Fatal("GetCertificate is nil: the certificate would be frozen at startup")
	}
	if len(cfg.Certificates) != 0 {
		t.Errorf("Certificates holds %d entries, want none: a listed certificate is what GetCertificate exists to replace", len(cfg.Certificates))
	}

	got, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil || got == nil {
		t.Fatalf("GetCertificate() = %v, %v, want the configured pair", got, err)
	}
	leaf, err := x509.ParseCertificate(got.Certificate[0])
	if err != nil {
		t.Fatalf("parsing the served leaf: %v", err)
	}
	if leaf.SerialNumber.Int64() != 5 {
		t.Errorf("served serial = %d, want the pair that was configured", leaf.SerialNumber.Int64())
	}

	// And the config a rotation is served through is still the config the
	// listener needs: dropping h2 here would put every client back on HTTP/1.1
	// with nothing failing to say so.
	rotatingPair(t, dir, 6, base.Add(time.Minute))
	rotated, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate() after the rotation: %v", err)
	}
	rotatedLeaf, err := x509.ParseCertificate(rotated.Certificate[0])
	if err != nil {
		t.Fatalf("parsing the rotated leaf: %v", err)
	}
	if rotatedLeaf.SerialNumber.Int64() != 6 {
		t.Errorf("served serial after the rotation = %d, want 6", rotatedLeaf.SerialNumber.Int64())
	}
}

// captureReloadWarnings counts the reloader's warnings while the test runs.
func captureReloadWarnings(t *testing.T) func() int {
	t.Helper()

	counter := &reloadWarnCounter{}
	previous := slog.Default()
	slog.SetDefault(slog.New(counter))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return counter.count
}

// reloadWarnCounter is a [slog.Handler] that counts the reloader's warnings.
type reloadWarnCounter struct {
	mu sync.Mutex
	n  int
}

// Enabled reports that every level is handled, so nothing is filtered before it
// is counted.
func (c *reloadWarnCounter) Enabled(context.Context, slog.Level) bool { return true }

// Handle counts a record that is one of the reloader's two failure messages.
func (c *reloadWarnCounter) Handle(_ context.Context, record slog.Record) error {
	if record.Level == slog.LevelWarn && strings.Contains(record.Message, "TLS certificate") {
		c.mu.Lock()
		c.n++
		c.mu.Unlock()
	}
	return nil
}

// WithAttrs returns the handler unchanged; the counter carries no attributes.
func (c *reloadWarnCounter) WithAttrs([]slog.Attr) slog.Handler { return c }

// WithGroup returns the handler unchanged; the counter carries no groups.
func (c *reloadWarnCounter) WithGroup(string) slog.Handler { return c }

// count returns how many warnings have been seen.
func (c *reloadWarnCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
