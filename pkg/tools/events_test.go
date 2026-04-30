package tools

import "testing"

func TestTypedToolStartEventsPlan(t *testing.T) {
	events := TypedToolStartEvents("plan", map[string]interface{}{
		"steps": []interface{}{"read trigger context", "draft reply", "queue for approval"},
	}, "/tmp", ToolEventContext{})
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	event, ok := events[0].(PlanEvent)
	if !ok {
		t.Fatalf("expected PlanEvent, got %T", events[0])
	}
	if event.Type != "plan" {
		t.Fatalf("Type = %q", event.Type)
	}
	if len(event.Steps) != 3 || event.Steps[1] != "draft reply" {
		t.Fatalf("Steps = %#v", event.Steps)
	}
}

func TestTypedToolStartEventsPlanStepComplete(t *testing.T) {
	events := TypedToolStartEvents("plan-step-complete", map[string]interface{}{
		"step": float64(2),
	}, "/tmp", ToolEventContext{})
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	event, ok := events[0].(PlanStepCompleteEvent)
	if !ok {
		t.Fatalf("expected PlanStepCompleteEvent, got %T", events[0])
	}
	if event.Type != "plan_step_complete" || event.Step != 2 {
		t.Fatalf("event = %+v", event)
	}
}

func TestTypedToolStartEventsDelegate(t *testing.T) {
	events := TypedToolStartEvents("delegate", map[string]interface{}{
		"agent":    "ivar",
		"task":     "TASK: Update kit/hirdforge RBAC manifests\nSTEPS:\n1. Inspect current files\nDONE WHEN: PR is open",
		"_task_id": "sess-123",
	}, "/tmp", ToolEventContext{AgentName: "rune"})
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	event, ok := events[0].(DelegateEvent)
	if !ok {
		t.Fatalf("expected DelegateEvent, got %T", events[0])
	}
	if event.Type != "delegate" {
		t.Fatalf("Type = %q", event.Type)
	}
	if event.FromAgent != "rune" || event.ToAgent != "ivar" {
		t.Fatalf("event agents = %+v", event)
	}
	if event.SessionID != "sess-123" {
		t.Fatalf("SessionID = %q", event.SessionID)
	}
	if event.ObjectiveSummary != "Update kit/hirdforge RBAC manifests" {
		t.Fatalf("ObjectiveSummary = %q", event.ObjectiveSummary)
	}
	if event.TargetRepo != "kit/hirdforge" {
		t.Fatalf("TargetRepo = %q", event.TargetRepo)
	}
}
