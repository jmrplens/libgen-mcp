//go:build !unix

// Socket permissions on the platforms that do not have them.

package main

import "os"

// withSocketUmask runs bind unchanged. Windows has no umask, and Go's AF_UNIX
// support there does not create a filesystem object whose mode means anything.
func withSocketUmask(_ os.FileMode, bind func()) { bind() }

// chmodSocket is a no-op. os.Chmod on Windows only toggles the read-only bit, so
// applying a permission mode would report success while enforcing nothing;
// mainWithExit refuses an explicit --http-socket-mode on these platforms rather
// than accepting a value it cannot honor.
func chmodSocket(_ string, _ os.FileMode) error { return nil }

// socketModesEnforced reports whether chmodSocket actually enforces a mode on
// this platform.
const socketModesEnforced = false
