package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// GiteaAPITool is retained for internal use (DLQ, auto-close, etc.) and as
// the backend for the individual gitea action tools in gitea_split.go.
type GiteaAPITool struct {
	GiteaURL       string
	Token          string
	ReviewersToken string
	Client         *http.Client
}

func NewGiteaAPITool(giteaURL, token string) *GiteaAPITool {
	return &GiteaAPITool{
		GiteaURL:       strings.TrimRight(giteaURL, "/"),
		Token:          resolveGiteaToken(token),
		ReviewersToken: resolveGiteaReviewersToken(""),
		Client:         &http.Client{Timeout: 30 * time.Second},
	}
}

// parseRepo splits "owner/repo" or returns the default owner + repo.
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

// apiRequest performs an authenticated API request using the primary token.
func (t *GiteaAPITool) apiRequest(method, path string, body interface{}) ([]byte, int, error) {
	return t.apiRequestWithToken(method, path, body, t.Token)
}

// apiRequestWithToken performs an authenticated API request with a custom token.
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

// resolveLabelIDs resolves a comma-separated list of label names to IDs.
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

// --- Internal helpers used by main.go DLQ and auto-complete ---

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

// FindOpenPullRequest returns the matching open PR for the given head/base pair,
// or nil when no matching PR is found.
func (t *GiteaAPITool) FindOpenPullRequest(owner, repo, head, base string) (map[string]interface{}, error) {
	q := url.Values{}
	q.Set("state", "open")
	q.Set("head", fmt.Sprintf("%s:%s", owner, strings.TrimSpace(head)))
	if base = strings.TrimSpace(base); base != "" {
		q.Set("base", base)
	}
	q.Set("limit", "50")

	resp, status, err := t.apiRequest("GET", fmt.Sprintf("/repos/%s/%s/pulls?%s", owner, repo, q.Encode()), nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", status, string(resp))
	}

	var pulls []map[string]interface{}
	if err := json.Unmarshal(resp, &pulls); err != nil {
		return nil, err
	}

	head = strings.TrimSpace(head)
	base = strings.TrimSpace(base)
	for _, pr := range pulls {
		if pr == nil {
			continue
		}
		if head != "" {
			headRef, _ := pr["head"].(map[string]interface{})
			if headRef == nil {
				continue
			}
			if gotHead, _ := headRef["ref"].(string); strings.TrimSpace(gotHead) != head {
				continue
			}
		}
		if base != "" {
			baseRef, _ := pr["base"].(map[string]interface{})
			if baseRef == nil {
				continue
			}
			if gotBase, _ := baseRef["ref"].(string); strings.TrimSpace(gotBase) != base {
				continue
			}
		}
		return pr, nil
	}

	return nil, nil
}

// Verify performs post-execution verification for create-pr actions.
func (t *GiteaAPITool) Verify(args map[string]interface{}, result ToolResult) error {
	if result.Error != "" {
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

// GetIssue fetches a single issue (used internally by DLQ logic in main.go).
func (t *GiteaAPITool) GetIssue(owner, repo string, issueNum int) (map[string]interface{}, error) {
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
	return issue, nil
}

// ListBranches fetches all branches for a repo.
func (t *GiteaAPITool) ListBranches(owner, repo string) ([]string, error) {
	resp, status, err := t.apiRequest("GET", fmt.Sprintf("/repos/%s/%s/branches", owner, repo), nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", status, string(resp))
	}
	var branches []map[string]interface{}
	if err := json.Unmarshal(resp, &branches); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(branches))
	for _, b := range branches {
		if name, ok := b["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names, nil
}

// stringifyArg extracts a string from an interface{} argument, falling back to fmt.Sprint.
func stringifyArg(args map[string]interface{}, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	if v, ok := args[key]; ok {
		return strings.TrimSpace(fmt.Sprint(v))
	}
	return ""
}

// formatIssueSummary formats an issue map into a human-readable string.
func formatIssueSummary(issue map[string]interface{}) string {
	var sb strings.Builder
	title, _ := issue["title"].(string)
	state, _ := issue["state"].(string)
	body, _ := issue["body"].(string)
	labelNames := []string{}
	if ls, ok := issue["labels"].([]interface{}); ok {
		for _, l := range ls {
			if lm, ok := l.(map[string]interface{}); ok {
				if n, ok := lm["name"].(string); ok {
					labelNames = append(labelNames, n)
				}
			}
		}
	}
	sb.WriteString(fmt.Sprintf("Title: %s\n", title))
	sb.WriteString(fmt.Sprintf("State: %s\n", state))
	if len(labelNames) > 0 {
		sb.WriteString(fmt.Sprintf("Labels: %s\n", strings.Join(labelNames, ", ")))
	} else {
		sb.WriteString("Labels: \n")
	}
	sb.WriteString("Body:\n")
	sb.WriteString(body)
	return sb.String()
}

// formatIssueList formats a list of issues into JSON summary.
func formatIssueList(issues []map[string]interface{}) (string, error) {
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
		return "", err
	}
	return string(out), nil
}

// formatPRFiles formats a list of PR files into JSON summary.
func formatPRFiles(files []map[string]interface{}) (string, error) {
	summary := make([]map[string]interface{}, 0, len(files))
	for _, f := range files {
		filename, _ := f["filename"].(string)
		status, _ := f["status"].(string)
		additions, _ := f["additions"].(float64)
		deletions, _ := f["deletions"].(float64)
		summary = append(summary, map[string]interface{}{
			"filename":  filename,
			"status":    status,
			"additions": int(additions),
			"deletions": int(deletions),
		})
	}
	out, err := json.Marshal(summary)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// formatBranches formats a list of branches into a plain text string.
func formatBranches(branches []map[string]interface{}) string {
	if len(branches) == 0 {
		return "no branches found"
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
	return sb.String()
}

// buildIssueListURL constructs the API path for listing issues with filters.
func buildIssueListURL(owner, repo, state, labels string) string {
	q := url.Values{}
	q.Set("state", state)
	if labels != "" {
		q.Set("labels", labels)
	}
	return fmt.Sprintf("/repos/%s/%s/issues?%s", owner, repo, q.Encode())
}
