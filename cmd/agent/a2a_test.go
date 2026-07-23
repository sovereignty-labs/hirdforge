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

	tasklifepkg "git.hirdforge.com/kit/hirdforge/pkg/tasklife"
	taskspkg "git.hirdforge.com/kit/hirdforge/pkg/tasks"
)

func TestJSONRPCMessageSend(t *testing.T) {
	runtime := newA2ARuntime("ragnar", "", func(ctx context.Context, sessionID, taskID, content string, emit func(interface{}) bool, logTool func(taskspkg.ToolLog)) (string, error) {
		return "done", nil
	})
	reqBody := `{"jsonrpc":"2.0","id":"req-1","method":"message/send","params":{"message":{"role":"user","parts":[{"text":"hello"}],"messageId":"ctx-123"}}}`
	req := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	runtime.handleJSONRPC(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /a2a status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp jsonRPCResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("json-rpc error: %+v", resp.Error)
	}
	body, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var task a2aTask
	if err := json.Unmarshal(body, &task); err != nil {
		t.Fatalf("unmarshal task: %v", err)
	}
	if strings.TrimSpace(task.ID) == "" {
		t.Fatalf("expected task id")
	}
	if task.Status.State != a2aTaskStateSubmitted {
		t.Fatalf("task state = %q", task.Status.State)
	}
}

func TestAgentCardV1Format(t *testing.T) {
	oldEnabledTools := append([]string(nil), enabledTools...)
	oldPortValue := portValue
	oldGRPCPortValue := grpcPortValue
	defer func() {
		enabledTools = oldEnabledTools
		portValue = oldPortValue
		grpcPortValue = oldGRPCPortValue
	}()
	enabledTools = []string{"exec", "read", "write"}
	portValue = "8081"
	grpcPortValue = "8082"

	mux := http.NewServeMux()
	registerRoutes(mux, serverDeps{
		workspace:        ".",
		agentName:        "ragnar",
		agentDescription: "Executes code and coordinates tools",
		processConversation: func(ctx context.Context, sessionID, taskID, content string, emit func(interface{}) bool, logTool func(taskspkg.ToolLog)) (string, error) {
			return "", nil
		},
		taskStore:           taskspkg.NewStore(),
		taskTracker:         tasklifepkg.NewTaskTracker(),
		sovereignStates:     map[string]bool{},
		sovereignReporter:   tasklifepkg.NewSovereignReporter("", "ragnar", false),
		taskCancelMu:        new(sync.Mutex),
		taskCancels:         map[string]context.CancelFunc{},
		completionMaxNudges: 1,
		reviewTracker:       newReviewContextTracker("ragnar", "", time.Minute),
		a2aRuntime: newA2ARuntime("ragnar", "", func(ctx context.Context, sessionID, taskID, content string, emit func(interface{}) bool, logTool func(taskspkg.ToolLog)) (string, error) {
			return "", nil
		}),
	})
	req := httptest.NewRequest(http.MethodGet, "http://agent.example/.well-known/agent.json", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent card status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var card map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	if got := strings.TrimSpace(fmt.Sprint(card["version"])); got != "1.0.0" {
		t.Fatalf("version = %q", got)
	}
	interfaces, ok := card["interfaces"].([]interface{})
	if !ok || len(interfaces) != 2 {
		t.Fatalf("interfaces = %#v", card["interfaces"])
	}
	if got := strings.TrimSpace(fmt.Sprint(card["url"])); got != "http://agent.example:8081" {
		t.Fatalf("url = %q", got)
	}
}

func TestToolCallEventShape(t *testing.T) {
	// The gateway's deriveTypedEventFromToolCall reads
	// metadata.tool_call.{name,arguments}. Lock that contract from the agent
	// side so a future sseChunk refactor can't silently break the projector
	// pipeline again.
	events := make(chan map[string]interface{}, 4)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/events" {
			http.NotFound(w, r)
			return
		}
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode event payload: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		events <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()

	runtime := newA2ARuntime("ragnar", gateway.URL, func(ctx context.Context, sessionID, taskID, content string, emit func(interface{}) bool, logTool func(taskspkg.ToolLog)) (string, error) {
		emit(sseChunk{
			Type: "tool_call",
			Tool: "read",
			Args: map[string]interface{}{"path": "cmd/gateway/main.go"},
			Done: false,
		})
		return "ok", nil
	})

	_, done, err := runtime.submit(a2aSendMessageRequest{
		Message: a2aMessage{
			Role:      "user",
			Parts:     []a2aPart{{Text: "go"}},
			MessageID: "ctx-tool-call",
		},
	}, nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("task did not complete")
	}

	var toolCallEvt map[string]interface{}
	deadline := time.After(2 * time.Second)
	for toolCallEvt == nil {
		select {
		case evt := <-events:
			if t, _ := evt["type"].(string); t == "tool_call" {
				toolCallEvt = evt
			}
		case <-deadline:
			t.Fatalf("did not receive tool_call event; got %d others", len(events))
		}
	}

	meta, _ := toolCallEvt["metadata"].(map[string]interface{})
	if meta == nil {
		t.Fatalf("metadata missing: %#v", toolCallEvt)
	}
	tc, _ := meta["tool_call"].(map[string]interface{})
	if tc == nil {
		t.Fatalf("metadata.tool_call missing or wrong shape: %#v", meta)
	}
	if name, _ := tc["name"].(string); name != "read" {
		t.Fatalf("metadata.tool_call.name = %q, want %q", name, "read")
	}
	args, _ := tc["arguments"].(map[string]interface{})
	if args == nil {
		t.Fatalf("metadata.tool_call.arguments missing or not a map: %#v", tc)
	}
	if path, _ := args["path"].(string); path != "cmd/gateway/main.go" {
		t.Fatalf("metadata.tool_call.arguments.path = %q", path)
	}
	if sid, _ := meta["session_id"].(string); sid != "ctx-tool-call" {
		t.Fatalf("metadata.session_id = %q", sid)
	}
}

func TestPushNotification(t *testing.T) {
	pushes := make(chan a2aPushPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload a2aPushPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode push payload: %v", err)
		}
		pushes <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	runtime := newA2ARuntime("ragnar", "", func(ctx context.Context, sessionID, taskID, content string, emit func(interface{}) bool, logTool func(taskspkg.ToolLog)) (string, error) {
		return "finished", nil
	})
	_, done, err := runtime.submit(a2aSendMessageRequest{
		Message: a2aMessage{
			Role: "user",
			Parts: []a2aPart{{
				Text: "ship it",
			}},
		},
		PushNotification: &a2aPushNotificationConfig{URL: server.URL},
	}, nil)
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("task did not complete")
	}
	select {
	case push := <-pushes:
		if push.Status.State != a2aTaskStateCompleted {
			t.Fatalf("push state = %q", push.Status.State)
		}
		if len(push.Artifacts) != 1 || strings.TrimSpace(push.Artifacts[0].Parts[0].Text) != "finished" {
			t.Fatalf("push artifacts = %#v", push.Artifacts)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("push notification not received")
	}
}
