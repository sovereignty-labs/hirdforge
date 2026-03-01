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

func TestValidateDelegationRejectsMissingSections(t *testing.T) {
	if _, err := ValidateDelegation("TASK: Only task\nDONE WHEN: Eventually"); err == nil {
		t.Fatalf("expected validation error")
	}
}
