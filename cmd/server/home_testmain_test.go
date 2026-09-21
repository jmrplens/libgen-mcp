package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain gives this package's tests a throwaway home directory and removes it
// when they are done.
//
// These tests drive the binary's real startup path, and one of the things
// startup does is resolve the download directory: with LIBGEN_MCP_DOWNLOAD_DIR
// unset that is <home>/Downloads, and config validates it by creating it and
// writing a probe file inside. Against the developer's own home that is a
// directory the test suite creates on a machine where nobody asked for one, and
// it survives the run; measured on 2026-09-21, it was the only thing the whole
// unit suite still left behind.
//
// Six variables are redirected, because the standard library reads a different
// one per platform and per directory. os.UserHomeDir takes HOME on Unix and
// USERPROFILE on Windows; os.UserCacheDir takes XDG_CACHE_HOME on Linux and
// LocalAppData on Windows; os.UserConfigDir takes XDG_CONFIG_HOME on Linux and
// AppData on Windows. macOS derives both from HOME. Leaving any one of them
// pointing at the real home would hand the leak back by another route, and the
// set is written out rather than reduced to the ones a leak has been measured
// through, because the next one will be measured on whichever platform was left
// out. A test that needs one unset, or set to something else, still says so
// with t.Setenv, which is restored per test.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "libgen-mcp-server-test-home-")
	if err != nil {
		panic(err)
	}
	vars := map[string]string{
		"HOME":            home,
		"USERPROFILE":     home,
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"LocalAppData":    filepath.Join(home, ".cache"),
		"AppData":         filepath.Join(home, ".config"),
	}
	for name, value := range vars {
		if setErr := os.Setenv(name, value); setErr != nil {
			panic(setErr)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
