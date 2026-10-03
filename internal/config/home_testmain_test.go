package config

import (
	"fmt"
	"os"
	"testing"
)

// TestMain gives this package's tests a home directory of their own.
//
// [Load] reads ~/.libgen-mcp.env before anything else, and a blank variable no
// longer shields a setting from it: a case that clears a variable with
// t.Setenv(name, "") to ask for the default would otherwise take the value a
// developer keeps in their real home file, and fail on their machine and on
// nobody else's. Both variables, since os.UserHomeDir reads HOME on unix and
// USERPROFILE on Windows. A case that needs a home file writes its own, through
// resetEnvFileState.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "libgen-mcp-config-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "creating a home directory for the tests:", err)
		os.Exit(1)
	}
	if err = os.Setenv("HOME", home); err != nil {
		fmt.Fprintln(os.Stderr, "setting HOME:", err)
		os.Exit(1)
	}
	if err = os.Setenv("USERPROFILE", home); err != nil {
		fmt.Fprintln(os.Stderr, "setting USERPROFILE:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
