package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	webhookReadyLabelID      = 3
	webhookInProgressLabelID = 4
	webhookEndpointURL       = "http://gateway.valhalla.svc:8080/api/v1/webhooks/gitea"
	pipelineStateConfigMap   = "gateway-pipeline-state"
	pipelineStateNamespace   = "valhalla"
)

var webhookTaskRefRE = regexp.MustCompile(`(?i)(?:closes?\s+)?kit/hirdforge-tasks#(\d+)`)

type webhookPR struct {
	Number  int64
	Title   string
	Body    string
	Repo    string
	Base    string
	Head    string
	HTMLURL string
	User    string
	Merged  bool
}

type discordField struct {
	Name   string
	Value  string
	Inline bool
}

type webhookIssue struct {
	Number int64
	Title  string
	Body   string
	Labels []string
}

type webhookPRReview struct {
	PRNumber    int64
	PRTitle     string
	PRHead      string
	Repo        string
	ReviewBody  string
	ReviewState string
	Reviewer    string
}

type prReviewState struct {
	PeerReviewed          bool
	PeerAgent             string
	Author                string
	TaskNumber            int64
	ChangesRequestedCount int
	ReviewFeedback        []string
}

type dispatchedTask struct {
	TaskNumber     int64     `json:"task_number"`
	Agent          string    `json:"agent"`
	DispatchedAt   time.Time `json:"dispatched_at"`
	Repo           string    `json:"repo"`
	Attempts       int       `json:"attempts"`
	FailedAgents   []string  `json:"failed_agents"`
	TaskTitle      string    `json:"task_title"`
	TaskBody       string    `json:"task_body"`
	TaskLabels     []string  `json:"task_labels"`
	ReviewFeedback []string  `json:"review_feedback"`
}

type persistedPipelineState struct {
	PRReviewState   map[string]prReviewState  `json:"pr_review_state"`
	DispatchedTasks map[int64]*dispatchedTask `json:"dispatched_tasks"`
}

func (g *gateway) validateWebhookSignature(body []byte, signature string) bool {
	if strings.TrimSpace(g.webhookSecret) == "" {
		return true
	}
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return false
	}
	got, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(g.webhookSecret))
	mac.Write(body)
	expected := mac.Sum(nil)
	return hmac.Equal(got, expected)
}

func (g *gateway) registerWebhookHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/webhooks/gitea", g.handleGiteaWebhook)
}

func (g *gateway) handleGiteaWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	if !g.validateWebhookSignature(body, r.Header.Get("X-Gitea-Signature")) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	giteaEvent := strings.TrimSpace(r.Header.Get("X-Gitea-Event"))
	if giteaEvent != "pull_request" && giteaEvent != "pull_request_review" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ignored"})
		return
	}

	var payload struct {
		Action     string `json:"action"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		PullRequest struct {
			Number  int64  `json:"number"`
			Title   string `json:"title"`
			Body    string `json:"body"`
			HTMLURL string `json:"html_url"`
			Merged  bool   `json:"merged"`
			User    struct {
				Login string `json:"login"`
			} `json:"user"`
			Head struct {
				Ref  string `json:"ref"`
				Repo struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"head"`
			Base struct {
				Ref  string `json:"ref"`
				Repo struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"base"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	repo := strings.TrimSpace(payload.PullRequest.Base.Repo.FullName)
	if repo == "" {
		repo = strings.TrimSpace(payload.PullRequest.Head.Repo.FullName)
	}
	if repo == "" {
		repo = strings.TrimSpace(payload.Repository.FullName)
	}
	pr := webhookPR{
		Number:  payload.PullRequest.Number,
		Title:   strings.TrimSpace(payload.PullRequest.Title),
		Body:    payload.PullRequest.Body,
		Repo:    repo,
		Base:    strings.TrimSpace(payload.PullRequest.Base.Ref),
		Head:    strings.TrimSpace(payload.PullRequest.Head.Ref),
		HTMLURL: strings.TrimSpace(payload.PullRequest.HTMLURL),
		User:    strings.TrimSpace(payload.PullRequest.User.Login),
		Merged:  payload.PullRequest.Merged,
	}
	dedupKey := fmt.Sprintf("%s:%s:%s:%d", giteaEvent, strings.TrimSpace(payload.Action), repo, pr.Number)
	if g.shouldSkipWebhookEvent(dedupKey) {
		log.Printf("webhook: dedup skipped %s", dedupKey)
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok"})
		return
	}

	if giteaEvent == "pull_request" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok"})

		switch {
		case strings.TrimSpace(payload.Action) == "opened":
			go g.handleAgentPROpened(pr)
		case strings.TrimSpace(payload.Action) == "synchronized":
			go g.handleAgentPRSynchronized(pr)
		case strings.TrimSpace(payload.Action) == "closed" && pr.Merged:
			go g.handlePRMerged(pr)
		}
		return
	}

	if giteaEvent == "pull_request_review" {
		var reviewPayload struct {
			Action     string `json:"action"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
			Review struct {
				Number int64  `json:"number"`
				State  string `json:"state"`
				Body   string `json:"body"`
				User   struct {
					Login string `json:"login"`
				} `json:"user"`
			} `json:"review"`
			PullRequest struct {
				Number int64  `json:"number"`
				Title  string `json:"title"`
				Head   struct {
					Ref string `json:"ref"`
				} `json:"head"`
				Base struct {
					Ref  string `json:"ref"`
					Repo struct {
						FullName string `json:"full_name"`
					} `json:"repo"`
				} `json:"base"`
			} `json:"pull_request"`
		}
		if err := json.Unmarshal(body, &reviewPayload); err != nil {
			log.Printf("webhook: failed to parse pull_request_review payload: %v", err)
			writeJSON(w, http.StatusOK, map[string]interface{}{"status": "error"})
			return
		}

		repo := strings.TrimSpace(reviewPayload.PullRequest.Base.Repo.FullName)
		if repo == "" {
			repo = strings.TrimSpace(reviewPayload.Repository.FullName)
		}

		if strings.TrimSpace(reviewPayload.Action) == "submitted" && reviewPayload.Review.State == "approved" {
			writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok"})
			go g.handlePRReviewApproved(webhookPRReview{
				PRNumber:    reviewPayload.PullRequest.Number,
				PRTitle:     strings.TrimSpace(reviewPayload.PullRequest.Title),
				PRHead:      strings.TrimSpace(reviewPayload.PullRequest.Head.Ref),
				Repo:        repo,
				ReviewBody:  strings.TrimSpace(reviewPayload.Review.Body),
				ReviewState: reviewPayload.Review.State,
				Reviewer:    strings.TrimSpace(reviewPayload.Review.User.Login),
			})
		} else if strings.TrimSpace(reviewPayload.Action) == "submitted" && reviewPayload.Review.State == "changes_requested" {
			rpr := webhookPRReview{
				PRNumber:    reviewPayload.PullRequest.Number,
				PRTitle:     strings.TrimSpace(reviewPayload.PullRequest.Title),
				PRHead:      strings.TrimSpace(reviewPayload.PullRequest.Head.Ref),
				Repo:        repo,
				ReviewBody:  strings.TrimSpace(reviewPayload.Review.Body),
				ReviewState: reviewPayload.Review.State,
				Reviewer:    strings.TrimSpace(reviewPayload.Review.User.Login),
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok"})
			go g.handleReviewChangesRequested(rpr)
		} else {
			writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ignored"})
		}
		return
	}
}

func (g *gateway) handleAgentPROpened(pr webhookPR) {
	log.Printf("webhook: agent PR opened repo=%s number=%d author=%s", pr.Repo, pr.Number, pr.User)
	if pr.Repo == "kit/valhalla-infra" && strings.HasPrefix(pr.Head, "ci/") {
		if err := g.mergeDevelopPR(pr.Repo, pr.Number); err != nil {
			log.Printf("webhook: CI auto-merge failed for PR #%d on %s: %v", pr.Number, pr.Repo, err)
		} else {
			log.Printf("webhook: auto-merged CI PR #%d on %s", pr.Number, pr.Repo)
		}
		return
	}
	if strings.EqualFold(pr.Base, "main") {
		g.dispatchPeerReview(pr)
		return
	}
	if strings.HasPrefix(pr.Head, "ci/") {
		log.Printf("webhook: review skipped for PR #%d on %s: ci branch %s", pr.Number, pr.Repo, pr.Head)
		return
	}
	owner, repoName, ok := splitFullRepoName(pr.Repo)
	if !ok {
		owner = ""
		repoName = strings.TrimSpace(pr.Repo)
	}
	reviewMsg := fmt.Sprintf(
		"Review PR #%d on %s: %q. Clone the repo if not already cloned, then run git fetch origin and git diff origin/develop...%s to review the changes. Alternatively try gitea action=list-pr-files owner=%s repo=%s index=%d if available. Assess code quality, correctness, and style. Then post a review comment on the PR using gitea action=create-comment owner=%s repo=%s issue=%d with your assessment. Be concise - 3-5 sentences max.",
		pr.Number,
		pr.Repo,
		pr.Title,
		pr.Head,
		owner,
		repoName,
		pr.Number,
		owner,
		repoName,
		pr.Number,
	)
	reviewer, ok := g.selectNonDevelopReviewer(g.builderFromBranch(pr.Head))
	if !ok {
		log.Printf("webhook: review skipped for PR #%d on %s: no healthy reviewer or builder available", pr.Number, pr.Repo)
		return
	}
	if _, err := g.dispatchReviewToAgent(reviewer.Name, reviewer.URL, pr, reviewMsg, "webhook_review"); err != nil {
		log.Printf("webhook: review dispatch failed for PR #%d on %s via %s: %v", pr.Number, pr.Repo, reviewer.Name, err)
	}
}

func (g *gateway) handleAgentPRSynchronized(pr webhookPR) {
	if !strings.EqualFold(pr.Base, "main") {
		return
	}
	key := g.prReviewKey(pr.Repo, pr.Number)
	g.clearPRReviewState(key)
	log.Printf("webhook: reset review pipeline state for PR #%d on %s after synchronize", pr.Number, pr.Repo)
	g.dispatchPeerReview(pr)
}

func (g *gateway) handlePRMerged(pr webhookPR) {
	if pr.Repo == "kit/valhalla-infra" && strings.HasPrefix(pr.Head, "ci/") {
		log.Printf("webhook: merged PR #%d on %s ignored (ci branch %s)", pr.Number, pr.Repo, pr.Head)
		return
	}
	log.Printf("webhook: merged PR repo=%s number=%d", pr.Repo, pr.Number)
	if owner, repoName, ok := splitFullRepoName(pr.Repo); ok {
		go func() {
			giteaClient := &http.Client{Timeout: 5 * time.Second}
			filesPath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/files", url.PathEscape(owner), url.PathEscape(repoName), pr.Number)
			var files []map[string]interface{}
			status, _, err := giteaGetJSONWithStatus(giteaClient, g.giteaURL, g.giteaToken, filesPath, &files)
			if err != nil || status < 200 || status >= 300 {
				return
			}
			paths := make([]string, 0, len(files))
			for _, file := range files {
				filename := strings.TrimSpace(asString(file["filename"]))
				if filename != "" {
					paths = append(paths, filename)
				}
			}
			filesList := strings.Join(paths, ", ")
			if len(filesList) > 500 {
				filesList = filesList[:500]
			}
			seidrRememberAsync(
				g.seidrURL,
				"warband",
				truncateWebhookMemory(fmt.Sprintf("WORKFLOW: PR #%d on %s by %s. Title: %s. Files: %s", pr.Number, pr.Repo, pr.User, pr.Title, filesList), 1000),
				"workflow",
			)
		}()
	}
	if matches := webhookTaskRefRE.FindStringSubmatch(pr.Body); len(matches) == 2 {
		log.Printf("webhook: merged PR #%d references task kit/hirdforge-tasks#%s", pr.Number, matches[1])
		if taskNumber, err := parseWebhookTaskNumber(matches[1]); err == nil {
			g.clearDispatchedTask(taskNumber)
		}
	}

	taskOwner, taskRepoName, ok := splitFullRepoName(g.taskRepo)
	if !ok {
		log.Printf("webhook: task dispatch skipped: invalid task repo %q", g.taskRepo)
		return
	}

	giteaClient := &http.Client{Timeout: 10 * time.Second}
	issuePath := fmt.Sprintf(
		"/api/v1/repos/%s/%s/issues?type=issues&state=open&labels=%s&sort=oldest&limit=10",
		url.PathEscape(taskOwner),
		url.PathEscape(taskRepoName),
		url.QueryEscape("status/ready"),
	)
	var issues []map[string]interface{}
	status, body, err := giteaGetJSONWithStatus(giteaClient, g.giteaURL, g.giteaToken, issuePath, &issues)
	if err != nil {
		log.Printf("webhook: task query failed after PR #%d on %s: %v", pr.Number, pr.Repo, err)
		return
	}
	if status < 200 || status >= 300 {
		log.Printf("webhook: task query returned %d after PR #%d on %s: %s", status, pr.Number, pr.Repo, strings.TrimSpace(string(body)))
		return
	}
	if len(issues) == 0 {
		log.Printf("webhook: no ready tasks after PR #%d on %s", pr.Number, pr.Repo)
		return
	}

	var task webhookIssue
	foundDispatchable := false
	for _, rawIssue := range issues {
		candidate := parseWebhookIssue(rawIssue)
		if candidate.Number == 0 {
			continue
		}
		skip := false
		for _, label := range candidate.Labels {
			if label == "tier/codex" {
				skip = true
				break
			}
		}
		if skip {
			log.Printf("webhook: skipping tier/codex task #%d during dispatch scan", candidate.Number)
			continue
		}
		task = candidate
		foundDispatchable = true
		break
	}
	if !foundDispatchable {
		log.Printf("webhook: no dispatchable tasks after PR #%d on %s", pr.Number, pr.Repo)
		return
	}
	tierName := ""
	for _, label := range task.Labels {
		if strings.HasPrefix(label, "tier/") {
			tierName = strings.TrimPrefix(label, "tier/")
		}
	}

	// Bifrost: intelligent agent selection
	labelAgent := ""
	for _, label := range task.Labels {
		if strings.HasPrefix(label, "agent/") {
			labelAgent = strings.TrimPrefix(label, "agent/")
		}
	}
	selectedAgent := ""
	reason := ""
	if labelAgent != "" {
		if agent, ok := g.getAgent(labelAgent); ok && agent.Healthy {
			selectedAgent = labelAgent
			reason = fmt.Sprintf("label: agent/%s", labelAgent)
		}
	}
	if selectedAgent == "" {
		selectedAgent, reason = g.selectAgentForTask(task)
		if selectedAgent == "" && labelAgent != "" {
			selectedAgent = labelAgent
			reason = "bifrost: no candidates, using label fallback"
		}
	}
	if selectedAgent == "" {
		log.Printf("webhook: task #%d skipped: no agent from bifrost or labels", task.Number)
		return
	}
	log.Printf("webhook: bifrost selected %s for task #%d (%s)", selectedAgent, task.Number, reason)

	targetAgent := selectedAgent
	agent, ok := g.getAgent(targetAgent)
	if !ok || !agent.Healthy {
		if fallback, ok := g.pickHealthyTaskAgent(tierName, targetAgent); ok {
			log.Printf("webhook: task #%d rerouted from %s to %s within tier %q", task.Number, targetAgent, fallback, tierName)
			targetAgent = fallback
			agent, _ = g.getAgent(targetAgent)
		} else {
			log.Printf("webhook: task #%d skipped: assigned agent %q unhealthy and no healthy fallback for tier %q", task.Number, targetAgent, tierName)
			g.sendDiscordWebhookNotification(
				"Task Dispatch Skipped",
				fmt.Sprintf("Task #%d could not be dispatched because `%s` was unavailable.", task.Number, targetAgent),
				15548997,
				[]discordField{
					{Name: "Task", Value: fmt.Sprintf("%s#%d", g.taskRepo, task.Number), Inline: false},
					{Name: "Tier", Value: blankAsNone(tierName), Inline: true},
				},
			)
			return
		}
	}

	removePath := fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d/labels/%d", url.PathEscape(taskOwner), url.PathEscape(taskRepoName), task.Number, webhookReadyLabelID)
	removeResp, err := giteaRequest(giteaClient, http.MethodDelete, g.giteaURL, g.giteaToken, removePath, nil)
	if err != nil {
		log.Printf("webhook: failed removing status/ready from task #%d: %v", task.Number, err)
	} else {
		removeBody, _ := io.ReadAll(io.LimitReader(removeResp.Body, 2048))
		removeResp.Body.Close()
		if removeResp.StatusCode >= 400 && removeResp.StatusCode != http.StatusNotFound {
			log.Printf("webhook: remove ready label returned %d for task #%d: %s", removeResp.StatusCode, task.Number, strings.TrimSpace(string(removeBody)))
		}
	}

	addPayload, _ := json.Marshal(map[string]interface{}{"labels": []int{webhookInProgressLabelID}})
	addPath := fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d/labels", url.PathEscape(taskOwner), url.PathEscape(taskRepoName), task.Number)
	addResp, err := giteaRequest(giteaClient, http.MethodPost, g.giteaURL, g.giteaToken, addPath, bytes.NewReader(addPayload))
	if err != nil {
		log.Printf("webhook: failed adding status/in-progress to task #%d: %v", task.Number, err)
		return
	}
	addBody, _ := io.ReadAll(io.LimitReader(addResp.Body, 2048))
	addResp.Body.Close()
	if addResp.StatusCode < 200 || addResp.StatusCode >= 300 {
		log.Printf("webhook: add in-progress label returned %d for task #%d: %s", addResp.StatusCode, task.Number, strings.TrimSpace(string(addBody)))
		return
	}

	if err := g.dispatchTaskToAgent(targetAgent, agent.URL, task, wrapTaskForDispatch(task.Body, task.Number), fmt.Sprintf("webhook-task-%d", task.Number)); err != nil {
		log.Printf("webhook: dispatch failed for task #%d to %s: %v", task.Number, targetAgent, err)
		return
	}
	g.recordDispatchedTask(&dispatchedTask{
		TaskNumber:   task.Number,
		Agent:        targetAgent,
		DispatchedAt: time.Now().UTC(),
		Repo:         pr.Repo,
		Attempts:     1,
		TaskTitle:    task.Title,
		TaskBody:     task.Body,
		TaskLabels:   append([]string(nil), task.Labels...),
	})

	log.Printf("webhook: dispatched task #%d to %s after merge of PR #%d on %s", task.Number, targetAgent, pr.Number, pr.Repo)
	g.sendDiscordWebhookNotification(
		"Task Dispatched",
		fmt.Sprintf("Dispatched `%s` on task #%d: %s", targetAgent, task.Number, task.Title),
		5763719,
		[]discordField{
			{Name: "Task Repo", Value: g.taskRepo, Inline: true},
			{Name: "PR", Value: pr.HTMLURL, Inline: false},
		},
	)
}

func (g *gateway) builderFromBranch(branch string) string {
	candidate := strings.TrimSpace(strings.SplitN(branch, "/", 2)[0])
	if candidate == "" {
		return ""
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if _, ok := g.agents[candidate]; ok {
		return candidate
	}
	return ""
}

func (g *gateway) prReviewKey(repo string, number int64) string {
	return fmt.Sprintf("%s:%d", strings.TrimSpace(repo), number)
}

func (g *gateway) getPRReviewState(key string) (prReviewState, bool) {
	g.prReviewMu.RLock()
	defer g.prReviewMu.RUnlock()
	state, ok := g.prReviewState[key]
	return state, ok
}

func (g *gateway) setPRReviewState(key string, state prReviewState) {
	g.prReviewMu.Lock()
	g.prReviewState[key] = state
	g.prReviewMu.Unlock()
	g.savePipelineState()
}

func (g *gateway) clearPRReviewState(key string) {
	g.prReviewMu.Lock()
	delete(g.prReviewState, key)
	g.prReviewMu.Unlock()
	g.savePipelineState()
}

func (g *gateway) snapshotPRReviewState() map[string]prReviewState {
	g.prReviewMu.RLock()
	defer g.prReviewMu.RUnlock()
	out := make(map[string]prReviewState, len(g.prReviewState))
	for key, state := range g.prReviewState {
		out[key] = state
	}
	return out
}

func (g *gateway) getDispatchedTask(taskNumber int64) (*dispatchedTask, bool) {
	g.dispatchedTasksMu.Lock()
	defer g.dispatchedTasksMu.Unlock()
	task, ok := g.dispatchedTasks[taskNumber]
	if !ok || task == nil {
		return nil, false
	}
	cp := *task
	cp.FailedAgents = append([]string(nil), task.FailedAgents...)
	cp.TaskLabels = append([]string(nil), task.TaskLabels...)
	cp.ReviewFeedback = append([]string(nil), task.ReviewFeedback...)
	return &cp, true
}

func (g *gateway) recordDispatchedTask(task *dispatchedTask) {
	if task == nil {
		return
	}
	g.dispatchedTasksMu.Lock()
	cp := *task
	cp.FailedAgents = append([]string(nil), task.FailedAgents...)
	cp.TaskLabels = append([]string(nil), task.TaskLabels...)
	cp.ReviewFeedback = append([]string(nil), task.ReviewFeedback...)
	g.dispatchedTasks[task.TaskNumber] = &cp
	g.dispatchedTasksMu.Unlock()
	g.savePipelineState()
}

func (g *gateway) clearDispatchedTask(taskNumber int64) {
	g.dispatchedTasksMu.Lock()
	delete(g.dispatchedTasks, taskNumber)
	g.dispatchedTasksMu.Unlock()
	g.savePipelineState()
}

func (g *gateway) snapshotDispatchedTasks() map[int64]*dispatchedTask {
	g.dispatchedTasksMu.Lock()
	defer g.dispatchedTasksMu.Unlock()
	out := make(map[int64]*dispatchedTask, len(g.dispatchedTasks))
	for key, task := range g.dispatchedTasks {
		if task == nil {
			continue
		}
		cp := *task
		cp.FailedAgents = append([]string(nil), task.FailedAgents...)
		cp.TaskLabels = append([]string(nil), task.TaskLabels...)
		cp.ReviewFeedback = append([]string(nil), task.ReviewFeedback...)
		out[key] = &cp
	}
	return out
}

func (g *gateway) savePipelineState() {
	if g.k8s == nil || !g.k8s.enabled {
		return
	}
	stateJSON, err := json.Marshal(persistedPipelineState{
		PRReviewState:   g.snapshotPRReviewState(),
		DispatchedTasks: g.snapshotDispatchedTasks(),
	})
	if err != nil {
		log.Printf("webhook: failed to marshal pipeline state: %v", err)
		return
	}
	configMapPath := fmt.Sprintf("/api/v1/namespaces/%s/configmaps/%s", pipelineStateNamespace, pipelineStateConfigMap)
	resp, err := g.k8s.do(http.MethodGet, configMapPath, nil)
	if err != nil {
		log.Printf("webhook: failed to read pipeline state ConfigMap: %v", err)
		return
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		if err := g.createPipelineStateConfigMap(string(stateJSON)); err != nil {
			log.Printf("webhook: failed to create pipeline state ConfigMap: %v", err)
		}
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		log.Printf("webhook: failed to read pipeline state ConfigMap: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		return
	}
	var configMap map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&configMap); err != nil {
		resp.Body.Close()
		log.Printf("webhook: failed to decode pipeline state ConfigMap: %v", err)
		return
	}
	resp.Body.Close()
	metadata, _ := configMap["metadata"].(map[string]interface{})
	resourceVersion := strings.TrimSpace(fmt.Sprint(metadata["resourceVersion"]))
	payload := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]string{
			"name":            pipelineStateConfigMap,
			"namespace":       pipelineStateNamespace,
			"resourceVersion": resourceVersion,
		},
		"data": map[string]string{
			"state": string(stateJSON),
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("webhook: failed to encode pipeline state ConfigMap: %v", err)
		return
	}
	updateResp, err := g.k8s.do(http.MethodPut, configMapPath, bytes.NewReader(body))
	if err != nil {
		log.Printf("webhook: failed to update pipeline state ConfigMap: %v", err)
		return
	}
	defer updateResp.Body.Close()
	if updateResp.StatusCode < 200 || updateResp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(updateResp.Body, 2048))
		log.Printf("webhook: failed to update pipeline state ConfigMap: status %d: %s", updateResp.StatusCode, strings.TrimSpace(string(respBody)))
	}
}

func (g *gateway) loadPipelineState() error {
	if g.k8s == nil || !g.k8s.enabled {
		return nil
	}
	configMapPath := fmt.Sprintf("/api/v1/namespaces/%s/configmaps/%s", pipelineStateNamespace, pipelineStateConfigMap)
	resp, err := g.k8s.do(http.MethodGet, configMapPath, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		if err := g.createPipelineStateConfigMap("{}"); err != nil {
			return err
		}
		g.prReviewMu.Lock()
		g.prReviewState = map[string]prReviewState{}
		g.prReviewMu.Unlock()
		g.dispatchedTasksMu.Lock()
		g.dispatchedTasks = map[int64]*dispatchedTask{}
		g.dispatchedTasksMu.Unlock()
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var configMap struct {
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&configMap); err != nil {
		resp.Body.Close()
		return err
	}
	resp.Body.Close()
	loaded := persistedPipelineState{
		PRReviewState:   map[string]prReviewState{},
		DispatchedTasks: map[int64]*dispatchedTask{},
	}
	if raw := strings.TrimSpace(configMap.Data["state"]); raw != "" {
		if err := json.Unmarshal([]byte(raw), &loaded); err != nil {
			legacy := map[string]prReviewState{}
			if err := json.Unmarshal([]byte(raw), &legacy); err != nil {
				return err
			}
			loaded.PRReviewState = legacy
		}
	}
	g.prReviewMu.Lock()
	g.prReviewState = loaded.PRReviewState
	g.prReviewMu.Unlock()
	g.dispatchedTasksMu.Lock()
	if loaded.DispatchedTasks == nil {
		loaded.DispatchedTasks = map[int64]*dispatchedTask{}
	}
	g.dispatchedTasks = loaded.DispatchedTasks
	g.dispatchedTasksMu.Unlock()
	return nil
}

func (g *gateway) createPipelineStateConfigMap(stateJSON string) error {
	payload := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]string{
			"name":      pipelineStateConfigMap,
			"namespace": pipelineStateNamespace,
		},
		"data": map[string]string{
			"state": stateJSON,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := g.k8s.do(http.MethodPost, fmt.Sprintf("/api/v1/namespaces/%s/configmaps", pipelineStateNamespace), bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

func (g *gateway) selectPeerReviewer(author string, excluded ...string) (*Agent, bool) {
	author = strings.TrimSpace(author)
	excludedSet := map[string]bool{}
	for _, name := range excluded {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			excludedSet[trimmed] = true
		}
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, name := range g.order {
		if name == "" || name == author || excludedSet[name] {
			continue
		}
		agent, ok := g.agents[name]
		if !ok || agent == nil || !agent.Healthy || agent.Role != "builder" {
			continue
		}
		cp := *agent
		return &cp, true
	}
	return nil, false
}

func (g *gateway) selectSecondPassReviewer(peerAgent string) (*Agent, bool) {
	g.mu.RLock()
	eligible := make([]*Agent, 0, len(g.order))
	for _, name := range g.order {
		agent, ok := g.agents[name]
		if !ok || agent == nil || !agent.Healthy || name == strings.TrimSpace(peerAgent) {
			continue
		}
		if agent.Role != "builder" && agent.Role != "reviewer" {
			continue
		}
		cp := *agent
		eligible = append(eligible, &cp)
	}
	g.mu.RUnlock()
	if len(eligible) == 0 {
		return nil, false
	}
	idx := int(atomic.AddUint64(&g.secondPassCounter, 1)-1) % len(eligible)
	return eligible[idx], true
}

func (g *gateway) selectNonDevelopReviewer(author string) (*Agent, bool) {
	author = strings.TrimSpace(author)
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, role := range []string{"reviewer", "builder"} {
		for _, name := range g.order {
			agent, ok := g.agents[name]
			if !ok || agent == nil || !agent.Healthy || agent.Role != role || name == author {
				continue
			}
			cp := *agent
			return &cp, true
		}
	}
	return nil, false
}

func (g *gateway) dispatchReviewToAgent(agentName string, agentURL string, pr webhookPR, prompt string, eventType string) (string, error) {
	sessionID := fmt.Sprintf("webhook-review-%s-%d-%s", sanitizeWebhookToken(pr.Repo), pr.Number, sanitizeWebhookToken(agentName))
	payload, err := json.Marshal(map[string]string{
		"content":    prompt,
		"session_id": sessionID,
	})
	if err != nil {
		return "", fmt.Errorf("marshal review request: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(agentURL, "/")+"/message", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("create review request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", fmt.Errorf("dispatch review request: %w", err)
	}
	reviewText, err := readAgentSSEContent(resp)
	if err != nil {
		return "", fmt.Errorf("read review response: %w", err)
	}
	reviewText = strings.TrimSpace(thinkTagRE.ReplaceAllString(reviewText, ""))
	log.Printf("webhook: %s completed for PR #%d on %s by %s: %s", eventType, pr.Number, pr.Repo, agentName, reviewText)
	g.addEvent(eventType, agentName, fmt.Sprintf("Reviewed PR #%d on %s", pr.Number, pr.Repo))
	return reviewText, nil
}

func (g *gateway) dispatchPeerReview(pr webhookPR) {
	// dispatchPeerReview selects a healthy peer builder and requests code review for a develop-targeted PR.
	author := g.builderFromBranch(pr.Head)
	peer, ok := g.selectPeerReviewer(author)
	if !ok {
		log.Printf("webhook: peer review skipped for PR #%d on %s: no healthy peer reviewer available", pr.Number, pr.Repo)
		return
	}
	stateAuthor := author
	if stateAuthor == "" {
		stateAuthor = "unknown"
		log.Printf("webhook: peer review author unknown for PR #%d on %s from branch %s; assigning healthy peer reviewer without self-review exclusion", pr.Number, pr.Repo, pr.Head)
	}
	key := g.prReviewKey(pr.Repo, pr.Number)
	taskNumber := extractTaskNumberFromText(pr.Body)
	g.setPRReviewState(key, prReviewState{
		PeerAgent:  peer.Name,
		Author:     stateAuthor,
		TaskNumber: taskNumber,
	})
	owner, repoName, ok := splitFullRepoName(pr.Repo)
	if !ok {
		owner = ""
		repoName = strings.TrimSpace(pr.Repo)
	}
	prompt := fmt.Sprintf(
		"Review PR #%d on %s: %q. Clone the repo if not already cloned, then run git fetch origin and git diff origin/develop...%s to review the changes. Alternatively try gitea action=list-pr-files owner=%s repo=%s index=%d if available. Assess correctness, code quality, and style. Submit a formal PR review using gitea action=create-review owner=%s repo=%s index=%d body with your concise assessment and state APPROVED or REQUEST_CHANGES.",
		pr.Number,
		pr.Repo,
		pr.Title,
		pr.Head,
		owner,
		repoName,
		pr.Number,
		owner,
		repoName,
		pr.Number,
	)
	reviewText, err := g.dispatchReviewToAgent(peer.Name, peer.URL, pr, prompt, "webhook_peer_review")
	if strings.Contains(strings.ToLower(reviewText), "429") {
		log.Printf("webhook: peer review hit 429 for PR #%d on %s via %s; retrying once after 30s", pr.Number, pr.Repo, peer.Name)
		time.Sleep(30 * time.Second)
		reviewText, err = g.dispatchReviewToAgent(peer.Name, peer.URL, pr, prompt, "webhook_peer_review")
		if strings.Contains(strings.ToLower(reviewText), "429") {
			err = fmt.Errorf("peer review response contained 429 after retry")
		}
	}
	if err != nil {
		log.Printf("webhook: peer review dispatch failed for PR #%d on %s via %s: %v", pr.Number, pr.Repo, peer.Name, err)
		retryPeer, retryOK := g.selectPeerReviewer(author, peer.Name)
		if !retryOK {
			g.clearPRReviewState(key)
			return
		}
		state, stateOK := g.getPRReviewState(key)
		if stateOK {
			state.PeerAgent = retryPeer.Name
			g.setPRReviewState(key, state)
		}
		reviewText, err = g.dispatchReviewToAgent(retryPeer.Name, retryPeer.URL, pr, prompt, "webhook_peer_review")
		if strings.Contains(strings.ToLower(reviewText), "429") {
			log.Printf("webhook: peer review retry hit 429 for PR #%d on %s via %s; retrying once after 30s", pr.Number, pr.Repo, retryPeer.Name)
			time.Sleep(30 * time.Second)
			reviewText, err = g.dispatchReviewToAgent(retryPeer.Name, retryPeer.URL, pr, prompt, "webhook_peer_review")
			if strings.Contains(strings.ToLower(reviewText), "429") {
				err = fmt.Errorf("peer review retry response contained 429 after retry")
			}
		}
		if err != nil {
			g.clearPRReviewState(key)
			log.Printf("webhook: peer review retry failed for PR #%d on %s via %s: %v", pr.Number, pr.Repo, retryPeer.Name, err)
			return
		}
		peer = retryPeer
	}
	log.Printf("webhook: queued peer review for PR #%d on %s via %s", pr.Number, pr.Repo, peer.Name)
	if looksApproved(reviewText) {
		state, ok := g.getPRReviewState(key)
		if !ok {
			return
		}
		state.PeerReviewed = true
		g.setPRReviewState(key, state)
		if pr.Repo == "kit/hirdforge-personas" || pr.Repo == "kit/hirdforge-tasks" {
			log.Printf("webhook: peer review text signaled approval for PR #%d on %s; skipping second-pass review", pr.Number, pr.Repo)
			if err := g.mergeDevelopPR(pr.Repo, pr.Number); err != nil {
				if g.handleSovereignMergePending(pr.Repo, pr.Number, err) {
					return
				}
				log.Printf("webhook: direct merge after peer text approval failed for PR #%d on %s: %v", pr.Number, pr.Repo, err)
			}
			return
		}
		log.Printf("webhook: peer review text signaled approval for PR #%d on %s; dispatching second-pass without waiting for review webhook", pr.Number, pr.Repo)
		g.dispatchSecondPassReview(pr)
	}
}

func (g *gateway) dispatchSecondPassReview(pr webhookPR) {
	key := g.prReviewKey(pr.Repo, pr.Number)
	state, ok := g.getPRReviewState(key)
	if !ok {
		log.Printf("webhook: second-pass review skipped for PR #%d on %s: no pipeline state", pr.Number, pr.Repo)
		return
	}
	reviewer, ok := g.selectSecondPassReviewer(state.PeerAgent)
	if !ok {
		log.Printf("webhook: second-pass review skipped for PR #%d on %s: no healthy builder/reviewer available", pr.Number, pr.Repo)
		return
	}
	owner, repoName, ok := splitFullRepoName(pr.Repo)
	if !ok {
		owner = ""
		repoName = strings.TrimSpace(pr.Repo)
	}
	prompt := fmt.Sprintf(
		"Peer review has approved PR #%d on %s: %q. Perform final review. Clone the repo if not already cloned, then run git fetch origin and git diff origin/develop...%s to review the changes. Alternatively try gitea action=list-pr-files owner=%s repo=%s index=%d if available. Submit a formal PR review using gitea action=create-review owner=%s repo=%s index=%d body with your concise assessment and state APPROVED or REQUEST_CHANGES.",
		pr.Number,
		pr.Repo,
		pr.Title,
		pr.Head,
		owner,
		repoName,
		pr.Number,
		owner,
		repoName,
		pr.Number,
	)
	reviewText, err := g.dispatchReviewToAgent(reviewer.Name, reviewer.URL, pr, prompt, "webhook_second_pass_review")
	if strings.Contains(strings.ToLower(reviewText), "429") {
		log.Printf("webhook: second-pass review hit 429 for PR #%d on %s via %s; retrying once after 45s", pr.Number, pr.Repo, reviewer.Name)
		time.Sleep(45 * time.Second)
		reviewText, err = g.dispatchReviewToAgent(reviewer.Name, reviewer.URL, pr, prompt, "webhook_second_pass_review")
		if strings.Contains(strings.ToLower(reviewText), "429") {
			err = fmt.Errorf("second-pass review response contained 429 after retry")
		}
	}
	if err != nil {
		log.Printf("webhook: second-pass review dispatch failed for PR #%d on %s via %s: %v", pr.Number, pr.Repo, reviewer.Name, err)
		return
	}
	if looksApproved(reviewText) {
		log.Printf("webhook: second-pass review text signaled approval for PR #%d on %s via %s; merging without waiting for review webhook", pr.Number, pr.Repo, reviewer.Name)
		if err := g.mergeDevelopPR(pr.Repo, pr.Number); err != nil {
			if g.handleSovereignMergePending(pr.Repo, pr.Number, err) {
				return
			}
			log.Printf("webhook: direct merge after second-pass text approval failed for PR #%d on %s: %v", pr.Number, pr.Repo, err)
		}
	}
}

func (g *gateway) handleSovereignMergePending(repo string, prNumber int64, err error) bool {
	msg := err.Error()
	if !strings.Contains(msg, "403") && !strings.Contains(msg, "405") {
		return false
	}
	log.Printf("webhook: PR #%d on %s approved — awaiting Sovereign merge", prNumber, repo)
	g.sendDiscordWebhookNotification(
		"Awaiting Sovereign",
		fmt.Sprintf("PR #%d on %s is approved and awaiting Sovereign merge.", prNumber, repo),
		16776960,
		[]discordField{
			{Name: "Repo", Value: repo, Inline: true},
			{Name: "PR", Value: fmt.Sprintf("#%d", prNumber), Inline: true},
		},
	)
	return true
}

func (g *gateway) handlePRReviewApproved(review webhookPRReview) {
	key := g.prReviewKey(review.Repo, review.PRNumber)
	state, ok := g.getPRReviewState(key)
	if !ok {
		log.Printf("webhook: approved review ignored for PR #%d on %s: no pipeline state", review.PRNumber, review.Repo)
		return
	}

	if state.PeerReviewed {
		if merged, err := g.prAlreadyMerged(review.Repo, review.PRNumber); err == nil && merged {
			log.Printf("webhook: approval ignored for PR #%d on %s: already merged", review.PRNumber, review.Repo)
			return
		}
	}

	pr := webhookPR{
		Number: review.PRNumber,
		Title:  review.PRTitle,
		Repo:   review.Repo,
		Head:   review.PRHead,
		Base:   "develop",
	}

	if !state.PeerReviewed {
		if review.Reviewer != "warband_review" {
			log.Printf("webhook: approved review ignored for PR #%d on %s: expected peer approval via warband_review for dispatched peer %s, got %s", review.PRNumber, review.Repo, state.PeerAgent, review.Reviewer)
			return
		}
		state.PeerReviewed = true
		g.setPRReviewState(key, state)
		log.Printf("webhook: peer review approved for PR #%d on %s by %s; dispatching second-pass", review.PRNumber, review.Repo, review.Reviewer)
		g.dispatchSecondPassReview(pr)
		return
	}

	if review.Reviewer != "warband_review" {
		log.Printf("webhook: second approval ignored for PR #%d on %s: expected second-pass approval via warband_review, got %s", review.PRNumber, review.Repo, review.Reviewer)
		return
	}

	if err := g.mergeDevelopPR(review.Repo, review.PRNumber); err != nil {
		log.Printf("webhook: merge failed for PR #%d on %s: %v", review.PRNumber, review.Repo, err)
		return
	}
	log.Printf("webhook: merged PR #%d on %s after peer and second-pass approvals", review.PRNumber, review.Repo)
	g.addEvent("webhook_pr_merged", "second-pass", fmt.Sprintf("Merged PR #%d on %s", review.PRNumber, review.Repo))
}

func (g *gateway) mergeDevelopPR(repo string, prNumber int64) error {
	owner, repoName, ok := splitFullRepoName(repo)
	if !ok {
		return fmt.Errorf("invalid repo %q", repo)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	updatePath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/update", url.PathEscape(owner), url.PathEscape(repoName), prNumber)
	updateResp, err := giteaRequest(client, http.MethodPost, g.giteaURL, g.giteaToken, updatePath, bytes.NewReader(mustJSON(map[string]string{})))
	if err != nil {
		log.Printf("webhook: update-branch failed for PR #%d on %s: %v", prNumber, repo, err)
	} else {
		updateBody, _ := io.ReadAll(io.LimitReader(updateResp.Body, 2048))
		updateResp.Body.Close()
		updateMsg := strings.TrimSpace(string(updateBody))
		switch {
		case updateResp.StatusCode >= 200 && updateResp.StatusCode < 300:
		case updateResp.StatusCode == http.StatusConflict && strings.Contains(strings.ToLower(updateMsg), "already up to date"):
		case updateResp.StatusCode == http.StatusConflict:
			return fmt.Errorf("update-branch conflict: %s", updateMsg)
		default:
			log.Printf("webhook: update-branch returned %d for PR #%d on %s: %s", updateResp.StatusCode, prNumber, repo, updateMsg)
		}
	}
	payload := map[string]string{"Do": "merge"}
	resp, err := giteaRequest(client, http.MethodPost, g.giteaURL, g.giteaToken, fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/merge", url.PathEscape(owner), url.PathEscape(repoName), prNumber), bytes.NewReader(mustJSON(payload)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("merge returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	g.clearPRReviewState(g.prReviewKey(repo, prNumber))
	return nil
}

func (g *gateway) prAlreadyMerged(repo string, prNumber int64) (bool, error) {
	owner, repoName, ok := splitFullRepoName(repo)
	if !ok {
		return false, fmt.Errorf("invalid repo %q", repo)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	var pull map[string]interface{}
	status, body, err := giteaGetJSONWithStatus(client, g.giteaURL, g.giteaToken, fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d", url.PathEscape(owner), url.PathEscape(repoName), prNumber), &pull)
	if err != nil {
		return false, err
	}
	if status < 200 || status >= 300 {
		return false, fmt.Errorf("status %d: %s", status, strings.TrimSpace(string(body)))
	}
	return pull["merged"] == true, nil
}

func looksApproved(text string) bool {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "request_changes") ||
		strings.Contains(lower, "changes requested") ||
		strings.Contains(lower, "requesting changes") {
		return false
	}
	return strings.Contains(lower, "approved") ||
		strings.Contains(lower, "approve") ||
		strings.Contains(lower, "ready to merge") ||
		strings.Contains(lower, "lgtm") ||
		strings.Contains(lower, "no issues") ||
		strings.Contains(lower, "looks good") ||
		strings.Contains(lower, "ship it") ||
		strings.Contains(lower, "no critical") ||
		strings.Contains(lower, "no concerns")
}

func mustJSON(v interface{}) []byte {
	body, _ := json.Marshal(v)
	return body
}

func (g *gateway) handleReviewChangesRequested(review webhookPRReview) {
	key := g.prReviewKey(review.Repo, review.PRNumber)
	state, hasState := g.getPRReviewState(key)
	state.ChangesRequestedCount++
	if body := strings.TrimSpace(review.ReviewBody); body != "" {
		state.ReviewFeedback = append(state.ReviewFeedback, body)
	}
	if hasState {
		g.setPRReviewState(key, state)
	}
	builder := g.builderFromBranch(review.PRHead)
	if builder == "" {
		log.Printf("webhook: review feedback skipped - unable to detect builder from branch %s", review.PRHead)
		return
	}
	phase := "peer"
	if hasState {
		if state.PeerReviewed {
			phase = "second-pass"
		}
	} else {
		phase = "unknown"
	}
	if hasState && state.ChangesRequestedCount >= 2 && state.TaskNumber > 0 {
		task, ok := g.getDispatchedTask(state.TaskNumber)
		if !ok {
			log.Printf("webhook: review escalation skipped for PR #%d on %s: no dispatched task state for #%d", review.PRNumber, review.Repo, state.TaskNumber)
			return
		}
		task.ReviewFeedback = append(append([]string(nil), task.ReviewFeedback...), state.ReviewFeedback...)
		g.recordDispatchedTask(task)
		g.clearPRReviewState(key)
		log.Printf("webhook: rotating task #%d after %d changes_requested reviews on PR #%d during %s phase", task.TaskNumber, state.ChangesRequestedCount, review.PRNumber, phase)
		g.rotateTask(task)
		return
	}
	agent, ok := g.getAgent(builder)
	if !ok || !agent.Healthy {
		log.Printf("webhook: review feedback skipped - builder agent %s not found or unhealthy", builder)
		return
	}
	g.clearPRReviewState(key)
	owner, repoName, ok := splitFullRepoName(review.Repo)
	if !ok {
		log.Printf("webhook: fix dispatch skipped - invalid repo %q", review.Repo)
		return
	}
	fixMsg := fmt.Sprintf(
		"A reviewer requested changes on PR #%d (%q) on %s.\n\nFeedback:\n%s\n\nFIX INSTRUCTIONS:\n1. Clone the repo: git-clone url=%s/%s/%s.git\n2. Fetch and checkout the existing branch:\n   exec: cd /workspace/%s && git fetch origin %s && git checkout %s\n3. Make the requested fixes\n4. Commit: git-commit message=\"fix: address review feedback on PR #%d\"\n5. Push to the SAME branch: exec: cd /workspace/%s && git push origin %s\n6. Do NOT create a new PR. Pushing to the branch updates the existing PR automatically.",
		review.PRNumber,
		review.PRTitle,
		review.Repo,
		review.ReviewBody,
		g.giteaURL,
		owner,
		repoName,
		repoName,
		review.PRHead,
		review.PRHead,
		review.PRNumber,
		repoName,
		review.PRHead,
	)
	if body := strings.TrimSpace(review.ReviewBody); len(body) > 20 {
		seidrRememberAsync(
			g.seidrURL,
			"warband",
			truncateWebhookMemory(fmt.Sprintf("REVIEW_LESSON: Reviewer flagged issue on PR #%d (%s). Feedback: %s. File context: branch %s", review.PRNumber, review.Repo, body, review.PRHead), 1000),
			"review_lesson",
		)
	}
	sessionID := fmt.Sprintf("webhook-review-fix-%s-%d", sanitizeWebhookToken(review.Repo), review.PRNumber)
	payload, err := json.Marshal(map[string]string{
		"content":    fixMsg,
		"session_id": sessionID,
	})
	if err != nil {
		log.Printf("webhook: build fix request failed for PR #%d on %s: %v", review.PRNumber, review.Repo, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(agent.URL, "/")+"/message", bytes.NewReader(payload))
	if err != nil {
		log.Printf("webhook: create fix request failed for PR #%d on %s: %v", review.PRNumber, review.Repo, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	agentClient := &http.Client{}
	agentResp, err := agentClient.Do(req)
	if err != nil {
		log.Printf("webhook: fix dispatch failed for PR #%d on %s to %s: %v", review.PRNumber, review.Repo, builder, err)
		return
	}
	defer agentResp.Body.Close()
	if agentResp.StatusCode < 200 || agentResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(agentResp.Body, 2048))
		log.Printf("webhook: fix dispatch returned %d for PR #%d on %s: %s", agentResp.StatusCode, review.PRNumber, review.Repo, strings.TrimSpace(string(body)))
		return
	}
	log.Printf("webhook: queued %s-phase fix request for PR #%d on %s to builder %s after review by %s", phase, review.PRNumber, review.Repo, builder, review.Reviewer)
	g.addEvent("webhook_review_fix", builder, fmt.Sprintf("Queued %s-phase fix request for PR #%d on %s", phase, review.PRNumber, review.Repo))
}

func truncateWebhookMemory(content string, max int) string {
	content = strings.TrimSpace(content)
	if max <= 0 || len(content) <= max {
		return content
	}
	return content[:max]
}

func seidrRememberAsync(seidrURL, agentName, content, memoryType string) {
	if strings.TrimSpace(seidrURL) == "" || strings.TrimSpace(content) == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		payload, _ := json.Marshal(map[string]interface{}{
			"agent_name": agentName,
			"content":    content,
			"type":       memoryType,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(seidrURL, "/")+"/remember", bytes.NewReader(payload))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		resp.Body.Close()
	}()
}

func (g *gateway) dispatchTaskToAgent(agentName, agentURL string, task webhookIssue, content, sessionID string) error {
	taskReqBody, err := json.Marshal(map[string]string{
		"content":    content,
		"session_id": sessionID,
	})
	if err != nil {
		return fmt.Errorf("marshal dispatch: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(agentURL, "/")+"/message", bytes.NewReader(taskReqBody))
	if err != nil {
		return fmt.Errorf("create dispatch request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	agentClient := &http.Client{Timeout: 5 * time.Second}
	resp, err := agentClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("dispatch returned %d", resp.StatusCode)
	}
	g.addEvent("webhook_dispatch", agentName, fmt.Sprintf("Dispatched task #%d: %s", task.Number, task.Title))
	return nil
}

func (g *gateway) runTaskCompletionGuard() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		g.checkDispatchedTasks()
	}
}

func (g *gateway) shouldSkipWebhookEvent(key string) bool {
	if key == "" {
		return false
	}
	now := time.Now()
	if existing, ok := g.webhookDedup.Load(key); ok {
		if ts, ok := existing.(time.Time); ok && now.Sub(ts) < 2*time.Minute {
			return true
		}
	}
	g.webhookDedup.Store(key, now)
	return false
}

func (g *gateway) runWebhookDedupCleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-2 * time.Minute)
		g.webhookDedup.Range(func(key, value interface{}) bool {
			ts, ok := value.(time.Time)
			if !ok || ts.Before(cutoff) {
				g.webhookDedup.Delete(key)
			}
			return true
		})
	}
}

func (g *gateway) checkDispatchedTasks() {
	for _, task := range g.snapshotDispatchedTasks() {
		if task == nil || time.Since(task.DispatchedAt) < 30*time.Minute {
			continue
		}
		hasPR, err := g.taskHasOpenPR(task)
		if err != nil {
			log.Printf("webhook: completion guard check failed for task #%d (%s): %v", task.TaskNumber, task.Agent, err)
			continue
		}
		if hasPR {
			log.Printf("webhook: completion guard confirmed open PR for task #%d from %s", task.TaskNumber, task.Agent)
			continue
		}
		log.Printf("webhook: completion guard timed out task #%d for %s after %s with no PR", task.TaskNumber, task.Agent, time.Since(task.DispatchedAt).Round(time.Minute))
		g.rotateTask(task)
	}
}

func (g *gateway) taskHasOpenPR(task *dispatchedTask) (bool, error) {
	owner, repoName, ok := splitFullRepoName(task.Repo)
	if !ok {
		return false, fmt.Errorf("invalid repo %q", task.Repo)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	path := fmt.Sprintf("/api/v1/repos/%s/%s/pulls?state=open&limit=50", url.PathEscape(owner), url.PathEscape(repoName))
	var pulls []map[string]interface{}
	status, body, err := giteaGetJSONWithStatus(client, g.giteaURL, g.giteaToken, path, &pulls)
	if err != nil {
		return false, err
	}
	if status < 200 || status >= 300 {
		return false, fmt.Errorf("status %d: %s", status, strings.TrimSpace(string(body)))
	}
	branchPrefix := strings.TrimSpace(task.Agent) + "/"
	for _, pull := range pulls {
		head := asMap(pull["head"])
		if !strings.HasPrefix(strings.TrimSpace(asString(head["ref"])), branchPrefix) {
			continue
		}
		if extractTaskNumberFromText(asString(pull["body"])) == task.TaskNumber {
			return true, nil
		}
	}
	return false, nil
}

func (g *gateway) rotateTask(task *dispatchedTask) {
	if task == nil {
		return
	}
	if strings.TrimSpace(task.Agent) != "" {
		task.FailedAgents = appendIfMissing(task.FailedAgents, task.Agent)
	}
	if task.Attempts >= 3 {
		log.Printf("webhook: task #%d exceeded retry budget after %d attempts", task.TaskNumber, task.Attempts)
		g.markTaskFailed(task.TaskNumber)
		g.clearDispatchedTask(task.TaskNumber)
		return
	}
	nextAgent, reason := g.selectAgentForTaskExcluding(task, task.FailedAgents)
	if nextAgent == "" {
		log.Printf("webhook: task #%d failed - no healthy replacement agent available", task.TaskNumber)
		g.markTaskFailed(task.TaskNumber)
		g.clearDispatchedTask(task.TaskNumber)
		return
	}
	agent, ok := g.getAgent(nextAgent)
	if !ok || !agent.Healthy {
		log.Printf("webhook: task #%d failed - replacement agent %s unavailable", task.TaskNumber, nextAgent)
		g.markTaskFailed(task.TaskNumber)
		g.clearDispatchedTask(task.TaskNumber)
		return
	}
	prefix := fmt.Sprintf("Previous agent %s failed to complete this task. Their attempt timed out after 30 minutes with no PR created. Pick up where they left off.\n\n", task.Agent)
	if len(task.ReviewFeedback) > 0 {
		prefix += "Accumulated review feedback:\n- " + strings.Join(task.ReviewFeedback, "\n- ") + "\n\n"
	}
	issue := webhookIssue{
		Number: task.TaskNumber,
		Title:  task.TaskTitle,
		Body:   task.TaskBody,
		Labels: append([]string(nil), task.TaskLabels...),
	}
	if err := g.dispatchTaskToAgent(nextAgent, agent.URL, issue, prefix+wrapTaskForDispatch(task.TaskBody, task.TaskNumber), fmt.Sprintf("webhook-task-%d-retry-%d", task.TaskNumber, task.Attempts)); err != nil {
		log.Printf("webhook: task #%d rotation dispatch to %s failed: %v", task.TaskNumber, nextAgent, err)
		g.markTaskFailed(task.TaskNumber)
		g.clearDispatchedTask(task.TaskNumber)
		return
	}
	task.Agent = nextAgent
	task.Attempts++
	task.DispatchedAt = time.Now().UTC()
	g.recordDispatchedTask(task)
	log.Printf("webhook: rotated task #%d to %s (%s)", task.TaskNumber, nextAgent, reason)
}

func (g *gateway) selectAgentForTaskExcluding(task *dispatchedTask, failed []string) (string, string) {
	excluded := map[string]bool{}
	for _, name := range failed {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			excluded[trimmed] = true
		}
	}
	issue := webhookIssue{
		Number: task.TaskNumber,
		Title:  task.TaskTitle,
		Body:   task.TaskBody,
		Labels: append([]string(nil), task.TaskLabels...),
	}
	if agentName, reason := g.selectAgentForTask(issue); agentName != "" && !excluded[agentName] {
		return agentName, reason
	}
	for _, candidate := range g.order {
		if excluded[candidate] {
			continue
		}
		agent, ok := g.getAgent(candidate)
		if ok && agent.Healthy && agent.Role == "builder" {
			return candidate, "rotation fallback: healthy agent not in failed set"
		}
	}
	return "", ""
}

func (g *gateway) markTaskFailed(taskNumber int64) {
	taskOwner, taskRepoName, ok := splitFullRepoName(g.taskRepo)
	if !ok {
		log.Printf("webhook: failed task update skipped: invalid task repo %q", g.taskRepo)
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	labelID, err := g.lookupIssueLabelID(client, taskOwner, taskRepoName, "status/failed")
	if err != nil {
		log.Printf("webhook: failed task #%d could not resolve status/failed label: %v", taskNumber, err)
		return
	}
	payload := map[string]interface{}{"labels": []int64{labelID}}
	resp, err := giteaRequest(client, http.MethodPost, g.giteaURL, g.giteaToken, fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d/labels", url.PathEscape(taskOwner), url.PathEscape(taskRepoName), taskNumber), bytes.NewReader(mustJSON(payload)))
	if err != nil {
		log.Printf("webhook: failed task #%d label update failed: %v", taskNumber, err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("webhook: failed task #%d label update returned %d: %s", taskNumber, resp.StatusCode, strings.TrimSpace(string(body)))
		return
	}
	log.Printf("webhook: marked task #%d status/failed", taskNumber)
}

func (g *gateway) lookupIssueLabelID(client *http.Client, owner, repoName, labelName string) (int64, error) {
	var labels []map[string]interface{}
	status, body, err := giteaGetJSONWithStatus(client, g.giteaURL, g.giteaToken, fmt.Sprintf("/api/v1/repos/%s/%s/labels?limit=100", url.PathEscape(owner), url.PathEscape(repoName)), &labels)
	if err != nil {
		return 0, err
	}
	if status < 200 || status >= 300 {
		return 0, fmt.Errorf("status %d: %s", status, strings.TrimSpace(string(body)))
	}
	for _, label := range labels {
		if strings.TrimSpace(asString(label["name"])) == labelName {
			return asInt64(label["id"]), nil
		}
	}
	return 0, fmt.Errorf("label %q not found", labelName)
}

func parseWebhookTaskNumber(raw string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
}

func extractTaskNumberFromText(text string) int64 {
	matches := webhookTaskRefRE.FindStringSubmatch(text)
	if len(matches) != 2 {
		return 0
	}
	n, _ := parseWebhookTaskNumber(matches[1])
	return n
}

func appendIfMissing(items []string, candidate string) []string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return items
	}
	for _, item := range items {
		if strings.TrimSpace(item) == candidate {
			return items
		}
	}
	return append(items, candidate)
}

func (g *gateway) sendDiscordWebhookNotification(title, description string, color int, fields []discordField) {
	webhookURL := strings.TrimSpace(g.discordWebhookURL)
	if webhookURL == "" {
		return
	}
	fieldPayload := make([]map[string]interface{}, 0, len(fields))
	for _, field := range fields {
		fieldPayload = append(fieldPayload, map[string]interface{}{
			"name":   field.Name,
			"value":  field.Value,
			"inline": field.Inline,
		})
	}
	payload := map[string]interface{}{
		"embeds": []map[string]interface{}{{
			"title":       title,
			"description": description,
			"color":       color,
			"fields":      fieldPayload,
			"footer": map[string]string{
				"text": "Valhalla Gateway",
			},
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("webhook: discord marshal failed: %v", err)
		return
	}
	req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		log.Printf("webhook: discord request build failed: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("webhook: discord send failed: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		log.Printf("webhook: discord returned %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
}

func (g *gateway) ensureGiteaWebhooks() {
	if strings.TrimSpace(g.giteaURL) == "" || strings.TrimSpace(g.giteaToken) == "" {
		log.Printf("webhook: auto-registration skipped: missing Gitea URL or token")
		return
	}
	g.reposMu.RLock()
	repos := append([]string(nil), g.repos...)
	g.reposMu.RUnlock()
	if len(repos) == 0 {
		g.refreshRepos()
		g.reposMu.RLock()
		repos = append([]string(nil), g.repos...)
		g.reposMu.RUnlock()
	}
	if len(repos) == 0 {
		log.Printf("webhook: auto-registration skipped: no discovered repos")
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	for _, fullRepo := range repos {
		owner, repoName, ok := splitFullRepoName(fullRepo)
		if !ok {
			log.Printf("webhook: auto-registration skipped invalid repo %q", fullRepo)
			continue
		}
		path := fmt.Sprintf("/api/v1/repos/%s/%s/hooks", url.PathEscape(owner), url.PathEscape(repoName))
		var hooks []map[string]interface{}
		status, body, err := giteaGetJSONWithStatus(client, g.giteaURL, g.giteaToken, path, &hooks)
		if err != nil {
			log.Printf("webhook: list hooks failed for %s: %v", fullRepo, err)
			continue
		}
		if status < 200 || status >= 300 {
			log.Printf("webhook: list hooks returned %d for %s: %s", status, fullRepo, strings.TrimSpace(string(body)))
			continue
		}
		found := false
		for _, hook := range hooks {
			config, _ := hook["config"].(map[string]interface{})
			if strings.TrimSpace(fmt.Sprint(config["url"])) == webhookEndpointURL {
				found = true
				break
			}
		}
		if found {
			log.Printf("webhook: existing Gitea webhook already configured for %s", fullRepo)
			continue
		}
		payload := map[string]interface{}{
			"type":   "gitea",
			"active": true,
			"config": map[string]interface{}{
				"url":          webhookEndpointURL,
				"content_type": "json",
				"secret":       g.webhookSecret,
			},
			"events": []string{"pull_request", "pull_request_review"},
		}
		bodyJSON, err := json.Marshal(payload)
		if err != nil {
			log.Printf("webhook: marshal hook payload failed for %s: %v", fullRepo, err)
			continue
		}
		resp, err := giteaRequest(client, http.MethodPost, g.giteaURL, g.giteaToken, path, bytes.NewReader(bodyJSON))
		if err != nil {
			log.Printf("webhook: create hook failed for %s: %v", fullRepo, err)
			continue
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			log.Printf("webhook: create hook returned %d for %s: %s", resp.StatusCode, fullRepo, strings.TrimSpace(string(respBody)))
			continue
		}
		log.Printf("webhook: created Gitea webhook for %s", fullRepo)
	}
}

func splitFullRepoName(full string) (string, string, bool) {
	full = strings.TrimSpace(full)
	parts := strings.SplitN(full, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", false
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
}

func sanitizeWebhookToken(s string) string {
	replacer := strings.NewReplacer("/", "-", " ", "-", "_", "-")
	return replacer.Replace(strings.TrimSpace(s))
}

func readAgentSSEContent(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	reader := bufio.NewReader(resp.Body)
	var content strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data:") {
				raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if raw != "" {
					var evt map[string]interface{}
					if json.Unmarshal([]byte(raw), &evt) == nil {
						if chunk, ok := evt["content"].(string); ok {
							content.WriteString(chunk)
						}
					}
				}
			}
		}
		if err == io.EOF {
			return content.String(), nil
		}
		if err != nil {
			return "", err
		}
	}
}

func parseWebhookIssue(raw map[string]interface{}) webhookIssue {
	issue := webhookIssue{
		Title: strings.TrimSpace(fmt.Sprint(raw["title"])),
		Body:  fmt.Sprint(raw["body"]),
	}
	switch n := raw["number"].(type) {
	case float64:
		issue.Number = int64(n)
	case int64:
		issue.Number = n
	}
	if labels, ok := raw["labels"].([]interface{}); ok {
		for _, item := range labels {
			labelMap, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			name := strings.TrimSpace(fmt.Sprint(labelMap["name"]))
			if name != "" && name != "<nil>" {
				issue.Labels = append(issue.Labels, name)
			}
		}
	}
	return issue
}

func wrapTaskForDispatch(taskBody string, taskNumber int64) string {
	return fmt.Sprintf(`AUTONOMOUS TASK DISPATCH - Issue #%d

CRITICAL RULES:
1. Execute EVERY step as a tool call. Do NOT describe or narrate steps - CALL THE TOOL.
2. If you find yourself writing "I will now..." - STOP and make the actual tool call.
3. After EVERY tool call, check the output before proceeding.
4. If any step fails, try to fix it. If you cannot fix it after 2 attempts, stop.

ENVIRONMENT: This container runs Alpine sh (POSIX). No bash. No associative arrays. Use sort/uniq/awk.

VERIFICATION REQUIRED:
- After git push: run `+"`"+`git ls-remote origin <branch>`+"`"+` to verify the branch exists on remote.
- After gitea create-pr: confirm the tool returned a PR URL.
- If push or PR creation fails, debug and retry.

The task is NOT done until a PR URL is confirmed.

--- TASK BODY ---

%s

--- END TASK BODY ---

Remember: Execute tools, don't narrate. Verify push with ls-remote. Confirm PR URL.
`, taskNumber, taskBody)
}

func (g *gateway) pickHealthyTaskAgent(tierName, preferred string) (string, bool) {
	if strings.TrimSpace(tierName) != "autonomous" {
		return "", false
	}
	for _, candidate := range g.order {
		if candidate == strings.TrimSpace(preferred) {
			continue
		}
		agent, ok := g.getAgent(candidate)
		if ok && agent.Healthy && agent.Role == "builder" {
			return candidate, true
		}
	}
	return "", false
}

func blankAsNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none"
	}
	return s
}
