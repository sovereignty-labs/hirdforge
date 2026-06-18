package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
	wb := &workbench{store: newEventStore(), project: newProjectState(), provider: newProviderState(), sessions: newSessionStore()}
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

func openGitProjectForBuilder(t *testing.T, wb *workbench) string {
	t.Helper()
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	dir := initGitRepo(t)
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"https://api.example.com","api_key":"k","model":"m"}`)
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
