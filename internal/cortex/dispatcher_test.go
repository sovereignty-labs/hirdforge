package cortex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.hirdforge.com/kit/hirdforge/internal/profile"
	"git.hirdforge.com/kit/hirdforge/internal/sandbox"
)

// fakeSandbox scripts the sandbox outcome per test.
type fakeSandbox struct {
	allocated *sandbox.RunSpec
	result    sandbox.RunResult
	gateLog   string
	destroyed bool
	allocErr  error
	jobExists bool
	existsErr error
}

func (f *fakeSandbox) Allocate(_ context.Context, spec sandbox.RunSpec) (sandbox.Ref, error) {
	if f.allocErr != nil {
		return sandbox.Ref{}, f.allocErr
	}
	f.allocated = &spec
	return sandbox.Ref{Namespace: "sandbox", JobName: "hf-task-test", CMName: "hf-task-test-envelope"}, nil
}
func (f *fakeSandbox) Wait(_ context.Context, ref *sandbox.Ref) (sandbox.RunResult, error) {
	ref.PodName = "pod-1"
	return f.result, nil
}
func (f *fakeSandbox) GateOutput(_ context.Context, _ sandbox.Ref) ([]byte, error) {
	return []byte(f.gateLog), nil
}
func (f *fakeSandbox) Destroy(_ context.Context, _ sandbox.Ref) error {
	f.destroyed = true
	return nil
}
func (f *fakeSandbox) Exists(_ context.Context, _ sandbox.Ref) (bool, error) {
	return f.jobExists, f.existsErr
}

func newDispatchFixture(t *testing.T, fs *fakeSandbox, prFound bool) (*Dispatcher, *Config, *Route, string, *MemStore, *[]Event) {
	t.Helper()
	cfg := mustConfig(t)
	store := NewMemStore()
	task := &TaskRecord{ID: "hf-01-test", RouteID: "build-on-label", IssueRepo: "kit/hirdforge",
		IssueNumber: 41, IssueTitle: "t", Bundle: cfg.Bundles["build-default"]}
	if err := store.CreateTask(task, "matched build-on-label", Cause{Kind: CauseWebhook}); err != nil {
		t.Fatal(err)
	}
	var events []Event
	d := &Dispatcher{
		Store:   store,
		Sandbox: fs,
		PRLookup: func(_ context.Context, repo, head string) (int64, bool, error) {
			return 55, prFound, nil
		},
		Events:           func(ev Event) { events = append(events, ev) },
		AgentImage:       "registry/agent:test",
		AgentCommandBase: []string{"/agent", "-one-shot"},
		Profiles: map[string]profile.Profile{
			"builder": {Version: 1, Name: "builder", Tools: []string{"read", "edit", "gitea"}, Procedure: "builder", Budgets: profile.Budgets{MaxToolRounds: 80}},
		},
		CloneURLBase:  "https://git.hirdforge.com",
		BaseBranch:    "main",
		CredentialRef: "sandbox-git-cred",
	}
	return d, cfg, &cfg.Routes[0], task.ID, store, &events
}

func historyReasons(t *testing.T, store *MemStore, id string) []string {
	t.Helper()
	_, history, err := store.GetTask(id)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(history))
	for _, h := range history {
		out = append(out, h.ToStatus+": "+h.Reason)
	}
	return out
}

func TestDispatchGreenPathReachesReview(t *testing.T) {
	fs := &fakeSandbox{
		result:  sandbox.RunResult{CleanCheckOK: true, CheckoutOK: true, AgentExitCode: 0, GateExitCode: 0, Phase: "succeeded"},
		gateLog: "ok  \tall tests pass\t1.2s",
	}
	d, cfg, route, taskID, store, events := newDispatchFixture(t, fs, true)

	if err := d.DispatchTask(context.Background(), cfg, route, taskID); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	task, history, _ := store.GetTask(taskID)
	if task.Status != StatusReview {
		t.Fatalf("status = %q, want review (history: %v)", task.Status, historyReasons(t, store, taskID))
	}
	// queued -> dispatched -> building -> review, each with a reason.
	if len(history) != 4 {
		t.Fatalf("history = %v", historyReasons(t, store, taskID))
	}
	final := history[len(history)-1]
	if !strings.Contains(final.Reason, "gate_passed:test-command") || !strings.Contains(final.Reason, "PR #55") {
		t.Fatalf("final reason = %q", final.Reason)
	}
	gate, _ := final.Cause.Detail["gate"].(map[string]any)
	if gate["evidence"] == "" {
		t.Fatal("green transition carries no evidence — forbidden (DONE_GATE.md)")
	}
	if !fs.destroyed {
		t.Fatal("sandbox not destroyed after collect")
	}
	if len(*events) != 1 || (*events)[0].Type != EventTaskGatePassed || (*events)[0].PRNumber != 55 {
		t.Fatalf("events = %+v", *events)
	}
	// The envelope the sandbox received is the contract shape.
	envStr := string(fs.allocated.Envelope)
	for _, want := range []string{`"envelope_version":1`, `"task_id":"hf-01-test"`, `"role":"builder"`,
		`"work_branch":"agent/hf-01-test"`, `"done_when"`, "does not decide completion"} {
		if !strings.Contains(envStr, want) {
			t.Errorf("envelope missing %q", want)
		}
	}
	if fs.allocated.GateCommand == "" {
		t.Fatal("RunSpec carries no gate command")
	}
}

func TestDispatchRedGateFailsWithExcerpt(t *testing.T) {
	fs := &fakeSandbox{
		result:  sandbox.RunResult{CleanCheckOK: true, CheckoutOK: true, AgentExitCode: 0, GateExitCode: 1, Phase: "failed"},
		gateLog: "--- FAIL: TestX (0.00s)\n    x_test.go:1: boom",
	}
	d, cfg, route, taskID, store, events := newDispatchFixture(t, fs, true)

	if err := d.DispatchTask(context.Background(), cfg, route, taskID); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	task, _, _ := store.GetTask(taskID)
	if task.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", task.Status)
	}
	reasons := historyReasons(t, store, taskID)
	final := reasons[len(reasons)-1]
	if !strings.Contains(final, "gate_failed:test-command exit 1") || !strings.Contains(final, "FAIL: TestX") {
		t.Fatalf("final = %q — the excerpt must ride the reason", final)
	}
	if len(*events) != 1 || (*events)[0].Type != EventTaskGateFailed {
		t.Fatalf("events = %+v", *events)
	}
}

// TestDispatchAgentClaimsSuccessButNoPR pins the core doctrine: the agent's
// clean exit means nothing without an observable PR.
func TestDispatchAgentClaimsSuccessButNoPR(t *testing.T) {
	fs := &fakeSandbox{
		result: sandbox.RunResult{CleanCheckOK: true, CheckoutOK: true, AgentExitCode: 0, GateExitCode: 0, Phase: "succeeded"},
	}
	d, cfg, route, taskID, store, _ := newDispatchFixture(t, fs, false /* no PR on Gitea */)

	if err := d.DispatchTask(context.Background(), cfg, route, taskID); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	task, _, _ := store.GetTask(taskID)
	if task.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", task.Status)
	}
	reasons := historyReasons(t, store, taskID)
	if !strings.Contains(reasons[len(reasons)-1], "no_pr") {
		t.Fatalf("final = %q", reasons[len(reasons)-1])
	}
}

func TestDispatchDirtyWorkspaceFails(t *testing.T) {
	fs := &fakeSandbox{
		result: sandbox.RunResult{CleanCheckOK: false, CheckoutOK: false, Phase: "failed", AgentExitCode: -1, GateExitCode: -1},
	}
	d, cfg, route, taskID, store, _ := newDispatchFixture(t, fs, true)
	if err := d.DispatchTask(context.Background(), cfg, route, taskID); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	task, _, _ := store.GetTask(taskID)
	reasons := historyReasons(t, store, taskID)
	if task.Status != StatusFailed || !strings.Contains(reasons[len(reasons)-1], "guard failed") {
		t.Fatalf("status=%q reasons=%v", task.Status, reasons)
	}
	if !fs.destroyed {
		t.Fatal("sandbox must be destroyed even on guard failure")
	}
}

func TestDispatchTimeoutIsTimeoutCause(t *testing.T) {
	fs := &fakeSandbox{
		result: sandbox.RunResult{CleanCheckOK: true, CheckoutOK: true, Phase: "deadline", AgentExitCode: -1, GateExitCode: -1},
	}
	d, cfg, route, taskID, store, _ := newDispatchFixture(t, fs, true)
	if err := d.DispatchTask(context.Background(), cfg, route, taskID); err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	_, history, _ := store.GetTask(taskID)
	final := history[len(history)-1]
	if final.Cause.Kind != CauseTimeout || !strings.Contains(final.Reason, "timeout") {
		t.Fatalf("final = %+v", final)
	}
}

func TestBuildEnvelopeReviewerVariantTruncatesDiff(t *testing.T) {
	cfg := mustConfig(t)
	route := &cfg.Routes[1] // review-on-gate
	task := &TaskRecord{ID: "hf-02", RouteID: route.ID, IssueRepo: "kit/hirdforge", Attempt: 1}
	big := strings.Repeat("x", maxReviewDiffBytes+10)
	env, err := BuildEnvelope(cfg, route, EnvelopeParams{
		Task: task, CloneURL: "u", BaseBranch: "main", CredentialRef: "c",
		Review: &ReviewContext{PR: ReviewPR{Repo: "kit/hirdforge", Number: 55, Head: "agent/hf-01", Base: "main"}, Diff: big},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !env.Review.DiffTruncated || len(env.Review.Diff) != maxReviewDiffBytes {
		t.Fatalf("diff not truncated: %d", len(env.Review.Diff))
	}
	if !strings.Contains(env.DoneWhen, "Gitea review") {
		t.Fatalf("reviewer DONE WHEN = %q", env.DoneWhen)
	}
}

func TestBuildEnvelopeDeterministic(t *testing.T) {
	cfg := mustConfig(t)
	route := &cfg.Routes[0]
	task := &TaskRecord{ID: "hf-03", RouteID: route.ID, IssueRepo: "kit/hirdforge", IssueNumber: 9, IssueTitle: "t", Attempt: 1, Bundle: cfg.Bundles["build-default"]}
	p := EnvelopeParams{Task: task, Event: Event{IssueBody: "body"}, CloneURL: "u", BaseBranch: "main", CredentialRef: "c"}
	a, err := BuildEnvelope(cfg, route, p)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		b, err := BuildEnvelope(cfg, route, p)
		if err != nil {
			t.Fatal(err)
		}
		aj, _ := json.Marshal(a)
		bj, _ := json.Marshal(b)
		if string(aj) != string(bj) {
			t.Fatal("envelope assembly is not deterministic")
		}
	}
}

// TestReconcileFailsOrphanWhenJobGone: a task stuck at building whose sandbox
// Job is gone (gateway restarted) is failed loudly, not left a zombie.
func TestReconcileFailsOrphanWhenJobGone(t *testing.T) {
	cfg := mustConfig(t)
	store := NewMemStore()
	taskInBuilding(t, store, "hf-orph", "build-on-label")
	fs := &fakeSandbox{jobExists: false}
	var events []Event
	d := &Dispatcher{Store: store, Sandbox: fs, Events: func(e Event) { events = append(events, e) },
		CloneURLBase: "http://g", BaseBranch: "main"}

	d.ReconcileOnStartup(context.Background(), cfg, "sandbox")

	task, _, _ := store.GetTask("hf-orph")
	if task.Status != StatusFailed {
		t.Fatalf("orphan not failed: %s", task.Status)
	}
	reasons := historyReasons(t, store, "hf-orph")
	if !strings.Contains(reasons[len(reasons)-1], "orphaned") {
		t.Fatalf("reason = %q", reasons[len(reasons)-1])
	}
}

// TestReconcileReattachesWhenJobExists: a still-running Job is re-attached and
// finalized (here: green gate -> review), recovering the lost waiter.
func TestReconcileReattachesWhenJobExists(t *testing.T) {
	cfg := mustConfig(t)
	store := NewMemStore()
	taskInBuilding(t, store, "hf-reatt", "build-on-label")
	fs := &fakeSandbox{jobExists: true,
		result:  sandbox.RunResult{CleanCheckOK: true, CheckoutOK: true, GateExitCode: 0, Phase: "succeeded"},
		gateLog: "ok"}
	d := &Dispatcher{Store: store, Sandbox: fs,
		PRLookup: func(_ context.Context, _, _ string) (int64, bool, error) { return 7, true, nil },
		Events:   func(Event) {}, CloneURLBase: "http://g", BaseBranch: "main"}

	d.ReconcileOnStartup(context.Background(), cfg, "sandbox")
	// re-attach runs in a goroutine; wait briefly for it to finalize.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tk, _, _ := store.GetTask("hf-reatt"); tk.Status == StatusReview {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	tk, _, _ := store.GetTask("hf-reatt")
	t.Fatalf("re-attach did not finalize to review: %s", tk.Status)
}

// TestWatchdogFailsPastTimeout: any active task past timeout_at is failed.
func TestWatchdogFailsPastTimeout(t *testing.T) {
	store := NewMemStore()
	taskInBuilding(t, store, "hf-to", "build-on-label")
	past := time.Now().Add(-time.Minute)
	if tk, _, _ := store.GetTask("hf-to"); tk != nil {
		tk.TimeoutAt = &past
		// write back through the mem map
	}
	// MemStore stores pointers; set timeout via a direct helper.
	setMemTimeout(store, "hf-to", past)
	d := &Dispatcher{Store: store, Sandbox: &fakeSandbox{}}
	ctx, cancel := context.WithCancel(context.Background())
	go d.RunTimeoutWatchdog(ctx, 20*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tk, _, _ := store.GetTask("hf-to"); tk.Status == StatusFailed {
			cancel()
			reasons := historyReasons(t, store, "hf-to")
			if !strings.Contains(reasons[len(reasons)-1], "timeout") {
				t.Fatalf("reason = %q", reasons[len(reasons)-1])
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	t.Fatal("watchdog did not fail the past-timeout task")
}

func taskInBuilding(t *testing.T, store *MemStore, id, routeID string) {
	t.Helper()
	task := &TaskRecord{ID: id, RouteID: routeID, IssueRepo: "kit/hirdforge", IssueNumber: 1}
	if err := store.CreateTask(task, "m", Cause{Kind: CauseWebhook}); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{StatusDispatched, StatusBuilding} {
		if err := store.Transition(id, to, "step", Cause{Kind: CauseSandbox}); err != nil {
			t.Fatal(err)
		}
	}
}

func setMemTimeout(store *MemStore, id string, at time.Time) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if tk, ok := store.tasks[id]; ok {
		tk.TimeoutAt = &at
	}
}

// TestDispatchReviewerAdvancesOnObservedVerdict pins the P2.7 tail fix: the
// reviewer leg drives approve/revise from the reliable API verdict observation
// (review.state) rather than depending on the fragile pr.review_submitted
// webhook — it emits EventPRReviewSubmitted so the approve-on-review route fires.
func TestDispatchReviewerAdvancesOnObservedVerdict(t *testing.T) {
	fs := &fakeSandbox{}
	d, cfg, _, taskID, store, events := newDispatchFixture(t, fs, true)
	// Put the task in review with a PR, as the green path would.
	if err := store.SetPR(taskID, "kit/hirdforge", 55); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{StatusDispatched, StatusBuilding, StatusReview} {
		if err := store.Transition(taskID, to, "setup", Cause{Kind: CauseSandbox}); err != nil {
			t.Fatalf("transition %s: %v", to, err)
		}
	}
	// Reviewer profile + the diff/verdict observers.
	d.Profiles["reviewer"] = profile.Profile{Version: 1, Name: "reviewer", Tools: []string{"read", "git-diff", "create-review"}, Procedure: "reviewer", Budgets: profile.Budgets{MaxToolRounds: 30}}
	d.DiffFetch = func(_ context.Context, _ string, _ int64) (string, string, string, error) {
		return "diff --git a/x b/x", "agent/x", "main", nil
	}
	d.ReviewLookup = func(_ context.Context, _ string, _ int64) (string, string, bool, error) {
		return "APPROVED", "reviewers", true, nil
	}
	// Find the review-on-gate route.
	var reviewRoute *Route
	for i := range cfg.Routes {
		if cfg.Routes[i].ID == "review-on-gate" {
			reviewRoute = &cfg.Routes[i]
		}
	}
	if reviewRoute == nil {
		t.Fatal("review-on-gate route missing from config")
	}

	if err := d.DispatchReviewer(context.Background(), cfg, reviewRoute, Event{TaskID: taskID}); err != nil {
		t.Fatalf("DispatchReviewer: %v", err)
	}
	// The observed APPROVED verdict must be emitted as a review-submitted event.
	var got *Event
	for i := range *events {
		if (*events)[i].Type == EventPRReviewSubmitted {
			got = &(*events)[i]
		}
	}
	if got == nil {
		t.Fatalf("no EventPRReviewSubmitted emitted; events=%+v", *events)
	}
	if got.ReviewState != "APPROVED" || got.PRNumber != 55 {
		t.Fatalf("emitted event = %+v, want APPROVED on PR 55", *got)
	}
}
