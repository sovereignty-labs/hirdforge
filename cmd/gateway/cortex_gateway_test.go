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

// reviewWebhookBody builds a Gitea pull_request_review payload. Gitea sends
// action "reviewed" for EVERY review; the verdict lives in review.type.
func reviewWebhookBody(reviewType, reviewer string, pr int64) []byte {
	payload := map[string]any{
		"action":     "reviewed",
		"repository": map[string]any{"full_name": "kit/hirdforge"},
		"review":     map[string]any{"type": reviewType, "content": "verdict"},
		"sender":     map[string]any{"login": reviewer},
		"pull_request": map[string]any{
			"number": pr,
			"base":   map[string]any{"ref": "main", "repo": map[string]any{"full_name": "kit/hirdforge"}},
			"head":   map[string]any{"ref": "agent/x", "repo": map[string]any{"full_name": "kit/hirdforge"}},
			"user":   map[string]any{"login": "warband"},
		},
	}
	b, _ := json.Marshal(payload)
	return b
}

// TestWebhookDistinctReviewVerdictsAreNotDeduped pins a real defect found while
// closing P3.1: Gitea sends action="reviewed" for every review, so the dedup key
// (event:action:repo:number) collapsed EVERY verdict on a PR into one — the first
// review won and every later one was silently dropped inside the dedup window.
// That swallowed a human REQUEST_CHANGES landing after an agent's APPROVE,
// leaving the revise route unreachable.
func TestWebhookDistinctReviewVerdictsAreNotDeduped(t *testing.T) {
	gw, _ := newCortexTestGateway(t)

	approve := reviewWebhookBody("pull_request_review_approved", "reviewers", 77)
	if rec := postWebhook(t, gw, "pull_request_review", approve, signBody("s3cret", approve)); rec.Code != http.StatusOK {
		t.Fatalf("approve status = %d", rec.Code)
	}
	// A DIFFERENT verdict on the SAME PR, well inside the dedup window.
	reject := reviewWebhookBody("pull_request_review_rejected", "kit", 77)
	if rec := postWebhook(t, gw, "pull_request_review", reject, signBody("s3cret", reject)); rec.Code != http.StatusOK {
		t.Fatalf("reject status = %d", rec.Code)
	}

	// Both verdicts must have reached Cortex as decisions — the second must not
	// have been swallowed by dedup.
	var sawApproved, sawRejected bool
	for _, d := range gw.cortex.RecentDecisions(20) {
		switch d.Event.ReviewState {
		case "APPROVED":
			sawApproved = true
		case "REQUEST_CHANGES":
			sawRejected = true
		}
	}
	if !sawApproved {
		t.Error("APPROVED verdict never reached Cortex")
	}
	if !sawRejected {
		t.Fatal("REQUEST_CHANGES was DEDUPED away — the revise route is unreachable for a second verdict")
	}

	// A true re-delivery of the same verdict still dedups.
	if rec := postWebhook(t, gw, "pull_request_review", reject, signBody("s3cret", reject)); rec.Code != http.StatusOK {
		t.Fatalf("redelivery status = %d", rec.Code)
	}
	n := 0
	for _, d := range gw.cortex.RecentDecisions(20) {
		if d.Event.ReviewState == "REQUEST_CHANGES" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("identical re-delivery should dedup, got %d REQUEST_CHANGES decisions", n)
	}
}

// TestWebhookSpecificReviewEventHeaderRoutes pins the P3.1 root cause: Gitea
// delivers review verdicts under the SPECIFIC header
// (pull_request_review_approved / _rejected), not the plain
// "pull_request_review" the subscription is named after. Matching only the exact
// name dropped every verdict silently — the gateway answered 200 "ignored" and
// logged nothing, so the revise route looked broken while delivery looked fine.
func TestWebhookSpecificReviewEventHeaderRoutes(t *testing.T) {
	for _, tc := range []struct {
		event string
		want  string
	}{
		{"pull_request_review_rejected", "REQUEST_CHANGES"},
		{"pull_request_review_approved", "APPROVED"},
	} {
		gw, _ := newCortexTestGateway(t)
		// Payload with an EMPTY review.type — the header alone must carry it.
		body := reviewWebhookBody("", "kit", 88)
		if rec := postWebhook(t, gw, tc.event, body, signBody("s3cret", body)); rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", tc.event, rec.Code)
		}
		var got string
		for _, d := range gw.cortex.RecentDecisions(10) {
			if d.Event.ReviewState != "" {
				got = d.Event.ReviewState
			}
		}
		if got != tc.want {
			t.Errorf("%s: review state = %q, want %q (verdict dropped?)", tc.event, got, tc.want)
		}
	}
}
