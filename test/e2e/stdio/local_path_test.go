//go:build stdioe2e

// local_path_test.go pins which directories a caller-supplied local path may
// resolve into on stdio, where such paths are honored at all.
//
// The roots are computed from the process: its working directory, its
// os.TempDir, the download directory it was configured with, and the allow-list
// variable it was given. A unit test can only ask the guard about the directory
// the test binary happens to be running in, which is the package directory and
// never the interesting one. The case that matters is a server started in the
// user's home directory, and the only way to produce one is to start a process
// there.
//
// It is not a hypothetical arrangement. Claude Desktop starts its servers in
// "/", other clients start them in the user's home, and neither asks. A home
// directory kept as an implicit root allow-lists ~/.ssh, ~/.aws, the browser
// profiles and this server's own .env — and read's own tool description calls
// the text it returns UNTRUSTED, so a model acting on an instruction embedded
// in a book it just read can name any of them and get the contents back in the
// text field.

package stdioe2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// localSecretFile stands for the credentials a home directory holds.
//
// A readable one on purpose. This server's own dotenv is `.env`, and `read`
// refuses that outright — "unsupported file extension .env and its bytes match
// no supported format" — so a case built on it would assert the containment
// against a file the extractor was going to decline anyway, and the leak check
// beside the refusal would pass whether or not anything was contained. The
// exposure the guard exists for is what this tool CAN return: a text file, a
// PDF, an EPUB. A home directory holds plenty.
const localSecretFile = "credentials.txt"

// readPathCall drives one read naming a local file and returns the text of the
// result plus whether it was reported as a failure.
func readPathCall(t *testing.T, s *session, filePath string) (text string, isError bool) {
	t.Helper()

	arguments, err := json.Marshal(map[string]any{"path": filePath, "max_chars": 500})
	if err != nil {
		t.Fatalf("building the call arguments: %v", err)
	}
	got := s.call(t, request(2, "tools/call", `{"name":"read","arguments":`+string(arguments)+`}`))
	return toolResultText(t, got)
}

// toolResultText pulls the text blocks and the failure flag out of a decoded
// tools/call response.
//
// The text blocks are what a client prints and a model reads, so they are what
// a refusal has to be legible in — and what a leak would appear in. A
// protocol-level error is returned rather than failed on, because a case that
// expects one should say so itself.
func toolResultText(t *testing.T, msg map[string]any) (text string, isError bool) {
	t.Helper()

	encoded, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("re-encoding the response: %v", err)
	}
	var decoded struct {
		Result *struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if unmarshalErr := json.Unmarshal(encoded, &decoded); unmarshalErr != nil {
		t.Fatalf("the response is not a tool result: %v: %s", unmarshalErr, encoded)
	}
	if decoded.Result == nil {
		if decoded.Error != nil {
			return decoded.Error.Message, true
		}
		t.Fatalf("the response carries neither a result nor an error: %s", encoded)
	}
	var b strings.Builder
	for _, block := range decoded.Result.Content {
		b.WriteString(block.Text)
	}
	return b.String(), decoded.Result.IsError
}

// localPathEnv is the environment for a case that chooses where the server
// believes its home and its temporary directory are.
//
// os.TempDir is always a root and t.TempDir hands out subdirectories of it, so
// a home directory left under the real temporary directory would be allow-listed
// for a reason that has nothing to do with the behavior under test. Pointing the
// server's temporary directory somewhere else is what makes the home case
// observable at all. All three spellings are set because os.TempDir reads TMPDIR
// on Unix and TMP or TEMP on Windows.
//
// They go in the environment handed to the child rather than through a
// t.Setenv-style helper: what has to be isolated is the temporary directory of
// the SERVER process, and this test process has no say in that one. A helper
// that set the variables here would change the wrong process and the case would
// assert nothing.
func localPathEnv(t *testing.T, m *mirror, home, tempDir string) map[string]string {
	t.Helper()
	env := baseEnv(t, m)
	env["HOME"] = home
	env["TMPDIR"] = tempDir
	env["TMP"] = tempDir
	env["TEMP"] = tempDir
	// The download directory is an implicit root, and baseEnv points it at a
	// directory of its own under the real temporary directory. Moved under the
	// server's temporary directory here so it is still a legitimate root and
	// still not an ancestor of the home the cases are about.
	env["LIBGEN_MCP_DOWNLOAD_DIR"] = filepath.Join(tempDir, "downloads")
	return env
}

// TestLocalPath_HomeDirectoryIsNotAnImplicitRoot starts the real binary in the
// user's home directory and asserts a path naming a file there is refused,
// while the same containment still accepts the directories it is meant to.
//
// The three rows are one decision seen from three sides, and any one alone
// would pass against a broken implementation. The first is the containment. The
// second is the escape hatch that keeps it a default rather than a policy: an
// operator whose workspace really is their home directory names it and gets it
// back. The third guards against over-correcting, because dropping the working
// directory as a root altogether would satisfy the first two and break every
// ordinary read.
//
// The refusal is checked for the variable that widens the roots as well as for
// the refusal itself. Without that it reads as a bad path, and the person whose
// setup just stopped working has nothing to act on.
func TestLocalPath_HomeDirectoryIsNotAnImplicitRoot(t *testing.T) {
	m := startMirror(t)

	home := t.TempDir()
	// Somewhere the server may legitimately read from, chosen so it is not an
	// ancestor of the home directory: otherwise the temporary root would allow
	// the home file and the first row could never refuse.
	serverTemp := t.TempDir()

	secret := filepath.Join(home, localSecretFile)
	writeLocalFile(t, secret, "LIBGEN_MCP_ANNAS_KEY=the-operators-own-key\n")

	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		t.Fatalf("creating the workspace directory: %v", err)
	}
	inWorkspace := filepath.Join(workspace, "notes.txt")
	writeLocalFile(t, inWorkspace, "an ordinary file in the project being worked on\n")

	tests := []struct {
		name string
		// dir is the working directory the client starts the server in.
		dir string
		// allowlist is what LIBGEN_MCP_ALLOWED_READ_DIRS is set to, if anything.
		allowlist string
		path      string
		// wantRefused says whether the read must be refused.
		wantRefused bool
		// wantText is a fragment of the file, for the rows that must be read.
		// "Not refused" is not the same claim as "read", and only one of them
		// is what an allowed path is owed.
		wantText string
	}{
		{
			name:        "a file in the home directory the server was started in",
			dir:         home,
			path:        secret,
			wantRefused: true,
		},
		{
			name:      "the same file once the operator allow-lists that directory",
			dir:       home,
			allowlist: home,
			path:      secret,
			wantText:  "the-operators-own-key",
		},
		{
			name:     "a file in the workspace the server was started in",
			dir:      workspace,
			path:     inWorkspace,
			wantText: "the project being worked on",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := localPathEnv(t, m, home, serverTemp)
			if tt.allowlist != "" {
				env["LIBGEN_MCP_ALLOWED_READ_DIRS"] = tt.allowlist
			}

			s := startSessionInDir(t, tt.dir, env)
			if got := s.call(t, initializeRequest(1)); got["error"] != nil {
				t.Fatalf("initialize failed: %v", got["error"])
			}
			body, isError := readPathCall(t, s, tt.path)

			if tt.wantRefused {
				assertReadRefused(t, s, body, isError)
				return
			}
			assertReadAccepted(t, body, isError, tt.wantText)
		})
	}
}

// assertReadRefused checks everything a containment refusal owes its caller:
// that it happened, that it says what it was and which variable widens the
// roots, that the file's contents are not in the answer anyway, and that the
// server explained why the working directory stopped being a root.
func assertReadRefused(t *testing.T, s *session, body string, isError bool) {
	t.Helper()

	if !isError {
		t.Fatalf("a file in the home directory was read: %s", body)
	}
	if !strings.Contains(body, "outside the allowed directories") {
		t.Errorf("the refusal does not say the path was outside the allowed roots: %s", body)
	}
	if !strings.Contains(body, "LIBGEN_MCP_ALLOWED_READ_DIRS") {
		t.Errorf("the refusal does not name the variable that widens the roots, so nobody can act on it: %s", body)
	}
	// The refusal and the leak are separate failures: a call reported as an
	// error that still carried the file's bytes would satisfy the checks above
	// and be the whole defect.
	if strings.Contains(body, "the-operators-own-key") {
		t.Errorf("the refused read returned the file's contents anyway: %s", body)
	}
	// The narrowing is silent otherwise, and a working setup that stops working
	// deserves the reason rather than a puzzle.
	s.waitForStderr(t, "working directory is the home directory", 10*time.Second)
}

// assertReadAccepted checks that an allowed path was not merely tolerated but
// actually read.
//
// The contents are the assertion rather than the absence of an error. A guard
// that refused everything and reported it as a successful empty read would pass
// "not an error", and the two rows here exist to prove the containment is a
// default and not a wall.
func assertReadAccepted(t *testing.T, body string, isError bool, wantText string) {
	t.Helper()

	if isError {
		t.Fatalf("an allowed path was refused: %s", body)
	}
	if !strings.Contains(body, wantText) {
		t.Errorf("the allowed path came back without the file's contents (want %q):\n%s", wantText, body)
	}
}

// writeLocalFile writes one fixture file, failing the test rather than the read
// under it if the fixture itself cannot be created.
func writeLocalFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
