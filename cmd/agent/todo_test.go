package main

import (
	"testing"
)

func TestParseTodoItems(t *testing.T) {
	raw := `[ ] read the files
[~] extract the helper
[x] run the tests
- [ ] commit and push
1. open the PR
just a bare line`
	items := parseTodoItems(raw)
	if len(items) != 6 {
		t.Fatalf("expected 6 items, got %d: %+v", len(items), items)
	}
	wants := []struct {
		content string
		status  todoStatus
	}{
		{"read the files", todoPending},
		{"extract the helper", todoInProgress},
		{"run the tests", todoCompleted},
		{"commit and push", todoPending},
		{"open the PR", todoPending},
		{"just a bare line", todoPending},
	}
	for i, w := range wants {
		if items[i].Content != w.content || items[i].Status != w.status {
			t.Errorf("item %d = %+v, want {%q %s}", i, items[i], w.content, w.status)
		}
	}
}

func TestTodoStoreOpenItemsAndRemaining(t *testing.T) {
	s := &todoStore{lists: map[string][]todoItem{}}
	sid := "sess-todo"
	if s.hasOpenItems(sid) {
		t.Error("empty store should have no open items")
	}
	s.set(sid, []todoItem{
		{"a", todoCompleted},
		{"b", todoInProgress},
		{"c", todoPending},
	})
	if !s.hasItems(sid) {
		t.Error("expected hasItems true")
	}
	if !s.hasOpenItems(sid) {
		t.Error("expected open items")
	}
	rem := s.remaining(sid)
	if len(rem) != 2 || rem[0] != "b" || rem[1] != "c" {
		t.Errorf("remaining = %v, want [b c]", rem)
	}
	// All complete -> no open items.
	s.set(sid, []todoItem{{"a", todoCompleted}, {"b", todoCompleted}})
	if s.hasOpenItems(sid) {
		t.Error("all-completed list should have no open items")
	}
	s.clear(sid)
	if s.hasItems(sid) {
		t.Error("cleared store should be empty")
	}
}

func TestTodoStoreRender(t *testing.T) {
	s := &todoStore{lists: map[string][]todoItem{}}
	sid := "sess-render"
	if s.render(sid) != "" {
		t.Error("empty render should be blank")
	}
	s.set(sid, []todoItem{{"do a", todoCompleted}, {"do b", todoPending}})
	got := s.render(sid)
	want := "[x] do a\n[ ] do b"
	if got != want {
		t.Errorf("render = %q, want %q", got, want)
	}
}

func TestContentHasTerminalOutcome(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"Done. https://git.local/kit/repo/pulls/7", true}, // PR URL
		{"opened PR #12", true},                            // PR #N
		{"FAILED: cannot build", true},
		{"NOOP: already correct", true},
		{"QUESTION: which base branch should I target?", true}, // fail-open
		{"BLOCKED: the issue is ambiguous", true},
		{"INPUT-NEEDED: need the API key", true},
		{"I think I'm done now", false},
		{"", false},
	}
	for _, c := range cases {
		if got := contentHasTerminalOutcome(c.in); got != c.want {
			t.Errorf("contentHasTerminalOutcome(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestContentHasTerminalOutcomeNotStandardCompletion(t *testing.T) {
	// A fail-open question is a terminal OUTCOME but not a completion SIGNAL —
	// the two must stay distinct so anti-dilution holds.
	q := "QUESTION: which module owns this?"
	if contentHasCompletionSignal(q) {
		t.Error("a question must not count as a completion signal")
	}
	if !contentHasTerminalOutcome(q) {
		t.Error("a question must count as a terminal outcome (fail open)")
	}
}

func TestTodoToolExecuteStoresAndReports(t *testing.T) {
	sessionTodos.clear("sess-tool")
	tool := &todoTool{}
	res := tool.Execute(map[string]interface{}{
		"_session_id": "sess-tool",
		"items":       "[x] read files\n[ ] extract helper\n[ ] open the PR",
	})
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	items := sessionTodos.get("sess-tool")
	if len(items) != 3 {
		t.Fatalf("expected 3 stored items, got %d", len(items))
	}
	if !sessionTodos.hasOpenItems("sess-tool") {
		t.Error("expected open items after write")
	}
	sessionTodos.clear("sess-tool")
}

func TestTodoToolExecuteRejectsEmpty(t *testing.T) {
	tool := &todoTool{}
	if res := tool.Execute(map[string]interface{}{"_session_id": "x", "items": "   "}); res.Error == "" {
		t.Error("expected error for empty items")
	}
	if res := tool.Execute(map[string]interface{}{"_session_id": "x", "items": "\n\n  \n"}); res.Error == "" {
		t.Error("expected error when only blank lines are sent")
	}
	sessionTodos.clear("x")
}

func TestReanchorReminderEmptyWithoutPlan(t *testing.T) {
	sessionTodos.clear("sess-empty")
	if reanchorReminder("sess-empty") != "" {
		t.Error("reanchor should be empty when no plan exists")
	}
	sessionTodos.set("sess-empty", []todoItem{{"open the PR", todoPending}})
	if reanchorReminder("sess-empty") == "" {
		t.Error("reanchor should surface an existing plan")
	}
	sessionTodos.clear("sess-empty")
}
