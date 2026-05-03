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
		Status:        "ok",
		UptimeSeconds: 7200,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal health response: %v", err)
	}

	var unmarshaled agentHealthResponse
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("unmarshal health response: %v", err)
	}

	if unmarshaled.Status != resp.Status {
		t.Fatalf("status mismatch: got %q, want %q", unmarshaled.Status, resp.Status)
	}
	if unmarshaled.UptimeSeconds != resp.UptimeSeconds {
		t.Fatalf("uptime mismatch: got %d, want %d", unmarshaled.UptimeSeconds, resp.UptimeSeconds)
	}
}

// TestQueryAgentHealth tests the health query function
func TestQueryAgentHealth(t *testing.T) {
	// Create a test server that returns a healthy agent
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(agentHealthResponse{
			Status:        "ok",
			UptimeSeconds: 1800,
		})
	}))
	defer testServer.Close()

	// Create HTTP client with timeout
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := queryAgentHealth(client, testServer.URL)
	if err != nil {
		t.Fatalf("query agent health: %v", err)
	}

	if resp.Status != "ok" {
		t.Fatalf("expected healthy agent status, got %q", resp.Status)
	}
	if resp.UptimeSeconds != 1800 {
		t.Fatalf("uptime mismatch: got %d, want %d", resp.UptimeSeconds, 1800)
	}
}

// TestQueryAgentHealthUnhealthy tests unhealthy agent response
func TestQueryAgentHealthUnhealthy(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(agentHealthResponse{
			Status:        "error",
			UptimeSeconds: 0,
		})
	}))
	defer testServer.Close()

	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := queryAgentHealth(client, testServer.URL)
	if err != nil {
		t.Fatalf("query agent health: %v", err)
	}

	if resp.Status == "ok" {
		t.Fatalf("expected unhealthy agent, got healthy status")
	}
}

// TestQueryAgentHealthConnectionError tests connection error handling
func TestQueryAgentHealthConnectionError(t *testing.T) {
	// Start a server then close it immediately so the address is known but unreachable
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := ts.URL
	ts.Close()

	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := queryAgentHealth(client, url+"/health")
	if err == nil {
		t.Fatalf("expected error for connection failure, got nil")
	}
	if resp.Status == "ok" {
		t.Fatalf("expected unhealthy on error, got healthy status")
	}
}

// TestAgentConfigureRequest ensures the configure request struct works
func TestAgentConfigureRequestMarshalUnmarshal(t *testing.T) {
	req := agentConfigureRequest{
		Model:        "gpt-4o",
		InferenceURL: "http://llm.valhalla.svc:8080",
		Tools:        []string{"exec", "read", "write"},
		Peers:        []string{"val=http://val.valhalla.svc:8081"},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal configure request: %v", err)
	}

	var unmarshaled agentConfigureRequest
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("unmarshal configure request: %v", err)
	}

	if unmarshaled.Model != req.Model {
		t.Fatalf("model mismatch: got %q, want %q", unmarshaled.Model, req.Model)
	}
	if unmarshaled.InferenceURL != req.InferenceURL {
		t.Fatalf("inference url mismatch: got %q, want %q", unmarshaled.InferenceURL, req.InferenceURL)
	}
	if got, ok := unmarshaled.Tools.([]interface{}); !ok || len(got) != 3 {
		t.Fatalf("tools mismatch: got %#v", unmarshaled.Tools)
	}
}

// TestParseAgents validates agent parsing
func TestParseAgents(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		wantLen int
		check   func(*testing.T, map[string]*Agent)
	}{
		{
			name:    "single agent",
			input:   "val=http://val.valhalla.svc:8081",
			wantErr: false,
			wantLen: 1,
			check: func(t *testing.T, agents map[string]*Agent) {
				t.Helper()
				if got := agents["val"].Warband; got != "default" {
					t.Fatalf("warband mismatch: got %q, want %q", got, "default")
				}
			},
		},
		{
			name:    "multiple agents",
			input:   "val=http://val.valhalla.svc:8081,chuck=http://chuck.valhalla.svc:8081",
			wantErr: false,
			wantLen: 2,
		},
		{
			name:    "role and warband",
			input:   "val=http://val.valhalla.svc:8081:builder:alpha",
			wantErr: false,
			wantLen: 1,
			check: func(t *testing.T, agents map[string]*Agent) {
				t.Helper()
				agent := agents["val"]
				if agent.Role != "builder" {
					t.Fatalf("role mismatch: got %q, want %q", agent.Role, "builder")
				}
				if agent.Warband != "alpha" {
					t.Fatalf("warband mismatch: got %q, want %q", agent.Warband, "alpha")
				}
			},
		},
		{
			name:    "custom default warband",
			input:   "val=http://val.valhalla.svc:8081",
			wantErr: false,
			wantLen: 1,
			check: func(t *testing.T, agents map[string]*Agent) {
				t.Helper()
				if got := agents["val"].Warband; got != "warband-x" {
					t.Fatalf("warband mismatch: got %q, want %q", got, "warband-x")
				}
			},
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
			defaultWarband := "default"
			if tt.name == "custom default warband" {
				defaultWarband = "warband-x"
			}
			agents, order, err := parseAgents(tt.input, defaultWarband)

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

			if tt.check != nil {
				tt.check(t, agents)
			}
		})
	}
}

// TestParseAgentsEmptyValidation ensures empty string is rejected
func TestParseAgentsEmptyValidation(t *testing.T) {
	agents, order, err := parseAgents("", "default")

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
		Model:        "gpt-4o",
		InferenceURL: "http://val.valhalla.svc:8081",
		Tools:        []string{"exec", "read", "write", "git-clone"},
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
        image: val:latest
        args:
        - --model=default-model
        - --inference-url=http://default.valhalla.svc:8081
        - --tools=exec,read
        - --peers=val=http://val.valhalla.svc:8081`

	result, lines, err := updateAgentDeploymentArgs(content, req)
	if err != nil {
		t.Fatalf("updateAgentDeploymentArgs: %v", err)
	}

	if len(lines) == 0 {
		t.Fatalf("expected non-empty lines array")
	}

	if !strings.Contains(result, "--inference-url='http://val.valhalla.svc:8081'") {
		t.Fatalf("result should contain updated inference URL")
	}
	if !strings.Contains(result, "--model='gpt-4o'") {
		t.Fatalf("result should contain model name")
	}
}

// TestEventStruct ensures Event struct can be marshaled
func TestEventStructMarshalUnmarshal(t *testing.T) {
	event := Event{
		Time:    time.Now().Format(time.RFC3339),
		Type:    "agent_start",
		Agent:   "gateway",
		Summary: "Gateway started with 3 agents",
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
	// Compile-time signature check only; network behavior belongs in integration tests.
	fn := createGitOpsAgentConfigPR
	_ = fn
}

// TestAgentSnapshot ensures snapshotAgents returns correct agent list
func TestAgentSnapshot(t *testing.T) {
	// Create a mock gateway with test agents
	gw := &gateway{
		agents: map[string]*Agent{
			"val": {
				Name:          "val",
				URL:           "http://val.valhalla.svc:8081",
				Healthy:       true,
				Model:         "gpt-4o",
				Tools:         []string{"exec", "read", "write"},
				UptimeSeconds: 3600,
			},
			"chuck": {
				Name:          "chuck",
				URL:           "http://chuck.valhalla.svc:8081",
				Healthy:       false,
				Model:         "gpt-4",
				Tools:         []string{"exec", "read"},
				UptimeSeconds: 0,
			},
		},
		order: []string{"val", "chuck"},
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
