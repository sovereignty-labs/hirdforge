package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExtractAgentName(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]interface{}
		want    string
		wantKey bool
	}{
		{
			name:    "extracts and trims",
			args:    map[string]interface{}{"_agent_name": "  jeeves  ", "other": "value"},
			want:    "jeeves",
			wantKey: false,
		},
		{
			name:    "missing key",
			args:    map[string]interface{}{"other": "value"},
			want:    "",
			wantKey: false,
		},
		{
			name:    "empty after trim",
			args:    map[string]interface{}{"_agent_name": "   "},
			want:    "",
			wantKey: false,
		},
		{
			name:    "nil value",
			args:    map[string]interface{}{"_agent_name": nil},
			want:    "",
			wantKey: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAgentName(tt.args)
			if got != tt.want {
				t.Fatalf("extractAgentName()=%q want %q", got, tt.want)
			}
			_, exists := tt.args["_agent_name"]
			if exists != tt.wantKey {
				t.Fatalf("_agent_name key exists=%v want %v", exists, tt.wantKey)
			}
		})
	}
}

func TestListQueuesIncludesAgentNameWhenSet(t *testing.T) {
	state := &lockboxState{
		queues: map[string]*WriteQueue{
			"q-1": {
				QueueID:   "q-1",
				HuntID:    "hunt-1",
				Service:   "mcp",
				Action:    "gmail_send_email",
				AgentName: "jeeves",
				Params:    map[string]interface{}{"to": "kit@example.com"},
				Status:    "pending",
				QueuedAt:  time.Now().UTC(),
			},
			"q-2": {
				QueueID:  "q-2",
				HuntID:   "hunt-2",
				Service:  "mcp",
				Action:   "gmail_send_email",
				Params:   map[string]interface{}{"to": "kit@example.com"},
				Status:   "pending",
				QueuedAt: time.Now().UTC().Add(1 * time.Second),
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/queues", nil)
	rec := httptest.NewRecorder()
	state.listQueues(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("listQueues status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Queues []map[string]interface{} `json:"queues"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	var withAgent map[string]interface{}
	var withoutAgent map[string]interface{}
	for _, item := range resp.Queues {
		switch item["queue_id"] {
		case "q-1":
			withAgent = item
		case "q-2":
			withoutAgent = item
		}
	}

	if got := withAgent["agent_name"]; got != "jeeves" {
		t.Fatalf("agent_name=%#v want %q", got, "jeeves")
	}
	if _, exists := withoutAgent["agent_name"]; exists {
		t.Fatalf("expected agent_name to be omitted when empty, got %#v", withoutAgent["agent_name"])
	}
}
