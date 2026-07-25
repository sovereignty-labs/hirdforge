package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.hirdforge.com/kit/hirdforge/internal/steward"
)

type stubInfer struct{ out string }

func (s *stubInfer) Complete(context.Context, string, []steward.Turn, string) (string, error) {
	return s.out, nil
}

type stubIssues struct{ calls int }

func (s *stubIssues) CreateLabeledIssue(_ context.Context, repo, _, _, label string) (steward.Created, error) {
	s.calls++
	return steward.Created{Repo: repo, Number: 5, URL: "http://git/" + repo + "/issues/5", Label: label}, nil
}

func stewardTestGateway(t *testing.T, modelOut string) (*gateway, *http.ServeMux, *stubIssues) {
	t.Helper()
	gw, _ := newCortexTestGateway(t)
	issues := &stubIssues{}
	gw.steward = &steward.Engine{
		Infer: &stubInfer{out: modelOut}, Issues: issues,
		Sessions: steward.NewSessionStore(), DefaultRepo: "kit/hirdforge", BuildLabel: "agent:build",
	}
	mux := http.NewServeMux()
	registerStewardRoutes(mux, gw)
	return gw, mux, issues
}

func postChat(t *testing.T, mux *http.ServeMux, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/steward/chat", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// TestStewardChatCreatesIssueAndRecordsSession is the §7 happy path: prose in,
// an issue filed, and the turn retrievable for a UI to rehydrate.
func TestStewardChatCreatesIssueAndRecordsSession(t *testing.T) {
	_, mux, issues := stewardTestGateway(t,
		`{"intent":"create_issue","reply":"On it.","issue":{"title":"Add Clamp","body":"Add Clamp to pkg/tools. Acceptance: table-driven test."}}`)

	out := postChat(t, mux, `{"session_id":"s1","message":"add a clamp helper"}`)
	if out["intent"] != "create_issue" {
		t.Fatalf("intent = %v", out["intent"])
	}
	if issues.calls != 1 {
		t.Fatalf("expected one issue created, got %d", issues.calls)
	}
	ci, ok := out["created_issue"].(map[string]any)
	if !ok || ci["number"].(float64) != 5 {
		t.Fatalf("created_issue not returned: %v", out["created_issue"])
	}

	// The session is retrievable (GET /steward/sessions/{id}).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/steward/sessions/s1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "add a clamp helper") {
		t.Fatalf("session history missing: %d %s", rec.Code, rec.Body.String())
	}
}

// TestStewardChatRejectsEmptyMessage.
func TestStewardChatRejectsEmptyMessage(t *testing.T) {
	_, mux, _ := stewardTestGateway(t, `{"intent":"chat","reply":"hi"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/steward/chat", strings.NewReader(`{"session_id":"s","message":"   "}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty message status = %d, want 400", rec.Code)
	}
}

// TestStewardChatJunkProposalCreatesNothing: the endpoint must inherit the
// engine's safety property — a model that returns garbage files no issue.
func TestStewardChatJunkProposalCreatesNothing(t *testing.T) {
	_, mux, issues := stewardTestGateway(t, "I went ahead and merged it for you")
	out := postChat(t, mux, `{"session_id":"s2","message":"do something"}`)
	if issues.calls != 0 {
		t.Fatalf("junk model output created %d issue(s)", issues.calls)
	}
	if out["intent"] != "clarify" {
		t.Fatalf("intent = %v, want clarify", out["intent"])
	}
}

// TestStewardChatMintsSessionID: a first turn without a session id still works
// and returns one, so a UI can continue the thread.
func TestStewardChatMintsSessionID(t *testing.T) {
	_, mux, _ := stewardTestGateway(t, `{"intent":"chat","reply":"hello"}`)
	out := postChat(t, mux, `{"message":"hi"}`)
	if s, _ := out["session_id"].(string); s == "" {
		t.Fatal("no session_id returned for a new conversation")
	}
}
