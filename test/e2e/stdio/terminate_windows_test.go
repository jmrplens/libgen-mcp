//go:build stdioe2e && windows

// terminate_windows_test.go is the termination path a Windows console uses: a
// CTRL_BREAK event to the server's process group, which the Go runtime delivers
// as os.Interrupt — one of the two signals this server stops on. Windows has no
// SIGTERM to send, and TerminateProcess is a kill, which would test nothing
// about shutdown.

package stdioe2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// terminationSignalName names what signalTermination sends, for messages.
const terminationSignalName = "CTRL_BREAK"

// prepareForTermination puts the server in a console process group of its own,
// so a CTRL_BREAK aimed at that group reaches the server and nothing else, the
// test binary included.
func prepareForTermination(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// signalTermination sends CTRL_BREAK to the process group the server was
// started in, whose id is the server's pid.
func signalTermination(p *os.Process) error {
	return windows.GenerateConsoleCtrlEvent(syscall.CTRL_BREAK_EVENT, uint32(p.Pid)) //#nosec G115 -- a pid is a non-negative 32-bit value on Windows
}

// withPlatformHome mirrors the effective HOME into the two variables Windows
// resolves a user's directories through.
//
// USERPROFILE is what os.UserHomeDir reads there, and LocalAppData is what
// os.UserCacheDir reads. Both matter to this server: the mirror cache is written
// under the cache directory, and NewManagerFor fails outright when there is no
// cache directory to resolve — so without this line a Windows session would not
// merely configure a home the server ignores, it would not start at all.
func withPlatformHome(environ []string) []string {
	home := ""
	for _, kv := range environ {
		if value, ok := strings.CutPrefix(kv, "HOME="); ok {
			home = value
		}
	}
	return append(environ,
		"USERPROFILE="+home,
		"LocalAppData="+filepath.Join(home, "AppData", "Local"),
	)
}
