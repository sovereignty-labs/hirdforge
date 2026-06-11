package main

import (
	"context"
	"testing"
)

// These tests drive the real agent inference call path
// (callOllamaNonStreamingWithContext -> /v1/chat/completions) against the
// deterministic stub, then assert the canonical session_termination reason that
// the session loop would derive — using the same classifiers the loop uses
// (classifyModelTurn, classifyInferenceError, hasRepeatedToolCallLoop). No live
// model, GPU, or network is involved.

const stubModel = "qwen" // routes to /v1/chat/completions (not claude/codex)

func stubCall(t *testing.T, srv *stubInferenceServer, userMsg string) (chatResponse, error) {
	t.Helper()
	return callOllamaNonStreamingWithContext(
		context.Background(),
		[]message{{Role: "user", Content: userMsg}},
		nil,
		srv.URL(),
		stubModel,
		"",
	)
}

func TestStubNormalContentCompletes(t *testing.T) {
	srv := newStubInferenceServer(t, stubContent("Done: opened PR #1"))
	resp, err := stubCall(t, srv, "ship it")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	msg := resp.Choices[0].Message
	if msg.Content != "Done: opened PR #1" {
		t.Errorf("content = %q", msg.Content)
	}
	if len(msg.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(msg.ToolCalls))
	}
	if got := classifyModelTurn(msg.Content); got != terminationCompleted {
		t.Errorf("termination reason = %q, want %q", got, terminationCompleted)
	}

	// The stub captured exactly one request with the expected model.
	if srv.callCount() != 1 {
		t.Errorf("callCount = %d, want 1", srv.callCount())
	}
	if req, ok := srv.lastRequest(); !ok || req.Model != stubModel {
		t.Errorf("captured request model = %q, want %q", req.Model, stubModel)
	}
}

func TestStubToolCallParsed(t *testing.T) {
	srv := newStubInferenceServer(t, stubToolCall("git-clone", `{"repo":"hirdforge"}`))
	resp, err := stubCall(t, srv, "clone it")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Choices) != 1 || len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("expected exactly one tool call, got choices=%d", len(resp.Choices))
	}
	tc := resp.Choices[0].Message.ToolCalls[0]
	if tc.Function.Name != "git-clone" {
		t.Errorf("tool name = %q, want git-clone", tc.Function.Name)
	}
	if tc.Function.Arguments != `{"repo":"hirdforge"}` {
		t.Errorf("tool args = %q", tc.Function.Arguments)
	}
}

func TestStubRepeatedToolCallLoopDetected(t *testing.T) {
	// Script the same tool call on every turn; the stub repeats the last entry
	// once exhausted, so the model never stops calling it.
	same := stubToolCall("list-issues", `{"repo":"x"}`)
	srv := newStubInferenceServer(t, same, same, same, same, same)

	var signatures []string
	reason := terminationUnknown
	for round := 0; round < 8; round++ {
		resp, err := stubCall(t, srv, "work")
		if err != nil {
			t.Fatalf("round %d: unexpected error: %v", round, err)
		}
		if len(resp.Choices) == 0 {
			reason = terminationNoActionableOutput
			break
		}
		assistant := resp.Choices[0].Message
		if len(assistant.ToolCalls) == 0 {
			reason = classifyModelTurn(assistant.Content)
			break
		}
		for _, tc := range assistant.ToolCalls {
			signatures = append(signatures, toolCallSignature(tc.Function.Name, tc.Function.Arguments))
		}
		if hasRepeatedToolCallLoop(signatures, repeatedToolCallThreshold) {
			reason = terminationRepeatedToolCall
			break
		}
	}
	if reason != terminationRepeatedToolCall {
		t.Errorf("termination reason = %q, want %q", reason, terminationRepeatedToolCall)
	}
}

func TestStubEmptyOutputIsNoActionable(t *testing.T) {
	srv := newStubInferenceServer(t, stubEmpty())
	resp, err := stubCall(t, srv, "anything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	if got := classifyModelTurn(resp.Choices[0].Message.Content); got != terminationNoActionableOutput {
		t.Errorf("termination reason = %q, want %q", got, terminationNoActionableOutput)
	}
}

func TestStubNoChoicesIsNoActionable(t *testing.T) {
	srv := newStubInferenceServer(t, stubNoChoices())
	resp, err := stubCall(t, srv, "anything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The session loop maps an empty choices array to no_actionable_output.
	if len(resp.Choices) != 0 {
		t.Errorf("expected 0 choices, got %d", len(resp.Choices))
	}
}

func TestStubContextLengthErrorIsExhaustion(t *testing.T) {
	srv := newStubInferenceServer(t, stubContextLengthError())
	_, err := stubCall(t, srv, "a very long prompt")
	if err == nil {
		t.Fatal("expected an inference error, got nil")
	}
	if !isContextLengthError(err) {
		t.Errorf("isContextLengthError(%v) = false, want true", err)
	}
	if got := classifyInferenceError(context.Background().Err(), err); got != terminationContextExhaustion {
		t.Errorf("termination reason = %q, want %q", got, terminationContextExhaustion)
	}
}

func TestStubGenericBackendErrorIsInferenceError(t *testing.T) {
	srv := newStubInferenceServer(t, stubHTTPError(500, "upstream exploded"))
	_, err := stubCall(t, srv, "anything")
	if err == nil {
		t.Fatal("expected an inference error, got nil")
	}
	if isContextLengthError(err) {
		t.Errorf("generic error misclassified as context-length: %v", err)
	}
	if got := classifyInferenceError(nil, err); got != terminationInferenceError {
		t.Errorf("termination reason = %q, want %q", got, terminationInferenceError)
	}
}
