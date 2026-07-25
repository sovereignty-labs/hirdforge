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
	"regexp"
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

var fencedBlockRE = regexp.MustCompile("(?s)```(?:json|plan)?\\s*\\n?(.*?)```")

// ParseChatOutput decodes a turn's model output, robust to the two model shapes
// we must support (D-INTERLOCUTOR: configurable to any model):
//
//   - A CAPABLE model may emit one clean JSON object {"reply":..., "plan":...}.
//   - A CHATTY / reasoning model replies in PROSE and, when proposing work, appends
//     the plan as a fenced ```json block. Requiring it to JSON-encode its prose
//     reply is an invariant it cannot hold — it produces unescaped quotes and
//     duplicate text (observed live on qwen-reserved). So the reply is taken as
//     prose and never has to be valid JSON; only the optional plan is structured,
//     and a malformed plan degrades to a reply-only turn (safe — no work).
//
// The RESULT is validated strictly by Validate; a plan that parses but is
// ill-formed is rejected there.
func ParseChatOutput(raw string) (ChatOutput, error) {
	// Prefer a plan from a fenced block; the prose around it is the reply.
	plan, _ := extractFencedPlan(raw)
	prose := strings.TrimSpace(fencedBlockRE.ReplaceAllString(raw, ""))
	prose = strings.TrimSpace(stripTrailingJSONEcho(prose))

	// If the reply is prose (the common, robust case) return it with any plan.
	if prose != "" && !looksLikeBareJSONObject(prose) {
		return ChatOutput{Reply: prose, Plan: plan}, nil
	}

	// Otherwise the model emitted JSON where prose was expected (a capable model, or
	// a fenced/bare {reply,plan} object). Try to read a clean {reply,...} from the
	// whole output or any fenced body; honor the first that carries a reply.
	for _, cand := range candidateJSON(raw) {
		var c ChatOutput
		if err := json.Unmarshal([]byte(cand), &c); err == nil && strings.TrimSpace(c.Reply) != "" {
			return c, nil
		}
	}
	return ChatOutput{}, fmt.Errorf("steward: no usable reply in model output")
}

// looksLikeBareJSONObject reports whether s is (just) a JSON object — so raw JSON
// the model failed to make usable is not passed off as a prose reply.
func looksLikeBareJSONObject(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")
}

// candidateJSON returns JSON strings worth trying as a clean ChatOutput: the first
// balanced object in the raw output, plus each fenced block body.
func candidateJSON(raw string) []string {
	var out []string
	if obj := extractJSONObject(raw); obj != "" {
		out = append(out, obj)
	}
	for _, m := range fencedBlockRE.FindAllStringSubmatch(raw, -1) {
		if body := strings.TrimSpace(m[1]); body != "" {
			out = append(out, body)
		}
	}
	return out
}

// extractFencedPlan returns the first fenced code block that parses as a valid
// Plan (a title and at least one step). A block that does not parse as a plan is
// ignored — it is not this turn's plan.
func extractFencedPlan(raw string) (*Plan, bool) {
	for _, m := range fencedBlockRE.FindAllStringSubmatch(raw, -1) {
		body := strings.TrimSpace(m[1])
		if body == "" {
			continue
		}
		var p Plan
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			continue
		}
		if strings.TrimSpace(p.Title) != "" && len(p.Steps) > 0 {
			return &p, true
		}
	}
	return nil, false
}

// stripTrailingJSONEcho removes a trailing bare JSON object that some models append
// after their prose (echoing a "{\"reply\":...}" contract). It only strips when the
// tail begins a `{"reply"`-shaped object, so ordinary prose ending in a brace is
// left alone.
func stripTrailingJSONEcho(s string) string {
	idx := strings.LastIndex(s, "{")
	if idx < 0 {
		return s
	}
	tail := strings.TrimSpace(s[idx:])
	if strings.HasPrefix(tail, `{"reply"`) || strings.HasPrefix(tail, `{ "reply"`) || strings.HasPrefix(tail, "{\n") && strings.Contains(tail, `"reply"`) {
		return s[:idx]
	}
	return s
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
