package steward

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// recordingFiler files steps into an in-memory list, standing in for the
// gateway's createLabeledIssueAndRoute so the blessing logic is tested without a
// live write path.
type recordingFiler struct {
	filed  []Step
	failOn string // step id to fail on, "" = never
	n      int64
}

func (f *recordingFiler) file(_ context.Context, _ string, s Step) (Created, error) {
	if s.ID == f.failOn {
		return Created{}, errors.New("gitea unreachable")
	}
	f.filed = append(f.filed, s)
	f.n++
	return Created{StepID: s.ID, Repo: "kit/hirdforge", Number: f.n, Label: "agent:build"}, nil
}

// seatPlan drives one chat turn so a plan is recorded in the session, the way a
// real handoff always follows a proposal.
func seatPlan(t *testing.T, out string) (*Engine, string) {
	t.Helper()
	e := NewEngine(&stubRunner{out: out}).WithClock(fixedClock())
	if _, err := e.Chat(context.Background(), "s", "do the thing"); err != nil {
		t.Fatalf("seat plan: %v", err)
	}
	return e, "s"
}

const tlsPlan = `{"reply":"plan","plan":{"id":"tls","title":"TLS","steps":[
	{"id":"s1","title":"cert","gate":"custom-validator","needs_operator":false},
	{"id":"s2","title":"vhost","gate":"ci-status","needs_operator":false},
	{"id":"s3","title":"dns","gate":"operator","needs_operator":true}
]}}`

// The blessing files the dispatchable steps and holds the operator step —
// partial dispatch, with provenance stamped on each created record.
func TestHandoffFilesDispatchableStepsOnly(t *testing.T) {
	e, s := seatPlan(t, tlsPlan)
	f := &recordingFiler{}
	created, err := e.Handoff(context.Background(), s, "tls", nil, f.file)
	if err != nil {
		t.Fatalf("Handoff: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("filed %d, want 2 (s3 held)", len(created))
	}
	for _, s := range f.filed {
		if s.ID == "s3" {
			t.Fatal("operator-held step s3 must not be filed")
		}
	}
	if created[0].StepID == "" {
		t.Fatal("created record missing step_id provenance")
	}
	// Traceability: the session records what was filed.
	if got := len(e.Created(s)); got != 2 {
		t.Fatalf("session recorded %d created, want 2", got)
	}
}

// A blessing may name an explicit subset; only those file.
func TestHandoffExplicitSubset(t *testing.T) {
	e, s := seatPlan(t, tlsPlan)
	f := &recordingFiler{}
	created, err := e.Handoff(context.Background(), s, "tls", []string{"s2"}, f.file)
	if err != nil {
		t.Fatalf("Handoff: %v", err)
	}
	if len(created) != 1 || f.filed[0].ID != "s2" {
		t.Fatalf("subset dispatch wrong: %+v", f.filed)
	}
}

// Blessing a held or unknown step refuses the WHOLE handoff — loud, never a
// silent partial — and files nothing.
func TestHandoffRefusesHeldOrUnknownStepLoudly(t *testing.T) {
	for _, tc := range []struct{ name, id, want string }{
		{"held step", "s3", "held for the operator"},
		{"unknown step", "s9", "not in plan"},
	} {
		e, s := seatPlan(t, tlsPlan)
		f := &recordingFiler{}
		_, err := e.Handoff(context.Background(), s, "tls", []string{tc.id}, f.file)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: expected refusal %q, got %v", tc.name, tc.want, err)
		}
		if len(f.filed) != 0 {
			t.Errorf("%s: nothing must be filed on a refused blessing", tc.name)
		}
	}
}

// An unblessable plan (unknown, or all-held) is refused before anything is filed.
func TestHandoffRejectsUnblessablePlans(t *testing.T) {
	e, s := seatPlan(t, tlsPlan)
	f := &recordingFiler{}
	if _, err := e.Handoff(context.Background(), s, "nope", nil, f.file); !errors.Is(err, ErrNoSuchPlan) {
		t.Fatalf("unknown plan: want ErrNoSuchPlan, got %v", err)
	}
	heldOnly := `{"reply":"p","plan":{"id":"held","title":"t","steps":[{"id":"h1","title":"you","gate":"operator","needs_operator":true}]}}`
	e2, s2 := seatPlan(t, heldOnly)
	if _, err := e2.Handoff(context.Background(), s2, "held", nil, f.file); !errors.Is(err, ErrNoDispatchableSteps) {
		t.Fatalf("all-held plan: want ErrNoDispatchableSteps, got %v", err)
	}
	if len(f.filed) != 0 {
		t.Fatal("nothing must be filed for an unblessable plan")
	}
}

// A plan cannot be blessed twice — the guard against a double-dispatch.
func TestHandoffIsOnceOnly(t *testing.T) {
	e, s := seatPlan(t, tlsPlan)
	f := &recordingFiler{}
	if _, err := e.Handoff(context.Background(), s, "tls", nil, f.file); err != nil {
		t.Fatalf("first bless: %v", err)
	}
	if _, err := e.Handoff(context.Background(), s, "tls", nil, f.file); !errors.Is(err, ErrAlreadyBlessed) {
		t.Fatalf("second bless: want ErrAlreadyBlessed, got %v", err)
	}
	if len(f.filed) != 2 {
		t.Fatalf("double-bless must not double-file: filed %d", len(f.filed))
	}
}

// A filer error mid-way returns honest partial progress plus the error, and the
// once-only guard means the plan is not silently re-blessable to "finish" it.
func TestHandoffFilerErrorIsHonestPartial(t *testing.T) {
	e, s := seatPlan(t, tlsPlan)
	f := &recordingFiler{failOn: "s2"}
	created, err := e.Handoff(context.Background(), s, "tls", nil, f.file)
	if err == nil || !strings.Contains(err.Error(), "filing step \"s2\"") {
		t.Fatalf("expected filing error naming s2, got %v", err)
	}
	// s1 filed before s2 failed — reported and recorded, not dropped.
	if len(created) != 1 || created[0].StepID != "s1" {
		t.Fatalf("partial progress wrong: %+v", created)
	}
	if got := len(e.Created(s)); got != 1 {
		t.Fatalf("session should record the one that succeeded, got %d", got)
	}
}
