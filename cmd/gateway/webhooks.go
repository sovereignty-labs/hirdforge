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
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	webhookReadyLabelID      = 3
	webhookInProgressLabelID = 4
	webhookEndpointURL       = "http://gateway.valhalla.svc:8080/api/v1/webhooks/gitea"
	freyaAgentURL            = "http://freya.valhalla.svc:8081"
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
	PeerReviewed bool
	PeerAgent    string
	Author       string
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
				Number int64 `json:"number"`
				State  string `json:"state"`
				Body   string `json:"body"`
				User   struct {
					Login string `json:"login"`
				} `json:"user"`
			} `json:"review"`
			PullRequest struct {
				Number  int64 `json:"number"`
				Title   string `json:"title"`
				Head    struct {
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
	if strings.EqualFold(pr.Base, "develop") {
		g.dispatchPeerReview(pr)
		return
	}
	if strings.HasPrefix(pr.Head, "ci/") {
		log.Printf("webhook: review skipped for PR #%d on %s: ci branch %s", pr.Number, pr.Repo, pr.Head)
		return
	}
	if pr.User == "warband" && strings.HasPrefix(pr.Head, "freya/") {
		log.Printf("webhook: review skipped for PR #%d on %s: self-review blocked for %s", pr.Number, pr.Repo, pr.Head)
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
	sessionID := fmt.Sprintf("webhook-review-%s-%d", sanitizeWebhookToken(pr.Repo), pr.Number)
	payload, err := json.Marshal(map[string]string{
		"content":    reviewMsg,
		"session_id": sessionID,
	})
	if err != nil {
		log.Printf("webhook: build review request failed for PR #%d on %s: %v", pr.Number, pr.Repo, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(freyaAgentURL, "/")+"/message", bytes.NewReader(payload))
	if err != nil {
		log.Printf("webhook: create reviewer request failed for PR #%d on %s: %v", pr.Number, pr.Repo, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	agentClient := &http.Client{}
	agentResp, err := agentClient.Do(req)
	if err != nil {
		log.Printf("webhook: review dispatch failed for PR #%d on %s via freya: %v", pr.Number, pr.Repo, err)
		return
	}
	reviewText, err := readAgentSSEContent(agentResp)
	if err != nil {
		log.Printf("webhook: review read failed for PR #%d on %s via freya: %v", pr.Number, pr.Repo, err)
		return
	}
	reviewText = strings.TrimSpace(thinkTagRE.ReplaceAllString(reviewText, ""))
	if reviewText == "" {
		log.Printf("webhook: review completed for PR #%d on %s via freya with empty response", pr.Number, pr.Repo)
		g.addEvent("webhook_review", "freya", fmt.Sprintf("Queued review for PR #%d on %s", pr.Number, pr.Repo))
		return
	}
	log.Printf("webhook: review completed for PR #%d on %s via freya: %s", pr.Number, pr.Repo, reviewText)
	g.addEvent("webhook_review", "freya", fmt.Sprintf("Reviewed PR #%d on %s", pr.Number, pr.Repo))
}

func (g *gateway) handleAgentPRSynchronized(pr webhookPR) {
	if !strings.EqualFold(pr.Base, "develop") {
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
	if strings.EqualFold(pr.Base, "develop") {
		g.syncDevelopToMain(pr.Repo)
	}
	if strings.EqualFold(pr.Base, "main") && (pr.Repo == "gitea_admin/project_valhalla" || pr.Repo == "kit/hirdforge-personas") {
		g.syncMainToDevelop(pr.Repo)
	}
	if matches := webhookTaskRefRE.FindStringSubmatch(pr.Body); len(matches) == 2 {
		log.Printf("webhook: merged PR #%d references task kit/hirdforge-tasks#%s", pr.Number, matches[1])
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
	selectedAgent, reason := g.selectAgentForTask(task)
	if selectedAgent == "" {
		selectedAgent = labelAgent
		reason = "bifrost: no candidates, using label fallback"
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

	taskReqBody, err := json.Marshal(map[string]string{
		"content":    wrapTaskForDispatch(task.Body, task.Number),
		"session_id": fmt.Sprintf("webhook-task-%d", task.Number),
	})
	if err != nil {
		log.Printf("webhook: failed marshaling task dispatch for #%d: %v", task.Number, err)
		return
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(agent.URL, "/")+"/message", bytes.NewReader(taskReqBody))
	if err != nil {
		log.Printf("webhook: failed creating dispatch request for task #%d: %v", task.Number, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	agentClient := &http.Client{Timeout: 5 * time.Second}
	resp, err := agentClient.Do(req)
	if err != nil {
		log.Printf("webhook: dispatch failed for task #%d to %s: %v", task.Number, targetAgent, err)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("webhook: dispatch returned %d for task #%d to %s", resp.StatusCode, task.Number, targetAgent)
		return
	}

	log.Printf("webhook: dispatched task #%d to %s after merge of PR #%d on %s", task.Number, targetAgent, pr.Number, pr.Repo)
	g.addEvent("webhook_dispatch", targetAgent, fmt.Sprintf("Dispatched task #%d: %s", task.Number, task.Title))
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

func (g *gateway) syncMainToDevelop(fullRepo string) {
	g.syncMu.Lock()
	defer g.syncMu.Unlock()

	owner, repoName, ok := splitFullRepoName(fullRepo)
	if !ok {
		log.Printf("webhook: main->develop sync skipped: invalid repo %q", fullRepo)
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}

	// Check for existing open sync PRs to avoid duplicates
	checkPath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls?state=open&base=develop&head=main&limit=10", url.PathEscape(owner), url.PathEscape(repoName))
	var pulls []map[string]interface{}
	status, body, err := giteaGetJSONWithStatus(client, g.giteaURL, g.giteaToken, checkPath, &pulls)
	if err != nil {
		log.Printf("webhook: main->develop PR existence check failed for %s: %v", fullRepo, err)
		return
	}
	if status < 200 || status >= 300 {
		log.Printf("webhook: main->develop PR existence check returned %d for %s: %s", status, fullRepo, strings.TrimSpace(string(body)))
		return
	}
	if len(pulls) > 0 {
		log.Printf("webhook: main->develop sync PR already exists, skipping")
		return
	}

	createPayload := map[string]string{
		"title": "sync: main \u2192 develop",
		"body":  "Automated sync PR to keep develop up to date with main after merge.",
		"head":  "main",
		"base":  "develop",
	}
	createResp, err := giteaRequest(client, http.MethodPost, g.giteaURL, g.giteaToken, fmt.Sprintf("/api/v1/repos/%s/%s/pulls", url.PathEscape(owner), url.PathEscape(repoName)), bytes.NewReader(mustJSON(createPayload)))
	if err != nil {
		log.Printf("webhook: main->develop sync PR create failed for %s: %v", fullRepo, err)
		return
	}
	createBody, _ := io.ReadAll(io.LimitReader(createResp.Body, 4096))
	createResp.Body.Close()
	if createResp.StatusCode < 200 || createResp.StatusCode >= 300 {
		if createResp.StatusCode == http.StatusConflict {
			log.Printf("webhook: main->develop sync PR not created for %s (likely already up to date): %s", fullRepo, strings.TrimSpace(string(createBody)))
			return
		}
		log.Printf("webhook: main->develop sync PR create returned %d for %s: %s", createResp.StatusCode, fullRepo, strings.TrimSpace(string(createBody)))
		return
	}
	var created map[string]interface{}
	if err := json.Unmarshal(createBody, &created); err != nil {
		log.Printf("webhook: main->develop sync PR decode failed for %s: %v", fullRepo, err)
		return
	}
	prNum, _ := created["number"].(float64)
	if prNum == 0 {
		log.Printf("webhook: main->develop sync PR create for %s returned no PR number", fullRepo)
		return
	}
	reviewersToken := strings.TrimSpace(os.Getenv("GITEA_REVIEWERS_TOKEN"))
	if reviewersToken == "" {
		log.Printf("webhook: main->develop sync approval skipped for %s PR #%d: missing GITEA_REVIEWERS_TOKEN", fullRepo, int64(prNum))
		return
	}
	approvalPayload := map[string]string{
		"event": "APPROVED",
		"body":  "Auto-approved sync PR",
	}
	approvalResp, err := giteaRequest(client, http.MethodPost, g.giteaURL, reviewersToken, fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/reviews", url.PathEscape(owner), url.PathEscape(repoName), int64(prNum)), bytes.NewReader(mustJSON(approvalPayload)))
	if err != nil {
		log.Printf("webhook: main->develop sync approval failed for %s PR #%d: %v", fullRepo, int64(prNum), err)
		return
	}
	approvalBody, _ := io.ReadAll(io.LimitReader(approvalResp.Body, 4096))
	approvalResp.Body.Close()
	if approvalResp.StatusCode < 200 || approvalResp.StatusCode >= 300 {
		log.Printf("webhook: main->develop sync approval returned %d for %s PR #%d: %s", approvalResp.StatusCode, fullRepo, int64(prNum), strings.TrimSpace(string(approvalBody)))
		return
	}
	mergePayload := map[string]string{"Do": "merge"}
	mergeResp, err := giteaRequest(client, http.MethodPost, g.giteaURL, g.giteaToken, fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/merge", url.PathEscape(owner), url.PathEscape(repoName), int64(prNum)), bytes.NewReader(mustJSON(mergePayload)))
	if err != nil {
		log.Printf("webhook: main->develop sync merge failed for %s PR #%d: %v", fullRepo, int64(prNum), err)
		return
	}
	defer mergeResp.Body.Close()
	mergeBody, _ := io.ReadAll(io.LimitReader(mergeResp.Body, 4096))
	if mergeResp.StatusCode < 200 || mergeResp.StatusCode >= 300 {
		log.Printf("webhook: main->develop sync merge returned %d for %s PR #%d: %s", mergeResp.StatusCode, fullRepo, int64(prNum), strings.TrimSpace(string(mergeBody)))
		return
	}
	log.Printf("webhook: synced main back to develop for %s via PR #%d", fullRepo, int64(prNum))
}

func (g *gateway) syncDevelopToMain(fullRepo string) {
	g.syncMu.Lock()
	defer g.syncMu.Unlock()

	owner, repoName, ok := splitFullRepoName(fullRepo)
	if !ok {
		log.Printf("webhook: develop->main sync skipped: invalid repo %q", fullRepo)
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	checkPath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls?state=open&base=main&head=develop&limit=10", url.PathEscape(owner), url.PathEscape(repoName))
	var pulls []map[string]interface{}
	status, body, err := giteaGetJSONWithStatus(client, g.giteaURL, g.giteaToken, checkPath, &pulls)
	if err != nil {
		log.Printf("webhook: develop->main PR existence check failed for %s: %v", fullRepo, err)
		return
	}
	if status < 200 || status >= 300 {
		log.Printf("webhook: develop->main PR existence check returned %d for %s: %s", status, fullRepo, strings.TrimSpace(string(body)))
		return
	}
	if len(pulls) > 0 {
		log.Printf("webhook: develop->main PR already exists, skipping")
		return
	}

	createPayload := map[string]string{
		"title": "develop → main",
		"body":  "Batched develop changes ready for Sovereign merge.",
		"head":  "develop",
		"base":  "main",
	}
	createResp, err := giteaRequest(client, http.MethodPost, g.giteaURL, g.giteaToken, fmt.Sprintf("/api/v1/repos/%s/%s/pulls", url.PathEscape(owner), url.PathEscape(repoName)), bytes.NewReader(mustJSON(createPayload)))
	if err != nil {
		log.Printf("webhook: develop->main PR create failed for %s: %v", fullRepo, err)
		return
	}
	createBody, _ := io.ReadAll(io.LimitReader(createResp.Body, 4096))
	createResp.Body.Close()
	if createResp.StatusCode < 200 || createResp.StatusCode >= 300 {
		if createResp.StatusCode == http.StatusConflict {
			log.Printf("webhook: develop->main PR already exists, skipping")
			return
		}
		log.Printf("webhook: develop->main PR create returned %d for %s: %s", createResp.StatusCode, fullRepo, strings.TrimSpace(string(createBody)))
		return
	}
	log.Printf("webhook: Auto-created develop→main PR")
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

func (g *gateway) savePipelineState() {
	if g.k8s == nil || !g.k8s.enabled {
		return
	}
	stateJSON, err := json.Marshal(g.snapshotPRReviewState())
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
	loaded := map[string]prReviewState{}
	if raw := strings.TrimSpace(configMap.Data["state"]); raw != "" {
		if err := json.Unmarshal([]byte(raw), &loaded); err != nil {
			return err
		}
	}
	g.prReviewMu.Lock()
	g.prReviewState = loaded
	g.prReviewMu.Unlock()
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

func (g *gateway) selectPeerReviewer(author string) (*Agent, bool) {
	author = strings.TrimSpace(author)
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, name := range g.order {
		if name == "" || name == author || name == "freya" || name == "ragnar" {
			continue
		}
		agent, ok := g.agents[name]
		if !ok || agent == nil || !agent.Healthy {
			continue
		}
		cp := *agent
		return &cp, true
	}
	return nil, false
}

func (g *gateway) dispatchReviewToAgent(agentName string, agentURL string, pr webhookPR, prompt string, eventType string) error {
	sessionID := fmt.Sprintf("webhook-review-%s-%d-%s", sanitizeWebhookToken(pr.Repo), pr.Number, sanitizeWebhookToken(agentName))
	payload, err := json.Marshal(map[string]string{
		"content":    prompt,
		"session_id": sessionID,
	})
	if err != nil {
		return fmt.Errorf("marshal review request: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(agentURL, "/")+"/message", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create review request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("dispatch review request: %w", err)
	}
	reviewText, err := readAgentSSEContent(resp)
	if err != nil {
		return fmt.Errorf("read review response: %w", err)
	}
	reviewText = strings.TrimSpace(thinkTagRE.ReplaceAllString(reviewText, ""))
	log.Printf("webhook: %s completed for PR #%d on %s by %s: %s", eventType, pr.Number, pr.Repo, agentName, reviewText)
	g.addEvent(eventType, agentName, fmt.Sprintf("Reviewed PR #%d on %s", pr.Number, pr.Repo))
	return nil
}

func (g *gateway) dispatchPeerReview(pr webhookPR) {
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
	g.setPRReviewState(key, prReviewState{
		PeerAgent: peer.Name,
		Author:    stateAuthor,
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
	if err := g.dispatchReviewToAgent(peer.Name, peer.URL, pr, prompt, "webhook_peer_review"); err != nil {
		g.clearPRReviewState(key)
		log.Printf("webhook: peer review dispatch failed for PR #%d on %s via %s: %v", pr.Number, pr.Repo, peer.Name, err)
		return
	}
	log.Printf("webhook: queued peer review for PR #%d on %s via %s", pr.Number, pr.Repo, peer.Name)
}

func (g *gateway) dispatchFreyaApprovalReview(pr webhookPR) {
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
	if err := g.dispatchReviewToAgent("freya", freyaAgentURL, pr, prompt, "webhook_freya_review"); err != nil {
		log.Printf("webhook: freya review dispatch failed for PR #%d on %s: %v", pr.Number, pr.Repo, err)
	}
}

func (g *gateway) handlePRReviewApproved(review webhookPRReview) {
	key := g.prReviewKey(review.Repo, review.PRNumber)
	state, ok := g.getPRReviewState(key)
	if !ok {
		log.Printf("webhook: approved review ignored for PR #%d on %s: no pipeline state", review.PRNumber, review.Repo)
		return
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
		log.Printf("webhook: peer review approved for PR #%d on %s by %s; dispatching freya", review.PRNumber, review.Repo, review.Reviewer)
		g.dispatchFreyaApprovalReview(pr)
		return
	}

	if review.Reviewer != "warband_review" {
		log.Printf("webhook: second approval ignored for PR #%d on %s: expected freya approval via warband_review, got %s", review.PRNumber, review.Repo, review.Reviewer)
		return
	}

	owner, repoName, ok := splitFullRepoName(review.Repo)
	if !ok {
		log.Printf("webhook: merge skipped for PR #%d: invalid repo %q", review.PRNumber, review.Repo)
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	updatePath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/update", url.PathEscape(owner), url.PathEscape(repoName), review.PRNumber)
	updateResp, err := giteaRequest(client, http.MethodPost, g.giteaURL, g.giteaToken, updatePath, bytes.NewReader(mustJSON(map[string]string{})))
	if err != nil {
		log.Printf("webhook: update-branch failed for PR #%d on %s: %v", review.PRNumber, review.Repo, err)
	} else {
		updateBody, _ := io.ReadAll(io.LimitReader(updateResp.Body, 2048))
		updateResp.Body.Close()
		updateMsg := strings.TrimSpace(string(updateBody))
		switch {
		case updateResp.StatusCode >= 200 && updateResp.StatusCode < 300:
		case updateResp.StatusCode == http.StatusConflict && strings.Contains(strings.ToLower(updateMsg), "already up to date"):
		case updateResp.StatusCode == http.StatusConflict:
			log.Printf("webhook: update-branch conflict for PR #%d on %s: %s", review.PRNumber, review.Repo, updateMsg)
			return
		default:
			log.Printf("webhook: update-branch returned %d for PR #%d on %s: %s", updateResp.StatusCode, review.PRNumber, review.Repo, updateMsg)
		}
	}
	payload := map[string]string{"Do": "merge"}
	resp, err := giteaRequest(client, http.MethodPost, g.giteaURL, g.giteaToken, fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/merge", url.PathEscape(owner), url.PathEscape(repoName), review.PRNumber), bytes.NewReader(mustJSON(payload)))
	if err != nil {
		log.Printf("webhook: merge failed for PR #%d on %s: %v", review.PRNumber, review.Repo, err)
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("webhook: merge returned %d for PR #%d on %s: %s", resp.StatusCode, review.PRNumber, review.Repo, strings.TrimSpace(string(respBody)))
		return
	}
	g.clearPRReviewState(key)
	log.Printf("webhook: merged PR #%d on %s after peer and freya approvals", review.PRNumber, review.Repo)
	g.addEvent("webhook_pr_merged", "freya", fmt.Sprintf("Merged PR #%d on %s", review.PRNumber, review.Repo))
}

func mustJSON(v interface{}) []byte {
	body, _ := json.Marshal(v)
	return body
}

func (g *gateway) handleReviewChangesRequested(review webhookPRReview) {
	builder := g.builderFromBranch(review.PRHead)
	if builder == "" {
		log.Printf("webhook: review feedback skipped - unable to detect builder from branch %s", review.PRHead)
		return
	}
	agent, ok := g.getAgent(builder)
	if !ok || !agent.Healthy {
		log.Printf("webhook: review feedback skipped - builder agent %s not found or unhealthy", builder)
		return
	}
	key := g.prReviewKey(review.Repo, review.PRNumber)
	phase := "peer"
	if state, ok := g.getPRReviewState(key); ok {
		if state.PeerReviewed {
			phase = "freya"
		}
		g.clearPRReviewState(key)
	} else {
		phase = "unknown"
	}
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
	repos := []string{
		"gitea_admin/project_valhalla",
		"kit/valhalla-infra",
		"kit/hirdforge-personas",
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
	for _, candidate := range []string{"val", "leif", "chuck", "freya"} {
		if candidate == strings.TrimSpace(preferred) {
			continue
		}
		agent, ok := g.getAgent(candidate)
		if ok && agent.Healthy {
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
