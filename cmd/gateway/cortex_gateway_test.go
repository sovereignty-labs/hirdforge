package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.hirdforge.com/kit/hirdforge/internal/cortex"
)

const testCortexYAML = `
version: 1
defaults:
  repo: kit/hirdforge
bundles:
  build-default:
    profile: default
routes:
  - id: build-on-label
    on:
      event: issue.labeled
      label: agent:build
    dispatch:
      role: builder
      bundle: build-default
    done_gate:
      type: test-command
      command: "go test ./..."
`

func newCortexTestGateway(t *testing.T) (*gateway, *cortex.MemStore) {
	t.Helper()
	cfg, err := cortex.ParseConfig([]byte(testCortexYAML))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	store := cortex.NewMemStore()
	gw := &gateway{webhookSecret: "s3cret"}
	gw.cortex = cortex.New(cfg, store)
	return gw, store
}

func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func issuesWebhookBody(label string) []byte {
	payload := map[string]any{
		"action":     "label_updated",
		"repository": map[string]any{"full_name": "kit/hirdforge"},
		"issue": map[string]any{
			"number": 41,
			"title":  "add a thing",
			"body":   "details here",
			"labels": []map[string]any{{"name": label}},
		},
		"label": map[string]any{"name": label},
	}
	b, _ := json.Marshal(payload)
	return b
}

func postWebhook(t *testing.T, gw *gateway, event string, body []byte, sig string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	gw.registerWebhookHandlers(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/gitea", strings.NewReader(string(body)))
	req.Header.Set("X-Gitea-Event", event)
	req.Header.Set("X-Gitea-Signature", sig)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestWebhookIssueLabeledCreatesQueuedTask is P1.1's acceptance end-to-end at
// the gateway boundary: signed webhook -> Cortex -> queued task + decision.
func TestWebhookIssueLabeledCreatesQueuedTask(t *testing.T) {
	gw, store := newCortexTestGateway(t)
	body := issuesWebhookBody("agent:build")

	rec := postWebhook(t, gw, "issues", body, signBody("s3cret", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	tasks, _ := store.ListTasks(cortex.TaskFilter{})
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(tasks))
	}
	task := tasks[0]
	if task.Status != cortex.StatusQueued || task.RouteID != "build-on-label" || task.IssueNumber != 41 {
		t.Fatalf("task = %+v", task)
	}
	ds, _ := store.ListDecisions(10)
	if len(ds) != 1 || ds[0].MatchedRoute != "build-on-label" {
		t.Fatalf("decisions = %+v", ds)
	}
}

// TestWebhookIssueUnmatchedLabelLogsNoMatch: the other acceptance half.
func TestWebhookIssueUnmatchedLabelLogsNoMatch(t *testing.T) {
	gw, store := newCortexTestGateway(t)
	body := issuesWebhookBody("bug")

	rec := postWebhook(t, gw, "issues", body, signBody("s3cret", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if tasks, _ := store.ListTasks(cortex.TaskFilter{}); len(tasks) != 0 {
		t.Fatalf("unmatched label created tasks: %+v", tasks)
	}
	ds, _ := store.ListDecisions(10)
	if len(ds) != 1 || ds[0].MatchedRoute != "" || !strings.HasPrefix(ds[0].Reason, "no-match:") {
		t.Fatalf("decisions = %+v", ds)
	}
}

// TestWebhookIssueBadSignatureRejected: HMAC gates the cortex path too.
func TestWebhookIssueBadSignatureRejected(t *testing.T) {
	gw, store := newCortexTestGateway(t)
	body := issuesWebhookBody("agent:build")

	rec := postWebhook(t, gw, "issues", body, "deadbeef")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if tasks, _ := store.ListTasks(cortex.TaskFilter{}); len(tasks) != 0 {
		t.Fatal("unsigned webhook reached cortex")
	}
}

// TestWebhookIssuesIgnoredWhenCortexDisabled: no --cortex-config = v1 exactly.
func TestWebhookIssuesIgnoredWhenCortexDisabled(t *testing.T) {
	gw := &gateway{webhookSecret: "s3cret"}
	body := issuesWebhookBody("agent:build")
	rec := postWebhook(t, gw, "issues", body, signBody("s3cret", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

// TestCortexReadEndpoints: /routes, /log, /tasks render real state.
func TestCortexReadEndpoints(t *testing.T) {
	gw, _ := newCortexTestGateway(t)
	body := issuesWebhookBody("agent:build")
	postWebhook(t, gw, "issues", body, signBody("s3cret", body))

	mux := http.NewServeMux()
	registerCortexRoutes(mux, gw)

	for path, contains := range map[string]string{
		"/api/v1/cortex/routes": "build-on-label",
		"/api/v1/cortex/log":    "matched build-on-label",
		"/api/v1/cortex/tasks":  "\"status\":\"queued\"",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), contains) {
			t.Fatalf("%s: body %q missing %q", path, rec.Body.String(), contains)
		}
	}
}
