package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tasklifepkg "github.com/kitporath/project_valhalla/pkg/tasklife"
	taskspkg "github.com/kitporath/project_valhalla/pkg/tasks"
	toolpkg "github.com/kitporath/project_valhalla/pkg/tools"
)

func TestTaskSendEmitsWorkspaceUpdatesForTypedEvents(t *testing.T) {
	oldGatewayURL := gatewayURLValue
	defer func() {
		gatewayURLValue = oldGatewayURL
	}()

	type gatewayEvent struct {
		Type     string                 `json:"type"`
		Agent    string                 `json:"agent"`
		Metadata map[string]interface{} `json:"metadata"`
	}

	eventsCh := make(chan gatewayEvent, 8)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/events" {
			http.NotFound(w, r)
			return
		}
		var evt gatewayEvent
		if err := json.NewDecoder(r.Body).Decode(&evt); err != nil {
			t.Fatalf("decode gateway event: %v", err)
		}
		eventsCh <- evt
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer gateway.Close()
	gatewayURLValue = gateway.URL

	reviewTracker := newReviewContextTracker("ragnar", "", time.Minute)
	defer reviewTracker.Stop()

	taskStore := taskspkg.NewStore()
	taskTracker := tasklifepkg.NewTaskTracker()
	taskCancels := map[string]context.CancelFunc{}
	var taskCancelMu sync.Mutex

	mux := http.NewServeMux()
	registerTaskRoutes(mux, serverDeps{
		agentName: "ragnar",
		processConversation: func(ctx context.Context, sessionID, taskID, content string, emit func(interface{}) bool, logTool func(taskspkg.ToolLog)) (string, error) {
			if emit == nil {
				t.Fatalf("emit callback must be wired for /tasks/send")
			}
			if !emit(toolpkg.FileWriteEvent{
				Type:      "file_write",
				Repo:      "kit/hirdforge",
				Branch:    "feature/task-workspace",
				Path:      "cmd/agent/tasks.go",
				LineStart: 10,
				LineEnd:   20,
				Status:    "writing",
			}) {
				t.Fatalf("emit returned false")
			}
			return "done", nil
		},
		taskStore:           taskStore,
		taskTracker:         taskTracker,
		sovereignStates:     map[string]bool{},
		sovereignReporter:   tasklifepkg.NewSovereignReporter("", "ragnar", false),
		taskCancelMu:        &taskCancelMu,
		taskCancels:         taskCancels,
		completionMaxNudges: 1,
		reviewTracker:       reviewTracker,
	})

	req := httptest.NewRequest(http.MethodPost, "/tasks/send", strings.NewReader(`{"content":"Update the gateway workspace","from":"freya","session_id":"sess-42"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /tasks/send status = %d, body = %s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	completed := false
	for time.Now().Before(deadline) {
		tasks := taskStore.List("", "")
		if len(tasks) == 1 && tasks[0].Status == "completed" {
			completed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !completed {
		t.Fatalf("task did not reach completed status")
	}

	var sawFileWrite bool
	var workspace map[string]interface{}
	collectUntil := time.Now().Add(2 * time.Second)
	for time.Now().Before(collectUntil) {
		select {
		case evt := <-eventsCh:
			switch evt.Type {
			case "file_write":
				sawFileWrite = true
				if got := strings.TrimSpace(evt.Agent); got != "ragnar" {
					t.Fatalf("file_write agent = %q", got)
				}
				if got := strings.TrimSpace(fmt.Sprint(evt.Metadata["session_id"])); got != "sess-42" {
					t.Fatalf("file_write session_id = %q", got)
				}
			case "workspace_update":
				if meta, ok := evt.Metadata["workspace"].(map[string]interface{}); ok {
					workspace = meta
				}
			}
			if sawFileWrite && workspace != nil {
				goto assertions
			}
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

assertions:
	if !sawFileWrite {
		t.Fatalf("expected file_write event to be forwarded to gateway")
	}
	if workspace == nil {
		t.Fatalf("expected workspace_update event to be forwarded to gateway")
	}
	if got := strings.TrimSpace(fmt.Sprint(workspace["current_file"])); got != "cmd/agent/tasks.go" {
		t.Fatalf("workspace current_file = %q", got)
	}
	if got := strings.TrimSpace(fmt.Sprint(workspace["current_session_id"])); got != "sess-42" {
		t.Fatalf("workspace current_session_id = %q", got)
	}
}

func TestCaptureTaskCompletionPostsStructuredMemory(t *testing.T) {
	type rememberRequest struct {
		Agent   string   `json:"agent"`
		Content string   `json:"content"`
		Tags    []string `json:"tags"`
		Type    string   `json:"type"`
	}

	reqCh := make(chan rememberRequest, 1)
	memory := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/remember" {
			http.NotFound(w, r)
			return
		}
		var req rememberRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode remember request: %v", err)
		}
		reqCh <- req
		w.WriteHeader(http.StatusOK)
	}))
	defer memory.Close()

	task := taskspkg.Task{
		Content: "Update kit/hirdforge to support memory capture " + strings.Repeat("a", 240),
		Result:  "Completed the capture flow " + strings.Repeat("b", 240),
		From:    "freya",
		Tools: []taskspkg.ToolLog{
			{Name: "deploy"},
			{Name: "build"},
			{Name: "build"},
		},
	}

	startTime := time.Now().Add(-90 * time.Second)
	captureTaskCompletion(memory.URL, "ragnar", task, startTime)

	var req rememberRequest
	select {
	case req = <-reqCh:
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive memory request")
	}

	if req.Agent != "ragnar" {
		t.Fatalf("agent = %q", req.Agent)
	}
	if req.Type != "fact" {
		t.Fatalf("type = %q", req.Type)
	}
	expectedObjective := truncateTaskText(task.Content, 200)
	expectedOutcome := truncateTaskText(task.Result, 200)
	expectedPrefix := "TASK COMPLETED | objective: " + expectedObjective + " | outcome: " + expectedOutcome + " | tools: build,deploy | duration: "
	if !strings.HasPrefix(req.Content, expectedPrefix) {
		t.Fatalf("content prefix mismatch: %q", req.Content)
	}
	if !strings.HasSuffix(req.Content, " | from: freya") {
		t.Fatalf("content suffix mismatch: %q", req.Content)
	}
	if got := strings.Join(req.Tags, ","); got != "task_completion,ragnar,repo:kit/hirdforge" {
		t.Fatalf("tags = %q", got)
	}
}
