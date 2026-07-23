package cortex

import "fmt"

// The task lifecycle spine (OBSERVABILITY_CONTRACT.md §1). Every transition
// is caused by a mechanical event and recorded with its reason — there is no
// bare status setter anywhere in this package (PERSISTENCE.md invariant #1).
const (
	StatusQueued     = "queued"
	StatusDispatched = "dispatched"
	StatusBuilding   = "building"
	StatusReview     = "review"
	StatusApproved   = "approved"
	StatusMerged     = "merged"
	StatusValidated  = "validated"
	StatusFailed     = "failed"
)

// Cause is the mechanical cause of a transition. Kind is a closed set — there
// is deliberately no kind for model output (PERSISTENCE.md invariant #2).
type Cause struct {
	Kind   string         `json:"kind"` // webhook | gate | timeout | operator | sandbox
	Detail map[string]any `json:"detail,omitempty"`
}

const (
	CauseWebhook  = "webhook"
	CauseGate     = "gate"
	CauseTimeout  = "timeout"
	CauseOperator = "operator"
	CauseSandbox  = "sandbox"
)

var validCauseKinds = map[string]bool{
	CauseWebhook: true, CauseGate: true, CauseTimeout: true,
	CauseOperator: true, CauseSandbox: true,
}

// legalTransitions is the legal-transition table, in code as the contract
// requires. Key "" is the creating transition.
var legalTransitions = map[string]map[string]bool{
	"":               {StatusQueued: true},
	StatusQueued:     {StatusDispatched: true, StatusFailed: true},
	StatusDispatched: {StatusBuilding: true, StatusFailed: true},
	StatusBuilding:   {StatusReview: true, StatusFailed: true},
	StatusReview:     {StatusApproved: true, StatusFailed: true},
	StatusApproved:   {StatusMerged: true, StatusFailed: true},
	StatusMerged:     {StatusValidated: true},
	StatusValidated:  {},                       // terminal
	StatusFailed:     {StatusDispatched: true}, // retry (D-CONTROL)
}

// IsValidStatus reports whether s is a lifecycle status.
func IsValidStatus(s string) bool {
	_, ok := legalTransitions[s]
	return ok && s != ""
}

// CheckTransition validates from→to legality and the cause. An illegal
// transition is a bug that fails loudly — callers must treat the error as
// fatal to the operation, never swallow it.
func CheckTransition(from, to string, cause Cause) error {
	if !validCauseKinds[cause.Kind] {
		return fmt.Errorf("cortex: invalid cause kind %q for transition %s->%s", cause.Kind, from, to)
	}
	next, ok := legalTransitions[from]
	if !ok {
		return fmt.Errorf("cortex: unknown from-status %q", from)
	}
	if !next[to] {
		return fmt.Errorf("cortex: illegal transition %s->%s", from, to)
	}
	return nil
}
