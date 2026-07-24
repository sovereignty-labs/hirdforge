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

// taskInReview creates a task and walks it legally to review with a PR ref.
func taskInReview(t *testing.T, store *MemStore, id string, pr int64) {
	t.Helper()
	task := &TaskRecord{ID: id, RouteID: "build-on-label", IssueRepo: "kit/hirdforge", IssueNumber: 1}
	if err := store.CreateTask(task, "matched", Cause{Kind: CauseWebhook}); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{StatusDispatched, StatusBuilding, StatusReview} {
		if err := store.Transition(id, to, "step", Cause{Kind: CauseSandbox}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetPR(id, "kit/hirdforge", pr); err != nil {
		t.Fatal(err)
	}
}

// TestHandleEventApproveAdvances: APPROVED webhook → review → approved, with
// the webhook as the mechanical cause.
func TestHandleEventApproveAdvances(t *testing.T) {
	cfg := mustConfig(t)
	store := NewMemStore()
	c := New(cfg, store)
	taskInReview(t, store, "hf-adv", 3)

	d, err := c.HandleEvent(Event{Type: EventPRReviewSubmitted, Repo: "kit/hirdforge", ReviewState: "APPROVED", PRNumber: 3, Actor: "kit"})
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if d.MatchedRoute != "approve-on-review" || d.TaskID != "hf-adv" {
		t.Fatalf("decision = %+v", d)
	}
	task, history, _ := store.GetTask("hf-adv")
	if task.Status != StatusApproved {
		t.Fatalf("status = %q", task.Status)
	}
	final := history[len(history)-1]
	if final.Cause.Kind != CauseWebhook || !strings.Contains(final.Reason, "by kit") {
		t.Fatalf("final = %+v", final)
	}
}

// TestHandleEventAdvanceIllegalIsLoud: an advance the lifecycle table forbids
// is rejected and the decision says so.
func TestHandleEventAdvanceIllegalIsLoud(t *testing.T) {
	cfg := mustConfig(t)
	store := NewMemStore()
	c := New(cfg, store)
	task := &TaskRecord{ID: "hf-q", RouteID: "build-on-label", IssueRepo: "kit/hirdforge"}
	if err := store.CreateTask(task, "m", Cause{Kind: CauseWebhook}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPR("hf-q", "kit/hirdforge", 4); err != nil {
		t.Fatal(err)
	}
	d, err := c.HandleEvent(Event{Type: EventPRReviewSubmitted, Repo: "kit/hirdforge", ReviewState: "APPROVED", PRNumber: 4})
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if !strings.Contains(d.Reason, "REJECTED") {
		t.Fatalf("illegal advance must be loud, got %q", d.Reason)
	}
	got, _, _ := store.GetTask("hf-q")
	if got.Status != StatusQueued {
		t.Fatalf("status mutated to %q by an illegal advance", got.Status)
	}
}

// TestHandleEventReviewerDispatchIsTaskScoped: gate_passed fires the reviewer
// against THE task — no second task row.
func TestHandleEventReviewerDispatchIsTaskScoped(t *testing.T) {
	cfg := mustConfig(t)
	store := NewMemStore()
	c := New(cfg, store)
	taskInReview(t, store, "hf-rev", 5)

	var hookRoute string
	var hookEv Event
	c.OnReviewerDispatch = func(route *Route, ev Event) { hookRoute, hookEv = route.ID, ev }

	d, err := c.HandleEvent(Event{Type: EventTaskGatePassed, Repo: "kit/hirdforge", RouteID: "build-on-label", TaskID: "hf-rev", PRNumber: 5})
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if d.MatchedRoute != "review-on-gate" || d.TaskID != "hf-rev" {
		t.Fatalf("decision = %+v", d)
	}
	if hookRoute != "review-on-gate" || hookEv.TaskID != "hf-rev" {
		t.Fatalf("hook = %q %+v", hookRoute, hookEv)
	}
	tasks, _ := store.ListTasks(TaskFilter{})
	if len(tasks) != 1 {
		t.Fatalf("reviewer dispatch created a second task row: %d", len(tasks))
	}
}

// TestHandleEventRevisePath: REQUEST_CHANGES → same task fails with the
// reviewer feedback as failure context and re-dispatches (D-LESSONS #2+#3).
func TestHandleEventRevisePath(t *testing.T) {
	yaml := strings.Replace(validYAML, "routes:", `routes:
  - id: revise-on-changes-requested
    on:
      event: pr.review_submitted
      state: REQUEST_CHANGES
    dispatch:
      role: builder
      bundle: build-default
      carry: review-feedback
    done_gate:
      type: test-command
      command: "go test ./..."`, 1)
	cfg, err := ParseConfig([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemStore()
	c := New(cfg, store)
	taskInReview(t, store, "hf-fix", 6)

	var redispatched string
	c.OnTaskQueued = func(route *Route, taskID string) { redispatched = taskID }

	d, err := c.HandleEvent(Event{Type: EventPRReviewSubmitted, Repo: "kit/hirdforge",
		ReviewState: "REQUEST_CHANGES", PRNumber: 6, Actor: "sindri", ReviewBody: "fix the error path"})
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if !strings.Contains(d.Reason, "revise dispatch for task hf-fix (attempt 2)") {
		t.Fatalf("decision = %+v", d)
	}
	if redispatched != "hf-fix" {
		t.Fatalf("no re-dispatch, got %q", redispatched)
	}
	task, history, _ := store.GetTask("hf-fix")
	if task.Status != StatusFailed || task.Attempt != 2 {
		t.Fatalf("task = status %q attempt %d", task.Status, task.Attempt)
	}
	if !strings.Contains(string(task.FailureContext), "fix the error path") {
		t.Fatalf("failure context lost the feedback: %s", task.FailureContext)
	}
	final := history[len(history)-1]
	if !strings.Contains(final.Reason, "changes_requested by sindri") {
		t.Fatalf("final = %+v", final)
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

// TestHandleEventDedupsDuplicateLabelWebhooks pins the double-dispatch fix: Gitea
// delivers a single label as two webhooks (labeled + label_updated), and an
// operator dispatch can add a third. Each must collapse to ONE task+sandbox, or
// twins race on the same work branch. The guard is scoped to active tasks so a
// terminal task never blocks a genuine re-run.
func TestHandleEventDedupsDuplicateLabelWebhooks(t *testing.T) {
	cfg := mustConfig(t)
	store := NewMemStore()
	c := New(cfg, store)

	ev := Event{Type: EventIssueLabeled, Repo: "kit/hirdforge", Label: "agent:build", IssueNumber: 42, IssueTitle: "x"}
	d1, err := c.HandleEvent(ev)
	if err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if d1.TaskID == "" {
		t.Fatal("first dispatch created no task")
	}
	// The twin webhook for the same label.
	d2, err := c.HandleEvent(ev)
	if err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	if d2.TaskID != d1.TaskID {
		t.Fatalf("dedup failed: twin webhook made task %q, want the existing %q", d2.TaskID, d1.TaskID)
	}
	if !strings.Contains(d2.Reason, "dedup") {
		t.Fatalf("twin decision should record a dedup: %q", d2.Reason)
	}
	if got := countActiveForIssue(t, store, 42); got != 1 {
		t.Fatalf("want exactly 1 active task for issue 42, got %d", got)
	}

	// A different issue is a different task.
	d3, err := c.HandleEvent(Event{Type: EventIssueLabeled, Repo: "kit/hirdforge", Label: "agent:build", IssueNumber: 43})
	if err != nil {
		t.Fatalf("distinct issue: %v", err)
	}
	if d3.TaskID == "" || d3.TaskID == d1.TaskID {
		t.Fatalf("distinct issue must create a new task, got %q", d3.TaskID)
	}

	// Once the first task is terminal, a genuine re-label creates a fresh task —
	// dedup must not permanently wedge the issue.
	if err := store.Transition(d1.TaskID, StatusFailed, "gate failed", Cause{Kind: CauseSandbox}); err != nil {
		t.Fatalf("transition to failed: %v", err)
	}
	d4, err := c.HandleEvent(ev)
	if err != nil {
		t.Fatalf("re-label after terminal: %v", err)
	}
	if d4.TaskID == "" || d4.TaskID == d1.TaskID {
		t.Fatalf("re-label after terminal must create a new task, got %q (first %q)", d4.TaskID, d1.TaskID)
	}
}

func countActiveForIssue(t *testing.T, store *MemStore, issue int64) int {
	t.Helper()
	active, err := store.ListActive()
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	n := 0
	for _, tk := range active {
		if tk.IssueNumber == issue {
			n++
		}
	}
	return n
}
