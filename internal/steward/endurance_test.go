package steward

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// stubSummarizer folds incrementally: it accumulates the running count of turns
// folded, proving the prior summary is threaded in (not discarded each time).
type stubSummarizer struct {
	calls int
}

func (s *stubSummarizer) Fold(_ context.Context, prior string, older []Turn) (string, error) {
	s.calls++
	total := len(older)
	var n int
	if _, err := fmt.Sscanf(prior, "SUMMARY of %d earlier turns", &n); err == nil {
		total += n // incremental: fold onto the prior summary, don't lose it
	}
	return fmt.Sprintf("SUMMARY of %d earlier turns", total), nil
}

// drive runs n plain turns on the engine (no plan), returning it.
func drive(t *testing.T, e *Engine, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := e.Chat(context.Background(), "s", fmt.Sprintf("msg %d", i)); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}
}

// A short session threads every turn verbatim, no summary, no compaction.
func TestEnduranceShortSessionNoCompaction(t *testing.T) {
	r := &stubRunner{out: `{"reply":"ok"}`}
	sum := &stubSummarizer{}
	e := NewEngine(r).WithClock(fixedClock()).WithSummarizer(sum).WithRecentWindow(12)
	drive(t, e, 5)
	if sum.calls != 0 {
		t.Fatalf("short session must not summarize; calls=%d", sum.calls)
	}
	if r.lastCtx.Summary != "" || r.lastCtx.Folded != 0 {
		t.Fatalf("unexpected compaction: %+v", r.lastCtx)
	}
	if len(r.lastCtx.Recent) != 4 { // context excludes the current in-flight turn
		t.Fatalf("recent = %d, want 4", len(r.lastCtx.Recent))
	}
}

// Past the budget, the older span is summarized and the recent window bounded.
// The loud OnCompact hook fires.
func TestEnduranceSummarizesOlderSpan(t *testing.T) {
	r := &stubRunner{out: `{"reply":"ok"}`}
	sum := &stubSummarizer{}
	var compactedFolded int
	e := NewEngine(r).WithClock(fixedClock()).WithSummarizer(sum).WithRecentWindow(4).
		OnCompact(func(_ string, folded int) { compactedFolded = folded })
	drive(t, e, 20)
	if sum.calls == 0 {
		t.Fatal("long session must summarize the older span")
	}
	if len(r.lastCtx.Recent) != 4 {
		t.Fatalf("recent window = %d, want 4", len(r.lastCtx.Recent))
	}
	if !strings.HasPrefix(r.lastCtx.Summary, "SUMMARY of") || r.lastCtx.Folded == 0 {
		t.Fatalf("older span not summarized: %+v", r.lastCtx)
	}
	if compactedFolded == 0 {
		t.Fatal("OnCompact must fire loudly with the folded count")
	}
}

// The core endurance property: the active plan stays pinned even after its
// originating turn falls into the summarized span — it is carried, not dropped.
func TestEndurancePinsActivePlanPastBudget(t *testing.T) {
	r := &stubRunner{out: `{"reply":"ok"}`}
	sum := &stubSummarizer{}
	e := NewEngine(r).WithClock(fixedClock()).WithSummarizer(sum).WithRecentWindow(4)
	// Turn 0 proposes a plan; then many plain turns bury it past the window.
	planRunner := &stubRunner{out: tlsPlan}
	e.runner = planRunner
	if _, err := e.Chat(context.Background(), "s", "make a plan"); err != nil {
		t.Fatalf("plan turn: %v", err)
	}
	e.runner = r // subsequent turns are plain replies
	drive(t, e, 15)
	// The plan's turn is now deep in the summarized span, yet it is pinned.
	if r.lastCtx.ActivePlan == nil || r.lastCtx.ActivePlan.ID != "tls" {
		t.Fatalf("active plan lost past the budget: %+v", r.lastCtx.ActivePlan)
	}
	// And it is not sitting in the recent window — it survived via pinning.
	for _, tn := range r.lastCtx.Recent {
		if tn.Plan != nil {
			t.Fatal("plan should have aged out of the recent window; pinning is what kept it")
		}
	}
	// A blessing can still act on the pinned plan after a long session.
	if p := e.PlanFor("s"); p == nil || p.ID != "tls" {
		t.Fatalf("PlanFor lost the plan: %+v", p)
	}
}

// A summarizer failure must never lose coverage: because folding only drops turns
// AFTER a successful fold, a failing summarizer simply retains the full tail — no
// fabricated summary, nothing folded, nothing dropped. The tail growing past the
// window is the observable signal, not a silent gap.
func TestEnduranceSummarizerFailureLosesNothing(t *testing.T) {
	r := &stubRunner{out: `{"reply":"ok"}`}
	e := NewEngine(r).WithClock(fixedClock()).WithSummarizer(failSummarizer{}).WithRecentWindow(4)
	drive(t, e, 12)
	if r.lastCtx.Summary != "" {
		t.Fatalf("failed summarizer must not fabricate a summary: %q", r.lastCtx.Summary)
	}
	if r.lastCtx.Folded != 0 {
		t.Fatalf("nothing should be folded when the summarizer fails; folded=%d", r.lastCtx.Folded)
	}
	// The turns are retained, not dropped — coverage is fully preserved.
	if len(r.lastCtx.Recent) <= 4 {
		t.Fatalf("failed fold must retain the full tail (no silent drop); recent=%d", len(r.lastCtx.Recent))
	}
}

type failSummarizer struct{}

func (failSummarizer) Fold(context.Context, string, []Turn) (string, error) {
	return "", fmt.Errorf("model unreachable")
}

// An EMPTY summary (no error) — a reasoning model that spent its whole budget
// thinking and returned no content — must be treated as a failed fold: turns are
// retained, nothing is dropped, no coverage lost. (Regression: FoldOldest used to
// drop the span and leave the summary empty.)
func TestEnduranceEmptySummaryLosesNothing(t *testing.T) {
	r := &stubRunner{out: `{"reply":"ok"}`}
	e := NewEngine(r).WithClock(fixedClock()).WithSummarizer(emptySummarizer{}).WithRecentWindow(4)
	drive(t, e, 12)
	if r.lastCtx.Folded != 0 {
		t.Fatalf("empty summary must fold nothing; folded=%d", r.lastCtx.Folded)
	}
	if r.lastCtx.Summary != "" {
		t.Fatalf("must not record an empty summary: %q", r.lastCtx.Summary)
	}
	if len(r.lastCtx.Recent) <= 4 {
		t.Fatalf("turns must be retained when the summary is empty; recent=%d", len(r.lastCtx.Recent))
	}
}

type emptySummarizer struct{}

func (emptySummarizer) Fold(context.Context, string, []Turn) (string, error) {
	return "   ", nil // whitespace only — no real content
}
