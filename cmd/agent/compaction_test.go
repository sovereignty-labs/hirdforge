package main

import (
	"strings"
	"testing"
)

// buildHistory makes a system message, a task statement, then n filler
// user/assistant pairs — the shape of a long one-shot run.
func buildHistory(n int) []message {
	msgs := []message{
		{Role: "system", Content: "PERSONA + PROCEDURE"},
		{Role: "user", Content: "TASK: add the Foo helper. DONE WHEN: PR opened."},
	}
	for i := 0; i < n; i++ {
		msgs = append(msgs,
			message{Role: "assistant", Content: "step"},
			message{Role: "user", Content: "tool result"},
		)
	}
	return msgs
}

// TestCompactionPinsTaskStatement is the P3.3 correctness fix: on a long TASK-mode
// run the sliding window must never drop the task statement. Before this, only
// messages[0] (the generic procedure) was pinned, so the agent could keep "how to
// work" while losing "what to do".
func TestCompactionPinsTaskStatement(t *testing.T) {
	msgs := buildHistory(40)
	out := compactIfNeeded(msgs, 10, "Builder", "sess-1", "task-1")

	if len(out) >= len(msgs) {
		t.Fatalf("expected compaction to drop messages: in=%d out=%d", len(msgs), len(out))
	}
	if out[0].Content != "PERSONA + PROCEDURE" {
		t.Fatalf("system prompt not pinned: %q", out[0].Content)
	}
	if !strings.Contains(out[1].Content, "TASK: add the Foo helper") {
		t.Fatalf("TASK STATEMENT was trimmed away — the agent lost its requirements: %q", out[1].Content)
	}
}

// TestCompactionAnnouncesTheLoss pins the observability half: a dropped span is
// never silent — the model is told, so it re-reads instead of reasoning from a
// hole (doctrine: degrade never silently).
func TestCompactionAnnouncesTheLoss(t *testing.T) {
	out := compactIfNeeded(buildHistory(40), 10, "Builder", "sess-2", "task-2")
	last := out[len(out)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "Context limit reached") {
		t.Fatalf("compaction must announce the loss to the model, got %q: %q", last.Role, last.Content)
	}
	for _, want := range []string{"were dropped", "do not assume", "re-read"} {
		if !strings.Contains(last.Content, want) {
			t.Errorf("notice missing %q: %q", want, last.Content)
		}
	}
}

// TestCompactionNoOpUnderBudget: below the threshold nothing is touched — no
// dropped context, no spurious notice.
func TestCompactionNoOpUnderBudget(t *testing.T) {
	msgs := buildHistory(2)
	out := compactIfNeeded(msgs, 20, "Builder", "sess-3", "task-3")
	if len(out) != len(msgs) {
		t.Fatalf("under budget must be a no-op: in=%d out=%d", len(msgs), len(out))
	}
	// Disabled budget is also a no-op.
	if got := compactIfNeeded(buildHistory(40), 0, "Builder", "sess-3", "task-3"); len(got) != len(buildHistory(40)) {
		t.Fatal("maxContext=0 must disable compaction entirely")
	}
}

// TestCompactionConversationModePinsSystemOnly: outside task mode (no taskID)
// there is no task statement to protect, so only the system prompt is pinned.
func TestCompactionConversationModePinsSystemOnly(t *testing.T) {
	out := compactIfNeeded(buildHistory(40), 10, "chuck", "sess-4", "")
	if out[0].Content != "PERSONA + PROCEDURE" {
		t.Fatalf("system prompt not pinned: %q", out[0].Content)
	}
	if strings.Contains(out[len(out)-1].Content, "task statement") {
		t.Error("conversation mode should not claim a pinned task statement")
	}
}
