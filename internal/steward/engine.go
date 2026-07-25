package steward

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// TurnRunner runs one conversational turn against the interlocutor model and
// returns its raw final output. This is the SINGLE seam where a model is
// consulted on this surface — and it returns TEXT, never an action. The real
// implementation calls the steward agent over HTTP (P4.6, the gateway proxy);
// tests inject a stub. Keeping the model behind this interface is what lets the
// engine be exercised — including its inertness — without live inference.
type TurnRunner interface {
	RunTurn(ctx context.Context, sessionID, message string, history []Turn) (string, error)
}

// Clock returns the current time; injectable so tests are deterministic.
type Clock func() time.Time

// Engine runs the interlocutor's plan mode. It holds the conversation and turns
// model output into a validated, INERT ChatOutput — a reply and an optional plan
// proposal. It has no dependency on any dispatch, issue-creation, or lifecycle
// path: structurally, a chat turn cannot file work. The only write in the whole
// §7 surface is the operator's blessing (Handoff, P4.3), which lives elsewhere.
type Engine struct {
	runner TurnRunner
	store  *SessionStore
	now    Clock
}

// NewEngine builds an engine over a turn runner. A nil clock defaults to
// time.Now.
func NewEngine(runner TurnRunner) *Engine {
	return &Engine{runner: runner, store: NewSessionStore(), now: time.Now}
}

// WithClock overrides the clock (tests).
func (e *Engine) WithClock(c Clock) *Engine {
	if c != nil {
		e.now = c
	}
	return e
}

// Chat runs one inert conversational turn: consult the model with the session
// history, parse and strictly validate its output, record the turn, and return
// it. Every exit but a clean validated turn records NOTHING and files NOTHING —
// a malformed or hostile model output costs an error the caller can turn into a
// graceful reply, never a side effect.
func (e *Engine) Chat(ctx context.Context, sessionID, message string) (ChatOutput, error) {
	raw, err := e.runner.RunTurn(ctx, sessionID, message, e.store.History(sessionID))
	if err != nil {
		return ChatOutput{}, err
	}
	out, err := ParseChatOutput(raw)
	if err != nil {
		return ChatOutput{}, err
	}
	if err := out.Validate(); err != nil {
		return ChatOutput{}, err
	}
	e.store.Append(sessionID, Turn{
		At:      e.now(),
		Message: message,
		Reply:   out.Reply,
		Plan:    out.Plan,
	})
	return out, nil
}

// History returns a copy of a session's recorded turns (§7 GET
// /steward/sessions/{id}).
func (e *Engine) History(sessionID string) []Turn {
	return e.store.History(sessionID)
}

// Created returns the issues a session's blessings filed (§7 traceability).
func (e *Engine) Created(sessionID string) []Created {
	return e.store.Created(sessionID)
}

// StepFiler files one dispatchable plan step as routed work and returns the record
// of what it created. The gateway supplies one backed by createLabeledIssueAndRoute
// (P4.6); the engine never touches Gitea or Cortex itself, so the write path stays
// OUTSIDE plan mode — the engine only decides WHAT the blessing files, mechanically.
type StepFiler func(ctx context.Context, planID string, s Step) (Created, error)

var (
	// ErrNoSuchPlan: the blessed plan was never proposed in this session. A client
	// cannot smuggle in an arbitrary plan body — only a plan the interlocutor
	// recorded is blessable.
	ErrNoSuchPlan = errors.New("steward: no such plan in this session")
	// ErrNoDispatchableSteps: the plan (or the blessed subset) has nothing the
	// fleet can do — every step is held for the operator.
	ErrNoDispatchableSteps = errors.New("steward: plan has no dispatchable steps")
	// ErrAlreadyBlessed: the plan was already handed off. The guard against a
	// double-dispatch.
	ErrAlreadyBlessed = errors.New("steward: plan already blessed")
)

// Handoff is THE blessing (§7): the one place a plan becomes work. It files the
// dispatchable steps of a plan the interlocutor proposed IN THIS SESSION, through
// the caller's filer. It is the only writing entry point on the surface, and it is
// mechanical, not a model decision — the operator approved the plan; this executes
// it deterministically. Properties:
//
//   - Partial dispatch: steps held for the operator (needs_operator) are NEVER
//     filed; they are the human's to do.
//   - Explicit subset: when stepIDs is non-empty, only those steps are filed —
//     and each named id must exist AND be dispatchable, or the WHOLE blessing is
//     refused (loud, never a silent partial).
//   - Provenance: the plan must be one this session recorded; an arbitrary
//     client-supplied plan is rejected (ErrNoSuchPlan).
//   - Once only: a plan cannot be blessed twice (ErrAlreadyBlessed), so a
//     double-click cannot double-dispatch.
//
// On a filer error mid-way it returns what was already filed plus the error —
// honest partial progress, never a silent drop.
func (e *Engine) Handoff(ctx context.Context, sessionID, planID string, stepIDs []string, file StepFiler) ([]Created, error) {
	plan := e.planByID(sessionID, planID)
	if plan == nil {
		return nil, ErrNoSuchPlan
	}
	// Validate an explicit subset loudly BEFORE marking blessed or filing anything.
	var want map[string]bool
	if len(stepIDs) > 0 {
		byID := make(map[string]Step, len(plan.Steps))
		for _, s := range plan.Steps {
			byID[s.ID] = s
		}
		want = make(map[string]bool, len(stepIDs))
		for _, id := range stepIDs {
			s, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("steward: blessed step %q is not in plan %q", id, planID)
			}
			if !s.Dispatchable() {
				return nil, fmt.Errorf("steward: blessed step %q is held for the operator and cannot be dispatched", id)
			}
			want[id] = true
		}
	}
	// Confirm there is something to do before consuming the once-only blessing.
	any := false
	for _, s := range plan.Steps {
		if s.Dispatchable() && (want == nil || want[s.ID]) {
			any = true
			break
		}
	}
	if !any {
		return nil, ErrNoDispatchableSteps
	}
	if !e.store.MarkBlessed(sessionID, planID) {
		return nil, ErrAlreadyBlessed
	}
	var created []Created
	for _, s := range plan.Steps {
		if !s.Dispatchable() {
			continue // held for the operator — never filed
		}
		if want != nil && !want[s.ID] {
			continue
		}
		c, err := file(ctx, plan.ID, s)
		if err != nil {
			return created, fmt.Errorf("steward: filing step %q: %w", s.ID, err)
		}
		if c.StepID == "" {
			c.StepID = s.ID
		}
		e.store.AppendCreated(sessionID, c)
		created = append(created, c)
	}
	return created, nil
}

// planByID finds a plan the session actually proposed. Walks backward so the
// latest revision of a plan id wins.
func (e *Engine) planByID(sessionID, planID string) *Plan {
	turns := e.store.History(sessionID)
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Plan != nil && turns[i].Plan.ID == planID {
			return turns[i].Plan
		}
	}
	return nil
}

// PlanFor returns the most recent plan proposed in a session, if any — the
// candidate a subsequent blessing (Handoff, P4.3) would act on. It walks history
// backward so a revised plan supersedes an earlier one.
func (e *Engine) PlanFor(sessionID string) *Plan {
	turns := e.store.History(sessionID)
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Plan != nil {
			return turns[i].Plan
		}
	}
	return nil
}
