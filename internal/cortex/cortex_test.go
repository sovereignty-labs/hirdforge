package cortex

import (
	"strings"
	"testing"
)

// TestHandleEventBuildLabel pins P1.1's acceptance: a labeled issue creates a
// queued task with a logged routing decision.
func TestHandleEventBuildLabel(t *testing.T) {
	cfg := mustConfig(t)
	store := NewMemStore()
	c := New(cfg, store)

	d, err := c.HandleEvent(Event{
		Type: EventIssueLabeled, Repo: "kit/hirdforge", Label: "agent:build",
		IssueNumber: 41, IssueTitle: "add a thing", IssueBody: "details",
	})
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if d.MatchedRoute != "build-on-label" || d.TaskID == "" {
		t.Fatalf("decision = %+v", d)
	}

	task, history, err := store.GetTask(d.TaskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Status != StatusQueued {
		t.Fatalf("status = %q, want queued", task.Status)
	}
	if task.RouteID != "build-on-label" || task.IssueNumber != 41 {
		t.Fatalf("task = %+v", task)
	}
	if task.Bundle.Profile != "default" {
		t.Fatalf("bundle not snapshotted: %+v", task.Bundle)
	}
	if !strings.Contains(string(task.DoneGate), "test-command") {
		t.Fatalf("done_gate not snapshotted: %s", task.DoneGate)
	}
	if task.TimeoutAt == nil {
		t.Fatal("timeout_at not set from route timeout")
	}
	if len(history) != 1 || history[0].Cause.Kind != CauseWebhook {
		t.Fatalf("creating transition = %+v", history)
	}

	// The decision is in both the ring and the store.
	ring := c.RecentDecisions(10)
	if len(ring) != 1 || ring[0].TaskID != d.TaskID {
		t.Fatalf("ring = %+v", ring)
	}
	persisted, _ := store.ListDecisions(10)
	if len(persisted) != 1 || persisted[0].MatchedRoute != "build-on-label" {
		t.Fatalf("persisted decisions = %+v", persisted)
	}
}

// TestHandleEventNoMatch pins the other half of P1.1 acceptance: an unmatched
// event logs a no-match and creates nothing.
func TestHandleEventNoMatch(t *testing.T) {
	cfg := mustConfig(t)
	store := NewMemStore()
	c := New(cfg, store)

	d, err := c.HandleEvent(Event{Type: EventIssueLabeled, Repo: "kit/hirdforge", Label: "bug", IssueNumber: 5})
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if d.MatchedRoute != "" || d.TaskID != "" {
		t.Fatalf("no-match decision = %+v", d)
	}
	if !strings.HasPrefix(d.Reason, "no-match:") {
		t.Fatalf("reason = %q", d.Reason)
	}
	tasks, _ := store.ListTasks(TaskFilter{})
	if len(tasks) != 0 {
		t.Fatalf("no-match created a task: %+v", tasks)
	}
	if ds, _ := store.ListDecisions(10); len(ds) != 1 {
		t.Fatalf("no-match decision not persisted")
	}
}

// TestHandleEventAdvanceNotYetWired pins that matched advance routes are
// logged explicitly as not-yet-wired rather than silently ignored.
func TestHandleEventAdvanceNotYetWired(t *testing.T) {
	cfg := mustConfig(t)
	c := New(cfg, NewMemStore())
	d, err := c.HandleEvent(Event{Type: EventPRReviewSubmitted, Repo: "kit/hirdforge", ReviewState: "APPROVED", PRNumber: 3})
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if d.MatchedRoute != "approve-on-review" {
		t.Fatalf("decision = %+v", d)
	}
	if !strings.Contains(d.Reason, "not yet wired") {
		t.Fatalf("advance without wiring must say so, got %q", d.Reason)
	}
}

// TestReloadKeepsOldConfigOnError pins the never-a-silent-half-load rule.
func TestReloadKeepsOldConfigOnError(t *testing.T) {
	cfg := mustConfig(t)
	c := New(cfg, NewMemStore())
	if err := c.Reload("/nonexistent/cortex.yaml"); err == nil {
		t.Fatal("Reload of missing file must error")
	}
	if c.Config() != cfg {
		t.Fatal("failed reload must keep the previous config")
	}
}
