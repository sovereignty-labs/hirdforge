package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newCountingGateway returns a test gateway that returns a "working" task
// status and increments hits each request, so tests can assert that the
// gateway was (or was not) actually contacted.
func newCountingGateway(t *testing.T) (*httptest.Server, *int64) {
	t.Helper()
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"task-poll","agent":"ivar","status":{"state":"working"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestTaskStatusToolEnforcesPollLimit(t *testing.T) {
	gw, hits := newCountingGateway(t)
	tool := &taskStatusTool{gatewayURL: gw.URL}

	for i := 1; i <= taskStatusPollLimit; i++ {
		res := tool.Execute(map[string]interface{}{"task_id": "task-poll"})
		if res.Error != "" {
			t.Fatalf("poll #%d returned error: %s", i, res.Error)
		}
		if !strings.Contains(res.Output, `"state":"working"`) {
			t.Fatalf("poll #%d expected real gateway response, got %q", i, res.Output)
		}
	}
	if got := atomic.LoadInt64(hits); got != int64(taskStatusPollLimit) {
		t.Fatalf("expected %d gateway hits after first %d polls, got %d", taskStatusPollLimit, taskStatusPollLimit, got)
	}

	// The (limit+1)th poll must be synthetic and must NOT hit the gateway.
	res := tool.Execute(map[string]interface{}{"task_id": "task-poll"})
	if res.Output != taskStatusPollLimitMessage {
		t.Fatalf("expected synthetic message after exceeding limit, got %q", res.Output)
	}
	if got := atomic.LoadInt64(hits); got != int64(taskStatusPollLimit) {
		t.Fatalf("gateway was contacted after limit exceeded: hits=%d", got)
	}

	// Subsequent polls keep returning the synthetic message — the cap is
	// sticky for that task_id.
	res = tool.Execute(map[string]interface{}{"task_id": "task-poll"})
	if res.Output != taskStatusPollLimitMessage {
		t.Fatalf("expected sticky synthetic message on further polls, got %q", res.Output)
	}
}

func TestTaskStatusToolResetsOnDifferentTaskID(t *testing.T) {
	gw, hits := newCountingGateway(t)
	tool := &taskStatusTool{gatewayURL: gw.URL}

	// Burn the cap on task A.
	for i := 0; i < taskStatusPollLimit+1; i++ {
		_ = tool.Execute(map[string]interface{}{"task_id": "task-A"})
	}
	hitsAfterA := atomic.LoadInt64(hits)
	if hitsAfterA != int64(taskStatusPollLimit) {
		t.Fatalf("expected %d hits for task-A polls, got %d", taskStatusPollLimit, hitsAfterA)
	}

	// Switching to a new task_id must reset the counter. The first poll for
	// task-B should hit the gateway, not return the synthetic.
	res := tool.Execute(map[string]interface{}{"task_id": "task-B"})
	if !strings.Contains(res.Output, `"state":"working"`) {
		t.Fatalf("task-B first poll expected real gateway response, got %q", res.Output)
	}
	if got := atomic.LoadInt64(hits); got != hitsAfterA+1 {
		t.Fatalf("task-B first poll did not hit gateway: hits=%d (want %d)", got, hitsAfterA+1)
	}

	// And the cap should now apply fresh to task-B.
	for i := 0; i < taskStatusPollLimit-1; i++ {
		res := tool.Execute(map[string]interface{}{"task_id": "task-B"})
		if !strings.Contains(res.Output, `"state":"working"`) {
			t.Fatalf("task-B poll #%d expected real response, got %q", i+2, res.Output)
		}
	}
	res = tool.Execute(map[string]interface{}{"task_id": "task-B"})
	if res.Output != taskStatusPollLimitMessage {
		t.Fatalf("expected synthetic message on task-B after fresh cap, got %q", res.Output)
	}
}

func TestTaskStatusToolMissingTaskIDStillRejected(t *testing.T) {
	tool := &taskStatusTool{gatewayURL: "http://unreachable.invalid"}
	res := tool.Execute(map[string]interface{}{})
	if res.Error == "" {
		t.Fatalf("expected error for missing task_id, got output=%q", res.Output)
	}
	if !strings.Contains(res.Error, "task_id is required") {
		t.Fatalf("unexpected error message: %s", res.Error)
	}
}

// TestTaskStatusToolEnforcesMinInterval verifies that consecutive polls for
// the same task_id within minInterval return a synthetic "wait" message
// instead of hitting the gateway, and that the gated polls do NOT consume a
// slot of the 5-poll cap (otherwise tight-loop spam would drain the budget
// instantly).
func TestTaskStatusToolEnforcesMinInterval(t *testing.T) {
	gw, hits := newCountingGateway(t)
	clock := time.Unix(1_700_000_000, 0)
	tool := &taskStatusTool{
		gatewayURL:  gw.URL,
		minInterval: 10 * time.Second,
		nowFn:       func() time.Time { return clock },
	}

	first := tool.Execute(map[string]interface{}{"task_id": "task-int"})
	if first.Error != "" || !strings.Contains(first.Output, `"state":"working"`) {
		t.Fatalf("first poll expected real gateway response, got err=%q output=%q", first.Error, first.Output)
	}
	if got := atomic.LoadInt64(hits); got != 1 {
		t.Fatalf("first poll hits = %d, want 1", got)
	}

	// Three rapid follow-ups within the 10s interval. Each must be gated.
	for i, advance := range []time.Duration{0, 3 * time.Second, 9 * time.Second} {
		clock = time.Unix(1_700_000_000, 0).Add(advance)
		res := tool.Execute(map[string]interface{}{"task_id": "task-int"})
		if !strings.Contains(res.Output, "Next check available in") {
			t.Fatalf("gated poll #%d expected synthetic 'wait' message, got %q", i, res.Output)
		}
		if got := atomic.LoadInt64(hits); got != 1 {
			t.Fatalf("gated poll #%d hit gateway: hits=%d", i, got)
		}
	}

	// Advance past the interval. Next poll should hit the gateway.
	clock = time.Unix(1_700_000_000, 0).Add(10 * time.Second)
	res := tool.Execute(map[string]interface{}{"task_id": "task-int"})
	if !strings.Contains(res.Output, `"state":"working"`) {
		t.Fatalf("post-interval poll expected real response, got %q", res.Output)
	}
	if got := atomic.LoadInt64(hits); got != 2 {
		t.Fatalf("post-interval poll hits = %d, want 2", got)
	}
}

// TestTaskStatusToolMinIntervalDoesNotConsumeCap proves that a pile of spammy
// polls denied by the interval gate doesn't drain the 5-poll cap. The agent
// should still get five real polls if it waits enough between them.
// Note: cap takes precedence over interval gate — once count reaches the
// limit, every poll returns the "stop polling" synthetic, not "wait Xs".
func TestTaskStatusToolMinIntervalDoesNotConsumeCap(t *testing.T) {
	gw, hits := newCountingGateway(t)
	clock := time.Unix(1_700_000_000, 0)
	tool := &taskStatusTool{
		gatewayURL:  gw.URL,
		minInterval: 10 * time.Second,
		nowFn:       func() time.Time { return clock },
	}

	for i := 0; i < taskStatusPollLimit; i++ {
		res := tool.Execute(map[string]interface{}{"task_id": "task-spam"})
		if !strings.Contains(res.Output, `"state":"working"`) {
			t.Fatalf("real poll #%d denied; got %q", i+1, res.Output)
		}
		// Two gated spam polls before the cap is hit. After the final real
		// poll the cap takes over and these would return the "limit"
		// synthetic instead — that's fine, we only need to prove the cap
		// wasn't drained mid-cycle.
		if i < taskStatusPollLimit-1 {
			for s := 1; s <= 2; s++ {
				clock = clock.Add(time.Second)
				res := tool.Execute(map[string]interface{}{"task_id": "task-spam"})
				if !strings.Contains(res.Output, "Next check available in") {
					t.Fatalf("real#%d spam#%d expected 'wait' synthetic, got %q", i+1, s, res.Output)
				}
			}
		}
		clock = clock.Add(10 * time.Second)
	}
	if got := atomic.LoadInt64(hits); got != int64(taskStatusPollLimit) {
		t.Fatalf("expected %d real gateway hits despite spam, got %d", taskStatusPollLimit, got)
	}
}
