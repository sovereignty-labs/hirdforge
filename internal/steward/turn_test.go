package steward

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeInfer struct {
	out  string
	err  error
	seen struct {
		system  string
		history []Turn
		message string
	}
}

func (f *fakeInfer) Complete(_ context.Context, system string, history []Turn, message string) (string, error) {
	f.seen.system, f.seen.history, f.seen.message = system, history, message
	return f.out, f.err
}

type fakeIssues struct {
	calls    int
	lastRep  string
	lastLbl  string
	lastBody string
	err      error
}

func (f *fakeIssues) CreateLabeledIssue(_ context.Context, repo, title, body, label string) (Created, error) {
	f.calls++
	f.lastRep, f.lastLbl, f.lastBody = repo, label, body
	if f.err != nil {
		return Created{}, f.err
	}
	return Created{Repo: repo, Number: 77, URL: "http://git/x/77", Label: label}, nil
}

type fakeStatus struct {
	out string
	err error
}

func (f *fakeStatus) Summarize(context.Context, string) (string, error) { return f.out, f.err }

func newEngine(infer *fakeInfer, issues *fakeIssues, status *fakeStatus) *Engine {
	return &Engine{Infer: infer, Issues: issues, Status: status, Sessions: NewSessionStore(),
		DefaultRepo: "kit/hirdforge", BuildLabel: "agent:build"}
}

// TestCreateIssueTurnFilesThroughTheOnePath: a create_issue proposal results in
// exactly one labeled issue on the configured repo, and the reply tells the
// operator what happened.
func TestCreateIssueTurnFilesThroughTheOnePath(t *testing.T) {
	infer := &fakeInfer{out: `{"intent":"create_issue","reply":"On it.","issue":{"title":"Add Clamp","body":"Add Clamp to pkg/tools.","acceptance":"table-driven test passes"}}`}
	issues := &fakeIssues{}
	e := newEngine(infer, issues, nil)

	res, err := e.Run(context.Background(), "s1", "please add a Clamp helper")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if issues.calls != 1 {
		t.Fatalf("expected exactly one issue created, got %d", issues.calls)
	}
	if issues.lastRep != "kit/hirdforge" || issues.lastLbl != "agent:build" {
		t.Fatalf("defaults not applied: repo=%q label=%q", issues.lastRep, issues.lastLbl)
	}
	if !strings.Contains(issues.lastBody, "Acceptance:") {
		t.Fatalf("acceptance criteria not folded into the body: %q", issues.lastBody)
	}
	if res.Created == nil || res.Created.Number != 77 {
		t.Fatalf("result missing the created issue: %+v", res)
	}
	if !strings.Contains(res.Reply, "77") {
		t.Fatalf("reply should tell the operator what was filed: %q", res.Reply)
	}
	if h := e.Sessions.History("s1"); len(h) != 1 || h[0].Issue == nil {
		t.Fatalf("turn not recorded with its issue: %+v", h)
	}
}

// TestUnusableProposalNeverActs is the safety property: if the model returns
// junk, the turn degrades to a clarification and NOTHING is created.
func TestUnusableProposalNeverActs(t *testing.T) {
	for _, out := range []string{
		"I'll just do it myself",                                       // no JSON
		`{"intent":"merge_it","reply":"done"}`,                         // unknown intent
		`{"intent":"create_issue","reply":"ok"}`,                       // no issue
		`{"intent":"create_issue","reply":"ok","issue":{"title":"t"}}`, // no body
	} {
		issues := &fakeIssues{}
		e := newEngine(&fakeInfer{out: out}, issues, nil)
		res, err := e.Run(context.Background(), "s2", "do the thing")
		if err != nil {
			t.Fatalf("run must not error on a bad proposal: %v", err)
		}
		if issues.calls != 0 {
			t.Errorf("output %q caused %d issue(s) — must create none", out, issues.calls)
		}
		if res.Intent != IntentClarify {
			t.Errorf("output %q -> intent %q, want clarify", out, res.Intent)
		}
	}
}

// TestStatusAnswersFromRecordsNotTheModel: a status_query is answered by the
// StatusReader projection; the model's own prose is replaced.
func TestStatusAnswersFromRecordsNotTheModel(t *testing.T) {
	infer := &fakeInfer{out: `{"intent":"status_query","reply":"I think everything is fine probably"}`}
	e := newEngine(infer, &fakeIssues{}, &fakeStatus{out: "task hf-1 is building (attempt 2)"})
	res, err := e.Run(context.Background(), "s3", "what's running?")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Reply, "probably") {
		t.Fatalf("model recollection leaked into a status answer: %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "hf-1") {
		t.Fatalf("status answer must come from the records: %q", res.Reply)
	}
}

// TestStatusReadFailureIsHonest: if the records can't be read, say so rather
// than falling back to the model's guess.
func TestStatusReadFailureIsHonest(t *testing.T) {
	infer := &fakeInfer{out: `{"intent":"status_query","reply":"everything looks great"}`}
	e := newEngine(infer, &fakeIssues{}, &fakeStatus{err: errors.New("db down")})
	res, _ := e.Run(context.Background(), "s4", "status?")
	if strings.Contains(res.Reply, "great") {
		t.Fatalf("must not fall back to the model's guess: %q", res.Reply)
	}
	if !strings.Contains(res.Reply, "won't guess") {
		t.Fatalf("failure should be stated plainly: %q", res.Reply)
	}
}

// TestIssueCreateFailureIsNotClaimedAsSuccess.
func TestIssueCreateFailureIsNotClaimedAsSuccess(t *testing.T) {
	infer := &fakeInfer{out: `{"intent":"create_issue","reply":"Filed!","issue":{"title":"t","body":"b"}}`}
	e := newEngine(infer, &fakeIssues{err: errors.New("gitea 502")}, nil)
	res, _ := e.Run(context.Background(), "s5", "add a thing")
	if res.Created != nil {
		t.Fatal("must not report a created issue when creation failed")
	}
	if !strings.Contains(res.Reply, "couldn't file it") {
		t.Fatalf("failure must be admitted: %q", res.Reply)
	}
}

// TestHistoryIsPassedToTheModel: a conversation has continuity.
func TestHistoryIsPassedToTheModel(t *testing.T) {
	infer := &fakeInfer{out: `{"intent":"chat","reply":"hi"}`}
	e := newEngine(infer, &fakeIssues{}, nil)
	if _, err := e.Run(context.Background(), "s6", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), "s6", "second"); err != nil {
		t.Fatal(err)
	}
	if len(infer.seen.history) != 1 || infer.seen.history[0].Message != "first" {
		t.Fatalf("second turn did not receive prior history: %+v", infer.seen.history)
	}
	if !strings.Contains(infer.seen.system, "CANNOT dispatch") {
		t.Fatal("system prompt must state the Steward's limits")
	}
}
