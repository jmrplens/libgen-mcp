package main

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	buildversion "github.com/jmrplens/libgen-mcp/internal/version"
)

// decodeDiscoveryCard builds the SEP-2127 card and returns it as a map.
func decodeDiscoveryCard(t *testing.T, publicURL string, stateless bool) map[string]any {
	t.Helper()

	raw, err := buildDiscoveryCard(publicURL, stateless)
	if err != nil {
		t.Fatalf("buildDiscoveryCard() error = %v", err)
	}
	var card map[string]any
	if unmarshalErr := json.Unmarshal(raw, &card); unmarshalErr != nil {
		t.Fatalf("the card is not JSON: %v (%s)", unmarshalErr, raw)
	}
	return card
}

// TestDiscoveryCardCarriesIdentityAndNothingElse is the whole shape, and the
// absences are the assertion.
//
// SEP-2127 cards deliberately carry no primitives: what a server exposes can
// vary by session and configuration, so a card that listed tools would be
// answering a question only a live listing can answer. The enumerating document
// at the legacy path is where a scanner gets that, and nothing here should be
// enriched to help it.
func TestDiscoveryCardCarriesIdentityAndNothingElse(t *testing.T) {
	card := decodeDiscoveryCard(t, "", true)

	for _, key := range []string{"$schema", "name", "version", "description", "title", "websiteUrl", "repository"} {
		t.Run(key, func(t *testing.T) {
			if _, ok := card[key]; !ok {
				t.Errorf("the card carries no %q", key)
			}
		})
	}
	for _, key := range []string{"tools", "prompts", "resources", "resourceTemplates", "capabilities", "authentication"} {
		t.Run(key, func(t *testing.T) {
			if _, ok := card[key]; ok {
				t.Errorf("the card carries %q; a SEP-2127 card carries no primitives and no capability block", key)
			}
		})
	}
	if card["version"] != buildversion.Current() {
		t.Errorf("version = %v, want the running binary's %q", card["version"], buildversion.Current())
	}
	// The schema URL is pinned by the wire format's own pattern, so it is not
	// ours to vary even while it answers 404 upstream.
	if card["$schema"] != discoveryCardSchema {
		t.Errorf("$schema = %v, want the exact string the format pins", card["$schema"])
	}
}

// TestDiscoveryCardAgreesWithServerJSON keeps the two identities this project
// publishes from drifting.
//
// The registry knows this server by the reverse-DNS name in server.json; the
// card's own pattern requires a namespace, which the handshake's plain
// "libgen-mcp" does not have. Two files stating an identity is one file too
// many unless something compares them.
func TestDiscoveryCardAgreesWithServerJSON(t *testing.T) {
	raw, err := os.ReadFile("../../server.json")
	if err != nil {
		t.Fatalf("reading server.json: %v", err)
	}
	var registry struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		WebsiteURL  string `json:"websiteUrl"`
		Repository  struct {
			URL    string `json:"url"`
			Source string `json:"source"`
		} `json:"repository"`
	}
	if unmarshalErr := json.Unmarshal(raw, &registry); unmarshalErr != nil {
		t.Fatalf("server.json is not JSON: %v", unmarshalErr)
	}

	card := decodeDiscoveryCard(t, "", true)
	repo, _ := card["repository"].(map[string]any)

	for _, tc := range []struct {
		field     string
		got, want any
	}{
		{field: "name", got: card["name"], want: registry.Name},
		{field: "description", got: card["description"], want: registry.Description},
		{field: "websiteUrl", got: card["websiteUrl"], want: registry.WebsiteURL},
		{field: "repository.url", got: repo["url"], want: registry.Repository.URL},
		{field: "repository.source", got: repo["source"], want: registry.Repository.Source},
	} {
		t.Run(tc.field, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s = %v, want server.json's %v", tc.field, tc.got, tc.want)
			}
		})
	}
}

// TestDiscoveryCardRemotesFollowThePublicURL is the connection block, in both
// states.
//
// A card describes a remote server, and the only address this process knows to
// be reachable from outside is the one --public-url states: a listen address is
// frequently loopback or a socket behind a proxy, and publishing it would send a
// client somewhere it cannot go. No public URL therefore means no `remotes` at
// all, which is true rather than misleading.
func TestDiscoveryCardRemotesFollowThePublicURL(t *testing.T) {
	t.Run("absent without one", func(t *testing.T) {
		if _, ok := decodeDiscoveryCard(t, "", true)["remotes"]; ok {
			t.Error("the card advertises a remote although no --public-url was given")
		}
	})

	t.Run("present with one", func(t *testing.T) {
		const url = "https://mcp.example.org/libgen"
		card := decodeDiscoveryCard(t, url, true)

		remotes, ok := card["remotes"].([]any)
		if !ok || len(remotes) != 1 {
			t.Fatalf("remotes = %v, want exactly one entry", card["remotes"])
		}
		remote, ok := remotes[0].(map[string]any)
		if !ok {
			t.Fatalf("the remote is %T, want an object", remotes[0])
		}
		if remote["url"] != url {
			t.Errorf("remote url = %v, want the --public-url value", remote["url"])
		}
		if remote["type"] != "streamable-http" {
			t.Errorf("remote type = %v, want streamable-http", remote["type"])
		}
		// No credential block, and the absence is the statement: every tool on
		// this server works keyless, so a client that sent one would be sending
		// it for nothing.
		if _, present := remote["headers"]; present {
			t.Error("the remote describes a credential header; this server takes none")
		}
	})
}

// TestDiscoveryCardVersionsFollowStatelessness is the consistency the extension
// asks for in writing: a card "SHOULD accurately reflect the server's runtime
// behavior", and the versions it declares "SHOULD NOT contradict the equivalent
// values" a client observes once connected.
//
// A stateful deployment does not serve 2026-07-28 and the version guard answers
// it 400, so a card advertising it would hand a client the one answer that
// cannot work. The list is read from the same helper that guard refuses with,
// rather than copied, so the two cannot disagree by drifting.
func TestDiscoveryCardVersionsFollowStatelessness(t *testing.T) {
	for _, stateless := range []bool{true, false} {
		t.Run("stateless="+boolText(stateless), func(t *testing.T) {
			card := decodeDiscoveryCard(t, "https://mcp.example.org/libgen", stateless)
			remotes, _ := card["remotes"].([]any)
			remote, _ := remotes[0].(map[string]any)

			declared, ok := remote["supportedProtocolVersions"].([]any)
			if !ok || len(declared) == 0 {
				t.Fatalf("supportedProtocolVersions = %v, want the list this deployment negotiates", remote["supportedProtocolVersions"])
			}
			got := make([]string, 0, len(declared))
			for _, v := range declared {
				s, _ := v.(string)
				got = append(got, s)
			}
			if want := negotiableProtocolVersions(stateless); !slices.Equal(got, want) {
				t.Errorf("supportedProtocolVersions = %q, want %q", got, want)
			}
			if !stateless && slices.Contains(got, protocolVersionStatelessOnly) {
				t.Errorf("a stateful card advertises %q, which the version guard refuses", protocolVersionStatelessOnly)
			}
		})
	}
}

// boolText renders a boolean for a subtest name.
func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestDiscoveryCardIsIndentedAndStable keeps the document readable by whoever
// curls it, and byte-identical between two builds of the same version — which is
// what lets two replicas publish one validator.
func TestDiscoveryCardIsIndentedAndStable(t *testing.T) {
	first, err := buildDiscoveryCard("https://mcp.example.org/libgen", true)
	if err != nil {
		t.Fatalf("buildDiscoveryCard() error = %v", err)
	}
	second, err := buildDiscoveryCard("https://mcp.example.org/libgen", true)
	if err != nil {
		t.Fatalf("buildDiscoveryCard() error = %v", err)
	}
	if string(first) != string(second) {
		t.Error("two renders of the same configuration differ, so two replicas would publish two validators")
	}
	if !strings.Contains(string(first), "\n  ") {
		t.Error("the card is not indented; it is read by people with curl as often as by scanners")
	}
}
