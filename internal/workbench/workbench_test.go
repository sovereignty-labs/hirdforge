package workbench

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
	mux := New().mux
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
	mux := New().mux
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("expected text/html content type, got %q", got)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Hirdforge Workbench") {
		t.Errorf("expected body to contain title, got: %q", body)
	}
	// The page is split into embedded assets; it must reference both.
	if !strings.Contains(body, "app.js") {
		t.Errorf("expected index to reference app.js")
	}
	if !strings.Contains(body, "styles.css") {
		t.Errorf("expected index to reference styles.css")
	}
}

// TestWorkbenchUIAssets verifies the split CSS/JS assets are served with a
// usable content type and non-empty body (not 404).
func TestWorkbenchUIAssets(t *testing.T) {
	mux := New().mux
	cases := []struct{ path, ctype string }{
		{"/styles.css", "text/css"},
		{"/app.js", "javascript"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("%s: expected 200, got %d", c.path, w.Code)
			continue
		}
		if got := w.Header().Get("Content-Type"); !strings.Contains(got, c.ctype) {
			t.Errorf("%s: expected content type containing %q, got %q", c.path, c.ctype, got)
		}
		if w.Body.Len() == 0 {
			t.Errorf("%s: expected non-empty body", c.path)
		}
	}
}

// TestWorkbenchUIResumeMarkers checks the served app.js carries the reload
// resume/polling logic, by stable identifier rather than brittle full content.
func TestWorkbenchUIResumeMarkers(t *testing.T) {
	mux := New().mux
	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/app.js: expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	for _, marker := range []string{
		"hf.selectedLaneId.v1",      // persisted selected lane id
		"hf.selectedConsoleKind.v1", // persisted console kind
		"restoreSelection",          // resume-after-reload logic
		"EVENTS_POLL_MS",            // event polling
		"visibilitychange",          // polling pauses when the tab is hidden
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("expected app.js to contain %q", marker)
		}
	}
}

// TestWorkbenchUISetupMarkers checks the served UI carries the setup-hardening
// logic: the operator progress strip, API key input clearing, and the provider
// configured/test states. Markers are stable identifiers, not full content.
func TestWorkbenchUISetupMarkers(t *testing.T) {
	mux := New().mux
	getBody := func(path string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, w.Code)
		}
		return w.Body.String()
	}

	js := getBody("/app.js")
	for _, marker := range []string{
		"renderProgress",   // operator progress strip logic
		"renderNextHint",   // next-step guidance
		"clearApiKeyInput", // API key input cleared after save
		"untested",         // configured-but-untested provider state
	} {
		if !strings.Contains(js, marker) {
			t.Errorf("expected app.js to contain %q", marker)
		}
	}
	if html := getBody("/"); !strings.Contains(html, "progress-strip") {
		t.Error("expected index to contain the progress strip")
	}
}

// TestWorkbenchUIArchitectMarkers checks the served app.js carries the Architect
// flow UX logic by stable identifier: readiness gating, spec rendering with
// empty sections, accepted-state rendering, and the create-task guard.
func TestWorkbenchUIArchitectMarkers(t *testing.T) {
	mux := New().mux
	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/app.js: expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	for _, marker := range []string{
		"architectReadiness",      // start/send/accept gating
		"updateArchitectControls", // disabled-state logic
		"canCreateTask",           // create-task guard
		"spec-none",               // empty spec sections render "none"
		"spec-accepted",           // accepted-state rendering
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("expected app.js to contain %q", marker)
		}
	}
}

// TestWorkbenchUILaneBoardMarkers checks the served app.js carries the lane
// board UX logic by stable identifier: task summary, default lane selection,
// lane conversation lookup/reuse, and the lane inspector enhancement.
func TestWorkbenchUILaneBoardMarkers(t *testing.T) {
	mux := New().mux
	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/app.js: expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	for _, marker := range []string{
		"renderTaskSummary",          // Cortex task summary
		"selectDefaultLane",          // default lane selection
		"conversationForLane",        // lane conversation lookup
		"currentContextConversation", // conversation reuse for selected lane/kind
		"related task",               // lane inspector enhancement
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("expected app.js to contain %q", marker)
		}
	}
}

// TestWorkbenchUIConsoleMarkers checks the served app.js carries the Lane
// Console UX logic by stable identifier: scope/header rendering, kind-chip lane
// availability, conversation control readiness, and open/reuse behavior.
func TestWorkbenchUIConsoleMarkers(t *testing.T) {
	mux := New().mux
	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/app.js: expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	for _, marker := range []string{
		"renderDrawerScope",  // Lane Console scope/header
		"kindAvailable",      // kind-chip lane availability
		"consoleReadiness",   // conversation control readiness/disabled logic
		"Reuse Conversation", // open/reuse conversation behavior
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("expected app.js to contain %q", marker)
		}
	}
}

// TestWorkbenchUIProposalMarkers checks the served app.js carries the builder
// proposal UX logic by stable identifier: proposal state, generation/readiness
// gating, proposal display, and the duplicate-proposal guard.
func TestWorkbenchUIProposalMarkers(t *testing.T) {
	mux := New().mux
	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/app.js: expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	for _, marker := range []string{
		"proposalForLane",      // proposal state / lookup
		"proposalReadiness",    // generation readiness gating + duplicate guard
		"renderProposal",       // proposal display
		"generateLaneProposal", // generate action calling the backend
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("expected app.js to contain %q", marker)
		}
	}
}

func TestWorkbenchRejectsNonGET(t *testing.T) {
	mux := New().mux
	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", w.Code)
	}
}

func TestWorkbenchUnknownPath(t *testing.T) {
	mux := New().mux
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
	wb := New()
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
	wb := New()
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
	wb := &Server{store: newEventStore(), project: newProjectState(), provider: newProviderState(), sessions: newSessionStore(), inspection: newInspectionState(), proposals: newProposalStore(), lockbox: newLockboxStore(), cortex: newCortexStore(), cortexLaneProposals: newCortexLaneProposalStore(), cortexAggregates: newCortexAggregateStore(), cortexReviews: newCortexReviewStore(), cortexApplies: newCortexApplyStore(), cortexApplyPreviews: newCortexApplyPreviewStore(), cortexValidations: newCortexValidationStore(), architect: newArchitectSessionStore(), laneConversations: newLaneConversationStore()}
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
	wb := New()
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
	wb := New()
	req := httptest.NewRequest(http.MethodPost, "/api/workbench/events", strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestWorkbenchEventsPOSTMissingType(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodPost, "/api/workbench/events", strings.NewReader(`{"message":"no type"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestWorkbenchEventsMethodNotAllowed(t *testing.T) {
	wb := New()
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

func postJSON(t *testing.T, wb *Server, path, body string) *httptest.ResponseRecorder {
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

	wb := New()
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
	wb := New()
	w := postJSON(t, wb, "/api/workbench/project/open", `{"path":"relative/path"}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "absolute") {
		t.Errorf("expected an 'absolute' hint in the error, got: %q", w.Body.String())
	}
}

func TestWorkbenchProjectOpenMissing(t *testing.T) {
	wb := New()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	w := postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+missing+`"}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "does not exist") {
		t.Errorf("expected 'does not exist' in the error, got: %q", w.Body.String())
	}
}

func TestWorkbenchProjectOpenFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	wb := New()
	w := postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+file+`"}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "not a directory") {
		t.Errorf("expected 'not a directory' in the error, got: %q", w.Body.String())
	}
}

func TestWorkbenchProjectOpenMissingPathField(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/project/open", `{}`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchProjectOpenMethodNotAllowed(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/project/open", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestWorkbenchProjectGETNotOpen(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/project", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestWorkbenchProjectGETAfterOpen(t *testing.T) {
	dir := t.TempDir()
	wb := New()

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
	wb := New()

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
	wb := New()
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
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/provider", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestWorkbenchProviderGETConfigured(t *testing.T) {
	wb := New()
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
	wb := New()
	w := postJSON(t, wb, "/api/workbench/provider", `{"api_key":"key","model":"m"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchProviderRejectsMissingAPIKey(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/provider", `{"base_url":"https://api.example.com","model":"m"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchProviderRejectsMissingModel(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/provider", `{"base_url":"https://api.example.com","api_key":"key"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchProviderRejectsInvalidBaseURL(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/provider", `{"base_url":"ftp://example.com","api_key":"key","model":"m"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchProviderConfiguredEvent(t *testing.T) {
	wb := New()
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
	wb := New()
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

	wb := New()
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

	wb := New()
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

func openGitProjectForBuilder(t *testing.T, wb *Server) string {
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
	wb := New()
	openGitProjectForBuilder(t, wb)

	w := postJSON(t, wb, "/api/workbench/build/start", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchBuildStart409WithoutProject(t *testing.T) {
	wb := New()
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
	wb := New()
	dir := initGitRepo(t)
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)

	w := postJSON(t, wb, "/api/workbench/build/start", `{"goal":"ship it"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchBuildStartSuccess(t *testing.T) {
	wb := New()
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
	wb := New()
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
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/build/session", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestWorkbenchBuildSessionGETAfterStart(t *testing.T) {
	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/diff", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestWorkbenchDiff409NonGit(t *testing.T) {
	dir := t.TempDir()
	wb := New()
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

	wb := New()
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
	wb := New()
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
func openBuilderProvider(t *testing.T, wb *Server, baseURL, apiKey, model string) string {
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

	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
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

	wb := New()
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
	wb := New()
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
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/build/prompt", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchBuildPromptAfterSession(t *testing.T) {
	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb2 := New()
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
func inspectProjectAt(t *testing.T, wb *Server) ProjectInspection {
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
	wb := New()
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

	wb := New()
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

	wb := New()
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

	wb := New()
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

	wb := New()
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

	wb := New()
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

	wb := New()
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

	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
	openBuilderProvider(t, wb, server.URL, "k", "m")

	w := postJSON(t, wb, "/api/workbench/build/propose", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchBuildPropose409WithoutProject(t *testing.T) {
	wb := New()
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
	wb := New()
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
	wb := New()
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

	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb := New()
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
	wb2 := New()
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
	wb := New()
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

// workbenchWithProposal sets up a workbench with an open git project, a mock
// provider, and one stored "proposed" change proposal built from files. It
// returns the workbench and the decoded proposal.
func workbenchWithProposal(t *testing.T, files []BuilderProposedFile) (*Server, BuilderChangeProposal) {
	t.Helper()
	server := startMockProvider(t, proposalResponse("proposed summary", files))
	wb := New()
	openBuilderProvider(t, wb, server.URL, "k", "m")
	w := postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"do it"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("propose: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var proposal BuilderChangeProposal
	if err := json.Unmarshal(w.Body.Bytes(), &proposal); err != nil {
		t.Fatalf("decode proposal: %v", err)
	}
	return wb, proposal
}

// decodeLockboxRequest decodes a recorder body into a LockboxApprovalRequest.
func decodeLockboxRequest(t *testing.T, w *httptest.ResponseRecorder) LockboxApprovalRequest {
	t.Helper()
	var req LockboxApprovalRequest
	if err := json.Unmarshal(w.Body.Bytes(), &req); err != nil {
		t.Fatalf("decode lockbox request: %v", err)
	}
	return req
}

func TestWorkbenchLockboxRequest409NoProposal(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/lockbox/request", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchLockboxRequestFromCurrent(t *testing.T) {
	files := []BuilderProposedFile{
		{Path: "a.go", Action: "create", Content: "package a\n", Rationale: "x"},
		{Path: "b.go", Action: "delete", Content: "", Rationale: "y"},
	}
	wb, proposal := workbenchWithProposal(t, files)

	w := postJSON(t, wb, "/api/workbench/lockbox/request", `{}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	req := decodeLockboxRequest(t, w)
	if req.ID == "" {
		t.Error("expected id to be set")
	}
	if req.TS.IsZero() {
		t.Error("expected ts to be set")
	}
	if req.ProposalID != proposal.ID {
		t.Errorf("expected proposal_id=%q, got %q", proposal.ID, req.ProposalID)
	}
	if req.Goal != proposal.Goal {
		t.Errorf("expected goal=%q, got %q", proposal.Goal, req.Goal)
	}
	if req.Status != "pending" {
		t.Errorf("expected status=pending, got %q", req.Status)
	}
	if req.Summary != proposal.Summary {
		t.Errorf("expected summary=%q, got %q", proposal.Summary, req.Summary)
	}
	if len(req.Files) != len(files) {
		t.Fatalf("expected %d files, got %d", len(files), len(req.Files))
	}
	for i := range files {
		if req.Files[i] != files[i] {
			t.Errorf("file[%d]=%+v, want %+v", i, req.Files[i], files[i])
		}
	}
	if req.DecisionTS != nil {
		t.Errorf("expected nil decision_ts for pending, got %v", req.DecisionTS)
	}
	if req.DecisionReason != "" {
		t.Errorf("expected empty decision_reason, got %q", req.DecisionReason)
	}
}

func TestWorkbenchLockboxRequestFromExplicitID(t *testing.T) {
	files := []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "package a\n", Rationale: "x"}}
	wb, proposal := workbenchWithProposal(t, files)

	w := postJSON(t, wb, "/api/workbench/lockbox/request", `{"proposal_id":"`+proposal.ID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	req := decodeLockboxRequest(t, w)
	if req.ProposalID != proposal.ID {
		t.Errorf("expected proposal_id=%q, got %q", proposal.ID, req.ProposalID)
	}
	if req.Status != "pending" {
		t.Errorf("expected status=pending, got %q", req.Status)
	}
}

func TestWorkbenchLockboxRequest404MissingProposal(t *testing.T) {
	files := []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "package a\n", Rationale: "x"}}
	wb, _ := workbenchWithProposal(t, files)

	w := postJSON(t, wb, "/api/workbench/lockbox/request", `{"proposal_id":"nonexistent"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchLockboxRequest409FailedProposal(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	server := startMockProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	wb := New()
	openBuilderProvider(t, wb, server.URL, "k", "m")
	fr := postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"do it"}`)
	if fr.Code != http.StatusBadGateway {
		t.Fatalf("propose: expected 502, got %d: %s", fr.Code, fr.Body.String())
	}

	w := postJSON(t, wb, "/api/workbench/lockbox/request", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for failed proposal, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchLockboxRequestGET404(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/lockbox/request", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchLockboxRequestGETCurrent(t *testing.T) {
	files := []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "package a\n", Rationale: "x"}}
	wb, _ := workbenchWithProposal(t, files)
	created := decodeLockboxRequest(t, postJSON(t, wb, "/api/workbench/lockbox/request", `{}`))

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/lockbox/request", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got := decodeLockboxRequest(t, w)
	if got.ID != created.ID {
		t.Errorf("expected current request id=%q, got %q", created.ID, got.ID)
	}
	if got.Status != "pending" {
		t.Errorf("expected status=pending, got %q", got.Status)
	}
}

func TestWorkbenchLockboxRequestsEmpty(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/lockbox/requests", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := strings.TrimSpace(w.Body.String()); body != "[]" {
		t.Errorf("expected [] for empty requests, got %q", body)
	}
}

func TestWorkbenchLockboxRequestsNewestLast(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	files := []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "package a\n", Rationale: "x"}}
	server := startMockProvider(t, proposalResponse("s", files))
	wb := New()
	openBuilderProvider(t, wb, server.URL, "k", "m")

	postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"first"}`)
	postJSON(t, wb, "/api/workbench/lockbox/request", `{}`)
	postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"second"}`)
	postJSON(t, wb, "/api/workbench/lockbox/request", `{}`)

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/lockbox/requests", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var reqs []LockboxApprovalRequest
	if err := json.Unmarshal(w.Body.Bytes(), &reqs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(reqs))
	}
	if reqs[0].Goal != "first" {
		t.Errorf("expected oldest goal=first, got %q", reqs[0].Goal)
	}
	if reqs[1].Goal != "second" {
		t.Errorf("expected newest goal=second, got %q", reqs[1].Goal)
	}
}

func TestWorkbenchLockboxApproveMissingID(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/lockbox/approve", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchLockboxApprove404(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/lockbox/approve", `{"id":"nope"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchLockboxApprove409AlreadyDecided(t *testing.T) {
	files := []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "package a\n", Rationale: "x"}}
	wb, _ := workbenchWithProposal(t, files)
	req := decodeLockboxRequest(t, postJSON(t, wb, "/api/workbench/lockbox/request", `{}`))

	first := postJSON(t, wb, "/api/workbench/lockbox/approve", `{"id":"`+req.ID+`"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first approve: expected 200, got %d: %s", first.Code, first.Body.String())
	}
	second := postJSON(t, wb, "/api/workbench/lockbox/approve", `{"id":"`+req.ID+`"}`)
	if second.Code != http.StatusConflict {
		t.Fatalf("second approve: expected 409, got %d: %s", second.Code, second.Body.String())
	}
}

func TestWorkbenchLockboxApproveUpdates(t *testing.T) {
	files := []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "package a\n", Rationale: "x"}}
	wb, _ := workbenchWithProposal(t, files)
	req := decodeLockboxRequest(t, postJSON(t, wb, "/api/workbench/lockbox/request", `{}`))

	w := postJSON(t, wb, "/api/workbench/lockbox/approve", `{"id":"`+req.ID+`","reason":"looks good"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got := decodeLockboxRequest(t, w)
	if got.Status != "approved" {
		t.Errorf("expected status=approved, got %q", got.Status)
	}
	if got.DecisionTS == nil || got.DecisionTS.IsZero() {
		t.Error("expected decision_ts to be set")
	}
	if got.DecisionReason != "looks good" {
		t.Errorf("expected decision_reason='looks good', got %q", got.DecisionReason)
	}
}

func TestWorkbenchLockboxRejectMissingID(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/lockbox/reject", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchLockboxReject404(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/lockbox/reject", `{"id":"nope"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchLockboxReject409AlreadyDecided(t *testing.T) {
	files := []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "package a\n", Rationale: "x"}}
	wb, _ := workbenchWithProposal(t, files)
	req := decodeLockboxRequest(t, postJSON(t, wb, "/api/workbench/lockbox/request", `{}`))

	first := postJSON(t, wb, "/api/workbench/lockbox/reject", `{"id":"`+req.ID+`"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first reject: expected 200, got %d: %s", first.Code, first.Body.String())
	}
	second := postJSON(t, wb, "/api/workbench/lockbox/reject", `{"id":"`+req.ID+`"}`)
	if second.Code != http.StatusConflict {
		t.Fatalf("second reject: expected 409, got %d: %s", second.Code, second.Body.String())
	}
}

func TestWorkbenchLockboxRejectUpdates(t *testing.T) {
	files := []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "package a\n", Rationale: "x"}}
	wb, _ := workbenchWithProposal(t, files)
	req := decodeLockboxRequest(t, postJSON(t, wb, "/api/workbench/lockbox/request", `{}`))

	w := postJSON(t, wb, "/api/workbench/lockbox/reject", `{"id":"`+req.ID+`","reason":"not safe"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got := decodeLockboxRequest(t, w)
	if got.Status != "rejected" {
		t.Errorf("expected status=rejected, got %q", got.Status)
	}
	if got.DecisionTS == nil || got.DecisionTS.IsZero() {
		t.Error("expected decision_ts to be set")
	}
	if got.DecisionReason != "not safe" {
		t.Errorf("expected decision_reason='not safe', got %q", got.DecisionReason)
	}
}

func TestWorkbenchLockboxEventsAppended(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	files := []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "package a\n", Rationale: "x"}}
	server := startMockProvider(t, proposalResponse("s", files))
	wb := New()
	openBuilderProvider(t, wb, server.URL, "k", "m")

	postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"first"}`)
	r1 := decodeLockboxRequest(t, postJSON(t, wb, "/api/workbench/lockbox/request", `{}`))
	postJSON(t, wb, "/api/workbench/lockbox/approve", `{"id":"`+r1.ID+`"}`)

	postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"second"}`)
	r2 := decodeLockboxRequest(t, postJSON(t, wb, "/api/workbench/lockbox/request", `{}`))
	postJSON(t, wb, "/api/workbench/lockbox/reject", `{"id":"`+r2.ID+`"}`)

	msgs := map[string]string{}
	for _, e := range wb.store.List() {
		if strings.HasPrefix(e.Type, "lockbox.") {
			msgs[e.Type] = e.Message
		}
	}
	want := map[string]string{
		"lockbox.request.created":  "Created Lockbox approval request",
		"lockbox.request.approved": "Approved Lockbox request",
		"lockbox.request.rejected": "Rejected Lockbox request",
	}
	for typ, wantMsg := range want {
		got, ok := msgs[typ]
		if !ok {
			t.Errorf("expected event %q to be appended", typ)
			continue
		}
		if got != wantMsg {
			t.Errorf("event %q: expected message %q, got %q", typ, wantMsg, got)
		}
	}
}

func TestWorkbenchLockboxNoAPIKey(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	const secret = "lockbox-top-secret"
	files := []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "package a\n", Rationale: "x"}}
	server := startMockProvider(t, proposalResponse("s", files))
	wb := New()
	openBuilderProvider(t, wb, server.URL, secret, "m")

	postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"first"}`)
	created := postJSON(t, wb, "/api/workbench/lockbox/request", `{}`)
	assertNoSecret(t, "request create", secret, created.Body.Bytes())
	r1 := decodeLockboxRequest(t, created)

	for _, path := range []string{"/api/workbench/lockbox/request", "/api/workbench/lockbox/requests"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		wb.mux.ServeHTTP(rec, req)
		assertNoSecret(t, path, secret, rec.Body.Bytes())
	}

	approved := postJSON(t, wb, "/api/workbench/lockbox/approve", `{"id":"`+r1.ID+`","reason":"ok"}`)
	assertNoSecret(t, "approve", secret, approved.Body.Bytes())

	postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"second"}`)
	r2 := decodeLockboxRequest(t, postJSON(t, wb, "/api/workbench/lockbox/request", `{}`))
	rejected := postJSON(t, wb, "/api/workbench/lockbox/reject", `{"id":"`+r2.ID+`","reason":"no"}`)
	assertNoSecret(t, "reject", secret, rejected.Body.Bytes())

	// Lockbox event payloads must be clean too. (Other event types such as
	// provider.configured legitimately carry an "api_key_set" flag, so only the
	// lockbox events are checked here.)
	for _, e := range wb.store.List() {
		if strings.HasPrefix(e.Type, "lockbox.") {
			assertNoSecret(t, "event "+e.Type, secret, e.Data)
		}
	}
}

func TestWorkbenchLockboxApproveDoesNotModifyFiles(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	wb := New()
	dir := initGitRepo(t)
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, dir, "keep.txt", "keep me\n")

	before := snapshotDir(t, dir)

	files := []BuilderProposedFile{
		{Path: "newfile.go", Action: "create", Content: "package newpkg\n", Rationale: "add"},
		{Path: "main.go", Action: "modify", Content: "package main\n// rewritten\n", Rationale: "edit"},
		{Path: "keep.txt", Action: "delete", Content: "", Rationale: "remove"},
	}
	server := startMockProvider(t, proposalResponse("touch everything", files))
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"k","model":"m"}`)

	if pr := postJSON(t, wb, "/api/workbench/build/propose", `{"goal":"do it"}`); pr.Code != http.StatusCreated {
		t.Fatalf("propose: expected 201, got %d: %s", pr.Code, pr.Body.String())
	}
	cr := postJSON(t, wb, "/api/workbench/lockbox/request", `{}`)
	if cr.Code != http.StatusCreated {
		t.Fatalf("request: expected 201, got %d: %s", cr.Code, cr.Body.String())
	}
	req := decodeLockboxRequest(t, cr)

	ar := postJSON(t, wb, "/api/workbench/lockbox/approve", `{"id":"`+req.ID+`","reason":"go"}`)
	if ar.Code != http.StatusOK {
		t.Fatalf("approve: expected 200, got %d: %s", ar.Code, ar.Body.String())
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

// createCortexTask posts a cortex task and decodes the 201 response.
func createCortexTask(t *testing.T, wb *Server, body string) CortexTask {
	t.Helper()
	w := postJSON(t, wb, "/api/workbench/cortex/task", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("cortex task create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var task CortexTask
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Fatalf("decode cortex task: %v", err)
	}
	return task
}

func cortexRoleCounts(lanes []CortexLane) map[string]int {
	m := map[string]int{}
	for _, l := range lanes {
		m[l.Role]++
	}
	return m
}

func TestWorkbenchCortexRejectsMissingGoal(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/cortex/task", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexRejectsUnknownMode(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/cortex/task", `{"goal":"g","mode":"swarm"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexRejectsNegativeCount(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/cortex/task", `{"goal":"g","roles":{"builder":-1}}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexRejectsZeroLanes(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/cortex/task", `{"goal":"g","roles":{"architect":0,"builder":0,"reviewer":0,"validator":0}}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexRejectsTooManyLanes(t *testing.T) {
	wb := New()
	// 4 + 4 + 3 + 2 = 13 > 12
	w := postJSON(t, wb, "/api/workbench/cortex/task", `{"goal":"g","roles":{"architect":4,"builder":4,"reviewer":3,"validator":2}}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexSingleDefault(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"ship it"}`)
	if task.ID == "" {
		t.Error("expected id to be set")
	}
	if task.TS.IsZero() {
		t.Error("expected ts to be set")
	}
	if task.Goal != "ship it" {
		t.Errorf("expected goal=ship it, got %q", task.Goal)
	}
	if task.Status != "planned" {
		t.Errorf("expected status=planned, got %q", task.Status)
	}
	if len(task.Lanes) != 1 {
		t.Fatalf("expected 1 lane, got %d", len(task.Lanes))
	}
	lane := task.Lanes[0]
	if lane.Role != "builder" {
		t.Errorf("expected role=builder, got %q", lane.Role)
	}
	if lane.Index != 1 {
		t.Errorf("expected index=1, got %d", lane.Index)
	}
	if lane.Status != "pending" {
		t.Errorf("expected status=pending, got %q", lane.Status)
	}
	if lane.Task != "Builder lane 1: ship it" {
		t.Errorf("unexpected lane task: %q", lane.Task)
	}
}

func TestWorkbenchCortexMultiDefault(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	if len(task.Lanes) != 5 {
		t.Fatalf("expected 5 lanes, got %d", len(task.Lanes))
	}
	counts := cortexRoleCounts(task.Lanes)
	if counts["architect"] != 1 || counts["builder"] != 2 || counts["reviewer"] != 1 || counts["validator"] != 1 {
		t.Errorf("unexpected role counts: %v", counts)
	}
}

func TestWorkbenchCortexExplicitRoles(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","roles":{"builder":3,"reviewer":2}}`)
	if len(task.Lanes) != 5 {
		t.Fatalf("expected 5 lanes, got %d", len(task.Lanes))
	}
	counts := cortexRoleCounts(task.Lanes)
	if counts["builder"] != 3 {
		t.Errorf("expected 3 builders, got %d", counts["builder"])
	}
	if counts["reviewer"] != 2 {
		t.Errorf("expected 2 reviewers, got %d", counts["reviewer"])
	}
	if counts["architect"] != 0 || counts["validator"] != 0 {
		t.Errorf("expected no architect/validator lanes, got %v", counts)
	}
}

func TestWorkbenchCortexLaneOrdering(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	wantRoles := []string{"architect", "builder", "builder", "reviewer", "validator"}
	if len(task.Lanes) != len(wantRoles) {
		t.Fatalf("expected %d lanes, got %d", len(wantRoles), len(task.Lanes))
	}
	for i, want := range wantRoles {
		if task.Lanes[i].Role != want {
			t.Errorf("lane[%d] role=%q, want %q", i, task.Lanes[i].Role, want)
		}
		if task.Lanes[i].ID != strconv.Itoa(i+1) {
			t.Errorf("lane[%d] id=%q, want %q", i, task.Lanes[i].ID, strconv.Itoa(i+1))
		}
	}
}

func TestWorkbenchCortexLaneIndexes(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","roles":{"builder":3,"reviewer":2}}`)
	var builders, reviewers []int
	for _, l := range task.Lanes {
		switch l.Role {
		case "builder":
			builders = append(builders, l.Index)
		case "reviewer":
			reviewers = append(reviewers, l.Index)
		}
	}
	if len(builders) != 3 || builders[0] != 1 || builders[1] != 2 || builders[2] != 3 {
		t.Errorf("expected builder indexes [1 2 3], got %v", builders)
	}
	if len(reviewers) != 2 || reviewers[0] != 1 || reviewers[1] != 2 {
		t.Errorf("expected reviewer indexes [1 2], got %v", reviewers)
	}
}

func TestWorkbenchCortexLaneWorktreeFieldsEmpty(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	for _, l := range task.Lanes {
		if l.WorkspacePath != "" {
			t.Errorf("lane %s workspace_path should be empty, got %q", l.ID, l.WorkspacePath)
		}
		if l.BaseBranch != "" {
			t.Errorf("lane %s base_branch should be empty, got %q", l.ID, l.BaseBranch)
		}
		if l.WorktreeBranch != "" {
			t.Errorf("lane %s worktree_branch should be empty, got %q", l.ID, l.WorktreeBranch)
		}
	}
}

func TestWorkbenchCortexTaskGET404(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/task", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexTaskGETCurrent(t *testing.T) {
	wb := New()
	created := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/task", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var task CortexTask
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if task.ID != created.ID {
		t.Errorf("expected current task id=%q, got %q", created.ID, task.ID)
	}
}

func TestWorkbenchCortexTasksEmpty(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/tasks", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := strings.TrimSpace(w.Body.String()); body != "[]" {
		t.Errorf("expected [] for empty tasks, got %q", body)
	}
}

func TestWorkbenchCortexTasksNewestLast(t *testing.T) {
	wb := New()
	createCortexTask(t, wb, `{"goal":"first"}`)
	createCortexTask(t, wb, `{"goal":"second"}`)
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/tasks", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var tasks []CortexTask
	if err := json.Unmarshal(w.Body.Bytes(), &tasks); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].Goal != "first" {
		t.Errorf("expected oldest goal=first, got %q", tasks[0].Goal)
	}
	if tasks[1].Goal != "second" {
		t.Errorf("expected newest goal=second, got %q", tasks[1].Goal)
	}
}

func TestWorkbenchCortexLanesGET404(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/lanes", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexLanesGETCurrent(t *testing.T) {
	wb := New()
	createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/lanes", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var lanes []CortexLane
	if err := json.Unmarshal(w.Body.Bytes(), &lanes); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(lanes) != 5 {
		t.Fatalf("expected 5 lanes, got %d", len(lanes))
	}
}

func TestWorkbenchCortexRun404(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/cortex/run", `{}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexRunSingle(t *testing.T) {
	wb := New()
	createCortexTask(t, wb, `{"goal":"ship"}`)
	w := postJSON(t, wb, "/api/workbench/cortex/run", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var task CortexTask
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if task.Status != "completed" {
		t.Errorf("expected task status=completed, got %q", task.Status)
	}
	if len(task.Lanes) != 1 {
		t.Fatalf("expected 1 lane, got %d", len(task.Lanes))
	}
	lane := task.Lanes[0]
	if lane.Status != "completed" {
		t.Errorf("expected lane status=completed, got %q", lane.Status)
	}
	if lane.Result != "Builder lane 1 ready for isolated worktree proposal generation." {
		t.Errorf("unexpected lane result: %q", lane.Result)
	}
}

func TestWorkbenchCortexRunMulti(t *testing.T) {
	wb := New()
	createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	w := postJSON(t, wb, "/api/workbench/cortex/run", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var task CortexTask
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if task.Status != "completed" {
		t.Errorf("expected task status=completed, got %q", task.Status)
	}
	if len(task.Lanes) != 5 {
		t.Fatalf("expected 5 lanes, got %d", len(task.Lanes))
	}
	for _, l := range task.Lanes {
		if l.Status != "completed" {
			t.Errorf("lane %s (%s) status=%q, want completed", l.ID, l.Role, l.Status)
		}
		if l.Result == "" {
			t.Errorf("lane %s (%s) expected a non-empty result", l.ID, l.Role)
		}
	}
	results := map[string]string{}
	for _, l := range task.Lanes {
		results[l.Role+":"+strconv.Itoa(l.Index)] = l.Result
	}
	if results["architect:1"] != "Architect lane 1 planned decomposition." {
		t.Errorf("unexpected architect result: %q", results["architect:1"])
	}
	if results["builder:2"] != "Builder lane 2 ready for isolated worktree proposal generation." {
		t.Errorf("unexpected builder result: %q", results["builder:2"])
	}
	if results["validator:1"] != "Validator lane 1 ready to recommend validation checks." {
		t.Errorf("unexpected validator result: %q", results["validator:1"])
	}
}

func TestWorkbenchCortexRunAppendsEvents(t *testing.T) {
	wb := New()
	createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`) // 5 lanes
	w := postJSON(t, wb, "/api/workbench/cortex/run", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	counts := map[string]int{}
	for _, e := range wb.store.List() {
		counts[e.Type]++
	}
	if counts["cortex.task.started"] != 1 {
		t.Errorf("expected 1 cortex.task.started, got %d", counts["cortex.task.started"])
	}
	if counts["cortex.task.completed"] != 1 {
		t.Errorf("expected 1 cortex.task.completed, got %d", counts["cortex.task.completed"])
	}
	if counts["cortex.lane.started"] != 5 {
		t.Errorf("expected 5 cortex.lane.started, got %d", counts["cortex.lane.started"])
	}
	if counts["cortex.lane.completed"] != 5 {
		t.Errorf("expected 5 cortex.lane.completed, got %d", counts["cortex.lane.completed"])
	}
}

func TestWorkbenchCortexNoAPIKey(t *testing.T) {
	const secret = "cortex-top-secret"
	wb := New()
	// Configure a provider with a secret; Cortex must never touch or leak it.
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"https://api.example.com","api_key":"`+secret+`","model":"m"}`)

	created := postJSON(t, wb, "/api/workbench/cortex/task", `{"goal":"do it","mode":"multi"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", created.Code, created.Body.String())
	}
	assertNoSecret(t, "cortex task create", secret, created.Body.Bytes())

	run := postJSON(t, wb, "/api/workbench/cortex/run", `{}`)
	if run.Code != http.StatusOK {
		t.Fatalf("run: expected 200, got %d: %s", run.Code, run.Body.String())
	}
	assertNoSecret(t, "cortex run", secret, run.Body.Bytes())

	for _, path := range []string{"/api/workbench/cortex/task", "/api/workbench/cortex/tasks", "/api/workbench/cortex/lanes"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		wb.mux.ServeHTTP(rec, req)
		assertNoSecret(t, path, secret, rec.Body.Bytes())
	}

	for _, e := range wb.store.List() {
		if strings.HasPrefix(e.Type, "cortex.") {
			assertNoSecret(t, "event "+e.Type, secret, e.Data)
		}
	}
}

// registerWorktreeCleanup removes the worktree root that worktree allocation
// creates outside the project for the given task. The whole temp tree is also
// auto-removed by t.TempDir, so this is belt-and-suspenders.
func registerWorktreeCleanup(t *testing.T, projectDir, taskID string) {
	t.Helper()
	root := filepath.Join(filepath.Dir(projectDir), ".hirdforge-worktrees", filepath.Base(projectDir), "task-"+taskID)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
}

func TestWorkbenchCortexWorktrees404NoTask(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	wb := New()
	dir := initGitRepo(t)
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	w := postJSON(t, wb, "/api/workbench/cortex/worktrees", `{}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexWorktrees409NoProject(t *testing.T) {
	wb := New()
	createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	w := postJSON(t, wb, "/api/workbench/cortex/worktrees", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexWorktrees409NonGit(t *testing.T) {
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	w := postJSON(t, wb, "/api/workbench/cortex/worktrees", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexWorktreesAllocates(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	wb := New()
	dir := initGitRepo(t)
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`) // builder x2
	registerWorktreeCleanup(t, dir, task.ID)

	w := postJSON(t, wb, "/api/workbench/cortex/worktrees", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updated CortexTask
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode: %v", err)
	}

	root := filepath.Join(filepath.Dir(dir), ".hirdforge-worktrees", filepath.Base(dir), "task-"+task.ID)
	builders := 0
	for _, l := range updated.Lanes {
		if l.Role != "builder" {
			continue
		}
		builders++
		if l.WorkspacePath == "" || l.BaseBranch == "" || l.WorktreeBranch == "" {
			t.Errorf("builder lane %s has empty worktree fields: %q / %q / %q", l.ID, l.WorkspacePath, l.BaseBranch, l.WorktreeBranch)
			continue
		}
		wantPath := filepath.Join(root, "builder-"+strconv.Itoa(l.Index))
		if l.WorkspacePath != wantPath {
			t.Errorf("lane %s workspace_path=%q, want %q", l.ID, l.WorkspacePath, wantPath)
		}
		wantBranch := "hirdforge/task-" + task.ID + "/builder-" + strconv.Itoa(l.Index)
		if l.WorktreeBranch != wantBranch {
			t.Errorf("lane %s worktree_branch=%q, want %q", l.ID, l.WorktreeBranch, wantBranch)
		}
		if info, err := os.Stat(l.WorkspacePath); err != nil || !info.IsDir() {
			t.Errorf("lane %s worktree dir not created at %q (err=%v)", l.ID, l.WorkspacePath, err)
		}
	}
	if builders != 2 {
		t.Errorf("expected 2 builder lanes, saw %d", builders)
	}
}

func TestWorkbenchCortexWorktreesLeavesOtherRoles(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	wb := New()
	dir := initGitRepo(t)
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	registerWorktreeCleanup(t, dir, task.ID)

	w := postJSON(t, wb, "/api/workbench/cortex/worktrees", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updated CortexTask
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, l := range updated.Lanes {
		if l.Role == "builder" {
			continue
		}
		if l.WorkspacePath != "" || l.BaseBranch != "" || l.WorktreeBranch != "" {
			t.Errorf("non-builder lane %s (%s) should be unchanged, got %q / %q / %q",
				l.ID, l.Role, l.WorkspacePath, l.BaseBranch, l.WorktreeBranch)
		}
	}
}

func TestWorkbenchCortexWorktreesIdempotent(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	wb := New()
	dir := initGitRepo(t)
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	registerWorktreeCleanup(t, dir, task.ID)

	first := postJSON(t, wb, "/api/workbench/cortex/worktrees", `{}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first: expected 200, got %d: %s", first.Code, first.Body.String())
	}
	var firstTask CortexTask
	if err := json.Unmarshal(first.Body.Bytes(), &firstTask); err != nil {
		t.Fatalf("decode first: %v", err)
	}

	// Re-allocating must not re-run git worktree add for allocated lanes.
	second := postJSON(t, wb, "/api/workbench/cortex/worktrees", `{}`)
	if second.Code != http.StatusOK {
		t.Fatalf("second: expected 200 (idempotent), got %d: %s", second.Code, second.Body.String())
	}
	var secondTask CortexTask
	if err := json.Unmarshal(second.Body.Bytes(), &secondTask); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if len(firstTask.Lanes) != len(secondTask.Lanes) {
		t.Fatalf("lane count changed: %d vs %d", len(firstTask.Lanes), len(secondTask.Lanes))
	}
	for i := range firstTask.Lanes {
		f, s := firstTask.Lanes[i], secondTask.Lanes[i]
		if f.WorkspacePath != s.WorkspacePath || f.BaseBranch != s.BaseBranch || f.WorktreeBranch != s.WorktreeBranch {
			t.Errorf("lane %s changed on re-allocation: %q/%q/%q -> %q/%q/%q",
				f.ID, f.WorkspacePath, f.BaseBranch, f.WorktreeBranch, s.WorkspacePath, s.BaseBranch, s.WorktreeBranch)
		}
	}
}

func TestWorkbenchCortexWorktreesGET404(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/worktrees", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexWorktreesGETBuilderOnly(t *testing.T) {
	wb := New()
	createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`) // architect1, builder2, reviewer1, validator1
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/worktrees", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var lanes []CortexLane
	if err := json.Unmarshal(w.Body.Bytes(), &lanes); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(lanes) != 2 {
		t.Fatalf("expected 2 builder lanes, got %d", len(lanes))
	}
	for _, l := range lanes {
		if l.Role != "builder" {
			t.Errorf("expected only builder lanes, got role %q", l.Role)
		}
	}
}

func TestWorkbenchCortexWorktreesEvent(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	wb := New()
	dir := initGitRepo(t)
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	registerWorktreeCleanup(t, dir, task.ID)

	w := postJSON(t, wb, "/api/workbench/cortex/worktrees", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var found *WorkbenchEvent
	events := wb.store.List()
	for i := range events {
		if events[i].Type == "cortex.worktrees.allocated" {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatal("cortex.worktrees.allocated event not found")
	}
	if found.Message != "Allocated Cortex Builder worktrees" {
		t.Errorf("unexpected message: %q", found.Message)
	}
	if len(found.Data) == 0 {
		t.Error("expected event to carry task data")
	}
}

func TestWorkbenchCortexWorktreesNoAPIKey(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	const secret = "worktree-top-secret"
	wb := New()
	dir := initGitRepo(t)
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"https://api.example.com","api_key":"`+secret+`","model":"m"}`)
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	registerWorktreeCleanup(t, dir, task.ID)

	post := postJSON(t, wb, "/api/workbench/cortex/worktrees", `{}`)
	if post.Code != http.StatusOK {
		t.Fatalf("post: expected 200, got %d: %s", post.Code, post.Body.String())
	}
	assertNoSecret(t, "worktrees post", secret, post.Body.Bytes())

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/worktrees", nil)
	rec := httptest.NewRecorder()
	wb.mux.ServeHTTP(rec, req)
	assertNoSecret(t, "worktrees get", secret, rec.Body.Bytes())

	for _, e := range wb.store.List() {
		if strings.HasPrefix(e.Type, "cortex.") {
			assertNoSecret(t, "event "+e.Type, secret, e.Data)
		}
	}
}

// cortexFirstBuilderLane returns the first Builder lane of a task.
func cortexFirstBuilderLane(t *testing.T, task CortexTask) CortexLane {
	t.Helper()
	for _, l := range task.Lanes {
		if l.Role == "builder" {
			return l
		}
	}
	t.Fatalf("no builder lane in task %s", task.ID)
	return CortexLane{}
}

// cortexLaneProposeSetup builds a workbench with a git project open and a mock
// provider configured, returning the workbench and the project dir.
func cortexLaneProposeSetup(t *testing.T, handler http.HandlerFunc) (*Server, string) {
	t.Helper()
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	server := startMockProvider(t, handler)
	wb := New()
	dir := initGitRepo(t)
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"k","model":"m"}`)
	return wb, dir
}

// cortexHasEvent returns the first event of the given type, or nil.
func cortexHasEvent(wb *Server, evType string) *WorkbenchEvent {
	for _, e := range wb.store.List() {
		if e.Type == evType {
			ev := e
			return &ev
		}
	}
	return nil
}

func TestWorkbenchCortexLaneProposeRequiresLaneID(t *testing.T) {
	wb := New()
	createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	w := postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexLanePropose404NoTask(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"lane_id":"1"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexLanePropose404MissingLane(t *testing.T) {
	wb := New()
	createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	w := postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"lane_id":"999"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexLanePropose409NonBuilder(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	var architectID string
	for _, l := range task.Lanes {
		if l.Role == "architect" {
			architectID = l.ID
		}
	}
	if architectID == "" {
		t.Fatal("no architect lane present")
	}
	w := postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"lane_id":"`+architectID+`"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexLanePropose409NoProvider(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	b := cortexFirstBuilderLane(t, task)
	w := postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"lane_id":"`+b.ID+`"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexLanePropose409NoProject(t *testing.T) {
	wb := New()
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"https://api.example.com","api_key":"k","model":"m"}`)
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	b := cortexFirstBuilderLane(t, task)
	w := postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"lane_id":"`+b.ID+`"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexLaneProposeUsesWorkspacePath(t *testing.T) {
	var capturedUser string
	wb, dir := cortexLaneProposeSetup(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]string `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) == 2 {
			capturedUser = body.Messages[1]["content"]
		}
		proposalResponse("ok", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "package a\n", Rationale: "x"}})(w, r)
	})
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	registerWorktreeCleanup(t, dir, task.ID)
	wt := postJSON(t, wb, "/api/workbench/cortex/worktrees", `{}`)
	if wt.Code != http.StatusOK {
		t.Fatalf("worktrees: expected 200, got %d: %s", wt.Code, wt.Body.String())
	}
	var allocated CortexTask
	if err := json.Unmarshal(wt.Body.Bytes(), &allocated); err != nil {
		t.Fatalf("decode allocated: %v", err)
	}
	b := cortexFirstBuilderLane(t, allocated)
	if b.WorkspacePath == "" {
		t.Fatal("builder lane has no workspace_path after allocation")
	}

	w := postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"lane_id":"`+b.ID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var prop CortexLaneProposal
	if err := json.Unmarshal(w.Body.Bytes(), &prop); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if prop.WorkspacePath != b.WorkspacePath {
		t.Errorf("proposal workspace_path=%q, want lane workspace %q", prop.WorkspacePath, b.WorkspacePath)
	}
	if !strings.Contains(capturedUser, b.WorkspacePath) {
		t.Errorf("expected prompt to reference workspace path %q:\n%s", b.WorkspacePath, capturedUser)
	}
}

func TestWorkbenchCortexLaneProposeFallsBackToProjectPath(t *testing.T) {
	var capturedUser string
	wb, dir := cortexLaneProposeSetup(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]string `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) == 2 {
			capturedUser = body.Messages[1]["content"]
		}
		proposalResponse("ok", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "x"}})(w, r)
	})
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`) // no worktree allocation
	b := cortexFirstBuilderLane(t, task)
	if b.WorkspacePath != "" {
		t.Fatal("expected empty workspace_path before allocation")
	}

	w := postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"lane_id":"`+b.ID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var prop CortexLaneProposal
	if err := json.Unmarshal(w.Body.Bytes(), &prop); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if prop.WorkspacePath != dir {
		t.Errorf("expected fallback to project path %q, got %q", dir, prop.WorkspacePath)
	}
	if !strings.Contains(capturedUser, "Warning") {
		t.Errorf("expected fallback warning in prompt:\n%s", capturedUser)
	}
	if !strings.Contains(capturedUser, dir) {
		t.Errorf("expected project path %q in prompt", dir)
	}
}

func TestWorkbenchCortexLaneProposePromptContext(t *testing.T) {
	var capturedUser, capturedSystem string
	wb, dir := cortexLaneProposeSetup(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]string `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) == 2 {
			capturedSystem = body.Messages[0]["content"]
			capturedUser = body.Messages[1]["content"]
		}
		proposalResponse("ok", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "x"}})(w, r)
	})
	task := createCortexTask(t, wb, `{"goal":"ship the thing","mode":"multi"}`)
	registerWorktreeCleanup(t, dir, task.ID)
	wt := postJSON(t, wb, "/api/workbench/cortex/worktrees", `{}`)
	var allocated CortexTask
	if err := json.Unmarshal(wt.Body.Bytes(), &allocated); err != nil {
		t.Fatalf("decode allocated: %v", err)
	}
	b := cortexFirstBuilderLane(t, allocated)

	w := postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"lane_id":"`+b.ID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	for _, want := range []string{
		"ship the thing",
		"Lane id: " + b.ID,
		"Lane index: " + strconv.Itoa(b.Index),
		b.Task,
		b.WorktreeBranch,
		b.BaseBranch,
		`"summary"`,
	} {
		if !strings.Contains(capturedUser, want) {
			t.Errorf("expected prompt to contain %q:\n%s", want, capturedUser)
		}
	}
	if capturedSystem != builderProposalSystemPrompt {
		t.Errorf("unexpected system prompt: %q", capturedSystem)
	}
}

func TestWorkbenchCortexLaneProposeStores(t *testing.T) {
	files := []BuilderProposedFile{
		{Path: "main.go", Action: "modify", Content: "package main\n", Rationale: "u"},
		{Path: "x.go", Action: "create", Content: "package x\n", Rationale: "a"},
	}
	wb, _ := cortexLaneProposeSetup(t, proposalResponse("apply changes", files))
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	b := cortexFirstBuilderLane(t, task)
	w := postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"lane_id":"`+b.ID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var prop CortexLaneProposal
	if err := json.Unmarshal(w.Body.Bytes(), &prop); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if prop.ID == "" || prop.TS.IsZero() {
		t.Error("expected id and ts to be set")
	}
	if prop.TaskID != task.ID {
		t.Errorf("task_id=%q, want %q", prop.TaskID, task.ID)
	}
	if prop.LaneID != b.ID {
		t.Errorf("lane_id=%q, want %q", prop.LaneID, b.ID)
	}
	if prop.LaneIndex != b.Index {
		t.Errorf("lane_index=%d, want %d", prop.LaneIndex, b.Index)
	}
	if prop.Status != "proposed" {
		t.Errorf("status=%q, want proposed", prop.Status)
	}
	if prop.Summary != "apply changes" {
		t.Errorf("summary=%q", prop.Summary)
	}
	if len(prop.Files) != len(files) {
		t.Fatalf("expected %d files, got %d", len(files), len(prop.Files))
	}
	for i := range files {
		if prop.Files[i] != files[i] {
			t.Errorf("file[%d]=%+v, want %+v", i, prop.Files[i], files[i])
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/lane/proposal", nil)
	rec := httptest.NewRecorder()
	wb.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET lane/proposal: expected 200, got %d", rec.Code)
	}
}

func TestWorkbenchCortexLaneProposeReusesExisting(t *testing.T) {
	files := []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "package a\n", Rationale: "x"}}
	wb, _ := cortexLaneProposeSetup(t, proposalResponse("ok", files))
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	b := cortexFirstBuilderLane(t, task)

	first := postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"lane_id":"`+b.ID+`"}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first: expected 201, got %d: %s", first.Code, first.Body.String())
	}
	var p1 CortexLaneProposal
	json.Unmarshal(first.Body.Bytes(), &p1)

	// A repeat for the same lane reuses the existing proposal (200, same id) and
	// does not create a duplicate that would later double-count at aggregation.
	second := postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"lane_id":"`+b.ID+`"}`)
	if second.Code != http.StatusOK {
		t.Fatalf("repeat: expected 200 (reuse), got %d: %s", second.Code, second.Body.String())
	}
	var p2 CortexLaneProposal
	json.Unmarshal(second.Body.Bytes(), &p2)
	if p2.ID != p1.ID {
		t.Errorf("repeat returned proposal #%s, want existing #%s", p2.ID, p1.ID)
	}

	rec := httptest.NewRecorder()
	wb.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/lane/proposals?lane_id="+b.ID, nil))
	var list []CortexLaneProposal
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Errorf("expected exactly 1 proposal for the lane after a repeat, got %d", len(list))
	}
}

func TestWorkbenchCortexLaneProposeFailed(t *testing.T) {
	wb, _ := cortexLaneProposeSetup(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"upstream exploded"}`))
	})
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	b := cortexFirstBuilderLane(t, task)
	w := postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"lane_id":"`+b.ID+`"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Proposal CortexLaneProposal `json:"proposal"`
		Error    string             `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Proposal.Status != "failed" {
		t.Errorf("status=%q, want failed", result.Proposal.Status)
	}
	if result.Proposal.Error == "" {
		t.Error("expected proposal.error to be set")
	}
	if len(result.Proposal.Files) != 0 {
		t.Errorf("expected empty files on failure, got %d", len(result.Proposal.Files))
	}
	if !strings.Contains(result.Error, "upstream exploded") {
		t.Errorf("expected error to include upstream body, got %q", result.Error)
	}
}

func TestWorkbenchCortexLaneProposalGET404(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/lane/proposal", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexLaneProposalGETCurrent(t *testing.T) {
	wb, _ := cortexLaneProposeSetup(t, proposalResponse("ok", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "x"}}))
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	b := cortexFirstBuilderLane(t, task)
	created := postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"lane_id":"`+b.ID+`"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("propose: expected 201, got %d: %s", created.Code, created.Body.String())
	}
	var createdProp CortexLaneProposal
	if err := json.Unmarshal(created.Body.Bytes(), &createdProp); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/lane/proposal", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var prop CortexLaneProposal
	if err := json.Unmarshal(w.Body.Bytes(), &prop); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if prop.ID != createdProp.ID {
		t.Errorf("current proposal id=%q, want %q", prop.ID, createdProp.ID)
	}
}

func TestWorkbenchCortexLaneProposalsEmpty(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/lane/proposals", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := strings.TrimSpace(w.Body.String()); body != "[]" {
		t.Errorf("expected [] for empty proposals, got %q", body)
	}
}

func TestWorkbenchCortexLaneProposalsFilterByTask(t *testing.T) {
	wb, _ := cortexLaneProposeSetup(t, proposalResponse("ok", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "x"}}))
	task1 := createCortexTask(t, wb, `{"goal":"first","mode":"multi"}`)
	b1 := cortexFirstBuilderLane(t, task1)
	postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"task_id":"`+task1.ID+`","lane_id":"`+b1.ID+`"}`)
	task2 := createCortexTask(t, wb, `{"goal":"second","mode":"multi"}`)
	b2 := cortexFirstBuilderLane(t, task2)
	postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"task_id":"`+task2.ID+`","lane_id":"`+b2.ID+`"}`)

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/lane/proposals?task_id="+task1.ID, nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var props []CortexLaneProposal
	if err := json.Unmarshal(w.Body.Bytes(), &props); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(props) != 1 {
		t.Fatalf("expected 1 proposal for task1, got %d", len(props))
	}
	if props[0].TaskID != task1.ID {
		t.Errorf("task_id=%q, want %q", props[0].TaskID, task1.ID)
	}

	reqAll := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/lane/proposals", nil)
	wAll := httptest.NewRecorder()
	wb.mux.ServeHTTP(wAll, reqAll)
	var all []CortexLaneProposal
	if err := json.Unmarshal(wAll.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode all: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 total proposals, got %d", len(all))
	}
}

func TestWorkbenchCortexLaneProposalsFilterByLane(t *testing.T) {
	wb, _ := cortexLaneProposeSetup(t, proposalResponse("ok", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "x"}}))
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	var builders []CortexLane
	for _, l := range task.Lanes {
		if l.Role == "builder" {
			builders = append(builders, l)
		}
	}
	if len(builders) < 2 {
		t.Fatalf("expected >=2 builder lanes, got %d", len(builders))
	}
	for _, b := range builders {
		postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"lane_id":"`+b.ID+`"}`)
	}
	target := builders[0]
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/lane/proposals?lane_id="+target.ID, nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var props []CortexLaneProposal
	if err := json.Unmarshal(w.Body.Bytes(), &props); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(props) != 1 {
		t.Fatalf("expected 1 proposal for lane %s, got %d", target.ID, len(props))
	}
	if props[0].LaneID != target.ID {
		t.Errorf("lane_id=%q, want %q", props[0].LaneID, target.ID)
	}
}

func TestWorkbenchCortexLaneProposeEvents(t *testing.T) {
	wb, _ := cortexLaneProposeSetup(t, proposalResponse("ok", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "x"}}))
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	b := cortexFirstBuilderLane(t, task)
	if w := postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"lane_id":"`+b.ID+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("created: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if ev := cortexHasEvent(wb, "cortex.lane.proposal.created"); ev == nil {
		t.Error("cortex.lane.proposal.created event not appended")
	} else if ev.Message != "Created Cortex Builder lane proposal" {
		t.Errorf("created event message: %q", ev.Message)
	}

	wb2, _ := cortexLaneProposeSetup(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	task2 := createCortexTask(t, wb2, `{"goal":"g","mode":"multi"}`)
	b2 := cortexFirstBuilderLane(t, task2)
	if w := postJSON(t, wb2, "/api/workbench/cortex/lane/propose", `{"lane_id":"`+b2.ID+`"}`); w.Code != http.StatusBadGateway {
		t.Fatalf("failed: expected 502, got %d: %s", w.Code, w.Body.String())
	}
	if ev := cortexHasEvent(wb2, "cortex.lane.proposal.failed"); ev == nil {
		t.Error("cortex.lane.proposal.failed event not appended")
	} else if ev.Message != "Failed Cortex Builder lane proposal" {
		t.Errorf("failed event message: %q", ev.Message)
	}
}

func TestWorkbenchCortexLaneProposeNoAPIKey(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	const secret = "lane-propose-secret"
	server := startMockProvider(t, proposalResponse("ok", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "x"}}))
	wb := New()
	dir := initGitRepo(t)
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"`+secret+`","model":"m"}`)
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	b := cortexFirstBuilderLane(t, task)

	post := postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"lane_id":"`+b.ID+`"}`)
	if post.Code != http.StatusCreated {
		t.Fatalf("propose: expected 201, got %d: %s", post.Code, post.Body.String())
	}
	assertNoSecret(t, "lane propose", secret, post.Body.Bytes())

	for _, path := range []string{"/api/workbench/cortex/lane/proposal", "/api/workbench/cortex/lane/proposals"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		wb.mux.ServeHTTP(rec, req)
		assertNoSecret(t, path, secret, rec.Body.Bytes())
	}
	for _, e := range wb.store.List() {
		if strings.HasPrefix(e.Type, "cortex.") {
			assertNoSecret(t, "event "+e.Type, secret, e.Data)
		}
	}
}

func TestWorkbenchCortexLaneProposeDoesNotWriteFiles(t *testing.T) {
	if !gitAvailable(t) {
		t.Skip("git not available")
	}
	files := []BuilderProposedFile{
		{Path: "newfile.go", Action: "create", Content: "package newpkg\n", Rationale: "add"},
		{Path: ".gitkeep", Action: "modify", Content: "changed\n", Rationale: "edit"},
	}
	server := startMockProvider(t, proposalResponse("touch", files))
	wb := New()
	dir := initGitRepo(t)
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"k","model":"m"}`)
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	b := cortexFirstBuilderLane(t, task) // empty workspace_path -> falls back to project dir

	before := snapshotDir(t, dir)
	w := postJSON(t, wb, "/api/workbench/cortex/lane/propose", `{"lane_id":"`+b.ID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("propose: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	after := snapshotDir(t, dir)

	if len(before) != len(after) {
		t.Fatalf("file count changed: %d -> %d", len(before), len(after))
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

// seedLaneProposal stores a proposed Cortex Builder lane proposal directly,
// without a provider round-trip, for aggregation tests.
func seedLaneProposal(t *testing.T, wb *Server, taskID, laneID string, files []BuilderProposedFile) CortexLaneProposal {
	t.Helper()
	return wb.cortexLaneProposals.Append(CortexLaneProposal{
		TaskID:  taskID,
		LaneID:  laneID,
		Status:  "proposed",
		Summary: "lane " + laneID,
		Files:   files,
	})
}

func aggregatePaths(agg CortexAggregateProposal) []string {
	out := []string{}
	for _, f := range agg.Files {
		out = append(out, f.Path)
	}
	return out
}

func TestWorkbenchCortexAggregate404NoTask(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexAggregate409NoProposed(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	// Only a failed lane proposal exists -> nothing to aggregate.
	wb.cortexLaneProposals.Append(CortexLaneProposal{TaskID: task.ID, LaneID: "2", Status: "failed", Error: "x"})
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexAggregateCombinesFiles(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	seedLaneProposal(t, wb, task.ID, "2", []BuilderProposedFile{
		{Path: "a.go", Action: "create", Content: "A"},
		{Path: "b.go", Action: "create", Content: "B"},
	})
	seedLaneProposal(t, wb, task.ID, "3", []BuilderProposedFile{
		{Path: "c.go", Action: "create", Content: "C"},
	})
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var agg CortexAggregateProposal
	if err := json.Unmarshal(w.Body.Bytes(), &agg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if agg.Status != "aggregated" {
		t.Errorf("status=%q, want aggregated", agg.Status)
	}
	if agg.TaskID != task.ID {
		t.Errorf("task_id=%q, want %q", agg.TaskID, task.ID)
	}
	want := []string{"a.go", "b.go", "c.go"}
	got := aggregatePaths(agg)
	if len(got) != len(want) {
		t.Fatalf("files=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("file[%d]=%q, want %q", i, got[i], want[i])
		}
	}
	if len(agg.Conflicts) != 0 {
		t.Errorf("expected no conflicts, got %d", len(agg.Conflicts))
	}
}

func TestWorkbenchCortexAggregateSourceOrder(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	p1 := seedLaneProposal(t, wb, task.ID, "2", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A"}})
	p2 := seedLaneProposal(t, wb, task.ID, "3", []BuilderProposedFile{{Path: "b.go", Action: "create", Content: "B"}})
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var agg CortexAggregateProposal
	if err := json.Unmarshal(w.Body.Bytes(), &agg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(agg.SourceProposalIDs) != 2 || agg.SourceProposalIDs[0] != p1.ID || agg.SourceProposalIDs[1] != p2.ID {
		t.Errorf("source_proposal_ids=%v, want [%s %s]", agg.SourceProposalIDs, p1.ID, p2.ID)
	}
}

func TestWorkbenchCortexAggregateConflictContent(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	p1 := seedLaneProposal(t, wb, task.ID, "2", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A"}})
	p2 := seedLaneProposal(t, wb, task.ID, "3", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "DIFFERENT"}})
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var agg CortexAggregateProposal
	if err := json.Unmarshal(w.Body.Bytes(), &agg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if agg.Status != "conflicted" {
		t.Errorf("status=%q, want conflicted", agg.Status)
	}
	if len(agg.Conflicts) != 1 {
		t.Fatalf("conflicts=%d, want 1", len(agg.Conflicts))
	}
	c := agg.Conflicts[0]
	if c.Path != "a.go" {
		t.Errorf("conflict path=%q, want a.go", c.Path)
	}
	ids := map[string]bool{}
	for _, id := range c.ProposalIDs {
		ids[id] = true
	}
	if !ids[p1.ID] || !ids[p2.ID] {
		t.Errorf("conflict proposal_ids=%v, want both %s and %s", c.ProposalIDs, p1.ID, p2.ID)
	}
	for _, f := range agg.Files {
		if f.Path == "a.go" {
			t.Error("conflicting file a.go should be excluded from files")
		}
	}
}

func TestWorkbenchCortexAggregateConflictAction(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	seedLaneProposal(t, wb, task.ID, "2", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A"}})
	seedLaneProposal(t, wb, task.ID, "3", []BuilderProposedFile{{Path: "a.go", Action: "delete", Content: ""}})
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var agg CortexAggregateProposal
	if err := json.Unmarshal(w.Body.Bytes(), &agg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if agg.Status != "conflicted" {
		t.Errorf("status=%q, want conflicted", agg.Status)
	}
	if len(agg.Conflicts) != 1 || agg.Conflicts[0].Path != "a.go" {
		t.Errorf("expected 1 conflict on a.go, got %v", agg.Conflicts)
	}
}

func TestWorkbenchCortexAggregateSamePathSameContent(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	seedLaneProposal(t, wb, task.ID, "2", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "SAME"}})
	seedLaneProposal(t, wb, task.ID, "3", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "SAME"}})
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var agg CortexAggregateProposal
	if err := json.Unmarshal(w.Body.Bytes(), &agg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if agg.Status != "aggregated" {
		t.Errorf("status=%q, want aggregated", agg.Status)
	}
	if len(agg.Conflicts) != 0 {
		t.Errorf("expected no conflicts, got %d", len(agg.Conflicts))
	}
	if len(agg.Files) != 1 {
		t.Errorf("expected 1 deduplicated file, got %d: %v", len(agg.Files), aggregatePaths(agg))
	}
	if len(agg.Files) > 0 && agg.Files[0].Path != "a.go" {
		t.Errorf("file path=%q, want a.go", agg.Files[0].Path)
	}
}

func TestWorkbenchCortexAggregateConflictExcludes(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	seedLaneProposal(t, wb, task.ID, "2", []BuilderProposedFile{
		{Path: "a.go", Action: "create", Content: "A"},
		{Path: "keep.go", Action: "create", Content: "K"},
	})
	seedLaneProposal(t, wb, task.ID, "3", []BuilderProposedFile{
		{Path: "a.go", Action: "create", Content: "DIFFERENT"},
	})
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var agg CortexAggregateProposal
	if err := json.Unmarshal(w.Body.Bytes(), &agg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if agg.Status != "conflicted" {
		t.Errorf("status=%q, want conflicted", agg.Status)
	}
	hasKeep := false
	for _, f := range agg.Files {
		if f.Path == "a.go" {
			t.Error("conflicting file a.go should be excluded")
		}
		if f.Path == "keep.go" {
			hasKeep = true
		}
	}
	if !hasKeep {
		t.Error("non-conflicting file keep.go should be included")
	}
	if len(agg.Conflicts) != 1 || agg.Conflicts[0].Path != "a.go" {
		t.Errorf("expected 1 conflict on a.go, got %v", agg.Conflicts)
	}
}

func TestWorkbenchCortexAggregateGET404(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/aggregate", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexAggregateGETCurrent(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	seedLaneProposal(t, wb, task.ID, "2", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A"}})
	created := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("aggregate: expected 201, got %d: %s", created.Code, created.Body.String())
	}
	var createdAgg CortexAggregateProposal
	if err := json.Unmarshal(created.Body.Bytes(), &createdAgg); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/aggregate", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var agg CortexAggregateProposal
	if err := json.Unmarshal(w.Body.Bytes(), &agg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if agg.ID != createdAgg.ID {
		t.Errorf("current aggregate id=%q, want %q", agg.ID, createdAgg.ID)
	}
}

func TestWorkbenchCortexAggregatesEmpty(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/aggregates", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := strings.TrimSpace(w.Body.String()); body != "[]" {
		t.Errorf("expected [] for empty aggregates, got %q", body)
	}
}

func TestWorkbenchCortexAggregatesFilterByTask(t *testing.T) {
	wb := New()
	task1 := createCortexTask(t, wb, `{"goal":"first","mode":"multi"}`)
	seedLaneProposal(t, wb, task1.ID, "2", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A"}})
	postJSON(t, wb, "/api/workbench/cortex/aggregate", `{"task_id":"`+task1.ID+`"}`)
	task2 := createCortexTask(t, wb, `{"goal":"second","mode":"multi"}`)
	seedLaneProposal(t, wb, task2.ID, "2", []BuilderProposedFile{{Path: "b.go", Action: "create", Content: "B"}})
	postJSON(t, wb, "/api/workbench/cortex/aggregate", `{"task_id":"`+task2.ID+`"}`)

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/aggregates?task_id="+task1.ID, nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var filtered []CortexAggregateProposal
	if err := json.Unmarshal(w.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(filtered) != 1 || filtered[0].TaskID != task1.ID {
		t.Fatalf("expected 1 aggregate for task1, got %v", filtered)
	}

	reqAll := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/aggregates", nil)
	wAll := httptest.NewRecorder()
	wb.mux.ServeHTTP(wAll, reqAll)
	var all []CortexAggregateProposal
	if err := json.Unmarshal(wAll.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode all: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 total aggregates, got %d", len(all))
	}
}

func TestWorkbenchCortexAggregateEvent(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	seedLaneProposal(t, wb, task.ID, "2", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A"}})
	if w := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`); w.Code != http.StatusCreated {
		t.Fatalf("aggregate: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	ev := cortexHasEvent(wb, "cortex.aggregate.created")
	if ev == nil {
		t.Fatal("cortex.aggregate.created event not appended")
	}
	if ev.Message != "Created Cortex aggregate proposal" {
		t.Errorf("event message: %q", ev.Message)
	}
	if len(ev.Data) == 0 {
		t.Error("expected event to carry aggregate data")
	}
}

func TestWorkbenchCortexAggregateNoAPIKey(t *testing.T) {
	const secret = "aggregate-secret"
	wb := New()
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"https://api.example.com","api_key":"`+secret+`","model":"m"}`)
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	seedLaneProposal(t, wb, task.ID, "2", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A"}})

	agg := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	if agg.Code != http.StatusCreated {
		t.Fatalf("aggregate: expected 201, got %d: %s", agg.Code, agg.Body.String())
	}
	assertNoSecret(t, "aggregate", secret, agg.Body.Bytes())

	for _, path := range []string{"/api/workbench/cortex/aggregate", "/api/workbench/cortex/aggregates"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		wb.mux.ServeHTTP(rec, req)
		assertNoSecret(t, path, secret, rec.Body.Bytes())
	}
	for _, e := range wb.store.List() {
		if strings.HasPrefix(e.Type, "cortex.") {
			assertNoSecret(t, "event "+e.Type, secret, e.Data)
		}
	}
}

func TestWorkbenchCortexAggregateLockbox404NoAggregate(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate/lockbox", `{}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexAggregateLockbox409Conflicted(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	seedLaneProposal(t, wb, task.ID, "2", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A"}})
	seedLaneProposal(t, wb, task.ID, "3", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "DIFFERENT"}})
	agg := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	var decoded CortexAggregateProposal
	json.Unmarshal(agg.Body.Bytes(), &decoded)
	if decoded.Status != "conflicted" {
		t.Fatalf("expected conflicted aggregate, got %q", decoded.Status)
	}
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate/lockbox", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexAggregateLockboxCreatesRequest(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"ship it","mode":"multi"}`)
	seedLaneProposal(t, wb, task.ID, "2", []BuilderProposedFile{
		{Path: "a.go", Action: "create", Content: "A", Rationale: "x"},
		{Path: "b.go", Action: "create", Content: "B", Rationale: "y"},
	})
	aggResp := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	if aggResp.Code != http.StatusCreated {
		t.Fatalf("aggregate: expected 201, got %d: %s", aggResp.Code, aggResp.Body.String())
	}
	var agg CortexAggregateProposal
	json.Unmarshal(aggResp.Body.Bytes(), &agg)
	if agg.Status != "aggregated" {
		t.Fatalf("expected aggregated, got %q", agg.Status)
	}

	w := postJSON(t, wb, "/api/workbench/cortex/aggregate/lockbox", `{}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var req LockboxApprovalRequest
	if err := json.Unmarshal(w.Body.Bytes(), &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.ProposalID != "aggregate:"+agg.ID {
		t.Errorf("proposal_id=%q, want aggregate:%s", req.ProposalID, agg.ID)
	}
	if req.Goal != "ship it" {
		t.Errorf("goal=%q, want 'ship it'", req.Goal)
	}
	if req.Summary != agg.Summary {
		t.Errorf("summary=%q, want %q", req.Summary, agg.Summary)
	}
	if req.Status != "pending" {
		t.Errorf("status=%q, want pending", req.Status)
	}
	if len(req.Files) != len(agg.Files) {
		t.Fatalf("files=%d, want %d", len(req.Files), len(agg.Files))
	}
	for i := range agg.Files {
		if req.Files[i] != agg.Files[i] {
			t.Errorf("file[%d]=%+v, want %+v", i, req.Files[i], agg.Files[i])
		}
	}

	// The request is stored in the Lockbox.
	getReq := httptest.NewRequest(http.MethodGet, "/api/workbench/lockbox/request", nil)
	getRec := httptest.NewRecorder()
	wb.mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Errorf("GET lockbox/request: expected 200, got %d", getRec.Code)
	}
}

func TestWorkbenchCortexAggregateLockboxEvent(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	seedLaneProposal(t, wb, task.ID, "2", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A"}})
	postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	if w := postJSON(t, wb, "/api/workbench/cortex/aggregate/lockbox", `{}`); w.Code != http.StatusCreated {
		t.Fatalf("aggregate/lockbox: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	ev := cortexHasEvent(wb, "cortex.aggregate.lockbox_requested")
	if ev == nil {
		t.Fatal("cortex.aggregate.lockbox_requested event not appended")
	}
	if ev.Message != "Created Lockbox request from Cortex aggregate" {
		t.Errorf("event message: %q", ev.Message)
	}
}

func TestWorkbenchCortexAggregateLockboxDoesNotApplyFiles(t *testing.T) {
	wb := New()
	dir := t.TempDir()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	seedLaneProposal(t, wb, task.ID, "2", []BuilderProposedFile{{Path: "newfile.go", Action: "create", Content: "package x\n"}})
	postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)

	before := snapshotDir(t, dir)
	if w := postJSON(t, wb, "/api/workbench/cortex/aggregate/lockbox", `{}`); w.Code != http.StatusCreated {
		t.Fatalf("aggregate/lockbox: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	after := snapshotDir(t, dir)

	if len(before) != len(after) {
		t.Fatalf("file count changed: %d -> %d", len(before), len(after))
	}
	if _, err := os.Stat(filepath.Join(dir, "newfile.go")); !os.IsNotExist(err) {
		t.Errorf("aggregate proposed file newfile.go must not be written to disk (err=%v)", err)
	}
}

// reviewResponse returns a handler that replies with an OpenAI-style envelope
// whose message content is a JSON aggregate review.
func reviewResponse(verdict, summary string, risks, recommendations []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		content, _ := json.Marshal(map[string]any{
			"verdict":         verdict,
			"summary":         summary,
			"risks":           risks,
			"recommendations": recommendations,
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "test",
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": string(content)}},
			},
		})
	}
}

func cortexReviewerLanes(task CortexTask) []CortexLane {
	out := []CortexLane{}
	for _, l := range task.Lanes {
		if l.Role == "reviewer" {
			out = append(out, l)
		}
	}
	return out
}

func cortexFirstReviewerLane(t *testing.T, task CortexTask) CortexLane {
	t.Helper()
	lanes := cortexReviewerLanes(task)
	if len(lanes) == 0 {
		t.Fatalf("no reviewer lane in task %s", task.ID)
	}
	return lanes[0]
}

// cortexReviewSetup builds a workbench with a mock provider configured and one
// aggregated Cortex aggregate, returning the workbench, task, and aggregate.
func cortexReviewSetup(t *testing.T, handler http.HandlerFunc) (*Server, CortexTask, CortexAggregateProposal) {
	t.Helper()
	server := startMockProvider(t, handler)
	wb := New()
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"k","model":"m"}`)
	task := createCortexTask(t, wb, `{"goal":"ship it","mode":"multi"}`)
	seedLaneProposal(t, wb, task.ID, "2", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A"}})
	aggResp := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	if aggResp.Code != http.StatusCreated {
		t.Fatalf("aggregate: expected 201, got %d: %s", aggResp.Code, aggResp.Body.String())
	}
	var agg CortexAggregateProposal
	if err := json.Unmarshal(aggResp.Body.Bytes(), &agg); err != nil {
		t.Fatalf("decode aggregate: %v", err)
	}
	return wb, task, agg
}

func TestWorkbenchCortexAggregateReviewRequiresLaneID(t *testing.T) {
	wb, _, _ := cortexReviewSetup(t, reviewResponse("approve", "ok", nil, nil))
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexAggregateReview404NoAggregate(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"lane_id":"4"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexAggregateReview404MissingTask(t *testing.T) {
	wb := New()
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"https://api.example.com","api_key":"k","model":"m"}`)
	// An aggregate whose task is unknown.
	wb.cortexAggregates.Append(CortexAggregateProposal{TaskID: "nonexistent", Status: "aggregated", Files: []BuilderProposedFile{}})
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"lane_id":"4"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexAggregateReview404MissingLane(t *testing.T) {
	wb, _, _ := cortexReviewSetup(t, reviewResponse("approve", "ok", nil, nil))
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"lane_id":"999"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexAggregateReview409NonReviewer(t *testing.T) {
	wb, task, _ := cortexReviewSetup(t, reviewResponse("approve", "ok", nil, nil))
	b := cortexFirstBuilderLane(t, task)
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"lane_id":"`+b.ID+`"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexAggregateReview409NoProvider(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	seedLaneProposal(t, wb, task.ID, "2", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A"}})
	postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	rv := cortexFirstReviewerLane(t, task)
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"lane_id":"`+rv.ID+`"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexAggregateReviewPromptContext(t *testing.T) {
	var capturedUser, capturedSystem string
	wb, task, agg := cortexReviewSetup(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]string `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) == 2 {
			capturedSystem = body.Messages[0]["content"]
			capturedUser = body.Messages[1]["content"]
		}
		reviewResponse("approve", "ok", []string{"r1"}, []string{"rec1"})(w, r)
	})
	rv := cortexFirstReviewerLane(t, task)
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"lane_id":"`+rv.ID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	for _, want := range []string{
		"ship it",
		"Aggregate id: " + agg.ID,
		"Aggregate status: " + agg.Status,
		agg.Summary,
		"Reviewer lane id: " + rv.ID,
		"Reviewer lane index: " + strconv.Itoa(rv.Index),
		rv.Task,
		"Source proposal ids: " + strings.Join(agg.SourceProposalIDs, ", "),
		`"verdict"`,
	} {
		if !strings.Contains(capturedUser, want) {
			t.Errorf("expected review prompt to contain %q:\n%s", want, capturedUser)
		}
	}
	if capturedSystem != cortexReviewSystemPrompt {
		t.Errorf("unexpected system prompt: %q", capturedSystem)
	}
}

func TestWorkbenchCortexAggregateReviewConflictsInPrompt(t *testing.T) {
	var capturedUser string
	server := startMockProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]string `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) == 2 {
			capturedUser = body.Messages[1]["content"]
		}
		reviewResponse("revise", "conflicts present", nil, nil)(w, r)
	})
	wb := New()
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"k","model":"m"}`)
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	seedLaneProposal(t, wb, task.ID, "2", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A"}})
	seedLaneProposal(t, wb, task.ID, "3", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "DIFFERENT"}})
	aggResp := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	var agg CortexAggregateProposal
	json.Unmarshal(aggResp.Body.Bytes(), &agg)
	if agg.Status != "conflicted" {
		t.Fatalf("expected conflicted aggregate, got %q", agg.Status)
	}
	rv := cortexFirstReviewerLane(t, task)
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"lane_id":"`+rv.ID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(capturedUser, "Conflicts (1)") {
		t.Errorf("expected conflicts section in prompt:\n%s", capturedUser)
	}
	if !strings.Contains(capturedUser, "a.go") {
		t.Errorf("expected conflicting path a.go in prompt:\n%s", capturedUser)
	}
}

func TestWorkbenchCortexAggregateReviewApprove(t *testing.T) {
	wb, task, agg := cortexReviewSetup(t, reviewResponse("approve", "looks good", []string{"low risk"}, []string{"merge it"}))
	rv := cortexFirstReviewerLane(t, task)
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"lane_id":"`+rv.ID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var review CortexAggregateReview
	if err := json.Unmarshal(w.Body.Bytes(), &review); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if review.ID == "" || review.TS.IsZero() {
		t.Error("expected id and ts to be set")
	}
	if review.AggregateID != agg.ID {
		t.Errorf("aggregate_id=%q, want %q", review.AggregateID, agg.ID)
	}
	if review.TaskID != task.ID {
		t.Errorf("task_id=%q, want %q", review.TaskID, task.ID)
	}
	if review.LaneID != rv.ID {
		t.Errorf("lane_id=%q, want %q", review.LaneID, rv.ID)
	}
	if review.LaneIndex != rv.Index {
		t.Errorf("lane_index=%d, want %d", review.LaneIndex, rv.Index)
	}
	if review.Status != "reviewed" {
		t.Errorf("status=%q, want reviewed", review.Status)
	}
	if review.Verdict != "approve" {
		t.Errorf("verdict=%q, want approve", review.Verdict)
	}
	if review.Summary != "looks good" {
		t.Errorf("summary=%q", review.Summary)
	}
	if len(review.Risks) != 1 || review.Risks[0] != "low risk" {
		t.Errorf("risks=%v, want [low risk]", review.Risks)
	}
	if len(review.Recommendations) != 1 || review.Recommendations[0] != "merge it" {
		t.Errorf("recommendations=%v, want [merge it]", review.Recommendations)
	}
}

func TestWorkbenchCortexAggregateReviewRevise(t *testing.T) {
	wb, task, _ := cortexReviewSetup(t, reviewResponse("revise", "needs work", nil, nil))
	rv := cortexFirstReviewerLane(t, task)
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"lane_id":"`+rv.ID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var review CortexAggregateReview
	if err := json.Unmarshal(w.Body.Bytes(), &review); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if review.Verdict != "revise" {
		t.Errorf("verdict=%q, want revise", review.Verdict)
	}
	if review.Risks == nil || len(review.Risks) != 0 {
		t.Errorf("expected empty (non-nil) risks, got %v", review.Risks)
	}
	if review.Recommendations == nil || len(review.Recommendations) != 0 {
		t.Errorf("expected empty (non-nil) recommendations, got %v", review.Recommendations)
	}
}

func TestWorkbenchCortexAggregateReviewInvalidVerdict(t *testing.T) {
	wb, task, _ := cortexReviewSetup(t, reviewResponse("maybe", "x", nil, nil))
	rv := cortexFirstReviewerLane(t, task)
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"lane_id":"`+rv.ID+`"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Review CortexAggregateReview `json:"review"`
		Error  string                `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Review.Status != "failed" {
		t.Errorf("status=%q, want failed", result.Review.Status)
	}
	if result.Error == "" {
		t.Error("expected error to be populated")
	}
}

func TestWorkbenchCortexAggregateReviewFailed(t *testing.T) {
	wb, task, _ := cortexReviewSetup(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"upstream exploded"}`))
	})
	rv := cortexFirstReviewerLane(t, task)
	w := postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"lane_id":"`+rv.ID+`"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Review CortexAggregateReview `json:"review"`
		Error  string                `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Review.Status != "failed" {
		t.Errorf("status=%q, want failed", result.Review.Status)
	}
	if result.Review.Error == "" {
		t.Error("expected review.error to be set")
	}
	if !strings.Contains(result.Error, "upstream exploded") {
		t.Errorf("expected error to include upstream body, got %q", result.Error)
	}
}

func TestWorkbenchCortexAggregateReviewGET404(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/aggregate/review", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexAggregateReviewGETCurrent(t *testing.T) {
	wb, task, _ := cortexReviewSetup(t, reviewResponse("approve", "ok", nil, nil))
	rv := cortexFirstReviewerLane(t, task)
	created := postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"lane_id":"`+rv.ID+`"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("review: expected 201, got %d: %s", created.Code, created.Body.String())
	}
	var createdReview CortexAggregateReview
	json.Unmarshal(created.Body.Bytes(), &createdReview)

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/aggregate/review", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var review CortexAggregateReview
	json.Unmarshal(w.Body.Bytes(), &review)
	if review.ID != createdReview.ID {
		t.Errorf("current review id=%q, want %q", review.ID, createdReview.ID)
	}
}

func TestWorkbenchCortexAggregateReviewsEmpty(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/aggregate/reviews", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := strings.TrimSpace(w.Body.String()); body != "[]" {
		t.Errorf("expected [] for empty reviews, got %q", body)
	}
}

func TestWorkbenchCortexAggregateReviewsFilterByAggregate(t *testing.T) {
	wb, task, agg1 := cortexReviewSetup(t, reviewResponse("approve", "ok", nil, nil))
	rv := cortexFirstReviewerLane(t, task)
	// second aggregate for the same task
	agg2Resp := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	var agg2 CortexAggregateProposal
	json.Unmarshal(agg2Resp.Body.Bytes(), &agg2)

	postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"aggregate_id":"`+agg1.ID+`","lane_id":"`+rv.ID+`"}`)
	postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"aggregate_id":"`+agg2.ID+`","lane_id":"`+rv.ID+`"}`)

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/aggregate/reviews?aggregate_id="+agg1.ID, nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	var reviews []CortexAggregateReview
	json.Unmarshal(w.Body.Bytes(), &reviews)
	if len(reviews) != 1 || reviews[0].AggregateID != agg1.ID {
		t.Fatalf("expected 1 review for agg1, got %v", reviews)
	}

	reqAll := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/aggregate/reviews", nil)
	wAll := httptest.NewRecorder()
	wb.mux.ServeHTTP(wAll, reqAll)
	var all []CortexAggregateReview
	json.Unmarshal(wAll.Body.Bytes(), &all)
	if len(all) != 2 {
		t.Errorf("expected 2 total reviews, got %d", len(all))
	}
}

func TestWorkbenchCortexAggregateReviewsFilterByTask(t *testing.T) {
	server := startMockProvider(t, reviewResponse("approve", "ok", nil, nil))
	wb := New()
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"k","model":"m"}`)

	task1 := createCortexTask(t, wb, `{"goal":"first","mode":"multi"}`)
	seedLaneProposal(t, wb, task1.ID, "2", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A"}})
	agg1Resp := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{"task_id":"`+task1.ID+`"}`)
	var agg1 CortexAggregateProposal
	json.Unmarshal(agg1Resp.Body.Bytes(), &agg1)
	rv1 := cortexFirstReviewerLane(t, task1)
	postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"aggregate_id":"`+agg1.ID+`","lane_id":"`+rv1.ID+`"}`)

	task2 := createCortexTask(t, wb, `{"goal":"second","mode":"multi"}`)
	seedLaneProposal(t, wb, task2.ID, "2", []BuilderProposedFile{{Path: "b.go", Action: "create", Content: "B"}})
	agg2Resp := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{"task_id":"`+task2.ID+`"}`)
	var agg2 CortexAggregateProposal
	json.Unmarshal(agg2Resp.Body.Bytes(), &agg2)
	rv2 := cortexFirstReviewerLane(t, task2)
	postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"aggregate_id":"`+agg2.ID+`","lane_id":"`+rv2.ID+`"}`)

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/aggregate/reviews?task_id="+task1.ID, nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	var reviews []CortexAggregateReview
	json.Unmarshal(w.Body.Bytes(), &reviews)
	if len(reviews) != 1 || reviews[0].TaskID != task1.ID {
		t.Fatalf("expected 1 review for task1, got %v", reviews)
	}
}

func TestWorkbenchCortexAggregateReviewsFilterByLane(t *testing.T) {
	server := startMockProvider(t, reviewResponse("approve", "ok", nil, nil))
	wb := New()
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"k","model":"m"}`)
	task := createCortexTask(t, wb, `{"goal":"g","roles":{"builder":1,"reviewer":2}}`)
	seedLaneProposal(t, wb, task.ID, "1", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A"}})
	postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)

	reviewers := cortexReviewerLanes(task)
	if len(reviewers) < 2 {
		t.Fatalf("expected >=2 reviewer lanes, got %d", len(reviewers))
	}
	for _, rv := range reviewers {
		postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"lane_id":"`+rv.ID+`"}`)
	}
	target := reviewers[0]
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/aggregate/reviews?lane_id="+target.ID, nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	var reviews []CortexAggregateReview
	json.Unmarshal(w.Body.Bytes(), &reviews)
	if len(reviews) != 1 || reviews[0].LaneID != target.ID {
		t.Fatalf("expected 1 review for lane %s, got %v", target.ID, reviews)
	}
}

func TestWorkbenchCortexAggregateReviewEvents(t *testing.T) {
	wb, task, _ := cortexReviewSetup(t, reviewResponse("approve", "ok", nil, nil))
	rv := cortexFirstReviewerLane(t, task)
	if w := postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"lane_id":"`+rv.ID+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("created: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if ev := cortexHasEvent(wb, "cortex.aggregate.review.created"); ev == nil {
		t.Error("cortex.aggregate.review.created event not appended")
	} else if ev.Message != "Created Cortex aggregate review" {
		t.Errorf("created event message: %q", ev.Message)
	}

	wb2, task2, _ := cortexReviewSetup(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	rv2 := cortexFirstReviewerLane(t, task2)
	if w := postJSON(t, wb2, "/api/workbench/cortex/aggregate/review", `{"lane_id":"`+rv2.ID+`"}`); w.Code != http.StatusBadGateway {
		t.Fatalf("failed: expected 502, got %d: %s", w.Code, w.Body.String())
	}
	if ev := cortexHasEvent(wb2, "cortex.aggregate.review.failed"); ev == nil {
		t.Error("cortex.aggregate.review.failed event not appended")
	} else if ev.Message != "Failed Cortex aggregate review" {
		t.Errorf("failed event message: %q", ev.Message)
	}
}

func TestWorkbenchCortexAggregateReviewNoAPIKey(t *testing.T) {
	const secret = "review-secret"
	server := startMockProvider(t, reviewResponse("approve", "ok", []string{"r"}, []string{"rec"}))
	wb := New()
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"`+secret+`","model":"m"}`)
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	seedLaneProposal(t, wb, task.ID, "2", []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A"}})
	postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	rv := cortexFirstReviewerLane(t, task)

	post := postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"lane_id":"`+rv.ID+`"}`)
	if post.Code != http.StatusCreated {
		t.Fatalf("review: expected 201, got %d: %s", post.Code, post.Body.String())
	}
	assertNoSecret(t, "review", secret, post.Body.Bytes())

	for _, path := range []string{"/api/workbench/cortex/aggregate/review", "/api/workbench/cortex/aggregate/reviews"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		wb.mux.ServeHTTP(rec, req)
		assertNoSecret(t, path, secret, rec.Body.Bytes())
	}
	for _, e := range wb.store.List() {
		if strings.HasPrefix(e.Type, "cortex.") {
			assertNoSecret(t, "event "+e.Type, secret, e.Data)
		}
	}
}

func TestWorkbenchCortexAggregateReviewDoesNotWriteFiles(t *testing.T) {
	dir := t.TempDir()
	server := startMockProvider(t, reviewResponse("approve", "ok", nil, nil))
	wb := New()
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"k","model":"m"}`)
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	seedLaneProposal(t, wb, task.ID, "2", []BuilderProposedFile{{Path: "newfile.go", Action: "create", Content: "package x\n"}})
	postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	rv := cortexFirstReviewerLane(t, task)

	before := snapshotDir(t, dir)
	if w := postJSON(t, wb, "/api/workbench/cortex/aggregate/review", `{"lane_id":"`+rv.ID+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("review: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	after := snapshotDir(t, dir)

	if len(before) != len(after) {
		t.Fatalf("file count changed: %d -> %d", len(before), len(after))
	}
	if _, err := os.Stat(filepath.Join(dir, "newfile.go")); !os.IsNotExist(err) {
		t.Errorf("review must not write proposed file newfile.go (err=%v)", err)
	}
}

// cortexApplyApprovedSetup opens projectDir, builds an aggregated aggregate from
// files, creates a Lockbox request from it and approves it. It returns the
// workbench (with the approved request current) and the aggregate.
func cortexApplyApprovedSetup(t *testing.T, projectDir string, files []BuilderProposedFile) (*Server, CortexAggregateProposal) {
	t.Helper()
	wb := New()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+projectDir+`"}`)
	task := createCortexTask(t, wb, `{"goal":"ship it","mode":"multi"}`)
	seedLaneProposal(t, wb, task.ID, "2", files)
	aggResp := postJSON(t, wb, "/api/workbench/cortex/aggregate", `{}`)
	if aggResp.Code != http.StatusCreated {
		t.Fatalf("aggregate: expected 201, got %d: %s", aggResp.Code, aggResp.Body.String())
	}
	var agg CortexAggregateProposal
	if err := json.Unmarshal(aggResp.Body.Bytes(), &agg); err != nil {
		t.Fatalf("decode aggregate: %v", err)
	}
	lbResp := postJSON(t, wb, "/api/workbench/cortex/aggregate/lockbox", `{}`)
	if lbResp.Code != http.StatusCreated {
		t.Fatalf("aggregate/lockbox: expected 201, got %d: %s", lbResp.Code, lbResp.Body.String())
	}
	var request LockboxApprovalRequest
	if err := json.Unmarshal(lbResp.Body.Bytes(), &request); err != nil {
		t.Fatalf("decode lockbox request: %v", err)
	}
	if app := postJSON(t, wb, "/api/workbench/lockbox/approve", `{"id":"`+request.ID+`"}`); app.Code != http.StatusOK {
		t.Fatalf("approve: expected 200, got %d: %s", app.Code, app.Body.String())
	}
	return wb, agg
}

func TestCortexApplySafePathRejectsEmpty(t *testing.T) {
	if _, err := resolveProjectPath(t.TempDir(), ""); err == nil {
		t.Error("expected error for empty path")
	}
}

func TestCortexApplySafePathRejectsAbsolute(t *testing.T) {
	if _, err := resolveProjectPath(t.TempDir(), "/etc/passwd"); err == nil {
		t.Error("expected error for absolute path")
	}
}

func TestCortexApplySafePathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := resolveProjectPath(root, "../outside.txt"); err == nil {
		t.Error("expected error for parent traversal")
	}
	if _, err := resolveProjectPath(root, "a/../../b.txt"); err == nil {
		t.Error("expected error for nested traversal escape")
	}
}

func TestCortexApplySafePathAllowsNested(t *testing.T) {
	root := t.TempDir()
	got, err := resolveProjectPath(root, "a/b/c.go")
	if err != nil {
		t.Fatalf("unexpected error for nested path: %v", err)
	}
	want := filepath.Join(root, "a", "b", "c.go")
	if got != want {
		t.Errorf("resolved=%q, want %q", got, want)
	}
}

func TestWorkbenchCortexApply404NoLockboxRequest(t *testing.T) {
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	w := postJSON(t, wb, "/api/workbench/cortex/apply", `{}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyRejectsPending(t *testing.T) {
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	agg := wb.cortexAggregates.Append(CortexAggregateProposal{Status: "aggregated", Files: []BuilderProposedFile{}})
	wb.lockbox.Append(LockboxApprovalRequest{ProposalID: "aggregate:" + agg.ID, Status: "pending", Files: []BuilderProposedFile{}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyRejectsRejected(t *testing.T) {
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	agg := wb.cortexAggregates.Append(CortexAggregateProposal{Status: "aggregated", Files: []BuilderProposedFile{}})
	wb.lockbox.Append(LockboxApprovalRequest{ProposalID: "aggregate:" + agg.ID, Status: "rejected", Files: []BuilderProposedFile{}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyRejectsNonAggregateProposal(t *testing.T) {
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	wb.lockbox.Append(LockboxApprovalRequest{ProposalID: "1", Status: "approved", Files: []BuilderProposedFile{}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApply404MissingAggregate(t *testing.T) {
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	wb.lockbox.Append(LockboxApprovalRequest{ProposalID: "aggregate:nonexistent", Status: "approved", Files: []BuilderProposedFile{}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply", `{}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyRejectsConflictedAggregate(t *testing.T) {
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	agg := wb.cortexAggregates.Append(CortexAggregateProposal{Status: "conflicted", Files: []BuilderProposedFile{}})
	wb.lockbox.Append(LockboxApprovalRequest{ProposalID: "aggregate:" + agg.ID, Status: "approved", Files: []BuilderProposedFile{}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyRejectsWithoutProject(t *testing.T) {
	wb := New()
	agg := wb.cortexAggregates.Append(CortexAggregateProposal{Status: "aggregated", Files: []BuilderProposedFile{}})
	wb.lockbox.Append(LockboxApprovalRequest{ProposalID: "aggregate:" + agg.ID, Status: "approved", Files: []BuilderProposedFile{}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyCreatesFile(t *testing.T) {
	dir := t.TempDir()
	wb, agg := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "newfile.go", Action: "create", Content: "package x\n"}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var result CortexApplyResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Status != "applied" {
		t.Errorf("status=%q, want applied", result.Status)
	}
	if result.AggregateID != agg.ID {
		t.Errorf("aggregate_id=%q, want %q", result.AggregateID, agg.ID)
	}
	if len(result.Files) != 1 || result.Files[0].Status != "applied" || result.Files[0].Action != "create" {
		t.Fatalf("unexpected applied files: %+v", result.Files)
	}
	content, err := os.ReadFile(filepath.Join(dir, "newfile.go"))
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if string(content) != "package x\n" {
		t.Errorf("content=%q", content)
	}
}

func TestWorkbenchCortexApplyModifiesFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "existing.go"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "existing.go", Action: "modify", Content: "new\n"}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	content, err := os.ReadFile(filepath.Join(dir, "existing.go"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(content) != "new\n" {
		t.Errorf("content=%q, want %q", content, "new\n")
	}
}

func TestWorkbenchCortexApplyDeletesFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gone.go"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "gone.go", Action: "delete", Content: ""}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.go")); !os.IsNotExist(err) {
		t.Errorf("file should be deleted (err=%v)", err)
	}
}

func TestWorkbenchCortexApplyRejectsModifyMissing(t *testing.T) {
	dir := t.TempDir()
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "missing.go", Action: "modify", Content: "x\n"}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply", `{}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	var result CortexApplyResult
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.Status != "failed" {
		t.Errorf("status=%q, want failed", result.Status)
	}
	if len(result.Files) != 1 || result.Files[0].Status != "failed" {
		t.Errorf("expected one failed file, got %+v", result.Files)
	}
}

func TestWorkbenchCortexApplyRejectsDeleteMissing(t *testing.T) {
	dir := t.TempDir()
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "missing.go", Action: "delete", Content: ""}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply", `{}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyRejectsUnsupportedAction(t *testing.T) {
	dir := t.TempDir()
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "a.go", Action: "rename", Content: "x\n"}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply", `{}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	var result CortexApplyResult
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.Status != "failed" {
		t.Errorf("status=%q, want failed", result.Status)
	}
}

func TestWorkbenchCortexApplyStopsOnFirstFailure(t *testing.T) {
	dir := t.TempDir()
	files := []BuilderProposedFile{
		{Path: "good.go", Action: "create", Content: "g\n"},
		{Path: "missing.go", Action: "modify", Content: "m\n"},
		{Path: "other.go", Action: "create", Content: "o\n"},
	}
	wb, _ := cortexApplyApprovedSetup(t, dir, files)
	w := postJSON(t, wb, "/api/workbench/cortex/apply", `{}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	var result CortexApplyResult
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.Status != "failed" {
		t.Errorf("status=%q, want failed", result.Status)
	}
	if len(result.Files) != 2 {
		t.Fatalf("expected 2 attempted files (stopped on failure), got %d: %+v", len(result.Files), result.Files)
	}
	if result.Files[0].Status != "applied" || result.Files[1].Status != "failed" {
		t.Errorf("unexpected file statuses: %+v", result.Files)
	}
	if _, err := os.Stat(filepath.Join(dir, "good.go")); err != nil {
		t.Errorf("good.go should be created before the failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "other.go")); !os.IsNotExist(err) {
		t.Errorf("other.go must not be created after stopping (err=%v)", err)
	}
}

func TestWorkbenchCortexApplyCompletedEvent(t *testing.T) {
	dir := t.TempDir()
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A\n"}})
	if w := postJSON(t, wb, "/api/workbench/cortex/apply", `{}`); w.Code != http.StatusOK {
		t.Fatalf("apply: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ev := cortexHasEvent(wb, "cortex.apply.completed"); ev == nil {
		t.Error("cortex.apply.completed event not appended")
	} else if ev.Message != "Applied Cortex aggregate" {
		t.Errorf("event message: %q", ev.Message)
	}
}

func TestWorkbenchCortexApplyFailedEvent(t *testing.T) {
	dir := t.TempDir()
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "missing.go", Action: "modify", Content: "x\n"}})
	if w := postJSON(t, wb, "/api/workbench/cortex/apply", `{}`); w.Code != http.StatusBadGateway {
		t.Fatalf("apply: expected 502, got %d: %s", w.Code, w.Body.String())
	}
	if ev := cortexHasEvent(wb, "cortex.apply.failed"); ev == nil {
		t.Error("cortex.apply.failed event not appended")
	} else if ev.Message != "Failed to apply Cortex aggregate" {
		t.Errorf("event message: %q", ev.Message)
	}
}

func TestWorkbenchCortexApplyGET404(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/apply", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyGETCurrent(t *testing.T) {
	dir := t.TempDir()
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A\n"}})
	created := postJSON(t, wb, "/api/workbench/cortex/apply", `{}`)
	if created.Code != http.StatusOK {
		t.Fatalf("apply: expected 200, got %d: %s", created.Code, created.Body.String())
	}
	var createdResult CortexApplyResult
	json.Unmarshal(created.Body.Bytes(), &createdResult)

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/apply", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var result CortexApplyResult
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.ID != createdResult.ID {
		t.Errorf("current result id=%q, want %q", result.ID, createdResult.ID)
	}
}

func TestWorkbenchCortexAppliesEmpty(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/applies", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := strings.TrimSpace(w.Body.String()); body != "[]" {
		t.Errorf("expected [] for empty applies, got %q", body)
	}
}

func TestWorkbenchCortexAppliesFilterByAggregate(t *testing.T) {
	wb := New()
	wb.cortexApplies.Append(CortexApplyResult{AggregateID: "agg-1", Status: "applied", Files: []CortexAppliedFile{}})
	wb.cortexApplies.Append(CortexApplyResult{AggregateID: "agg-2", Status: "applied", Files: []CortexAppliedFile{}})

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/applies?aggregate_id=agg-1", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	var filtered []CortexApplyResult
	json.Unmarshal(w.Body.Bytes(), &filtered)
	if len(filtered) != 1 || filtered[0].AggregateID != "agg-1" {
		t.Fatalf("expected 1 result for agg-1, got %v", filtered)
	}

	reqAll := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/applies", nil)
	wAll := httptest.NewRecorder()
	wb.mux.ServeHTTP(wAll, reqAll)
	var all []CortexApplyResult
	json.Unmarshal(wAll.Body.Bytes(), &all)
	if len(all) != 2 {
		t.Errorf("expected 2 total results, got %d", len(all))
	}
}

func TestWorkbenchCortexApplyNoAPIKey(t *testing.T) {
	const secret = "apply-secret"
	dir := t.TempDir()
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A\n"}})
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"https://api.example.com","api_key":"`+secret+`","model":"m"}`)

	post := postJSON(t, wb, "/api/workbench/cortex/apply", `{}`)
	if post.Code != http.StatusOK {
		t.Fatalf("apply: expected 200, got %d: %s", post.Code, post.Body.String())
	}
	assertNoSecret(t, "apply", secret, post.Body.Bytes())

	for _, path := range []string{"/api/workbench/cortex/apply", "/api/workbench/cortex/applies"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		wb.mux.ServeHTTP(rec, req)
		assertNoSecret(t, path, secret, rec.Body.Bytes())
	}
	for _, e := range wb.store.List() {
		if strings.HasPrefix(e.Type, "cortex.apply") {
			assertNoSecret(t, "event "+e.Type, secret, e.Data)
		}
	}
}

func previewFileByPath(preview CortexApplyPreview, path string) (CortexApplyPreviewFile, bool) {
	for _, pf := range preview.Files {
		if pf.Path == path {
			return pf, true
		}
	}
	return CortexApplyPreviewFile{}, false
}

func TestWorkbenchCortexApplyPreview404NoLockboxRequest(t *testing.T) {
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	w := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyPreviewRejectsPending(t *testing.T) {
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	agg := wb.cortexAggregates.Append(CortexAggregateProposal{Status: "aggregated", Files: []BuilderProposedFile{}})
	wb.lockbox.Append(LockboxApprovalRequest{ProposalID: "aggregate:" + agg.ID, Status: "pending", Files: []BuilderProposedFile{}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyPreviewRejectsRejected(t *testing.T) {
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	agg := wb.cortexAggregates.Append(CortexAggregateProposal{Status: "aggregated", Files: []BuilderProposedFile{}})
	wb.lockbox.Append(LockboxApprovalRequest{ProposalID: "aggregate:" + agg.ID, Status: "rejected", Files: []BuilderProposedFile{}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyPreviewRejectsNonAggregateProposal(t *testing.T) {
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	wb.lockbox.Append(LockboxApprovalRequest{ProposalID: "1", Status: "approved", Files: []BuilderProposedFile{}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyPreview404MissingAggregate(t *testing.T) {
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	wb.lockbox.Append(LockboxApprovalRequest{ProposalID: "aggregate:nonexistent", Status: "approved", Files: []BuilderProposedFile{}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyPreviewRejectsConflictedAggregate(t *testing.T) {
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	agg := wb.cortexAggregates.Append(CortexAggregateProposal{Status: "conflicted", Files: []BuilderProposedFile{}})
	wb.lockbox.Append(LockboxApprovalRequest{ProposalID: "aggregate:" + agg.ID, Status: "approved", Files: []BuilderProposedFile{}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyPreviewRejectsWithoutProject(t *testing.T) {
	wb := New()
	agg := wb.cortexAggregates.Append(CortexAggregateProposal{Status: "aggregated", Files: []BuilderProposedFile{}})
	wb.lockbox.Append(LockboxApprovalRequest{ProposalID: "aggregate:" + agg.ID, Status: "approved", Files: []BuilderProposedFile{}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyPreviewCreateNew(t *testing.T) {
	dir := t.TempDir()
	wb, agg := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "newfile.go", Action: "create", Content: "package x\n"}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var preview CortexApplyPreview
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if preview.Status != "ready" {
		t.Errorf("status=%q, want ready", preview.Status)
	}
	if preview.AggregateID != agg.ID {
		t.Errorf("aggregate_id=%q, want %q", preview.AggregateID, agg.ID)
	}
	pf, ok := previewFileByPath(preview, "newfile.go")
	if !ok {
		t.Fatal("missing preview file for newfile.go")
	}
	if pf.Status != "ready" || pf.Exists {
		t.Errorf("file preview=%+v, want ready and not existing", pf)
	}
	if !strings.Contains(pf.Diff, "+++ b/newfile.go") || !strings.Contains(pf.Diff, "+package x") {
		t.Errorf("unexpected diff: %q", pf.Diff)
	}
	if _, err := os.Stat(filepath.Join(dir, "newfile.go")); !os.IsNotExist(err) {
		t.Errorf("preview must not write the file (err=%v)", err)
	}
}

func TestWorkbenchCortexApplyPreviewCreateIdentical(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("same\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "same\n"}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (ready), got %d: %s", w.Code, w.Body.String())
	}
	var preview CortexApplyPreview
	json.Unmarshal(w.Body.Bytes(), &preview)
	if preview.Status != "ready" {
		t.Errorf("status=%q, want ready", preview.Status)
	}
	pf, _ := previewFileByPath(preview, "a.go")
	if pf.Status != "ready" || !pf.Exists {
		t.Errorf("file preview=%+v, want ready and existing", pf)
	}
	if !strings.Contains(pf.Diff, "no-op") {
		t.Errorf("expected no-op diff, got %q", pf.Diff)
	}
}

func TestWorkbenchCortexApplyPreviewCreateDifferent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "new\n"}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 (blocked), got %d: %s", w.Code, w.Body.String())
	}
	var preview CortexApplyPreview
	json.Unmarshal(w.Body.Bytes(), &preview)
	if preview.Status != "blocked" {
		t.Errorf("status=%q, want blocked", preview.Status)
	}
	pf, _ := previewFileByPath(preview, "a.go")
	if pf.Status != "blocked" || pf.Error == "" {
		t.Errorf("file preview=%+v, want blocked with error", pf)
	}
}

func TestWorkbenchCortexApplyPreviewModifyExisting(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "existing.go"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "existing.go", Action: "modify", Content: "new\n"}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var preview CortexApplyPreview
	json.Unmarshal(w.Body.Bytes(), &preview)
	pf, _ := previewFileByPath(preview, "existing.go")
	if pf.Status != "ready" || !pf.Exists {
		t.Errorf("file preview=%+v, want ready and existing", pf)
	}
	if !strings.Contains(pf.Diff, "-old") || !strings.Contains(pf.Diff, "+new") {
		t.Errorf("expected modify diff, got %q", pf.Diff)
	}
	content, _ := os.ReadFile(filepath.Join(dir, "existing.go"))
	if string(content) != "old\n" {
		t.Errorf("preview must not modify the file, got %q", content)
	}
}

func TestWorkbenchCortexApplyPreviewModifyMissing(t *testing.T) {
	dir := t.TempDir()
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "missing.go", Action: "modify", Content: "x\n"}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	var preview CortexApplyPreview
	json.Unmarshal(w.Body.Bytes(), &preview)
	pf, _ := previewFileByPath(preview, "missing.go")
	if pf.Status != "blocked" {
		t.Errorf("file status=%q, want blocked", pf.Status)
	}
}

func TestWorkbenchCortexApplyPreviewDeleteExisting(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gone.go"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "gone.go", Action: "delete", Content: ""}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var preview CortexApplyPreview
	json.Unmarshal(w.Body.Bytes(), &preview)
	pf, _ := previewFileByPath(preview, "gone.go")
	if pf.Status != "ready" || !pf.Exists {
		t.Errorf("file preview=%+v, want ready and existing", pf)
	}
	if !strings.Contains(pf.Diff, "-x") {
		t.Errorf("expected delete diff with removals, got %q", pf.Diff)
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.go")); err != nil {
		t.Errorf("preview must not delete the file: %v", err)
	}
}

func TestWorkbenchCortexApplyPreviewDeleteMissing(t *testing.T) {
	dir := t.TempDir()
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "missing.go", Action: "delete", Content: ""}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	var preview CortexApplyPreview
	json.Unmarshal(w.Body.Bytes(), &preview)
	pf, _ := previewFileByPath(preview, "missing.go")
	if pf.Status != "blocked" {
		t.Errorf("file status=%q, want blocked", pf.Status)
	}
}

func TestWorkbenchCortexApplyPreviewUnsupportedAction(t *testing.T) {
	dir := t.TempDir()
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "a.go", Action: "rename", Content: "x\n"}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	var preview CortexApplyPreview
	json.Unmarshal(w.Body.Bytes(), &preview)
	pf, _ := previewFileByPath(preview, "a.go")
	if pf.Status != "blocked" || pf.Error == "" {
		t.Errorf("file preview=%+v, want blocked with error", pf)
	}
}

func TestWorkbenchCortexApplyPreviewEvaluatesAllFiles(t *testing.T) {
	dir := t.TempDir()
	files := []BuilderProposedFile{
		{Path: "new.go", Action: "create", Content: "n\n"},
		{Path: "missing.go", Action: "modify", Content: "m\n"},
		{Path: "absent.go", Action: "delete", Content: ""},
	}
	wb, _ := cortexApplyApprovedSetup(t, dir, files)
	w := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 (some blocked), got %d: %s", w.Code, w.Body.String())
	}
	var preview CortexApplyPreview
	json.Unmarshal(w.Body.Bytes(), &preview)
	if preview.Status != "blocked" {
		t.Errorf("status=%q, want blocked", preview.Status)
	}
	if len(preview.Files) != 3 {
		t.Fatalf("expected all 3 files evaluated, got %d", len(preview.Files))
	}
	statuses := map[string]string{}
	for _, pf := range preview.Files {
		statuses[pf.Path] = pf.Status
	}
	if statuses["new.go"] != "ready" {
		t.Errorf("new.go status=%q, want ready", statuses["new.go"])
	}
	if statuses["missing.go"] != "blocked" || statuses["absent.go"] != "blocked" {
		t.Errorf("expected missing.go and absent.go blocked, got %v", statuses)
	}
}

func TestWorkbenchCortexApplyPreviewReadyEvent(t *testing.T) {
	dir := t.TempDir()
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A\n"}})
	if w := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`); w.Code != http.StatusOK {
		t.Fatalf("preview: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ev := cortexHasEvent(wb, "cortex.apply.preview.created"); ev == nil {
		t.Error("cortex.apply.preview.created event not appended")
	} else if ev.Message != "Created Cortex apply preview" {
		t.Errorf("event message: %q", ev.Message)
	}
}

func TestWorkbenchCortexApplyPreviewBlockedEvent(t *testing.T) {
	dir := t.TempDir()
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "missing.go", Action: "modify", Content: "m\n"}})
	if w := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`); w.Code != http.StatusConflict {
		t.Fatalf("preview: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if ev := cortexHasEvent(wb, "cortex.apply.preview.created"); ev == nil {
		t.Error("cortex.apply.preview.created event not appended for blocked preview")
	}
}

func TestWorkbenchCortexApplyPreviewGET404(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/apply/preview", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyPreviewGETCurrent(t *testing.T) {
	dir := t.TempDir()
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A\n"}})
	created := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`)
	var createdPreview CortexApplyPreview
	json.Unmarshal(created.Body.Bytes(), &createdPreview)

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/apply/preview", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var preview CortexApplyPreview
	json.Unmarshal(w.Body.Bytes(), &preview)
	if preview.ID != createdPreview.ID {
		t.Errorf("current preview id=%q, want %q", preview.ID, createdPreview.ID)
	}
}

func TestWorkbenchCortexApplyPreviewsEmpty(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/apply/previews", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := strings.TrimSpace(w.Body.String()); body != "[]" {
		t.Errorf("expected [] for empty previews, got %q", body)
	}
}

func TestWorkbenchCortexApplyPreviewsFilterByAggregate(t *testing.T) {
	wb := New()
	wb.cortexApplyPreviews.Append(CortexApplyPreview{AggregateID: "agg-1", Status: "ready", Files: []CortexApplyPreviewFile{}})
	wb.cortexApplyPreviews.Append(CortexApplyPreview{AggregateID: "agg-2", Status: "ready", Files: []CortexApplyPreviewFile{}})

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/apply/previews?aggregate_id=agg-1", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	var filtered []CortexApplyPreview
	json.Unmarshal(w.Body.Bytes(), &filtered)
	if len(filtered) != 1 || filtered[0].AggregateID != "agg-1" {
		t.Fatalf("expected 1 preview for agg-1, got %v", filtered)
	}

	reqAll := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/apply/previews", nil)
	wAll := httptest.NewRecorder()
	wb.mux.ServeHTTP(wAll, reqAll)
	var all []CortexApplyPreview
	json.Unmarshal(wAll.Body.Bytes(), &all)
	if len(all) != 2 {
		t.Errorf("expected 2 total previews, got %d", len(all))
	}
}

func TestWorkbenchCortexApplyPreviewNoAPIKey(t *testing.T) {
	const secret = "apply-preview-secret"
	dir := t.TempDir()
	wb, _ := cortexApplyApprovedSetup(t, dir, []BuilderProposedFile{{Path: "a.go", Action: "create", Content: "A\n"}})
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"https://api.example.com","api_key":"`+secret+`","model":"m"}`)

	post := postJSON(t, wb, "/api/workbench/cortex/apply/preview", `{}`)
	if post.Code != http.StatusOK {
		t.Fatalf("preview: expected 200, got %d: %s", post.Code, post.Body.String())
	}
	assertNoSecret(t, "apply preview", secret, post.Body.Bytes())

	for _, path := range []string{"/api/workbench/cortex/apply/preview", "/api/workbench/cortex/apply/previews"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		wb.mux.ServeHTTP(rec, req)
		assertNoSecret(t, path, secret, rec.Body.Bytes())
	}
	for _, e := range wb.store.List() {
		if strings.HasPrefix(e.Type, "cortex.apply.preview") {
			assertNoSecret(t, "event "+e.Type, secret, e.Data)
		}
	}
}

// cortexValidationSetup builds a workbench with a project open and one applied
// apply result current, ready for a validation run.
func cortexValidationSetup(t *testing.T) (*Server, string, CortexApplyResult) {
	t.Helper()
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	applyResult := wb.cortexApplies.Append(CortexApplyResult{AggregateID: "agg-1", Status: "applied", Files: []CortexAppliedFile{}})
	return wb, dir, applyResult
}

// buildValidatorBinary compiles a tiny program that writes the given stdout and
// stderr and exits with exitCode, returning the executable path. It is run as a
// validation command (single token, no shell needed).
func buildValidatorBinary(t *testing.T, stdoutMsg, stderrMsg string, exitCode int) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module validatorbin\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf(`package main

import (
	"fmt"
	"os"
)

func main() {
	if s := %q; s != "" {
		fmt.Fprint(os.Stdout, s)
	}
	if s := %q; s != "" {
		fmt.Fprint(os.Stderr, s)
	}
	os.Exit(%d)
}
`, stdoutMsg, stderrMsg, exitCode)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "vbin")
	cmd := exec.Command("go", "build", "-o", exe, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build validator: %v\n%s", err, out)
	}
	return exe
}

func postValidateCommand(t *testing.T, wb *Server, command string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	return postJSON(t, wb, "/api/workbench/cortex/apply/validate", string(body))
}

func TestWorkbenchCortexApplyValidate404NoApply(t *testing.T) {
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	w := postJSON(t, wb, "/api/workbench/cortex/apply/validate", `{}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyValidateRejectsFailedApply(t *testing.T) {
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	wb.cortexApplies.Append(CortexApplyResult{Status: "failed", Files: []CortexAppliedFile{}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply/validate", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyValidateRejectsWithoutProject(t *testing.T) {
	wb := New()
	wb.cortexApplies.Append(CortexApplyResult{Status: "applied", Files: []CortexAppliedFile{}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply/validate", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyValidateRejectsNoCommandNoHints(t *testing.T) {
	wb, _, _ := cortexValidationSetup(t)
	w := postJSON(t, wb, "/api/workbench/cortex/apply/validate", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyValidateExplicitCommand(t *testing.T) {
	exe := buildValidatorBinary(t, "ok", "", 0)
	wb, _, apply := cortexValidationSetup(t)
	w := postValidateCommand(t, wb, exe)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var v CortexApplyValidation
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.Status != "passed" {
		t.Errorf("status=%q, want passed", v.Status)
	}
	if v.Command != exe {
		t.Errorf("command=%q, want %q", v.Command, exe)
	}
	if v.ApplyID != apply.ID {
		t.Errorf("apply_id=%q, want %q", v.ApplyID, apply.ID)
	}
	if v.AggregateID != apply.AggregateID {
		t.Errorf("aggregate_id=%q, want %q", v.AggregateID, apply.AggregateID)
	}
}

func TestWorkbenchCortexApplyValidateUsesTestHint(t *testing.T) {
	exe := buildValidatorBinary(t, "", "", 0)
	wb, dir, _ := cortexValidationSetup(t)
	wb.inspection.Set(ProjectInspection{Project: ProjectState{Path: dir}, TestHints: []string{exe}})
	w := postJSON(t, wb, "/api/workbench/cortex/apply/validate", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var v CortexApplyValidation
	json.Unmarshal(w.Body.Bytes(), &v)
	if v.Command != exe {
		t.Errorf("command=%q, want first test hint %q", v.Command, exe)
	}
	if v.Status != "passed" {
		t.Errorf("status=%q, want passed", v.Status)
	}
}

func TestWorkbenchCortexApplyValidatePassed(t *testing.T) {
	exe := buildValidatorBinary(t, "", "", 0)
	wb, _, _ := cortexValidationSetup(t)
	w := postValidateCommand(t, wb, exe)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var v CortexApplyValidation
	json.Unmarshal(w.Body.Bytes(), &v)
	if v.Status != "passed" || v.ExitCode != 0 {
		t.Errorf("status=%q exit=%d, want passed/0", v.Status, v.ExitCode)
	}
}

func TestWorkbenchCortexApplyValidateFailed(t *testing.T) {
	exe := buildValidatorBinary(t, "", "boom", 3)
	wb, _, _ := cortexValidationSetup(t)
	w := postValidateCommand(t, wb, exe)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	var v CortexApplyValidation
	json.Unmarshal(w.Body.Bytes(), &v)
	if v.Status != "failed" {
		t.Errorf("status=%q, want failed", v.Status)
	}
	if v.ExitCode != 3 {
		t.Errorf("exit_code=%d, want 3", v.ExitCode)
	}
}

func TestWorkbenchCortexApplyValidateErrorMissingExecutable(t *testing.T) {
	wb, _, _ := cortexValidationSetup(t)
	w := postValidateCommand(t, wb, "hirdforge-no-such-validator-binary-xyz")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	var v CortexApplyValidation
	json.Unmarshal(w.Body.Bytes(), &v)
	if v.Status != "error" {
		t.Errorf("status=%q, want error", v.Status)
	}
	if v.Error == "" {
		t.Error("expected error to be populated")
	}
}

func TestWorkbenchCortexApplyValidateCapturesOutput(t *testing.T) {
	exe := buildValidatorBinary(t, "STDOUT-MARK", "STDERR-MARK", 0)
	wb, _, _ := cortexValidationSetup(t)
	w := postValidateCommand(t, wb, exe)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var v CortexApplyValidation
	json.Unmarshal(w.Body.Bytes(), &v)
	if !strings.Contains(v.Stdout, "STDOUT-MARK") {
		t.Errorf("stdout=%q, want STDOUT-MARK", v.Stdout)
	}
	if !strings.Contains(v.Stderr, "STDERR-MARK") {
		t.Errorf("stderr=%q, want STDERR-MARK", v.Stderr)
	}
}

func TestWorkbenchCortexApplyValidateCapsOutput(t *testing.T) {
	big := strings.Repeat("x", cortexValidationOutputCap+1000)
	exe := buildValidatorBinary(t, big, "", 0)
	wb, _, _ := cortexValidationSetup(t)
	w := postValidateCommand(t, wb, exe)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var v CortexApplyValidation
	json.Unmarshal(w.Body.Bytes(), &v)
	if len(v.Stdout) != cortexValidationOutputCap {
		t.Errorf("stdout len=%d, want capped to %d", len(v.Stdout), cortexValidationOutputCap)
	}
}

func TestWorkbenchCortexApplyValidationCompletedEvent(t *testing.T) {
	exe := buildValidatorBinary(t, "", "", 0)
	wb, _, _ := cortexValidationSetup(t)
	if w := postValidateCommand(t, wb, exe); w.Code != http.StatusOK {
		t.Fatalf("validate: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ev := cortexHasEvent(wb, "cortex.apply.validation.completed"); ev == nil {
		t.Error("cortex.apply.validation.completed event not appended")
	} else if ev.Message != "Completed Cortex apply validation" {
		t.Errorf("event message: %q", ev.Message)
	}
}

func TestWorkbenchCortexApplyValidationGET404(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/apply/validation", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchCortexApplyValidationGETCurrent(t *testing.T) {
	exe := buildValidatorBinary(t, "", "", 0)
	wb, _, _ := cortexValidationSetup(t)
	created := postValidateCommand(t, wb, exe)
	var createdV CortexApplyValidation
	json.Unmarshal(created.Body.Bytes(), &createdV)

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/apply/validation", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var v CortexApplyValidation
	json.Unmarshal(w.Body.Bytes(), &v)
	if v.ID != createdV.ID {
		t.Errorf("current validation id=%q, want %q", v.ID, createdV.ID)
	}
}

func TestWorkbenchCortexApplyValidationsEmpty(t *testing.T) {
	wb := New()
	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/apply/validations", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := strings.TrimSpace(w.Body.String()); body != "[]" {
		t.Errorf("expected [] for empty validations, got %q", body)
	}
}

func TestWorkbenchCortexApplyValidationsFilterByApply(t *testing.T) {
	wb := New()
	wb.cortexValidations.Append(CortexApplyValidation{ApplyID: "ap-1", AggregateID: "agg-1", Status: "passed"})
	wb.cortexValidations.Append(CortexApplyValidation{ApplyID: "ap-2", AggregateID: "agg-2", Status: "passed"})

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/apply/validations?apply_id=ap-1", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	var filtered []CortexApplyValidation
	json.Unmarshal(w.Body.Bytes(), &filtered)
	if len(filtered) != 1 || filtered[0].ApplyID != "ap-1" {
		t.Fatalf("expected 1 validation for ap-1, got %v", filtered)
	}

	reqAll := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/apply/validations", nil)
	wAll := httptest.NewRecorder()
	wb.mux.ServeHTTP(wAll, reqAll)
	var all []CortexApplyValidation
	json.Unmarshal(wAll.Body.Bytes(), &all)
	if len(all) != 2 {
		t.Errorf("expected 2 total validations, got %d", len(all))
	}
}

func TestWorkbenchCortexApplyValidationsFilterByAggregate(t *testing.T) {
	wb := New()
	wb.cortexValidations.Append(CortexApplyValidation{ApplyID: "ap-1", AggregateID: "agg-1", Status: "passed"})
	wb.cortexValidations.Append(CortexApplyValidation{ApplyID: "ap-2", AggregateID: "agg-2", Status: "passed"})

	req := httptest.NewRequest(http.MethodGet, "/api/workbench/cortex/apply/validations?aggregate_id=agg-2", nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	var filtered []CortexApplyValidation
	json.Unmarshal(w.Body.Bytes(), &filtered)
	if len(filtered) != 1 || filtered[0].AggregateID != "agg-2" {
		t.Fatalf("expected 1 validation for agg-2, got %v", filtered)
	}
}

func TestWorkbenchCortexApplyValidateNoAPIKey(t *testing.T) {
	const secret = "validation-secret"
	exe := buildValidatorBinary(t, "out", "err", 0)
	wb, _, _ := cortexValidationSetup(t)
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"https://api.example.com","api_key":"`+secret+`","model":"m"}`)

	post := postValidateCommand(t, wb, exe)
	if post.Code != http.StatusOK {
		t.Fatalf("validate: expected 200, got %d: %s", post.Code, post.Body.String())
	}
	assertNoSecret(t, "validate", secret, post.Body.Bytes())

	for _, path := range []string{"/api/workbench/cortex/apply/validation", "/api/workbench/cortex/apply/validations"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		wb.mux.ServeHTTP(rec, req)
		assertNoSecret(t, path, secret, rec.Body.Bytes())
	}
	for _, e := range wb.store.List() {
		if strings.HasPrefix(e.Type, "cortex.apply.validation") {
			assertNoSecret(t, "event "+e.Type, secret, e.Data)
		}
	}
}

// architectGET issues a GET against the workbench mux and returns the recorder.
func architectGET(t *testing.T, wb *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	wb.mux.ServeHTTP(w, req)
	return w
}

// architectResponse returns a handler that replies with an OpenAI-style
// envelope whose message content is the Architect JSON {message, spec}.
func architectResponse(message string, spec map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		content, _ := json.Marshal(map[string]any{"message": message, "spec": spec})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "test",
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": string(content)}},
			},
		})
	}
}

// newArchitectWorkbench builds a workbench with a non-git temp project open and,
// when handler is non-nil, a mock provider configured against it.
func newArchitectWorkbench(t *testing.T, handler http.HandlerFunc) (*Server, string) {
	t.Helper()
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	if handler != nil {
		server := startMockProvider(t, handler)
		postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"k","model":"m"}`)
	}
	return wb, dir
}

// createArchitectSession creates an active Architect session and returns it.
func createArchitectSession(t *testing.T, wb *Server, goal string) ArchitectSession {
	t.Helper()
	w := postJSON(t, wb, "/api/workbench/architect/session", `{"goal":"`+goal+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("architect session: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var s ArchitectSession
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode architect session: %v", err)
	}
	return s
}

func TestWorkbenchArchitectSessionRequiresProject(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/architect/session", `{"goal":"ship it"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchArchitectSessionRequiresGoal(t *testing.T) {
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	w := postJSON(t, wb, "/api/workbench/architect/session", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchArchitectSessionCreatesActive(t *testing.T) {
	wb, dir := newArchitectWorkbench(t, nil)
	w := postJSON(t, wb, "/api/workbench/architect/session", `{"goal":"ship it"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var s ArchitectSession
	json.Unmarshal(w.Body.Bytes(), &s)
	if s.ID == "" {
		t.Error("expected session id")
	}
	if s.Status != "active" {
		t.Errorf("status=%q, want active", s.Status)
	}
	if s.Spec.Goal != "ship it" {
		t.Errorf("spec goal=%q, want ship it", s.Spec.Goal)
	}
	if s.Project.Path != dir {
		t.Errorf("project path=%q, want %q", s.Project.Path, dir)
	}
	// Empty spec slices must serialize as [] rather than null.
	if body := w.Body.String(); !strings.Contains(body, `"constraints":[]`) {
		t.Errorf("expected empty constraints slice in %s", body)
	}
}

func TestWorkbenchArchitectSessionAppendsUserMessage(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, nil)
	s := createArchitectSession(t, wb, "ship it")
	if len(s.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(s.Messages))
	}
	if s.Messages[0].Role != "user" {
		t.Errorf("role=%q, want user", s.Messages[0].Role)
	}
	if s.Messages[0].Content != "ship it" {
		t.Errorf("content=%q, want ship it", s.Messages[0].Content)
	}
}

func TestWorkbenchArchitectSessionEventAppended(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, nil)
	createArchitectSession(t, wb, "ship it")
	if ev := cortexHasEvent(wb, "architect.session.created"); ev == nil {
		t.Error("architect.session.created event not appended")
	} else if ev.Message != "Created Architect session" {
		t.Errorf("event message: %q", ev.Message)
	}
}

func TestWorkbenchArchitectSessionGET404(t *testing.T) {
	wb := New()
	w := architectGET(t, wb, "/api/workbench/architect/session")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchArchitectSessionGETCurrent(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, nil)
	created := createArchitectSession(t, wb, "ship it")
	w := architectGET(t, wb, "/api/workbench/architect/session")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var s ArchitectSession
	json.Unmarshal(w.Body.Bytes(), &s)
	if s.ID != created.ID {
		t.Errorf("current session id=%q, want %q", s.ID, created.ID)
	}
	if s.Spec.Goal != "ship it" {
		t.Errorf("spec goal=%q, want ship it", s.Spec.Goal)
	}
}

func TestWorkbenchArchitectSessionsEmpty(t *testing.T) {
	wb := New()
	w := architectGET(t, wb, "/api/workbench/architect/sessions")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := strings.TrimSpace(w.Body.String()); body != "[]" {
		t.Errorf("expected [] for empty sessions, got %q", body)
	}
}

func TestWorkbenchArchitectSessionsIncludes(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, nil)
	createArchitectSession(t, wb, "first")
	createArchitectSession(t, wb, "second")
	w := architectGET(t, wb, "/api/workbench/architect/sessions")
	var list []ArchitectSession
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(list))
	}
	if list[0].Spec.Goal != "first" || list[1].Spec.Goal != "second" {
		t.Errorf("unexpected session order: %q, %q", list[0].Spec.Goal, list[1].Spec.Goal)
	}
}

func TestWorkbenchArchitectMessageRequiresSession(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/architect/message", `{"message":"hi"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchArchitectMessageRejectsAccepted(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, nil)
	createArchitectSession(t, wb, "ship it")
	if w := postJSON(t, wb, "/api/workbench/architect/accept", `{}`); w.Code != http.StatusOK {
		t.Fatalf("accept: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	w := postJSON(t, wb, "/api/workbench/architect/message", `{"message":"more"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchArchitectMessageRequiresProvider(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, nil) // no provider configured
	createArchitectSession(t, wb, "ship it")
	w := postJSON(t, wb, "/api/workbench/architect/message", `{"message":"hi"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchArchitectMessageRequiresMessage(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, architectResponse("ok", map[string]any{"goal": "g"}))
	createArchitectSession(t, wb, "ship it")
	w := postJSON(t, wb, "/api/workbench/architect/message", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchArchitectMessageSendsContext(t *testing.T) {
	var gotUser string
	handler := func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]string `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) == 2 {
			gotUser = body.Messages[1]["content"]
		}
		architectResponse("refined", map[string]any{"goal": "ship it", "constraints": []string{"c1"}})(w, r)
	}
	wb := New()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := startMockProvider(t, handler)
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"k","model":"m"}`)
	// Populate the latest inspection so it is included in the prompt.
	architectGET(t, wb, "/api/workbench/project/inspect")
	createArchitectSession(t, wb, "ship it")

	w := postJSON(t, wb, "/api/workbench/architect/message", `{"message":"please add tests"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	for _, want := range []string{
		"Project path: " + dir, // project state
		"Config files: go.mod", // latest inspection
		"user: ship it",        // prior conversation history
		"Current spec draft:",  // current spec draft
		"Goal: ship it",
		"New user message: please add tests", // new user message
	} {
		if !strings.Contains(gotUser, want) {
			t.Errorf("expected provider prompt to contain %q, got:\n%s", want, gotUser)
		}
	}
}

func TestWorkbenchArchitectMessageParsesValidJSON(t *testing.T) {
	spec := map[string]any{
		"goal":                "ship it well",
		"constraints":         []string{"no new deps"},
		"affected_areas":      []string{"api"},
		"acceptance_criteria": []string{"tests pass"},
		"risks":               []string{"scope creep"},
		"open_questions":      []string{"which db?"},
		"suggested_lanes":     []string{"builder"},
	}
	wb, _ := newArchitectWorkbench(t, architectResponse("Here is the refined spec.", spec))
	createArchitectSession(t, wb, "ship it")
	w := postJSON(t, wb, "/api/workbench/architect/message", `{"message":"refine"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var s ArchitectSession
	json.Unmarshal(w.Body.Bytes(), &s)
	if s.Spec.Goal != "ship it well" {
		t.Errorf("spec goal=%q, want ship it well", s.Spec.Goal)
	}
	if len(s.Spec.Constraints) != 1 || s.Spec.Constraints[0] != "no new deps" {
		t.Errorf("constraints=%v", s.Spec.Constraints)
	}
	if len(s.Spec.SuggestedLanes) != 1 || s.Spec.SuggestedLanes[0] != "builder" {
		t.Errorf("suggested_lanes=%v", s.Spec.SuggestedLanes)
	}
}

func TestWorkbenchArchitectMessageNormalizesNilSlices(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, architectResponse("ok", map[string]any{"goal": "g2"}))
	createArchitectSession(t, wb, "ship it")
	w := postJSON(t, wb, "/api/workbench/architect/message", `{"message":"refine"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, field := range []string{"constraints", "affected_areas", "acceptance_criteria", "risks", "open_questions", "suggested_lanes"} {
		if !strings.Contains(body, `"`+field+`":[]`) {
			t.Errorf("expected %q to serialize as [], got:\n%s", field, body)
		}
	}
}

func TestWorkbenchArchitectMessageUpdatesSpec(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, architectResponse("ok", map[string]any{"goal": "updated goal", "constraints": []string{"x"}}))
	created := createArchitectSession(t, wb, "original goal")
	w := postJSON(t, wb, "/api/workbench/architect/message", `{"message":"refine"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	// The persisted session reflects the updated spec.
	got := architectGET(t, wb, "/api/workbench/architect/session")
	var s ArchitectSession
	json.Unmarshal(got.Body.Bytes(), &s)
	if s.ID != created.ID {
		t.Fatalf("expected same session id %q, got %q", created.ID, s.ID)
	}
	if s.Spec.Goal != "updated goal" {
		t.Errorf("spec goal=%q, want updated goal", s.Spec.Goal)
	}
	if len(s.Spec.Constraints) != 1 || s.Spec.Constraints[0] != "x" {
		t.Errorf("constraints=%v", s.Spec.Constraints)
	}
}

func TestWorkbenchArchitectMessageAppendsArchitectMessage(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, architectResponse("architect reply", map[string]any{"goal": "g"}))
	createArchitectSession(t, wb, "g")
	w := postJSON(t, wb, "/api/workbench/architect/message", `{"message":"hello"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var s ArchitectSession
	json.Unmarshal(w.Body.Bytes(), &s)
	if len(s.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(s.Messages), s.Messages)
	}
	if s.Messages[1].Role != "user" || s.Messages[1].Content != "hello" {
		t.Errorf("unexpected user message: %+v", s.Messages[1])
	}
	if s.Messages[2].Role != "architect" || s.Messages[2].Content != "architect reply" {
		t.Errorf("unexpected architect message: %+v", s.Messages[2])
	}
}

func TestWorkbenchArchitectMessageEvents(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, architectResponse("ok", map[string]any{"goal": "g"}))
	createArchitectSession(t, wb, "g")
	if w := postJSON(t, wb, "/api/workbench/architect/message", `{"message":"hi"}`); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if cortexHasEvent(wb, "architect.message.created") == nil {
		t.Error("architect.message.created event not appended")
	}
	if cortexHasEvent(wb, "architect.spec.updated") == nil {
		t.Error("architect.spec.updated event not appended")
	}
}

func TestWorkbenchArchitectMessageProviderFailure(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"upstream boom"}`))
	}
	wb, _ := newArchitectWorkbench(t, handler)
	createArchitectSession(t, wb, "g")
	w := postJSON(t, wb, "/api/workbench/architect/message", `{"message":"hi"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	var s ArchitectSession
	json.Unmarshal(w.Body.Bytes(), &s)
	// user(goal), user(hi), system(error)
	if len(s.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(s.Messages), s.Messages)
	}
	if s.Messages[1].Role != "user" || s.Messages[1].Content != "hi" {
		t.Errorf("expected user message appended, got %+v", s.Messages[1])
	}
	last := s.Messages[2]
	if last.Role != "system" {
		t.Errorf("last message role=%q, want system", last.Role)
	}
	if last.Content == "" {
		t.Error("expected a system error summary")
	}
	if s.Spec.Goal != "g" {
		t.Errorf("spec must not change on failure, got goal=%q", s.Spec.Goal)
	}
	if cortexHasEvent(wb, "architect.message.failed") == nil {
		t.Error("architect.message.failed event not appended")
	}
}

func TestWorkbenchArchitectNoAPIKey(t *testing.T) {
	const secret = "architect-secret"
	server := startMockProvider(t, architectResponse("ok", map[string]any{"goal": "g", "constraints": []string{"c"}}))
	wb := New()
	dir := t.TempDir()
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"`+secret+`","model":"m"}`)
	createArchitectSession(t, wb, "g")

	msg := postJSON(t, wb, "/api/workbench/architect/message", `{"message":"hi"}`)
	if msg.Code != http.StatusOK {
		t.Fatalf("message: expected 200, got %d: %s", msg.Code, msg.Body.String())
	}
	assertNoSecret(t, "message", secret, msg.Body.Bytes())

	for _, path := range []string{"/api/workbench/architect/session", "/api/workbench/architect/sessions"} {
		assertNoSecret(t, path, secret, architectGET(t, wb, path).Body.Bytes())
	}
	for _, e := range wb.store.List() {
		if strings.HasPrefix(e.Type, "architect.") {
			assertNoSecret(t, "event "+e.Type, secret, e.Data)
		}
	}
}

func TestWorkbenchArchitectMessageNoFileWrite(t *testing.T) {
	wb, dir := newArchitectWorkbench(t, architectResponse("ok", map[string]any{"goal": "g", "constraints": []string{"c"}}))
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	createArchitectSession(t, wb, "g")
	before := snapshotDir(t, dir)
	if w := postJSON(t, wb, "/api/workbench/architect/message", `{"message":"add a file please"}`); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	after := snapshotDir(t, dir)
	if len(before) != len(after) {
		t.Fatalf("file count changed: %d -> %d", len(before), len(after))
	}
	for path, content := range before {
		if after[path] != content {
			t.Errorf("file %s changed", path)
		}
	}
}

func TestWorkbenchArchitectAcceptRequiresSession(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/architect/accept", `{}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchArchitectAcceptRejectsAlreadyAccepted(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, nil)
	createArchitectSession(t, wb, "g")
	if w := postJSON(t, wb, "/api/workbench/architect/accept", `{}`); w.Code != http.StatusOK {
		t.Fatalf("first accept: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	w := postJSON(t, wb, "/api/workbench/architect/accept", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchArchitectAcceptMarksAccepted(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, nil)
	createArchitectSession(t, wb, "g")
	w := postJSON(t, wb, "/api/workbench/architect/accept", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var s ArchitectSession
	json.Unmarshal(w.Body.Bytes(), &s)
	if s.Status != "accepted" {
		t.Errorf("status=%q, want accepted", s.Status)
	}
}

func TestWorkbenchArchitectAcceptEvent(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, nil)
	createArchitectSession(t, wb, "g")
	postJSON(t, wb, "/api/workbench/architect/accept", `{}`)
	if ev := cortexHasEvent(wb, "architect.spec.accepted"); ev == nil {
		t.Error("architect.spec.accepted event not appended")
	} else if ev.Message != "Accepted Architect spec" {
		t.Errorf("event message: %q", ev.Message)
	}
}

func TestWorkbenchArchitectCortexTaskRequiresAccepted(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, nil)
	createArchitectSession(t, wb, "g") // active, not accepted
	w := postJSON(t, wb, "/api/workbench/architect/cortex-task", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchArchitectCortexTaskCreatesTask(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, nil)
	createArchitectSession(t, wb, "ship the thing")
	postJSON(t, wb, "/api/workbench/architect/accept", `{}`)
	w := postJSON(t, wb, "/api/workbench/architect/cortex-task", `{}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var task CortexTask
	json.Unmarshal(w.Body.Bytes(), &task)
	if task.Goal != "ship the thing" {
		t.Errorf("task goal=%q, want ship the thing", task.Goal)
	}
	if task.Status != "planned" {
		t.Errorf("task status=%q, want planned", task.Status)
	}
	if len(task.Lanes) == 0 {
		t.Error("expected lanes to be created")
	}
}

func TestWorkbenchArchitectCortexTaskIdempotent(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, nil)
	createArchitectSession(t, wb, "ship the thing")
	postJSON(t, wb, "/api/workbench/architect/accept", `{}`)

	first := postJSON(t, wb, "/api/workbench/architect/cortex-task", `{}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first: expected 201, got %d: %s", first.Code, first.Body.String())
	}
	var t1 CortexTask
	json.Unmarshal(first.Body.Bytes(), &t1)

	// A repeat request must be idempotent: same task id, 200, one task total.
	second := postJSON(t, wb, "/api/workbench/architect/cortex-task", `{}`)
	if second.Code != http.StatusOK {
		t.Fatalf("repeat: expected 200 (idempotent), got %d: %s", second.Code, second.Body.String())
	}
	var t2 CortexTask
	json.Unmarshal(second.Body.Bytes(), &t2)
	if t2.ID != t1.ID {
		t.Errorf("repeat returned task #%s, want existing #%s", t2.ID, t1.ID)
	}

	listW := architectGET(t, wb, "/api/workbench/cortex/tasks")
	var tasks []CortexTask
	json.Unmarshal(listW.Body.Bytes(), &tasks)
	if len(tasks) != 1 {
		t.Errorf("expected exactly 1 cortex task after a duplicate request, got %d", len(tasks))
	}

	// The session records the task id, so the UI knows one already exists.
	sessW := architectGET(t, wb, "/api/workbench/architect/session")
	var sess ArchitectSession
	json.Unmarshal(sessW.Body.Bytes(), &sess)
	if sess.CortexTaskID != t1.ID {
		t.Errorf("session cortex_task_id=%q, want %q", sess.CortexTaskID, t1.ID)
	}
}

func TestWorkbenchArchitectCortexTaskDefaultsMulti(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, nil)
	createArchitectSession(t, wb, "g")
	postJSON(t, wb, "/api/workbench/architect/accept", `{}`)
	w := postJSON(t, wb, "/api/workbench/architect/cortex-task", `{}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var task CortexTask
	json.Unmarshal(w.Body.Bytes(), &task)
	counts := cortexRoleCounts(task.Lanes)
	if counts["architect"] != 1 || counts["builder"] != 2 || counts["reviewer"] != 1 || counts["validator"] != 1 {
		t.Errorf("expected multi defaults (1/2/1/1), got %v", counts)
	}
}

func TestWorkbenchArchitectCortexTaskRespectsRoles(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, nil)
	createArchitectSession(t, wb, "g")
	postJSON(t, wb, "/api/workbench/architect/accept", `{}`)
	w := postJSON(t, wb, "/api/workbench/architect/cortex-task", `{"roles":{"architect":0,"builder":3,"reviewer":0,"validator":0}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var task CortexTask
	json.Unmarshal(w.Body.Bytes(), &task)
	counts := cortexRoleCounts(task.Lanes)
	if counts["builder"] != 3 {
		t.Errorf("expected 3 builder lanes, got %v", counts)
	}
	if len(task.Lanes) != 3 {
		t.Errorf("expected 3 total lanes, got %d", len(task.Lanes))
	}
}

func TestWorkbenchArchitectCortexTaskEvent(t *testing.T) {
	wb, _ := newArchitectWorkbench(t, nil)
	session := createArchitectSession(t, wb, "g")
	postJSON(t, wb, "/api/workbench/architect/accept", `{}`)
	postJSON(t, wb, "/api/workbench/architect/cortex-task", `{}`)
	ev := cortexHasEvent(wb, "architect.cortex_task.created")
	if ev == nil {
		t.Fatal("architect.cortex_task.created event not appended")
	}
	if ev.Message != "Created Cortex task from Architect spec" {
		t.Errorf("event message: %q", ev.Message)
	}
	var data struct {
		ArchitectSessionID string     `json:"architect_session_id"`
		Task               CortexTask `json:"task"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		t.Fatalf("decode event data: %v", err)
	}
	if data.ArchitectSessionID != session.ID {
		t.Errorf("event architect_session_id=%q, want %q", data.ArchitectSessionID, session.ID)
	}
	if data.Task.Goal != "g" {
		t.Errorf("event task goal=%q, want g", data.Task.Goal)
	}
}

// laneProviderResponse returns a handler whose assistant message content is
// exactly content (plain text or a JSON object, as the caller chooses).
func laneProviderResponse(content string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "test",
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": content}},
			},
		})
	}
}

// newLaneConvWorkbench builds a workbench with a mock provider configured when
// handler is non-nil. Lane conversations do not require a project to be open.
func newLaneConvWorkbench(t *testing.T, handler http.HandlerFunc) *Server {
	t.Helper()
	wb := New()
	if handler != nil {
		server := startMockProvider(t, handler)
		postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"k","model":"m"}`)
	}
	return wb
}

func createLaneConversation(t *testing.T, wb *Server, body string) LaneConversation {
	t.Helper()
	w := postJSON(t, wb, "/api/workbench/lane-conversation", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("lane conversation: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var c LaneConversation
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode lane conversation: %v", err)
	}
	return c
}

func laneOfRole(t *testing.T, task CortexTask, role string) string {
	t.Helper()
	for _, l := range task.Lanes {
		if l.Role == role {
			return l.ID
		}
	}
	t.Fatalf("no %s lane in task", role)
	return ""
}

func TestWorkbenchLaneConversationRequiresKind(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/lane-conversation", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchLaneConversationRejectsUnknownKind(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/lane-conversation", `{"kind":"wizard"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchLaneConversationLaneKindsRequireLane(t *testing.T) {
	wb := New()
	for _, kind := range []string{"builder", "reviewer", "validator"} {
		w := postJSON(t, wb, "/api/workbench/lane-conversation", `{"kind":"`+kind+`"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("kind %s: expected 400, got %d: %s", kind, w.Code, w.Body.String())
		}
	}
}

func TestWorkbenchLaneConversationResolvesLaneRole(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	laneID := laneOfRole(t, task, "builder")
	c := createLaneConversation(t, wb, `{"kind":"builder","lane_id":"`+laneID+`"}`)
	if c.Status != "active" {
		t.Errorf("status=%q, want active", c.Status)
	}
	if c.Context.LaneRole != "builder" {
		t.Errorf("lane_role=%q, want builder", c.Context.LaneRole)
	}
	if c.Context.TaskID != task.ID {
		t.Errorf("task_id=%q, want %q", c.Context.TaskID, task.ID)
	}
}

func TestWorkbenchLaneConversationArchitectNoLane(t *testing.T) {
	wb := New()
	c := createLaneConversation(t, wb, `{"kind":"architect"}`)
	if c.Context.Kind != "architect" {
		t.Errorf("kind=%q, want architect", c.Context.Kind)
	}
	if c.Context.LaneID != "" {
		t.Errorf("expected no lane id, got %q", c.Context.LaneID)
	}
}

func TestWorkbenchLaneConversationLockboxApplyArtifactContext(t *testing.T) {
	wb := New()
	for _, kind := range []string{"lockbox", "apply"} {
		c := createLaneConversation(t, wb, `{"kind":"`+kind+`","artifact_id":"a1","artifact_type":"lockbox_request"}`)
		if c.Context.Kind != kind {
			t.Errorf("kind=%q, want %q", c.Context.Kind, kind)
		}
		if c.Context.ArtifactID != "a1" {
			t.Errorf("artifact_id=%q, want a1", c.Context.ArtifactID)
		}
		if c.Context.ArtifactType != "lockbox_request" {
			t.Errorf("artifact_type=%q, want lockbox_request", c.Context.ArtifactType)
		}
	}
}

func TestWorkbenchLaneConversationCreatedEvent(t *testing.T) {
	wb := New()
	createLaneConversation(t, wb, `{"kind":"architect"}`)
	if ev := cortexHasEvent(wb, "lane.conversation.created"); ev == nil {
		t.Error("lane.conversation.created event not appended")
	} else if ev.Message != "Created lane conversation" {
		t.Errorf("event message: %q", ev.Message)
	}
}

func TestWorkbenchLaneConversationGET404(t *testing.T) {
	wb := New()
	w := architectGET(t, wb, "/api/workbench/lane-conversation")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchLaneConversationGETCurrent(t *testing.T) {
	wb := New()
	created := createLaneConversation(t, wb, `{"kind":"architect"}`)
	w := architectGET(t, wb, "/api/workbench/lane-conversation")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var c LaneConversation
	json.Unmarshal(w.Body.Bytes(), &c)
	if c.ID != created.ID {
		t.Errorf("current conversation id=%q, want %q", c.ID, created.ID)
	}
}

func TestWorkbenchLaneConversationsEmpty(t *testing.T) {
	wb := New()
	w := architectGET(t, wb, "/api/workbench/lane-conversations")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := strings.TrimSpace(w.Body.String()); body != "[]" {
		t.Errorf("expected [] for empty conversations, got %q", body)
	}
}

func TestWorkbenchLaneConversationsFilterByTask(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	laneID := laneOfRole(t, task, "builder")
	createLaneConversation(t, wb, `{"kind":"builder","lane_id":"`+laneID+`"}`)
	createLaneConversation(t, wb, `{"kind":"architect"}`)
	w := architectGET(t, wb, "/api/workbench/lane-conversations?task_id="+task.ID)
	var list []LaneConversation
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0].Context.TaskID != task.ID {
		t.Fatalf("expected 1 conversation for task %s, got %v", task.ID, list)
	}
}

func TestWorkbenchLaneConversationsFilterByLane(t *testing.T) {
	wb := New()
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	builderLane := laneOfRole(t, task, "builder")
	reviewerLane := laneOfRole(t, task, "reviewer")
	createLaneConversation(t, wb, `{"kind":"builder","lane_id":"`+builderLane+`"}`)
	createLaneConversation(t, wb, `{"kind":"reviewer","lane_id":"`+reviewerLane+`"}`)
	w := architectGET(t, wb, "/api/workbench/lane-conversations?lane_id="+builderLane)
	var list []LaneConversation
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0].Context.LaneID != builderLane {
		t.Fatalf("expected 1 conversation for lane %s, got %v", builderLane, list)
	}
}

func TestWorkbenchLaneConversationsFilterByKind(t *testing.T) {
	wb := New()
	createLaneConversation(t, wb, `{"kind":"architect"}`)
	createLaneConversation(t, wb, `{"kind":"lockbox"}`)
	w := architectGET(t, wb, "/api/workbench/lane-conversations?kind=lockbox")
	var list []LaneConversation
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0].Context.Kind != "lockbox" {
		t.Fatalf("expected 1 lockbox conversation, got %v", list)
	}
}

func TestWorkbenchLaneMessageRequiresConversation(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/lane-conversation/message", `{"message":"hi"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchLaneMessageRejectsClosed(t *testing.T) {
	wb := newLaneConvWorkbench(t, laneProviderResponse("reply"))
	createLaneConversation(t, wb, `{"kind":"architect"}`)
	if w := postJSON(t, wb, "/api/workbench/lane-conversation/close", `{}`); w.Code != http.StatusOK {
		t.Fatalf("close: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	w := postJSON(t, wb, "/api/workbench/lane-conversation/message", `{"message":"hi"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchLaneMessageRequiresProvider(t *testing.T) {
	wb := New() // no provider configured
	createLaneConversation(t, wb, `{"kind":"architect"}`)
	w := postJSON(t, wb, "/api/workbench/lane-conversation/message", `{"message":"hi"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchLaneMessageRequiresMessage(t *testing.T) {
	wb := newLaneConvWorkbench(t, laneProviderResponse("reply"))
	createLaneConversation(t, wb, `{"kind":"architect"}`)
	w := postJSON(t, wb, "/api/workbench/lane-conversation/message", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchLaneMessageSendsContext(t *testing.T) {
	var gotUser string
	handler := func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]string `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) == 2 {
			gotUser = body.Messages[1]["content"]
		}
		laneProviderResponse("assistant reply")(w, r)
	}
	wb := New()
	server := startMockProvider(t, handler)
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"k","model":"m"}`)
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	builderLane := laneOfRole(t, task, "builder")
	// Seed a lane proposal so the builder artifact context is non-empty.
	wb.cortexLaneProposals.Append(CortexLaneProposal{
		TaskID:    task.ID,
		LaneID:    builderLane,
		LaneIndex: 1,
		Status:    builderProposalStatusProposed,
		Summary:   "proposal summary here",
		Files:     []BuilderProposedFile{{Path: "a.go", Action: "create"}},
	})
	createLaneConversation(t, wb, `{"kind":"builder","lane_id":"`+builderLane+`"}`)

	// First message establishes history; the second message's prompt is captured.
	if w := postJSON(t, wb, "/api/workbench/lane-conversation/message", `{"message":"first question"}`); w.Code != http.StatusOK {
		t.Fatalf("first message: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w := postJSON(t, wb, "/api/workbench/lane-conversation/message", `{"message":"second question"}`); w.Code != http.StatusOK {
		t.Fatalf("second message: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	for _, want := range []string{
		"Conversation kind: builder",        // conversation context
		"Lane id: " + builderLane,           // lane scope
		"Lane role: builder",                // resolved lane role
		"Selected lane:",                    // selected lane details
		"proposal summary here",             // artifact context
		"first question",                    // prior conversation history
		"New user message: second question", // new user message
	} {
		if !strings.Contains(gotUser, want) {
			t.Errorf("expected provider prompt to contain %q, got:\n%s", want, gotUser)
		}
	}
}

func TestWorkbenchLaneMessagePlainText(t *testing.T) {
	wb := newLaneConvWorkbench(t, laneProviderResponse("plain reply text"))
	createLaneConversation(t, wb, `{"kind":"architect"}`)
	w := postJSON(t, wb, "/api/workbench/lane-conversation/message", `{"message":"hi"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var c LaneConversation
	json.Unmarshal(w.Body.Bytes(), &c)
	last := c.Messages[len(c.Messages)-1]
	if last.Role != "assistant" || last.Content != "plain reply text" {
		t.Errorf("unexpected assistant message: %+v", last)
	}
}

func TestWorkbenchLaneMessageJSONResponse(t *testing.T) {
	wb := newLaneConvWorkbench(t, laneProviderResponse(`{"message":"json reply"}`))
	createLaneConversation(t, wb, `{"kind":"architect"}`)
	w := postJSON(t, wb, "/api/workbench/lane-conversation/message", `{"message":"hi"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var c LaneConversation
	json.Unmarshal(w.Body.Bytes(), &c)
	last := c.Messages[len(c.Messages)-1]
	if last.Content != "json reply" {
		t.Errorf("assistant content=%q, want json reply", last.Content)
	}
}

func TestWorkbenchLaneMessageAppendsMessages(t *testing.T) {
	wb := newLaneConvWorkbench(t, laneProviderResponse("reply"))
	createLaneConversation(t, wb, `{"kind":"architect"}`)
	w := postJSON(t, wb, "/api/workbench/lane-conversation/message", `{"message":"hello"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var c LaneConversation
	json.Unmarshal(w.Body.Bytes(), &c)
	if len(c.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(c.Messages), c.Messages)
	}
	if c.Messages[0].Role != "user" || c.Messages[0].Content != "hello" {
		t.Errorf("unexpected user message: %+v", c.Messages[0])
	}
	if c.Messages[1].Role != "assistant" || c.Messages[1].Content != "reply" {
		t.Errorf("unexpected assistant message: %+v", c.Messages[1])
	}
}

func TestWorkbenchLaneMessageGenericEvent(t *testing.T) {
	wb := newLaneConvWorkbench(t, laneProviderResponse("reply"))
	createLaneConversation(t, wb, `{"kind":"architect"}`)
	if w := postJSON(t, wb, "/api/workbench/lane-conversation/message", `{"message":"hi"}`); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if cortexHasEvent(wb, "lane.message.created") == nil {
		t.Error("lane.message.created event not appended")
	}
}

func TestWorkbenchLaneMessageRoleEventArchitect(t *testing.T) {
	wb := newLaneConvWorkbench(t, laneProviderResponse("reply"))
	createLaneConversation(t, wb, `{"kind":"architect"}`)
	postJSON(t, wb, "/api/workbench/lane-conversation/message", `{"message":"hi"}`)
	if cortexHasEvent(wb, "architect.message.created") == nil {
		t.Error("architect.message.created event not appended")
	}
}

func TestWorkbenchLaneMessageRoleEventBuilder(t *testing.T) {
	wb := newLaneConvWorkbench(t, laneProviderResponse("reply"))
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	laneID := laneOfRole(t, task, "builder")
	createLaneConversation(t, wb, `{"kind":"builder","lane_id":"`+laneID+`"}`)
	postJSON(t, wb, "/api/workbench/lane-conversation/message", `{"message":"hi"}`)
	if cortexHasEvent(wb, "builder.message.created") == nil {
		t.Error("builder.message.created event not appended")
	}
}

func TestWorkbenchLaneMessageRoleEventReviewer(t *testing.T) {
	wb := newLaneConvWorkbench(t, laneProviderResponse("reply"))
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	laneID := laneOfRole(t, task, "reviewer")
	createLaneConversation(t, wb, `{"kind":"reviewer","lane_id":"`+laneID+`"}`)
	postJSON(t, wb, "/api/workbench/lane-conversation/message", `{"message":"hi"}`)
	if cortexHasEvent(wb, "reviewer.message.created") == nil {
		t.Error("reviewer.message.created event not appended")
	}
}

func TestWorkbenchLaneMessageRoleEventValidator(t *testing.T) {
	wb := newLaneConvWorkbench(t, laneProviderResponse("reply"))
	task := createCortexTask(t, wb, `{"goal":"g","mode":"multi"}`)
	laneID := laneOfRole(t, task, "validator")
	createLaneConversation(t, wb, `{"kind":"validator","lane_id":"`+laneID+`"}`)
	postJSON(t, wb, "/api/workbench/lane-conversation/message", `{"message":"hi"}`)
	if cortexHasEvent(wb, "validator.message.created") == nil {
		t.Error("validator.message.created event not appended")
	}
}

func TestWorkbenchLaneMessageRoleEventLockboxApply(t *testing.T) {
	for _, kind := range []string{"lockbox", "apply"} {
		wb := newLaneConvWorkbench(t, laneProviderResponse("reply"))
		createLaneConversation(t, wb, `{"kind":"`+kind+`"}`)
		postJSON(t, wb, "/api/workbench/lane-conversation/message", `{"message":"hi"}`)
		if cortexHasEvent(wb, "lockbox.message.created") == nil {
			t.Errorf("kind %s: lockbox.message.created event not appended", kind)
		}
	}
}

func TestWorkbenchLaneMessageProviderFailure(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"upstream boom"}`))
	}
	wb := newLaneConvWorkbench(t, handler)
	createLaneConversation(t, wb, `{"kind":"architect"}`)
	w := postJSON(t, wb, "/api/workbench/lane-conversation/message", `{"message":"hi"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	var c LaneConversation
	json.Unmarshal(w.Body.Bytes(), &c)
	if len(c.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(c.Messages), c.Messages)
	}
	if c.Messages[0].Role != "user" || c.Messages[0].Content != "hi" {
		t.Errorf("expected user message appended, got %+v", c.Messages[0])
	}
	last := c.Messages[1]
	if last.Role != "system" {
		t.Errorf("last message role=%q, want system", last.Role)
	}
	if last.Content == "" {
		t.Error("expected a system error summary")
	}
	if cortexHasEvent(wb, "lane.message.failed") == nil {
		t.Error("lane.message.failed event not appended")
	}
}

func TestWorkbenchLaneConversationCloseRequiresConversation(t *testing.T) {
	wb := New()
	w := postJSON(t, wb, "/api/workbench/lane-conversation/close", `{}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchLaneConversationCloseRejectsClosed(t *testing.T) {
	wb := New()
	createLaneConversation(t, wb, `{"kind":"architect"}`)
	if w := postJSON(t, wb, "/api/workbench/lane-conversation/close", `{}`); w.Code != http.StatusOK {
		t.Fatalf("first close: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	w := postJSON(t, wb, "/api/workbench/lane-conversation/close", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchLaneConversationCloseMarksClosed(t *testing.T) {
	wb := New()
	createLaneConversation(t, wb, `{"kind":"architect"}`)
	w := postJSON(t, wb, "/api/workbench/lane-conversation/close", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var c LaneConversation
	json.Unmarshal(w.Body.Bytes(), &c)
	if c.Status != "closed" {
		t.Errorf("status=%q, want closed", c.Status)
	}
}

func TestWorkbenchLaneConversationCloseEvent(t *testing.T) {
	wb := New()
	createLaneConversation(t, wb, `{"kind":"architect"}`)
	postJSON(t, wb, "/api/workbench/lane-conversation/close", `{}`)
	if ev := cortexHasEvent(wb, "lane.conversation.closed"); ev == nil {
		t.Error("lane.conversation.closed event not appended")
	} else if ev.Message != "Closed lane conversation" {
		t.Errorf("event message: %q", ev.Message)
	}
}

func TestWorkbenchLaneConversationNoAPIKey(t *testing.T) {
	const secret = "lane-secret"
	server := startMockProvider(t, laneProviderResponse("reply"))
	wb := New()
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"`+secret+`","model":"m"}`)
	createLaneConversation(t, wb, `{"kind":"architect"}`)

	msg := postJSON(t, wb, "/api/workbench/lane-conversation/message", `{"message":"hi"}`)
	if msg.Code != http.StatusOK {
		t.Fatalf("message: expected 200, got %d: %s", msg.Code, msg.Body.String())
	}
	assertNoSecret(t, "message", secret, msg.Body.Bytes())

	for _, path := range []string{"/api/workbench/lane-conversation", "/api/workbench/lane-conversations"} {
		assertNoSecret(t, path, secret, architectGET(t, wb, path).Body.Bytes())
	}
	for _, e := range wb.store.List() {
		if strings.HasPrefix(e.Type, "lane.") || strings.HasPrefix(e.Type, "architect.") {
			assertNoSecret(t, "event "+e.Type, secret, e.Data)
		}
	}
}

func TestWorkbenchLaneMessageNoFileWrite(t *testing.T) {
	wb := New()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := startMockProvider(t, laneProviderResponse("reply"))
	postJSON(t, wb, "/api/workbench/project/open", `{"path":"`+dir+`"}`)
	postJSON(t, wb, "/api/workbench/provider", `{"base_url":"`+server.URL+`","api_key":"k","model":"m"}`)
	createLaneConversation(t, wb, `{"kind":"architect"}`)

	before := snapshotDir(t, dir)
	if w := postJSON(t, wb, "/api/workbench/lane-conversation/message", `{"message":"add a file please"}`); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	after := snapshotDir(t, dir)
	if len(before) != len(after) {
		t.Fatalf("file count changed: %d -> %d", len(before), len(after))
	}
	for path, content := range before {
		if after[path] != content {
			t.Errorf("file %s changed", path)
		}
	}
}
