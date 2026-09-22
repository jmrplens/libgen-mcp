package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestStartCatalog_ServesTheCapturedPageAndCountsWhatArrived verifies the
// stand-in a measured process talks to instead of the internet.
//
// The size of the page is part of the measurement: an empty table would make
// every search cost nothing to parse and would publish a number no deployment
// will ever see. The count is how a scenario can say its searches reached the
// stand-in at all rather than being served from somewhere else.
func TestStartCatalog_ServesTheCapturedPageAndCountsWhatArrived(t *testing.T) {
	c, stop := startCatalog()
	t.Cleanup(stop)

	if c.requestCount() != 0 {
		t.Errorf("requestCount() = %d before anything asked", c.requestCount())
	}
	body := fetch(t, c.url+"/index.php?req=algorithms")
	if len(body) < 10000 {
		t.Errorf("the catalog served %d bytes; the captured page is tens of kilobytes", len(body))
	}
	if !strings.Contains(body, "<table") {
		t.Error("the served page carries no results table")
	}
	if c.requestCount() != 1 {
		t.Errorf("requestCount() = %d after one request", c.requestCount())
	}

	t.Run("every path answers the same body", func(t *testing.T) {
		other := fetch(t, c.url+"/something/else")
		if other != body {
			t.Error("two paths served different bodies; the stand-in is not meant to route")
		}
	})
}

// TestStartCollector_AcceptsEveryExport verifies the cheapest possible receiver,
// which is the honest floor for what switching telemetry on costs.
//
// It decodes nothing on purpose. Whether the payload is a valid export is a
// different question, and test/e2e/collector asks it against a real collector.
func TestStartCollector_AcceptsEveryExport(t *testing.T) {
	c, stop := startCollector()
	t.Cleanup(stop)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, c.url+"/v1/traces", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if c.requestCount() != 1 {
		t.Errorf("requestCount() = %d after one export", c.requestCount())
	}
}

// fetch reads a URL and returns its body.
func fetch(t *testing.T, url string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return string(body)
}
