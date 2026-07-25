package steward

import (
	"context"
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
