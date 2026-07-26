package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The exact shape qwen27b-worker leaked into the cockpit reply (2026-07-26):
// a Hermes/Qwen <tool_call><function=…><parameter=…> block the harness did not
// parse, so it surfaced as raw text instead of executing.
func TestParseQwenToolCalls_LiveLeak(t *testing.T) {
	content := "<tool_call>\n<function=list-issues>\n<parameter=labels>\n\n</parameter>\n<parameter=repo>\nhirdforge\n</parameter>\n<parameter=state>\nopen\n</parameter>\n</function>\n</tool_call>"
	calls, cleaned := parseQwenToolCalls(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Function.Name != "list-issues" {
		t.Fatalf("name = %q, want list-issues", calls[0].Function.Name)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("args not JSON: %v (%s)", err, calls[0].Function.Arguments)
	}
	if args["repo"] != "hirdforge" || args["state"] != "open" {
		t.Fatalf("args wrong: %+v", args)
	}
	if _, ok := args["labels"]; !ok {
		t.Fatalf("empty labels param dropped: %+v", args)
	}
	if strings.Contains(cleaned, "<tool_call>") {
		t.Fatalf("tool-call block not stripped from content: %q", cleaned)
	}
}

// Prose with no tool call is returned untouched.
func TestParseQwenToolCalls_NoCall(t *testing.T) {
	c := "Sure — here are a few ideas we could build."
	calls, cleaned := parseQwenToolCalls(c)
	if calls != nil || cleaned != c {
		t.Fatalf("plain prose disturbed: calls=%v cleaned=%q", calls, cleaned)
	}
}

// Two calls in one turn both parse.
func TestParseQwenToolCalls_Multiple(t *testing.T) {
	c := "<tool_call><function=read><parameter=path>go.mod</parameter></function></tool_call> and " +
		"<tool_call><function=get-issue><parameter=number>466</parameter></function></tool_call>"
	calls, _ := parseQwenToolCalls(c)
	if len(calls) != 2 || calls[0].Function.Name != "read" || calls[1].Function.Name != "get-issue" {
		t.Fatalf("expected read+get-issue, got %+v", calls)
	}
}
