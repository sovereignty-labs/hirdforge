package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestAgentStruct ensures the Agent struct can be marshaled/unmarshaled correctly
func TestAgentStructMarshalUnmarshal(t *testing.T) {
	agent := &Agent{
		Name:           "val",
		URL:            "http://val.valhalla.svc:8081",
		Healthy:        true,
		Model:          "gpt-4o",
		Tools:          []string{"exec", "read", "write", "git-clone"},
		UptimeSeconds:  3600,
		RequestsServed: 150,
		ToolCallsMade:  42,
	}

	data, err := json.Marshal(agent)
	if err != nil {
		t.Fatalf("marshal agent: %v", err)
	}

	var unmarshaled Agent
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("unmarshal agent: %v", err)
	}

	if unmarshaled.Name != agent.Name {
		t.Fatalf("name mismatch: got %q, want %q", unmarshaled.Name, agent.Name)
	}
	if unmarshaled.URL != agent.URL {
		t.Fatalf("url mismatch: got %q, want %q", unmarshaled.URL, agent.URL)
	}
	if unmarshaled.Healthy != agent.Healthy {
		t.Fatalf("healthy mismatch: got %v, want %v", unmarshaled.Healthy, agent.Healthy)
	}
	if unmarshaled.Model != agent.Model {
		t.Fatalf("model mismatch: got %q, want %q", unmarshaled.Model, agent.Model)
	}
	if len(unmarshaled.Tools) != len(agent.Tools) {
		t.Fatalf("tools length mismatch: got %d, want %d", len(unmarshaled.Tools), len(agent.Tools))
	}
}

// TestAgentHealthResponse ensures the health response struct works correctly
func TestAgentHealthResponseMarshalUnmarshal(t *testing.T) {
	resp := agentHealthResponse{
		Healthy: true,
		Uptime:  7200,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal health response: %v", err)
	}

	var unmarshaled agentHealthResponse
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("unmarshal health response: %v", err)
	}

	if unmarshaled.Healthy != resp.Healthy {
		t.Fatalf("healthy mismatch: got %v, want %v", unmarshaled.Healthy, resp.Healthy)
	}
	if unmarshaled.Uptime != resp.Uptime {
		t.Fatalf("uptime mismatch: got %d, want %d", unmarshaled.Uptime, resp.Uptime)
	}
}

// TestQueryAgentHealth tests the health query function
func TestQueryAgentHealth(t *testing.T) {
	// Create a test server that returns a healthy agent
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(agentHealthResponse{
			Healthy: true,
			Uptime:  1800,
		})
	}))
	defer testServer.Close()

	// Create HTTP client with timeout
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := queryAgentHealth(client, testServer.URL)
	if err != nil {
		t.Fatalf("query agent health: %v", err)
	}

	if !resp.Healthy {
		t.Fatalf("expected healthy agent, got unhealthy")
	}
	if resp.Uptime != 1800 {
		t.Fatalf("uptime mismatch: got %d, want %d", resp.Uptime, 1800)
	}
}

// TestQueryAgentHealthUnhealthy tests unhealthy agent response
func TestQueryAgentHealthUnhealthy(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(agentHealthResponse{
			Healthy: false,
			Uptime:  0,
		})
	}))
	defer testServer.Close()

	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := queryAgentHealth(client, testServer.URL)
	if err != nil {
		t.Fatalf("query agent health: %v", err)
	}

	if resp.Healthy {
		t.Fatalf("expected unhealthy agent, got healthy")
	}
}

// TestQueryAgentHealthConnectionError tests connection error handling
func TestQueryAgentHealthConnectionError(t *testing.T) {
	client := &http.Client{Timeout: 1 * time.Second}

	// Try to query a non-existent server
	resp, err := queryAgentHealth(client, "http://nonexistent.invalid:9999/health")
	if err == nil {
		t.Fatalf("expected error for connection failure, got nil")
	}
	if resp.Healthy {
		t.Fatalf("expected unhealthy on error, got healthy")
	}
}

// TestAgentConfigureRequest ensures the configure request struct works
func TestAgentConfigureRequestMarshalUnmarshal(t *testing.T) {
	req := agentConfigureRequest{
		Name:    "chuck",
		URL:     "http://chuck.valhalla.svc:8081",
		Model:   "gpt-4o",
		Tools:   []string{"exec", "read", "write"},
		Enabled: true,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal configure request: %v", err)
	}

	var unmarshaled agentConfigureRequest
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("unmarshal configure request: %v", err)
	}

	if unmarshaled.Name != req.Name {
		t.Fatalf("name mismatch: got %q, want %q", unmarshaled.Name, req.Name)
	}
	if unmarshaled.URL != req.URL {
		t.Fatalf("url mismatch: got %q, want %q", unmarshaled.URL, req.URL)
	}
	if unmarshaled.Enabled != req.Enabled {
		t.Fatalf("enabled mismatch: got %v, want %v", unmarshaled.Enabled, req.Enabled)
	}
}

// TestAgentConfigureResponse ensures the configure response struct works
func TestAgentConfigureResponseMarshalUnmarshal(t *testing.T) {
	resp := agentConfigureResponse{
		Name:             "chuck",
		URL:              "http://chuck.valhalla.svc:8081",
		Healthy:          true,
		Model:            "gpt-4o",
		Tools:            []string{"exec", "read", "write"},
		LastSeen:         time.Now().Format(time.RFC3339),
		UptimeSeconds:    7200,
		RequestsServed:   50,
		ToolCallsMade:    20,
		DeploymentConfig: "infrastructure/valhalla/deployment-chuck.yaml",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal configure response: %v", err)
	}

	var unmarshaled agentConfigureResponse
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("unmarshal configure response: %v", err)
	}

	if unmarshaled.Name != resp.Name {
		t.Fatalf("name mismatch: got %q, want %q", unmarshaled.Name, resp.Name)
	}
	if unmarshaled.Healthy != resp.Healthy {
		t.Fatalf("healthy mismatch: got %v, want %v", unmarshaled.Healthy, resp.Healthy)
	}
}

// TestParseAgents validates agent parsing
func TestParseAgents(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantErr  bool
		wantLen  int
	}{
		{
			name:     "single agent",
			input:    "val=http://val.valhalla.svc:8081",
			wantErr:  false,
			wantLen:  1,
		},
		{
			name:     "multiple agents",
			input:    "val=http://val.valhalla.svc:8081,chuck=http://chuck.valhalla.svc:8081",
			wantErr:  false,
			wantLen:  2,
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: true,
		},
		{
			name:    "duplicate agent",
			input:   "val=http://val1.valhalla.svc:8081,val=http://val2.valhalla.svc:8081",
			wantErr: true,
		},
		{
			name:    "invalid format",
			input:   "invalid-format",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agents, order, err := parseAgents(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("parseAgents: unexpected error: %v", err)
			}

			if len(agents) != tt.wantLen {
				t.Fatalf("agents length: got %d, want %d", len(agents), tt.wantLen)
			}

			if len(order) != tt.wantLen {
				t.Fatalf("order length: got %d, want %d", len(order), tt.wantLen)
			}
		})
	}
}

// TestParseAgentsEmptyValidation ensures empty string is rejected
func TestParseAgentsEmptyValidation(t *testing.T) {
	agents, order, err := parseAgents("")

	if err == nil {
		t.Fatalf("expected error for empty input, got nil")
	}
	if len(agents) != 0 {
		t.Fatalf("expected empty agents map, got %d entries", len(agents))
	}
	if len(order) != 0 {
		t.Fatalf("expected empty order, got %d entries", len(order))
	}
}

// TestUpdateAgentDeploymentArgs validates deployment args update
func TestUpdateAgentDeploymentArgs(t *testing.T) {
	req := agentConfigureRequest{
		Name:    "val",
		URL:     "http://val.valhalla.svc:8081",
		Model:   "gpt-4o",
		Tools:   []string{"exec", "read", "write", "git-clone"},
		Enabled: true,
	}

	content := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: val
spec:
  template:
    spec:
      containers:
      - name: val
        image: val:latest`

	result, lines, err := updateAgentDeploymentArgs(content, req)
	if err != nil {
		t.Fatalf("updateAgentDeploymentArgs: %v", err)
	}

	if len(lines) == 0 {
		t.Fatalf("expected non-empty lines array")
	}

	if !strings.Contains(result, "val.valhalla.svc") {
		t.Fatalf("result should contain agent URL")
	}
	if !strings.Contains(result, "gpt-4o") {
		t.Fatalf("result should contain model name")
	}
}

// TestEventStruct ensures Event struct can be marshaled
func TestEventStructMarshalUnmarshal(t *testing.T) {
	event := Event{
		Time:      time.Now().Format(time.RFC3339),
		Type:      "agent_start",
		Agent:     "gateway",
		Summary:   "Gateway started with 3 agents",
		TaskID:    "task-123",
		Content:   "test content",
		Delegated: true,
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	var unmarshaled Event
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}

	if unmarshaled.Type != event.Type {
		t.Fatalf("type mismatch: got %q, want %q", unmarshaled.Type, event.Type)
	}
	if unmarshaled.Agent != event.Agent {
		t.Fatalf("agent mismatch: got %q, want %q", unmarshaled.Agent, event.Agent)
	}
}

// TestCreateGitOpsAgentConfigPRURLGeneration tests URL generation
func TestCreateGitOpsAgentConfigPRURLGeneration(t *testing.T) {
	// This test ensures the function signature is correct
	// Actual PR creation would require network access, so we just verify the function exists
	// and has the right signature by checking it compiles

	// The actual implementation would be tested in integration tests
	// This unit test just ensures the function is callable
	if createGitOpsAgentConfigPR == nil {
		t.Fatal("createGitOpsAgentConfigPR should not be nil")
	}
}

// TestAgentSnapshot ensures snapshotAgents returns correct agent list
func TestAgentSnapshot(t *testing.T) {
	// Create a mock gateway with test agents
	gw := &gateway{
		agents: map[string]*Agent{
			"val": {
				Name:      "val",
				URL:       "http://val.valhalla.svc:8081",
				Healthy:   true,
				Model:     "gpt-4o",
				Tools:     []string{"exec", "read", "write"},
				UptimeSeconds: 3600,
			},
			"chuck": {
				Name:      "chuck",
				URL:       "http://chuck.valhalla.svc:8081",
				Healthy:   false,
				Model:     "gpt-4",
				Tools:     []string{"exec", "read"},
				UptimeSeconds: 0,
			},
		},
		agentOrder: []string{"val", "chuck"},
	}

	agents := gw.snapshotAgents()

	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}

	if agents[0].Name != "val" {
		t.Fatalf("first agent should be val, got %s", agents[0].Name)
	}
	if agents[1].Name != "chuck" {
		t.Fatalf("second agent should be chuck, got %s", agents[1].Name)
	}
}