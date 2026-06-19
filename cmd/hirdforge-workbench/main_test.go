package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWorkbenchHealth(t *testing.T) {
	mux := newWorkbench().mux
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected application/json content type, got %q", got)
	}

	var body struct {
		Status string `json:"status"`
		Mode   string `json:"mode"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal body: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("expected status=ok, got %q", body.Status)
	}
	if body.Mode != "workbench" {
		t.Errorf("expected mode=workbench, got %q", body.Mode)
	}
}

func TestWorkbenchIndex(t *testing.T) {
	mux := newWorkbench().mux
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("expected text/html content type, got %q", got)
	}
	if !strings.Contains(w.Body.String(), "Hirdforge Workbench") {
		t.Errorf("expected body to contain title, got: %q", w.Body.String())
	}
}

func TestWorkbenchRejectsNonGET(t *testing.T) {
	mux := newWorkbench().mux
	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", w.Code)
	}
}

func TestWorkbenchUnknownPath(t *testing.T) {
	mux := newWorkbench().mux
	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", w.Code)
	}
}

func TestEventStoreAppendList(t *testing.T) {
	s := newEventStore()

	first := s.Append("alpha", "first message", json.RawMessage(`{"k":1}`))
	second := s.Append("beta", "", nil)

	if first.ID == "" || second.ID == "" {
		t.Fatal("expected ids to be set")
	}
	if first.ID == second.ID {
		t.Fatalf("expected distinct ids, got %q twice", first.ID)
	}
	if first.TS.IsZero() || second.TS.IsZero() {
		t.Fatal("expected timestamps to be set")
	}
	if !first.TS.Before(second.TS) && !first.TS.Equal(second.TS) {
		t.Errorf("expected non-decreasing timestamps, got first=%v second=%v", first.TS, second.TS)
	}
	if first.Type != "alpha" || second.Type != "beta" {
		t.Errorf("unexpected types: %q %q", first.Type, second.Type)
	}
	if first.Message != "first message" {
		t.Errorf("expected first message to round-trip, got %q", first.Message)
	}
	if string(first.Data) != `{"k":1}` {
		t.Errorf("expected first data to round-trip, got %s", first.Data)
	}
	if second.Message != "" {
		t.Errorf("expected empty message, got %q", second.Message)
	}
	if second.Data != nil {
		t.Errorf("expected nil data, got %s", second.Data)
	}

	list := s.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 events, got %d", len(list))
	}
	if list[0].ID != first.ID || list[1].ID != second.ID {
		t.Errorf("expected insertion order, got %+v", list)
	}

	list[0].Type = "mutated"
	again := s.List()
	if again[0].Type != "alpha" {
		t.Errorf("expected List to return a defensive copy, got %q", again[0].Type)
	}
}

func TestWorkbenchHasStartupEvent(t *testing.T) {
	wb := newWorkbench()
	events := wb.store.List()
	if len(events) != 1 {
		t.Fatalf("expected 1 event after startup, got %d", len(events))
	}
	if events[0].Type != startupType {
		t.Errorf("expected type=%q, got %q", startupType, events[0].Type)
	}
	if events[0].ID == "" {
		t.Error("expected startup event id to be set")
	}
	if events[0].TS.IsZero() {
		t.Error("expected startup event ts to be set")
	}
}

func TestWorkbenchEventsGET(t *testing.T) {
	wb := newWorkbench()
	wb.store.Append("custom.event", "hello", json.RawMessage(`{"foo":"bar"}`))

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/events", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected application/json content type, got %q", got)
	}

	var got []WorkbenchEvent
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got))
	}
	if got[0].Type != startupType {
		t.Errorf("expected first event %q, got %q", startupType, got[0].Type)
	}
	if got[1].Type != "custom.event" {
		t.Errorf("expected second event custom.event, got %q", got[1].Type)
	}
	if got[1].Message != "hello" {
		t.Errorf("expected message=hello, got %q", got[1].Message)
	}
	if string(got[1].Data) != `{"foo":"bar"}` {
		t.Errorf("expected data to round-trip, got %s", got[1].Data)
	}
}

func TestWorkbenchEventsGETEmpty(t *testing.T) {
	wb := &workbench{store: newEventStore(), project: newProjectState(), provider: newProviderState(), sessions: newSessionStore(), inspection: newInspectionState(), proposals: newProposalStore()}
	wb.mux = wb.registerRoutes()

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/events", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := strings.TrimSpace(w.Body.String())
	if body != "[]" {
		t.Errorf("expected body to be [] for empty store, got %q", body)
	}
}

func TestWorkbenchEventsPOST(t *testing.T) {
	wb := newWorkbench()
	payload := `{"type":"user.action","message":"clicked","data":{"button":"save"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/workbench/events", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected application/json content type, got %q", got)
	}

	var ev WorkbenchEvent
	if err := json.Unmarshal(w.Body.Bytes(), &ev); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ev.Type != "user.action" {
		t.Errorf("expected type=user.action, got %q", ev.Type)
	}
	if ev.Message != "clicked" {
		t.Errorf("expected message=clicked, got %q", ev.Message)
	}
	if string(ev.Data) != `{"button":"save"}` {
		t.Errorf("expected data round-trip, got %s", ev.Data)
	}
	if ev.ID == "" {
		t.Error("expected id to be set on response")
	}
	if ev.TS.IsZero() {
		t.Error("expected ts to be set on response")
	}
	if time.Since(ev.TS) > time.Minute {
		t.Errorf("expected ts to be recent, got %v", ev.TS)
	}

	list := wb.store.List()
	if len(list) != 2 {
		t.Fatalf("expected store to have 2 events, got %d", len(list))
	}
	if list[1].ID != ev.ID {
		t.Errorf("expected returned event to match store tail, got %q vs %q", ev.ID, list[1].ID)
	}
}

func TestWorkbenchEventsPOSTBadJSON(t *testing.T) {
	wb := newWorkbench()
	req := httptest.NewRequest(http.MethodPost, "/api/workbench/events", strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestWorkbenchEventsPOSTMissingType(t *testing.T) {
	wb := newWorkbench()
	req := httptest.NewRequest(http.MethodPost, "/api/workbench/events", strings.NewReader(`{"message":"no type"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestWorkbenchEventsMethodNotAllowed(t *testing.T) {
	wb := newWorkbench()
	req := httptest.NewRequest(http.MethodPut, "/api/workbench/events", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func gitAvailable(t *testing.T) bool {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
		return false
	}
	return true
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, ".gitkeep"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".gitkeep")
	runGit(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func postJSON(t *testing.T, wb *workbench, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	return w
}

func TestWorkbenchProjectOpenGitRepo(t *testing.T) {
	if !gitAvailable(t) {
		return
	}
	dir := initGitRepo(t)

	wb := newWorkbench()
	w := postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected application/json content type, got %q", got)
	}

	var state ProjectState
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.Path != dir {
		t.Errorf("expected path=%q, got %q", dir, state.Path)
	}
	if state.Name != filepath.Base(dir) {
		t.Errorf("expected name=%q, got %q", filepath.Base(dir), state.Name)
	}
	if !state.Git {
		t.Error("expected git=true")
	}
	if state.CurrentBranch == "" {
		t.Error("expected current_branch to be set")
	}
}

func TestWorkbenchProjectOpenRelative(t *testing.T) {
	wb := newWorkbench()
	w := postJSON(t, wb, "/api/workbench/project/open", `{"path":"relative/path"}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchProjectOpenMissing(t *testing.T) {
	wb := newWorkbench()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	w := postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+missing+`"}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchProjectOpenFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	wb := newWorkbench()
	w := postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+file+`"}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchProjectOpenMissingPathField(t *testing.T) {
	wb := newWorkbench()
	w := postJSON(t, wb, "/api/workbench/project/open", `{}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchProjectOpenMethodNotAllowed(t *testing.T) {
	wb := newWorkbench()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/project/open", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestWorkbenchProjectGETNotOpen(t *testing.T) {
	wb := newWorkbench()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/project", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestWorkbenchProjectGETAfterOpen(t *testing.T) {
	dir := t.TempDir()
	wb := newWorkbench()

	openResp := postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	if openResp.Code != http.StatusCreated {
		t.Fatalf("open: expected 201, got %d: %s", openResp.Code, openResp.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/project", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected application/json content type, got %q", got)
	}

	var state ProjectState
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.Path != dir {
		t.Errorf("expected path=%q, got %q", dir, state.Path)
	}
	if state.Name != filepath.Base(dir) {
		t.Errorf("expected name=%q, got %q", filepath.Base(dir), state.Name)
	}
	if state.Git {
		t.Error("expected git=false for non-git dir")
	}
	if state.CurrentBranch != "" {
		t.Errorf("expected empty current_branch, got %q", state.CurrentBranch)
	}
}

func TestWorkbenchProjectOpenedEvent(t *testing.T) {
	dir := t.TempDir()
	wb := newWorkbench()

	openResp := postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	if openResp.Code != http.StatusCreated {
		t.Fatalf("open: expected 201, got %d: %s", openResp.Code, openResp.Body.String())
	}

	events := wb.store.List()
	if len(events) != 2 {
		t.Fatalf("expected 2 events (startup + project.opened), got %d", len(events))
	}
	if events[0].Type != startupType {
		t.Errorf("expected first event %q, got %q", startupType, events[0].Type)
	}
	if events[1].Type != "project.opened" {
		t.Errorf("expected second event project.opened, got %q", events[1].Type)
	}
	expectedMsg := "Opened project " + filepath.Base(dir)
	if events[1].Message != expectedMsg {
		t.Errorf("expected message=%q, got %q", expectedMsg, events[1].Message)
	}
	if len(events[1].Data) == 0 {
		t.Fatal("expected data payload on project.opened event")
	}
	var data ProjectState
	if err := json.Unmarshal(events[1].Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if data.Path != dir {
		t.Errorf("expected data.path=%q, got %q", dir, data.Path)
	}
	if data.Name != filepath.Base(dir) {
		t.Errorf("expected data.name=%q, got %q", filepath.Base(dir), data.Name)
	}
	if data.Git {
		t.Error("expected data.git=false")
	}
}

func TestWorkbenchProviderConfigureStores(t *testing.T) {
	wb := newWorkbench()
	body := `{"base_url":"https://api.minimax.io/v1","api_key":"super-secret","model":"MiniMax-M3"}`
	w := postJSON(t, wb, "/api/workbench/provider", body)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected application/json content type, got %q", got)
	}

	var state ProviderState
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.BaseURL != "https://api.minimax.io/v1" {
		t.Errorf("expected base_url=https://api.minimax.io/v1, got %q", state.BaseURL)
	}
	if !state.APIKeySet {
		t.Error("expected api_key_set=true")
	}
	if state.Model != "MiniMax-M3" {
		t.Errorf("expected model=MiniMax-M3, got %q", state.Model)
	}

	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, hasKey := raw["api_key"]; hasKey {
		t.Error("POST response must not contain api_key field")
	}
	if v, ok := raw["api_key_set"]; !ok || v != true {
		t.Errorf("expected api_key_set=true in raw response, got %v", v)
	}
}

func TestWorkbenchProviderGETNotConfigured(t *testing.T) {
	wb := newWorkbench()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/provider", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestWorkbenchProviderGETConfigured(t *testing.T) {
	wb := newWorkbench()
	body := `{"base_url":"https://api.minimax.io/v1","api_key":"super-secret","model":"MiniMax-M3"}`
	postJSON(t, wb, "/api/workbench/provider", body)

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/provider", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var state ProviderState
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.BaseURL != "https://api.minimax.io/v1" {
		t.Errorf("expected base_url=https://api.minimax.io/v1, got %q", state.BaseURL)
	}
	if !state.APIKeySet {
		t.Error("expected api_key_set=true")
	}
	if state.Model != "MiniMax-M3" {
		t.Errorf("expected model=MiniMax-M3, got %q", state.Model)
	}

	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, hasKey := raw["api_key"]; hasKey {
		t.Error("GET response must not contain api_key field")
	}
}

func TestWorkbenchProviderRejectsMissingBaseURL(t *testing.T) {
	wb := newWorkbench()
	w := postJSON(t, wb, "/api/workbench/provider", `{"api_key":"key","model":"m"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchProviderRejectsMissingAPIKey(t *testing.T) {
	wb := newWorkbench()
	w := postJSON(t, wb, "/api/workbench/provider", `{"base_url":"https://api.example.com","model":"m"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchProviderRejectsMissingModel(t *testing.T) {
	wb := newWorkbench()
	w := postJSON(t, wb, "/api/workbench/provider", `{"base_url":"https://api.example.com","api_key":"key"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchProviderRejectsInvalidBaseURL(t *testing.T) {
	wb := newWorkbench()
	w := postJSON(t, wb, "/api/workbench/provider", `{"base_url":"ftp://example.com","api_key":"key","model":"m"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchProviderConfiguredEvent(t *testing.T) {
	wb := newWorkbench()
	body := `{"base_url":"https://api.minimax.io/v1","api_key":"super-secret","model":"MiniMax-M3"}`
	postJSON(t, wb, "/api/workbench/provider", body)

	events := wb.store.List()
	var found *WorkbenchEvent
	for i := range events {
		if events[i].Type == "provider.configured" {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatal("provider.configured event not found")
	}
	if found.Message != "Configured provider model MiniMax-M3" {
		t.Errorf("unexpected message: %q", found.Message)
	}

	var data map[string]any
	if err := json.Unmarshal(found.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if _, hasKey := data["api_key"]; hasKey {
		t.Error("event data must not contain api_key field")
	}
	if data["base_url"] != "https://api.minimax.io/v1" {
		t.Errorf("expected data.base_url=https://api.minimax.io/v1, got %v", data["base_url"])
	}
	if data["model"] != "MiniMax-M3" {
		t.Errorf("expected data.model=MiniMax-M3, got %v", data["model"])
	}
	if data["api_key_set"] != true {
		t.Errorf("expected data.api_key_set=true, got %v", data["api_key_set"])
	}
}

func TestWorkbenchProviderTestNotConfigured(t *testing.T) {
	wb := newWorkbench()
	w := postJSON(t, wb, "/api/workbench/provider/test", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchProviderTestSuccess(t *testing.T) {
	var receivedAuth string
	var receivedBody struct {
		Model       string             `json:"model"`
		Messages    []map[string]string `json:"messages"`
		Temperature float64            `json:"temperature"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		receivedAuth = r.Header.Get("Authorization")
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("unexpected content-type: %s", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "test",
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "hirdforge provider online"}},
			},
		})
	}))
	defer server.Close()

	wb := newWorkbench()
	configBody := `{"base_url":"` + server.URL + `","api_key":"test-key","model":"test-model"}`
	postJSON(t, wb, "/api/workbench/provider", configBody)

	w := postJSON(t, wb, "/api/workbench/provider/test", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if receivedAuth != "Bearer test-key" {
		t.Errorf("expected auth=Bearer test-key, got %q", receivedAuth)
	}
	if receivedBody.Model != "test-model" {
		t.Errorf("expected upstream model=test-model, got %q", receivedBody.Model)
	}
	if receivedBody.Temperature != 0 {
		t.Errorf("expected upstream temperature=0, got %v", receivedBody.Temperature)
	}
	if len(receivedBody.Messages) != 1 ||
		receivedBody.Messages[0]["role"] != "user" ||
		receivedBody.Messages[0]["content"] != "Reply with exactly: hirdforge provider online" {
		t.Errorf("unexpected upstream messages: %+v", receivedBody.Messages)
	}

	var result struct {
		OK     bool   `json:"ok"`
		Status int    `json:"status"`
		Model  string `json:"model"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.OK {
		t.Error("expected ok=true")
	}
	if result.Status != http.StatusOK {
		t.Errorf("expected status=200, got %d", result.Status)
	}
	if result.Model != "test-model" {
		t.Errorf("expected model=test-model, got %q", result.Model)
	}

	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, hasKey := raw["api_key"]; hasKey {
		t.Error("test response must not contain api_key field")
	}

	events := wb.store.List()
	var found *WorkbenchEvent
	for i := range events {
		if events[i].Type == "provider.tested" {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatal("provider.tested event not found")
	}
	if found.Message != "Provider test succeeded" {
		t.Errorf("unexpected message: %q", found.Message)
	}
}

func TestWorkbenchProviderTestFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream is on fire","type":"server_error"}}`))
	}))
	defer server.Close()

	wb := newWorkbench()
	configBody := `{"base_url":"` + server.URL + `","api_key":"test-key","model":"test-model"}`
	postJSON(t, wb, "/api/workbench/provider", configBody)

	w := postJSON(t, wb, "/api/workbench/provider/test", "")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}

	var result struct {
		OK     bool   `json:"ok"`
		Status int    `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.OK {
		t.Error("expected ok=false")
	}
	if result.Status != http.StatusInternalServerError {
		t.Errorf("expected status=500, got %d", result.Status)
	}
	if result.Error == "" {
		t.Error("expected error message to be populated")
	}
	if !strings.Contains(result.Error, "upstream is on fire") {
		t.Errorf("expected error to include upstream message, got %q", result.Error)
	}

	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, hasKey := raw["api_key"]; hasKey {
		t.Error("failure response must not contain api_key field")
	}

	events := wb.store.List()
	var found *WorkbenchEvent
	for i := range events {
		if events[i].Type == "provider.test_failed" {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatal("provider.test_failed event not found")
	}
	if found.Message != "Provider test failed" {
		t.Errorf("unexpected message: %q", found.Message)
	}
}

// startMockProvider starts an httptest server that stands in for the provider
// and registers its shutdown with the test. It is used so build/start tests
// never make a real network call.
func startMockProvider(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

// planResponse returns a handler that replies with an OpenAI-style envelope
// whose message content is a JSON plan with the given steps.
func planResponse(plan []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		content, _ := json.Marshal(map[string]any{"plan": plan})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "test",
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": string(content)}},
			},
		})
	}
}

func openGitProjectForBuilder(t *testing.T, wb *workbench) string {
	t.Helper()
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	dir := initGitRepo(t)
	server := startMockProvider(t, planResponse([]string{"alpha", "beta", "gamma"}))
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"k","model":"m"}`)
	return dir
}

func TestWorkbenchBuildStartRejectsMissingGoal(t *testing.T) {
	wb := newWorkbench()
	openGitProjectForBuilder(t, wb)

	w := postJSON(t, wb, "/api/workbench/build/start", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchBuildStart409WithoutProject(t *testing.T) {
	wb := newWorkbench()
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"https://api.example.com","api_key":"k","model":"m"}`)

	w := postJSON(t, wb, "/api/workbench/build/start", `{"goal":"ship it"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchBuildStart409WithoutProvider(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	wb := newWorkbench()
	dir := initGitRepo(t)
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)

	w := postJSON(t, wb, "/api/workbench/build/start", `{"goal":"ship it"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchBuildStartSuccess(t *testing.T) {
	wb := newWorkbench()
	dir := openGitProjectForBuilder(t, wb)

	w := postJSON(t, wb, "/api/workbench/build/start", `{"goal":"ship it"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var session BuilderSession
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if session.ID == "" {
		t.Error("expected id to be set")
	}
	if session.TS.IsZero() {
		t.Error("expected ts to be set")
	}
	if session.Goal != "ship it" {
		t.Errorf("expected goal=ship it, got %q", session.Goal)
	}
	if session.Status != "planned" {
		t.Errorf("expected status=planned, got %q", session.Status)
	}
	if session.Project.Path != dir {
		t.Errorf("expected project.path=%q, got %q", dir, session.Project.Path)
	}
	if session.Project.Name != filepath.Base(dir) {
		t.Errorf("expected project.name=%q, got %q", filepath.Base(dir), session.Project.Name)
	}
	if !session.Project.Git {
		t.Error("expected project.git=true")
	}
	if session.Project.CurrentBranch == "" {
		t.Error("expected project.current_branch to be set")
	}
	if session.ProviderModel != "m" {
		t.Errorf("expected provider_model=m, got %q", session.ProviderModel)
	}
	if len(session.Plan) != 3 {
		t.Errorf("expected 3 plan items, got %d", len(session.Plan))
	}

	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, hasKey := raw["api_key"]; hasKey {
		t.Error("session response must not contain api_key field")
	}
	if _, hasKey := raw["provider"]; hasKey {
		t.Error("session response must not contain provider object")
	}
}

func TestWorkbenchBuildStartAppendsEvents(t *testing.T) {
	wb := newWorkbench()
	openGitProjectForBuilder(t, wb)

	w := postJSON(t, wb, "/api/workbench/build/start", `{"goal":"ship it"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	events := wb.store.List()
	var goalCreated, planCreated *WorkbenchEvent
	for i := range events {
		switch events[i].Type {
		case "goal.created":
			goalCreated = &events[i]
		case "builder.plan.created":
			planCreated = &events[i]
		}
	}
	if goalCreated == nil {
		t.Fatal("goal.created event not found")
	}
	if planCreated == nil {
		t.Fatal("builder.plan.created event not found")
	}
	if goalCreated.Message != "Created goal: ship it" {
		t.Errorf("unexpected goal.created message: %q", goalCreated.Message)
	}
	if planCreated.Message != "Created Builder plan" {
		t.Errorf("unexpected builder.plan.created message: %q", planCreated.Message)
	}
	if len(goalCreated.Data) == 0 || len(planCreated.Data) == 0 {
		t.Error("expected both events to carry session data")
	}
}

func TestWorkbenchBuildSessionGET404(t *testing.T) {
	wb := newWorkbench()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/build/session", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestWorkbenchBuildSessionGETAfterStart(t *testing.T) {
	wb := newWorkbench()
	openGitProjectForBuilder(t, wb)
	postJSON(t, wb, "/api/workbench/build/start", `{"goal":"ship it"}`)

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/build/session", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var session BuilderSession
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if session.Goal != "ship it" {
		t.Errorf("expected goal=ship it, got %q", session.Goal)
	}
	if session.Status != "planned" {
		t.Errorf("expected status=planned, got %q", session.Status)
	}
}

func TestWorkbenchBuildSessionsGETEmpty(t *testing.T) {
	wb := newWorkbench()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/build/sessions", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := strings.TrimSpace(w.Body.String())
	if body != "[]" {
		t.Errorf("expected body to be [] for empty sessions, got %q", body)
	}
}

func TestWorkbenchBuildSessionsGETIncludes(t *testing.T) {
	wb := newWorkbench()
	openGitProjectForBuilder(t, wb)
	postJSON(t, wb, "/api/workbench/build/start", `{"goal":"ship it"}`)
	postJSON(t, wb, "/api/workbench/build/start", `{"goal":"polish it"}`)

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/build/sessions", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var sessions []BuilderSession
	if err := json.Unmarshal(w.Body.Bytes(), &sessions); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	if sessions[0].Goal != "ship it" {
		t.Errorf("expected oldest goal=ship it, got %q", sessions[0].Goal)
	}
	if sessions[1].Goal != "polish it" {
		t.Errorf("expected newest goal=polish it, got %q", sessions[1].Goal)
	}
}

func TestWorkbenchValidationBlocked(t *testing.T) {
	wb := newWorkbench()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/validation", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var result struct {
		ProjectOpen        bool   `json:"project_open"`
		ProviderConfigured bool   `json:"provider_configured"`
		CurrentSession     bool   `json:"current_session"`
		Status             string `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.ProjectOpen {
		t.Error("expected project_open=false")
	}
	if result.ProviderConfigured {
		t.Error("expected provider_configured=false")
	}
	if result.CurrentSession {
		t.Error("expected current_session=false")
	}
	if result.Status != "blocked" {
		t.Errorf("expected status=blocked, got %q", result.Status)
	}

	if got := len(wb.store.List()); got != 1 {
		t.Errorf("expected validation GET to append no events, store has %d", got)
	}
}

func TestWorkbenchValidationReady(t *testing.T) {
	wb := newWorkbench()
	openGitProjectForBuilder(t, wb)

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/validation", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var result struct {
		ProjectOpen        bool   `json:"project_open"`
		ProviderConfigured bool   `json:"provider_configured"`
		CurrentSession     bool   `json:"current_session"`
		Status             string `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.ProjectOpen {
		t.Error("expected project_open=true")
	}
	if !result.ProviderConfigured {
		t.Error("expected provider_configured=true")
	}
	if result.Status != "ready" {
		t.Errorf("expected status=ready, got %q", result.Status)
	}
}

func TestWorkbenchDiff409WithoutProject(t *testing.T) {
	wb := newWorkbench()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/diff", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestWorkbenchDiff409NonGit(t *testing.T) {
	dir := t.TempDir()
	wb := newWorkbench()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/diff", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchDiffSuccess(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	dir := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".gitkeep"), []byte("modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wb := newWorkbench()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/diff", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected application/json content type, got %q", got)
	}

	var result struct {
		Stat string `json:"stat"`
		Diff string `json:"diff"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Stat == "" {
		t.Error("expected stat to be non-empty")
	}
	if result.Diff == "" {
		t.Error("expected diff to be non-empty")
	}
	if !strings.Contains(result.Diff, "modified") {
		t.Errorf("expected diff to contain modified content, got %q", result.Diff)
	}
}

func TestWorkbenchDiffEventAppended(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	dir := initGitRepo(t)
	wb := newWorkbench()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/diff", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	events := wb.store.List()
	var found *WorkbenchEvent
	for i := range events {
		if events[i].Type == "diff.requested" {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatal("diff.requested event not found")
	}
	if found.Message != "Requested project diff" {
		t.Errorf("unexpected message: %q", found.Message)
	}
}

// openBuilderProvider opens a fresh git project and configures the provider at
// the given base URL with the given key and model.
func openBuilderProvider(t *testing.T, wb *workbench, baseURL, apiKey, model string) string {
	t.Helper()
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	dir := initGitRepo(t)
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	postJSON(t, wb, "/api/workbench/provider",
		`{"base_url":"`+baseURL+`","api_key":"`+apiKey+`","model":"`+model+`"}`)
	return dir
}

func TestWorkbenchBuildStartUpstreamRequest(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	var (
		gotPath   string
		gotMethod string
		gotAuth   string
		gotCT     string
		gotBody   struct {
			Model       string              `json:"model"`
			Messages    []map[string]string `json:"messages"`
			Temperature float64             `json:"temperature"`
		}
	)
	server := startMockProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		planResponse([]string{"only step"})(w, r)
	})

	wb := newWorkbench()
	dir := openBuilderProvider(t, wb, server.URL, "secret-key", "plan-model")

	w := postJSON(t, wb, "/api/workbench/build/start", `{"goal":"add a feature"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	if gotPath != "/chat/completions" {
		t.Errorf("expected path /chat/completions, got %q", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %q", gotMethod)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("expected Authorization=Bearer secret-key, got %q", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("expected content-type application/json, got %q", gotCT)
	}
	if gotBody.Model != "plan-model" {
		t.Errorf("expected model plan-model, got %q", gotBody.Model)
	}
	if gotBody.Temperature != 0 {
		t.Errorf("expected temperature 0, got %v", gotBody.Temperature)
	}
	if len(gotBody.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(gotBody.Messages), gotBody.Messages)
	}
	if gotBody.Messages[0]["role"] != "system" || gotBody.Messages[0]["content"] != builderSystemPrompt {
		t.Errorf("unexpected system message: %+v", gotBody.Messages[0])
	}
	if gotBody.Messages[1]["role"] != "user" {
		t.Errorf("expected second message role=user, got %q", gotBody.Messages[1]["role"])
	}
	userContent := gotBody.Messages[1]["content"]
	for _, want := range []string{
		"Goal: add a feature",
		"Project name: " + filepath.Base(dir),
		"Project path: " + dir,
		"Git: true",
		"Current branch:",
		`{"plan":["step 1","step 2","step 3"]}`,
	} {
		if !strings.Contains(userContent, want) {
			t.Errorf("expected user prompt to contain %q, got:\n%s", want, userContent)
		}
	}
}

func TestWorkbenchBuildStartUsesProviderPlan(t *testing.T) {
	server := startMockProvider(t, planResponse([]string{"first step", "second step"}))
	wb := newWorkbench()
	openBuilderProvider(t, wb, server.URL, "k", "m")

	w := postJSON(t, wb, "/api/workbench/build/start", `{"goal":"do it"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var session BuilderSession
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if session.Status != "planned" {
		t.Errorf("expected status planned, got %q", session.Status)
	}
	want := []string{"first step", "second step"}
	if len(session.Plan) != len(want) {
		t.Fatalf("expected %d plan items, got %d (%v)", len(want), len(session.Plan), session.Plan)
	}
	for i := range want {
		if session.Plan[i] != want[i] {
			t.Errorf("plan[%d]=%q, want %q", i, session.Plan[i], want[i])
		}
	}
}

func TestWorkbenchBuildStartFallsBackOnInvalidContent(t *testing.T) {
	server := startMockProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "here is some prose, not JSON"}},
			},
		})
	})
	wb := newWorkbench()
	openBuilderProvider(t, wb, server.URL, "k", "m")

	w := postJSON(t, wb, "/api/workbench/build/start", `{"goal":"do it"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var session BuilderSession
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if session.Status != "planned" {
		t.Errorf("expected status planned, got %q", session.Status)
	}
	want := fallbackPlan()
	if len(session.Plan) != len(want) {
		t.Fatalf("expected fallback plan length %d, got %d (%v)", len(want), len(session.Plan), session.Plan)
	}
	for i := range want {
		if session.Plan[i] != want[i] {
			t.Errorf("plan[%d]=%q, want fallback %q", i, session.Plan[i], want[i])
		}
	}
}

func TestWorkbenchBuildStartUpstream500(t *testing.T) {
	server := startMockProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"upstream exploded"}`))
	})
	wb := newWorkbench()
	openBuilderProvider(t, wb, server.URL, "k", "m")

	w := postJSON(t, wb, "/api/workbench/build/start", `{"goal":"do it"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Session BuilderSession `json:"session"`
		Error   string         `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Session.Status != "failed" {
		t.Errorf("expected status failed, got %q", result.Session.Status)
	}
	if result.Session.ID == "" {
		t.Error("expected failed session to be assigned an id")
	}
	if result.Error == "" {
		t.Error("expected error message to be populated")
	}
	if !strings.Contains(result.Error, "upstream exploded") {
		t.Errorf("expected error to include upstream body, got %q", result.Error)
	}
	want := fallbackPlan()
	if len(result.Session.Plan) != len(want) {
		t.Errorf("expected fallback plan length %d, got %d", len(want), len(result.Session.Plan))
	}
	if result.Session.PromptPreview == nil {
		t.Error("expected prompt_preview on failed session")
	}

	events := wb.store.List()
	var found bool
	for _, e := range events {
		if e.Type == "builder.plan.failed" {
			found = true
		}
	}
	if !found {
		t.Error("expected builder.plan.failed event")
	}
}

func TestWorkbenchBuildStartNetworkError(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	// Start a server then immediately close it so the address refuses
	// connections, producing a transport-layer error.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	wb := newWorkbench()
	openBuilderProvider(t, wb, deadURL, "k", "m")

	w := postJSON(t, wb, "/api/workbench/build/start", `{"goal":"do it"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Session BuilderSession `json:"session"`
		Error   string         `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Session.Status != "failed" {
		t.Errorf("expected status failed, got %q", result.Session.Status)
	}
	if result.Error == "" {
		t.Error("expected error message for network failure")
	}
	if result.Session.PromptPreview == nil {
		t.Error("expected prompt_preview on failed session")
	}
}

func TestWorkbenchBuildStartIncludesPromptPreview(t *testing.T) {
	server := startMockProvider(t, planResponse([]string{"a step"}))
	wb := newWorkbench()
	dir := openBuilderProvider(t, wb, server.URL, "k", "m")

	w := postJSON(t, wb, "/api/workbench/build/start", `{"goal":"ship the thing"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var session BuilderSession
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if session.PromptPreview == nil {
		t.Fatal("expected prompt_preview to be set")
	}
	if session.PromptPreview.SystemPrompt != builderSystemPrompt {
		t.Errorf("expected system prompt %q, got %q", builderSystemPrompt, session.PromptPreview.SystemPrompt)
	}
	for _, want := range []string{"ship the thing", filepath.Base(dir), dir, "Git: true"} {
		if !strings.Contains(session.PromptPreview.UserPrompt, want) {
			t.Errorf("expected user prompt to contain %q, got:\n%s", want, session.PromptPreview.UserPrompt)
		}
	}
}

func TestWorkbenchBuildPrompt404BeforeSession(t *testing.T) {
	wb := newWorkbench()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/build/prompt", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchBuildPromptAfterSession(t *testing.T) {
	wb := newWorkbench()
	openGitProjectForBuilder(t, wb)
	if w := postJSON(t, wb, "/api/workbench/build/start", `{"goal":"ship it"}`); w.Code != http.StatusCreated {
		t.Fatalf("build/start: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/build/prompt", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected application/json content type, got %q", got)
	}
	var preview PromptPreview
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if preview.SystemPrompt != builderSystemPrompt {
		t.Errorf("expected system prompt %q, got %q", builderSystemPrompt, preview.SystemPrompt)
	}
	if !strings.Contains(preview.UserPrompt, "ship it") {
		t.Errorf("expected user prompt to include goal, got %q", preview.UserPrompt)
	}

	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, has := raw["api_key"]; has {
		t.Error("prompt response must not contain api_key field")
	}
}

func TestWorkbenchBuilderPromptEventNoAPIKey(t *testing.T) {
	server := startMockProvider(t, planResponse([]string{"a step"}))
	wb := newWorkbench()
	openBuilderProvider(t, wb, server.URL, "super-secret-key", "m")

	w := postJSON(t, wb, "/api/workbench/build/start", `{"goal":"ship it"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	events := wb.store.List()
	var found *WorkbenchEvent
	for i := range events {
		if events[i].Type == "builder.prompt.created" {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatal("builder.prompt.created event not found")
	}
	if found.Message != "Created Builder prompt" {
		t.Errorf("unexpected message: %q", found.Message)
	}
	if strings.Contains(string(found.Data), "super-secret-key") {
		t.Error("builder.prompt.created data must not contain the raw api key")
	}

	var data map[string]any
	if err := json.Unmarshal(found.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if _, has := data["api_key"]; has {
		t.Error("builder.prompt.created data must not contain api_key field")
	}
	// The event must carry the sanitized BuilderPrompt fields.
	for _, k := range []string{"goal", "project", "provider_model", "system_prompt", "user_prompt"} {
		if _, ok := data[k]; !ok {
			t.Errorf("expected builder.prompt.created data to contain %q", k)
		}
	}
}

func TestWorkbenchBuildStartResponseNoAPIKey(t *testing.T) {
	// Success path.
	okServer := startMockProvider(t, planResponse([]string{"a step"}))
	wb := newWorkbench()
	openBuilderProvider(t, wb, okServer.URL, "top-secret", "m")

	w := postJSON(t, wb, "/api/workbench/build/start", `{"goal":"ship it"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "top-secret") {
		t.Error("success response leaked api key")
	}
	var rawOK map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rawOK); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, has := rawOK["api_key"]; has {
		t.Error("success response must not contain api_key field")
	}

	// Failure path.
	badServer := startMockProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	wb2 := newWorkbench()
	openBuilderProvider(t, wb2, badServer.URL, "top-secret", "m")

	w2 := postJSON(t, wb2, "/api/workbench/build/start", `{"goal":"ship it"}`)
	if w2.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w2.Code, w2.Body.String())
	}
	if strings.Contains(w2.Body.String(), "top-secret") {
		t.Error("failure response leaked api key")
	}
	var rawFail map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &rawFail); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, has := rawFail["api_key"]; has {
		t.Error("failure response must not contain api_key field")
	}
}

func TestFallbackPlanDeterministic(t *testing.T) {
	want := []string{"Inspect project context", "Prepare Builder prompt", builderPlanPlaceholder}

	a := fallbackPlan()
	b := fallbackPlan()
	if len(a) != len(want) {
		t.Fatalf("expected %d items, got %d", len(want), len(a))
	}
	for i := range want {
		if a[i] != want[i] || b[i] != want[i] {
			t.Errorf("item %d: a=%q b=%q want=%q", i, a[i], b[i], want[i])
		}
	}

	// Each call must return an independent slice.
	a[0] = "mutated"
	c := fallbackPlan()
	if c[0] != want[0] {
		t.Errorf("fallback plan not independent across calls, got %q", c[0])
	}
}

// writeFile writes content to dir/rel, creating parent directories as needed.
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// inspectProjectAt calls GET /api/workbench/project/inspect and decodes the
// resulting inspection, failing the test on a non-200 response.
func inspectProjectAt(t *testing.T, wb *workbench) ProjectInspection {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/project/inspect", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("inspect: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("inspect: expected application/json, got %q", got)
	}
	var insp ProjectInspection
	if err := json.Unmarshal(w.Body.Bytes(), &insp); err != nil {
		t.Fatalf("decode inspection: %v", err)
	}
	return insp
}

func TestWorkbenchProjectInspect409WithoutProject(t *testing.T) {
	wb := newWorkbench()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/project/inspect", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchProjectInspectGitRepo(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	dir := initGitRepo(t)
	writeFile(t, dir, "untracked.go", "package main\n")

	wb := newWorkbench()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	insp := inspectProjectAt(t, wb)

	if insp.Project.Path != dir {
		t.Errorf("expected project.path=%q, got %q", dir, insp.Project.Path)
	}
	if !insp.Project.Git {
		t.Error("expected project.git=true")
	}
	if len(insp.Files) == 0 {
		t.Error("expected non-empty files")
	}
	if !strings.Contains(insp.GitStatus, "untracked.go") {
		t.Errorf("expected git_status to mention untracked.go, got %q", insp.GitStatus)
	}
}

func TestWorkbenchProjectInspectNonGitEmptyStatus(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wb := newWorkbench()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	insp := inspectProjectAt(t, wb)

	if insp.Project.Git {
		t.Error("expected project.git=false for non-git dir")
	}
	if insp.GitStatus != "" {
		t.Errorf("expected empty git_status for non-git dir, got %q", insp.GitStatus)
	}
}

func TestWorkbenchProjectInspectSkipsHeavyDirs(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	dir := initGitRepo(t)
	writeFile(t, dir, "node_modules/pkg/index.js", "x")
	writeFile(t, dir, "vendor/dep/d.go", "x")
	writeFile(t, dir, "dist/out.js", "x")
	writeFile(t, dir, "build/artifact.o", "x")
	writeFile(t, dir, ".venv/lib/site.py", "x")
	writeFile(t, dir, "__pycache__/c.pyc", "x")
	writeFile(t, dir, "main.go", "package main\n")

	wb := newWorkbench()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	insp := inspectProjectAt(t, wb)

	for _, f := range insp.Files {
		for _, bad := range []string{".git", "node_modules", "vendor", "dist", "build", ".venv", "__pycache__"} {
			if f == bad || strings.HasPrefix(f, bad+"/") {
				t.Errorf("expected %q to be skipped, but found %q", bad, f)
			}
		}
	}
	var foundMain bool
	for _, f := range insp.Files {
		if f == "main.go" {
			foundMain = true
		}
	}
	if !foundMain {
		t.Errorf("expected main.go in files, got %v", insp.Files)
	}
}

func TestWorkbenchProjectInspectDetectsLanguages(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	dir := initGitRepo(t)
	writeFile(t, dir, "a.go", "package main\n")
	writeFile(t, dir, "b.py", "print(1)\n")
	writeFile(t, dir, "c.ts", "const x = 1\n")
	writeFile(t, dir, "d.rs", "fn main() {}\n")

	wb := newWorkbench()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	insp := inspectProjectAt(t, wb)

	langs := map[string]bool{}
	for _, l := range insp.Languages {
		langs[l] = true
	}
	for _, want := range []string{"Go", "Python", "TypeScript", "Rust"} {
		if !langs[want] {
			t.Errorf("expected language %q in %v", want, insp.Languages)
		}
	}
}

func TestWorkbenchProjectInspectDetectsConfigFiles(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	dir := initGitRepo(t)
	writeFile(t, dir, "go.mod", "module x\n")
	writeFile(t, dir, "package.json", "{}\n")
	writeFile(t, dir, "Dockerfile", "FROM scratch\n")

	wb := newWorkbench()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	insp := inspectProjectAt(t, wb)

	cfg := map[string]bool{}
	for _, c := range insp.ConfigFiles {
		cfg[c] = true
	}
	for _, want := range []string{"go.mod", "package.json", "Dockerfile"} {
		if !cfg[want] {
			t.Errorf("expected config file %q in %v", want, insp.ConfigFiles)
		}
	}
}

func TestWorkbenchProjectInspectInfersTestHints(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	dir := initGitRepo(t)
	writeFile(t, dir, "go.mod", "module x\n")
	writeFile(t, dir, "Makefile", "test:\n\techo hi\n")
	// Both Python config files present: pytest must still appear exactly once.
	writeFile(t, dir, "pyproject.toml", "")
	writeFile(t, dir, "requirements.txt", "")

	wb := newWorkbench()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	insp := inspectProjectAt(t, wb)

	hints := map[string]int{}
	for _, h := range insp.TestHints {
		hints[h]++
	}
	if hints["go test ./..."] == 0 {
		t.Errorf("expected 'go test ./...' in %v", insp.TestHints)
	}
	if hints["make test"] == 0 {
		t.Errorf("expected 'make test' in %v", insp.TestHints)
	}
	if hints["pytest"] != 1 {
		t.Errorf("expected exactly one 'pytest' hint, got %d in %v", hints["pytest"], insp.TestHints)
	}
}

func TestWorkbenchProjectInspectReadmeCapped(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	dir := initGitRepo(t)
	content := strings.Repeat("a", maxReadmeExcerpt+1000)
	writeFile(t, dir, "README.md", content)

	wb := newWorkbench()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	insp := inspectProjectAt(t, wb)

	if got := len([]rune(insp.ReadmeExcerpt)); got != maxReadmeExcerpt {
		t.Errorf("expected README excerpt capped to %d runes, got %d", maxReadmeExcerpt, got)
	}
	if !strings.HasPrefix(content, insp.ReadmeExcerpt) {
		t.Error("expected excerpt to be a prefix of the README")
	}
}

func TestWorkbenchProjectInspectedEvent(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	dir := initGitRepo(t)
	wb := newWorkbench()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	inspectProjectAt(t, wb)

	events := wb.store.List()
	var found *WorkbenchEvent
	for i := range events {
		if events[i].Type == "project.inspected" {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatal("project.inspected event not found")
	}
	if found.Message != "Inspected project "+filepath.Base(dir) {
		t.Errorf("unexpected message: %q", found.Message)
	}
	if len(found.Data) == 0 {
		t.Fatal("expected event to carry inspection data")
	}
	var data ProjectInspection
	if err := json.Unmarshal(found.Data, &data); err != nil {
		t.Fatalf("decode event data: %v", err)
	}
	if data.Project.Path != dir {
		t.Errorf("expected event data project path %q, got %q", dir, data.Project.Path)
	}
}

func TestWorkbenchProjectInspectionGET404(t *testing.T) {
	wb := newWorkbench()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/project/inspection", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchProjectInspectionGETAfterInspect(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	dir := initGitRepo(t)
	writeFile(t, dir, "main.go", "package main\n")
	wb := newWorkbench()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	inspectProjectAt(t, wb)

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/project/inspection", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var insp ProjectInspection
	if err := json.Unmarshal(w.Body.Bytes(), &insp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if insp.Project.Path != dir {
		t.Errorf("expected project path %q, got %q", dir, insp.Project.Path)
	}
	langs := map[string]bool{}
	for _, l := range insp.Languages {
		langs[l] = true
	}
	if !langs["Go"] {
		t.Errorf("expected Go language, got %v", insp.Languages)
	}
}

func TestWorkbenchBuildStartAutoInspects(t *testing.T) {
	server := startMockProvider(t, planResponse([]string{"a step"}))
	wb := newWorkbench()
	openBuilderProvider(t, wb, server.URL, "k", "m")

	if wb.inspection.Get() != nil {
		t.Fatal("expected no inspection before build/start")
	}

	w := postJSON(t, wb, "/api/workbench/build/start", `{"goal":"ship it"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	if wb.inspection.Get() == nil {
		t.Fatal("expected build/start to auto-create an inspection")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/project/inspection", nil)
	rec := httptest.NewRecorder()
	wb.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected inspection 200 after auto-inspect, got %d", rec.Code)
	}
	var found bool
	for _, e := range wb.store.List() {
		if e.Type == "project.inspected" {
			found = true
		}
	}
	if !found {
		t.Error("expected project.inspected event from auto-inspect")
	}
}

func TestWorkbenchBuildStartPromptIncludesInspection(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	server := startMockProvider(t, planResponse([]string{"a step"}))
	wb := newWorkbench()
	dir := initGitRepo(t)
	writeFile(t, dir, "go.mod", "module x\n")
	writeFile(t, dir, "main.go", "package main\n")
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"k","model":"m"}`)

	w := postJSON(t, wb, "/api/workbench/build/start", `{"goal":"do it"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var session BuilderSession
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if session.PromptPreview == nil {
		t.Fatal("expected prompt_preview")
	}
	up := session.PromptPreview.UserPrompt
	for _, want := range []string{
		"Git status:",
		"Config files:",
		"go.mod",
		"Languages:",
		"Test hints:",
		"go test ./...",
		"Files (",
		"- main.go",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("expected user prompt to contain %q, got:\n%s", want, up)
		}
	}
}

func TestWorkbenchBuildStartPromptFileCap(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	server := startMockProvider(t, planResponse([]string{"a step"}))
	wb := newWorkbench()
	dir := initGitRepo(t)
	for i := 0; i < 120; i++ {
		writeFile(t, dir, fmt.Sprintf("f%03d.txt", i), "x")
	}
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"k","model":"m"}`)

	w := postJSON(t, wb, "/api/workbench/build/start", `{"goal":"do it"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var session BuilderSession
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode: %v", err)
	}
	up := session.PromptPreview.UserPrompt

	var bullets int
	for _, line := range strings.Split(up, "\n") {
		if strings.HasPrefix(line, "- ") {
			bullets++
		}
	}
	if bullets != builderPromptMaxFiles {
		t.Errorf("expected %d file bullet lines, got %d", builderPromptMaxFiles, bullets)
	}
	if !strings.Contains(up, "Files ("+strconv.Itoa(builderPromptMaxFiles)+" shown):") {
		t.Errorf("expected file header to show %d, prompt:\n%s", builderPromptMaxFiles, up)
	}
}

// assertNoAPIKey fails if body contains the provider secret or an api_key field.
func assertNoAPIKey(t *testing.T, label, secret string, body []byte) {
	t.Helper()
	if strings.Contains(string(body), secret) {
		t.Errorf("%s response leaked api key", label)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("%s: decode raw: %v", label, err)
	}
	if _, has := raw["api_key"]; has {
		t.Errorf("%s response contains api_key field", label)
	}
}

func TestWorkbenchInspectionAndBuildNoAPIKey(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	const secret = "inspect-top-secret"
	server := startMockProvider(t, planResponse([]string{"a step"}))
	wb := newWorkbench()
	dir := initGitRepo(t)
	writeFile(t, dir, "main.go", "package main\n")
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"`+secret+`","model":"m"}`)

	inspReq := httptest.NewRequest(http.MethodGet, "/api/workbench/project/inspect", nil)
	inspRec := httptest.NewRecorder()
	wb.mux.ServeHTTP(inspRec, inspReq)
	if inspRec.Code != http.StatusOK {
		t.Fatalf("inspect expected 200, got %d", inspRec.Code)
	}
	assertNoAPIKey(t, "inspect", secret, inspRec.Body.Bytes())

	buildRec := postJSON(t, wb, "/api/workbench/build/start", `{"goal":"do it"}`)
	if buildRec.Code != http.StatusCreated {
		t.Fatalf("build expected 201, got %d: %s", buildRec.Code, buildRec.Body.String())
	}
	assertNoAPIKey(t, "build", secret, buildRec.Body.Bytes())

	getReq := httptest.NewRequest(http.MethodGet, "/api/workbench/project/inspection", nil)
	getRec := httptest.NewRecorder()
	wb.mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("inspection GET expected 200, got %d", getRec.Code)
	}
	assertNoAPIKey(t, "inspection", secret, getRec.Body.Bytes())
}

// proposalResponse returns a handler that replies with an OpenAI-style envelope
// whose message content is a JSON change proposal.
func proposalResponse(summary string, files []BuilderProposedFile) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		content, _ := json.Marshal(map[string]any{"summary": summary, "files": files})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "test",
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": string(content)}},
			},
		})
	}
}

// assertNoSecret fails if body contains the api key value or the literal token
// "api_key". It works for both object and array response bodies.
func assertNoSecret(t *testing.T, label, secret string, body []byte) {
	t.Helper()
	s := string(body)
	if strings.Contains(s, secret) {
		t.Errorf("%s response leaked the api key value", label)
	}
	if strings.Contains(s, "api_key") {
		t.Errorf("%s response contains the api_key token", label)
	}
}

// snapshotDir returns a map of relative path -> content for every file under
// root, excluding the .git directory (whose index git status may touch).
func snapshotDir(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		data, derr := os.ReadFile(path)
		if derr != nil {
			return nil
		}
		snap[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return snap
}

func TestWorkbenchBuildProposeRejectsMissingGoal(t *testing.T) {
	server := startMockProvider(t, proposalResponse("s", nil))
	wb := newWorkbench()
	openBuilderProvider(t, wb, server.URL, "k", "m")

	w := postJSON(t, wb, "/api/workbench/build/propose", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchBuildPropose409WithoutProject(t *testing.T) {
	wb := newWorkbench()
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"https://api.example.com","api_key":"k","model":"m"}`)

	w := postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"ship it"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchBuildPropose409WithoutProvider(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	wb := newWorkbench()
	dir := initGitRepo(t)
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)

	w := postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"ship it"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchBuildProposeAutoInspects(t *testing.T) {
	server := startMockProvider(t, proposalResponse("did stuff", []BuilderProposedFile{
		{Path: "a.go", Action: "create", Content: "package a\n", Rationale: "add"},
	}))
	wb := newWorkbench()
	openBuilderProvider(t, wb, server.URL, "k", "m")

	if wb.inspection.Get() != nil {
		t.Fatal("expected no inspection before propose")
	}

	w := postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"ship it"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	if wb.inspection.Get() == nil {
		t.Fatal("expected propose to auto-create an inspection")
	}
	var found bool
	for _, e := range wb.store.List() {
		if e.Type == "project.inspected" {
			found = true
		}
	}
	if !found {
		t.Error("expected project.inspected event from auto-inspect")
	}
}

func TestWorkbenchBuildProposeUpstreamRequest(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	var (
		gotPath   string
		gotMethod string
		gotAuth   string
		gotBody   struct {
			Model       string              `json:"model"`
			Messages    []map[string]string `json:"messages"`
			Temperature float64             `json:"temperature"`
		}
	)
	server := startMockProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		proposalResponse("s", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "x"}})(w, r)
	})

	wb := newWorkbench()
	openBuilderProvider(t, wb, server.URL, "secret-key", "plan-model")

	w := postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"do it"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	if gotPath != "/chat/completions" {
		t.Errorf("expected path /chat/completions, got %q", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %q", gotMethod)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("expected Authorization=Bearer secret-key, got %q", gotAuth)
	}
	if gotBody.Model != "plan-model" {
		t.Errorf("expected model plan-model, got %q", gotBody.Model)
	}
	if gotBody.Temperature != 0 {
		t.Errorf("expected temperature 0, got %v", gotBody.Temperature)
	}
	if len(gotBody.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(gotBody.Messages), gotBody.Messages)
	}
	if gotBody.Messages[0]["role"] != "system" || gotBody.Messages[0]["content"] != builderProposalSystemPrompt {
		t.Errorf("unexpected system message: %+v", gotBody.Messages[0])
	}
	if gotBody.Messages[1]["role"] != "user" {
		t.Errorf("expected second message role=user, got %q", gotBody.Messages[1]["role"])
	}
	userContent := gotBody.Messages[1]["content"]
	for _, want := range []string{"Goal: do it", `"summary"`, `"files"`, "create|modify|delete"} {
		if !strings.Contains(userContent, want) {
			t.Errorf("expected user prompt to contain %q, got:\n%s", want, userContent)
		}
	}
}

func TestWorkbenchBuildProposeStoresValidProposal(t *testing.T) {
	files := []BuilderProposedFile{
		{Path: "main.go", Action: "modify", Content: "package main\n// changed\n", Rationale: "update"},
		{Path: "old.go", Action: "delete", Content: "", Rationale: "remove"},
		{Path: "new.go", Action: "create", Content: "package new\n", Rationale: "add"},
	}
	server := startMockProvider(t, proposalResponse("apply changes", files))
	wb := newWorkbench()
	openBuilderProvider(t, wb, server.URL, "k", "m")

	w := postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"refactor"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var proposal BuilderChangeProposal
	if err := json.Unmarshal(w.Body.Bytes(), &proposal); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if proposal.ID == "" {
		t.Error("expected id to be set")
	}
	if proposal.TS.IsZero() {
		t.Error("expected ts to be set")
	}
	if proposal.Goal != "refactor" {
		t.Errorf("expected goal=refactor, got %q", proposal.Goal)
	}
	if proposal.Status != "proposed" {
		t.Errorf("expected status=proposed, got %q", proposal.Status)
	}
	if proposal.Summary != "apply changes" {
		t.Errorf("expected summary='apply changes', got %q", proposal.Summary)
	}
	if len(proposal.Files) != len(files) {
		t.Fatalf("expected %d files, got %d", len(files), len(proposal.Files))
	}
	for i := range files {
		if proposal.Files[i] != files[i] {
			t.Errorf("file[%d]=%+v, want %+v", i, proposal.Files[i], files[i])
		}
	}
}

func TestWorkbenchBuildProposalGET404(t *testing.T) {
	wb := newWorkbench()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/build/proposal", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchBuildProposalGETReturnsCurrent(t *testing.T) {
	server := startMockProvider(t, proposalResponse("current proposal", []BuilderProposedFile{
		{Path: "a.go", Action: "create", Content: "package a\n", Rationale: "x"},
	}))
	wb := newWorkbench()
	openBuilderProvider(t, wb, server.URL, "k", "m")
	if w := postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"do it"}`); w.Code != http.StatusCreated {
		t.Fatalf("propose: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/build/proposal", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var proposal BuilderChangeProposal
	if err := json.Unmarshal(w.Body.Bytes(), &proposal); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if proposal.Summary != "current proposal" {
		t.Errorf("expected summary='current proposal', got %q", proposal.Summary)
	}
	if proposal.Status != "proposed" {
		t.Errorf("expected status=proposed, got %q", proposal.Status)
	}
}

func TestWorkbenchBuildProposalsGETEmpty(t *testing.T) {
	wb := newWorkbench()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/build/proposals", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := strings.TrimSpace(w.Body.String()); body != "[]" {
		t.Errorf("expected [] for empty proposals, got %q", body)
	}
}

func TestWorkbenchBuildProposalsGETIncludes(t *testing.T) {
	server := startMockProvider(t, proposalResponse("a proposal", []BuilderProposedFile{
		{Path: "a.go", Action: "create", Content: "package a\n", Rationale: "x"},
	}))
	wb := newWorkbench()
	openBuilderProvider(t, wb, server.URL, "k", "m")
	postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"first"}`)
	postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"second"}`)

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/build/proposals", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var proposals []BuilderChangeProposal
	if err := json.Unmarshal(w.Body.Bytes(), &proposals); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(proposals) != 2 {
		t.Fatalf("expected 2 proposals, got %d", len(proposals))
	}
	if proposals[0].Goal != "first" {
		t.Errorf("expected oldest goal=first, got %q", proposals[0].Goal)
	}
	if proposals[1].Goal != "second" {
		t.Errorf("expected newest goal=second, got %q", proposals[1].Goal)
	}
}

func TestWorkbenchBuildProposeInvalidActionFails(t *testing.T) {
	server := startMockProvider(t, proposalResponse("bad", []BuilderProposedFile{
		{Path: "a.go", Action: "frobnicate", Content: "x", Rationale: "y"},
	}))
	wb := newWorkbench()
	openBuilderProvider(t, wb, server.URL, "k", "m")

	w := postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"do it"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Proposal BuilderChangeProposal `json:"proposal"`
		Error    string                `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Proposal.Status != "failed" {
		t.Errorf("expected status=failed, got %q", result.Proposal.Status)
	}
	if result.Proposal.Summary != builderProposalFallbackSummary {
		t.Errorf("expected fallback summary, got %q", result.Proposal.Summary)
	}
	if len(result.Proposal.Files) != 0 {
		t.Errorf("expected empty files on failed proposal, got %d", len(result.Proposal.Files))
	}
	if !strings.Contains(result.Error, "action") {
		t.Errorf("expected error to mention action, got %q", result.Error)
	}
}

func TestWorkbenchBuildProposeMissingPathFails(t *testing.T) {
	server := startMockProvider(t, proposalResponse("bad", []BuilderProposedFile{
		{Path: "", Action: "create", Content: "x", Rationale: "y"},
	}))
	wb := newWorkbench()
	openBuilderProvider(t, wb, server.URL, "k", "m")

	w := postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"do it"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Proposal BuilderChangeProposal `json:"proposal"`
		Error    string                `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Proposal.Status != "failed" {
		t.Errorf("expected status=failed, got %q", result.Proposal.Status)
	}
	if !strings.Contains(result.Error, "path") {
		t.Errorf("expected error to mention path, got %q", result.Error)
	}
}

func TestWorkbenchBuildPropose500Fails(t *testing.T) {
	server := startMockProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"upstream exploded"}`))
	})
	wb := newWorkbench()
	openBuilderProvider(t, wb, server.URL, "k", "m")

	w := postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"do it"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Proposal BuilderChangeProposal `json:"proposal"`
		Error    string                `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Proposal.Status != "failed" {
		t.Errorf("expected status=failed, got %q", result.Proposal.Status)
	}
	if !strings.Contains(result.Error, "upstream exploded") {
		t.Errorf("expected error to include upstream body, got %q", result.Error)
	}
}

func TestWorkbenchBuildProposalCreatedEventNoAPIKey(t *testing.T) {
	const secret = "propose-secret-key"
	server := startMockProvider(t, proposalResponse("ok", []BuilderProposedFile{
		{Path: "a.go", Action: "create", Content: "package a\n", Rationale: "x"},
	}))
	wb := newWorkbench()
	openBuilderProvider(t, wb, server.URL, secret, "m")

	w := postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"do it"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	events := wb.store.List()
	var found *WorkbenchEvent
	for i := range events {
		if events[i].Type == "builder.proposal.created" {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatal("builder.proposal.created event not found")
	}
	if found.Message != "Created Builder change proposal" {
		t.Errorf("unexpected message: %q", found.Message)
	}
	assertNoSecret(t, "proposal.created event", secret, found.Data)
}

func TestWorkbenchBuildProposalFailedEventNoAPIKey(t *testing.T) {
	const secret = "propose-secret-key"
	server := startMockProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	wb := newWorkbench()
	openBuilderProvider(t, wb, server.URL, secret, "m")

	w := postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"do it"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	events := wb.store.List()
	var found *WorkbenchEvent
	for i := range events {
		if events[i].Type == "builder.proposal.failed" {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatal("builder.proposal.failed event not found")
	}
	if found.Message != "Builder change proposal failed" {
		t.Errorf("unexpected message: %q", found.Message)
	}
	assertNoSecret(t, "proposal.failed event", secret, found.Data)
}

func TestWorkbenchBuildProposeNoResponseHasAPIKey(t *testing.T) {
	const secret = "propose-top-secret"
	server := startMockProvider(t, proposalResponse("ok", []BuilderProposedFile{
		{Path: "a.go", Action: "create", Content: "package a\n", Rationale: "x"},
	}))
	wb := newWorkbench()
	openBuilderProvider(t, wb, server.URL, secret, "m")

	proposeRec := postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"do it"}`)
	if proposeRec.Code != http.StatusCreated {
		t.Fatalf("propose expected 201, got %d", proposeRec.Code)
	}
	assertNoSecret(t, "propose", secret, proposeRec.Body.Bytes())

	for _, path := range []string{"/api/workbench/build/proposal", "/api/workbench/build/proposals"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		wb.mux.ServeHTTP(rec, req)
		assertNoSecret(t, path, secret, rec.Body.Bytes())
	}

	// Failed proposal response must also be clean.
	bad := startMockProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	wb2 := newWorkbench()
	openBuilderProvider(t, wb2, bad.URL, secret, "m")
	failRec := postJSON(t, wb2, "/api/workbench/build/propose", `{"goal":"do it"}`)
	if failRec.Code != http.StatusBadGateway {
		t.Fatalf("failed propose expected 502, got %d", failRec.Code)
	}
	assertNoSecret(t, "failed propose", secret, failRec.Body.Bytes())
}

func TestWorkbenchBuildProposeDoesNotModifyFiles(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	wb := newWorkbench()
	dir := initGitRepo(t)
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, dir, "keep.txt", "keep me\n")

	before := snapshotDir(t, dir)

	// The provider proposes create/modify/delete; none must touch disk.
	files := []BuilderProposedFile{
		{Path: "newfile.go", Action: "create", Content: "package newpkg\n", Rationale: "add"},
		{Path: "main.go", Action: "modify", Content: "package main\n// rewritten\n", Rationale: "edit"},
		{Path: "keep.txt", Action: "delete", Content: "", Rationale: "remove"},
	}
	server := startMockProvider(t, proposalResponse("touch everything", files))
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"k","model":"m"}`)

	w := postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"do it"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	after := snapshotDir(t, dir)
	if len(before) != len(after) {
		t.Fatalf("file count changed: before %d, after %d", len(before), len(after))
	}
	for path, content := range before {
		if after[path] != content {
			t.Errorf("file %q content changed", path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("unexpected new file %q created", path)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "newfile.go")); !os.IsNotExist(err) {
		t.Errorf("proposed file newfile.go must not exist on disk (err=%v)", err)
	}
}
