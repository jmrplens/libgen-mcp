package tools

import (
	"os"
	"testing"

	"github.com/jmrplens/libgen-mcp/internal/netguard"
	"github.com/jmrplens/libgen-mcp/internal/pathguard"
)

// TestMain permits private destinations for this package's tests.
//
// Every fixture here is served by an httptest server, which listens on loopback —
// the address family internal/netguard refuses, because a third party able to
// place a URL in an open-access index must not be able to aim this server at the
// operator's own network. Under the real policy these tests would spend their
// retry schedules failing to dial their own fixtures.
//
// Flipping it once per binary keeps the allowance out of the fifty-odd places a
// test builds a Config, and means a test added later inherits it rather than
// having to know the policy exists. The policy itself is exercised against real
// dials in internal/netguard.
func TestMain(m *testing.M) {
	restore := netguard.SetAllowPrivateForTest(true)
	// Caller-supplied local paths are refused until a deployment says otherwise,
	// and the tests here are a local deployment. Register sets this for real; the
	// many tests that call a read branch directly never reach it, and without this
	// they would all fail closed rather than exercise what they are about.
	//
	// It is only half of what a local path needs: the containment roots still
	// apply, so a test reading a fixture outside its own working directory names
	// that directory in the config it passes. See readTestCfg.
	pathguard.SetLocalAccess(true)
	code := m.Run()
	restore()
	os.Exit(code)
}
