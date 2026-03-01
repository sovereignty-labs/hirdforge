package tasklife

import (
	"errors"
	"sync"
	"testing"
)

func TestTaskTrackerDispatchBlocksDuplicateWorkingAgent(t *testing.T) {
	tracker := NewTaskTracker()
	if _, err := tracker.Dispatch("task-1", "agent-a"); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if _, err := tracker.Dispatch("task-2", "agent-a"); !errors.Is(err, ErrDuplicateDispatch) {
		t.Fatalf("Dispatch() error = %v, want %v", err, ErrDuplicateDispatch)
	}
}

func TestTaskTrackerCompleteWithAndWithoutPR(t *testing.T) {
	tracker := NewTaskTracker()
	record, err := tracker.Dispatch("task-1", "agent-a")
	if err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if record.State != StateWorking {
		t.Fatalf("Dispatch() state = %q, want %q", record.State, StateWorking)
	}

	record, err = tracker.Complete("task-1", "opened PR #9", true)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if record.State != StateCompleted {
		t.Fatalf("Complete() state = %q, want %q", record.State, StateCompleted)
	}
	if got := record.History; len(got) != 4 || got[2] != StateGateCheck || got[3] != StateCompleted {
		t.Fatalf("unexpected history: %+v", got)
	}

	if _, err := tracker.Dispatch("task-2", "agent-a"); err != nil {
		t.Fatalf("Dispatch() after completion error = %v", err)
	}
	record, err = tracker.Complete("task-2", "no PR linked", false)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if record.State != StateFailedNoPR {
		t.Fatalf("Complete() state = %q, want %q", record.State, StateFailedNoPR)
	}
}

func TestTaskTrackerNudge(t *testing.T) {
	tracker := NewTaskTracker()
	if _, err := tracker.Dispatch("task-1", "agent-a"); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	record, err := tracker.Nudge("task-1", "Please include the PR URL.")
	if err != nil {
		t.Fatalf("Nudge() error = %v", err)
	}
	if record.State != StateNudged {
		t.Fatalf("Nudge() state = %q, want %q", record.State, StateNudged)
	}
	if record.Nudge == "" {
		t.Fatalf("expected nudge message to be stored")
	}
}

func TestTaskTrackerConcurrentReads(t *testing.T) {
	tracker := NewTaskTracker()
	if _, err := tracker.Dispatch("task-1", "agent-a"); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := tracker.Task("task-1"); !ok {
				t.Errorf("Task() returned missing record")
			}
		}()
	}
	wg.Wait()
}
