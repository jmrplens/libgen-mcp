package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jmrplens/libgen-mcp/v2/internal/config"
)

// probeHealth drives the /health handler and returns the recorder and the
// decoded body.
func probeHealth(t *testing.T, digest string, draining *atomic.Bool) (*httptest.ResponseRecorder, healthResponse) {
	t.Helper()

	rec := httptest.NewRecorder()
	healthHandler(digest, draining).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil))

	var body healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the health body is not JSON: %v (%q)", err, rec.Body.String())
	}
	return rec, body
}

// TestHealthFlipsToDrainingWithItsStatus covers the pair a balancer reads: the
// HTTP status and the body have to carry the same verdict, or a probe that looks
// at one of them keeps sending work to an instance that is going away.
func TestHealthFlipsToDrainingWithItsStatus(t *testing.T) {
	var draining atomic.Bool

	rec, body := probeHealth(t, "abc123", &draining)
	if rec.Code != http.StatusOK || body.Status != healthStatusOK {
		t.Fatalf("a serving instance answered %d/%q", rec.Code, body.Status)
	}
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Errorf("a serving probe carries Cache-Control %q; only the flip needs one", got)
	}

	draining.Store(true)

	rec, body = probeHealth(t, "abc123", &draining)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d once draining", rec.Code, http.StatusServiceUnavailable)
	}
	if body.Status != healthStatusDraining {
		t.Errorf("body status = %q, want %q", body.Status, healthStatusDraining)
	}
	// A balancer must not serve the last 200 out of a cache across the flip;
	// being noticed is the whole point of flipping.
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q on the draining answer, want no-store", got)
	}
}

// TestHealthCarriesTheBuildAndDigest pins the two fields a fleet is compared on.
func TestHealthCarriesTheBuildAndDigest(t *testing.T) {
	_, body := probeHealth(t, "0123456789ab", nil)

	if body.Build == "" {
		t.Error("build is empty; it is the one string a display wants")
	}
	if body.ConfigDigest != "0123456789ab" {
		t.Errorf("config_digest = %q, want the digest the listener was built with", body.ConfigDigest)
	}
	if body.Version == "" || body.StartedAt == "" {
		t.Error("the fields that were already there went missing")
	}
}

// TestHealthOmitsAnEmptyDigest keeps a stdio-shaped or unconfigured listener from
// publishing an empty string that reads like a digest.
func TestHealthOmitsAnEmptyDigest(t *testing.T) {
	rec, _ := probeHealth(t, "", nil)
	if strings.Contains(rec.Body.String(), "config_digest") {
		t.Errorf("the body carries a config_digest member with nothing in it: %q", rec.Body.String())
	}
}

// TestAnnounceDrainingFlipsBeforeItSleeps is the ordering the delay exists for.
//
// Flip first, then hold the listener open: the delay is there so a balancer sees
// the 503 and stops sending work *before* the listener goes. Sleeping first
// would hold a serving instance open for the delay and then close it with no
// warning at all, which is the behavior without the flag.
func TestAnnounceDrainingFlipsBeforeItSleeps(t *testing.T) {
	var draining atomic.Bool

	flipped := make(chan bool, 1)
	go func() {
		// Sampled while announceDraining is inside its sleep.
		time.Sleep(20 * time.Millisecond)
		flipped <- draining.Load()
	}()

	start := time.Now()
	announceDraining(t.Context(), &draining, 100*time.Millisecond)
	elapsed := time.Since(start)

	if !<-flipped {
		t.Error("the flag was still false while the delay was being waited out, so the delay announced nothing")
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("announceDraining returned after %v, want it to hold the listener for the whole delay", elapsed)
	}
}

// TestAnnounceDrainingWithNoDelayDoesNotWait covers the default: closing at once
// is what a deployment with nothing in front wants, and a delay it never asked
// for would be a shutdown that appears to hang.
func TestAnnounceDrainingWithNoDelayDoesNotWait(t *testing.T) {
	var draining atomic.Bool

	start := time.Now()
	announceDraining(t.Context(), &draining, 0)

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("announceDraining waited %v with no delay configured", elapsed)
	}
	if !draining.Load() {
		t.Error("the flag was not set, so /health keeps answering 200 while the listener closes")
	}
}

// TestShutdownBudgetTakesTheTighterBound is what makes raising the budget safe.
//
// A caller with a deadline of its own has already been told how long it has, by
// a supervisor or by a test. Spending longer on the graceful phase means the
// process is killed mid-drain instead of closing its listener, so the budget is
// a ceiling and never a floor.
func TestShutdownBudgetTakesTheTighterBound(t *testing.T) {
	t.Run("no deadline uses the whole budget", func(t *testing.T) {
		if got := shutdownBudget(t.Context()); got != httpShutdownTimeout {
			t.Errorf("budget = %v, want %v", got, httpShutdownTimeout)
		}
	})

	t.Run("a tighter deadline wins", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		got := shutdownBudget(ctx)
		if got > 2*time.Second {
			t.Errorf("budget = %v, want no more than the caller's own 2s", got)
		}
		if got <= 0 {
			t.Errorf("budget = %v, want something for Shutdown to work with", got)
		}
	})

	t.Run("a looser deadline does not raise it", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()
		if got := shutdownBudget(ctx); got != httpShutdownTimeout {
			t.Errorf("budget = %v, want the server's own %v", got, httpShutdownTimeout)
		}
	})

	t.Run("an expired deadline still leaves one pass", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
		defer cancel()
		time.Sleep(time.Millisecond)
		if got := shutdownBudget(ctx); got <= 0 {
			t.Errorf("budget = %v; Shutdown gets no pass at the idle connections at all", got)
		}
	})
}

// TestBuildIdentifierReadsBackAsALabel covers the string a display shows.
//
// The pseudo-version case is the one worth the code: the toolchain records the
// patch that does not exist yet, so the release such a build is closest to is
// the one before it.
func TestBuildIdentifierReadsBackAsALabel(t *testing.T) {
	cases := []struct {
		name    string
		version string
		commit  string
		want    string
	}{
		{name: "a release build", version: "1.7.2", commit: "404e3671234", want: "1.7.2+404e367"},
		{name: "no commit recorded", version: "1.7.2", commit: "none", want: "1.7.2"},
		{name: "an empty commit", version: "1.7.2", commit: "", want: "1.7.2"},
		{name: "a short commit is not truncated", version: "1.7.2", commit: "abc12", want: "1.7.2+abc12"},
		{
			name:    "a pseudo-version reports the release before it",
			version: "1.7.3-0.20260903061404-6e6ff5beb20e",
			commit:  "",
			want:    "1.7.2+6e6ff5b",
		},
		{
			name:    "a dirty pseudo-version says so",
			version: "1.7.3-0.20260903061404-6e6ff5beb20e+dirty",
			commit:  "",
			want:    "1.7.2+6e6ff5b.dirty",
		},
		{
			name:    "a stamped commit wins over the one in the pseudo-version",
			version: "1.7.3-0.20260903061404-6e6ff5beb20e",
			commit:  "404e3671234",
			want:    "1.7.2+404e367",
		},
		{
			name:    "a patch of zero has nothing below it",
			version: "2.0.0-0.20260903061404-6e6ff5beb20e",
			commit:  "",
			want:    "2.0.0+6e6ff5b",
		},
		{name: "a dirty release", version: "1.7.2+dirty", commit: "404e3671234", want: "1.7.2+404e367.dirty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildIdentifier(tc.version, tc.commit); got != tc.want {
				t.Errorf("buildIdentifier(%q, %q) = %q, want %q", tc.version, tc.commit, got, tc.want)
			}
		})
	}
}

// digestConfig is a configuration with every field the digest reads set to
// something distinguishable.
func digestConfig() *config.Config {
	fetch := true
	return &config.Config{
		Sources:          []string{"libgen", "annas", "unpaywall"},
		UnpaywallEmail:   "ops@example.org",
		ExtraSources:     config.ExtraSourcesAuto,
		ServerFetch:      &fetch,
		RemoteDownloads:  false,
		EnrichEnabled:    true,
		ConfirmDownloads: false,
	}
}

// TestConfigDigestIsOrderFreeOverSources pins the property two replicas depend
// on: the same sources listed in a different order are the same configuration,
// so a digest that disagreed would report a fleet as mismatched forever.
func TestConfigDigestIsOrderFreeOverSources(t *testing.T) {
	first := digestConfig()
	second := digestConfig()
	slices.Reverse(second.Sources)

	if configDigest(first, "/", true) != configDigest(second, "/", true) {
		t.Error("two configurations listing the same sources in a different order produced different digests")
	}
}

// TestConfigDigestChangesWithEverySettingItCovers is the other half. Each of
// these decides what a client sees, so two replicas differing in one of them
// serve different things and the digest is how that is noticed.
func TestConfigDigestChangesWithEverySettingItCovers(t *testing.T) {
	base := configDigest(digestConfig(), "/", true)

	off := false
	cases := []struct {
		name     string
		mutate   func(*config.Config)
		basePath string
		// stateless defaults to true; a case that is about it says so.
		stateless *bool
	}{
		{name: "server_fetch", mutate: func(c *config.Config) { c.ServerFetch = &off }},
		{name: "server_fetch unset", mutate: func(c *config.Config) { c.ServerFetch = nil }},
		{name: "remote_downloads", mutate: func(c *config.Config) { c.RemoteDownloads = true }},
		{name: "enrich", mutate: func(c *config.Config) { c.EnrichEnabled = false }},
		{name: "confirm_downloads", mutate: func(c *config.Config) { c.ConfirmDownloads = true }},
		{name: "extra_sources", mutate: func(c *config.Config) { c.ExtraSources = config.ExtraSourcesNever }},
		{name: "a source removed", mutate: func(c *config.Config) { c.Sources = c.Sources[:2] }},
		// The two credentials that switch a source on. Listing a source is
		// half the decision, so the digest has to see the other half too.
		{name: "the unpaywall email removed", mutate: func(c *config.Config) { c.UnpaywallEmail = "" }},
		{name: "a core key added", mutate: func(c *config.Config) {
			c.Sources = append(c.Sources, "core")
			c.CoreKey = "core-key"
		}},
		{name: "the base path", mutate: func(*config.Config) {}, basePath: "/libgen"},
		{name: "statelessness", mutate: func(*config.Config) {}, stateless: &off},
		// The limits that decide what an identical call gets back: one refuses
		// a file outright, and the other two change the answer whenever the
		// caller leaves its own limits out.
		{name: "max_download_bytes", mutate: func(c *config.Config) { c.MaxDownloadBytes = 1 << 20 }},
		{name: "read_max_chars", mutate: func(c *config.Config) { c.ReadMaxChars = 5000 }},
		{name: "read_default_pages", mutate: func(c *config.Config) { c.ReadDefaultPages = 3 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := digestConfig()
			tc.mutate(cfg)
			basePath := "/"
			if tc.basePath != "" {
				basePath = tc.basePath
			}
			stateless := true
			if tc.stateless != nil {
				stateless = *tc.stateless
			}
			if got := configDigest(cfg, basePath, stateless); got == base {
				t.Errorf("the digest did not move when %s changed, so a fleet split on it reads as matched", tc.name)
			}
		})
	}
}

// TestConfigDigestSeesWhichCredentialIsSetButNotItsValue pins both halves of
// how a credential reaches the digest: as the source it switches on, and as
// nothing else.
//
// The digest is published unauthenticated, so a key's value folded into it in
// any form would let whoever reads /health test a guessed key offline. Two
// replicas holding different keys serve the same surface, and the digest must
// say so.
func TestConfigDigestSeesWhichCredentialIsSetButNotItsValue(t *testing.T) {
	withCore := func(key, email string) *config.Config {
		cfg := digestConfig()
		cfg.Sources = nil // every source, so only the credentials gate core and unpaywall
		cfg.CoreKey = key
		cfg.UnpaywallEmail = email
		return cfg
	}
	cases := []struct {
		name      string
		a, b      *config.Config
		wantEqual bool
	}{
		{name: "a core key against none", a: withCore("", ""), b: withCore("key-one", ""), wantEqual: false},
		{name: "an unpaywall email against none", a: withCore("", ""), b: withCore("", "a@example.org"), wantEqual: false},
		{name: "two different core keys", a: withCore("key-one", ""), b: withCore("key-two", ""), wantEqual: true},
		{name: "two different unpaywall emails", a: withCore("", "a@example.org"), b: withCore("", "b@example.org"), wantEqual: true},
		{name: "two different anna's keys", a: annasKeyed("annas-one"), b: annasKeyed("annas-two"), wantEqual: true},
		{name: "core listed without a key against core not listed", a: listedCoreWithoutKey(true), b: listedCoreWithoutKey(false), wantEqual: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b := configDigest(tc.a, "/", true), configDigest(tc.b, "/", true)
			if (a == b) != tc.wantEqual {
				t.Errorf("digests %q and %q: equal = %t, want %t", a, b, a == b, tc.wantEqual)
			}
		})
	}
}

// annasKeyed is digestConfig with an Anna's Archive member key. The key changes
// how annas resolves, never whether it is in the chain or what the tools say.
func annasKeyed(key string) *config.Config {
	cfg := digestConfig()
	cfg.AnnasKey = key
	return cfg
}

// listedCoreWithoutKey is digestConfig with core named in LIBGEN_MCP_SOURCES
// or not, and no CORE key either way: the source is out of the chain in both.
func listedCoreWithoutKey(listed bool) *config.Config {
	cfg := digestConfig()
	if listed {
		cfg.Sources = append(cfg.Sources, "core")
	}
	return cfg
}

// TestConfigDigestMovesExactlyWhenTheServedToolsDo holds the digest to what it
// is for: two replicas whose tools/list differ must not report the same digest,
// and two whose tools/list match must not report different ones. Each pair is
// served for real through newRegisteredServer on an HTTP address, so the
// comparison is made against the schemas a client would receive rather than
// against a restatement of what decides them.
func TestConfigDigestMovesExactlyWhenTheServedToolsDo(t *testing.T) {
	type knobs struct {
		sources        []string
		coreKey, email string
	}
	cases := []struct {
		name string
		a, b knobs
	}{
		{name: "core key", a: knobs{}, b: knobs{coreKey: "key-one"}},
		{name: "unpaywall email", a: knobs{}, b: knobs{email: "ops@example.org"}},
		{name: "different core keys", a: knobs{coreKey: "key-one"}, b: knobs{coreKey: "key-two"}},
		{name: "core listed without its key", a: knobs{sources: []string{"libgen", "core"}}, b: knobs{sources: []string{"libgen"}}},
		{name: "source list", a: knobs{sources: []string{"libgen", "annas"}}, b: knobs{sources: []string{"libgen"}}},
	}
	surface := func(t *testing.T, k knobs) (digest, tools string) {
		t.Helper()
		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("config.Load() error = %v", err)
		}
		cfg.Sources, cfg.CoreKey, cfg.UnpaywallEmail = k.sources, k.coreKey, k.email
		server, err := newRegisteredServer(cfg, "127.0.0.1:0", nil, inflightFlag{}, identityChoice{})
		if err != nil {
			t.Fatalf("newRegisteredServer() error = %v", err)
		}
		st, ct := mcp.NewInMemoryTransports()
		serverSession, err := server.Connect(t.Context(), st, nil)
		if err != nil {
			t.Fatalf("server connect: %v", err)
		}
		t.Cleanup(func() { _ = serverSession.Close() })
		session, err := mcp.NewClient(&mcp.Implementation{Name: "digest-test", Version: "0"}, nil).Connect(t.Context(), ct, nil)
		if err != nil {
			t.Fatalf("client connect: %v", err)
		}
		t.Cleanup(func() { _ = session.Close() })
		listed, err := session.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		raw, err := json.Marshal(listed.Tools)
		if err != nil {
			t.Fatalf("marshal tools: %v", err)
		}
		// Digested after registration, as main does: the server_fetch
		// tri-state is resolved by then.
		return configDigest(cfg, "/", true), string(raw)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			digestA, toolsA := surface(t, tc.a)
			digestB, toolsB := surface(t, tc.b)
			if sameTools, sameDigest := toolsA == toolsB, digestA == digestB; sameTools != sameDigest {
				t.Errorf("tools/list equal = %t but digests equal = %t (%q, %q)", sameTools, sameDigest, digestA, digestB)
			}
		})
	}
}

// TestConfigDigestIsTwelveHexAndStable keeps it comparable by eye and byte for
// byte across probes of the same process.
func TestConfigDigestIsTwelveHexAndStable(t *testing.T) {
	got := configDigest(digestConfig(), "/", true)
	if len(got) != 12 {
		t.Errorf("digest %q is %d characters, want 12", got, len(got))
	}
	if strings.Trim(got, "0123456789abcdef") != "" {
		t.Errorf("digest %q is not hexadecimal", got)
	}
	if again := configDigest(digestConfig(), "/", true); again != got {
		t.Errorf("two digests of the same configuration differ: %q and %q", got, again)
	}
	if configDigest(nil, "/", true) != "" {
		t.Error("a nil configuration produced a digest")
	}
}

// TestMainWithExitRefusesAnUnusableDrainDelay covers the bound, from the flag.
//
// Past a few minutes a drain delay is not a handover, it is a shutdown that
// appears to hang — and every supervisor kills the process long before it
// elapses, so the operator waits for something that never happens.
func TestMainWithExitRefusesAnUnusableDrainDelay(t *testing.T) {
	for _, value := range []string{"-1s", "10m"} {
		t.Run(value, func(t *testing.T) {
			var code int
			awaitReturn(t, func() {
				code = callMainWithExit(t, "libgen-mcp", "--http", "127.0.0.1:0", "--drain-delay", value)
			})
			if code != 1 {
				t.Fatalf("mainWithExit(--drain-delay %s) = %d, want 1", value, code)
			}
		})
	}
}
