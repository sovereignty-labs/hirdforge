// Package steward implements the conversational front door (Phase 4).
//
// The doctrine constraint that shapes every line here: models are forbidden in
// the coordination path. A Steward is a model, so it is safe for exactly one
// reason —
//
//	the Steward writes issues; it never decides what the system does with them.
//
// Concretely: the model returns a typed PROPOSAL and never executes anything.
// This package validates that proposal; the gateway then performs the single
// permitted action (create a labeled issue through the existing operator
// dispatch path, from which Cortex routes deterministically). There is no agent
// tool-loop on this path, so model output cannot become any other action. A
// hallucination costs a badly-worded issue, never a wrong state transition.
package steward

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Intent is what the model says the turn is. The set is closed: an unrecognized
// intent is rejected, never guessed at.
type Intent string

const (
	// IntentChat: conversational reply, no side effect.
	IntentChat Intent = "chat"
	// IntentCreateIssue: the operator asked for work; propose an issue.
	IntentCreateIssue Intent = "create_issue"
	// IntentStatusQuery: the operator asked what is happening; answer from the
	// observability surface (never from the model's recollection).
	IntentStatusQuery Intent = "status_query"
	// IntentClarify: the request is underspecified; ask ONE focused question
	// rather than inventing acceptance criteria (the fail-open discipline).
	IntentClarify Intent = "clarify"
)

// IssueProposal is the only side-effecting thing a Steward turn may produce.
type IssueProposal struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Repo   string `json:"repo,omitempty"`  // defaults to the configured repo
	Label  string `json:"label,omitempty"` // defaults to the configured build label
	Accept string `json:"acceptance,omitempty"`
}

// Proposal is the model's structured output for one turn.
type Proposal struct {
	Intent Intent         `json:"intent"`
	Reply  string         `json:"reply"`
	Issue  *IssueProposal `json:"issue,omitempty"`
}

// controlVerbs are operator-only actions (D-CONTROL). The Steward surface must
// not reach them, so a proposal that tries to name one is refused outright —
// defense in depth behind the fact that this package simply has no code path to
// perform them.
var controlVerbs = []string{"retry", "cancel", "dispatch", "approve", "merge", "validate"}

// maxIssueBody bounds what a single turn can push into an issue.
const maxIssueBody = 8000

// ParseProposal decodes the model's turn output. Models wrap JSON in prose or
// fences often enough that we extract the object rather than demanding purity —
// but the RESULT is validated strictly.
func ParseProposal(raw string) (Proposal, error) {
	var p Proposal
	body := extractJSONObject(raw)
	if body == "" {
		return p, fmt.Errorf("steward: no JSON object in model output")
	}
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		return p, fmt.Errorf("steward: malformed proposal: %w", err)
	}
	return p, nil
}

// Validate enforces every §7 invariant on a decoded proposal. A proposal that
// fails validation is NEVER executed; the caller answers with a clarification.
func (p Proposal) Validate() error {
	switch p.Intent {
	case IntentChat, IntentStatusQuery, IntentClarify:
		if p.Issue != nil {
			return fmt.Errorf("steward: intent %q must not carry an issue", p.Intent)
		}
	case IntentCreateIssue:
		if p.Issue == nil {
			return fmt.Errorf("steward: intent create_issue carries no issue")
		}
		if strings.TrimSpace(p.Issue.Title) == "" {
			return fmt.Errorf("steward: issue has no title")
		}
		if strings.TrimSpace(p.Issue.Body) == "" {
			return fmt.Errorf("steward: issue has no body — a vague issue makes a vague build")
		}
		if len(p.Issue.Body) > maxIssueBody {
			return fmt.Errorf("steward: issue body exceeds %d bytes", maxIssueBody)
		}
	default:
		return fmt.Errorf("steward: unknown intent %q", p.Intent)
	}
	if strings.TrimSpace(p.Reply) == "" {
		return fmt.Errorf("steward: turn has no reply")
	}
	// A proposal must not try to invoke an operator verb. The surface cannot
	// perform them regardless; refusing here makes the attempt visible.
	if v, ok := namesControlVerb(p); ok {
		return fmt.Errorf("steward: proposal names the operator-only action %q", v)
	}
	return nil
}

// namesControlVerb reports whether a proposal is trying to request an operator
// verb as an action (not merely mentioning the word in prose — the reply is
// exempt, since discussing a retry is legitimate conversation).
func namesControlVerb(p Proposal) (string, bool) {
	if p.Issue == nil {
		return "", false
	}
	hay := strings.ToLower(p.Issue.Label)
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
