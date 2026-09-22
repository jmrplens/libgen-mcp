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
