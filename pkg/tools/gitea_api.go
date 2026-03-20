package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type GiteaAPITool struct {
	GiteaURL        string
	Token           string
	ReviewersToken  string
	Client          *http.Client
}

func NewGiteaAPITool(giteaURL, token string) *GiteaAPITool {
	return &GiteaAPITool{
		GiteaURL:       strings.TrimRight(giteaURL, "/"),
		Token:          resolveGiteaToken(token),
		ReviewersToken: resolveGiteaReviewersToken(""),
		Client:         &http.Client{Timeout: 30 * time.Second},
	}
}

func (t *GiteaAPITool) Name() string { return "gitea" }

func (t *GiteaAPITool) Description() string {
	return "Interact with Gitea API. Actions: create-issue, comment, list-issues, get-issue, list-branches, close-issue, create-pr, create-review, merge-pr, update-labels"
}

func (t *GiteaAPITool) Parameters() map[string]string {
	return map[string]string{
		"action": "One of: create-issue, comment, list-issues, get-issue, list-branches, close-issue, create-pr, create-review, merge-pr, update-labels",
		"owner":  "Repository owner for get-issue (optional if repo is owner/repo)",
		"repo":   "Repository name (e.g. project_valhalla) or owner/repo",
		"title":  "Title for issue or PR (create-issue, create-pr)",
		"body":   "Body text for issue, comment, PR description, or review text (create-review)",
		"labels": "Comma-separated label names (create-issue, list-issues, update-labels)",
		"state":  "Issue state for list-issues: open or closed (default open), or review state for create-review: APPROVED or REQUEST_CHANGES",
		"issue":  "Issue or PR number (comment, close-issue, update-labels)",
		"index":  "Issue or PR number for get-issue, update-labels, create-review, or merge-pr",
		"head":   "Source branch for PR (create-pr)",
		"base":   "Target branch for PR (create-pr, defaults to main)",
	}
}

func (t *GiteaAPITool) Execute(args map[string]interface{}) ToolResult {
	action, _ := args["action"].(string)
	repo, _ := args["repo"].(string)
	action = strings.TrimSpace(action)

	if action == "" {
		return ToolResult{Error: t.giteaOverviewUsage()}
	}

	if action == "get-issue" {
		owner, _ := args["owner"].(string)
		owner = strings.TrimSpace(owner)
		name := strings.TrimSpace(repo)
		if owner == "" {
			owner, name = t.parseRepo(name)
		}
		if name == "" {
			return ToolResult{Error: t.actionUsage("get-issue")}
		}
		return t.getIssue(owner, name, args)
	}
	if repo == "" {
		return ToolResult{Error: t.actionUsage(action)}
	}

	owner, name := t.parseRepo(repo)

	switch action {
	case "create-issue":
		return t.createIssue(owner, name, args)
	case "comment":
		return t.comment(owner, name, args)
	case "list-issues":
		return t.listIssues(owner, name, args)
	case "list-branches":
		return t.listBranches(owner, name)
	case "close-issue":
		return t.closeIssue(owner, name, args)
	case "create-pr":
		return t.createPR(owner, name, args)
	case "create-review":
		return t.createReview(owner, name, args)
	case "merge-pr":
		return t.mergePR(owner, name, args)
	case "update-labels":
		return t.updateLabels(owner, name, args)
	default:
		return ToolResult{Error: fmt.Sprintf("Unknown gitea action: %q. Available actions: create-pr, create-review, merge-pr, create-issue, list-issues, close-issue, comment, get-issue, update-labels.", action)}
	}
}

func (t *GiteaAPITool) parseRepo(repo string) (string, string) {
	if strings.Contains(repo, "/") {
		parts := strings.SplitN(repo, "/", 2)
		return parts[0], parts[1]
	}
	owner := os.Getenv("GITEA_DEFAULT_OWNER")
	if owner == "" {
		owner = "gitea_admin"
	}
	return owner, repo
}

func (t *GiteaAPITool) apiRequest(method, path string, body interface{}) ([]byte, int, error) {
	return t.apiRequestWithToken(method, path, body, t.Token)
}

func (t *GiteaAPITool) apiRequestWithToken(method, path string, body interface{}, token string) ([]byte, int, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, t.GiteaURL+"/api/v1"+path, reqBody)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token == "" {
		token = resolveGiteaToken("")
	}
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}

	resp, err := t.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return respBody, resp.StatusCode, nil
}

func (t *GiteaAPITool) createIssue(owner, repo string, args map[string]interface{}) ToolResult {
	title, _ := args["title"].(string)
	body, _ := args["body"].(string)
	labelsCSV, _ := args["labels"].(string)
	if title == "" {
		if v, ok := args["title"]; ok {
			title = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	if body == "" {
		if v, ok := args["body"]; ok {
			body = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	if labelsCSV == "" {
		if v, ok := args["labels"]; ok {
			labelsCSV = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	if title == "" {
		return ToolResult{Error: t.actionUsage("create-issue")}
	}

	labelIDs, err := t.resolveLabelIDs(owner, repo, labelsCSV)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	payload := map[string]interface{}{"title": title, "body": body}
	if len(labelIDs) > 0 {
		payload["labels"] = labelIDs
	}
	resp, status, err := t.apiRequest("POST", fmt.Sprintf("/repos/%s/%s/issues", owner, repo), payload)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}

	var result map[string]interface{}
	json.Unmarshal(resp, &result)
	num, _ := result["number"].(float64)
	url, _ := result["html_url"].(string)
	return ToolResult{Output: fmt.Sprintf("created issue #%d: %s\n%s", int(num), title, url)}
}

func (t *GiteaAPITool) resolveLabelIDs(owner, repo, labelsCSV string) ([]int, error) {
	labelsCSV = strings.TrimSpace(labelsCSV)
	if labelsCSV == "" {
		return nil, nil
	}
	resp, status, err := t.apiRequest("GET", fmt.Sprintf("/repos/%s/%s/labels", owner, repo), nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", status, string(resp))
	}
	var labels []map[string]interface{}
	if err := json.Unmarshal(resp, &labels); err != nil {
		return nil, err
	}
	labelIDByName := map[string]int{}
	for _, l := range labels {
		name, _ := l["name"].(string)
		if name == "" {
			continue
		}
		idf, _ := l["id"].(float64)
		labelIDByName[name] = int(idf)
	}
	names := strings.Split(labelsCSV, ",")
	out := make([]int, 0, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		id, ok := labelIDByName[name]
		if !ok {
			return nil, fmt.Errorf("label not found: %s", name)
		}
		out = append(out, id)
	}
	return out, nil
}

func (t *GiteaAPITool) comment(owner, repo string, args map[string]interface{}) ToolResult {
	body, _ := args["body"].(string)
	if body == "" {
		if v, ok := args["body"]; ok {
			body = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	if body == "" {
		return ToolResult{Error: t.actionUsage("comment")}
	}

	issueNum := t.extractNumber(args["index"])
	if issueNum == 0 {
		issueNum = t.extractNumber(args["issue"])
	}
	if issueNum == 0 {
		return ToolResult{Error: t.actionUsage("comment")}
	}

	payload := map[string]string{"body": body}
	resp, status, err := t.apiRequest("POST", fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, issueNum), payload)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}

	return ToolResult{Output: fmt.Sprintf("commented on issue #%d", issueNum)}
}

func (t *GiteaAPITool) listIssues(owner, repo string, args map[string]interface{}) ToolResult {
	state, _ := args["state"].(string)
	labelsCSV, _ := args["labels"].(string)
	state = strings.TrimSpace(strings.ToLower(state))
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "closed" && state != "all" {
		return ToolResult{Error: "state must be open, closed, or all"}
	}
	q := url.Values{}
	q.Set("state", state)
	if strings.TrimSpace(labelsCSV) != "" {
		q.Set("labels", strings.TrimSpace(labelsCSV))
	}
	path := fmt.Sprintf("/repos/%s/%s/issues?%s", owner, repo, q.Encode())
	resp, status, err := t.apiRequest("GET", path, nil)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}

	var issues []map[string]interface{}
	json.Unmarshal(resp, &issues)

	summary := make([]map[string]interface{}, 0, len(issues))
	for _, issue := range issues {
		num, _ := issue["number"].(float64)
		title, _ := issue["title"].(string)
		st, _ := issue["state"].(string)
		labelNames := []string{}
		if ls, ok := issue["labels"].([]interface{}); ok && len(ls) > 0 {
			for _, l := range ls {
				if lm, ok := l.(map[string]interface{}); ok {
					if n, ok := lm["name"].(string); ok {
						labelNames = append(labelNames, n)
					}
				}
			}
		}
		summary = append(summary, map[string]interface{}{
			"number": int(num),
			"title":  title,
			"labels": labelNames,
			"state":  st,
		})
	}
	out, err := json.Marshal(summary)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	return ToolResult{Output: string(out)}
}

func (t *GiteaAPITool) getIssue(owner, repo string, args map[string]interface{}) ToolResult {
	issueNum := t.extractNumber(args["index"])
	if issueNum == 0 {
		issueNum = t.extractNumber(args["issue"])
	}
	if issueNum == 0 {
		return ToolResult{Error: t.actionUsage("get-issue")}
	}

	issueResp, status, err := t.apiRequest("GET", fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, issueNum), nil)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(issueResp))}
	}

	commentsResp, status, err := t.apiRequest("GET", fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, issueNum), nil)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(commentsResp))}
	}

	var issue map[string]interface{}
	if err := json.Unmarshal(issueResp, &issue); err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to parse issue response: %v", err)}
	}
	var comments []map[string]interface{}
	if err := json.Unmarshal(commentsResp, &comments); err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to parse comments response: %v", err)}
	}

	title, _ := issue["title"].(string)
	state, _ := issue["state"].(string)
	body, _ := issue["body"].(string)

	labelNames := make([]string, 0)
	if ls, ok := issue["labels"].([]interface{}); ok {
		for _, l := range ls {
			if lm, ok := l.(map[string]interface{}); ok {
				if n, ok := lm["name"].(string); ok && n != "" {
					labelNames = append(labelNames, n)
				}
			}
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Title: %s\n", title))
	sb.WriteString(fmt.Sprintf("State: %s\n", state))
	if len(labelNames) > 0 {
		sb.WriteString(fmt.Sprintf("Labels: %s\n", strings.Join(labelNames, ", ")))
	} else {
		sb.WriteString("Labels: \n")
	}
	sb.WriteString("Body:\n")
	sb.WriteString(body)
	sb.WriteString("\n\nComments:\n")
	if len(comments) == 0 {
		sb.WriteString("(none)")
		return ToolResult{Output: sb.String()}
	}
	for i, c := range comments {
		author := "unknown"
		if user, ok := c["user"].(map[string]interface{}); ok {
			if login, ok := user["login"].(string); ok && login != "" {
				author = login
			}
		}
		commentBody, _ := c["body"].(string)
		sb.WriteString(fmt.Sprintf("%d. %s:\n%s\n", i+1, author, commentBody))
	}
	return ToolResult{Output: sb.String()}
}

func (t *GiteaAPITool) listBranches(owner, repo string) ToolResult {
	resp, status, err := t.apiRequest("GET", fmt.Sprintf("/repos/%s/%s/branches", owner, repo), nil)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}

	var branches []map[string]interface{}
	json.Unmarshal(resp, &branches)

	if len(branches) == 0 {
		return ToolResult{Output: "no branches found"}
	}

	var sb strings.Builder
	for _, branch := range branches {
		name, _ := branch["name"].(string)
		commit, _ := branch["commit"].(map[string]interface{})
		sha := ""
		if commit != nil {
			if id, ok := commit["id"].(string); ok && len(id) >= 7 {
				sha = id[:7]
			}
		}
		sb.WriteString(fmt.Sprintf("%s (%s)\n", name, sha))
	}
	return ToolResult{Output: sb.String()}
}

func (t *GiteaAPITool) closeIssue(owner, repo string, args map[string]interface{}) ToolResult {
	issueNum := t.extractNumber(args["index"])
	if issueNum == 0 {
		issueNum = t.extractNumber(args["issue"])
	}
	if issueNum == 0 {
		return ToolResult{Error: t.actionUsage("close-issue")}
	}

	payload := map[string]string{"state": "closed"}
	resp, status, err := t.apiRequest("PATCH", fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, issueNum), payload)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}

	return ToolResult{Output: fmt.Sprintf("closed issue #%d", issueNum)}
}

func (t *GiteaAPITool) updateLabels(owner, repo string, args map[string]interface{}) ToolResult {
	issueNum := t.extractNumber(args["index"])
	if issueNum == 0 {
		issueNum = t.extractNumber(args["issue"])
	}
	if issueNum == 0 {
		return ToolResult{Error: t.actionUsage("update-labels")}
	}
	labelsCSV, _ := args["labels"].(string)
	labelIDs, err := t.resolveLabelIDs(owner, repo, labelsCSV)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	payload := map[string]interface{}{"labels": labelIDs}
	resp, status, err := t.apiRequest("PUT", fmt.Sprintf("/repos/%s/%s/issues/%d/labels", owner, repo, issueNum), payload)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}
	return ToolResult{Output: fmt.Sprintf("updated labels on issue #%d to: %s", issueNum, labelsCSV)}
}

func (t *GiteaAPITool) createPR(owner, repo string, args map[string]interface{}) ToolResult {
	title, _ := args["title"].(string)
	body, _ := args["body"].(string)
	head, _ := args["head"].(string)
	base, _ := args["base"].(string)
	if title == "" {
		if v, ok := args["title"]; ok {
			title = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	if head == "" {
		if v, ok := args["head"]; ok {
			head = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	if body == "" {
		if v, ok := args["body"]; ok {
			body = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	if base == "" {
		if v, ok := args["base"]; ok {
			base = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	log.Printf("gitea: create-pr owner=%s repo=%s title=%q head=%q base=%q token_len=%d", owner, repo, title, head, base, len(t.Token))

	if title == "" {
		return ToolResult{Error: t.actionUsage("create-pr")}
	}
	if head == "" {
		return ToolResult{Error: t.actionUsage("create-pr")}
	}
	if base == "" {
		base = "main"
	}

	payload := map[string]string{
		"title": title,
		"body":  body,
		"head":  head,
		"base":  base,
	}
	resp, status, err := t.apiRequest("POST", fmt.Sprintf("/repos/%s/%s/pulls", owner, repo), payload)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status == 404 {
		return ToolResult{Error: fmt.Sprintf("HTTP 404: branch %q may not exist on remote, or token lacks permission. Push the branch first, then retry. Raw: %s", head, string(resp))}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}

	var result map[string]interface{}
	json.Unmarshal(resp, &result)
	num, _ := result["number"].(float64)
	url, _ := result["html_url"].(string)
	return ToolResult{Output: fmt.Sprintf("created PR #%d: %s\n%s", int(num), title, url)}
}

func (t *GiteaAPITool) createReview(owner, repo string, args map[string]interface{}) ToolResult {
	prNum := t.extractNumber(args["index"])
	if prNum == 0 {
		return ToolResult{Error: t.actionUsage("create-review")}
	}
	body, _ := args["body"].(string)
	if body == "" {
		if v, ok := args["body"]; ok {
			body = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	state, _ := args["state"].(string)
	if state == "" {
		if v, ok := args["state"]; ok {
			state = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	state = strings.ToUpper(strings.TrimSpace(state))
	if body == "" || (state != "APPROVED" && state != "REQUEST_CHANGES") {
		return ToolResult{Error: t.actionUsage("create-review")}
	}
	token := strings.TrimSpace(t.ReviewersToken)
	if token == "" {
		token = resolveGiteaReviewersToken("")
	}
	if token == "" {
		return ToolResult{Error: "missing Gitea reviewers token"}
	}

	payload := map[string]string{
		"body":  body,
		"event": state,
	}
	resp, status, err := t.apiRequestWithToken("POST", fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repo, prNum), payload, token)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}

	return ToolResult{Output: fmt.Sprintf("submitted %s review on PR #%d", state, prNum)}
}

func (t *GiteaAPITool) mergePR(owner, repo string, args map[string]interface{}) ToolResult {
	prNum := t.extractNumber(args["index"])
	if prNum == 0 {
		return ToolResult{Error: t.actionUsage("merge-pr")}
	}

	payload := map[string]string{"Do": "merge"}
	resp, status, err := t.apiRequest("POST", fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", owner, repo, prNum), payload)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}

	return ToolResult{Output: fmt.Sprintf("merged PR #%d", prNum)}
}

func (t *GiteaAPITool) ReplaceLabels(owner, repo string, issueNum int, labelNames []string) error {
	labelsCSV := strings.Join(labelNames, ",")
	labelIDs, err := t.resolveLabelIDs(owner, repo, labelsCSV)
	if err != nil {
		return err
	}
	payload := map[string]interface{}{"labels": labelIDs}
	resp, status, err := t.apiRequest("PUT", fmt.Sprintf("/repos/%s/%s/issues/%d/labels", owner, repo, issueNum), payload)
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("HTTP %d: %s", status, string(resp))
	}
	return nil
}

func (t *GiteaAPITool) PostComment(owner, repo string, issueNum int, body string) error {
	payload := map[string]string{"body": body}
	resp, status, err := t.apiRequest("POST", fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, issueNum), payload)
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("HTTP %d: %s", status, string(resp))
	}
	return nil
}

func (t *GiteaAPITool) CloseIssue(owner, repo string, issueNum int) error {
	payload := map[string]string{"state": "closed"}
	resp, status, err := t.apiRequest("PATCH", fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, issueNum), payload)
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("HTTP %d: %s", status, string(resp))
	}
	return nil
}

func (t *GiteaAPITool) GetIssueLabels(owner, repo string, issueNum int) ([]string, error) {
	resp, status, err := t.apiRequest("GET", fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, issueNum), nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", status, string(resp))
	}
	var issue map[string]interface{}
	if err := json.Unmarshal(resp, &issue); err != nil {
		return nil, err
	}
	var names []string
	if ls, ok := issue["labels"].([]interface{}); ok {
		for _, l := range ls {
			if lm, ok := l.(map[string]interface{}); ok {
				if name, ok := lm["name"].(string); ok && name != "" {
					names = append(names, name)
				}
			}
		}
	}
	return names, nil
}

func (t *GiteaAPITool) GetComments(owner, repo string, issueNum int) ([]map[string]interface{}, error) {
	resp, status, err := t.apiRequest("GET", fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, issueNum), nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", status, string(resp))
	}
	var comments []map[string]interface{}
	if err := json.Unmarshal(resp, &comments); err != nil {
		return nil, err
	}
	return comments, nil
}

func (t *GiteaAPITool) Verify(args map[string]interface{}, result ToolResult) error {
	if result.Error != "" {
		return nil
	}

	action, _ := args["action"].(string)
	if action != "create-pr" {
		return nil
	}

	repoArg, _ := args["repo"].(string)
	if repoArg == "" {
		return fmt.Errorf("gitea verification failed: repo is required")
	}
	title, _ := args["title"].(string)
	head, _ := args["head"].(string)
	base, _ := args["base"].(string)
	if base == "" {
		base = "main"
	}

	owner, repo := t.parseRepo(repoArg)
	resp, status, err := t.apiRequest("GET", fmt.Sprintf("/repos/%s/%s/pulls?state=open&limit=1&sort=newest", owner, repo), nil)
	if err != nil {
		return fmt.Errorf("gitea verification failed: %w", err)
	}
	if status >= 400 {
		return fmt.Errorf("gitea verification failed: HTTP %d: %s", status, string(resp))
	}

	var pulls []map[string]interface{}
	if err := json.Unmarshal(resp, &pulls); err != nil {
		return fmt.Errorf("gitea verification failed: decode response: %w", err)
	}
	if len(pulls) == 0 {
		return fmt.Errorf("gitea verification failed: no open pull requests found after create-pr")
	}

	pr := pulls[0]
	gotTitle, _ := pr["title"].(string)
	if title != "" && gotTitle != title {
		return fmt.Errorf("gitea verification failed: newest PR title mismatch: got %q", gotTitle)
	}
	if headRef, ok := pr["head"].(map[string]interface{}); ok {
		if gotHead, _ := headRef["ref"].(string); head != "" && gotHead != head {
			return fmt.Errorf("gitea verification failed: newest PR head mismatch: got %q", gotHead)
		}
	}
	if baseRef, ok := pr["base"].(map[string]interface{}); ok {
		if gotBase, _ := baseRef["ref"].(string); gotBase != base {
			return fmt.Errorf("gitea verification failed: newest PR base mismatch: got %q", gotBase)
		}
	}

	return nil
}

func (t *GiteaAPITool) extractNumber(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case string:
		var num int
		fmt.Sscanf(strings.TrimPrefix(n, "#"), "%d", &num)
		return num
	}
	return 0
}

func (t *GiteaAPITool) actionUsage(action string) string {
	switch action {
	case "create-pr":
		return `gitea create-pr — required params:
owner    Repository owner (e.g., kit, gitea_admin)
repo     Repository name
head     Source branch name
base     Target branch (usually: main)
title    PR title
body     (optional) PR description
Example: gitea create-pr owner=kit repo=hirdforge-personas head=feature/new-skill base=main title="feat: add new skill" body="Adds code-modification skill"`
	case "create-issue":
		return `gitea create-issue — required params:
owner    Repository owner
repo     Repository name
title    Issue title
body     Issue body (use body= prefix for multi-line content)
labels   (optional) Comma-separated label names (resolved to IDs automatically)
Example: gitea create-issue owner=kit repo=hirdforge-tasks title="Add health endpoint" body="TASK: Add /health to seidr..." labels="agent/leif,status/ready,priority/normal"`
	case "comment":
		return `gitea comment — required params:
owner    Repository owner
repo     Repository name
index    Issue or PR number
body     Comment text
Example: gitea comment owner=kit repo=hirdforge-tasks index=24 body="PR: http://..."`
	case "create-review":
		return `gitea create-review — required params:
owner    Repository owner
repo     Repository name
index    PR number
body     Review text
state    APPROVED or REQUEST_CHANGES
Example: gitea create-review owner=gitea_admin repo=project_valhalla index=250 state=APPROVED body="LGTM"`
	case "close-issue":
		return `gitea close-issue — required params:
owner    Repository owner
repo     Repository name
index    Issue number
Example: gitea close-issue owner=kit repo=hirdforge-tasks index=24`
	case "get-issue":
		return `gitea get-issue — required params:
owner    Repository owner
repo     Repository name
index    Issue number
Example: gitea get-issue owner=kit repo=hirdforge-tasks index=24`
	case "list-issues":
		return `gitea list-issues — required params:
owner    Repository owner
repo     Repository name
state    (optional) open, closed, all (default: open)
labels   (optional) Comma-separated label names to filter by
Example: gitea list-issues owner=kit repo=hirdforge-tasks state=open labels="agent/leif,status/ready"`
	case "merge-pr":
		return `gitea merge-pr — required params:
owner    Repository owner
repo     Repository name
index    PR number
Example: gitea merge-pr owner=gitea_admin repo=project_valhalla index=250`
	case "update-labels":
		return `gitea update-labels — required params:
owner    Repository owner
repo     Repository name
index    Issue number
labels   Comma-separated label names (replaces all labels on the issue)
Example: gitea update-labels owner=kit repo=hirdforge-tasks index=42 labels="status/done,agent/val,priority/normal,tier/autonomous"`
	default:
		return t.giteaOverviewUsage()
	}
}

func (t *GiteaAPITool) giteaOverviewUsage() string {
	return `Gitea tool — available actions:
create-pr      Create a pull request
create-review  Submit a formal PR review
merge-pr       Merge a pull request
create-issue   Create an issue (supports label names)
list-issues    List issues with state/label filters
close-issue    Close an issue
update-labels  Replace all labels on an issue
comment        Add comment to issue or PR
get-issue      Get full issue details including body
Usage: gitea {action} {params...}
Example: gitea create-pr owner=kit repo=hirdforge-tasks head=my-branch base=main title="My PR"`
}
