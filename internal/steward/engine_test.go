package steward

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stubRunner returns canned model output, so the engine is exercised without live
// inference. It records the history it was handed, to prove the engine threads the
// conversation through.
type stubRunner struct {
	out     string
	err     error
	lastCtx SessionContext
}

func (s *stubRunner) RunTurn(_ context.Context, _, _ string, sc SessionContext) (string, error) {
	s.lastCtx = sc
	return s.out, s.err
}

func fixedClock() Clock {
	t := time.Unix(1_700_000_000, 0)
	return func() time.Time { return t }
}

// A plain chat turn returns a reply, records it, and — critically — carries no
// plan and no side effect. This is the "however phrased, creates nothing" property
// at the engine level: the Engine has no dispatch dependency to reach.
func TestEngineChatIsInert(t *testing.T) {
	r := &stubRunner{out: `{"reply":"Sure — what host are we serving?"}`}
	e := NewEngine(r).WithClock(fixedClock())
	out, err := e.Chat(context.Background(), "sess1", "help me set up TLS")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if out.Reply == "" || out.Plan != nil {
		t.Fatalf("expected reply, no plan; got %+v", out)
	}
	hist := e.History("sess1")
	if len(hist) != 1 || hist[0].Message != "help me set up TLS" || hist[0].Plan != nil {
		t.Fatalf("turn not recorded inertly: %+v", hist)
	}
}

// A turn that proposes work returns an inert plan; held steps are surfaced, not
// dispatched; the latest plan is retrievable for a later blessing.
func TestEngineChatProposesPlanWithHeldStep(t *testing.T) {
	r := &stubRunner{out: `{"reply":"here is a plan","plan":{"id":"tls","title":"Serve studio over TLS","steps":[
		{"id":"s1","title":"Issue cert","gate":"custom-validator","needs_operator":false},
		{"id":"s2","title":"Caddy vhost","gate":"ci-status","needs_operator":false},
		{"id":"s3","title":"DNS A record — your token","gate":"operator","needs_operator":true}
	]}}`}
	e := NewEngine(r).WithClock(fixedClock())
	out, err := e.Chat(context.Background(), "sess1", "serve studio over TLS")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if out.Plan == nil || len(out.Plan.Steps) != 3 {
		t.Fatalf("plan not returned: %+v", out.Plan)
	}
	if got := len(out.Plan.DispatchSteps()); got != 2 {
		t.Fatalf("DispatchSteps = %d, want 2 (s3 held)", got)
	}
	// The engine surfaces the plan for a later blessing.
	if p := e.PlanFor("sess1"); p == nil || p.ID != "tls" {
		t.Fatalf("PlanFor did not return the proposed plan: %+v", p)
	}
}

// A malformed or invalid model output records NOTHING and returns an error — the
// caller turns it into a graceful reply, never a side effect.
func TestEngineChatRejectsBadOutputWithoutRecording(t *testing.T) {
	for _, tc := range []struct{ name, out string }{
		{"no json", "I couldn't decide."},
		{"no reply", `{"plan":{"id":"x","title":"t","steps":[{"id":"s1","title":"t","gate":"ci-status"}]}}`},
		{"fleet step with operator gate", `{"reply":"ok","plan":{"id":"x","title":"t","steps":[{"id":"s1","title":"t","gate":"operator","needs_operator":false}]}}`},
	} {
		r := &stubRunner{out: tc.out}
		e := NewEngine(r).WithClock(fixedClock())
		if _, err := e.Chat(context.Background(), "s", "do a thing"); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
		if len(e.History("s")) != 0 {
			t.Errorf("%s: a rejected turn must record nothing", tc.name)
		}
	}
}

// A runner error propagates and records nothing.
func TestEngineChatPropagatesRunnerError(t *testing.T) {
	r := &stubRunner{err: errors.New("model unreachable")}
	e := NewEngine(r).WithClock(fixedClock())
	if _, err := e.Chat(context.Background(), "s", "hi"); err == nil {
		t.Fatal("expected runner error to propagate")
	}
	if len(e.History("s")) != 0 {
		t.Fatal("a failed turn must record nothing")
	}
}

// The engine threads prior turns back to the model, so the conversation has memory.
func TestEngineThreadsHistory(t *testing.T) {
	r := &stubRunner{out: `{"reply":"ok"}`}
	e := NewEngine(r).WithClock(fixedClock())
	_, _ = e.Chat(context.Background(), "s", "first")
	_, _ = e.Chat(context.Background(), "s", "second")
	if len(r.lastCtx.Recent) != 1 || r.lastCtx.Recent[0].Message != "first" {
		t.Fatalf("engine did not thread history to the runner: %+v", r.lastCtx.Recent)
	}
}
