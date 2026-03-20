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
	"strings"
	"time"
)

const (
	webhookReadyLabelID      = 3
	webhookInProgressLabelID = 4
	webhookEndpointURL       = "http://gateway.valhalla.svc:8080/api/v1/webhooks/gitea"
	freyaAgentURL            = "http://freya.valhalla.svc:8081"
)

var webhookTaskRefRE = regexp.MustCompile(`(?i)(?:closes?\s+)?kit/hirdforge-tasks#(\d+)`)

type webhookPR struct {
	Number  int64
	Title   string
	Body    string
	Repo    string
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
	if strings.TrimSpace(r.Header.Get("X-Gitea-Event")) != "pull_request" {
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
		Head:    strings.TrimSpace(payload.PullRequest.Head.Ref),
		HTMLURL: strings.TrimSpace(payload.PullRequest.HTMLURL),
		User:    strings.TrimSpace(payload.PullRequest.User.Login),
		Merged:  payload.PullRequest.Merged,
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok"})

	switch {
	case strings.TrimSpace(payload.Action) == "opened":
		go g.handleAgentPROpened(pr)
	case strings.TrimSpace(payload.Action) == "closed" && pr.Merged:
		go g.handlePRMerged(pr)
	}
}

func (g *gateway) handleAgentPROpened(pr webhookPR) {
	log.Printf("webhook: agent PR opened repo=%s number=%d author=%s", pr.Repo, pr.Number, pr.User)
	if strings.HasPrefix(pr.Head, "ci/") {
		log.Printf("webhook: review skipped for PR #%d on %s: ci branch %s", pr.Number, pr.Repo, pr.Head)
		return
	}
	if pr.User == "warband" && strings.HasPrefix(pr.Head, "freya/") {
		log.Printf("webhook: review skipped for PR #%d on %s: self-review blocked for %s", pr.Number, pr.Repo, pr.Head)
		return
	}
	reviewMsg := fmt.Sprintf(
		"Review PR #%d on %s: %q. Read the diff using gitea tool with action list-pr-files on repo %s pr %d. Assess code quality, correctness, and style. Then post a review comment on the PR using gitea tool with action create-comment on repo %s issue %d with your assessment. Be concise - 3-5 sentences max.",
		pr.Number,
		pr.Repo,
		pr.Title,
		pr.Repo,
		pr.Number,
		pr.Repo,
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

func (g *gateway) handlePRMerged(pr webhookPR) {
	if pr.Repo == "kit/valhalla-infra" && strings.HasPrefix(pr.Head, "ci/") {
		log.Printf("webhook: merged PR #%d on %s ignored (ci branch %s)", pr.Number, pr.Repo, pr.Head)
		return
	}
	log.Printf("webhook: merged PR repo=%s number=%d", pr.Repo, pr.Number)
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
			"events": []string{"pull_request"},
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
