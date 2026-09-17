package libgen

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
	"github.com/jmrplens/libgen-mcp/internal/netguard"
)

// TestClientsCarryTheOperatorNamedDestinations pins the wiring this package owes
// the destination guard.
//
// Both clients matter and for the same reason: c.http asks the mirror questions
// and c.dl streams files from it, and each also reaches URLs a third party
// deposited. One boolean could not tell those apart, which is why the policy
// travels per request — but it only travels if New hands the operator-named set
// in, and that is what this asserts.
//
// The suite lifts the private tier for its own loopback fixtures (see TestMain),
// so this test puts it back for the clients it builds. Without that it would
// pass whatever New did, which is no test at all.
func TestClientsCarryTheOperatorNamedDestinations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	restore := netguard.SetAllowPrivateForTest(false)
	defer restore()

	named := New(staticMirrors{srv.URL}, &config.Config{Mirror: srv.URL, Timeout: 5 * time.Second})
	for _, tc := range []struct {
		name   string
		client *http.Client
	}{
		{name: "http", client: named.http},
		{name: "dl", client: named.dl},
	} {
		resp, err := fetchThrough(t, tc.client, srv.URL)
		if err != nil {
			t.Errorf("%s client could not reach the configured mirror: %v", tc.name, err)
			continue
		}
		_ = resp.Body.Close()
	}

	// The same address, from a deployment that configured no mirror at all, is
	// the class the guard exists for and stays refused.
	anonymous := New(staticMirrors{srv.URL}, &config.Config{Timeout: 5 * time.Second})
	resp, err := fetchThrough(t, anonymous.http, srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a private address nobody configured was reached")
	}
	if !errors.Is(err, netguard.ErrBlockedAddress) {
		t.Errorf("err = %v, want netguard.ErrBlockedAddress", err)
	}
}

// fetchThrough issues a context-carrying GET on the given client.
func fetchThrough(t *testing.T, c *http.Client, rawURL string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		t.Fatalf("NewRequestWithContext(%q) error = %v", rawURL, err)
	}
	return c.Do(req)
}
