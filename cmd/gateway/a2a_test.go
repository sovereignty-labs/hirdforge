package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTaskStateConstants(t *testing.T) {
	if TaskStateSubmitted != "submitted" {
		t.Fatalf("TaskStateSubmitted = %q", TaskStateSubmitted)
	}
	if TaskStateWorking != "working" {
		t.Fatalf("TaskStateWorking = %q", TaskStateWorking)
	}
	if TaskStateCompleted != "completed" {
		t.Fatalf("TaskStateCompleted = %q", TaskStateCompleted)
	}
	if TaskStateFailed != "failed" {
		t.Fatalf("TaskStateFailed = %q", TaskStateFailed)
	}
	if TaskStateCanceled != "canceled" {
		t.Fatalf("TaskStateCanceled = %q", TaskStateCanceled)
	}
	if TaskStateInputNeeded != "input-needed" {
		t.Fatalf("TaskStateInputNeeded = %q", TaskStateInputNeeded)
	}
}

func TestA2ATaskStore_CreateAndGet(t *testing.T) {
	store, cleanup := newTestA2ATaskStore(t)
	defer cleanup()

	task := &Task{
		ID:        testA2ATaskID("create-get"),
		ContextID: "ctx-create-get",
		Agent:     "ragnar",
		Status:    TaskStatus{State: TaskStateSubmitted},
		Metadata:  map[string]interface{}{"source": "test"},
		Artifacts: []Artifact{{ArtifactID: "artifact-1", Parts: []Part{{Text: "hello"}}}},
	}
	if err := store.CreateTask(task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	got, err := store.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got == nil {
		t.Fatalf("GetTask returned nil task")
	}
	if got.ID != task.ID || got.ContextID != task.ContextID || got.Agent != task.Agent {
		t.Fatalf("GetTask mismatch: %+v", got)
	}
	if got.Status.State != TaskStateSubmitted {
		t.Fatalf("status state = %q", got.Status.State)
	}
	if len(got.Artifacts) != 1 || got.Artifacts[0].ArtifactID != "artifact-1" {
		t.Fatalf("artifacts = %+v", got.Artifacts)
	}
	if got.Metadata["source"] != "test" {
		t.Fatalf("metadata = %+v", got.Metadata)
	}
}

func TestA2ATaskStore_ListByState(t *testing.T) {
	store, cleanup := newTestA2ATaskStore(t)
	defer cleanup()

	tasks := []*Task{
		{ID: testA2ATaskID("list-submitted"), ContextID: "ctx-list", Agent: "ragnar", Status: TaskStatus{State: TaskStateSubmitted}},
		{ID: testA2ATaskID("list-working"), ContextID: "ctx-list", Agent: "freya", Status: TaskStatus{State: TaskStateWorking}},
		{ID: testA2ATaskID("list-completed"), ContextID: "ctx-list", Agent: "ivar", Status: TaskStatus{State: TaskStateCompleted}},
	}
	for _, task := range tasks {
		if err := store.CreateTask(task); err != nil {
			t.Fatalf("CreateTask(%s): %v", task.ID, err)
		}
	}
	got, err := store.ListTasks(TaskFilter{State: TaskStateWorking, Limit: 10})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListTasks returned %d tasks", len(got))
	}
	if got[0].ID != tasks[1].ID {
		t.Fatalf("ListTasks returned %+v", got[0])
	}
}

func TestA2ATaskStore_UpdateStatus(t *testing.T) {
	store, cleanup := newTestA2ATaskStore(t)
	defer cleanup()

	task := &Task{
		ID:        testA2ATaskID("update-status"),
		ContextID: "ctx-update",
		Agent:     "ragnar",
		Status:    TaskStatus{State: TaskStateSubmitted},
	}
	if err := store.CreateTask(task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := store.UpdateTaskStatus(task.ID, TaskStatus{
		State:     TaskStateCompleted,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("UpdateTaskStatus: %v", err)
	}
	got, err := store.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got == nil {
		t.Fatalf("GetTask returned nil task")
	}
	if got.Status.State != TaskStateCompleted {
		t.Fatalf("status state = %q", got.Status.State)
	}
}

func TestA2ANotifyHandler(t *testing.T) {
	store := &A2ATaskStore{}
	var updated TaskStatus
	var artifacts []Artifact
	var messages []Message
	store.getTaskOverride = func(id string) (*Task, error) {
		if id != "task-123" {
			return nil, nil
		}
		return &Task{
			ID:        "task-123",
			ContextID: "ctx-123",
			Agent:     "ragnar",
			Status:    TaskStatus{State: TaskStateSubmitted},
		}, nil
	}
	store.updateStatusOverride = func(id string, status TaskStatus) error {
		updated = status
		return nil
	}
	store.addArtifactOverride = func(id string, artifact Artifact) error {
		artifacts = append(artifacts, artifact)
		return nil
	}
	store.addMessageOverride = func(taskID string, msg Message) error {
		messages = append(messages, msg)
		return nil
	}

	gw := &gateway{a2aStore: store}
	mux := http.NewServeMux()
	gw.registerA2ARoutes(mux)

	body := `{"taskId":"task-123","status":{"state":"working","timestamp":"2026-05-02T12:00:00Z","message":{"role":"agent","parts":[{"text":"working"}],"messageId":"msg-1"}},"artifacts":[{"artifactId":"artifact-1","parts":[{"text":"done"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/a2a/notify", strings.NewReader(body))
	req = req.WithContext(context.Background())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if updated.State != TaskStateWorking {
		t.Fatalf("updated state = %q", updated.State)
	}
	if len(artifacts) != 1 || artifacts[0].ArtifactID != "artifact-1" {
		t.Fatalf("artifacts = %+v", artifacts)
	}
	if len(messages) != 1 || messages[0].MessageID != "msg-1" {
		t.Fatalf("messages = %+v", messages)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestA2ANotifyCreatesMissingTask(t *testing.T) {
	store := &A2ATaskStore{}
	var current *Task
	store.getTaskOverride = func(id string) (*Task, error) {
		if current == nil || current.ID != id {
			return nil, nil
		}
		copy := *current
		return &copy, nil
	}
	store.createTaskOverride = func(task *Task) error {
		copy := *task
		current = &copy
		return nil
	}
	store.updateStatusOverride = func(id string, status TaskStatus) error {
		if current == nil || current.ID != id {
			t.Fatalf("updateStatusOverride missing task %q", id)
		}
		current.Status = status
		return nil
	}
	store.addMessageOverride = func(taskID string, msg Message) error {
		return nil
	}

	gw := &gateway{a2aStore: store}
	mux := http.NewServeMux()
	gw.registerA2ARoutes(mux)

	body := `{"taskId":"task-created","status":{"state":"submitted","timestamp":"2026-05-02T12:00:00Z","message":{"role":"system","messageId":"sess-99"}},"metadata":{"agent":"freya","transport":"a2a"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/a2a/notify", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if current == nil {
		t.Fatalf("expected task to be created")
	}
	if current.ID != "task-created" || current.ContextID != "sess-99" {
		t.Fatalf("created task = %+v", current)
	}
	if current.Agent != "freya" {
		t.Fatalf("created agent = %q", current.Agent)
	}
	if current.Status.State != TaskStateSubmitted {
		t.Fatalf("created state = %q", current.Status.State)
	}
}

func newTestA2ATaskStore(t *testing.T) (*A2ATaskStore, func()) {
	t.Helper()
	dbURL := strings.TrimSpace(os.Getenv("A2A_TEST_DB_URL"))
	if dbURL == "" {
		t.Skip("A2A_TEST_DB_URL not set")
	}
	store := &A2ATaskStore{}
	if err := store.Init(dbURL); err != nil {
		t.Fatalf("Init: %v", err)
	}
	cleanupPrefix := "test-a2a-"
	cleanup := func() {
		if store.db == nil {
			return
		}
		if _, err := store.db.Exec(`DELETE FROM a2a_messages WHERE task_id LIKE $1`, cleanupPrefix+"%"); err != nil {
			t.Fatalf("cleanup messages: %v", err)
		}
		if _, err := store.db.Exec(`DELETE FROM a2a_tasks WHERE id LIKE $1`, cleanupPrefix+"%"); err != nil {
			t.Fatalf("cleanup tasks: %v", err)
		}
		_ = store.db.Close()
	}
	if _, err := store.db.Exec(`DELETE FROM a2a_messages WHERE task_id LIKE $1`, cleanupPrefix+"%"); err != nil {
		cleanup()
		t.Fatalf("initial cleanup messages: %v", err)
	}
	if _, err := store.db.Exec(`DELETE FROM a2a_tasks WHERE id LIKE $1`, cleanupPrefix+"%"); err != nil {
		cleanup()
		t.Fatalf("initial cleanup tasks: %v", err)
	}
	return store, cleanup
}

func testA2ATaskID(suffix string) string {
	return fmt.Sprintf("test-a2a-%s-%d", suffix, time.Now().UTC().UnixNano())
}
