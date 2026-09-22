package main

import (
	"strings"
	"testing"
)

// TestMatrices_EveryPointNamesItsQuestion verifies both plans are well formed,
// and the one property that keeps the matrix small: a point with no question
// written down is a number that will be published, compared and argued about
// without ever having been asked.
func TestMatrices_EveryPointNamesItsQuestion(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plans []scenarioPlan
	}{
		{name: "full", plans: fullMatrix()},
		{name: "quick", plans: quickMatrix()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.plans) == 0 {
				t.Fatal("the matrix is empty")
			}
			seen := map[string]bool{}
			for _, plan := range tc.plans {
				if seen[plan.ID] {
					t.Errorf("scenario id %s appears twice; -scenarios keys on it", plan.ID)
				}
				seen[plan.ID] = true
				assertPlanIsWellFormed(t, plan)
			}
		})
	}
}

// assertPlanIsWellFormed checks one point of a matrix.
func assertPlanIsWellFormed(t *testing.T, plan scenarioPlan) {
	t.Helper()
	if plan.Why == "" {
		t.Errorf("scenario %s does not say what it is for", plan.ID)
	}
	if plan.Clients < 1 || plan.Parallel < 1 {
		t.Errorf("scenario %s has %d clients and %d parallel", plan.ID, plan.Clients, plan.Parallel)
	}
	if plan.Transport != transportHTTP && plan.Transport != transportStdio {
		t.Errorf("scenario %s has transport %q", plan.ID, plan.Transport)
	}
	if plan.OutboundRPS <= 0 || plan.OutboundBurst < 1 {
		t.Errorf("scenario %s has no outbound budget: %v rps burst %d",
			plan.ID, plan.OutboundRPS, plan.OutboundBurst)
	}
}

// TestFullMatrix_KeepsOneScenarioAtTheShippedBudget verifies the record can
// still say what a deployment does out of the box.
//
// Every other scenario opens the outbound valve, because with the shipped one
// request per second every tool call queues behind a single token and the
// latency measured is the queue rather than the server. Opening it everywhere
// would publish a server nobody runs; leaving it closed everywhere would
// publish a queue and call it a cost.
func TestFullMatrix_KeepsOneScenarioAtTheShippedBudget(t *testing.T) {
	var shipped, opened int
	for _, plan := range fullMatrix() {
		switch plan.OutboundRPS {
		case shippedOutboundRPS:
			shipped++
		case benchOutboundRPS:
			opened++
		default:
			t.Errorf("scenario %s has an outbound budget of %v, which is neither the shipped nor the bench one",
				plan.ID, plan.OutboundRPS)
		}
	}
	if shipped != 1 {
		t.Errorf("%d scenarios run at the shipped budget, want exactly one", shipped)
	}
	if opened == 0 {
		t.Error("no scenario opens the outbound valve, so nothing measures the server rather than its queue")
	}
}

// TestBenchOutboundBudget_StaysInsideWhatTheServerAccepts verifies the widest
// budget this command asks for is one config will start with. The bound is
// (0, 20] on the rate and [1, 100] on the burst, and a run that asked for more
// would fail at startup with every scenario's first process.
func TestBenchOutboundBudget_StaysInsideWhatTheServerAccepts(t *testing.T) {
	if benchOutboundRPS <= 0 || benchOutboundRPS > 20 {
		t.Errorf("benchOutboundRPS = %v, outside the (0, 20] the server accepts", float64(benchOutboundRPS))
	}
	if benchOutboundBurst < 1 || benchOutboundBurst > 100 {
		t.Errorf("benchOutboundBurst = %d, outside the [1, 100] the server accepts", benchOutboundBurst)
	}
}

// TestSelectScenarios_RefusesANameNothingMatches verifies a typo in -scenarios
// stops the run rather than writing a record with nothing in it, which reads
// exactly like a run where everything was skipped.
func TestSelectScenarios_RefusesANameNothingMatches(t *testing.T) {
	plans := fullMatrix()

	testCases := []struct {
		name, ids string
		wantIDs   string
		wantErr   string
	}{
		{name: "empty takes everything", ids: "", wantIDs: joinIDs(plans)},
		{name: "one id", ids: "http-1", wantIDs: "http-1"},
		{name: "two, in the matrix's order", ids: "http-1,stdio-1", wantIDs: "stdio-1,http-1"},
		{name: "spaces are trimmed", ids: " http-1 , stdio-1 ", wantIDs: "stdio-1,http-1"},
		{name: "an empty element is ignored", ids: "http-1,,", wantIDs: "http-1"},
		{name: "a name nothing matches", ids: "http-1,nonsense", wantErr: "nonsense"},
		{name: "two names nothing matches", ids: "aaa,bbb", wantErr: "aaa, bbb"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			chosen, err := selectScenarios(plans, tc.ids)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error naming %s", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %v, want it to name %s", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("selectScenarios: %v", err)
			}
			if got := joinIDs(chosen); got != tc.wantIDs {
				t.Errorf("selected %s, want %s", got, tc.wantIDs)
			}
		})
	}
}

// joinIDs renders a plan list as its ids, for comparing selections.
func joinIDs(plans []scenarioPlan) string {
	ids := make([]string, len(plans))
	for i, plan := range plans {
		ids[i] = plan.ID
	}
	return strings.Join(ids, ",")
}

// TestClientAddress_GivesEveryClientOneOfItsOwn verifies the two properties the
// synthesized addresses need.
//
// They are drawn from 198.18.0.0/15, which RFC 2544 reserves for benchmark
// testing of network devices — so nothing routes there and a header that
// escaped into a log names an address that cannot belong to a real person. And
// there are enough of them: the first version took a /24 and wrapped at 254, so
// a step measuring five hundred callers was really measuring two hundred and
// fifty-four of them twice, and the per-caller slope fitted through it was a
// number about the wrapping.
func TestClientAddress_GivesEveryClientOneOfItsOwn(t *testing.T) {
	seen := map[string]bool{}
	for i := range 2000 {
		address := clientAddress(i)
		if !strings.HasPrefix(address, "198.18.") && !strings.HasPrefix(address, "198.19.") {
			t.Fatalf("clientAddress(%d) = %q, outside the benchmarking range", i, address)
		}
		if seen[address] {
			t.Fatalf("clientAddress(%d) = %q, which an earlier client already presented", i, address)
		}
		seen[address] = true
	}

	t.Run("the largest ladder still gets distinct addresses", func(t *testing.T) {
		biggest := defaultSeriesClients()[len(defaultSeriesClients())-1]
		distinct := map[string]bool{}
		for i := range biggest {
			distinct[clientAddress(i)] = true
		}
		if len(distinct) != biggest {
			t.Errorf("%d clients drew %d addresses; a repeat makes the per-caller slope a number about the wrapping",
				biggest, len(distinct))
		}
	})

	t.Run("the block wraps rather than escaping it", func(t *testing.T) {
		if got := clientAddress(1 << 17); got != clientAddress(0) {
			t.Errorf("clientAddress(131072) = %q, want it back at the start of the block", got)
		}
	})
}
