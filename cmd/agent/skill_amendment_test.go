package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type skillAmendmentTestState struct {
	mu                sync.Mutex
	amendments        []map[string]interface{}
	validateCalls     []map[string]interface{}
	returnQueryErr    bool
	returnValidateErr bool
}

func TestValidateSkillAmendmentsAsyncTransitions(t *testing.T) {
	t.Run("confirms unvalidated amendment once", func(t *testing.T) {
		state := &skillAmendmentTestState{
			amendments: []map[string]interface{}{
				{
					"id":               "amend-1",
					"type":             "skill_amendment",
					"status":           "unvalidated",
					"failure_pattern":  "git-clone already exists",
					"validation_count": 2,
					"created_at":       "2026-06-01T14:00:00Z",
				},
			},
		}
		server := newSkillAmendmentTestServer(t, state)
		defer server.Close()

		validateSkillAmendmentsAsync(server.URL, "ragnar")

		state.mu.Lock()
		defer state.mu.Unlock()
		if len(state.validateCalls) != 1 {
			t.Fatalf("validate call count = %d, want 1", len(state.validateCalls))
		}
		call := state.validateCalls[0]
		if got := metadataString(call, "memory_id"); got != "amend-1" {
			t.Fatalf("validate memory_id = %q, want amend-1", got)
		}
		if got := metadataString(call, "outcome"); got != "confirmed" {
			t.Fatalf("validate outcome = %q, want confirmed", got)
		}
	})

	t.Run("skips contradicted amendment", func(t *testing.T) {
		state := &skillAmendmentTestState{
			amendments: []map[string]interface{}{
				{
					"id":               "amend-2",
					"type":             "skill_amendment",
					"status":           "contradicted",
					"failure_pattern":  "git-clone already exists",
					"validation_count": 1,
					"created_at":       "2026-06-01T14:00:00Z",
				},
			},
		}
		server := newSkillAmendmentTestServer(t, state)
		defer server.Close()

		validateSkillAmendmentsAsync(server.URL, "ragnar")

		state.mu.Lock()
		defer state.mu.Unlock()
		if len(state.validateCalls) != 0 {
			t.Fatalf("validate call count = %d, want 0", len(state.validateCalls))
		}
	})

	t.Run("skips already validated amendment", func(t *testing.T) {
		state := &skillAmendmentTestState{
			amendments: []map[string]interface{}{
				{
					"id":               "amend-3",
					"type":             "skill_amendment",
					"status":           "unvalidated",
					"failure_pattern":  "git-clone already exists",
					"validation_count": 3,
					"created_at":       "2026-06-01T14:00:00Z",
				},
			},
		}
		server := newSkillAmendmentTestServer(t, state)
		defer server.Close()

		validateSkillAmendmentsAsync(server.URL, "ragnar")

		state.mu.Lock()
		defer state.mu.Unlock()
		if len(state.validateCalls) != 0 {
			t.Fatalf("validate call count = %d, want 0", len(state.validateCalls))
		}
	})

	t.Run("survives seidr error", func(t *testing.T) {
		state := &skillAmendmentTestState{
			amendments: []map[string]interface{}{
				{
					"id":               "amend-4",
					"type":             "skill_amendment",
					"status":           "unvalidated",
					"failure_pattern":  "git-clone already exists",
					"validation_count": 0,
					"created_at":       "2026-06-01T14:00:00Z",
				},
			},
			returnValidateErr: true,
		}
		server := newSkillAmendmentTestServer(t, state)
		defer server.Close()

		validateSkillAmendmentsAsync(server.URL, "ragnar")

		state.mu.Lock()
		defer state.mu.Unlock()
		if len(state.validateCalls) != 1 {
			t.Fatalf("validate call count = %d, want 1", len(state.validateCalls))
		}
		call := state.validateCalls[0]
		if got := metadataString(call, "memory_id"); got != "amend-4" {
			t.Fatalf("validate memory_id = %q, want amend-4", got)
		}
		if got := metadataString(call, "outcome"); got != "confirmed" {
			t.Fatalf("validate outcome = %q, want confirmed", got)
		}
	})
}

func TestMapOutcomeToSeidr(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"success -> confirmed", "success", "confirmed"},
		{"contradiction -> contradicted", "contradiction", "contradicted"},
		{"unknown -> confirmed (default)", "bogus", "confirmed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapOutcomeToSeidr(tt.in)
			if got != tt.want {
				t.Fatalf("mapOutcomeToSeidr(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidateContextMemoriesAsyncOutcomeMapping(t *testing.T) {
	t.Run("maps success to confirmed", func(t *testing.T) {
		var mu sync.Mutex
		var calls []map[string]interface{}
		mux := http.NewServeMux()
		mux.HandleFunc("/validate", func(w http.ResponseWriter, r *http.Request) {
			var payload map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode validate payload: %v", err)
			}
			mu.Lock()
			calls = append(calls, payload)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"updated": true})
		})
		server := httptest.NewServer(mux)
		defer server.Close()

		addSessionContextMemoryIDs("sess-1", []string{"mem-1"})
		validateContextMemoriesAsync(server.URL, "sess-1", "success")

		time.Sleep(500 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		if len(calls) != 1 {
			t.Fatalf("validate call count = %d, want 1", len(calls))
		}
		if got := metadataString(calls[0], "outcome"); got != "confirmed" {
			t.Fatalf("outcome = %q, want confirmed", got)
		}
	})

	t.Run("maps contradiction to contradicted", func(t *testing.T) {
		var mu sync.Mutex
		var calls []map[string]interface{}
		mux := http.NewServeMux()
		mux.HandleFunc("/validate", func(w http.ResponseWriter, r *http.Request) {
			var payload map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode validate payload: %v", err)
			}
			mu.Lock()
			calls = append(calls, payload)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"updated": true})
		})
		server := httptest.NewServer(mux)
		defer server.Close()

		addSessionContextMemoryIDs("sess-2", []string{"mem-2"})
		validateContextMemoriesAsync(server.URL, "sess-2", "contradiction")

		time.Sleep(500 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		if len(calls) != 1 {
			t.Fatalf("validate call count = %d, want 1", len(calls))
		}
		if got := metadataString(calls[0], "outcome"); got != "contradicted" {
			t.Fatalf("outcome = %q, want contradicted", got)
		}
	})

	t.Run("rejects unknown outcome", func(t *testing.T) {
		mux := http.NewServeMux()
		var callCount int
		mux.HandleFunc("/validate", func(w http.ResponseWriter, r *http.Request) {
			callCount++
		})
		server := httptest.NewServer(mux)
		defer server.Close()

		addSessionContextMemoryIDs("sess-3", []string{"mem-3"})
		validateContextMemoriesAsync(server.URL, "sess-3", "bogus")

		if callCount != 0 {
			t.Fatalf("validate call count = %d, want 0", callCount)
		}
	})

	t.Run("rejects unknown outcome", func(t *testing.T) {
		mux := http.NewServeMux()
		var callCount int
		mux.HandleFunc("/validate", func(w http.ResponseWriter, r *http.Request) {
			callCount++
		})
		server := httptest.NewServer(mux)
		defer server.Close()

		addSessionContextMemoryIDs("sess-3", []string{"mem-3"})
		validateContextMemoriesAsync(server.URL, "sess-3", "bogus")

		if callCount != 0 {
			t.Fatalf("validate call count = %d, want 0", callCount)
		}
	})
}

func newSkillAmendmentTestServer(t *testing.T, state *skillAmendmentTestState) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/query":
			if state.returnQueryErr {
				http.Error(w, "seidr unavailable", http.StatusServiceUnavailable)
				return
			}
			var payload map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode query payload: %v", err)
			}
			if strings.TrimSpace(metadataString(payload, "query")) != "skill_amendment" {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"results": []interface{}{}})
				return
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			results := make([]map[string]interface{}, 0, len(state.amendments))
			for _, amendment := range state.amendments {
				results = append(results, map[string]interface{}{
					"id":         amendment["id"],
					"content":    "SKILL AMENDMENT",
					"metadata":   amendment,
					"created_at": amendment["created_at"],
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"results": results,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/validate":
			var payload map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode validate payload: %v", err)
			}
			state.mu.Lock()
			state.validateCalls = append(state.validateCalls, payload)
			state.mu.Unlock()
			if state.returnValidateErr {
				http.Error(w, "seidr unavailable", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"updated": true,
			})
		default:
			http.NotFound(w, r)
		}
	}))
}
