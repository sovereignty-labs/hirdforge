package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	toolpkg "git.hirdforge.com/kit/hirdforge/pkg/tools"
)

// M6 (externalized plan + re-anchoring) e2e scenarios on the deterministic stub
// bed. These prove the mechanism that must flip the P2.0 baseline: the terminal
// gate now works in TASK mode and lets the model RECOVER, the plan is
// re-surfaced on drift, and a fail-open question is honored as terminal.

// fakeNoopTool is a harmless always-succeeds tool for driving multi-round loops.
type fakeNoopTool struct{ calls int64 }

func (t *fakeNoopTool) Name() string                  { return "noop" }
func (t *fakeNoopTool) Description() string           { return "test-only no-op tool" }
func (t *fakeNoopTool) Parameters() map[string]string { return map[string]string{"n": "step"} }
func (t *fakeNoopTool) Execute(map[string]interface{}) toolpkg.ToolResult {
	atomic.AddInt64(&t.calls, 1)
	return toolpkg.ToolResult{Output: "ok"}
}

// anyRequestContains reports whether any inference request the stub received
// carried a message whose content contains sub.
func (s *stubInferenceServer) anyRequestContains(sub string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, req := range s.requests {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, sub) {
				return true
			}
		}
	}
	return false
}

// TestM6TaskModeTerminalGateFiresAndRecovers is the core M6 proof against the
// baseline failure: in TASK mode (not just conversation mode), a model that
// commits and then declares itself done WITHOUT a PR is caught, nudged, and
// RECOVERS by opening the PR — instead of the loop silently ending no_pr.
func TestM6TaskModeTerminalGateFiresAndRecovers(t *testing.T) {
	logs := captureLogs(t)
	const prURL = "http://127.0.0.1:18090/kit/benchfixture/pulls/5"
	srv := newStubInferenceServer(t,
		stubToolCall("git-commit", `{"repo":"benchfixture","message":"refactor","branch":"refactor-metrics"}`),
		stubContent("Done. I refactored and committed the changes."), // early stop — no PR
		stubToolCall("create-pr", `{"repo":"kit/benchfixture","head":"bench-builder/refactor-metrics","base":"main","title":"Refactor"}`),
		stubContent("Opened the PR: "+prURL),
	)

	commit := &flowFakeGitCommitTool{branch: "bench-builder/refactor-metrics"}
	createPR := &flowFakeCreatePRTool{url: prURL}
	deps := harnessDeps(srv)
	deps.maxToolRounds = 10
	deps.reg.Register(commit)
	deps.reg.Register(createPR)
	deps.workspace = t.TempDir()

	// TASK mode: non-empty taskID. Before M6 the gate was convo-only, so this
	// early stop would have terminated as-is.
	proc := newConversationProcessor(deps)
	out, err := proc(context.Background(), "sess-m6-recover", "task-m6-recover",
		"Refactor the package and create a PR. Report the PR URL.", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !logs.hasLogMsg("completion_gate_check") {
		t.Error("terminal gate must engage in task mode")
	}
	if !logs.hasLogMsg("completion_nudge_sent") {
		t.Error("expected a nudge on the PR-less early stop")
	}
	if atomic.LoadInt64(&createPR.calls) == 0 {
		t.Error("expected the model to RECOVER and call create-pr after the nudge")
	}
	if !strings.Contains(out, prURL) {
		t.Errorf("final content should carry the recovered PR URL: %q", out)
	}
	if !logs.hasLogMsg("completion_gate_passed") {
		t.Error("expected the gate to pass once the PR was reported")
	}
	if logs.hasLogMsg("completion_gate_failed") {
		t.Error("gate should not have failed — the model recovered")
	}
}

// TestM6ReanchorResurfacesPlan proves the externalized plan is pushed back into
// the model's context as it works: after the model writes a todo, a later
// inference request carries the plan as a system-reminder.
func TestM6ReanchorResurfacesPlan(t *testing.T) {
	logs := captureLogs(t)
	// After the plan is written, the model drifts into a repeated identical tool
	// call — a wobble signal that must fire an immediate plan re-anchor.
	srv := newStubInferenceServer(t,
		stubToolCall("todo", `{"items":"[ ] read files\n[ ] extract helper\n[ ] open the PR"}`),
		stubToolCall("noop", `{"n":"1"}`),
		stubToolCall("noop", `{"n":"1"}`),
		stubToolCall("noop", `{"n":"1"}`),
		stubContent("NOOP: nothing further to do."), // terminal, so the loop ends cleanly
	)
	deps := harnessDeps(srv)
	deps.maxToolRounds = 10
	deps.reg.Register(&todoTool{})
	deps.reg.Register(&fakeNoopTool{})
	deps.workspace = t.TempDir()

	proc := newConversationProcessor(deps)
	_, err := proc(context.Background(), "sess-m6-reanchor", "task-m6-reanchor",
		"Refactor and create a PR. Report the PR URL.", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !logs.hasLogMsg("todo_reanchor") {
		t.Error("expected a todo_reanchor event once a plan existed and the model drifted")
	}
	if !srv.anyRequestContains("Your current plan") {
		t.Error("expected the plan to be re-surfaced as a system-reminder in a later request")
	}
	if !srv.anyRequestContains("open the PR") {
		t.Error("expected the re-surfaced plan to carry the todo items")
	}
	sessionTodos.clear("sess-m6-reanchor")
}

// TestM6FailOpenQuestionIsTerminal proves the anti-dilution property: when the
// model fails open with a specific question, the gate accepts it as terminal —
// it is NOT nudged toward fabricating a PR.
func TestM6FailOpenQuestionIsTerminal(t *testing.T) {
	logs := captureLogs(t)
	srv := newStubInferenceServer(t,
		stubToolCall("noop", `{"n":"1"}`),
		stubContent("QUESTION: the issue doesn't say which base branch to target — main or develop?"),
	)
	deps := harnessDeps(srv)
	deps.maxToolRounds = 6
	deps.reg.Register(&fakeNoopTool{})
	deps.workspace = t.TempDir()

	proc := newConversationProcessor(deps)
	out, err := proc(context.Background(), "sess-m6-question", "task-m6-question",
		"Create a PR for the change. Report the PR URL.", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "QUESTION:") {
		t.Errorf("final content should be the fail-open question: %q", out)
	}
	if logs.hasLogMsg("completion_nudge_sent") {
		t.Error("a fail-open question must NOT be nudged toward a PR")
	}
	if logs.hasLogMsg("completion_gate_failed") {
		t.Error("a fail-open question must not be scored as a gate failure")
	}
	if !logs.hasLogMsg("completion_gate_passed") {
		t.Error("expected the gate to pass on a legitimate fail-open question")
	}
}
