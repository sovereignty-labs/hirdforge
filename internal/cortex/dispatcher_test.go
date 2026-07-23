package cortex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.hirdforge.com/kit/hirdforge/internal/sandbox"
)

// fakeSandbox scripts the sandbox outcome per test.
type fakeSandbox struct {
	allocated *sandbox.RunSpec
	result    sandbox.RunResult
	gateLog   string
	destroyed bool
	allocErr  error
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
		Events:        func(ev Event) { events = append(events, ev) },
		AgentImage:    "registry/agent:test",
		AgentCommand:  []string{"/agent", "-one-shot"},
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
