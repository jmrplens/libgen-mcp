package libgen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jmrplens/libgen-mcp/internal/config"
)

// headSizeConfig is a minimal config for the HeadSize probe tests: no rate
// throttling that would slow the probe, a single retry, and a short timeout.
func headSizeConfig() *config.Config {
	return &config.Config{
		Timeout:                5 * time.Second,
		RateRPS:                1000,
		RateBurst:              100,
		RetryAttempts:          1,
		MaxConcurrentDownloads: 2,
	}
}

// headServer serves a HEAD-answering endpoint at /file: it records the request
// method it saw and replies with the given Content-Length (a negative length
// omits the header). It never writes a body, matching a real HEAD probe.
func headServer(t *testing.T, contentLength int64, status int) (base string, sawMethod *string) {
	t.Helper()
	var method string
	mux := http.NewServeMux()
	mux.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		if contentLength >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
		}
		w.WriteHeader(status)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, &method
}

// TestHeadSize_ReportsContentLength verifies the happy path: a resolved URL whose
// HEAD returns a positive Content-Length yields that size with ok=true, and the
// probe used the HEAD method (never a body-fetching GET).
func TestHeadSize_ReportsContentLength(t *testing.T) {
	base, sawMethod := headServer(t, 4096, http.StatusOK)
	src := stubSource{name: "libgen", supports: true, resolved: Resolved{FileURL: base + "/file", VerifyMD5: true}}
	c := New(staticMirrors{}, headSizeConfig(), WithSources(src))

	n, ok := c.HeadSize(context.Background(), Item{MD5: "abc"})
	if !ok {
		t.Fatal("HeadSize ok = false, want true for a positive Content-Length")
	}
	if n != 4096 {
		t.Errorf("HeadSize = %d, want 4096", n)
	}
	if *sawMethod != http.MethodHead {
		t.Errorf("probe used %s, want HEAD", *sawMethod)
	}
}

// TestHeadSize_MissingContentLength verifies a resolved URL whose HEAD omits the
// Content-Length header yields ok=false (unknown size), never an error.
func TestHeadSize_MissingContentLength(t *testing.T) {
	base, _ := headServer(t, -1, http.StatusOK) // negative → no Content-Length header
	src := stubSource{name: "libgen", supports: true, resolved: Resolved{FileURL: base + "/file"}}
	c := New(staticMirrors{}, headSizeConfig(), WithSources(src))

	if _, ok := c.HeadSize(context.Background(), Item{MD5: "abc"}); ok {
		t.Error("HeadSize ok = true, want false when Content-Length is absent")
	}
}

// TestHeadSize_Non2xxStatus verifies a resolved URL whose HEAD returns a non-2xx
// status yields ok=false, never an error.
func TestHeadSize_Non2xxStatus(t *testing.T) {
	base, _ := headServer(t, 4096, http.StatusForbidden)
	src := stubSource{name: "libgen", supports: true, resolved: Resolved{FileURL: base + "/file"}}
	c := New(staticMirrors{}, headSizeConfig(), WithSources(src))

	if _, ok := c.HeadSize(context.Background(), Item{MD5: "abc"}); ok {
		t.Error("HeadSize ok = true, want false for a 403 response")
	}
}

// TestHeadSize_ResolveFailureIsBestEffort verifies that when no source can resolve
// the item, HeadSize returns ok=false without an error rather than propagating the
// resolve failure — the confirmation flow proceeds with an unknown size.
func TestHeadSize_ResolveFailureIsBestEffort(t *testing.T) {
	src := stubSource{name: "libgen", supports: false} // supports nothing → no resolution
	c := New(staticMirrors{}, headSizeConfig(), WithSources(src))

	if _, ok := c.HeadSize(context.Background(), Item{MD5: "abc"}); ok {
		t.Error("HeadSize ok = true, want false when the item cannot be resolved")
	}
}
