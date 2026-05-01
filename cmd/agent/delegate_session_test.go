package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDelegateToolForwardsSessionIDFromRequestContext(t *testing.T) {
	var sent taskSendRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/tasks/send":
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Fatalf("decode task send request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "task-peer", "status": "submitted"})
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/task-peer":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "completed", "result": "done"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tool := &delegateTool{
		peers:     map[string]string{"ivar": server.URL},
		agentName: "ragnar",
	}

	result := tool.Execute(map[string]interface{}{
		"agent":       "ivar",
		"task":        "TASK: Update gateway\nDONE WHEN: PR is open",
		"_session_id": "sess-comms-42",
	})
	if result.Error != "" {
		t.Fatalf("delegate Execute error: %s", result.Error)
	}
	if sent.SessionID != "sess-comms-42" {
		t.Fatalf("forwarded SessionID = %q", sent.SessionID)
	}
	if sent.From != "ragnar" {
		t.Fatalf("forwarded From = %q", sent.From)
	}
}
