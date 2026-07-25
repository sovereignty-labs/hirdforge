package steward

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fixtureSource struct {
	tasks []TaskView
}

func (f fixtureSource) Tasks(context.Context) ([]TaskView, error) { return f.tasks, nil }
func (f fixtureSource) Task(_ context.Context, id string) (TaskView, bool, error) {
	for _, t := range f.tasks {
		if t.ID == id {
			return t, true, nil
		}
	}
	return TaskView{}, false, nil
}

func tv(id, status, reason string, ago time.Duration) TaskView {
	return TaskView{ID: id, Status: status, Reason: reason, Repo: "kit/hirdforge", Issue: 1, Updated: time.Unix(1_700_000_000, 0).Add(-ago)}
}

// Running lists only in-flight tasks, cites each id, and quotes the mechanical
// reason verbatim. Terminal tasks are excluded.
func TestRunningProjectsActiveTasksVerbatim(t *testing.T) {
	src := fixtureSource{tasks: []TaskView{
		tv("t1", "building", "gate task-1 secret-exists PASS", 2*time.Minute),
		tv("t2", "review", "awaiting reviewer verdict", 1*time.Minute),
		tv("t3", "validated", "merged and validated", 5*time.Minute), // terminal, excluded
		tv("t4", "failed", "gate ci-status FAIL: build broke", 9*time.Minute),
	}}
	out, active, err := NewStatusProjector(src).Running(context.Background())
	if err != nil {
		t.Fatalf("Running: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("active = %d, want 2 (t3 validated, t4 failed excluded)", len(active))
	}
	for _, want := range []string{"task t1", "task t2", "gate task-1 secret-exists PASS", "awaiting reviewer verdict"} {
		if !strings.Contains(out, want) {
			t.Fatalf("Running output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "task t3") || strings.Contains(out, "task t4") {
		t.Fatalf("terminal task leaked into running:\n%s", out)
	}
	// Most-recently-updated first: t2 (1m) before t1 (2m).
	if strings.Index(out, "task t2") > strings.Index(out, "task t1") {
		t.Fatalf("running not ordered most-recent-first:\n%s", out)
	}
}

func TestRunningNothingActive(t *testing.T) {
	src := fixtureSource{tasks: []TaskView{tv("t3", "validated", "done", 0)}}
	out, active, _ := NewStatusProjector(src).Running(context.Background())
	if len(active) != 0 || !strings.Contains(out, "Nothing is running") {
		t.Fatalf("expected nothing-running, got %q (%d active)", out, len(active))
	}
}

// Explain quotes the failing task's mechanical reason verbatim and cites the id.
func TestExplainQuotesReasonVerbatim(t *testing.T) {
	reason := "gate ci-status FAIL: staticcheck U1000 unused var at foo.go:12"
	src := fixtureSource{tasks: []TaskView{{ID: "t9", Status: "failed", Reason: reason, Repo: "kit/hirdforge", Issue: 42, PR: 43}}}
	out, err := NewStatusProjector(src).Explain(context.Background(), "t9")
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if !strings.Contains(out, "task t9") || !strings.Contains(out, reason) {
		t.Fatalf("Explain must cite id and quote reason verbatim:\n%s", out)
	}
	// The verbatim reason must appear unmodified — not paraphrased.
	if !strings.Contains(out, "staticcheck U1000 unused var at foo.go:12") {
		t.Fatalf("reason was altered:\n%s", out)
	}
}

// An unknown task is answered "I don't have that", never invented.
func TestExplainUnknownTaskIsHonest(t *testing.T) {
	src := fixtureSource{tasks: []TaskView{tv("t1", "building", "x", 0)}}
	out, err := NewStatusProjector(src).Explain(context.Background(), "t404")
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if !strings.HasPrefix(out, unknownTaskPrefix) || strings.Contains(out, "building") {
		t.Fatalf("unknown task must be answered honestly, not invented:\n%s", out)
	}
}
