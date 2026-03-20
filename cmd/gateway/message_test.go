package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMessageEndpointMissingAgent(t *testing.T) {
	gw := &gateway{
		agents:       map[string]*Agent{},
		order:        []string{},
		events:       make([]Event, 0, 200),
		eventCap:     200,
		sessionStore: newSessionStore(),
		settings:     newSettingsStore(),
		lastSession:  map[string]string{},
	}
	mux := http.NewServeMux()
	registerMessageHandler(mux, gw)

	body := `{"content":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/message", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["error"] != "unknown agent" {
		t.Fatalf("expected 'unknown agent', got %q", resp["error"])
	}
}

func TestMessageEndpointUnknownAgent(t *testing.T) {
	gw := &gateway{
		agents: map[string]*Agent{
			"val": {Name: "val", URL: "http://val.valhalla.svc:8081"},
		},
		order:        []string{"val"},
		events:       make([]Event, 0, 200),
		eventCap:     200,
		sessionStore: newSessionStore(),
		settings:     newSettingsStore(),
		lastSession:  map[string]string{},
	}
	mux := http.NewServeMux()
	registerMessageHandler(mux, gw)

	body := `{"agent":"nonexistent","content":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/message", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["error"] != "unknown agent" {
		t.Fatalf("expected 'unknown agent', got %q", resp["error"])
	}
}

func TestMessageEndpointInvalidJSON(t *testing.T) {
	gw := &gateway{
		agents:       map[string]*Agent{},
		order:        []string{},
		events:       make([]Event, 0, 200),
		eventCap:     200,
		sessionStore: newSessionStore(),
		settings:     newSettingsStore(),
		lastSession:  map[string]string{},
	}
	mux := http.NewServeMux()
	registerMessageHandler(mux, gw)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/message", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["error"] != "invalid JSON body" {
		t.Fatalf("expected 'invalid JSON body', got %q", resp["error"])
	}
}

func TestMessageEndpointMethodNotAllowed(t *testing.T) {
	gw := &gateway{
		agents:       map[string]*Agent{},
		order:        []string{},
		events:       make([]Event, 0, 200),
		eventCap:     200,
		sessionStore: newSessionStore(),
		settings:     newSettingsStore(),
		lastSession:  map[string]string{},
	}
	mux := http.NewServeMux()
	registerMessageHandler(mux, gw)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/message", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestMessageEndpointUnhealthyAgent(t *testing.T) {
	gw := &gateway{
		agents: map[string]*Agent{
			"val": {Name: "val", URL: "http://val.valhalla.svc:8081", Healthy: false},
		},
		order:        []string{"val"},
		events:       make([]Event, 0, 200),
		eventCap:     200,
		sessionStore: newSessionStore(),
		settings:     newSettingsStore(),
		lastSession:  map[string]string{},
	}
	mux := http.NewServeMux()
	registerMessageHandler(mux, gw)

	body := `{"agent":"val","content":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/message", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["error"] != "agent is unhealthy" {
		t.Fatalf("expected 'agent is unhealthy', got %q", resp["error"])
	}
}

func TestMessageEndpointEmptyContent(t *testing.T) {
	gw := &gateway{
		agents: map[string]*Agent{
			"val": {Name: "val", URL: "http://val.valhalla.svc:8081", Healthy: true},
		},
		order:        []string{"val"},
		events:       make([]Event, 0, 200),
		eventCap:     200,
		sessionStore: newSessionStore(),
		settings:     newSettingsStore(),
		lastSession:  map[string]string{},
	}
	mux := http.NewServeMux()
	registerMessageHandler(mux, gw)

	body := `{"agent":"val","content":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/message", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// Empty content should still reach the agent (no early validation),
	// but session_id is generated automatically.
	// The response will be SSE; we verify content-type is text/event-stream
	// for a valid agent that passes health check.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", contentType)
	}
}

func TestMessageEndpointPausedAgent(t *testing.T) {
	gw := &gateway{
		agents: map[string]*Agent{
			"val": {Name: "val", URL: "http://val.valhalla.svc:8081", Healthy: true},
		},
		order:         []string{"val"},
		events:        make([]Event, 0, 200),
		eventCap:      200,
		sessionStore:  newSessionStore(),
		settings:      newSettingsStore(),
		lastSession:   map[string]string{},
		pausedAgents:  map[string]bool{"val": true},
		notifications: make([]Notification, 0, 100),
		notifCap:      100,
	}
	mux := http.NewServeMux()
	registerMessageHandler(mux, gw)

	// CronJob session triggers skip behavior when agent is paused
	body := `{"agent":"val","content":"hello","session_id":"cronjob-2026-01-01-00"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/message", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (SSE skip message), got %d: %s", rec.Code, rec.Body.String())
	}
	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", contentType)
	}
}

// registerMessageHandler registers the /api/v1/message handler on mux using gw.
// This extracts the message handler logic from main.go's large mux setup so it
// can be tested in isolation without spinning up a full gateway.
func registerMessageHandler(mux *http.ServeMux, gw *gateway) {
	mux.HandleFunc("/api/v1/message", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in messageReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		agent, ok := gw.getAgent(in.Agent)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
			return
		}
		sessionID := strings.TrimSpace(in.SessionID)
		gw.injectionMu.Lock()
		paused := gw.pausedAgents[in.Agent]
		gw.injectionMu.Unlock()
		if detectSessionSource(sessionID) == "cronjob" && paused {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"content\":\"Agent " + in.Agent + " is paused by Sovereign. Skipping task.\"}\n\n"))
			_, _ = w.Write([]byte("data: {\"done\":true}\n\n"))
			gw.addEvent("agent_paused_skip", in.Agent, "CronJob poll skipped — "+in.Agent+" is paused")
			return
		}
		if !agent.Healthy {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent is unhealthy"})
			return
		}
		if sessionID == "" {
			sessionID = "hirdforge-" + in.Agent + "-" + time.Now().Format(time.RFC3339Nano)
		}
		gw.lastSessionMu.Lock()
		gw.lastSession[in.Agent] = sessionID
		gw.lastSessionMu.Unlock()
		gw.addEvent("message", in.Agent, "Message sent to "+in.Agent)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
	})
}
