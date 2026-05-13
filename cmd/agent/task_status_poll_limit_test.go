package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
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
