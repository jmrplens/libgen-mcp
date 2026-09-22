// session_timeout.go decides what --session-timeout means in each transport
// mode, and refuses the one combination where it means nothing.
//
// A stateful session is the only thing here that outlives the request that
// created it. Under the default stateless transport a POST's session ends with
// its own response, so there is no idle session to close and no timeout to
// apply — which makes an operator who set one no better off than one who did
// not, with nothing in the log to tell the two apart.

package main

import (
	"fmt"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

// idleTimeouts is the pair of settings that decide what this deployment closes
// when it has gone quiet, as the flags delivered them.
//
// They travel together because they are validated together and for one reason:
// both are refusals, and a caller that checked one and forgot the other would
// start a server with a setting that is not in force.
type idleTimeouts struct {
	// session is --session-timeout, and sessionExplicit whether the operator
	// asked for it — by flag or by variable, which the overlay makes the same
	// question.
	session         time.Duration
	sessionExplicit bool
	// httpIdle is --http-idle-timeout.
	httpIdle time.Duration
	// stateless is the resolved transport mode, which decides whether sessions
	// exist to be closed at all.
	stateless bool
}

// checkIdleTimeouts reports why this pair cannot be served, or nil when it can.
func checkIdleTimeouts(t idleTimeouts) error {
	if err := checkSessionTimeout(t.session, t.stateless, t.sessionExplicit); err != nil {
		return err
	}
	if t.httpIdle < 0 {
		return fmt.Errorf("--http-idle-timeout %s must not be negative; 0 disables idle closure", t.httpIdle)
	}
	return nil
}

// checkSessionTimeout reports why the configured session timeout cannot be
// served, or nil when it can.
//
// Two refusals, and they are different failures. Out of range is a value nobody
// can mean: negative is not a duration and past maxSessionTimeout the setting
// has stopped reclaiming anything a deployment outlives. Set under the stateless
// transport is a value that parses and does nothing, which is the shape this
// server refuses everywhere else it appears — the rate limit on a listener that
// cannot tell two callers apart, the socket mode on a platform with no file
// modes. An operator who asked for a bound deserves to be told it cannot do what
// they think it does.
//
// explicit is what separates the two: the default is carried by every
// deployment, stateless ones included, so refusing it unasked would refuse the
// shipped configuration.
func checkSessionTimeout(timeout time.Duration, stateless, explicit bool) error {
	if timeout < 0 || timeout > maxSessionTimeout {
		return fmt.Errorf("--session-timeout %s must be between 0 and %s", timeout, maxSessionTimeout)
	}
	if explicit && stateless {
		return fmt.Errorf(
			"--session-timeout %s cannot apply under the default stateless transport, where each POST's session ends with its response: pass --stateless=false to keep sessions, or drop --session-timeout (also %s)",
			timeout, config.EnvName("SESSION_TIMEOUT"),
		)
	}
	return nil
}

// sessionTimeoutFor resolves the value the transport is given.
//
// Zero in stateless mode is the mode's own answer rather than a setting
// discarded: [checkSessionTimeout] has already refused an operator who asked for
// anything else, so what reaches here is the default nobody typed.
func sessionTimeoutFor(timeout time.Duration, stateless bool) time.Duration {
	if stateless {
		return 0
	}
	return timeout
}
