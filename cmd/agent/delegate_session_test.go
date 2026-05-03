package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDelegateToolForwardsSessionIDFromRequestContext(t *testing.T) {
	oldGatewayURL := gatewayURLValue
	defer func() {
		gatewayURLValue = oldGatewayURL
	}()
	var rpcReq jsonRPCRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/health":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"model": "qwen3:30b"})
		case r.Method == http.MethodPost && r.URL.Path == "/a2a":
			if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
				t.Fatalf("decode json-rpc request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{
				ID:      "req-1",
				JSONRPC: "2.0",
				Result: map[string]interface{}{
					"id":        "task-peer",
					"contextId": "sess-comms-42",
					"status": map[string]interface{}{
						"state": "submitted",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/a2a/notify" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer gateway.Close()
	gatewayURLValue = gateway.URL

	tool := &delegateTool{
		peers:      map[string]string{"ivar": server.URL},
		agentName:  "ragnar",
		gatewayURL: gateway.URL,
	}

	result := tool.Execute(map[string]interface{}{
		"agent":       "ivar",
		"task":        "TASK: Update gateway\nDONE WHEN: PR is open",
		"_session_id": "sess-comms-42",
	})
	if result.Error != "" {
		t.Fatalf("delegate Execute error: %s", result.Error)
	}
	if !strings.Contains(result.Output, "task-peer") {
		t.Fatalf("delegate output = %q", result.Output)
	}
	var params a2aSendMessageRequest
	if err := json.Unmarshal(rpcReq.Params, &params); err != nil {
		t.Fatalf("decode json-rpc params: %v", err)
	}
	if params.Message.MessageID != "sess-comms-42" {
		t.Fatalf("forwarded MessageID = %q", params.Message.MessageID)
	}
	if params.PushNotification == nil || params.PushNotification.URL != gateway.URL+"/api/v1/a2a/notify" {
		t.Fatalf("push notification = %#v", params.PushNotification)
	}
}

func TestTaskStatusAndResultToolsUseGatewayA2AStore(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/a2a/tasks/task-123" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"task-123","agent":"ivar","status":{"state":"completed"},"artifacts":[{"artifactId":"artifact-1","parts":[{"text":"done"}]}],"created_at":"2026-05-02T12:00:00Z","updated_at":"2026-05-02T12:05:00Z"}`))
	}))
	defer gateway.Close()

	statusTool := &taskStatusTool{gatewayURL: gateway.URL}
	statusResult := statusTool.Execute(map[string]interface{}{"task_id": "task-123"})
	if statusResult.Error != "" {
		t.Fatalf("task_status error: %s", statusResult.Error)
	}
	if !strings.Contains(statusResult.Output, `"agent":"ivar"`) || !strings.Contains(statusResult.Output, `"state":"completed"`) {
		t.Fatalf("task_status output = %q", statusResult.Output)
	}

	resultTool := &taskResultTool{gatewayURL: gateway.URL}
	result := resultTool.Execute(map[string]interface{}{"task_id": "task-123"})
	if result.Error != "" {
		t.Fatalf("task_result error: %s", result.Error)
	}
	if !strings.Contains(result.Output, `"artifactId":"artifact-1"`) || !strings.Contains(result.Output, `"text":"done"`) {
		t.Fatalf("task_result output = %q", result.Output)
	}
}
