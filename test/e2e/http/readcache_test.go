//go:build httpe2e && unix

package httpe2e

import (
	"context"
	"crypto/md5" //nolint:gosec // G501: the mirror protocol names files by md5; this is an identifier, not a hash of trust
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The files read fetches used to outlive the process that fetched them. The
// cache removed a file on eviction, eviction only runs inside a live process,
// and whatever the cache held when the process ended stayed in the temp
// directory for good: one directory per restart. These cases drive the REAL
// binary, because the promise is about what is left on disk after the process
// is gone, which no unit test can be on the far side of.

// readRootPattern matches the per-process read root inside a temp directory.
const readRootPattern = "libgen-mcp-read-*"

// readPayload is the file the stand-in mirror serves; the libgen source checks
// the bytes it receives against the md5 it asked for, so the identifier is
// derived from it rather than invented.
var readPayload = []byte("A plain text book the read tool can page through.\n")

func readPayloadMD5() string {
	sum := md5.Sum(readPayload) //nolint:gosec // G401: see the import
	return hex.EncodeToString(sum[:])
}

// startReadMirror serves the three hops a libgen download takes (ads.php, then
// get.php with its key, then the file itself) for readPayload.
func startReadMirror(t *testing.T) *mirror {
	t.Helper()
	var m *mirror
	m = startMirror(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ads.php":
			fmt.Fprintf(w, `<html><a href="get.php?md5=%s&key=READKEY">GET</a></html>`, readPayloadMD5())
		case "/get.php":
			http.Redirect(w, r, m.url+"/cdn/book.txt", http.StatusTemporaryRedirect)
		case "/cdn/book.txt":
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Disposition", `attachment; filename="book.txt"`)
			_, _ = w.Write(readPayload)
		default:
			http.NotFound(w, r)
		}
	})
	return m
}

// readServer is one server process this file starts itself, so it can end it
// with the signal the case is about rather than the harness's kill.
type readServer struct {
	*server
	cmd     *exec.Cmd
	stopped chan error
}

// startReadServer starts the binary serving read (fetching is on) against m,
// with tmp as its temp directory.
func startReadServer(t *testing.T, m *mirror, tmp string) *readServer {
	t.Helper()
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	// context.Background, not t.Context: the case ends the process itself, with
	// the signal it is about, and a context canceled at the end of the test
	// would kill it first.
	//nolint:gosec // G204: the binary this package built, on a port it reserved
	cmd := exec.CommandContext(context.Background(), serverBinary(t), "--http", addr)
	cmd.Env = append(os.Environ(), "LIBGEN_MCP_LOG_LEVEL=info", "LIBGEN_MCP_DOWNLOAD_DIR="+t.TempDir(),
		"LIBGEN_MCP_SERVER_FETCH=true", "TMPDIR="+tmp)
	for k, v := range mirrorEnv(m) {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the server: %v", err)
	}
	rs := &readServer{
		server:  &server{baseURL: "http://" + addr, logs: func() string { return "" }},
		cmd:     cmd,
		stopped: make(chan error, 1),
	}
	go func() { rs.stopped <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	waitHealthy(t, rs.server)
	return rs
}

// read asks for readPayload by md5 and fails the test unless it came back as
// text.
func (rs *readServer) read(t *testing.T) {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read","arguments":{"md5":%q}}}`, readPayloadMD5())
	reply := rs.do(t, request{body: body})
	if reply.status != http.StatusOK || strings.Contains(reply.body, `"isError":true`) || !strings.Contains(reply.body, "plain text book") {
		t.Fatalf("read did not return the book: %d %s", reply.status, truncate(reply.body))
	}
}

// stop sends sig and waits for the process to end.
func (rs *readServer) stop(t *testing.T, sig syscall.Signal) {
	t.Helper()
	if err := rs.cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signaling the server: %v", err)
	}
	select {
	case <-rs.stopped:
	case <-time.After(30 * time.Second):
		t.Fatalf("the server outlived %v by 30s", sig)
	}
}

// readRoots lists the read roots in tmp.
func readRoots(t *testing.T, tmp string) []string {
	t.Helper()
	roots, err := filepath.Glob(filepath.Join(tmp, readRootPattern))
	if err != nil {
		t.Fatal(err)
	}
	return roots
}

// filesUnder lists every regular file beneath dir. On Unix the read root's
// lock is on the directory itself, so every file there is one read fetched.
func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TestReadLeavesNothingBehindAfterACleanExit is the common case and the one
// that leaked on every restart: a server that read a file and was then asked
// to stop removes the file and its directory before it exits.
func TestReadLeavesNothingBehindAfterACleanExit(t *testing.T) {
	tmp := t.TempDir()
	rs := startReadServer(t, startReadMirror(t), tmp)
	rs.read(t)

	// Asserted by what is on disk, not by the layout this change introduced, so
	// the case fails against the code it fixes for the reason it was written:
	// the fetched file is still there once the process has gone.
	if fetched := filesUnder(t, tmp); len(fetched) != 1 {
		t.Fatalf("after a read the temp directory holds %d files, want the one fetched: %v", len(fetched), fetched)
	}

	rs.stop(t, syscall.SIGTERM)
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a clean exit left %d entries in the temp directory, first %q", len(entries), entries[0].Name())
	}
}

// TestReadRemovesWhatAKilledServerLeft covers the exit no cleanup survives: a
// server killed outright leaves its read root, and the next server to fetch a
// file from the same temp directory removes it, because the dead process's
// lock went with it.
func TestReadRemovesWhatAKilledServerLeft(t *testing.T) {
	tmp := t.TempDir()
	m := startReadMirror(t)

	killed := startReadServer(t, m, tmp)
	killed.read(t)
	killed.stop(t, syscall.SIGKILL)
	left := readRoots(t, tmp)
	if len(left) != 1 {
		t.Fatalf("a killed server left %d read roots, want 1 (the case needs the leftover): %v", len(left), left)
	}
	// The sweep leaves any root younger than a minute alone, whatever its
	// lock says, so a root its live owner has made and not yet locked is never
	// taken for dead. The leftover is aged past that rather than waited out.
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(left[0], old, old); err != nil {
		t.Fatal(err)
	}

	next := startReadServer(t, m, tmp)
	next.read(t)
	if _, err := os.Stat(left[0]); !os.IsNotExist(err) {
		t.Errorf("the killed server's root %q survived the next server's first read (stat err %v)", left[0], err)
	}
	if now := readRoots(t, tmp); len(now) != 1 || now[0] == left[0] {
		t.Errorf("after the next read the temp directory holds %v, want only the new server's root", now)
	}
	next.stop(t, syscall.SIGTERM)
	if now := readRoots(t, tmp); len(now) != 0 {
		t.Errorf("the next server's clean exit left %v", now)
	}
}
