package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	toolpkg "git.hirdforge.com/kit/hirdforge/pkg/tools"
)

// This file is the deterministic (stub-inference, offline) half of the P2.0
// full-flow benchmark. run-flow.sh scores real local models end to end; these
// tests pin the exact loop behavior the flow depends on — with no GPU, network,
// or live Gitea — so the Phase-2 mechanisms (M6 todo/re-anchor, M2 coaching,
// etc.) have a fixed, reproducible target. The failure scenario below is the
// one the mechanisms are meant to flip from red to green.

// flowFakeGitCommitTool is a test-only stand-in for the real git-commit tool. It
// reports a successful commit+push, echoing the pushed branch name the way the
// real tool does — deliberately WITHOUT any PR-completion signal, so it cannot
// accidentally satisfy the PR gate.
type flowFakeGitCommitTool struct {
	calls  int64
	branch string
}

func (t *flowFakeGitCommitTool) Name() string        { return "git-commit" }
func (t *flowFakeGitCommitTool) Description() string { return "test-only fake git-commit tool" }
func (t *flowFakeGitCommitTool) Parameters() map[string]string {
	return map[string]string{"repo": "repo", "message": "message", "branch": "branch"}
}
func (t *flowFakeGitCommitTool) Execute(map[string]interface{}) toolpkg.ToolResult {
	atomic.AddInt64(&t.calls, 1)
	return toolpkg.ToolResult{Output: "Committed: refactor metrics\n\n1 file changed\n\nPushed to " + t.branch}
}

// flowFakeCreatePRTool is a test-only stand-in for create-pr: it returns a PR
// URL the loop can extract, exactly like the real tool on success.
type flowFakeCreatePRTool struct {
	calls int64
	url   string
}

func (t *flowFakeCreatePRTool) Name() string        { return "create-pr" }
func (t *flowFakeCreatePRTool) Description() string { return "test-only fake create-pr tool" }
func (t *flowFakeCreatePRTool) Parameters() map[string]string {
	return map[string]string{"repo": "repo", "head": "head", "base": "base", "title": "title"}
}
func (t *flowFakeCreatePRTool) Execute(map[string]interface{}) toolpkg.ToolResult {
	atomic.AddInt64(&t.calls, 1)
	return toolpkg.ToolResult{Output: "created PR #3: Refactor metrics\n" + t.url}
}

const flowUserRequest = "Refactor the package in ./benchfixture, git-commit to a new branch, then create a PR. Report the PR URL."

// TestE2EFlowStopsBeforeCreatePR reproduces the Phase-1 early-stop failure at the
// exact seam the flow benchmark measures: the model does the edit and commits,
// then declares itself done WITHOUT ever calling create-pr or reporting a PR.
// The completion gate must catch this — no completion_gate_passed, the gate
// fails after its nudges are exhausted — so the run is scored as the failure it
// is, not a success. This is the deterministic target M6 is meant to fix.
func TestE2EFlowStopsBeforeCreatePR(t *testing.T) {
	logs := captureLogs(t)

	// Round 1: commit. Rounds 2+: "done" with no PR (the stub repeats the last
	// response, so every nudge gets the same PR-less answer).
	srv := newStubInferenceServer(t,
		stubToolCall("git-commit", `{"repo":"benchfixture","message":"refactor","branch":"refactor-metrics"}`),
		stubContent("Done. I extracted the helper and committed the refactor."),
	)

	commit := &flowFakeGitCommitTool{branch: "bench-builder/refactor-metrics"}
	deps := harnessDeps(srv)
	deps.maxToolRounds = 12 // enough rounds for the nudge budget to exhaust
	deps.reg.Register(commit)
	deps.workspace = t.TempDir()

	if !requestRequiresCompletionSignal(flowUserRequest) {
		t.Fatal("precondition: the flow request must require a PR completion signal")
	}

	proc := newConversationProcessor(deps)
	out, err := proc(context.Background(), "sess-flow-stops", "", flowUserRequest, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The agent reached the commit leg...
	if got := atomic.LoadInt64(&commit.calls); got == 0 {
		t.Fatal("expected the fake git-commit tool to be invoked")
	}
	// ...but never produced a PR, so the gate must NOT pass...
	if logs.hasLogMsg("completion_gate_passed") {
		t.Error("completion gate must not pass when no PR was created")
	}
	// ...and must be recorded as a failure once the nudges are spent.
	if !logs.hasLogMsg("completion_gate_failed") {
		t.Errorf("expected completion_gate_failed; log messages seen did not include it")
	}
	// The run resolves to an explicit FAILED marker, never a PR success: no PR
	// URL and no "PR #N" leak into the terminal content.
	if completionPRURLPattern.MatchString(out) || completionPRNumPattern.MatchString(out) {
		t.Errorf("final content must not report a PR when none was created: %q", out)
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("expected an explicit FAILED result, got: %q", out)
	}
}

// TestE2EFlowEditCommitCreatePR is the positive control: the full happy path —
// commit, then create-pr, then report the URL — terminates as a satisfied PR
// completion. It proves the flow the benchmark scores is representable and
// deterministic end to end offline.
func TestE2EFlowEditCommitCreatePR(t *testing.T) {
	logs := captureLogs(t)

	const prURL = "http://127.0.0.1:18090/kit/benchfixture/pulls/3"
	srv := newStubInferenceServer(t,
		stubToolCall("git-commit", `{"repo":"benchfixture","message":"refactor","branch":"refactor-metrics"}`),
		stubToolCall("create-pr", `{"repo":"kit/benchfixture","head":"bench-builder/refactor-metrics","base":"main","title":"Refactor metrics"}`),
		stubContent("Done. Opened the PR: "+prURL),
	)

	commit := &flowFakeGitCommitTool{branch: "bench-builder/refactor-metrics"}
	createPR := &flowFakeCreatePRTool{url: prURL}
	deps := harnessDeps(srv)
	deps.reg.Register(commit)
	deps.reg.Register(createPR)
	deps.workspace = t.TempDir()

	proc := newConversationProcessor(deps)
	out, err := proc(context.Background(), "sess-flow-full", "", flowUserRequest, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if atomic.LoadInt64(&commit.calls) == 0 {
		t.Error("expected the fake git-commit tool to be invoked")
	}
	if atomic.LoadInt64(&createPR.calls) == 0 {
		t.Error("expected the fake create-pr tool to be invoked")
	}
	if !strings.Contains(out, prURL) {
		t.Errorf("final content missing the PR URL %q: %q", prURL, out)
	}
	if !logs.hasLogMsg("completion_gate_passed") {
		t.Error("expected completion_gate_passed for a satisfied PR gate")
	}
	if !logs.hasTerminationReason(terminationCompleted) {
		t.Errorf("expected termination reason %q; saw %v", terminationCompleted, logs.terminationReasons())
	}
}
