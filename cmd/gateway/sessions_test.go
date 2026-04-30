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
