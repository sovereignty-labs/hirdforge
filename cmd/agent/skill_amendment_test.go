package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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
