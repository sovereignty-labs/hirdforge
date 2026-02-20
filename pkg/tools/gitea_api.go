package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type GiteaAPITool struct {
	GiteaURL string
	Token    string
	Client   *http.Client
}

func NewGiteaAPITool(giteaURL, token string) *GiteaAPITool {
	return &GiteaAPITool{
		GiteaURL: strings.TrimRight(giteaURL, "/"),
		Token:    token,
		Client:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (t *GiteaAPITool) Name() string { return "gitea" }

func (t *GiteaAPITool) Description() string {
	return "Interact with Gitea API. Actions: create-issue, comment, list-issues, list-branches, close-issue, create-pr"
}

func (t *GiteaAPITool) Parameters() map[string]string {
	return map[string]string{
		"action": "One of: create-issue, comment, list-issues, list-branches, close-issue, create-pr",
		"repo":   "Repository name (e.g. project_valhalla) or owner/repo",
		"title":  "Title for issue or PR (create-issue, create-pr)",
		"body":   "Body text for issue, comment, or PR description",
		"issue":  "Issue or PR number (comment, close-issue)",
		"head":   "Source branch for PR (create-pr)",
		"base":   "Target branch for PR (create-pr, defaults to main)",
	}
}

func (t *GiteaAPITool) Execute(args map[string]interface{}) ToolResult {
	action, _ := args["action"].(string)
	repo, _ := args["repo"].(string)

	if action == "" {
		return ToolResult{Error: "action is required (create-issue, comment, list-issues, list-branches, close-issue, create-pr)"}
	}
	if repo == "" {
		return ToolResult{Error: "repo is required"}
	}

	owner, name := t.parseRepo(repo)

	switch action {
	case "create-issue":
		return t.createIssue(owner, name, args)
	case "comment":
		return t.comment(owner, name, args)
	case "list-issues":
		return t.listIssues(owner, name)
	case "list-branches":
		return t.listBranches(owner, name)
	case "close-issue":
		return t.closeIssue(owner, name, args)
	case "create-pr":
		return t.createPR(owner, name, args)
	default:
		return ToolResult{Error: fmt.Sprintf("unknown action: %s", action)}
	}
}

func (t *GiteaAPITool) parseRepo(repo string) (string, string) {
	if strings.Contains(repo, "/") {
		parts := strings.SplitN(repo, "/", 2)
		return parts[0], parts[1]
	}
	return "gitea_admin", repo
}

func (t *GiteaAPITool) apiRequest(method, path string, body interface{}) ([]byte, int, error) {
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
	if t.Token != "" {
		req.Header.Set("Authorization", "token "+t.Token)
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
	if title == "" {
		return ToolResult{Error: "title is required for create-issue"}
	}

	payload := map[string]string{"title": title, "body": body}
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

func (t *GiteaAPITool) comment(owner, repo string, args map[string]interface{}) ToolResult {
	body, _ := args["body"].(string)
	if body == "" {
		return ToolResult{Error: "body is required for comment"}
	}

	issueNum := t.extractNumber(args["issue"])
	if issueNum == 0 {
		return ToolResult{Error: "issue number is required for comment"}
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

func (t *GiteaAPITool) listIssues(owner, repo string) ToolResult {
	resp, status, err := t.apiRequest("GET", fmt.Sprintf("/repos/%s/%s/issues?state=open&limit=20&type=issues", owner, repo), nil)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}

	var issues []map[string]interface{}
	json.Unmarshal(resp, &issues)

	if len(issues) == 0 {
		return ToolResult{Output: "no open issues"}
	}

	var sb strings.Builder
	for _, issue := range issues {
		num, _ := issue["number"].(float64)
		title, _ := issue["title"].(string)
		labels := ""
		if ls, ok := issue["labels"].([]interface{}); ok && len(ls) > 0 {
			names := make([]string, 0, len(ls))
			for _, l := range ls {
				if lm, ok := l.(map[string]interface{}); ok {
					if n, ok := lm["name"].(string); ok {
						names = append(names, n)
					}
				}
			}
			labels = " [" + strings.Join(names, ", ") + "]"
		}
		sb.WriteString(fmt.Sprintf("#%d %s%s\n", int(num), title, labels))
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
	issueNum := t.extractNumber(args["issue"])
	if issueNum == 0 {
		return ToolResult{Error: "issue number is required for close-issue"}
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

func (t *GiteaAPITool) createPR(owner, repo string, args map[string]interface{}) ToolResult {
	title, _ := args["title"].(string)
	body, _ := args["body"].(string)
	head, _ := args["head"].(string)
	base, _ := args["base"].(string)

	if title == "" {
		return ToolResult{Error: "title is required for create-pr"}
	}
	if head == "" {
		return ToolResult{Error: "head (source branch) is required for create-pr"}
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
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}

	var result map[string]interface{}
	json.Unmarshal(resp, &result)
	num, _ := result["number"].(float64)
	url, _ := result["html_url"].(string)
	return ToolResult{Output: fmt.Sprintf("created PR #%d: %s\n%s", int(num), title, url)}
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
