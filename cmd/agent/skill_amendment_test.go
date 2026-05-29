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
	mu             sync.Mutex
	amendment      map[string]interface{}
	failure        map[string]interface{}
	patches        []map[string]interface{}
	returnQueryErr bool
}

func TestValidateSkillAmendmentsAsyncTransitions(t *testing.T) {
	t.Run("increments then validates after three checks", func(t *testing.T) {
		state := &skillAmendmentTestState{
			amendment: map[string]interface{}{
				"id":                "amend-1",
				"type":              "skill_amendment",
				"status":            "unvalidated",
				"failure_pattern":   "git-clone already exists",
				"amendment_date":    "2026-06-01T14:00:00Z",
				"validation_checks": 2,
				"created_at":        "2026-06-01T14:00:00Z",
			},
		}
		server := newSkillAmendmentTestServer(t, state)
		defer server.Close()

		validateSkillAmendmentsAsync(server.URL, "ragnar")

		state.mu.Lock()
		if len(state.patches) != 1 {
			state.mu.Unlock()
			t.Fatalf("patch count after first pass = %d, want 1", len(state.patches))
		}
		firstPatch := state.patches[0]
		meta, _ := firstPatch["metadata"].(map[string]interface{})
		if meta == nil {
			state.mu.Unlock()
			t.Fatalf("first patch metadata missing: %#v", firstPatch)
		}
		if got := metadataInt(meta, "validation_checks"); got != 3 {
			state.mu.Unlock()
			t.Fatalf("first patch validation_checks = %d, want 3", got)
		}
		if got := metadataString(meta, "status"); got != "" {
			state.mu.Unlock()
			t.Fatalf("first patch status = %q, want empty", got)
		}
		if got := metadataString(state.amendment, "status"); got != "unvalidated" {
			state.mu.Unlock()
			t.Fatalf("amendment status after first pass = %q, want unvalidated", got)
		}
		if got := metadataInt(state.amendment, "validation_checks"); got != 3 {
			state.mu.Unlock()
			t.Fatalf("amendment validation_checks after first pass = %d, want 3", got)
		}
		state.mu.Unlock()

		validateSkillAmendmentsAsync(server.URL, "ragnar")

		state.mu.Lock()
		defer state.mu.Unlock()
		if len(state.patches) != 2 {
			t.Fatalf("patch count after second pass = %d, want 2", len(state.patches))
		}
		secondPatch := state.patches[1]
		meta, _ = secondPatch["metadata"].(map[string]interface{})
		if meta == nil {
			t.Fatalf("second patch metadata missing: %#v", secondPatch)
		}
		if got := metadataString(meta, "status"); got != "validated" {
			t.Fatalf("second patch status = %q, want validated", got)
		}
		if got := metadataString(meta, "validated_at"); got == "" {
			t.Fatalf("second patch validated_at missing")
		}
		if got := metadataString(meta, "last_checked"); got == "" {
			t.Fatalf("second patch last_checked missing")
		}
		if got := metadataString(state.amendment, "status"); got != "validated" {
			t.Fatalf("amendment status after second pass = %q, want validated", got)
		}
	})

	t.Run("contradicted on matching failure", func(t *testing.T) {
		state := &skillAmendmentTestState{
			amendment: map[string]interface{}{
				"id":                "amend-2",
				"type":              "skill_amendment",
				"status":            "unvalidated",
				"failure_pattern":   "git-clone already exists",
				"amendment_date":    "2026-06-01T14:00:00Z",
				"validation_checks": 1,
				"created_at":        "2026-06-01T14:00:00Z",
			},
			failure: map[string]interface{}{
				"id":         "fail-1",
				"type":       "tool_failure",
				"tool":       "git-clone",
				"failure":    "retry_exhausted",
				"timestamp":  "2026-06-02T10:00:00Z",
				"created_at": "2026-06-02T10:00:00Z",
			},
		}
		server := newSkillAmendmentTestServer(t, state)
		defer server.Close()

		validateSkillAmendmentsAsync(server.URL, "ragnar")

		state.mu.Lock()
		defer state.mu.Unlock()
		if len(state.patches) != 1 {
			t.Fatalf("patch count = %d, want 1", len(state.patches))
		}
		meta, _ := state.patches[0]["metadata"].(map[string]interface{})
		if meta == nil {
			t.Fatalf("patch metadata missing: %#v", state.patches[0])
		}
		if got := metadataString(meta, "status"); got != "contradicted" {
			t.Fatalf("patch status = %q, want contradicted", got)
		}
		if got := metadataString(meta, "contradicted_by"); got != "fail-1" {
			t.Fatalf("patch contradicted_by = %q, want fail-1", got)
		}
		if got := metadataString(state.amendment, "status"); got != "contradicted" {
			t.Fatalf("amendment status = %q, want contradicted", got)
		}
	})

	t.Run("no-op when seidr returns error", func(t *testing.T) {
		state := &skillAmendmentTestState{
			returnQueryErr: true,
		}
		server := newSkillAmendmentTestServer(t, state)
		defer server.Close()

		validateSkillAmendmentsAsync(server.URL, "ragnar")

		state.mu.Lock()
		defer state.mu.Unlock()
		if len(state.patches) != 0 {
			t.Fatalf("expected no patch requests on query error, got %d", len(state.patches))
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
			query := strings.TrimSpace(metadataString(payload, "query"))
			state.mu.Lock()
			defer state.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			switch query {
			case "skill_amendment":
				if state.amendment == nil {
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"results": []interface{}{}})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"results": []map[string]interface{}{
						{
							"id":         state.amendment["id"],
							"content":    "SKILL AMENDMENT",
							"metadata":   state.amendment,
							"created_at": state.amendment["created_at"],
						},
					},
				})
			default:
				if state.failure == nil {
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"results": []interface{}{}})
					return
				}
				if query != strings.TrimSpace(metadataString(state.amendment, "failure_pattern")) {
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"results": []interface{}{}})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"results": []map[string]interface{}{
						{
							"id":         state.failure["id"],
							"content":    "[FAILURE:retry_exhausted] tool=git-clone error=already exists",
							"metadata":   state.failure,
							"created_at": state.failure["created_at"],
						},
					},
				})
			}
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/memories/"):
			var payload map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode patch payload: %v", err)
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			state.patches = append(state.patches, payload)
			meta, _ := payload["metadata"].(map[string]interface{})
			if meta == nil {
				http.Error(w, "metadata required", http.StatusBadRequest)
				return
			}
			for _, key := range []string{"status", "validated_at", "last_checked", "contradicted_by", "contradicted_at"} {
				if val, ok := meta[key]; ok {
					state.amendment[key] = val
				}
			}
			if val, ok := meta["validation_checks"]; ok {
				state.amendment["validation_checks"] = metadataInt(meta, "validation_checks")
				_ = val
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
}
