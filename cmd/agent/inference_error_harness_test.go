package main

import (
	"context"
	"net/http"
	"testing"
)

// These full session-loop cases drive the real newConversationProcessor against
// the deterministic inference stub when the backend fails on the very first
// inference call, and assert the canonical session_termination reason. The loop
// classifies the failure and returns ("", err), so each test expects a non-nil
// error alongside the logged termination event.

func TestSessionLoopContextExhaustion(t *testing.T) {
	logs := captureLogs(t)
	srv := newStubInferenceServer(t, stubContextLengthError())

	deps := harnessDeps(srv)
	proc := newConversationProcessor(deps)
	out, err := proc(context.Background(), "sess-ctxlen", "", "summarize this huge thing", nil, nil)
	if err == nil {
		t.Fatal("expected an inference error, got nil")
	}
	if out != "" {
		t.Errorf("expected empty content on error, got %q", out)
	}

	if !logs.hasTerminationReason(terminationContextExhaustion) {
		t.Errorf("expected session_termination reason %q; reasons seen: %v",
			terminationContextExhaustion, logs.terminationReasons())
	}
	// A context-length overflow must not be misreported as a generic error.
	if logs.hasTerminationReason(terminationInferenceError) {
		t.Errorf("context overflow misclassified as inference_error; reasons seen: %v",
			logs.terminationReasons())
	}
}

func TestSessionLoopInferenceError(t *testing.T) {
	logs := captureLogs(t)
	srv := newStubInferenceServer(t, stubHTTPError(http.StatusInternalServerError, "upstream exploded"))

	deps := harnessDeps(srv)
	proc := newConversationProcessor(deps)
	out, err := proc(context.Background(), "sess-inferr", "", "do the thing", nil, nil)
	if err == nil {
		t.Fatal("expected an inference error, got nil")
	}
	if out != "" {
		t.Errorf("expected empty content on error, got %q", out)
	}

	if !logs.hasTerminationReason(terminationInferenceError) {
		t.Errorf("expected session_termination reason %q; reasons seen: %v",
			terminationInferenceError, logs.terminationReasons())
	}
	// A plain backend failure must not be misreported as context exhaustion.
	if logs.hasTerminationReason(terminationContextExhaustion) {
		t.Errorf("generic backend error misclassified as context_exhaustion; reasons seen: %v",
			logs.terminationReasons())
	}
}
