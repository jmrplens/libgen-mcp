//go:build httpe2e

package httpe2e

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// downloadCallBody resolves a book by md5, which reaches the mirror and then
// waits on it. The md5 is a well-formed one that the fixture never has to
// recognize: the point is where the call blocks, not what it finds.
const downloadCallBody = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"download",` +
	`"arguments":{"md5":"0123456789abcdef0123456789abcdef","resolve_only":true}}}`

// TestCeiling_ASecondDownloadFromOneCallerIsRefusedWhileTheFirstRuns drives the
// per-caller ceiling over the real binary against a mirror that never answers.
//
// A hanging mirror is what makes the case observable at all: the ceiling is
// about calls that are still running, and a download that completes in
// milliseconds never has a second one beside it. It is also the shape the
// ceiling exists for — one caller holding every download slot on the replica
// while the rate bucket still shows them well inside their rate.
func TestCeiling_ASecondDownloadFromOneCallerIsRefusedWhileTheFirstRuns(t *testing.T) {
	reached := make(chan struct{}, 1)
	release := make(chan struct{})
	m := startMirror(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case reached <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	// Registered after startMirror, so it runs BEFORE that fixture's own Close —
	// which waits for outstanding requests, and would otherwise wait out the
	// whole upstream timeout on a handler that is blocked on purpose.
	t.Cleanup(func() { close(release) })

	env := mirrorEnv(m)
	// mirrorEnv's five-second upstream timeout is the test's wall clock, and
	// deliberately left alone: the second and third calls reach the same blocked
	// mirror and have to end on it before their replies can be read. Five
	// seconds is far longer than the microseconds between issuing them, so the
	// first call is still in flight throughout.
	//
	// The staged start-retries are shortened, though. A minute of waiting is the
	// right default for a real mirror and is the whole runtime of this test.
	env["LIBGEN_MCP_DOWNLOAD_START_RETRY_WAITS"] = "100ms"
	// A loopback bind, because mirrorEnv lifts the private-address guard so the
	// fixture on 127.0.0.1 is reachable, and that pairing is refused outright on
	// a listener other machines can open. Naming the proxy is what keeps the
	// per-caller identity — and so the ceiling — real on a loopback bind.
	s := startServer(t, env,
		"--trusted-proxy-header", "X-Real-IP",
		"--trusted-proxies", "127.0.0.1/32",
		"--max-inflight-per-client", "1",
	)

	first := make(chan response, 1)
	go func() {
		reply, _ := s.try(t, downloadRequest("203.0.113.7"))
		first <- reply
	}()

	select {
	case <-reached:
	case <-time.After(60 * time.Second):
		t.Fatalf("the first download never reached the mirror. Output:\n%s", s.logs())
	}

	second := s.do(t, downloadRequest("203.0.113.7"))
	if !strings.Contains(second.body, "in flight") {
		t.Errorf("the second concurrent download from one caller was not refused by the ceiling: %s", truncate(second.body))
	}

	// A different caller is unaffected, which is the difference between this and
	// the process-wide download semaphore.
	other := s.do(t, downloadRequest("198.51.100.23"))
	if strings.Contains(other.body, "in flight") {
		t.Errorf("a second caller was refused for the first one's traffic: %s", truncate(other.body))
	}
}

// downloadRequest is a download call announcing a client address.
func downloadRequest(client string) request {
	return request{
		method:  http.MethodPost,
		body:    downloadCallBody,
		headers: map[string]string{"X-Real-IP": client},
	}
}
