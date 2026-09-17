//go:build stdioe2e

// env_file_test.go drives the dotenv rules against a real process, which is the
// only place they mean anything: the working directory a dotenv file's authority
// depends on is inherited from whoever started the server, and a test in the same
// process cannot have one it did not already have.

package stdioe2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// awaitLogRecord waits for the JSON log record carrying msg and returns it
// decoded.
//
// Decoded rather than matched as text, because half of what these cases assert
// is a path. slog's JSON handler escapes a backslash as two, so on Windows a
// substring search for `C:\Users\…` never matches the `C:\\Users\\…` that was
// actually written — the test fails on a platform where the server is correct,
// which is worse than not testing it. Comparing the decoded field asks the
// question a log consumer would.
func awaitLogRecord(t *testing.T, s *session, msg string, within time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		text := s.stderrText()
		for line := range strings.SplitSeq(text, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "{") {
				continue
			}
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				continue
			}
			if recorded, _ := record["msg"].(string); strings.Contains(recorded, msg) {
				return record
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no log record carrying %q within %s\nstderr: %s", msg, within, text)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// assertSamePath compares a path the server logged with one this test built.
//
// Both sides are resolved first, because they are two spellings of one file
// rather than one string: on macOS a temporary directory is /var/folders/… here
// and /private/var/folders/… in the process, and on a Windows runner whose temp
// sits under an 8.3 short name one side holds C:\Users\RUNNER~1\… and the other
// the long form. The assertion is about the file an operator can open, not about
// which spelling reached the log.
func assertSamePath(t *testing.T, logged, want, what string) {
	t.Helper()
	resolvedWant, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatalf("resolving %s (%q): %v", what, want, err)
	}
	resolvedLogged, err := filepath.EvalSymlinks(logged)
	if err != nil {
		t.Fatalf("the logged %s %q does not resolve to anything: %v", what, logged, err)
	}
	if resolvedLogged != resolvedWant {
		t.Errorf("the announcement names %s %q (resolves to %q), want %q", what, logged, resolvedLogged, resolvedWant)
	}
}

// writeDotenv writes a dotenv file and returns its path.
func writeDotenv(t *testing.T, path string, lines ...string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// configuredSources asks the running server which download sources it ended up
// with, by reading the download tool's own `source` enum off tools/list.
//
// A setting is only configured if something downstream sees it, and this is the
// one setting a dotenv file can change that the wire reports back. Asserting on
// a log line instead would pass for a server that logged the value and built its
// chain from something else.
func configuredSources(t *testing.T, s *session) []string {
	t.Helper()
	got := s.call(t, request(2, "tools/list", ""))
	if got["error"] != nil {
		t.Fatalf("tools/list failed: %v", got["error"])
	}
	result, _ := got["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	for _, entry := range tools {
		tool, _ := entry.(map[string]any)
		if name, _ := tool["name"].(string); name != "download" {
			continue
		}
		schema, _ := tool["inputSchema"].(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		source, _ := properties["source"].(map[string]any)
		values, _ := source["enum"].([]any)
		names := make([]string, 0, len(values))
		for _, v := range values {
			if name, ok := v.(string); ok {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			t.Fatalf("the download tool advertises no source enum: %v", source)
		}
		return names
	}
	t.Fatal("tools/list did not carry the download tool")
	return nil
}

// TestEnvFile_WorkingDirectoryDotenvIsIgnoredAndAnnounced is the security claim,
// against a process whose working directory is a workspace it did not choose.
//
// The file names one source, which would be visible in the download tool's enum
// if it were loaded. That it is not, and that the server says so, are separate
// assertions: the refusal is the fix and the announcement is what keeps it from
// being a mystery.
func TestEnvFile_WorkingDirectoryDotenvIsIgnoredAndAnnounced(t *testing.T) {
	workspace := t.TempDir()
	writeDotenv(t, filepath.Join(workspace, ".env"), "LIBGEN_MCP_SOURCES=libgen")

	env := baseEnv(t, startMirror(t))
	delete(env, "LIBGEN_MCP_SOURCES")
	s := startSessionInDir(t, workspace, env)
	if got := s.call(t, initializeRequest(1)); got["error"] != nil {
		t.Fatalf("initialize failed: %v", got["error"])
	}

	if sources := configuredSources(t, s); len(sources) == 1 {
		t.Errorf("the working-directory .env configured the source chain (%v); it must reach nothing", sources)
	}

	record := awaitLogRecord(t, s, "ignoring the .env file in the working directory", 10*time.Second)
	logged, _ := record["path"].(string)
	assertSamePath(t, logged, filepath.Join(workspace, ".env"), "the ignored file")
	keys, _ := record["keys"].([]any)
	if len(keys) != 1 || keys[0] != "LIBGEN_MCP_SOURCES" {
		t.Errorf("the announcement names keys %v, want the one the file would have set", keys)
	}
}

// TestEnvFile_NamedFileIsLoadedAndTheProcessEnvironmentStillWins pins the two
// ends of the precedence against the binary.
//
// The named file is the convenience the working-directory refusal takes away,
// and it has to actually work or the refusal is just a removal. The process
// environment winning is what keeps an MCP client's own configuration
// authoritative over a file on the machine.
func TestEnvFile_NamedFileIsLoadedAndTheProcessEnvironmentStillWins(t *testing.T) {
	named := writeDotenv(t, filepath.Join(t.TempDir(), "libgen.env"),
		"LIBGEN_MCP_SOURCES=libgen", "LIBGEN_MCP_LOG_LEVEL=error")

	env := baseEnv(t, startMirror(t))
	delete(env, "LIBGEN_MCP_SOURCES")
	env["LIBGEN_MCP_ENV_FILE"] = named
	// The file says error and the client says info, for the same setting. Only
	// one of them can be in force.
	env["LIBGEN_MCP_LOG_LEVEL"] = "info"
	s := startSessionInDir(t, t.TempDir(), env)
	if got := s.call(t, initializeRequest(1)); got["error"] != nil {
		t.Fatalf("initialize failed: %v", got["error"])
	}

	if sources := configuredSources(t, s); len(sources) != 1 || sources[0] != "libgen" {
		t.Errorf("source enum = %v, want just libgen from the named file", sources)
	}
	record := awaitLogRecord(t, s, "loading configuration from the env file named by", 10*time.Second)
	logged, _ := record["path"].(string)
	assertSamePath(t, logged, named, "the loaded file")

	// The level contest, decided by a record only one side admits. withRecovery
	// writes "tool call completed" at INFO for every call it meters, so it
	// appears if the client's info won and is suppressed if the file's error did.
	if got := s.call(t, request(3, "tools/call", searchCall)); got["error"] != nil {
		t.Fatalf("the search call failed: %v", got["error"])
	}
	s.waitForStderr(t, "tool call completed", 10*time.Second)
}

// TestEnvFile_FlagBeatsTheVariableAndIsAppliedBeforeTheLoaderRuns is the ordering
// this flag lives or dies by.
//
// LIBGEN_MCP_ENV_FILE is resolved once per process, before any file is read, so
// --env-file has to write it before that resolution happens. A flag applied one
// line too late parses, writes, and does nothing whatsoever — and the only
// evidence is a setting that did not take.
func TestEnvFile_FlagBeatsTheVariableAndIsAppliedBeforeTheLoaderRuns(t *testing.T) {
	genuine, impostor := startMirror(t), startMirror(t)
	fromFlag := writeDotenv(t, filepath.Join(t.TempDir(), "flag.env"), "LIBGEN_MIRROR="+genuine.url)
	fromVar := writeDotenv(t, filepath.Join(t.TempDir(), "var.env"), "LIBGEN_MIRROR="+impostor.url)

	env := baseEnv(t, genuine)
	// The files supply the mirror, so the process environment must not: it wins
	// over both, and a test where neither file could take effect would pass
	// whatever the flag did. The harness seeds its mirror cache from this value,
	// so with it gone the session makes one doomed discovery request — which the
	// dead proxy in baseEnv refuses in milliseconds, leaving the module's
	// isolation intact.
	delete(env, "LIBGEN_MIRROR")
	env["LIBGEN_MCP_ENV_FILE"] = fromVar

	s := startSessionWithArgs(t, env, "--env-file", fromFlag)
	if got := s.call(t, initializeRequest(1)); got["error"] != nil {
		t.Fatalf("initialize failed: %v", got["error"])
	}
	if got := s.call(t, request(2, "tools/call", searchCall)); got["error"] != nil {
		t.Fatalf("the search call failed: %v", got["error"])
	}

	if genuine.requestCount() == 0 {
		t.Error("the mirror the flag's file names was never contacted, so --env-file configured nothing")
	}
	// The stronger half: not merely that the flag's file won, but that the
	// variable's was never consulted at all. A server that read both and
	// preferred one would satisfy the assertion above and still be wrong.
	if got := impostor.requestCount(); got != 0 {
		t.Errorf("the mirror named by the variable's file received %d request(s) while the flag named another", got)
	}
}

// TestEnvFile_HomeFileConfiguresTheServer covers the file somebody deliberately
// put where the server looks, which is the whole of what the refusal leaves.
func TestEnvFile_HomeFileConfiguresTheServer(t *testing.T) {
	home := t.TempDir()
	writeDotenv(t, filepath.Join(home, ".libgen-mcp.env"), "LIBGEN_MCP_SOURCES=libgen")

	env := baseEnv(t, startMirror(t))
	delete(env, "LIBGEN_MCP_SOURCES")
	env["HOME"] = home
	s := startSessionInDir(t, t.TempDir(), env)
	if got := s.call(t, initializeRequest(1)); got["error"] != nil {
		t.Fatalf("initialize failed: %v", got["error"])
	}

	if sources := configuredSources(t, s); len(sources) != 1 || sources[0] != "libgen" {
		t.Errorf("source enum = %v, want just libgen from ~/.libgen-mcp.env", sources)
	}
}
