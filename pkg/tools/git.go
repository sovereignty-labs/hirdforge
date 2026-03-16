package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func runGit(dir string, args []string, timeout time.Duration) ToolResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	output := string(out)
	if ctx.Err() == context.DeadlineExceeded {
		return ToolResult{Output: output, Error: "git command timed out"}
	}
	if err != nil {
		return ToolResult{Output: output, Error: err.Error()}
	}
	return ToolResult{Output: output}
}

func runShellCapture(dir, command string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

type GitCloneTool struct {
	WorkDir  string
	GiteaURL string
	Token    string
}

func NewGitCloneTool(workDir, giteaURL, token string) *GitCloneTool {
	return &GitCloneTool{WorkDir: workDir, GiteaURL: giteaURL, Token: resolveGiteaToken(token)}
}

func (t *GitCloneTool) Name() string { return "git-clone" }

func (t *GitCloneTool) Description() string {
	return "Clone a Gitea repository into the workspace, or pull latest if already cloned. Optionally checkout a specific branch."
}

func (t *GitCloneTool) Parameters() map[string]string {
	return map[string]string{
		"repo":   "Repository name (e.g. project_valhalla) or owner/repo",
		"branch": "Branch to checkout (optional, defaults to default branch)",
	}
}

func (t *GitCloneTool) Execute(args map[string]interface{}) ToolResult {
	repo, ok := args["repo"].(string)
	if !ok || repo == "" {
		return ToolResult{Error: "repo is required"}
	}
	branch, _ := args["branch"].(string)

	repoName := repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		repoName = repo[i+1:]
	}
	repoDir := filepath.Join(t.WorkDir, repoName)

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil {
		if res := runGit(repoDir, []string{"git", "fetch", "--all"}, 60*time.Second); res.Error != "" {
			return res
		}
		if branch != "" {
			runGit(repoDir, []string{"git", "checkout", branch}, 10*time.Second)
			if res := runGit(repoDir, []string{"git", "pull", "origin", branch}, 60*time.Second); res.Error != "" {
				return res
			}
		} else {
			if res := runGit(repoDir, []string{"git", "pull"}, 60*time.Second); res.Error != "" {
				return res
			}
		}
		return ToolResult{Output: fmt.Sprintf("updated %s in %s", repo, repoDir)}
	}

	if err := os.RemoveAll(repoDir); err != nil {
		return ToolResult{Error: fmt.Sprintf("failed to clean workspace directory %s: %s", repoDir, err)}
	}

	cloneURL := t.buildURL(repo)
	cmdArgs := []string{"git", "clone"}
	if branch != "" {
		cmdArgs = append(cmdArgs, "-b", branch)
	}
	cmdArgs = append(cmdArgs, cloneURL, repoDir)

	if res := runGit(t.WorkDir, cmdArgs, 120*time.Second); res.Error != "" {
		if cloneTargetExistsError(res) {
			return ToolResult{Error: "Error: workspace already contains files. Run `exec: rm -rf /workspace/*` to clean, then retry git-clone."}
		}
		// If directory exists but clone failed (stale/corrupt workspace), remove and retry once
		if _, statErr := os.Stat(repoDir); statErr == nil {
			_ = os.RemoveAll(repoDir)
			if res2 := runGit(t.WorkDir, cmdArgs, 120*time.Second); res2.Error != "" {
				if cloneTargetExistsError(res2) {
					return ToolResult{Error: "Error: workspace already contains files. Run `exec: rm -rf /workspace/*` to clean, then retry git-clone."}
				}
				return res2
			}
		} else {
			return res
		}
	}

	runGit(repoDir, []string{"git", "config", "user.email", "agent@valhalla.local"}, 5*time.Second)
	runGit(repoDir, []string{"git", "config", "user.name", "Valhalla Agent"}, 5*time.Second)
	t.ensureCredentialedOrigin(repoDir, repo)

	return ToolResult{Output: fmt.Sprintf("cloned %s to %s", repo, repoDir)}
}

func (t *GitCloneTool) AppendProjectAwareness(repo string, output string) string {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return output
	}
	repoName := repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		repoName = repo[i+1:]
	}
	repoDir := filepath.Join(t.WorkDir, repoName)
	structureSection := buildProjectAwarenessSection(repoDir)
	activitySection := t.buildRecentActivitySection(repo)
	if structureSection == "" && activitySection == "" {
		return output
	}
	var b strings.Builder
	b.WriteString(output)
	if structureSection != "" {
		b.WriteString(structureSection)
	}
	if activitySection != "" {
		b.WriteString(activitySection)
	}
	return b.String()
}

func (t *GitCloneTool) buildRecentActivitySection(repo string) string {
	token := strings.TrimSpace(resolveGiteaToken(t.Token))
	if token == "" {
		return ""
	}
	owner, repoName := resolveRepoOwnerName(repo)
	if owner == "" || repoName == "" {
		return ""
	}
	client := &http.Client{Timeout: 3 * time.Second}
	base := "http://gitea-http.gitea.svc.cluster.local:3000/api/v1/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repoName)

	type prItem struct {
		Number   int    `json:"number"`
		Title    string `json:"title"`
		Merged   bool   `json:"merged"`
		MergedAt string `json:"merged_at"`
	}
	var prs []prItem
	if err := fetchGiteaJSON(client, token, base+"/pulls?state=closed&sort=updated&limit=5", &prs); err != nil {
		return ""
	}

	type issueLabel struct {
		Name string `json:"name"`
	}
	type issueItem struct {
		Number      int               `json:"number"`
		Title       string            `json:"title"`
		Labels      []issueLabel      `json:"labels"`
		PullRequest map[string]string `json:"pull_request"`
	}
	var issues []issueItem
	if err := fetchGiteaJSON(client, token, base+"/issues?state=open&sort=updated&limit=5", &issues); err != nil {
		return ""
	}

	var mergedLines []string
	for _, pr := range prs {
		if !pr.Merged && strings.TrimSpace(pr.MergedAt) == "" {
			continue
		}
		mergedLines = append(mergedLines, fmt.Sprintf("    #%d %s", pr.Number, strings.TrimSpace(pr.Title)))
		if len(mergedLines) == 5 {
			break
		}
	}

	var issueLines []string
	for _, issue := range issues {
		if len(issue.PullRequest) > 0 {
			continue
		}
		labels := make([]string, 0, len(issue.Labels))
		for _, label := range issue.Labels {
			name := strings.TrimSpace(label.Name)
			if name != "" {
				labels = append(labels, name)
			}
		}
		if len(labels) > 0 {
			issueLines = append(issueLines, fmt.Sprintf("    #%d %s (%s)", issue.Number, strings.TrimSpace(issue.Title), strings.Join(labels, ", ")))
		} else {
			issueLines = append(issueLines, fmt.Sprintf("    #%d %s", issue.Number, strings.TrimSpace(issue.Title)))
		}
		if len(issueLines) == 5 {
			break
		}
	}

	if len(mergedLines) == 0 && len(issueLines) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n\n---\nRecent activity:\n")
	b.WriteString("  Merged PRs:\n")
	if len(mergedLines) == 0 {
		b.WriteString("    (none)\n")
	} else {
		b.WriteString(strings.Join(mergedLines, "\n"))
		b.WriteString("\n")
	}
	b.WriteString("\n  Open issues:\n")
	if len(issueLines) == 0 {
		b.WriteString("    (none)\n")
	} else {
		b.WriteString(strings.Join(issueLines, "\n"))
		b.WriteString("\n")
	}
	b.WriteString("---")
	return b.String()
}

func fetchGiteaJSON(client *http.Client, token, rawURL string, out interface{}) error {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func buildProjectAwarenessSection(repoDir string) string {
	totalFilesOutput, _ := runShellCapture(repoDir, "find . -not -path './.git/*' -type f | wc -l", 5*time.Second)
	fileCountsOutput, _ := runShellCapture(repoDir, "find . -not -path './.git/*' -type f | sed 's/.*\\.//' | sort | uniq -c | sort -rn | head -10", 8*time.Second)
	recentCommitsOutput, _ := runShellCapture(repoDir, "git log --oneline -5", 5*time.Second)
	largeFilesOutput, _ := runShellCapture(repoDir, "find . -not -path './.git/*' -type f \\( -name '*.go' -o -name '*.py' -o -name '*.js' -o -name '*.yaml' \\) | head -20 | xargs wc -l 2>/dev/null | sort -rn | head -10", 8*time.Second)

	var sections []string
	if filesLine := formatFileSummary(totalFilesOutput, fileCountsOutput); filesLine != "" {
		sections = append(sections, "  "+filesLine)
	}
	if largeFilesSection := formatLargeFilesSection(largeFilesOutput); largeFilesSection != "" {
		sections = append(sections, "\n  Largest source files:\n"+largeFilesSection)
	}
	if commitsSection := formatRecentCommitsSection(recentCommitsOutput); commitsSection != "" {
		sections = append(sections, "\n  Recent commits:\n"+commitsSection)
	}
	if len(sections) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n\n---\nProject structure:\n")
	for _, section := range sections {
		b.WriteString(section)
		b.WriteString("\n")
	}
	if _, err := os.Stat(filepath.Join(repoDir, "ARCHITECTURE.md")); err == nil {
		b.WriteString("\n  Read ARCHITECTURE.md for full codebase map.\n")
	}
	b.WriteString("---")
	return b.String()
}

func formatFileSummary(totalFilesOutput, fileCountsOutput string) string {
	totalFiles, err := strconv.Atoi(strings.TrimSpace(totalFilesOutput))
	if err != nil || totalFiles <= 0 {
		return ""
	}

	type extCount struct {
		ext   string
		count int
	}
	var counts []extCount
	for _, line := range strings.Split(fileCountsOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		count, err := strconv.Atoi(fields[0])
		if err != nil || count <= 0 {
			continue
		}
		ext := fields[len(fields)-1]
		if strings.Contains(ext, "/") {
			continue
		}
		counts = append(counts, extCount{ext: ext, count: count})
		if len(counts) == 4 {
			break
		}
	}

	parts := make([]string, 0, len(counts)+1)
	shownCount := 0
	for _, item := range counts {
		shownCount += item.count
		parts = append(parts, fmt.Sprintf("%d .%s", item.count, item.ext))
	}
	if otherCount := totalFiles - shownCount; otherCount > 0 {
		parts = append(parts, fmt.Sprintf("%d other", otherCount))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Files: %d", totalFiles)
	}
	return fmt.Sprintf("Files: %d (%s)", totalFiles, strings.Join(parts, ", "))
}

func formatLargeFilesSection(largeFilesOutput string) string {
	var lines []string
	for _, line := range strings.Split(largeFilesOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[1] == "total" {
			continue
		}
		lines = append(lines, fmt.Sprintf("    %s (%s lines)", fields[1], fields[0]))
	}
	return strings.Join(lines, "\n")
}

func formatRecentCommitsSection(recentCommitsOutput string) string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(recentCommitsOutput), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, "    "+line)
	}
	return strings.Join(lines, "\n")
}

func (t *GitCloneTool) buildURL(repo string) string {
	t.Token = resolveGiteaToken(t.Token)
	base := strings.TrimRight(t.GiteaURL, "/")
	owner, name := resolveRepoOwnerName(repo)
	if t.Token != "" {
		stripped := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
		scheme := "http://"
		if strings.HasPrefix(base, "https://") {
			scheme = "https://"
		}
		return fmt.Sprintf("%swarband:%s@%s/%s/%s.git", scheme, t.Token, stripped, owner, name)
	}
	return fmt.Sprintf("%s/%s/%s.git", base, owner, name)
}

func (t *GitCloneTool) ensureCredentialedOrigin(repoDir, repo string) {
	t.Token = resolveGiteaToken(t.Token)
	if t.Token == "" {
		return
	}
	giteaHost := hostFromURL(t.GiteaURL)
	if giteaHost == "" {
		return
	}

	originRes := runGit(repoDir, []string{"git", "remote", "get-url", "origin"}, 5*time.Second)
	if originRes.Error != "" {
		return
	}
	originURL := strings.TrimSpace(originRes.Output)
	if originURL == "" {
		return
	}
	if hostFromURL(originURL) != giteaHost {
		return
	}
	if hasURLCredentials(originURL) {
		return
	}

	if res := runGit(repoDir, []string{"git", "remote", "set-url", "origin", t.buildURL(repo)}, 5*time.Second); res.Error != "" {
		log.Printf("git-clone: failed to set credentialed origin for %s: %s", repoDir, res.Error)
	}
}

func hostFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

func hasURLCredentials(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return u.User != nil
}

func cloneTargetExistsError(res ToolResult) bool {
	text := strings.ToLower(strings.TrimSpace(res.Output + "\n" + res.Error))
	return strings.Contains(text, "already exists and is not an empty directory")
}

func validateStagedFilesForCommit(repoDir string) error {
	if _, err := exec.LookPath("python3"); err != nil {
		return nil
	}

	stagedRes := runGit(repoDir, []string{"git", "diff", "--cached", "--name-only"}, 10*time.Second)
	if stagedRes.Error != "" {
		return nil
	}
	for _, file := range strings.Split(stagedRes.Output, "\n") {
		file = strings.TrimSpace(file)
		if file == "" || strings.HasPrefix(file, ".git/") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(file))
		switch ext {
		case ".py", ".yaml", ".yml", ".json":
		default:
			continue
		}
		absPath := filepath.Join(repoDir, file)
		info, err := os.Stat(absPath)
		if err != nil || info.IsDir() {
			continue
		}
		if binary, err := fileLooksBinary(absPath); err == nil && binary {
			continue
		}

		errOutput, failed, timedOut := runFileValidation(repoDir, file, ext)
		if timedOut {
			continue
		}
		if failed {
			if errOutput == "" {
				errOutput = "validation failed with no output"
			}
			return fmt.Errorf("Commit blocked: syntax error in %s:\n%s\nFix the error and try git-commit again.", file, errOutput)
		}
	}
	return nil
}

func runFileValidation(repoDir, file, ext string) (errOutput string, failed bool, timedOut bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	switch ext {
	case ".py":
		cmd = exec.CommandContext(ctx, "python3", "-m", "py_compile", file)
	case ".yaml", ".yml":
		cmd = exec.CommandContext(ctx, "python3", "-c", "import yaml,sys; yaml.safe_load(open(sys.argv[1]))", file)
	case ".json":
		cmd = exec.CommandContext(ctx, "python3", "-c", "import json,sys; json.load(open(sys.argv[1]))", file)
	default:
		return "", false, false
	}
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", false, true
	}
	if err != nil {
		return strings.TrimSpace(string(out)), true, false
	}
	return "", false, false
}

func fileLooksBinary(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if n <= 0 {
		return false, err
	}
	return bytes.IndexByte(buf[:n], 0) >= 0, nil
}

type GitCommitTool struct {
	WorkDir  string
	GiteaURL string
	Token    string
}

func NewGitCommitTool(workDir, giteaURL, token string) *GitCommitTool {
	return &GitCommitTool{WorkDir: workDir, GiteaURL: giteaURL, Token: resolveGiteaToken(token)}
}

func (t *GitCommitTool) Name() string { return "git-commit" }

func (t *GitCommitTool) Description() string {
	return "Stage all changes, commit with a message, and push. Optionally create and push to a new branch."
}

func (t *GitCommitTool) Parameters() map[string]string {
	return map[string]string{
		"repo":    "Repository directory name in workspace (e.g. project_valhalla)",
		"message": "Commit message",
		"branch":  "Branch to create/push to (optional, pushes to current branch if omitted)",
	}
}

func (t *GitCommitTool) Execute(args map[string]interface{}) ToolResult {
	repo, ok := args["repo"].(string)
	if !ok || repo == "" {
		return ToolResult{Error: "repo is required"}
	}
	message, ok := args["message"].(string)
	if !ok || message == "" {
		return ToolResult{Error: "message is required"}
	}
	branch, _ := args["branch"].(string)

	repoDir := filepath.Join(t.WorkDir, repo)
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		return ToolResult{Error: fmt.Sprintf("repo %s not found in workspace — clone it first", repo)}
	}

	if t.Token != "" {
		pushURL := t.buildPushURL(repo)
		runGit(repoDir, []string{"git", "remote", "set-url", "origin", pushURL}, 5*time.Second)
	}

	if branch != "" {
		res := runGit(repoDir, []string{"git", "checkout", branch}, 10*time.Second)
		if res.Error != "" {
			res = runGit(repoDir, []string{"git", "checkout", "-b", branch}, 10*time.Second)
			if res.Error != "" {
				return ToolResult{Error: fmt.Sprintf("failed to create branch %s: %s", branch, res.Error)}
			}
		}
	}

	if res := runGit(repoDir, []string{"git", "add", "-A"}, 10*time.Second); res.Error != "" {
		log.Printf("git-commit auto-add failed in %s: %s (%s)", repoDir, res.Error, strings.TrimSpace(res.Output))
	}

	statusRes := runGit(repoDir, []string{"git", "status", "--porcelain"}, 10*time.Second)
	if statusRes.Error == "" && strings.TrimSpace(statusRes.Output) == "" {
		return ToolResult{Output: "Nothing to commit. Did you forget to use the write tool or exec to create/modify files? Use `exec: git status` to check workspace state."}
	}
	if err := validateStagedFilesForCommit(repoDir); err != nil {
		return ToolResult{Error: err.Error()}
	}
	diffStatRes := runGit(repoDir, []string{"git", "diff", "--cached", "--stat"}, 10*time.Second)
	if diffStatRes.Error != "" {
		log.Printf("git-commit diff --stat failed in %s: %s (%s)", repoDir, diffStatRes.Error, strings.TrimSpace(diffStatRes.Output))
	}
	shortStatRes := runGit(repoDir, []string{"git", "diff", "--cached", "--shortstat"}, 10*time.Second)
	if shortStatRes.Error != "" {
		log.Printf("git-commit diff --shortstat failed in %s: %s (%s)", repoDir, shortStatRes.Error, strings.TrimSpace(shortStatRes.Output))
	}

	if res := runGit(repoDir, []string{"git", "commit", "-m", message}, 30*time.Second); res.Error != "" {
		return res
	}

	pushArgs := []string{"git", "push", "origin"}
	if branch != "" {
		pushArgs = append(pushArgs, branch)
	} else {
		pushArgs = append(pushArgs, "HEAD")
	}
	if res := runGit(repoDir, pushArgs, 60*time.Second); res.Error != "" {
		return res
	}

	target := "current branch"
	if branch != "" {
		target = branch
	}
	out := fmt.Sprintf("Committed: %s", message)
	if diff := strings.TrimSpace(diffStatRes.Output); diff != "" {
		out += "\n\n" + diff
	}
	if short := strings.TrimSpace(shortStatRes.Output); short != "" {
		out += "\n\n" + short
	}
	out += fmt.Sprintf("\n\nPushed to %s", target)
	return ToolResult{Output: out}
}

func (t *GitCommitTool) Verify(args map[string]interface{}, result ToolResult) error {
	if result.Error != "" || strings.HasPrefix(strings.TrimSpace(result.Output), "Nothing to commit.") {
		return nil
	}

	repo, ok := args["repo"].(string)
	if !ok || repo == "" {
		return fmt.Errorf("git verification failed: repo is required")
	}
	branch, _ := args["branch"].(string)

	repoDir := filepath.Join(t.WorkDir, repo)
	if branch == "" {
		headRes := runGit(repoDir, []string{"git", "rev-parse", "--abbrev-ref", "HEAD"}, 10*time.Second)
		if headRes.Error != "" {
			return fmt.Errorf("git verification failed: resolve branch: %s", headRes.Error)
		}
		branch = strings.TrimSpace(headRes.Output)
	}
	if branch == "" {
		return fmt.Errorf("git verification failed: branch is empty")
	}

	remoteRes := runGit(repoDir, []string{"git", "ls-remote", "--heads", "origin", branch}, 30*time.Second)
	if remoteRes.Error != "" {
		return fmt.Errorf("git verification failed: %s", remoteRes.Error)
	}
	if strings.TrimSpace(remoteRes.Output) == "" {
		return fmt.Errorf("git verification failed: branch %s not found on origin", branch)
	}

	return nil
}

func (t *GitCommitTool) buildPushURL(repo string) string {
	t.Token = resolveGiteaToken(t.Token)
	base := strings.TrimRight(t.GiteaURL, "/")
	owner, name := resolveRepoOwnerName(repo)
	stripped := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	scheme := "http://"
	if strings.HasPrefix(base, "https://") {
		scheme = "https://"
	}
	return fmt.Sprintf("%stoken:%s@%s/%s/%s.git", scheme, t.Token, stripped, owner, name)
}

func resolveRepoOwnerName(repo string) (owner, name string) {
	name = strings.TrimSpace(repo)
	if strings.Contains(name, "/") {
		parts := strings.SplitN(name, "/", 2)
		return parts[0], parts[1]
	}
	owner = strings.TrimSpace(os.Getenv("GITEA_DEFAULT_OWNER"))
	if owner == "" {
		owner = "gitea_admin"
	}
	log.Printf("git tool: repo %q missing owner, defaulting to %q", repo, owner)
	return owner, name
}

type GitDiffTool struct {
	WorkDir string
}

func NewGitDiffTool(workDir string) *GitDiffTool {
	return &GitDiffTool{WorkDir: workDir}
}

func (t *GitDiffTool) Name() string { return "git-diff" }

func (t *GitDiffTool) Description() string {
	return "Show the diff of changes in a repository. Compare working directory, staged changes, or a branch against main."
}

func (t *GitDiffTool) Parameters() map[string]string {
	return map[string]string{
		"repo":   "Repository directory name in workspace",
		"branch": "Compare this branch against main/master (optional — if omitted, shows uncommitted changes)",
	}
}

func (t *GitDiffTool) Execute(args map[string]interface{}) ToolResult {
	repo, ok := args["repo"].(string)
	if !ok || repo == "" {
		return ToolResult{Error: "repo is required"}
	}
	branch, _ := args["branch"].(string)

	repoDir := filepath.Join(t.WorkDir, repo)
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		return ToolResult{Error: fmt.Sprintf("repo %s not found in workspace — clone it first", repo)}
	}

	// Fetch all remote refs so branch comparisons work
	runGit(repoDir, []string{"git", "fetch", "origin"}, 60*time.Second)
	if branch != "" {
		diffArgs := []string{"git", "diff", "origin/main...origin/" + branch}
		res := runGit(repoDir, diffArgs, 30*time.Second)
		if res.Error != "" {
			diffArgs = []string{"git", "diff", "origin/master...origin/" + branch}
			res = runGit(repoDir, diffArgs, 30*time.Second)
		}
		if res.Error != "" {
			return res
		}
		if strings.TrimSpace(res.Output) == "" {
			return ToolResult{Output: fmt.Sprintf("no differences between main and %s", branch)}
		}
		return res
	}

	res := runGit(repoDir, []string{"git", "diff", "HEAD"}, 30*time.Second)
	if res.Error != "" {
		res = runGit(repoDir, []string{"git", "diff"}, 30*time.Second)
	}
	if res.Error != "" {
		return res
	}
	if strings.TrimSpace(res.Output) == "" {
		return ToolResult{Output: "no uncommitted changes"}
	}
	return res
}
