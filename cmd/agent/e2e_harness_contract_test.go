package main

import (
	"context"
	"sync/atomic"
	"testing"
)

// This is the first deterministic, offline end-to-end (e2e) harness contract.
// It drives the real newConversationProcessor far enough to prove the minimum an
// agent must do before we build A1 staging auto-merge:
//
//  1. run a task (dispatch at least one tool),
//  2. terminate reportably (emit a canonical session_termination event), and
//  3. produce an explicit result the orchestrator can key off — a created PR, a
//     FAILED, or a NOOP — recognized by the production contentHasCompletionSignal.
//
// It touches no real repo or network: the inference stub scripts the turns and a
// fake no-op tool stands in for task work, with a temp workspace.
//
// See docs/e2e-staging-harness-contract.md for the full contract and the PR
// sequence toward A1 staging.

// runE2EScenario scripts one tool-using turn followed by a final result message,
// drives the real processor, and returns the final content and captured logs.
func runE2EScenario(t *testing.T, userRequest, resultContent string) (string, *logCapture) {
	t.Helper()
	logs := captureLogs(t)

	// Turn 1: the agent does some work (a no-op probe tool). Turn 2: it reports
	// its result. The stub serves these in order.
	srv := newStubInferenceServer(t,
		stubToolCall("repeat-probe", `{}`),
		stubContent(resultContent),
	)

	probe := &repeatProbeTool{}
	deps := harnessDeps(srv)
	deps.reg.Register(probe)
	deps.workspace = t.TempDir() // fixture workspace; never a real repo

	proc := newConversationProcessor(deps)
	out, err := proc(context.Background(), "sess-e2e", "", userRequest, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt64(&probe.calls) == 0 {
		t.Fatalf("expected the task to dispatch the tool at least once")
	}
	return out, logs
}

// TestE2EReportableResults proves an agent run terminates reportably with each of
// the three orchestrator-visible outcomes: a created PR, a FAILED, or a NOOP.
func TestE2EReportableResults(t *testing.T) {
	cases := []struct {
		name   string
		result string
	}{
		{"pr_created_url", "Opened the pull request: http://gitea.local/kit/hirdforge/pulls/42"},
		{"pr_created_hash", "All set — PR #42 is up for review."},
		{"failed", "FAILED: the change does not build after three attempts."},
		{"noop", "NOOP: the requested fix is already present on main."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, logs := runE2EScenario(t, "please do the task", c.result)

			// The run produced an explicit, orchestrator-recognizable result.
			if !contentHasCompletionSignal(out) {
				t.Errorf("final content lacks a completion signal: %q", out)
			}
			// It terminated reportably.
			if !logs.hasTerminationReason(terminationCompleted) {
				t.Errorf("expected a session_termination with reason %q; reasons seen: %v",
					terminationCompleted, logs.terminationReasons())
			}
		})
	}
}

// TestE2EPRCompletionGatePasses proves that for a PR-requiring task, a final
// message carrying a PR URL satisfies the production completion gate (which
// otherwise nudges), and the run terminates as completed.
func TestE2EPRCompletionGatePasses(t *testing.T) {
	const prResult = "Done. Created the PR: http://gitea.local/kit/hirdforge/pulls/7"
	if !requestRequiresCompletionSignal("please create a PR for the fix") {
		t.Fatal("precondition: the request should require a completion signal")
	}

	out, logs := runE2EScenario(t, "please create a PR for the fix", prResult)

	if !contentHasCompletionSignal(out) {
		t.Errorf("final content lacks a completion signal: %q", out)
	}
	if !logs.hasLogMsg("completion_gate_passed") {
		t.Errorf("expected completion_gate_passed to be logged for a satisfied PR gate")
	}
	if !logs.hasTerminationReason(terminationCompleted) {
		t.Errorf("expected a session_termination with reason %q; reasons seen: %v",
			terminationCompleted, logs.terminationReasons())
	}
}
