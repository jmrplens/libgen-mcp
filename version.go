package libgenmcp

import (
	_ "embed"
	"strings"
)

// versionFile is the raw contents of VERSION, trailing newline included.
//
//go:embed VERSION
var versionFile string

// Version is the release number this build was compiled from, read from the
// repository's VERSION file. Release ldflags may still override what a command
// reports (see internal/version), which matters when a tag and the file disagree;
// this is the floor, and it is never a guess.
var Version = strings.TrimSpace(versionFile)
