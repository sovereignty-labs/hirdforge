package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestTerminationReasonValidAndDistinct(t *testing.T) {
	seen := map[terminationReason]bool{}
	for _, r := range allTerminationReasons {
		if !r.Valid() {
			t.Errorf("reason %q not reported Valid()", r)
		}
		if r.String() == "" {
			t.Errorf("reason has empty string value")
		}
		if seen[r] {
			t.Errorf("duplicate reason in allTerminationReasons: %q", r)
		}
		seen[r] = true
	}
	if terminationReason("definitely_not_a_reason").Valid() {
		t.Error("unknown reason should not be Valid()")
	}
}

func TestIsContextLengthError(t *testing.T) {
	positives := []string{
		"openai: This model's maximum context length is 8192 tokens",
		"error code 400: context_length_exceeded",
		"the prompt is too long for the context window",
		"input is too long",
		"please reduce the length of the messages",
	}
	for _, msg := range positives {
		if !isContextLengthError(errors.New(msg)) {
			t.Errorf("expected context-length detection for: %q", msg)
		}
	}
	negatives := []string{
		"connection refused",
		"500 internal server error",
		"model not found",
		"",
	}
	for _, msg := range negatives {
		if isContextLengthError(errors.New(msg)) {
			t.Errorf("did not expect context-length detection for: %q", msg)
		}
	}
	if isContextLengthError(nil) {
		t.Error("nil error must not be a context-length error")
	}
}

func TestClassifyInferenceError(t *testing.T) {
	cases := []struct {
		name   string
		ctxErr error
		err    error
		want   terminationReason
	}{
		{"canceled context", context.Canceled, errors.New("request canceled"), terminationContextCanceled},
		{"deadline exceeded", context.DeadlineExceeded, errors.New("timeout"), terminationContextCanceled},
		{"wrapped canceled err", nil, fmt.Errorf("call failed: %w", context.Canceled), terminationContextCanceled},
		{"context length", nil, errors.New("maximum context length is 8192 tokens"), terminationContextExhaustion},
		{"generic backend error", nil, errors.New("502 bad gateway"), terminationInferenceError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyInferenceError(c.ctxErr, c.err); got != c.want {
				t.Errorf("classifyInferenceError(%v, %v) = %q, want %q", c.ctxErr, c.err, got, c.want)
			}
		})
	}
}

func TestClassifyModelTurn(t *testing.T) {
	if got := classifyModelTurn("Here is the PR: #42"); got != terminationCompleted {
		t.Errorf("non-empty content should be completed, got %q", got)
	}
	for _, blank := range []string{"", "   ", "\n\t  \n"} {
		if got := classifyModelTurn(blank); got != terminationNoActionableOutput {
			t.Errorf("blank content %q should be no_actionable_output, got %q", blank, got)
		}
	}
}

func TestToolCallSignature(t *testing.T) {
	a := toolCallSignature("git-clone", `{"repo":"hirdforge"}`)
	b := toolCallSignature("  git-clone ", `  {"repo":"hirdforge"}  `)
	if a != b {
		t.Errorf("signatures should ignore surrounding whitespace: %q vs %q", a, b)
	}
	if toolCallSignature("git-clone", `{"repo":"a"}`) == toolCallSignature("git-clone", `{"repo":"b"}`) {
		t.Error("different arguments must yield different signatures")
	}
	if toolCallSignature("tool-a", `{}`) == toolCallSignature("tool-b", `{}`) {
		t.Error("different tool names must yield different signatures")
	}
}

func TestHasRepeatedToolCallLoop(t *testing.T) {
	sig := func(args string) string { return toolCallSignature("list-issues", args) }
	same := sig(`{"repo":"x"}`)

	cases := []struct {
		name      string
		history   []string
		threshold int
		want      bool
	}{
		{"empty history", nil, repeatedToolCallThreshold, false},
		{"below threshold", []string{same, same}, repeatedToolCallThreshold, false},
		{"exactly threshold identical", []string{same, same, same}, repeatedToolCallThreshold, true},
		{"threshold identical at tail", []string{sig(`{"a":1}`), same, same, same}, repeatedToolCallThreshold, true},
		{"last differs", []string{same, same, sig(`{"repo":"y"}`)}, repeatedToolCallThreshold, false},
		{"interleaved not consecutive", []string{same, sig(`{"o":1}`), same}, repeatedToolCallThreshold, false},
		{"blank last", []string{"", "", ""}, repeatedToolCallThreshold, false},
		{"threshold below 2 disabled", []string{same, same}, 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasRepeatedToolCallLoop(c.history, c.threshold); got != c.want {
				t.Errorf("hasRepeatedToolCallLoop(%v, %d) = %v, want %v", c.history, c.threshold, got, c.want)
			}
		})
	}
}

func TestTerminationLogLevel(t *testing.T) {
	errorLevel := []terminationReason{terminationInferenceError, terminationContextExhaustion, terminationStallFatal}
	for _, r := range errorLevel {
		if got := terminationLogLevel(r); got != "error" {
			t.Errorf("reason %q expected error level, got %q", r, got)
		}
	}
	warnLevel := []terminationReason{
		terminationMaxTurns, terminationMaxToolCalls, terminationRepeatedToolCall,
		terminationNoActionableOutput, terminationToolErrorsExhausted, terminationContextCanceled,
	}
	for _, r := range warnLevel {
		if got := terminationLogLevel(r); got != "warn" {
			t.Errorf("reason %q expected warn level, got %q", r, got)
		}
	}
	if got := terminationLogLevel(terminationCompleted); got != "info" {
		t.Errorf("completed should log at info, got %q", got)
	}
}
