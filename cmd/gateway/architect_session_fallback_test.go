package main

import (
	"testing"

	workspacepkg "github.com/kitporath/project_valhalla/pkg/workspace"
)

func TestApplyWorkspaceEventDelegateFallsBackToActiveSession(t *testing.T) {
	gw := &gateway{
		sessionStore:        newSessionStore(),
		projector:           workspacepkg.NewProjector(),
		activeRequests:      map[string]*ActiveRequest{},
		delegationTimelines: map[string][]delegationTimelineEvent{},
	}
	gw.activeRequests["ragnar"] = &ActiveRequest{
		Agent:     "ragnar",
		SessionID: "sess-42",
	}
	gw.sessionStore.appendMessage("sess-42", "ragnar", ChatMessage{
		Role:    "user",
		Content: "Coordinate the gateway rollout. Then delegate implementation details.",
	})

	gw.applyWorkspaceEvent("ragnar", map[string]interface{}{
		"type":              "delegate",
		"objective_summary": "Implement the diff viewer",
		"to_agent":          "ivar",
		"target_repo":       "kit/hirdforge",
	})

	ws := gw.projector.Get("ragnar")
	if ws.CurrentSessionID != "sess-42" {
		t.Fatalf("CurrentSessionID = %q", ws.CurrentSessionID)
	}
	if ws.CurrentObjective != "Coordinate the gateway rollout." {
		t.Fatalf("CurrentObjective = %q", ws.CurrentObjective)
	}
	events := gw.delegationTimeline("sess-42")
	if len(events) != 1 {
		t.Fatalf("delegation timeline len = %d", len(events))
	}
	if got := asString(events[0].Metadata["session_id"]); got != "sess-42" {
		t.Fatalf("timeline session_id = %q", got)
	}
}
