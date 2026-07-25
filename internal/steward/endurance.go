package steward

import "context"

// Session endurance (P4.5, O-M7-SCOPE reopened for the interlocutor).
//
// The builder-side M7 deferral stands — builders are short-lived. The
// interlocutor is not: it holds a working session "for as long as we can help the
// model hold it together" (Kit, 2026-07-25), which makes context management its
// core competency, not an edge case. So instead of the builder's trim-and-announce
// (pin + drop), the interlocutor SUMMARIZES the older span and PINS the active
// plan, so a long conversation keeps its thread past the context budget.

// SessionContext is what the engine threads to the model each turn. For a short
// session it is just the recent turns; for a long one, older turns are folded
// into Summary while ActivePlan is pinned so it survives compaction. This is the
// "keeps the active plan and grounding facts intact" property (P4.5 acceptance):
// the plan is never merely announced-and-dropped — it is carried explicitly.
type SessionContext struct {
	Summary    string `json:"summary,omitempty"`     // condensed older turns (may be empty)
	ActivePlan *Plan  `json:"active_plan,omitempty"` // pinned across compaction
	Recent     []Turn `json:"recent"`                // most-recent turns, verbatim
	Folded     int    `json:"folded"`                // how many older turns Summary stands in for
}

// Summarizer folds newly-aged-out turns into a running summary, given the prior
// summary — an INCREMENTAL condensation, so the cost per turn stays bounded and no
// span is ever re-summarized or lost. It is behind a seam because summarization
// needs inference (the real one calls the deep-lane model); the endurance POLICY —
// when to fold, what to pin, what to thread — needs none, so it is fully tested
// here. A nil summarizer means the engine keeps the full tail (no folding).
type Summarizer interface {
	Fold(ctx context.Context, priorSummary string, older []Turn) (string, error)
}

// buildContext assembles the SessionContext for the next turn. When the unfolded
// tail exceeds the recent window it first folds the excess into the running
// summary (incrementally, dropping the folded turns from memory), firing OnCompact
// — the loud half of compaction (context_compacted; degrade never silently). The
// active plan is pinned from the durable plan list, so it is carried even after
// its originating turn was folded away. A fold error keeps the tail intact (no
// silent loss); the tail simply stays longer until the next successful fold.
func (e *Engine) buildContext(ctx context.Context, sessionID string) SessionContext {
	if e.summarizer != nil {
		folded, err := e.store.FoldOldest(sessionID, e.recent, func(prior string, older []Turn) (string, error) {
			return e.summarizer.Fold(ctx, prior, older)
		})
		if err == nil && folded > 0 && e.onCompact != nil {
			e.onCompact(sessionID, folded)
		}
	}
	return SessionContext{
		Summary:    e.store.Summary(sessionID),
		ActivePlan: e.store.ActivePlan(sessionID),
		Recent:     e.store.History(sessionID),
		Folded:     e.store.FoldedCount(sessionID),
	}
}
