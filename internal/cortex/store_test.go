package cortex

import (
	"os"
	"strings"
	"testing"
	"time"
)

// storeUnderTest runs the same invariant suite against any Store. MemStore
// always runs; PGStore runs when CORTEX_TEST_DB_URL is set (the repo's
// env-gated integration convention, cf. cmd/gateway/a2a_test.go).
func storeInvariantSuite(t *testing.T, s Store) {
	t.Helper()
	cause := Cause{Kind: CauseWebhook, Detail: map[string]any{"event": "issue.labeled"}}
	task := &TaskRecord{
		ID:          "test-cortex-" + NewTaskID(),
		RouteID:     "build-on-label",
		IssueRepo:   "kit/hirdforge",
		IssueNumber: 7,
		IssueTitle:  "test issue",
		Bundle:      Bundle{Profile: "default"},
	}

	if err := s.CreateTask(task, "matched build-on-label", cause); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, history, err := s.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != StatusQueued {
		t.Fatalf("status = %q, want queued", got.Status)
	}
	if len(history) != 1 || history[0].ToStatus != StatusQueued || history[0].Reason == "" {
		t.Fatalf("creating transition row missing or reasonless: %+v", history)
	}

	// Legal transition succeeds and appends exactly one history row.
	if err := s.Transition(task.ID, StatusDispatched, "dispatched to agent", Cause{Kind: CauseOperator}); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	got, history, _ = s.GetTask(task.ID)
	if got.Status != StatusDispatched || len(history) != 2 {
		t.Fatalf("after transition: status=%q history=%d", got.Status, len(history))
	}

	// Illegal transition is rejected AND leaves no trace.
	if err := s.Transition(task.ID, StatusValidated, "cheat", Cause{Kind: CauseOperator}); err == nil {
		t.Fatal("illegal transition dispatched->validated accepted")
	}
	got, history, _ = s.GetTask(task.ID)
	if got.Status != StatusDispatched || len(history) != 2 {
		t.Fatalf("illegal transition left a trace: status=%q history=%d", got.Status, len(history))
	}

	// Invalid cause kind rejected.
	if err := s.Transition(task.ID, StatusBuilding, "x", Cause{Kind: "model"}); err == nil {
		t.Fatal("cause kind 'model' accepted by store")
	}

	// Decisions round-trip newest-first.
	if err := s.RecordDecision(Decision{Event: Event{Type: EventIssueLabeled, Repo: "kit/hirdforge"}, Reason: "first", At: time.Now()}); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	if err := s.RecordDecision(Decision{Event: Event{Type: EventIssueLabeled, Repo: "kit/hirdforge"}, MatchedRoute: "r", Reason: "second", At: time.Now().Add(time.Millisecond)}); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	ds, err := s.ListDecisions(10)
	if err != nil {
		t.Fatalf("ListDecisions: %v", err)
	}
	if len(ds) < 2 || ds[0].Reason != "second" {
		t.Fatalf("decisions not newest-first: %+v", ds)
	}

	// List filter.
	tasks, err := s.ListTasks(TaskFilter{Status: StatusDispatched})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	found := false
	for _, x := range tasks {
		if x.ID == task.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListTasks(status=dispatched) missing task")
	}
}

func TestMemStoreInvariants(t *testing.T) {
	storeInvariantSuite(t, NewMemStore())
}

func TestPGStoreInvariants(t *testing.T) {
	dbURL := strings.TrimSpace(os.Getenv("CORTEX_TEST_DB_URL"))
	if dbURL == "" {
		t.Skip("CORTEX_TEST_DB_URL not set")
	}
	s, err := InitPGStore(dbURL)
	if err != nil {
		t.Fatalf("InitPGStore: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.db.Exec(`DELETE FROM cortex_transitions WHERE task_id LIKE 'test-cortex-%'`)
		_, _ = s.db.Exec(`DELETE FROM cortex_tasks WHERE id LIKE 'test-cortex-%'`)
		_ = s.db.Close()
	})
	storeInvariantSuite(t, s)
}

func TestNewTaskIDSortableAndUnique(t *testing.T) {
	a := NewTaskID()
	time.Sleep(2 * time.Millisecond)
	b := NewTaskID()
	if !strings.HasPrefix(a, "hf-") || !strings.HasPrefix(b, "hf-") {
		t.Fatalf("ids missing hf- prefix: %s %s", a, b)
	}
	if a >= b {
		t.Fatalf("ids not creation-sortable: %s >= %s", a, b)
	}
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewTaskID()
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
}
