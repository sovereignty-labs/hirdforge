package tools

import "testing"

func TestTypedToolStartEventsPlan(t *testing.T) {
	events := TypedToolStartEvents("plan", map[string]interface{}{
		"steps": []interface{}{"read trigger context", "draft reply", "queue for approval"},
	}, "/tmp")
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
	}, "/tmp")
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
