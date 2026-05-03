package main

import (
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
	"strings"
	"time"
)

const (
	webhookReadyLabelID      = 3
	webhookInProgressLabelID = 4
	webhookEndpointURL       = "http://gateway.asgard.svc:8080/api/v1/webhooks/gitea"
)

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
	if giteaEvent == "pull_request_review" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ignored"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok"})
	switch strings.TrimSpace(payload.Action) {
	case "opened":
		go g.handleAgentPROpened(pr)
	case "synchronized":
		go g.handleAgentPRSynchronized(pr)
	case "closed":
		if pr.Merged {
			go g.handlePRMerged(pr)
		}
	}
}

func (g *gateway) handleAgentPROpened(pr webhookPR) {
	log.Printf("webhook: agent PR opened repo=%s number=%d author=%s", pr.Repo, pr.Number, pr.User)
	g.dispatchA2APeerReview(pr)
}

func (g *gateway) handleAgentPRSynchronized(pr webhookPR) {
	log.Printf("webhook: PR #%d on %s synchronized; dispatching fresh A2A review", pr.Number, pr.Repo)
	g.dispatchA2APeerReview(pr)
}

func (g *gateway) dispatchA2APeerReview(pr webhookPR) {
	if strings.HasPrefix(pr.Head, "ci/") {
		log.Printf("webhook: review skipped for PR #%d on %s: ci branch %s", pr.Number, pr.Repo, pr.Head)
		return
	}
	author := g.builderFromBranch(pr.Head)
	warband := g.agentWarband(author)
	peerName := ""
	fallbackPeer := ""
	g.mu.RLock()
	for _, name := range g.order {
		agent := g.agents[name]
		if name == "" || name == author || agent == nil || !agent.Healthy || agent.Role != "builder" {
			continue
		}
		if fallbackPeer == "" {
			fallbackPeer = name
		}
		if normalizeWarbandName(agent.Warband) == warband {
			peerName = name
			break
		}
	}
	g.mu.RUnlock()
	if peerName == "" {
		peerName = fallbackPeer
	}
	if peerName == "" {
		log.Printf("webhook: A2A peer review skipped for PR #%d on %s: no healthy peer builder available", pr.Number, pr.Repo)
		return
	}

	now := time.Now().UTC()
	taskID := fmt.Sprintf("review-%s-%d-%d", pr.Repo, pr.Number, now.Unix())
	if g.a2aStore != nil {
		task := &Task{
			ID:        taskID,
			ContextID: fmt.Sprintf("pr-review-%s-%d", pr.Repo, pr.Number),
			Status: TaskStatus{
				State:     TaskStateSubmitted,
				Timestamp: now.Format(time.RFC3339),
			},
			Metadata: map[string]interface{}{
				"type":      "peer_review",
				"repo":      pr.Repo,
				"pr_number": pr.Number,
				"pr_title":  pr.Title,
				"pr_head":   pr.Head,
				"pr_author": author,
			},
			Agent: peerName,
		}
		if err := g.a2aStore.CreateTask(task); err != nil {
			log.Printf("webhook: failed to create A2A peer review task for PR #%d on %s: %v", pr.Number, pr.Repo, err)
		}
	}

	messageText := fmt.Sprintf(
		"Review PR #%d on %s: %q.\nClone the repo, run git fetch origin, and review the diff for branch %s.\nAssess correctness, code quality, and style.\nUse the create-review tool to post a formal Gitea review with event APPROVED or REQUEST_CHANGES on the PR.\nThen report your review result to ragnar using the delegate tool:\ndelegate agent=ragnar message='Review complete for PR #%d on %s. Result: [APPROVED/REQUEST_CHANGES]. Summary: [your 2-3 sentence assessment]'",
		pr.Number,
		pr.Repo,
		pr.Title,
		pr.Head,
		pr.Number,
		pr.Repo,
	)
	body := mustJSON(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "message/send",
		"params": map[string]interface{}{
			"message": map[string]interface{}{
				"role":      "user",
				"parts":     []map[string]string{{"text": messageText}},
				"messageId": fmt.Sprintf("review-%s-%d", pr.Repo, pr.Number),
			},
			"pushNotification": map[string]string{
				"url": "http://gateway.asgard.svc:8080/api/v1/a2a/notify",
			},
		},
		"id": fmt.Sprintf("review-dispatch-%d", pr.Number),
	})
	go func(peer string, payload []byte) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://%s.asgard.svc:8081/a2a", peer), bytes.NewReader(payload))
		if err != nil {
			log.Printf("webhook: failed to build A2A peer review request for PR #%d on %s to %s: %v", pr.Number, pr.Repo, peer, err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			log.Printf("webhook: A2A peer review dispatch failed for PR #%d on %s to %s: %v", pr.Number, pr.Repo, peer, err)
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			log.Printf("webhook: A2A peer review dispatch returned %d for PR #%d on %s to %s: %s", resp.StatusCode, pr.Number, pr.Repo, peer, strings.TrimSpace(string(respBody)))
		}
	}(peerName, body)

	log.Printf("webhook: dispatched A2A peer review for PR #%d on %s to %s", pr.Number, pr.Repo, peerName)
	g.addEvent("webhook_a2a_review", peerName, fmt.Sprintf("Dispatched A2A peer review for PR #%d on %s", pr.Number, pr.Repo))
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
	if taskNumber := extractTaskNumberFromText(pr.Body); taskNumber > 0 {
		log.Printf("webhook: merged PR #%d references task kit/hirdforge-tasks#%d", pr.Number, taskNumber)
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
	author := g.builderFromBranch(pr.Head)
	warband := g.agentWarband(author)

	tierName := ""
	for _, label := range task.Labels {
		if strings.HasPrefix(label, "tier/") {
			tierName = strings.TrimPrefix(label, "tier/")
		}
	}
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
			if normalizeWarbandName(agent.Warband) == warband {
				selectedAgent = labelAgent
				reason = fmt.Sprintf("label: agent/%s", labelAgent)
			}
		}
	}
	if selectedAgent == "" {
		selectedAgent, reason = g.selectAgentForTask(task, warband)
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
		if fallback, ok := g.pickHealthyTaskAgent(tierName, targetAgent, warband); ok {
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

	dispatch := dispatchedTask{
		TaskNumber:   task.Number,
		Agent:        targetAgent,
		DispatchedAt: time.Now().UTC(),
		Repo:         pr.Repo,
		Warband:      warband,
		Attempts:     1,
		TaskTitle:    task.Title,
		TaskBody:     task.Body,
		TaskLabels:   append([]string(nil), task.Labels...),
	}
	if err := g.dispatchTaskToAgent(dispatch.Agent, agent.URL, task, wrapTaskForDispatch(task.Body, task.Number), fmt.Sprintf("webhook-task-%d", task.Number)); err != nil {
		log.Printf("webhook: dispatch failed for task #%d to %s: %v", task.Number, dispatch.Agent, err)
		return
	}
	if err := g.recordA2ATaskDispatch(dispatch, pr); err != nil {
		log.Printf("webhook: failed to record A2A task for task #%d dispatched to %s: %v", task.Number, dispatch.Agent, err)
	}

	log.Printf("webhook: dispatched task #%d to %s after merge of PR #%d on %s", dispatch.TaskNumber, dispatch.Agent, pr.Number, pr.Repo)
	g.sendDiscordWebhookNotification(
		"Task Dispatched",
		fmt.Sprintf("Dispatched `%s` on task #%d: %s", dispatch.Agent, dispatch.TaskNumber, dispatch.TaskTitle),
		5763719,
		[]discordField{
			{Name: "Task Repo", Value: g.taskRepo, Inline: true},
			{Name: "PR", Value: pr.HTMLURL, Inline: false},
		},
	)
}
