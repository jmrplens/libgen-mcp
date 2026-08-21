//go:build e2e

// Loads the developer's .env before any test runs, and reports which optional
// credentials it found.

package e2e

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmrplens/libgen-mcp/internal/netguard"
)

// credentialVars are the optional settings that decide how much of the suite
// actually exercises. Each one that is missing turns real coverage into a skip,
// silently, so they are reported once at start-up rather than discovered by
// reading a run's skip list.
var credentialVars = []struct {
	name    string
	unlocks string
}{
	{"LIBGEN_MCP_UNPAYWALL_EMAIL", "the unpaywall article source"},
	{"LIBGEN_MCP_CORE_KEY", "the core article source"},
	{"LIBGEN_MCP_ANNAS_KEY", "Anna's member fast-download (IPFS is used without it)"},
}

// TestMain loads .env from the repository root, then runs the suite.
//
// `make test-e2e` already sources .env, but that is not the only way this suite
// is run: CLAUDE.md documents the bare `go test -tags e2e ./test/e2e/`, and an
// IDE running a single test does neither. Those invocations silently produced a
// reduced run — core skipped, Anna's on the keyless path, unpaywall on a
// fallback address — which looks the same in the output as a full one.
//
// An entry already present in the environment always wins, so an explicit
// export still overrides the file and CI, which has no .env, is unaffected.
//
// It also permits private destinations for the whole binary, the way the six
// unit packages with fixtures do. Part of this suite serves its fixtures from
// httptest servers, which listen on loopback — the address family
// internal/netguard refuses — so under the real policy those cases spend their
// retry schedules failing to dial their own fixtures. Unlike those packages this
// one also talks to live mirrors, and the allowance covers those calls too; that
// is acceptable in a test binary aimed at hosts we choose, and the policy itself
// is exercised against real dials in internal/netguard.
func TestMain(m *testing.M) {
	if path, err := findDotEnv(); err == nil {
		if n, loadErr := loadDotEnv(path); loadErr != nil {
			fmt.Fprintf(os.Stderr, "e2e: could not read %s: %v\n", path, loadErr)
		} else if n > 0 {
			fmt.Fprintf(os.Stderr, "e2e: loaded %d setting(s) from %s\n", n, path)
		}
	}
	reportCredentials()
	restore := netguard.SetAllowPrivateForTest(true)
	code := m.Run()
	restore()
	os.Exit(code)
}

// reportCredentials prints which optional credentials are present, so a
// developer can tell a full run from a partial one before reading the results.
func reportCredentials() {
	for _, c := range credentialVars {
		if strings.TrimSpace(os.Getenv(c.name)) != "" {
			fmt.Fprintf(os.Stderr, "e2e: %s set — exercising %s\n", c.name, c.unlocks)
		} else {
			fmt.Fprintf(os.Stderr, "e2e: %s unset — NOT exercising %s\n", c.name, c.unlocks)
		}
	}
}

// findDotEnv walks up from the working directory looking for a .env beside a
// go.mod, so the suite finds the repository root whether it was started from
// there or from test/e2e.
func findDotEnv() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, ".env")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, nil
		}
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return "", os.ErrNotExist // reached the module root without finding one
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

// loadDotEnv reads KEY=VALUE lines into the environment, skipping blanks,
// comments and any key already set. It returns how many it applied.
//
// The format handled is the one `set -a; . ./.env` accepts in the Makefile:
// optional `export `, surrounding quotes stripped. Anything needing shell
// evaluation is out of scope on purpose — a test harness should not run the
// contents of a credentials file.
func loadDotEnv(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()

	applied := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, ok := parseDotEnvLine(scanner.Text())
		if !ok || os.Getenv(key) != "" {
			continue
		}
		if setErr := os.Setenv(key, value); setErr == nil {
			applied++
		}
	}
	return applied, scanner.Err()
}

// parseDotEnvLine splits one line into a key and value, reporting false for a
// blank, a comment, or anything that is not an assignment.
func parseDotEnvLine(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")
	name, raw, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", false
	}
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 {
		if (raw[0] == '"' && raw[len(raw)-1] == '"') || (raw[0] == '\'' && raw[len(raw)-1] == '\'') {
			raw = raw[1 : len(raw)-1]
		}
	}
	return name, raw, true
}
