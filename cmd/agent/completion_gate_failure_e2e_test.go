package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

// TestE2ECompletionGateFailure proves the unhappy path of the e2e contract: a
// PR-requiring task whose model never emits a PR/FAILED/NOOP signal does not
// succeed silently — the completion gate exhausts its nudges and the run
// terminates reportably with an explicit FAILED result.
//
// Deterministic and offline: a no-op probe tool engages the gate (it only fires
// once a tool call has happened), and the stub then returns final content with no
// completion signal on every turn.
func TestE2ECompletionGateFailure(t *testing.T) {
	logs := captureLogs(t)

	// Turn 0: a tool call, so hadToolCalls becomes true and the gate engages.
	// Turn 1+: final content with no PR/FAILED/NOOP signal. The stub repeats its
	// last response, so every subsequent turn returns the same no-signal text and
	// the gate keeps nudging until it exhausts.
	srv := newStubInferenceServer(t,
		stubToolCall("repeat-probe", `{}`),
		stubContent("I reviewed the code but have nothing conclusive to report."),
	)

	probe := &repeatProbeTool{}
	deps := harnessDeps(srv)
	deps.reg.Register(probe)
	deps.workspace = t.TempDir() // fixture workspace; never a real repo
	// Headroom so the loop reaches gate exhaustion before the turn limit: one
	// round for the tool call + maxCompletionNudges nudges + the exhaustion turn.
	deps.maxToolRounds = maxCompletionNudges + 4

	const userRequest = "Please create a PR for the fix."
	if !requestRequiresCompletionSignal(userRequest) {
		t.Fatal("precondition: the request should require a completion signal")
	}

	proc := newConversationProcessor(deps)
	out, err := proc(context.Background(), "sess-gate-fail", "", userRequest, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The tool ran, so the gate actually engaged (it is a no-op before any tool call).
	if atomic.LoadInt64(&probe.calls) == 0 {
		t.Fatal("expected the probe tool to be dispatched so the gate engages")
	}
	// The gate exhausted its nudges and failed the run.
	if !logs.hasLogMsg("completion_gate_failed") {
		t.Errorf("expected completion_gate_failed to be logged; reasons seen: %v", logs.terminationReasons())
	}
	// The terminal result is the explicit FAILED signal...
	if !strings.Contains(out, "FAILED: completion gate exhausted") {
		t.Errorf("final content = %q, want it to contain the FAILED gate message", out)
	}
	// ...which is a recognized completion signal — reportable, not silent success.
	if !contentHasCompletionSignal(out) {
		t.Errorf("FAILED gate result should satisfy contentHasCompletionSignal: %q", out)
	}
	// And the run terminated as no_actionable_output.
	if !logs.hasTerminationReason(terminationNoActionableOutput) {
		t.Errorf("expected session_termination reason %q; reasons seen: %v",
			terminationNoActionableOutput, logs.terminationReasons())
	}
}
