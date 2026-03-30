package tasklife

import (
	"strings"
	"testing"
)

func TestValidateDelegation(t *testing.T) {
	input := `TASK: Fix the flaky gateway notification path
ISSUE: Dashboard clients are missing updates.
STEPS:
1. Add notification storage.
2. Broadcast payloads to websocket clients.
DONE WHEN: Polling and websocket delivery both work.`

	got, err := ValidateDelegation(input)
	if err != nil {
		t.Fatalf("ValidateDelegation() error = %v", err)
	}
	if got.Task == "" || got.DoneWhen == "" || len(got.Steps) != 2 {
		t.Fatalf("unexpected parsed delegation: %+v", got)
	}
}

func TestFormatDelegationDropsIssueWhenOverTokenLimit(t *testing.T) {
	format := DelegationFormat{
		Task:     "Ship the gateway notification path",
		Issue:    strings.Repeat("verbose ", 40),
		Steps:    []string{"Add POST endpoint", "Add GET endpoint", "Push websocket updates"},
		DoneWhen: "Notifications are stored and delivered.",
	}

	out := FormatDelegation(format, 30)
	if strings.Contains(out, "ISSUE:") {
		t.Fatalf("expected ISSUE section to be removed when over limit:\n%s", out)
	}
	if !strings.Contains(out, "TASK:") || !strings.Contains(out, "STEPS:") || !strings.Contains(out, "DONE WHEN:") {
		t.Fatalf("missing required sections:\n%s", out)
	}
}

func TestValidateDelegationAcceptsUnstructuredInput(t *testing.T) {
	// ValidateDelegation now accepts any non-empty string.
	// Missing TASK:/STEPS:/DONE WHEN: headers should NOT cause an error.
	// The full input should be returned in the Task field.
	input := "Do the thing and make sure it works"
	got, err := ValidateDelegation(input)
	if err != nil {
		t.Fatalf("ValidateDelegation() unexpected error = %v", err)
	}
	if got.Task != input {
		t.Fatalf("expected Task to be full input, got %q, want %q", got.Task, input)
	}
	if got.DoneWhen != "" {
		t.Fatalf("expected empty DoneWhen, got %q", got.DoneWhen)
	}
	if len(got.Steps) != 0 {
		t.Fatalf("expected empty Steps, got %v", got.Steps)
	}
}

func TestValidateDelegationHandlesDoubleEncodedNewlines(t *testing.T) {
	// Simulates what happens when a multi-line message is JSON-encoded twice:
	// the newlines become literal backslash-n sequences.
	// This is the root cause of "invalid delegation format: missing DONE WHEN"
	// errors on ~50% of dispatch attempts.
	input := "TASK: Fix the delegate tool\nSTEPS:\n1. Investigate the issue\n2. Write a test\nDONE WHEN: Delegate no longer fails on multi-line messages."

	got, err := ValidateDelegation(input)
	if err != nil {
		t.Fatalf("ValidateDelegation() error = %v", err)
	}
	if got.Task == "" || got.DoneWhen == "" || len(got.Steps) != 2 {
		t.Fatalf("unexpected parsed delegation: %+v", got)
	}
}
