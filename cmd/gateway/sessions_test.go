package main

import (
	"context"
	"testing"
)

func TestExtractObjectiveSentence(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "task header uses first line",
			input: "TASK: Coordinate rollout of PR review improvements\nSTEPS:\n1. Delegate builder work",
			want:  "Coordinate rollout of PR review improvements",
		},
		{
			name:  "uses first sentence",
			input: "Coordinate the next release. Then fan out work to builders.",
			want:  "Coordinate the next release.",
		},
		{
			name:  "skips footer metadata",
			input: "FROM: rune\nTASK_ID: sess-1\nGATES: none",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractObjectiveSentence(tt.input); got != tt.want {
				t.Fatalf("extractObjectiveSentence()=%q want %q", got, tt.want)
			}
		})
	}
}

func TestObjectiveFromSessionPrefersFirstUserMessage(t *testing.T) {
	sess := &Session{
		TaskSummary: "fallback summary",
		Messages: []ChatMessage{
			{Role: "assistant", Content: "not this one"},
			{Role: "user", Content: "Coordinate the gateway UI rollout. Then verify production."},
			{Role: "user", Content: "second user message"},
		},
	}

	if got := objectiveFromSession(sess); got != "Coordinate the gateway UI rollout." {
		t.Fatalf("objectiveFromSession()=%q", got)
	}
}

func TestObjectiveFromSessionFallsBackToTaskSummary(t *testing.T) {
	sess := &Session{TaskSummary: "TASK: Ship the architect surface"}
	if got := objectiveFromSession(sess); got != "Ship the architect surface" {
		t.Fatalf("objectiveFromSession()=%q", got)
	}
}

func TestActiveSessionID(t *testing.T) {
	gw := &gateway{activeRequests: map[string]*ActiveRequest{}}
	if got := gw.activeSessionID("rune"); got != "" {
		t.Fatalf("activeSessionID()=%q want empty", got)
	}
	gw.activeRequests["rune"] = &ActiveRequest{
		Agent:     "rune",
		SessionID: "sess-9",
		Cancel:    func() {},
	}
	if got := gw.activeSessionID("rune"); got != "sess-9" {
		t.Fatalf("activeSessionID()=%q want %q", got, "sess-9")
	}
}

func TestSetAndClearActiveRequest(t *testing.T) {
	gw := &gateway{activeRequests: map[string]*ActiveRequest{}}
	gw.setActiveRequest("rune", "sess-1", context.CancelFunc(func() {}))
	if got := gw.activeSessionID("rune"); got != "sess-1" {
		t.Fatalf("active session after set = %q", got)
	}
	gw.clearActiveRequest("rune", nil)
	if got := gw.activeSessionID("rune"); got != "" {
		t.Fatalf("active session after clear = %q", got)
	}
}

// stopAgent / stopAllAgents must tolerate a nil Cancel — delegation_started
// events register an ActiveRequest via setActiveRequest(target, sid, nil),
// and previously stopAgent unconditionally dereferenced ar.Cancel.
func TestStopAgentToleratesNilCancel(t *testing.T) {
	gw := &gateway{activeRequests: map[string]*ActiveRequest{}}
	gw.setActiveRequest("rune", "sess-1", nil)
	if !gw.stopAgent("rune") {
		t.Fatalf("stopAgent returned false for registered agent")
	}
	if got := gw.activeSessionID("rune"); got != "" {
		t.Fatalf("agent still active after stop: %q", got)
	}
}

func TestStopAllAgentsToleratesNilCancel(t *testing.T) {
	gw := &gateway{activeRequests: map[string]*ActiveRequest{}}
	gw.setActiveRequest("rune", "sess-1", nil)
	cancelled := false
	gw.setActiveRequest("ivar", "sess-2", context.CancelFunc(func() { cancelled = true }))
	if got := gw.stopAllAgents(); got != 2 {
		t.Fatalf("stopAllAgents returned %d, want 2", got)
	}
	if !cancelled {
		t.Fatalf("non-nil Cancel was not invoked")
	}
	if len(gw.activeRequests) != 0 {
		t.Fatalf("activeRequests not cleared: %v", gw.activeRequests)
	}
}
