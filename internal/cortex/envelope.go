package cortex

import (
	"encoding/json"
	"fmt"
)

// The dispatch envelope (docs/specs/contracts/DISPATCH_ENVELOPE.md, approved).
// Assembly is a pure function over (config, route, task, event) — same inputs,
// same envelope, byte for byte. Issue bodies and reviewer feedback ride as
// data; nothing here interprets them.

type Envelope struct {
	EnvelopeVersion int    `json:"envelope_version"`
	TaskID          string `json:"task_id"`
	RouteID         string `json:"route_id"`
	Attempt         int    `json:"attempt"`
	Role            string `json:"role"`

	Issue *EnvelopeIssue `json:"issue,omitempty"`
	Git   EnvelopeGit    `json:"git"`

	Bundle Bundle `json:"bundle"`

	DoneWhen string `json:"done_when"`

	FailureContext *FailureContext `json:"failure_context,omitempty"`
	Review         *ReviewContext  `json:"review,omitempty"`
}

type EnvelopeIssue struct {
	Repo   string   `json:"repo"`
	Number int64    `json:"number"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels,omitempty"`
}

type EnvelopeGit struct {
	CloneURL          string `json:"clone_url"`
	BaseBranch        string `json:"base_branch"`
	WorkBranch        string `json:"work_branch"`
	PushCredentialRef string `json:"push_credential_ref"`
}

// FailureContext is the retry variant's cargo (D-LESSONS #2): a retry
// dispatch always says why the last attempt failed.
type FailureContext struct {
	PriorAgent       string   `json:"prior_agent,omitempty"`
	PriorAttempt     int      `json:"prior_attempt"`
	Reason           string   `json:"reason"`
	GateExcerpt      string   `json:"gate_excerpt,omitempty"`
	ReviewerFeedback []string `json:"reviewer_feedback,omitempty"`
}

// ReviewContext is the reviewer variant's cargo (D-LESSONS #4): the reviewer
// sees the artifact — the diff — and the mechanical gate's evidence.
type ReviewContext struct {
	PR            ReviewPR        `json:"pr"`
	Diff          string          `json:"diff"`
	DiffTruncated bool            `json:"diff_truncated"`
	GateResult    json.RawMessage `json:"gate_result,omitempty"`
}

type ReviewPR struct {
	Repo   string `json:"repo"`
	Number int64  `json:"number"`
	Head   string `json:"head"`
	Base   string `json:"base"`
}

// EnvelopeParams carries the non-config inputs to assembly.
type EnvelopeParams struct {
	Task           *TaskRecord
	Event          Event
	CloneURL       string // resolved by the caller from the repo (gateway config)
	BaseBranch     string
	CredentialRef  string
	FailureContext *FailureContext // retry variant; nil on first attempt
	Review         *ReviewContext  // reviewer variant; nil for builders
}

const maxReviewDiffBytes = 256 << 10 // DISPATCH_ENVELOPE.md: diff inline ≤256KiB

// BuildEnvelope assembles the deterministic dispatch envelope for a task on a
// route. Pure: no clock, no randomness — the task id was minted at creation.
func BuildEnvelope(cfg *Config, route *Route, p EnvelopeParams) (*Envelope, error) {
	if route.Dispatch == nil {
		return nil, fmt.Errorf("cortex: route %s has no dispatch", route.ID)
	}
	bundle, ok := cfg.Bundles[route.Dispatch.Bundle]
	if !ok {
		return nil, fmt.Errorf("cortex: route %s: unknown bundle %s", route.ID, route.Dispatch.Bundle)
	}
	if route.Dispatch.Role == RoleBuilder && route.DoneGate == nil {
		return nil, fmt.Errorf("cortex: route %s: builder without done_gate", route.ID)
	}
	env := &Envelope{
		EnvelopeVersion: 1,
		TaskID:          p.Task.ID,
		RouteID:         route.ID,
		Attempt:         p.Task.Attempt,
		Role:            route.Dispatch.Role,
		Bundle:          bundle,
		Git: EnvelopeGit{
			CloneURL:          p.CloneURL,
			BaseBranch:        p.BaseBranch,
			WorkBranch:        "agent/" + p.Task.ID,
			PushCredentialRef: p.CredentialRef,
		},
		FailureContext: p.FailureContext,
	}
	if p.Task.IssueNumber != 0 || p.Task.IssueTitle != "" {
		env.Issue = &EnvelopeIssue{
			Repo:   p.Task.IssueRepo,
			Number: p.Task.IssueNumber,
			Title:  p.Task.IssueTitle,
			Body:   p.Event.IssueBody,
			Labels: p.Event.IssueLabels,
		}
	}
	if p.Review != nil {
		r := *p.Review
		if len(r.Diff) > maxReviewDiffBytes {
			r.Diff = r.Diff[:maxReviewDiffBytes]
			r.DiffTruncated = true
		}
		env.Review = &r
	}
	env.DoneWhen = renderDoneWhen(env, route)
	return env, nil
}

// renderDoneWhen is the fixed template: same envelope ⇒ same DONE WHEN text.
// It states the mechanical bar and that the agent's own assessment does not
// decide completion (D-GATE).
func renderDoneWhen(env *Envelope, route *Route) string {
	switch env.Role {
	case RoleBuilder:
		gate := ""
		if route.DoneGate != nil && route.DoneGate.Type == GateTestCommand {
			gate = fmt.Sprintf(" The gate command is: %s (exit 0 required).", route.DoneGate.Command)
		}
		return fmt.Sprintf(
			"Commit your work on branch %s and push it. Open a PR from %s to %s on %s. "+
				"The task is complete ONLY when the route's mechanical done-gate passes after you exit;"+
				"%s Your own assessment does not decide completion.",
			env.Git.WorkBranch, env.Git.WorkBranch, env.Git.BaseBranch, repoForDoneWhen(env), gate)
	case RoleReviewer:
		return "Review the PR diff provided in this envelope. Deliver your verdict by submitting a real " +
			"Gitea review on the PR: APPROVE or REQUEST_CHANGES, with your reasoning as the review body. " +
			"Your verdict reaches the system only through that Gitea review — nothing you print here is read."
	}
	return ""
}

func repoForDoneWhen(env *Envelope) string {
	if env.Issue != nil {
		return env.Issue.Repo
	}
	return env.Git.CloneURL
}
