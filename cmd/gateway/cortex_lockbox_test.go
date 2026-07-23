package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.hirdforge.com/kit/hirdforge/internal/cortex"
)

func approvedTask(t *testing.T, store *cortex.MemStore, id string, pr int64) {
	t.Helper()
	task := &cortex.TaskRecord{ID: id, RouteID: "build-on-label", IssueRepo: "kit/hirdforge", IssueNumber: 9}
	if err := store.CreateTask(task, "m", cortex.Cause{Kind: cortex.CauseWebhook}); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{cortex.StatusDispatched, cortex.StatusBuilding, cortex.StatusReview, cortex.StatusApproved} {
		if err := store.Transition(id, to, "step", cortex.Cause{Kind: cortex.CauseWebhook}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetPR(id, "kit/hirdforge", pr); err != nil {
		t.Fatal(err)
	}
}

func mergeTestGateway(t *testing.T, giteaStatus int) (*gateway, *cortex.MemStore, *int) {
	t.Helper()
	calls := 0
	gitea := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/merge") && r.Method == http.MethodPost {
			calls++
			w.WriteHeader(giteaStatus)
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(gitea.Close)
	cfg, err := cortex.ParseConfig([]byte(testCortexYAML))
	if err != nil {
		t.Fatal(err)
	}
	store := cortex.NewMemStore()
	gw := &gateway{giteaURL: gitea.URL}
	gw.cortex = cortex.New(cfg, store)
	return gw, store, &calls
}

func postMerge(t *testing.T, gw *gateway, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	registerCortexInternalRoutes(mux, gw, "s3cret")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/cortex/internal/merge-approved", strings.NewReader(body))
	req.Header.Set("X-Hirdforge-Merge-Secret", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestMergeCallbackHappyPath: approved task + valid secret => Gitea merge +
// approved->merged with the Lockbox authorization as the reason.
func TestMergeCallbackHappyPath(t *testing.T) {
	gw, store, calls := mergeTestGateway(t, 200)
	approvedTask(t, store, "hf-m1", 77)

	rec := postMerge(t, gw, "s3cret", `{"task_id":"hf-m1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if *calls != 1 {
		t.Fatalf("gitea merge calls = %d", *calls)
	}
	task, history, _ := store.GetTask("hf-m1")
	if task.Status != cortex.StatusMerged {
		t.Fatalf("status = %q", task.Status)
	}
	final := history[len(history)-1]
	if !strings.Contains(final.Reason, "Lockbox-authorized merge") || final.Cause.Kind != cortex.CauseOperator {
		t.Fatalf("final = %+v", final)
	}
}

// TestMergeCallbackWrongSecret: no secret, no merge.
func TestMergeCallbackWrongSecret(t *testing.T) {
	gw, store, calls := mergeTestGateway(t, 200)
	approvedTask(t, store, "hf-m2", 78)
	rec := postMerge(t, gw, "wrong", `{"task_id":"hf-m2"}`)
	if rec.Code != http.StatusUnauthorized || *calls != 0 {
		t.Fatalf("status=%d calls=%d", rec.Code, *calls)
	}
	task, _, _ := store.GetTask("hf-m2")
	if task.Status != cortex.StatusApproved {
		t.Fatalf("status mutated: %q", task.Status)
	}
}

// TestMergeCallbackRefusesNonApproved: the callback is not trusted blindly —
// a task not in approved cannot merge regardless of authorization.
func TestMergeCallbackRefusesNonApproved(t *testing.T) {
	gw, store, calls := mergeTestGateway(t, 200)
	task := &cortex.TaskRecord{ID: "hf-m3", RouteID: "build-on-label", IssueRepo: "kit/hirdforge"}
	if err := store.CreateTask(task, "m", cortex.Cause{Kind: cortex.CauseWebhook}); err != nil {
		t.Fatal(err)
	}
	rec := postMerge(t, gw, "s3cret", `{"task_id":"hf-m3"}`)
	if rec.Code != http.StatusConflict || *calls != 0 {
		t.Fatalf("status=%d calls=%d — a queued task must not merge", rec.Code, *calls)
	}
}

// TestMergeCallbackGiteaFailureKeepsApproved: a failed Gitea merge does not
// advance the lifecycle.
func TestMergeCallbackGiteaFailureKeepsApproved(t *testing.T) {
	gw, store, _ := mergeTestGateway(t, 405)
	approvedTask(t, store, "hf-m4", 79)
	rec := postMerge(t, gw, "s3cret", `{"task_id":"hf-m4"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d", rec.Code)
	}
	task, _, _ := store.GetTask("hf-m4")
	if task.Status != cortex.StatusApproved {
		t.Fatalf("status = %q after failed merge", task.Status)
	}
}
