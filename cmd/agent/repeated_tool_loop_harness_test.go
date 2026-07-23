package main

import (
	"context"
	"sync/atomic"
	"testing"

	toolpkg "git.hirdforge.com/kit/hirdforge/pkg/tools"
)

// repeatProbeTool is the smallest possible registered tool: it has no external
// effects and returns a fixed benign result. It only counts how many times it
// ran so the test can confirm the loop actually dispatched it.
type repeatProbeTool struct {
	calls int64
}

func (t *repeatProbeTool) Name() string                  { return "repeat-probe" }
func (t *repeatProbeTool) Description() string           { return "test-only no-op probe" }
func (t *repeatProbeTool) Parameters() map[string]string { return map[string]string{} }
func (t *repeatProbeTool) Execute(map[string]interface{}) toolpkg.ToolResult {
	atomic.AddInt64(&t.calls, 1)
	return toolpkg.ToolResult{Output: "ok"}
}

// hasLogMsg reports whether any captured log entry has the given msg key.
func (c *logCapture) hasLogMsg(msg string) bool {
	for _, rec := range c.entries() {
		if rec["msg"] == msg {
			return true
		}
	}
	return false
}

// terminationEntry returns the first session_termination entry with the given
// reason, if any.
func (c *logCapture) terminationEntry(reason terminationReason) (map[string]interface{}, bool) {
	for _, rec := range c.entries() {
		if rec["msg"] == sessionTerminationMsg && rec["reason"] == reason.String() {
			return rec, true
		}
	}
	return nil, false
}

// TestSessionLoopRepeatedToolCalls drives the real session loop through the same
// tool call on every turn and asserts the canonical repeated-tool-call behavior:
// a repeated_tool_call_loop event is emitted, and the terminating
// session_termination event (max_turns_reached, since detection is observability
// only and does not break the loop) carries the repeated_tool_call signal.
func TestSessionLoopRepeatedToolCalls(t *testing.T) {
	logs := captureLogs(t)

	// The same tool call, returned on every turn (the stub repeats its last
	// scripted response once the queue is exhausted).
	sameCall := stubToolCall("repeat-probe", `{}`)
	srv := newStubInferenceServer(t, sameCall)

	probe := &repeatProbeTool{}
	deps := harnessDeps(srv)
	deps.reg.Register(probe)

	proc := newConversationProcessor(deps)
	if _, err := proc(context.Background(), "sess-repeat", "", "keep going", nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The loop must have actually dispatched the tool across rounds.
	if got := atomic.LoadInt64(&probe.calls); got < repeatedToolCallThreshold {
		t.Fatalf("probe called %d times, want >= %d", got, repeatedToolCallThreshold)
	}

	// 1) The repeated-tool-call loop was detected and logged.
	if !logs.hasLogMsg("repeated_tool_call_loop") {
		t.Errorf("expected a repeated_tool_call_loop event; reasons seen: %v", logs.terminationReasons())
	}

	// 2) The terminating event carries the repeated_tool_call signal. With
	// detection being observability-only, the loop runs to the turn limit, so the
	// terminating reason is max_turns_reached.
	entry, ok := logs.terminationEntry(terminationMaxTurns)
	if !ok {
		t.Fatalf("expected a session_termination with reason %q; reasons seen: %v",
			terminationMaxTurns, logs.terminationReasons())
	}
	if signal, _ := entry["repeated_tool_call"].(bool); !signal {
		t.Errorf("session_termination missing repeated_tool_call=true signal; entry: %v", entry)
	}
}
