package openalex

import (
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// t0 is the fixed instant every budget test measures from.
var t0 = time.Date(2026, 10, 3, 19, 0, 0, 0, time.UTC)

// headers builds the rate-limit headers a response carries; an empty value leaves
// that header out.
func headers(remaining, reset, retryAfter string) http.Header {
	h := http.Header{}
	for name, v := range map[string]string{
		"X-RateLimit-Remaining": remaining,
		"X-RateLimit-Reset":     reset,
		"Retry-After":           retryAfter,
	} {
		if v != "" {
			h.Set(name, v)
		}
	}
	return h
}

// TestBudget_Observe covers what each response shape leaves the budget holding.
func TestBudget_Observe(t *testing.T) {
	cases := []struct {
		name          string
		status        int
		header        http.Header
		wantKnown     bool
		wantRemaining int
		wantReset     time.Duration
	}{
		{name: "both headers", status: http.StatusOK, header: headers("968", "15880", ""), wantKnown: true, wantRemaining: 968, wantReset: 15880 * time.Second},
		{name: "no header map", status: http.StatusOK, header: nil},
		{name: "no headers", status: http.StatusOK, header: headers("", "", "")},
		{name: "remaining only", status: http.StatusOK, header: headers("968", "", "")},
		{name: "a malformed count", status: http.StatusOK, header: headers("lots", "60", "")},
		{name: "a negative count", status: http.StatusOK, header: headers("-1", "60", "")},
		{name: "a refusal with its own headers", status: http.StatusTooManyRequests, header: headers("0", "300", "5"), wantKnown: true, wantReset: 300 * time.Second},
		{name: "a bare refusal with Retry-After", status: http.StatusTooManyRequests, header: headers("", "", "42"), wantKnown: true, wantReset: 42 * time.Second},
		{name: "a bare refusal", status: http.StatusTooManyRequests, header: headers("", "", ""), wantKnown: true, wantReset: refusedBackoff},
		{name: "a bare refusal with a date Retry-After", status: http.StatusTooManyRequests, header: headers("", "", "Sat, 03 Oct 2026 20:00:00 GMT"), wantKnown: true, wantReset: refusedBackoff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b Budget
			b.Observe(tc.status, tc.header, t0)
			credits, resetAt, ok := b.Remaining(t0)
			if ok != tc.wantKnown {
				t.Fatalf("known = %v, want %v", ok, tc.wantKnown)
			}
			if !ok {
				return
			}
			if credits != tc.wantRemaining {
				t.Errorf("remaining = %d, want %d", credits, tc.wantRemaining)
			}
			if got := resetAt.Sub(t0); got != tc.wantReset {
				t.Errorf("reset in %v, want %v", got, tc.wantReset)
			}
		})
	}
}

// TestBudget_SpendKeepsTheReserve walks a budget down to its reserve: a spend is
// granted while it leaves the reserve whole, refused once it would not, and
// granted again once the window ends.
func TestBudget_SpendKeepsTheReserve(t *testing.T) {
	var b Budget
	if !b.Spend(SearchCost, KeylessReserve, t0) {
		t.Fatal("an unknown budget refused a spend")
	}
	b.Observe(http.StatusOK, headers("125", "3600", ""), t0)
	// sequential: each spend draws on what the previous one left
	for i, want := range []bool{true, true, false} {
		if got := b.Spend(SearchCost, KeylessReserve, t0); got != want {
			t.Errorf("spend %d = %v, want %v", i+1, got, want)
		}
	}
	if credits, _, _ := b.Remaining(t0); credits != 105 {
		t.Errorf("remaining = %d after two granted spends from 125, want 105", credits)
	}
	if !b.Spend(SearchCost, KeylessReserve, t0.Add(time.Hour)) {
		t.Error("a spend after the window ended was refused")
	}
	if _, _, ok := b.Remaining(t0.Add(time.Hour)); ok {
		t.Error("an ended window still reports a remaining count")
	}
}

// TestBudget_SameWindowKeepsTheSmallerCount checks an older response arriving late
// cannot raise the count, while a response from the next window replaces it.
func TestBudget_SameWindowKeepsTheSmallerCount(t *testing.T) {
	var b Budget
	b.Observe(http.StatusOK, headers("500", "3600", ""), t0)
	b.Observe(http.StatusOK, headers("700", "3599", ""), t0.Add(time.Second))
	if credits, _, _ := b.Remaining(t0); credits != 500 {
		t.Errorf("remaining = %d, want the smaller 500", credits)
	}
	b.Observe(http.StatusOK, headers("1000", strconv.Itoa(3600+86400), ""), t0.Add(2*time.Second))
	if credits, _, _ := b.Remaining(t0); credits != 1000 {
		t.Errorf("remaining = %d after the next window's response, want 1000", credits)
	}
	later := t0.Add(time.Duration(3601+86400) * time.Second)
	b.Observe(http.StatusOK, headers("990", "60", ""), later)
	if credits, _, _ := b.Remaining(later); credits != 990 {
		t.Errorf("remaining = %d after the window ended, want the fresh 990", credits)
	}
}

// TestBudget_ConcurrentSpendsNeverDipIntoTheReserve races many spends against one
// budget: exactly as many are granted as the budget above the reserve can pay for,
// and the race detector sees no unsynchronized access.
func TestBudget_ConcurrentSpendsNeverDipIntoTheReserve(t *testing.T) {
	var b Budget
	b.Observe(http.StatusOK, headers(strconv.Itoa(KeylessReserve+5*SearchCost), "3600", ""), t0)
	var granted atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if b.Spend(SearchCost, KeylessReserve, t0) {
				granted.Add(1)
			}
			b.Observe(http.StatusOK, headers("999", "3600", ""), t0)
		})
	}
	wg.Wait()
	if got := granted.Load(); got != 5 {
		t.Errorf("granted %d spends, want 5", got)
	}
}

// TestShared_IsOneBudget checks every caller is handed the same process budget.
func TestShared_IsOneBudget(t *testing.T) {
	first, second := Shared(), Shared()
	if first != second || first != &shared {
		t.Error("Shared returned two different budgets")
	}
}
