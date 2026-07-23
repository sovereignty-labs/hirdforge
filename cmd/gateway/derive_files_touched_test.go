package main

import (
	"testing"

	workspacepkg "git.hirdforge.com/kit/hirdforge/pkg/workspace"
)

// TestToolCallDerivationPopulatesFilesTouched simulates the /api/v1/events
// handler path for a write tool_call and asserts the projector's FilesTouched
// ends up populated. Pinned here so a regression in deriveTypedEventFromToolCall
// or applyWorkspaceEvent doesn't silently leave the workspace surface dark.
func TestToolCallDerivationPopulatesFilesTouched(t *testing.T) {
	gw := newTestGatewayForEvents()
	in := postedEvent{
		Type:  "tool_call",
		Agent: "warrior",
		Metadata: map[string]interface{}{
			"tool_call": map[string]interface{}{
				"name": "write",
				"arguments": map[string]interface{}{
					"path":    "cmd/test.go",
					"content": "package main\n",
				},
			},
			"session_id": "sess-1",
			"task_id":    "task-1",
		},
	}
	applyPostedEvent(gw, in)
	assertFilesTouched(t, gw, "warrior", "cmd/test.go", "writing")
}

// TestToolCallDerivationFlatShapePopulatesFilesTouched covers the pre-#248
// agent shape where tool name + args are flat top-level fields on the
// metadata (sseChunk json tags `tool` and `args`) rather than nested under
// `tool_call.{name, arguments}`. Without dual-shape support in the derive
// function, those agents silently leave the workspace projector empty.
func TestToolCallDerivationFlatShapePopulatesFilesTouched(t *testing.T) {
	gw := newTestGatewayForEvents()
	in := postedEvent{
		Type:  "tool_call",
		Agent: "warrior",
		Metadata: map[string]interface{}{
			"tool": "write",
			"args": map[string]interface{}{
				"path":    "cmd/flat.go",
				"content": "package main\n",
			},
			"session_id": "sess-1",
			"task_id":    "task-1",
			"done":       false,
		},
	}
	applyPostedEvent(gw, in)
	assertFilesTouched(t, gw, "warrior", "cmd/flat.go", "writing")
}

// TestToolCallDerivationEditNestedShape covers the edit tool variant —
// projector should mark the path "writing" the same as for write.
func TestToolCallDerivationEditNestedShape(t *testing.T) {
	gw := newTestGatewayForEvents()
	in := postedEvent{
		Type:  "tool_call",
		Agent: "warrior",
		Metadata: map[string]interface{}{
			"tool_call": map[string]interface{}{
				"name": "edit",
				"arguments": map[string]interface{}{
					"path":    "cmd/edit.go",
					"old_str": "foo",
					"new_str": "bar",
				},
			},
			"session_id": "sess-1",
			"task_id":    "task-1",
		},
	}
	applyPostedEvent(gw, in)
	assertFilesTouched(t, gw, "warrior", "cmd/edit.go", "writing")
}

type postedEvent struct {
	Type     string
	Agent    string
	Metadata map[string]interface{}
}

func newTestGatewayForEvents() *gateway {
	return &gateway{
		sessionStore:        newSessionStore(),
		projector:           workspacepkg.NewProjector(),
		activeRequests:      map[string]*ActiveRequest{},
		delegationTimelines: map[string][]delegationTimelineEvent{},
	}
}

// applyPostedEvent mirrors exactly what the /api/v1/events handler does at
// delegation.go:246-269 — build the eventMap from in.Metadata, apply the raw
// event (no-op for tool_call), then derive a typed event and apply it.
func applyPostedEvent(gw *gateway, in postedEvent) {
	eventMap := map[string]interface{}{
		"type":  in.Type,
		"agent": in.Agent,
	}
	for k, v := range in.Metadata {
		if k == "type" || k == "agent" {
			continue
		}
		eventMap[k] = v
	}
	gw.applyWorkspaceEvent(in.Agent, eventMap)
	if in.Type == "tool_call" {
		if typed, ok := deriveTypedEventFromToolCall(eventMap); ok {
			gw.applyWorkspaceEvent(in.Agent, typed)
		}
	}
}

func assertFilesTouched(t *testing.T, gw *gateway, agent, path, wantState string) {
	t.Helper()
	ws := gw.projector.Get(agent)
	state, ok := ws.FilesTouched[path]
	if !ok {
		t.Fatalf("FilesTouched missing %q; got keys: %v", path, mapKeys(ws.FilesTouched))
	}
	if state.State != wantState {
		t.Fatalf("FilesTouched[%q].State = %q, want %q", path, state.State, wantState)
	}
}

func mapKeys(m map[string]workspacepkg.FileState) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
