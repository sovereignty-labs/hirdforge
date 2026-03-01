package tasklife

import "testing"

func TestCheckCompletionGates(t *testing.T) {
	gates := []CompletionGate{
		{Name: "pr", Pattern: `PR #\d+`, Nudge: "Include the PR number."},
		{Name: "tests", Pattern: `tests passed`, Nudge: "Mention test status."},
	}

	result, err := CheckCompletionGates("PR #42 created and tests passed", gates)
	if err != nil {
		t.Fatalf("CheckCompletionGates() error = %v", err)
	}
	if !result.Passed {
		t.Fatalf("expected gates to pass, got %+v", result)
	}
}

func TestCheckCompletionGatesCollectsNudges(t *testing.T) {
	gates := []CompletionGate{
		{Name: "pr", Pattern: `PR #\d+`, Nudge: "Include the PR number."},
		{Name: "tests", Pattern: `tests passed`, Nudge: "Mention test status."},
	}

	result, err := CheckCompletionGates("tests passed", gates)
	if err != nil {
		t.Fatalf("CheckCompletionGates() error = %v", err)
	}
	if result.Passed {
		t.Fatalf("expected gates to fail")
	}
	if len(result.Failed) != 1 || result.Failed[0].Name != "pr" {
		t.Fatalf("unexpected failed gates: %+v", result.Failed)
	}
	if len(result.Nudges) != 1 || result.Nudges[0] != "Include the PR number." {
		t.Fatalf("unexpected nudges: %+v", result.Nudges)
	}
}
