package tasks

import "testing"

func TestStorePreservesSessionID(t *testing.T) {
	store := NewStore()
	created := store.Create(Task{
		ID:        "task-1",
		Agent:     "ivar",
		From:      "rune",
		SessionID: "sess-42",
		Content:   "TASK: Update gateway",
		Status:    "submitted",
	})

	if created.SessionID != "sess-42" {
		t.Fatalf("created.SessionID = %q", created.SessionID)
	}
	got, ok := store.Get("task-1")
	if !ok {
		t.Fatalf("expected task to exist")
	}
	if got.SessionID != "sess-42" {
		t.Fatalf("stored SessionID = %q", got.SessionID)
	}
	list := store.List("", "")
	if len(list) != 1 || list[0].SessionID != "sess-42" {
		t.Fatalf("listed tasks = %+v", list)
	}
}
