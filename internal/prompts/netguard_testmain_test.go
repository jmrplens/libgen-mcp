package prompts

import (
	"os"
	"testing"

	"github.com/jmrplens/libgen-mcp/internal/netguard"
)

// TestMain permits private destinations for this package's tests.
//
// The fixtures here are served by httptest servers on loopback, which
// internal/netguard refuses by default so that a URL supplied by a third party
// cannot aim this server at the operator's own network. The policy itself is
// exercised against real dials in internal/netguard.
func TestMain(m *testing.M) {
	restore := netguard.SetAllowPrivateForTest(true)
	code := m.Run()
	restore()
	os.Exit(code)
}
