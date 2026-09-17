// hostlocal_test.go covers the one question the private-address hatch is paired
// with: whether the listener this server is about to bind can be reached from
// another machine.
//
// The predicate is asserted apart from the refusal that reads it, because the
// two fail differently. A predicate that answers wrong about one address shape
// leaves a hatch open on a listener the whole network can reach; a refusal wired
// to the wrong value — a flag instead of the address actually bound — is right
// about every shape and never consulted.

package main

import (
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

// stubListenResolver answers for one hostname and fails for anything else,
// which is what a resolver does when it has nothing to say.
func stubListenResolver(t *testing.T, answers map[string][]string) {
	t.Helper()
	previous := lookupListenHost
	t.Cleanup(func() { lookupListenHost = previous })
	lookupListenHost = func(host string) ([]net.IP, error) {
		raw, ok := answers[host]
		if !ok {
			return nil, errors.New("no answer for " + host)
		}
		ips := make([]net.IP, 0, len(raw))
		for _, r := range raw {
			ips = append(ips, net.ParseIP(r))
		}
		return ips, nil
	}
}

// TestListenerIsHostLocal covers every shape --http accepts.
//
// The wildcard rows are the ones this exists for: `:8080` and `0.0.0.0:8080` are
// what every container recipe in the docs uses, and a check that read them as
// "no host was named, so this must be local" would accept the hatch on a
// listener the whole network can reach.
func TestListenerIsHostLocal(t *testing.T) {
	stubListenResolver(t, map[string][]string{
		"localhost":        {"127.0.0.1", "::1"},
		"moved.example":    {"192.168.1.50"},
		"split.example":    {"127.0.0.1", "10.0.0.7"},
		"answerless.local": {},
	})

	tests := []struct {
		name  string
		addr  string
		local bool
	}{
		{"loopback literal", "127.0.0.1:8080", true},
		{"loopback anywhere in 127/8", "127.9.9.9:8080", true},
		{"loopback v6", "[::1]:8080", true},
		{"a unix socket path", "/run/mcp/libgen.sock", true},
		{"a relative socket path", "./mcp.sock", true},
		// A name is judged by what it resolves to. "localhost" is loopback by
		// convention, not by rule.
		{"a name resolving only to loopback", "localhost:8080", true},
		{"a name pointed elsewhere", "moved.example:8080", false},
		// One public address among the answers is enough: the listener binds all
		// of them, so the network reaches it through that one.
		{"a name resolving to a mix", "split.example:8080", false},
		{"a name that resolves to nothing", "answerless.local:8080", false},
		{"a name nobody can resolve", "unknown.invalid:8080", false},
		// The shapes the container recipes use.
		{"a wildcard bind", ":8080", false},
		{"an explicit wildcard", "0.0.0.0:8080", false},
		{"an IPv6 wildcard", "[::]:8080", false},
		{"a public literal", "203.0.113.7:8080", false},
		// A LAN address binds a listener the LAN can reach, which is exactly the
		// network the hatch would open onto.
		{"a private literal", "192.168.1.50:8080", false},
		// Not an address at all: nothing can be shown to be local about it.
		{"an unparseable address", "not-an-address", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := listenerIsHostLocal(tt.addr); got != tt.local {
				t.Errorf("listenerIsHostLocal(%q) = %v, want %v", tt.addr, got, tt.local)
			}
		})
	}
}

// TestRefusePrivateHatchOnOpenListener pins the pairing itself: the hatch and
// the listener are one decision, and neither value decides alone.
//
// The flag off must never refuse anything, or a wildcard deployment that never
// asked for the hatch would stop starting. The flag on with no listener at all
// must not refuse either: a stdio server's caller is somebody with an account on
// this machine, which is the case the hatch was written for.
func TestRefusePrivateHatchOnOpenListener(t *testing.T) {
	stubListenResolver(t, map[string][]string{"localhost": {"127.0.0.1"}})
	socket := filepath.Join(t.TempDir(), "mcp.sock")

	tests := []struct {
		name    string
		addr    string
		hatch   bool
		refused bool
	}{
		{name: "the hatch on a wildcard bind", addr: ":8080", hatch: true, refused: true},
		{name: "the hatch on an explicit wildcard", addr: "0.0.0.0:8080", hatch: true, refused: true},
		{name: "the hatch on a public literal", addr: "203.0.113.7:8080", hatch: true, refused: true},
		{name: "the hatch on loopback", addr: "127.0.0.1:8080", hatch: true},
		{name: "the hatch on a loopback name", addr: "localhost:8080", hatch: true},
		{name: "the hatch on a unix socket", addr: socket, hatch: true},
		{name: "the hatch on stdio", addr: "", hatch: true},
		// Without the hatch there is nothing to pair, and every listener shape is
		// somebody's ordinary deployment.
		{name: "no hatch on a wildcard bind", addr: ":8080"},
		{name: "no hatch on stdio", addr: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := refusePrivateHatchOnOpenListener(
				listenSpec{addr: tt.addr},
				&config.Config{AllowPrivateAddresses: tt.hatch},
			)
			if !tt.refused {
				if err != nil {
					t.Fatalf("err = %v, want the deployment permitted", err)
				}
				return
			}
			if err == nil {
				t.Fatal("err = nil, want the hatch refused on a listener other machines can reach")
			}
			// The message has to be actionable from the console it appears on:
			// the variable to unset, and the address that was judged.
			for _, want := range []string{config.EnvName("ALLOW_PRIVATE_ADDRESSES"), tt.addr} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %q, want it to name %q", err, want)
				}
			}
		})
	}
}

// TestRefusalReadsTheBoundAddressNotTheFlag is the pin for where the check sits.
//
// It is written against the seam a later transport default would move: the
// refusal takes the resolved listenSpec, so an address that reached it from
// anywhere other than --http is judged the same way. A check that read the flag
// string instead would see "" for such a deployment, conclude stdio, and leave
// the hatch open on the wildcard bind it was about to make — which is the one
// case this whole step exists to refuse.
func TestRefusalReadsTheBoundAddressNotTheFlag(t *testing.T) {
	cfg := &config.Config{AllowPrivateAddresses: true}
	if err := refusePrivateHatchOnOpenListener(listenSpec{addr: "0.0.0.0:8080"}, cfg); err == nil {
		t.Fatal("a wildcard address in the listen spec was accepted; the check is not reading what will be bound")
	}
	if err := refusePrivateHatchOnOpenListener(listenSpec{}, cfg); err != nil {
		t.Errorf("err = %v, want an empty spec treated as stdio", err)
	}
}
