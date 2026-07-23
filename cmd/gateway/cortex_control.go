package main

// P1.8 — the D-CONTROL verbs (dispatch / retry / cancel; nothing more) and
// the push streams. Every verb acts through the same mechanical paths the
// system itself uses; every use is a loud event.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"git.hirdforge.com/kit/hirdforge/internal/cortex"
	"git.hirdforge.com/kit/hirdforge/internal/sandbox"
)

// broadcastingStore decorates the cortex Store: every successful lifecycle
// write is pushed to the WebSocket fan-out as the contract's typed streams
// (task.transition live; the tables remain the rehydration source).
type broadcastingStore struct {
	cortex.Store
	gw *gateway
}

func (b *broadcastingStore) CreateTask(t *cortex.TaskRecord, reason string, cause cortex.Cause) error {
	if err := b.Store.CreateTask(t, reason, cause); err != nil {
		return err
	}
	b.gw.broadcastPayload(map[string]any{
		"type": "task.transition", "task_id": t.ID, "from": "", "to": cortex.StatusQueued, "reason": reason,
	})
	return nil
}

func (b *broadcastingStore) Transition(taskID, to, reason string, cause cortex.Cause) error {
	task, _, _ := b.Store.GetTask(taskID)
	from := ""
	if task != nil {
		from = task.Status
	}
	if err := b.Store.Transition(taskID, to, reason, cause); err != nil {
		return err
	}
	b.gw.broadcastPayload(map[string]any{
		"type": "task.transition", "task_id": taskID, "from": from, "to": to, "reason": reason,
	})
	return nil
}

// registerCortexControlRoutes wires the three verbs.
func registerCortexControlRoutes(mux *http.ServeMux, g *gateway) {
	// POST /api/v1/cortex/dispatch — the manual entry point: creates a
	// labeled issue on Gitea; the issues webhook then drives the route
	// exactly as any other issue would. No side-channel dispatch path.
	mux.HandleFunc("/api/v1/cortex/dispatch", func(w http.ResponseWriter, r *http.Request) {
		if g.cortex == nil {
			http.Error(w, "cortex disabled", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Repo  string `json:"repo"`
			Title string `json:"title"`
			Body  string `json:"body"`
			Label string `json:"label"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Title) == "" {
			http.Error(w, "title required", http.StatusBadRequest)
			return
		}
		if req.Repo == "" {
			req.Repo = g.cortex.Config().Defaults.Repo
		}
		if req.Label == "" {
			req.Label = "agent:build"
		}
		issueBody, _ := json.Marshal(map[string]any{"title": req.Title, "body": req.Body})
		resp, err := giteaRequest(http.DefaultClient, http.MethodPost, g.giteaURL, g.giteaToken,
			fmt.Sprintf("/api/v1/repos/%s/issues", req.Repo), strings.NewReader(string(issueBody)))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		var issue struct {
			Number int64 `json:"number"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&issue)
		if resp.StatusCode != http.StatusCreated || issue.Number == 0 {
			http.Error(w, fmt.Sprintf("gitea issue create: status %d", resp.StatusCode), http.StatusBadGateway)
			return
		}
		labelBody, _ := json.Marshal(map[string]any{"labels": []string{req.Label}})
		lresp, err := giteaRequest(http.DefaultClient, http.MethodPost, g.giteaURL, g.giteaToken,
			fmt.Sprintf("/api/v1/repos/%s/issues/%d/labels", req.Repo, issue.Number), strings.NewReader(string(labelBody)))
		if err == nil {
			_ = lresp.Body.Close()
		}
		log.Printf("cortex: operator dispatch -> issue %s#%d labeled %s", req.Repo, issue.Number, req.Label)
		// Fire the routing event directly. Gitea does NOT emit an issue/label
		// webhook for label changes made via the /labels API (only UI changes),
		// so an operator dispatch must trigger Cortex itself rather than wait
		// for a delivery that never comes. External label adds still arrive via
		// the webhook; this is the manual entry point owning its own trigger.
		ev := cortex.Event{
			Type:        cortex.EventIssueLabeled,
			Repo:        req.Repo,
			Label:       req.Label,
			IssueNumber: issue.Number,
			IssueTitle:  req.Title,
			IssueBody:   req.Body,
			IssueLabels: []string{req.Label},
		}
		if _, err := g.cortex.HandleEvent(ev); err != nil {
			log.Printf("cortex: operator dispatch HandleEvent error: %v", err)
		}
		writeJSON(w, http.StatusCreated, map[string]any{"repo": req.Repo, "issue": issue.Number, "label": req.Label})
	})

	// POST /api/v1/cortex/tasks/{id}/retry and /cancel
	mux.HandleFunc("/api/v1/cortex/tasks/", func(w http.ResponseWriter, r *http.Request) {
		if g.cortex == nil {
			http.Error(w, "cortex disabled", http.StatusNotFound)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/cortex/tasks/")
		switch {
		case strings.HasSuffix(rest, "/retry") && r.Method == http.MethodPost:
			g.cortexRetry(w, strings.TrimSuffix(rest, "/retry"))
		case strings.HasSuffix(rest, "/cancel") && r.Method == http.MethodPost:
			g.cortexCancel(w, strings.TrimSuffix(rest, "/cancel"))
		case r.Method == http.MethodGet:
			id := rest
			task, history, err := g.cortex.Store().GetTask(id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"task": task, "history": history})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
}

// cortexRetry re-dispatches a failed task with its failure context
// (D-LESSONS #2 — blind retry is a defect, so the context is mandatory).
func (g *gateway) cortexRetry(w http.ResponseWriter, taskID string) {
	task, history, err := g.cortex.Store().GetTask(taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if task.Status != cortex.StatusFailed {
		http.Error(w, fmt.Sprintf("task is %s; only failed tasks retry", task.Status), http.StatusConflict)
		return
	}
	route := g.cortex.Config().RouteByID(task.RouteID)
	if route == nil {
		http.Error(w, "task's route no longer exists in cortex.yaml", http.StatusConflict)
		return
	}
	// Build the failure context from the last failed transition if the task
	// doesn't already carry one (e.g. first manual retry).
	if len(task.FailureContext) == 0 && len(history) > 0 {
		last := history[len(history)-1]
		fc, _ := json.Marshal(cortex.FailureContext{
			PriorAgent: task.Agent, PriorAttempt: task.Attempt, Reason: last.Reason,
		})
		if err := g.cortex.Store().PrepareRetry(taskID, fc); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else if err := g.cortex.Store().PrepareRetry(taskID, task.FailureContext); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("cortex: OPERATOR RETRY task %s (attempt %d)", taskID, task.Attempt+1)
	if g.cortex.OnTaskQueued != nil {
		g.cortex.OnTaskQueued(route, taskID)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"task_id": taskID, "attempt": task.Attempt + 1})
}

// cortexCancel tears the sandbox down and fails the task loudly.
func (g *gateway) cortexCancel(w http.ResponseWriter, taskID string) {
	task, _, err := g.cortex.Store().GetTask(taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	switch task.Status {
	case cortex.StatusMerged, cortex.StatusValidated, cortex.StatusFailed:
		http.Error(w, fmt.Sprintf("task is %s; nothing to cancel", task.Status), http.StatusConflict)
		return
	}
	if err := g.cortex.Store().Transition(taskID, cortex.StatusFailed,
		"cancelled by operator", cortex.Cause{Kind: cortex.CauseOperator}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Best-effort sandbox teardown by deterministic name; a failure is a loud
	// leak event, and the TTL + sweeper remain the backstop.
	if g.cortexSandboxDestroy != nil {
		go g.cortexSandboxDestroy(taskID)
	}
	log.Printf("cortex: OPERATOR CANCEL task %s", taskID)
	writeJSON(w, http.StatusAccepted, map[string]any{"task_id": taskID, "status": cortex.StatusFailed})
}

// makeSandboxDestroyer returns the cancel-path teardown for the live sandbox.
func makeSandboxDestroyer(client *sandbox.K8sClient, namespace string) func(string) {
	sb := sandbox.NewK8sSandbox(client, namespace)
	return func(taskID string) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		ref := sandbox.RefForTask(namespace, taskID)
		if err := sb.Destroy(ctx, ref); err != nil {
			log.Printf("cortex: SANDBOX LEAK on cancel %s: %v", taskID, err)
		}
	}
}
