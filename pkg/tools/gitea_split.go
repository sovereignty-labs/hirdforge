package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Individual Gitea action tools. Each wraps the shared GiteaAPITool backend.
// All are registered when enabled["gitea"] is true.

type createIssueTool struct{ api *GiteaAPITool }

func NewCreateIssueTool(api *GiteaAPITool) *createIssueTool    { return &createIssueTool{api: api} }
func (t *createIssueTool) Name() string                        { return "create-issue" }
func (t *createIssueTool) Description() string                 { return "Create a new issue in a Gitea repository." }
func (t *createIssueTool) Parameters() map[string]string {
	return map[string]string{
		"repo":   "Repository name (e.g. project_valhalla) or owner/repo",
		"title":  "Issue title",
		"body":   "Issue body text",
		"labels": "Comma-separated label names (optional)",
	}
}
func (t *createIssueTool) Execute(args map[string]interface{}) ToolResult {
	repo, _ := args["repo"].(string)
	owner, name := t.api.parseRepo(repo)
	if name == "" {
		return ToolResult{Error: "repo is required"}
	}
	title := stringifyArg(args, "title")
	if title == "" {
		return ToolResult{Error: "title is required"}
	}
	body := stringifyArg(args, "body")
	labelsCSV := stringifyArg(args, "labels")

	labelIDs, err := t.api.resolveLabelIDs(owner, name, labelsCSV)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	payload := map[string]interface{}{"title": title, "body": body}
	if len(labelIDs) > 0 {
		payload["labels"] = labelIDs
	}
	resp, status, err := t.api.apiRequest("POST", fmt.Sprintf("/repos/%s/%s/issues", owner, name), payload)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}

	var result map[string]interface{}
	json.Unmarshal(resp, &result)
	num, _ := result["number"].(float64)
	htmlURL, _ := result["html_url"].(string)
	return ToolResult{Output: fmt.Sprintf("created issue #%d: %s\n%s", int(num), title, htmlURL)}
}

type createPRTool struct{ api *GiteaAPITool }

func NewCreatePRTool(api *GiteaAPITool) *createPRTool    { return &createPRTool{api: api} }
func (t *createPRTool) Name() string                     { return "create-pr" }
func (t *createPRTool) Description() string               { return "Create a pull request in a Gitea repository." }
func (t *createPRTool) Parameters() map[string]string {
	return map[string]string{
		"repo":  "Repository name (e.g. project_valhalla) or owner/repo",
		"head":  "Source branch name",
		"base":  "Target branch (defaults to main)",
		"title": "PR title",
		"body":  "PR description (optional)",
	}
}
func (t *createPRTool) Execute(args map[string]interface{}) ToolResult {
	repo, _ := args["repo"].(string)
	owner, name := t.api.parseRepo(repo)
	if name == "" {
		return ToolResult{Error: "repo is required"}
	}
	title := stringifyArg(args, "title")
	head := stringifyArg(args, "head")
	body := stringifyArg(args, "body")
	base := stringifyArg(args, "base")
	if base == "" {
		base = "main"
	}
	if title == "" {
		return ToolResult{Error: "title is required"}
	}
	if head == "" {
		return ToolResult{Error: "head (source branch) is required"}
	}

	payload := map[string]string{"title": title, "body": body, "head": head, "base": base}
	resp, status, err := t.api.apiRequest("POST", fmt.Sprintf("/repos/%s/%s/pulls", owner, name), payload)
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
	htmlURL, _ := result["html_url"].(string)
	return ToolResult{Output: fmt.Sprintf("created PR #%d: %s\n%s", int(num), title, htmlURL)}
}

type listIssuesTool struct{ api *GiteaAPITool }

func NewListIssuesTool(api *GiteaAPITool) *listIssuesTool    { return &listIssuesTool{api: api} }
func (t *listIssuesTool) Name() string                      { return "list-issues" }
func (t *listIssuesTool) Description() string               { return "List issues in a Gitea repository with optional state and label filters." }
func (t *listIssuesTool) Parameters() map[string]string {
	return map[string]string{
		"repo":   "Repository name (e.g. project_valhalla) or owner/repo",
		"state":  "Issue state: open, closed, or all (default: open)",
		"labels": "Comma-separated label names to filter by (optional)",
	}
}
func (t *listIssuesTool) Execute(args map[string]interface{}) ToolResult {
	repo, _ := args["repo"].(string)
	owner, name := t.api.parseRepo(repo)
	if name == "" {
		return ToolResult{Error: "repo is required"}
	}
	state := strings.ToLower(strings.TrimSpace(stringifyArg(args, "state")))
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "closed" && state != "all" {
		return ToolResult{Error: "state must be open, closed, or all"}
	}
	labelsCSV := stringifyArg(args, "labels")
	path := buildIssueListURL(owner, name, state, labelsCSV)
	resp, status, err := t.api.apiRequest("GET", path, nil)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}

	var issues []map[string]interface{}
	json.Unmarshal(resp, &issues)

	out, err := formatIssueList(issues)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	return ToolResult{Output: out}
}

type closeIssueTool struct{ api *GiteaAPITool }

func NewCloseIssueTool(api *GiteaAPITool) *closeIssueTool    { return &closeIssueTool{api: api} }
func (t *closeIssueTool) Name() string                       { return "close-issue" }
func (t *closeIssueTool) Description() string                { return "Close an issue in a Gitea repository." }
func (t *closeIssueTool) Parameters() map[string]string {
	return map[string]string{
		"repo":  "Repository name (e.g. project_valhalla) or owner/repo",
		"index": "Issue number",
	}
}
func (t *closeIssueTool) Execute(args map[string]interface{}) ToolResult {
	repo, _ := args["repo"].(string)
	owner, name := t.api.parseRepo(repo)
	if name == "" {
		return ToolResult{Error: "repo is required"}
	}
	issueNum := t.api.extractNumber(args["index"])
	if issueNum == 0 {
		return ToolResult{Error: "index (issue number) is required"}
	}
	payload := map[string]string{"state": "closed"}
	resp, status, err := t.api.apiRequest("PATCH", fmt.Sprintf("/repos/%s/%s/issues/%d", owner, name, issueNum), payload)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}
	return ToolResult{Output: fmt.Sprintf("closed issue #%d", issueNum)}
}

type commentTool struct{ api *GiteaAPITool }

func NewCommentTool(api *GiteaAPITool) *commentTool    { return &commentTool{api: api} }
func (t *commentTool) Name() string                   { return "comment" }
func (t *commentTool) Description() string             { return "Add a comment to an issue or pull request in a Gitea repository." }
func (t *commentTool) Parameters() map[string]string {
	return map[string]string{
		"repo":  "Repository name (e.g. project_valhalla) or owner/repo",
		"index": "Issue or PR number",
		"body":  "Comment text",
	}
}
func (t *commentTool) Execute(args map[string]interface{}) ToolResult {
	repo, _ := args["repo"].(string)
	owner, name := t.api.parseRepo(repo)
	if name == "" {
		return ToolResult{Error: "repo is required"}
	}
	body := stringifyArg(args, "body")
	if body == "" {
		return ToolResult{Error: "body is required"}
	}
	issueNum := t.api.extractNumber(args["index"])
	if issueNum == 0 {
		return ToolResult{Error: "index (issue or PR number) is required"}
	}
	payload := map[string]string{"body": body}
	resp, status, err := t.api.apiRequest("POST", fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, name, issueNum), payload)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}
	return ToolResult{Output: fmt.Sprintf("commented on issue #%d", issueNum)}
}

type createReviewTool struct{ api *GiteaAPITool }

func NewCreateReviewTool(api *GiteaAPITool) *createReviewTool    { return &createReviewTool{api: api} }
func (t *createReviewTool) Name() string                         { return "create-review" }
func (t *createReviewTool) Description() string                  { return "Submit a formal review (APPROVED or REQUEST_CHANGES) on a pull request." }
func (t *createReviewTool) Parameters() map[string]string {
	return map[string]string{
		"repo":   "Repository name (e.g. project_valhalla) or owner/repo",
		"index":  "PR number",
		"state":  "Review state: APPROVED or REQUEST_CHANGES",
		"body":   "Review text",
	}
}
func (t *createReviewTool) Execute(args map[string]interface{}) ToolResult {
	repo, _ := args["repo"].(string)
	owner, name := t.api.parseRepo(repo)
	if name == "" {
		return ToolResult{Error: "repo is required"}
	}
	prNum := t.api.extractNumber(args["index"])
	if prNum == 0 {
		return ToolResult{Error: "index (PR number) is required"}
	}
	body := stringifyArg(args, "body")
	state := strings.ToUpper(strings.TrimSpace(stringifyArg(args, "state")))
	if body == "" || (state != "APPROVED" && state != "REQUEST_CHANGES") {
		return ToolResult{Error: "body and state (APPROVED or REQUEST_CHANGES) are required"}
	}
	token := strings.TrimSpace(t.api.ReviewersToken)
	if token == "" {
		token = resolveGiteaReviewersToken("")
	}
	if token == "" {
		return ToolResult{Error: "missing Gitea reviewers token"}
	}
	payload := map[string]string{"body": body, "event": state}
	resp, status, err := t.api.apiRequestWithToken("POST", fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, name, prNum), payload, token)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}
	return ToolResult{Output: fmt.Sprintf("submitted %s review on PR #%d", state, prNum)}
}

type mergePRTool struct{ api *GiteaAPITool }

func NewMergePRTool(api *GiteaAPITool) *mergePRTool    { return &mergePRTool{api: api} }
func (t *mergePRTool) Name() string                   { return "merge-pr" }
func (t *mergePRTool) Description() string            { return "Merge a pull request in a Gitea repository." }
func (t *mergePRTool) Parameters() map[string]string {
	return map[string]string{
		"repo":  "Repository name (e.g. project_valhalla) or owner/repo",
		"index": "PR number",
	}
}
func (t *mergePRTool) Execute(args map[string]interface{}) ToolResult {
	repo, _ := args["repo"].(string)
	owner, name := t.api.parseRepo(repo)
	if name == "" {
		return ToolResult{Error: "repo is required"}
	}
	prNum := t.api.extractNumber(args["index"])
	if prNum == 0 {
		return ToolResult{Error: "index (PR number) is required"}
	}
	payload := map[string]string{"do": "merge"}
	resp, status, err := t.api.apiRequest("POST", fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", owner, name, prNum), payload)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}
	return ToolResult{Output: fmt.Sprintf("merged PR #%d", prNum)}
}

type listPRFilesTool struct{ api *GiteaAPITool }

func NewListPRFilesTool(api *GiteaAPITool) *listPRFilesTool    { return &listPRFilesTool{api: api} }
func (t *listPRFilesTool) Name() string                       { return "list-pr-files" }
func (t *listPRFilesTool) Description() string                { return "List the files changed in a pull request." }
func (t *listPRFilesTool) Parameters() map[string]string {
	return map[string]string{
		"repo":  "Repository name (e.g. project_valhalla) or owner/repo",
		"index": "PR number",
	}
}
func (t *listPRFilesTool) Execute(args map[string]interface{}) ToolResult {
	repo, _ := args["repo"].(string)
	owner, name := t.api.parseRepo(repo)
	if name == "" {
		return ToolResult{Error: "repo is required"}
	}
	prNum := t.api.extractNumber(args["index"])
	if prNum == 0 {
		return ToolResult{Error: "index (PR number) is required"}
	}
	resp, status, err := t.api.apiRequest("GET", fmt.Sprintf("/repos/%s/%s/pulls/%d/files", owner, name, prNum), nil)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}
	var files []map[string]interface{}
	if err := json.Unmarshal(resp, &files); err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to parse response: %v", err)}
	}
	out, err := formatPRFiles(files)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	return ToolResult{Output: out}
}

type updateLabelsTool struct{ api *GiteaAPITool }

func NewUpdateLabelsTool(api *GiteaAPITool) *updateLabelsTool    { return &updateLabelsTool{api: api} }
func (t *updateLabelsTool) Name() string                        { return "update-labels" }
func (t *updateLabelsTool) Description() string                 { return "Replace all labels on an issue. Labels are resolved by name to IDs automatically." }
func (t *updateLabelsTool) Parameters() map[string]string {
	return map[string]string{
		"repo":   "Repository name (e.g. project_valhalla) or owner/repo",
		"index":  "Issue number",
		"labels": "Comma-separated label names (replaces all existing labels)",
	}
}
func (t *updateLabelsTool) Execute(args map[string]interface{}) ToolResult {
	repo, _ := args["repo"].(string)
	owner, name := t.api.parseRepo(repo)
	if name == "" {
		return ToolResult{Error: "repo is required"}
	}
	issueNum := t.api.extractNumber(args["index"])
	if issueNum == 0 {
		return ToolResult{Error: "index (issue number) is required"}
	}
	labelsCSV := stringifyArg(args, "labels")
	labelIDs, err := t.api.resolveLabelIDs(owner, name, labelsCSV)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	payload := map[string]interface{}{"labels": labelIDs}
	resp, status, err := t.api.apiRequest("PUT", fmt.Sprintf("/repos/%s/%s/issues/%d/labels", owner, name, issueNum), payload)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}
	return ToolResult{Output: fmt.Sprintf("updated labels on issue #%d to: %s", issueNum, labelsCSV)}
}

type getIssueTool struct{ api *GiteaAPITool }

func NewGetIssueTool(api *GiteaAPITool) *getIssueTool    { return &getIssueTool{api: api} }
func (t *getIssueTool) Name() string                    { return "get-issue" }
func (t *getIssueTool) Description() string             { return "Get the full details of an issue, including body and comments." }
func (t *getIssueTool) Parameters() map[string]string {
	return map[string]string{
		"owner": "Repository owner (optional if repo is owner/repo format)",
		"repo":  "Repository name (e.g. project_valhalla) or owner/repo",
		"index": "Issue number",
	}
}
func (t *getIssueTool) Execute(args map[string]interface{}) ToolResult {
	repo, _ := args["repo"].(string)
	ownerArg, _ := args["owner"].(string)
	ownerArg = strings.TrimSpace(ownerArg)
	name := strings.TrimSpace(repo)
	var owner string
	if ownerArg == "" {
		owner, name = t.api.parseRepo(name)
	} else {
		owner = ownerArg
	}
	if name == "" {
		return ToolResult{Error: "repo is required"}
	}
	issueNum := t.api.extractNumber(args["index"])
	if issueNum == 0 {
		return ToolResult{Error: "index (issue number) is required"}
	}

	issue, err := t.api.GetIssue(owner, name, issueNum)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if issue == nil {
		return ToolResult{Error: "issue not found"}
	}

	comments, err := t.api.GetComments(owner, name, issueNum)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if comments == nil {
		comments = []map[string]interface{}{}
	}

	var sb strings.Builder
	sb.WriteString(formatIssueSummary(issue))
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

type listBranchesTool struct{ api *GiteaAPITool }

func NewListBranchesTool(api *GiteaAPITool) *listBranchesTool    { return &listBranchesTool{api: api} }
func (t *listBranchesTool) Name() string                        { return "list-branches" }
func (t *listBranchesTool) Description() string                 { return "List all branches in a Gitea repository." }
func (t *listBranchesTool) Parameters() map[string]string {
	return map[string]string{
		"repo": "Repository name (e.g. project_valhalla) or owner/repo",
	}
}
func (t *listBranchesTool) Execute(args map[string]interface{}) ToolResult {
	repo, _ := args["repo"].(string)
	owner, name := t.api.parseRepo(repo)
	if name == "" {
		return ToolResult{Error: "repo is required"}
	}
	resp, status, err := t.api.apiRequest("GET", fmt.Sprintf("/repos/%s/%s/branches", owner, name), nil)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if status >= 400 {
		return ToolResult{Error: fmt.Sprintf("HTTP %d: %s", status, string(resp))}
	}

	var branches []map[string]interface{}
	json.Unmarshal(resp, &branches)

	return ToolResult{Output: formatBranches(branches)}
}
