package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
)

// TestSessionLoopMaxTurnsWithoutRepeatSignal drives the real session loop through
// the full turn limit with a *different* tool call on every round, and asserts
// the runtime reports an ordinary max_turns_reached termination — distinct from
// the repeated_tool_call_loop case in repeated_tool_loop_harness_test.go.
//
// Because the repeated-tool-call detector needs `repeatedToolCallThreshold`
// identical consecutive calls, varying the arguments each round keeps it from
// tripping, so the terminating event must carry no repeated_tool_call signal.
func TestSessionLoopMaxTurnsWithoutRepeatSignal(t *testing.T) {
	logs := captureLogs(t)

	// Discover the configured turn limit so the script always spans every round.
	rounds := harnessDeps(newStubInferenceServer(t)).maxToolRounds
	if rounds < repeatedToolCallThreshold {
		t.Fatalf("harness maxToolRounds=%d too small to exercise this case", rounds)
	}

	// One distinct tool call per round (plus spares for the trailing
	// streaming/recovery inference calls): same tool, different arguments, so no
	// run of identical consecutive calls ever forms.
	var scripted []stubResponse
	for i := 0; i < rounds+2; i++ {
		scripted = append(scripted, stubToolCall("repeat-probe", fmt.Sprintf(`{"n":%d}`, i)))
	}
	srv := newStubInferenceServer(t, scripted...)

	probe := &repeatProbeTool{}
	deps := harnessDeps(srv)
	deps.reg.Register(probe)

	proc := newConversationProcessor(deps)
	if _, err := proc(context.Background(), "sess-maxturns", "", "keep going", nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The tool was actually dispatched across rounds.
	if got := atomic.LoadInt64(&probe.calls); got < int64(repeatedToolCallThreshold) {
		t.Fatalf("probe called %d times, want >= %d", got, repeatedToolCallThreshold)
	}

	// A max_turns_reached termination was emitted...
	entry, ok := logs.terminationEntry(terminationMaxTurns)
	if !ok {
		t.Fatalf("expected session_termination reason %q; reasons seen: %v",
			terminationMaxTurns, logs.terminationReasons())
	}

	// ...with no repeated-tool-call signal (false or absent).
	if signal, present := entry["repeated_tool_call"].(bool); present && signal {
		t.Errorf("repeated_tool_call must be false/absent on ordinary max_turns; entry: %v", entry)
	}

	// And no repeated_tool_call_loop event at all.
	if logs.hasLogMsg("repeated_tool_call_loop") {
		t.Errorf("did not expect a repeated_tool_call_loop event for distinct tool calls")
	}
}
