package main

import (
	"context"
	"strings"
	"testing"
)

func TestTerminationReasonIsAbnormal(t *testing.T) {
	abnormal := []terminationReason{
		terminationMaxTurns, terminationRepeatedToolCall,
		terminationToolErrorsExhausted, terminationNoActionableOutput,
	}
	for _, r := range abnormal {
		if !r.IsAbnormal() {
			t.Errorf("%s should be abnormal", r)
		}
	}
	normalOrEarly := []terminationReason{
		terminationCompleted, terminationContextExhaustion,
		terminationContextCanceled, terminationInferenceError, terminationUnknown,
	}
	for _, r := range normalOrEarly {
		if r.IsAbnormal() {
			t.Errorf("%s should not be abnormal", r)
		}
	}
}

func TestAbnormalExitSummaryRequestCarriesPlanAndShape(t *testing.T) {
	sessionTodos.clear("sess-sum")
	// No plan yet → says so.
	empty := abnormalExitSummaryRequest("sess-sum", terminationMaxTurns)
	if !strings.Contains(empty, "no plan was written") {
		t.Errorf("expected the no-plan note: %q", empty)
	}
	// With a plan → carried verbatim, with the required report shape and the
	// anti-dilution guard.
	sessionTodos.set("sess-sum", []todoItem{{"open the PR", todoPending}})
	msg := abnormalExitSummaryRequest("sess-sum", terminationMaxTurns)
	for _, want := range []string{"WHY:", "DONE:", "REMAINING:", "NEXT:", "open the PR", "max_turns_reached", "Do not claim a PR you did not create"} {
		if !strings.Contains(msg, want) {
			t.Errorf("summary request missing %q", want)
		}
	}
	sessionTodos.clear("sess-sum")
}

// TestM1AbnormalExitProducesHandoff proves the loop appends the handoff request
// on an abnormal (max_turns) exit, and that the request reaches the model
// carrying the plan. (The final streamed *content* is not asserted here: the
// deterministic stub has no SSE, so stub tests source final content from the
// lastNoToolAssistantContent fallback, not the stream — the streamed handoff is
// live-model behavior. What M1 owns mechanically is the injection, verified.)
func TestM1AbnormalExitProducesHandoff(t *testing.T) {
	logs := captureLogs(t)
	srv := newStubInferenceServer(t,
		stubToolCall("todo", `{"items":"[x] edit metrics\n[ ] open the PR"}`),
		stubToolCall("noop", `{"n":"1"}`),
		stubToolCall("noop", `{"n":"2"}`),
	)
	deps := harnessDeps(srv)
	deps.maxToolRounds = 3 // todo, noop, noop -> max_turns
	deps.reg.Register(&todoTool{})
	deps.reg.Register(&fakeNoopTool{})
	deps.workspace = t.TempDir()

	proc := newConversationProcessor(deps)
	if _, err := proc(context.Background(), "sess-m1-handoff", "task-m1-handoff",
		"Refactor the package.", nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !logs.hasTerminationReason(terminationMaxTurns) {
		t.Fatalf("expected max_turns; reasons: %v", logs.terminationReasons())
	}
	if !logs.hasLogMsg("abnormal_exit_summary_requested") {
		t.Error("expected the abnormal-exit summary to be requested")
	}
	// The handoff request reached the model, carrying the M6 plan verbatim.
	if !srv.anyRequestContains("handoff report") {
		t.Error("the summary-request reminder should have reached the model")
	}
	if !srv.anyRequestContains("open the PR") {
		t.Error("the handoff request should carry the plan verbatim")
	}
	sessionTodos.clear("sess-m1-handoff")
}

// TestM1NoSummaryOnNormalCompletion guards the boundary: a clean completion must
// NOT get the abnormal-exit handoff injection.
func TestM1NoSummaryOnNormalCompletion(t *testing.T) {
	logs := captureLogs(t)
	srv := newStubInferenceServer(t, stubContent("All done. NOOP: nothing needed."))
	deps := harnessDeps(srv)
	deps.workspace = t.TempDir()

	proc := newConversationProcessor(deps)
	if _, err := proc(context.Background(), "sess-m1-normal", "", "just look at the code", nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if logs.hasLogMsg("abnormal_exit_summary_requested") {
		t.Error("normal completion must not trigger the abnormal-exit summary")
	}
}
