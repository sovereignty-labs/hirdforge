// Package steward implements the interlocutor's plan mode (Phase 4).
//
// The doctrine constraint that shapes every line here: models are forbidden in
// the coordination path. The interlocutor is a model, so it is safe for exactly
// one reason —
//
//	the interlocutor PROPOSES a plan; it never decides what the system does.
//
// Concretely: a conversational turn returns a REPLY and, when the conversation
// has produced work, an inert PLAN proposal. Nothing here has a side effect. Work
// is created only later, by an explicit operator blessing (§7 /steward/handoff),
// which files the plan's steps through the same createLabeledIssueAndRoute path
// the operator's manual dispatch uses, from which Cortex routes deterministically.
// A hallucinated plan costs a rejected proposal, never a wrong state transition.
//
// "Steward" is a stand-in name (D-INTERLOCUTOR); the user can replace it. Nothing
// in this package hardcodes it as a brand.
package steward

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Gate is a step's declared done-gate, mirroring D-GATE. Which mechanical check a
// step will be completed by is part of the proposal, so the operator sees how each
// piece of work will be judged before blessing it.
type Gate string

const (
	// GateCIStatus / GateTestCmd are CODE steps: they become a PR whose completion
	// is the CI status or a test-command exit code, merged behind Lockbox.
	GateCIStatus Gate = "ci-status"
	GateTestCmd  Gate = "test-command"
	// GateValidator is an OPERATIONAL step: an action whose completion is a declared
	// validator's exit code (e.g. "secret exists"), gated at Lockbox — no PR. Not
	// everything is a PR; this is the other task shape (D-INTERLOCUTOR).
	GateValidator Gate = "custom-validator"
	// GateOperator marks a step held for the human: work the interlocutor cannot do
	// itself (the mock's "[you]" DNS step). It is surfaced, never dispatched.
	GateOperator Gate = "operator"
)

var dispatchableGates = map[Gate]bool{GateCIStatus: true, GateTestCmd: true, GateValidator: true}
var knownGates = map[Gate]bool{GateCIStatus: true, GateTestCmd: true, GateValidator: true, GateOperator: true}

// Step is one piece of a plan. It proposes its own done-gate and whether it is
// work for the fleet or work held for the operator.
type Step struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Detail        string `json:"detail"`
	Gate          Gate   `json:"gate"`
	NeedsOperator bool   `json:"needs_operator"`
	// Repo / Label are optional dispatch hints for a code/operational step; the
	// configured defaults are applied at handoff when these are empty.
	Repo  string `json:"repo,omitempty"`
	Label string `json:"label,omitempty"`
}

// IsCode reports whether the step lands as a PR (a code step).
func (s Step) IsCode() bool { return s.Gate == GateCIStatus || s.Gate == GateTestCmd }

// IsOperational reports whether the step lands as a Lockbox-gated action (no PR).
func (s Step) IsOperational() bool { return s.Gate == GateValidator }

// Dispatchable reports whether a blessing would file this step. Steps held for the
// operator are never dispatched — dispatch is partial (§7).
func (s Step) Dispatchable() bool { return !s.NeedsOperator && dispatchableGates[s.Gate] }

// Plan is the interlocutor's proposal: a multi-step direction the operator can
// bless, revise, or ignore. It is inert until blessed.
type Plan struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Steps []Step `json:"steps"`
}

// DispatchSteps returns the steps a blessing would actually file (non-operator,
// dispatchable-gate). The rest are held.
func (p Plan) DispatchSteps() []Step {
	out := make([]Step, 0, len(p.Steps))
	for _, s := range p.Steps {
		if s.Dispatchable() {
			out = append(out, s)
		}
	}
	return out
}

// ChatOutput is the model's structured output for one turn: a reply, and — only
// when the conversation has produced work — an inert plan proposal. There is no
// field here that causes a side effect; that is the point.
type ChatOutput struct {
	Reply string `json:"reply"`
	Plan  *Plan  `json:"plan,omitempty"`
}

// controlVerbs are operator-only actions (D-CONTROL). The interlocutor surface has
// no code path to perform them; refusing a step that names one is defense in depth
// that also makes the attempt visible.
var controlVerbs = []string{"retry", "cancel", "dispatch", "approve", "merge", "validate"}

const (
	maxStepDetail = 8000
	maxSteps      = 24
)

// ParseChatOutput decodes a turn's model output. Models wrap JSON in prose or
// fences often enough that we extract the object rather than demand purity — but
// the RESULT is validated strictly.
func ParseChatOutput(raw string) (ChatOutput, error) {
	var c ChatOutput
	body := extractJSONObject(raw)
	if body == "" {
		return c, fmt.Errorf("steward: no JSON object in model output")
	}
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		return c, fmt.Errorf("steward: malformed turn output: %w", err)
	}
	return c, nil
}

// Validate enforces the turn invariants. A turn always has a reply; a plan, when
// present, must be well-formed so the UI and the eventual handoff can trust it.
// Note validation gates no side effect here (the turn is inert) — it guarantees a
// proposal the operator can reason about, and refuses a plan that reaches for an
// operator verb.
func (c ChatOutput) Validate() error {
	if strings.TrimSpace(c.Reply) == "" {
		return fmt.Errorf("steward: turn has no reply")
	}
	if c.Plan == nil {
		return nil
	}
	return c.Plan.Validate()
}

// Validate enforces plan shape: a title, at least one step, unique step ids, a
// known gate per step, bounded detail, a dispatchable gate on any step not held
// for the operator, and no step reaching for an operator verb.
func (p Plan) Validate() error {
	if strings.TrimSpace(p.Title) == "" {
		return fmt.Errorf("steward: plan has no title")
	}
	if len(p.Steps) == 0 {
		return fmt.Errorf("steward: plan has no steps")
	}
	if len(p.Steps) > maxSteps {
		return fmt.Errorf("steward: plan has %d steps (max %d)", len(p.Steps), maxSteps)
	}
	seen := make(map[string]bool, len(p.Steps))
	for i, s := range p.Steps {
		if strings.TrimSpace(s.ID) == "" {
			return fmt.Errorf("steward: step %d has no id", i)
		}
		if seen[s.ID] {
			return fmt.Errorf("steward: duplicate step id %q", s.ID)
		}
		seen[s.ID] = true
		if strings.TrimSpace(s.Title) == "" {
			return fmt.Errorf("steward: step %q has no title", s.ID)
		}
		if len(s.Detail) > maxStepDetail {
			return fmt.Errorf("steward: step %q detail exceeds %d bytes", s.ID, maxStepDetail)
		}
		if !knownGates[s.Gate] {
			return fmt.Errorf("steward: step %q has unknown gate %q", s.ID, s.Gate)
		}
		// A step the fleet will do must carry a dispatchable gate — otherwise the
		// blessing could not file it and it would silently vanish.
		if !s.NeedsOperator && !dispatchableGates[s.Gate] {
			return fmt.Errorf("steward: step %q is not operator-held but has non-dispatchable gate %q", s.ID, s.Gate)
		}
		if v, ok := namesControlVerb(s); ok {
			return fmt.Errorf("steward: step %q names the operator-only action %q", s.ID, v)
		}
	}
	return nil
}

// namesControlVerb reports whether a step's dispatch label reaches for an operator
// verb. The prose fields (title/detail) are exempt — discussing a retry is fine;
// only the machine-consumed label is policed.
func namesControlVerb(s Step) (string, bool) {
	hay := strings.ToLower(s.Label)
	for _, v := range controlVerbs {
		if strings.Contains(hay, v) {
			return v, true
		}
	}
	return "", false
}

// extractJSONObject pulls the first balanced {...} out of model output, ignoring
// surrounding prose or ``` fences.
func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	if start < 0 {
		return ""
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
			// skip
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
