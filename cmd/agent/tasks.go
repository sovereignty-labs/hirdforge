package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	tasklifepkg "github.com/kitporath/project_valhalla/pkg/tasklife"
	taskspkg "github.com/kitporath/project_valhalla/pkg/tasks"
	workspacepkg "github.com/kitporath/project_valhalla/pkg/workspace"
)

func newTaskID() string {
	return fmt.Sprintf("task-%08x", rand.Uint32())
}

func resolveTokenFlagOrFile(flagValue, filePath, envKey string) string {
	if token := strings.TrimSpace(flagValue); token != "" {
		return token
	}
	if data, err := os.ReadFile(filePath); err == nil {
		if token := strings.TrimSpace(string(data)); token != "" {
			return token
		}
	}
	return strings.TrimSpace(os.Getenv(envKey))
}

func taskNudgeCount(record tasklifepkg.TaskRecord) int {
	count := 0
	for _, state := range record.History {
		if state == tasklifepkg.StateNudged {
			count++
		}
	}
	return count
}

func sovereignStateEnabled(enabled map[string]bool, state tasklifepkg.TaskState) bool {
	if len(enabled) == 0 {
		return false
	}
	switch state {
	case tasklifepkg.StateCompleted:
		return enabled["completed"]
	case tasklifepkg.StateNudged:
		return enabled["nudged"]
	case tasklifepkg.StateFailedNoPR:
		return enabled["failed"]
	default:
		return false
	}
}

func notifyGateway(gatewayURL, eventType, agentName string, metadata map[string]interface{}) {
	if gatewayURL == "" {
		return
	}
	go func() {
		payload := map[string]interface{}{
			"type":     eventType,
			"agent":    agentName,
			"metadata": metadata,
		}
		body, _ := json.Marshal(payload)
		req, err := http.NewRequest(http.MethodPost, strings.TrimRight(gatewayURL, "/")+"/api/v1/events", bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: agentCommTimeout}
		resp, err := client.Do(req)
		if err != nil {
			logJSON("warn", "gateway notify failed", map[string]interface{}{"error": err.Error()})
			return
		}
		_ = resp.Body.Close()
	}()
}

func taskEventMetadata(chunk interface{}) (string, map[string]interface{}, bool) {
	body, err := json.Marshal(chunk)
	if err != nil {
		return "", nil, false
	}
	var metadata map[string]interface{}
	if err := json.Unmarshal(body, &metadata); err != nil {
		return "", nil, false
	}
	eventType := strings.TrimSpace(fmt.Sprint(metadata["type"]))
	if eventType == "" {
		return "", nil, false
	}
	return eventType, metadata, true
}

func taskObjectMap(value interface{}) (map[string]interface{}, bool) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, false
	}
	return out, true
}

func taskWorkspaceUpdateEmitter(gatewayURL, agentName, taskSessionID string) func(interface{}) bool {
	projector := workspacepkg.NewProjector()
	return func(chunk interface{}) bool {
		eventType, metadata, ok := taskEventMetadata(chunk)
		if !ok || !workspacepkg.IsTypedEvent(eventType) {
			return true
		}
		if strings.TrimSpace(taskSessionID) != "" {
			sessionID := strings.TrimSpace(fmt.Sprint(metadata["session_id"]))
			if sessionID == "" || sessionID == "<nil>" {
				metadata["session_id"] = strings.TrimSpace(taskSessionID)
			}
		}
		projector.Apply(agentName, eventType, metadata)
		if strings.TrimSpace(taskSessionID) != "" {
			ws := projector.Get(agentName)
			projector.SetArchitectContext(agentName, taskSessionID, ws.CurrentObjective)
		}
		notifyGateway(gatewayURL, eventType, agentName, metadata)
		workspace, ok := taskObjectMap(projector.Get(agentName))
		if !ok {
			return true
		}
		notifyGateway(gatewayURL, "workspace_update", agentName, map[string]interface{}{
			"workspace": workspace,
		})
		return true
	}
}

func verifyPRExists(giteaURL, giteaToken, prURL string) (bool, error) {
	prURL = strings.TrimSpace(prURL)
	if strings.TrimSpace(giteaURL) == "" || prURL == "" {
		return false, nil
	}
	prRE := regexp.MustCompile(`https?://[^/]+/([^/]+)/([^/]+)/pulls/(\d+)`)
	matches := prRE.FindStringSubmatch(prURL)
	if len(matches) != 4 {
		return false, fmt.Errorf("invalid PR URL: %s", prURL)
	}
	owner := matches[1]
	repo := matches[2]
	number := matches[3]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		strings.TrimRight(giteaURL, "/")+"/api/v1/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/pulls/"+url.PathEscape(number),
		nil,
	)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(giteaToken) != "" {
		req.Header.Set("Authorization", "token "+strings.TrimSpace(giteaToken))
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, nil
	}
	var out struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	state := strings.TrimSpace(strings.ToLower(out.State))
	return state == "open" || state == "closed", nil
}

// checkForAgentPR queries Gitea for open PRs in the target repo from the agent's branch.
// This is used by the completion gate to avoid false negatives when the agent's response
// text doesn't contain a PR URL but a PR was actually created.
func checkForAgentPR(giteaURL, giteaToken, branch, owner, repo string) (string, bool, error) {
	giteaURL = strings.TrimSpace(giteaURL)
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	branch = strings.TrimSpace(branch)
	if giteaURL == "" || owner == "" || repo == "" || branch == "" {
		return "", false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		strings.TrimRight(giteaURL, "/")+"/api/v1/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/pulls?state=open&head="+url.PathEscape(owner)+":"+url.PathEscape(branch),
		nil,
	)
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(giteaToken) != "" {
		req.Header.Set("Authorization", "token "+strings.TrimSpace(giteaToken))
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false, nil
	}
	var prs []struct {
		Number int    `json:"number"`
		URL    string `json:"html_url"`
		State  string `json:"state"`
		Head   struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&prs); err != nil {
		return "", false, err
	}
	for _, pr := range prs {
		if strings.TrimSpace(strings.ToLower(pr.State)) == "open" {
			return pr.URL, true, nil
		}
	}
	return "", false, nil
}

func registerTaskRoutes(mux *http.ServeMux, deps serverDeps) {
	mux.HandleFunc("/tasks/send", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req taskSendRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Content) == "" {
			http.Error(w, "content is required", http.StatusBadRequest)
			return
		}
		deps.reviewTracker.ClearIfTaskWithoutPR(req.Content)
		task := deps.taskStore.Create(taskspkg.Task{
			ID:        newTaskID(),
			Agent:     deps.agentName,
			From:      strings.TrimSpace(req.From),
			SessionID: strings.TrimSpace(req.SessionID),
			Content:   req.Content,
			Status:    "submitted",
		})
		ctx, cancel := context.WithCancel(context.Background())
		deps.taskCancelMu.Lock()
		deps.taskCancels[task.ID] = cancel
		deps.taskCancelMu.Unlock()
		go func(taskID string, content string) {
			defer func() {
				deps.taskCancelMu.Lock()
				delete(deps.taskCancels, taskID)
				deps.taskCancelMu.Unlock()
			}()
			current, ok := deps.taskStore.Get(taskID)
			if !ok {
				return
			}
			trackerKey := deps.agentName + ":" + current.ID
			if _, err := deps.taskTracker.Dispatch(trackerKey, trackerKey); err != nil {
				current.Status = "failed"
				current.Error = "completion_tracker_init_failed"
				deps.taskStore.Update(current)
				notifyGateway(gatewayURLValue, "task", deps.agentName, map[string]interface{}{
					"message":    fmt.Sprintf("failed task %s (from %s): %s", current.ID, current.From, current.Error),
					"session_id": current.SessionID,
				})
				return
			}
			current.Status = "working"
			current.Error = ""
			deps.taskStore.Update(current)
			notifyGateway(gatewayURLValue, "task", deps.agentName, map[string]interface{}{
				"message":    fmt.Sprintf("started task %s (from %s)", current.ID, current.From),
				"session_id": current.SessionID,
			})
			reportTrackedState := func(record tasklifepkg.TaskRecord) {
				if !sovereignStateEnabled(deps.sovereignStates, record.State) {
					return
				}
				resultText := record.Result
				if record.State == tasklifepkg.StateNudged {
					resultText = record.Nudge
				}
				deps.sovereignReporter.Report(tasklifepkg.TaskEvent{
					From:      current.From,
					TaskID:    current.ID,
					Agent:     deps.agentName,
					State:     record.State,
					Result:    resultText,
					Timestamp: record.UpdatedAt,
				})
			}
			appendToolLog := func(log taskspkg.ToolLog) {
				cur, ok := deps.taskStore.Get(taskID)
				if !ok {
					return
				}
				cur.Tools = append(cur.Tools, log)
				deps.taskStore.Update(cur)
			}
			pendingContent := content
			var result string
			var err error
			emitWorkspaceUpdate := taskWorkspaceUpdateEmitter(gatewayURLValue, deps.agentName, current.SessionID)
			completionGates := []tasklifepkg.CompletionGate(nil)
			if strings.TrimSpace(deps.requirePRPattern) != "" {
				completionGates = []tasklifepkg.CompletionGate{{
					Name:    "pr_url",
					Pattern: deps.requirePRPattern,
					Nudge:   fmt.Sprintf("Your completion message must include a PR reference matching %q. Reply with an updated completion message that includes it.", deps.requirePRPattern),
				}}
			}
			for {
				result, err = deps.processConversation(ctx, taskID, taskID, pendingContent, emitWorkspaceUpdate, appendToolLog)
				if err != nil || len(completionGates) == 0 {
					break
				}
				gateResult, gateErr := tasklifepkg.CheckCompletionGates(result, completionGates)
				if gateErr != nil {
					err = fmt.Errorf("completion gate check failed: %w", gateErr)
					break
				}
				if gateResult.Passed {
					prRE := regexp.MustCompile(`https?://[^\s]+/[^/]+/[^/]+/pulls/\d+`)
					prURL := prRE.FindString(result)
					if prURL != "" && strings.TrimSpace(giteaURLValue) != "" {
						exists, verifyErr := verifyPRExists(giteaURLValue, giteaTokenValue, prURL)
						if verifyErr != nil {
							log.Printf("PR verification error: %v", verifyErr)
						} else if !exists {
							gateResult.Passed = false
							gateResult.Nudges = []string{"VERIFICATION FAILED: The PR URL you reported does not exist in Gitea. You must actually create the PR using the gitea create-pr tool or exec+curl. Do not report a PR URL unless the tool confirmed creation. Try again."}
							log.Printf("PR verification failed: %s does not exist", prURL)
						}
					}
					if gateResult.Passed {
						if record, completeErr := deps.taskTracker.Complete(trackerKey, result, true); completeErr == nil {
							reportTrackedState(record)
						}
						break
					}
				}
				record, ok := deps.taskTracker.Task(trackerKey)
				if !ok {
					err = fmt.Errorf("completion tracker missing for task %s", taskID)
					break
				}
				if taskNudgeCount(record) >= deps.completionMaxNudges {
					prFound := false
					prURL := ""
					tracked, hasTracking := trackedTasks[taskID]
					if hasTracking && strings.TrimSpace(giteaURLValue) != "" && tracked != nil && tracked.BranchName != "" {
						if pr, found, checkErr := checkForAgentPR(giteaURLValue, giteaTokenValue, tracked.BranchName, tracked.IssueOwner, tracked.IssueRepo); checkErr == nil && found {
							prFound = true
							prURL = pr
							log.Printf("Found existing PR via Gitea check: %s", prURL)
						}
					}
					if prFound {
						completeResult := fmt.Sprintf("PR found via Gitea check: %s", prURL)
						if record, completeErr := deps.taskTracker.Complete(trackerKey, completeResult, true); completeErr == nil {
							reportTrackedState(record)
						}
						break
					}
					if failedRecord, completeErr := deps.taskTracker.Complete(trackerKey, "no_pr_url", false); completeErr == nil {
						reportTrackedState(failedRecord)
					}
					err = fmt.Errorf("no_pr_url")
					break
				}
				nudge := "Completion requirements were not met."
				if len(gateResult.Nudges) > 0 {
					nudge = strings.Join(gateResult.Nudges, "\n")
				}
				if nudgedRecord, nudgeErr := deps.taskTracker.Nudge(trackerKey, nudge); nudgeErr != nil {
					err = fmt.Errorf("completion tracker nudge failed: %w", nudgeErr)
					break
				} else {
					reportTrackedState(nudgedRecord)
				}
				pendingContent = nudge
			}
			cur, ok := deps.taskStore.Get(taskID)
			if !ok {
				return
			}
			if ctx.Err() == context.Canceled {
				cur.Status = "failed"
				cur.Error = "cancelled"
				deps.taskStore.Update(cur)
				notifyGateway(gatewayURLValue, "task", deps.agentName, map[string]interface{}{
					"message":    fmt.Sprintf("cancelled task %s (from %s)", cur.ID, cur.From),
					"session_id": cur.SessionID,
				})
				return
			}
			if err != nil {
				cur.Status = "failed"
				cur.Error = err.Error()
				deps.taskStore.Update(cur)
				notifyGateway(gatewayURLValue, "task", deps.agentName, map[string]interface{}{
					"message":    fmt.Sprintf("failed task %s (from %s): %s", cur.ID, cur.From, cur.Error),
					"session_id": cur.SessionID,
				})
				return
			}
			if len(completionGates) == 0 {
				if record, completeErr := deps.taskTracker.Complete(trackerKey, result, true); completeErr == nil {
					reportTrackedState(record)
				}
			}
			cur.Status = "completed"
			cur.Result = result
			cur.Error = ""
			deps.taskStore.Update(cur)
			summary := cur.Result
			if len(summary) > 200 {
				summary = summary[:200] + "..."
			}
			notifyGateway(gatewayURLValue, "task_completed", deps.agentName, map[string]interface{}{
				"task_id":    cur.ID,
				"from":       cur.From,
				"summary":    summary,
				"session_id": cur.SessionID,
			})
		}(task.ID, req.Content)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": task.ID, "status": task.Status})
	})
	mux.HandleFunc("/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		agentFilter := strings.TrimSpace(r.URL.Query().Get("agent"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(deps.taskStore.List(agentFilter, status))
	})
	mux.HandleFunc("/tasks/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/tasks/")
		if strings.HasSuffix(path, "/cancel") {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			taskID := strings.Trim(strings.TrimSuffix(path, "/cancel"), "/")
			task, ok := deps.taskStore.Get(taskID)
			if !ok {
				http.NotFound(w, r)
				return
			}
			task.Status = "failed"
			task.Error = "cancelled"
			deps.taskStore.Update(task)
			deps.taskCancelMu.Lock()
			cancel := deps.taskCancels[taskID]
			deps.taskCancelMu.Unlock()
			if cancel != nil {
				cancel()
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"id": taskID, "status": "failed"})
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		taskID := strings.Trim(path, "/")
		task, ok := deps.taskStore.Get(taskID)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(task)
	})
}
