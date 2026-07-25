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

// scriptedRunner returns canned model output per call, so the §7 handlers are
// tested without a live interlocutor agent.
type scriptedRunner struct {
	outs []string
	i    int
}

func (r *scriptedRunner) RunTurn(_ context.Context, _, _ string, _ steward.SessionContext) (string, error) {
	out := r.outs[r.i%len(r.outs)]
	r.i++
	return out, nil
}

// recordingFiler stands in for createLabeledIssueAndRoute, so /handoff is tested
// without a live write path but still proves the blessing files what it should.
type gwRecordingFiler struct{ filed []steward.Step }

func (f *gwRecordingFiler) file(_ context.Context, _ string, s steward.Step) (steward.Created, error) {
	f.filed = append(f.filed, s)
	return steward.Created{StepID: s.ID, Repo: "kit/hirdforge", Number: int64(len(f.filed)), Label: "agent:build"}, nil
}

func newStewardTestGateway(runner steward.TurnRunner, filer steward.StepFiler) (*gateway, *http.ServeMux) {
	gw := &gateway{stewardEngine: steward.NewEngine(runner), stewardFiler: filer}
	mux := http.NewServeMux()
	registerStewardRoutes(mux, gw)
	return gw, mux
}

func post(t *testing.T, mux *http.ServeMux, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// A chat turn returns the reply and (when proposed) an inert plan — and never
// files anything (the filer is untouched).
func TestStewardChatEndpointIsInert(t *testing.T) {
	planOut := "Here's a plan:\n```json\n{\"id\":\"p1\",\"title\":\"Do it\",\"steps\":[{\"id\":\"s1\",\"title\":\"build\",\"gate\":\"ci-status\",\"needs_operator\":false}]}\n```\nSound good?"
	filer := &gwRecordingFiler{}
	_, mux := newStewardTestGateway(&scriptedRunner{outs: []string{planOut}}, filer.file)

	rec, out := post(t, mux, "/api/v1/steward/chat", `{"session_id":"s","message":"do a thing"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(out["reply"].(string), "Sound good") {
		t.Fatalf("reply missing: %v", out["reply"])
	}
	if out["plan"] == nil {
		t.Fatal("plan should be surfaced")
	}
	if len(filer.filed) != 0 {
		t.Fatal("chat must not file anything")
	}
}

// A blessing files the plan's dispatchable steps through the filer and returns the
// created issues.
func TestStewardHandoffFiles(t *testing.T) {
	planOut := "ok\n```json\n{\"id\":\"p1\",\"title\":\"T\",\"steps\":[{\"id\":\"s1\",\"title\":\"build\",\"gate\":\"ci-status\",\"needs_operator\":false},{\"id\":\"s2\",\"title\":\"you\",\"gate\":\"operator\",\"needs_operator\":true}]}\n```"
	filer := &gwRecordingFiler{}
	_, mux := newStewardTestGateway(&scriptedRunner{outs: []string{planOut}}, filer.file)

	// Seat the plan with a chat turn, then bless it.
	if rec, _ := post(t, mux, "/api/v1/steward/chat", `{"session_id":"s","message":"plan it"}`); rec.Code != http.StatusOK {
		t.Fatalf("seed chat failed: %d", rec.Code)
	}
	rec, out := post(t, mux, "/api/v1/steward/handoff", `{"session_id":"s","plan_id":"p1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("handoff status = %d: %s", rec.Code, rec.Body.String())
	}
	created, _ := out["created_issues"].([]any)
	if len(created) != 1 {
		t.Fatalf("expected 1 created issue (s2 held), got %d", len(created))
	}
	if len(filer.filed) != 1 || filer.filed[0].ID != "s1" {
		t.Fatalf("wrong step filed: %+v", filer.filed)
	}
}

// A blessing that reaches for a held step is refused loudly with 409 and files
// nothing.
func TestStewardHandoffRefusalIs409(t *testing.T) {
	planOut := "ok\n```json\n{\"id\":\"p1\",\"title\":\"T\",\"steps\":[{\"id\":\"s2\",\"title\":\"you\",\"gate\":\"operator\",\"needs_operator\":true}]}\n```"
	filer := &gwRecordingFiler{}
	_, mux := newStewardTestGateway(&scriptedRunner{outs: []string{planOut}}, filer.file)
	post(t, mux, "/api/v1/steward/chat", `{"session_id":"s","message":"plan it"}`)

	rec, out := post(t, mux, "/api/v1/steward/handoff", `{"session_id":"s","plan_id":"p1","step_ids":["s2"]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("held-step blessing should be 409, got %d", rec.Code)
	}
	if out["error"] == nil || len(filer.filed) != 0 {
		t.Fatalf("refused blessing must file nothing and report an error: %+v", out)
	}
}

// The sessions endpoint rehydrates the recorded turns.
func TestStewardSessionsRehydrates(t *testing.T) {
	filer := &gwRecordingFiler{}
	_, mux := newStewardTestGateway(&scriptedRunner{outs: []string{"just chatting"}}, filer.file)
	post(t, mux, "/api/v1/steward/chat", `{"session_id":"abc","message":"hi"}`)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/steward/sessions/abc", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("sessions status = %d", rec.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	turns, _ := out["turns"].([]any)
	if len(turns) != 1 {
		t.Fatalf("expected 1 recorded turn, got %d", len(turns))
	}
}
