package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	webhookTaskRefRE = regexp.MustCompile(`(?i)(?:closes?\s+)?kit/hirdforge-tasks#(\d+)`)
	_                = webhookPRReview{}
)

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

func (g *gateway) recordA2ATaskDispatch(task dispatchedTask, pr webhookPR) error {
	if g.a2aStore == nil {
		return nil
	}
	now := time.Now().UTC()
	return g.a2aStore.CreateTask(&Task{
		ID:        fmt.Sprintf("task-%d-%d-%s", task.TaskNumber, now.Unix(), sanitizeWebhookToken(task.Agent)),
		ContextID: fmt.Sprintf("task-dispatch-%d", task.TaskNumber),
		Status: TaskStatus{
			State:     TaskStateSubmitted,
			Timestamp: now.Format(time.RFC3339),
		},
		Metadata: map[string]interface{}{
			"type":              "task_dispatch",
			"task_number":       task.TaskNumber,
			"task_title":        task.TaskTitle,
			"task_repo":         g.taskRepo,
			"trigger_pr_repo":   pr.Repo,
			"trigger_pr_number": pr.Number,
			"trigger_pr_title":  pr.Title,
		},
		Agent: task.Agent,
	})
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

func mustJSON(v interface{}) []byte {
	body, _ := json.Marshal(v)
	return body
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
	g.addDelegationTimelineEvent(sessionID, delegationTimelineEvent{
		Type:      "webhook_dispatch",
		Agent:     agentName,
		Timestamp: time.Now().Format(time.RFC3339),
		Metadata: map[string]interface{}{
			"session_id":  sessionID,
			"message":     fmt.Sprintf("Dispatched task #%d: %s", task.Number, task.Title),
			"task_number": task.Number,
			"task_title":  task.Title,
		},
	})
	return nil
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
				issue.Labels = appendIfMissing(issue.Labels, name)
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
