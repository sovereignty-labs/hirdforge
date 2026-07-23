package main

// P1.7 — the write boundary: approved tasks queue a Lockbox merge_pr action;
// human approval triggers the internal callback below; the gateway performs
// the Gitea merge (the apply — the sole write to a protected branch) and
// transitions approved→merged. A rejected queue entry never calls back, so a
// rejection mechanically cannot merge.

import (
	"bytes"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"git.hirdforge.com/kit/hirdforge/internal/cortex"
)

const cortexLockboxHunt = "cortex-merges"

// cortexOnTaskAdvanced is hooked to cortex.OnTaskAdvanced.
func (g *gateway) cortexOnTaskAdvanced(taskID, to string) {
	switch to {
	case cortex.StatusApproved:
		go g.cortexEnqueueMergeApproval(taskID)
	case cortex.StatusValidated:
		go g.cortexCloseIssue(taskID)
	}
}

// cortexEnqueueMergeApproval registers/refreshes the standing merge hunt and
// queues the merge_pr action for human approval. Failure is loud: the task
// stays `approved` and the error is logged + visible in task detail timing.
func (g *gateway) cortexEnqueueMergeApproval(taskID string) {
	if strings.TrimSpace(g.lockboxURL) == "" {
		log.Printf("cortex: ERROR task %s approved but no --lockbox-url — merge cannot be authorized", taskID)
		return
	}
	task, _, err := g.cortex.Store().GetTask(taskID)
	if err != nil {
		log.Printf("cortex: ERROR enqueueing merge for %s: %v", taskID, err)
		return
	}
	client := &http.Client{Timeout: 15 * time.Second}
	reg, _ := json.Marshal(map[string]any{
		"hunt_id":      cortexLockboxHunt,
		"permissions":  []map[string]any{{"service": "hirdforge", "actions": []string{"merge_pr"}}},
		"max_requests": 100,
		"ttl_seconds":  86400,
	})
	if resp, err := client.Post(g.lockboxURL+"/register", "application/json", bytes.NewReader(reg)); err != nil {
		log.Printf("cortex: ERROR lockbox register: %v", err)
		return
	} else {
		_ = resp.Body.Close()
	}
	act, _ := json.Marshal(map[string]any{
		"hunt_id":    cortexLockboxHunt,
		"service":    "hirdforge",
		"action":     "merge_pr",
		"agent_name": "cortex",
		"params": map[string]any{
			"task_id": task.ID,
			"repo":    task.PRRepo,
			"pr":      task.PRNumber,
			"note":    fmt.Sprintf("task %s: merge PR %s#%d (reviewer: %s)", task.ID, task.PRRepo, task.PRNumber, task.Reviewer),
		},
	})
	resp, err := client.Post(g.lockboxURL+"/action", "application/json", bytes.NewReader(act))
	if err != nil {
		log.Printf("cortex: ERROR lockbox action: %v", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	log.Printf("cortex: merge approval queued for %s (lockbox status %d: %s)", taskID, resp.StatusCode, strings.TrimSpace(string(body)))
}

// registerCortexInternalRoutes wires the Lockbox→gateway merge callback.
func registerCortexInternalRoutes(mux *http.ServeMux, g *gateway, secret string) {
	mux.HandleFunc("/api/v1/cortex/internal/merge-approved", func(w http.ResponseWriter, r *http.Request) {
		if g.cortex == nil {
			http.Error(w, "cortex disabled", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if secret == "" || !hmac.Equal([]byte(r.Header.Get("X-Hirdforge-Merge-Secret")), []byte(secret)) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			TaskID string `json:"task_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.TaskID == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := g.cortexExecuteMerge(req.TaskID); err != nil {
			log.Printf("cortex: MERGE FAILED for %s: %v", req.TaskID, err)
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "merged", "task_id": req.TaskID})
	})
}

// cortexExecuteMerge performs the authorized apply. Preconditions are
// re-checked mechanically here — the callback alone is not trusted blindly.
func (g *gateway) cortexExecuteMerge(taskID string) error {
	task, _, err := g.cortex.Store().GetTask(taskID)
	if err != nil {
		return err
	}
	if task.Status != cortex.StatusApproved {
		return fmt.Errorf("task %s is %s, not approved — refusing to merge", taskID, task.Status)
	}
	if task.PRRepo == "" || task.PRNumber == 0 {
		return fmt.Errorf("task %s has no observed PR — refusing to merge", taskID)
	}
	mergeBody := bytes.NewReader([]byte(`{"Do":"merge"}`))
	resp, err := giteaRequest(http.DefaultClient, http.MethodPost, g.giteaURL, g.giteaToken,
		fmt.Sprintf("/api/v1/repos/%s/pulls/%d/merge", task.PRRepo, task.PRNumber), mergeBody)
	if err != nil {
		return fmt.Errorf("gitea merge: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("gitea merge: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return g.cortex.Store().Transition(taskID, cortex.StatusMerged,
		fmt.Sprintf("Lockbox-authorized merge of PR %s#%d executed", task.PRRepo, task.PRNumber),
		cortex.Cause{Kind: cortex.CauseOperator, Detail: map[string]any{"via": "lockbox", "pr": task.PRNumber}})
}

// cortexCloseIssue closes the originating issue once the task validates.
func (g *gateway) cortexCloseIssue(taskID string) {
	task, _, err := g.cortex.Store().GetTask(taskID)
	if err != nil || task.IssueNumber == 0 {
		return
	}
	body := bytes.NewReader([]byte(`{"state":"closed"}`))
	resp, err := giteaRequest(http.DefaultClient, http.MethodPatch, g.giteaURL, g.giteaToken,
		fmt.Sprintf("/api/v1/repos/%s/issues/%d", task.IssueRepo, task.IssueNumber), body)
	if err != nil {
		log.Printf("cortex: ERROR closing issue for %s: %v", taskID, err)
		return
	}
	defer resp.Body.Close()
	log.Printf("cortex: task %s validated — issue %s#%d closed (status %d)", taskID, task.IssueRepo, task.IssueNumber, resp.StatusCode)
}
