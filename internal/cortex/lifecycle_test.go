package cortex

import "testing"

func TestCheckTransitionTable(t *testing.T) {
	ok := Cause{Kind: CauseWebhook}
	legal := []struct{ from, to string }{
		{"", StatusQueued},
		{StatusQueued, StatusDispatched},
		{StatusQueued, StatusFailed},
		{StatusDispatched, StatusBuilding},
		{StatusDispatched, StatusFailed},
		{StatusBuilding, StatusReview},
		{StatusBuilding, StatusFailed},
		{StatusReview, StatusApproved},
		{StatusReview, StatusFailed},
		{StatusApproved, StatusMerged},
		{StatusApproved, StatusFailed},
		{StatusMerged, StatusValidated},
		{StatusFailed, StatusDispatched}, // retry
	}
	for _, c := range legal {
		if err := CheckTransition(c.from, c.to, ok); err != nil {
			t.Errorf("legal transition %q->%q rejected: %v", c.from, c.to, err)
		}
	}
	illegal := []struct{ from, to string }{
		{StatusQueued, StatusBuilding},   // skipping dispatch
		{StatusBuilding, StatusApproved}, // skipping review
		{StatusValidated, StatusFailed},  // terminal
		{StatusMerged, StatusFailed},     // merged only validates
		{StatusFailed, StatusQueued},     // retry goes to dispatched
		{"", StatusDispatched},           // creation is queued only
	}
	for _, c := range illegal {
		if err := CheckTransition(c.from, c.to, ok); err == nil {
			t.Errorf("illegal transition %q->%q accepted", c.from, c.to)
		}
	}
}

func TestCheckTransitionRejectsInvalidCause(t *testing.T) {
	if err := CheckTransition(StatusQueued, StatusDispatched, Cause{Kind: "model"}); err == nil {
		t.Fatal("cause kind 'model' must be rejected — there is no cause kind for model output")
	}
	if err := CheckTransition(StatusQueued, StatusDispatched, Cause{}); err == nil {
		t.Fatal("empty cause kind must be rejected")
	}
}

func TestIsValidStatus(t *testing.T) {
	for _, s := range []string{StatusQueued, StatusDispatched, StatusBuilding, StatusReview,
		StatusApproved, StatusMerged, StatusValidated, StatusFailed} {
		if !IsValidStatus(s) {
			t.Errorf("IsValidStatus(%q) = false", s)
		}
	}
	if IsValidStatus("") || IsValidStatus("done") {
		t.Error("invalid statuses accepted")
	}
}
