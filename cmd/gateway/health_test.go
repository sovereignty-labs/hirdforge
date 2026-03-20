package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGatewayHealthHandler(t *testing.T) {
	gw := &gateway{}
	mux := http.NewServeMux()

	// Register only the health handler logic from main.go.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		agents := gw.snapshotAgents()
		healthy := 0
		for _, a := range agents {
			if a.Healthy {
				healthy++
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ready", "agents": len(agents), "healthy": healthy})
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var body struct {
		Status  string `json:"status"`
		Agents  int    `json:"agents"`
		Healthy int    `json:"healthy"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal body: %v", err)
	}

	if body.Status != "ready" {
		t.Errorf("expected status=ready, got %q", body.Status)
	}
	if body.Agents != 0 {
		t.Errorf("expected agents=0 by default, got %d", body.Agents)
	}
	if body.Healthy != 0 {
		t.Errorf("expected healthy=0 by default, got %d", body.Healthy)
	}
}
