package tools

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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
		// If directory exists but clone failed (stale/corrupt workspace), remove and retry once
		if _, statErr := os.Stat(repoDir); statErr == nil {
			_ = os.RemoveAll(repoDir)
			if res2 := runGit(t.WorkDir, cmdArgs, 120*time.Second); res2.Error != "" {
				return res2
			}
		} else {
			return res
		}
	}

	runGit(repoDir, []string{"git", "config", "user.email", "agent@valhalla.local"}, 5*time.Second)
	runGit(repoDir, []string{"git", "config", "user.name", "Valhalla Agent"}, 5*time.Second)

	return ToolResult{Output: fmt.Sprintf("cloned %s to %s", repo, repoDir)}
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
		return fmt.Sprintf("%stoken:%s@%s/%s/%s.git", scheme, t.Token, stripped, owner, name)
	}
	return fmt.Sprintf("%s/%s/%s.git", base, owner, name)
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
		return ToolResult{Output: "no changes to commit"}
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
	return ToolResult{Output: fmt.Sprintf("committed and pushed to %s: %s", target, message)}
}

func (t *GitCommitTool) Verify(args map[string]interface{}, result ToolResult) error {
	if result.Error != "" || strings.TrimSpace(result.Output) == "no changes to commit" {
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
