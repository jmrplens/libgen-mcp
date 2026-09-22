package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPercentile_PublishesAValueTheRunObserved verifies the choice of
// nearest-rank over interpolation: a p99 between two real samples is a call
// nobody made, and these numbers go on a page.
func TestPercentile_PublishesAValueTheRunObserved(t *testing.T) {
	samples := []time.Duration{
		30 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond,
	}

	testCases := []struct {
		name string
		q    float64
		want float64
	}{
		// Nearest rank of four samples: p50 is the second, not the third. The
		// first version of this truncated q*n instead of taking its ceiling and
		// reported the third, which is a number above the median published as
		// the median — and the test agreed with it, because it was written from
		// the code rather than from the definition.
		{name: "the median", q: 0.5, want: 20},
		{name: "the tail", q: 0.99, want: 40},
		{name: "the maximum", q: 1, want: 40},
		{name: "the floor", q: 0, want: 10},
		{name: "a quarter", q: 0.25, want: 10},
		{name: "three quarters", q: 0.75, want: 30},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := percentile(samples, tc.q); got != tc.want {
				t.Errorf("percentile(%v) = %v, want %v", tc.q, got, tc.want)
			}
		})
	}

	t.Run("nothing was measured", func(t *testing.T) {
		if got := percentile(nil, 0.5); got != 0 {
			t.Errorf("percentile(nil) = %v, want 0", got)
		}
	})

	t.Run("the caller's slice is left alone", func(t *testing.T) {
		original := []time.Duration{3, 1, 2}
		_ = percentile(original, 0.5)
		if original[0] != 3 {
			t.Errorf("percentile sorted the caller's slice: %v", original)
		}
	})
}

// TestRecordRoundTrip_RefusesASchemaItDoesNotKnow verifies the record can be
// written and read back, and that a document from another build is refused
// rather than rendered with a field that moved.
func TestRecordRoundTrip_RefusesASchemaItDoesNotKnow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "record.json")
	run := newRun(t.Context(), Settings{Rounds: 3, SampleIntervalMs: 100}, ServerInfo{Version: "1.2.3", BytesOnDisk: 42})
	run.Scenarios = []Scenario{{ID: "http-1", Transport: transportHTTP, Clients: 1}}

	if err := writeRecord(path, run); err != nil {
		t.Fatalf("writeRecord: %v", err)
	}
	back, err := readRecord(path)
	if err != nil {
		t.Fatalf("readRecord: %v", err)
	}
	if back.Schema != recordSchema || back.Server.Version != "1.2.3" || len(back.Scenarios) != 1 {
		t.Errorf("read back %+v, want the record that was written", back)
	}
	if back.Host.OS == "" {
		t.Error("the record carries no machine")
	}

	t.Run("a schema from another build", func(t *testing.T) {
		other := filepath.Join(t.TempDir(), "other.json")
		if err = os.WriteFile(other, []byte(`{"schema":99}`), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		_, err = readRecord(other)
		if err == nil || !strings.Contains(err.Error(), "schema 99") {
			t.Errorf("readRecord() error = %v, want it to name the schema", err)
		}
	})

	t.Run("a document that is not JSON", func(t *testing.T) {
		broken := filepath.Join(t.TempDir(), "broken.json")
		if err = os.WriteFile(broken, []byte("not json"), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		if _, err = readRecord(broken); err == nil {
			t.Error("expected a decode error")
		}
	})

	t.Run("a path that is not there", func(t *testing.T) {
		if _, err = readRecord(filepath.Join(t.TempDir(), "absent.json")); err == nil {
			t.Error("expected a read error")
		}
	})
}
