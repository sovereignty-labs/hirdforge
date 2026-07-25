package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatNewBranchName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentName string
		branch    string
		want      string
	}{
		{name: "prefixes and flattens branch", agentName: "chuck", branch: "feat/add-docs", want: "chuck/feat-add-docs"},
		{name: "keeps existing prefix", agentName: "chuck", branch: "chuck/feat/add-docs", want: "chuck/feat/add-docs"},
		{name: "no agent name", agentName: "", branch: "feat/add-docs", want: "feat/add-docs"},
		{name: "trims leading slash", agentName: "chuck", branch: "/feat/add-docs", want: "chuck/feat-add-docs"},
		// The sandbox work branch "agent/<task>" is authoritative — create-pr
		// opens the PR for exactly that ref, so it must never be rewritten.
		{name: "preserves sandbox work branch", agentName: "Builder", branch: "agent/hf-019f9489fb2a", want: "agent/hf-019f9489fb2a"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := formatNewBranchName(tt.agentName, tt.branch); got != tt.want {
				t.Fatalf("formatNewBranchName(%q, %q) = %q, want %q", tt.agentName, tt.branch, got, tt.want)
			}
		})
	}
}

func TestGitCommitToolPrefixesNewBranches(t *testing.T) {
	workspace, repoDir := setupWorkspaceRepo(t)
	writeFile(t, filepath.Join(repoDir, "notes.txt"), "new branch\n")

	tool := NewGitCommitTool(workspace, "", "", "chuck")
	res := tool.Execute(map[string]interface{}{
		"repo":    "project",
		"message": "test commit",
		"branch":  "feat/add-docs",
	})
	if res.Error != "" {
		t.Fatalf("git-commit failed: %s", res.Error)
	}

	assertCurrentBranch(t, repoDir, "chuck/feat-add-docs")
	assertRemoteBranch(t, repoDir, "chuck/feat-add-docs")
}

// TestGitCommitToolNoChangesCoaches pins the M6/M2 fix: when nothing is staged,
// git-commit must not read as a completed commit. It returns the coaching prefix
// the agent loop keys off (a wobble signal / re-anchor) with recovery steps that
// name the repo dir — the P2.0 baseline showed the model misreading the old bare
// "Nothing to commit" as done and stopping before the PR.
func TestGitCommitToolNoChangesCoaches(t *testing.T) {
	workspace, _ := setupWorkspaceRepo(t)
	// No file written — the working tree is clean.
	tool := NewGitCommitTool(workspace, "", "", "chuck")
	res := tool.Execute(map[string]interface{}{
		"repo":    "project",
		"message": "nothing here",
	})
	if !strings.HasPrefix(strings.TrimSpace(res.Output), GitCommitNoChangesPrefix) {
		t.Fatalf("expected output to start with %q, got: %q (err %q)", GitCommitNoChangesPrefix, res.Output, res.Error)
	}
	if !strings.Contains(res.Output, "project") {
		t.Errorf("coaching should name the repo dir 'project': %q", res.Output)
	}
	if !strings.Contains(strings.ToLower(res.Output), "not") {
		t.Errorf("coaching should make clear this is NOT done: %q", res.Output)
	}
}

func TestGitCommitToolKeepsExistingRemoteBranchName(t *testing.T) {
	workspace, repoDir := setupWorkspaceRepo(t)
	createRemoteBranch(t, repoDir, "feat/existing")
	writeFile(t, filepath.Join(repoDir, "notes.txt"), "existing remote branch\n")

	tool := NewGitCommitTool(workspace, "", "", "chuck")
	res := tool.Execute(map[string]interface{}{
		"repo":    "project",
		"message": "test commit",
		"branch":  "feat/existing",
	})
	if res.Error != "" {
		t.Fatalf("git-commit failed: %s", res.Error)
	}

	assertCurrentBranch(t, repoDir, "feat/existing")
	assertRemoteBranch(t, repoDir, "feat/existing")
}

// TestGitCommitToolKeepsFileOriginWhenNoHost pins the sandbox regression: a
// token present (e.g. resolved from /vault/secrets) but an empty GiteaURL must
// NOT rewrite a working file:// origin to a hostless "http:///owner/repo.git",
// which fails the push. This failed the v2 sandbox gate while passing on hosts
// with no ambient token.
func TestGitCommitToolKeepsFileOriginWhenNoHost(t *testing.T) {
	workspace, repoDir := setupWorkspaceRepo(t)
	writeFile(t, filepath.Join(repoDir, "notes.txt"), "no host\n")

	// Token set, GiteaURL empty — the exact sandbox condition.
	tool := NewGitCommitTool(workspace, "", "ambient-token-value", "chuck")
	res := tool.Execute(map[string]interface{}{
		"repo":    "project",
		"message": "test commit",
		"branch":  "feat/add-docs",
	})
	if res.Error != "" {
		t.Fatalf("git-commit clobbered the file origin: %s (%s)", res.Error, res.Output)
	}
	assertRemoteBranch(t, repoDir, "chuck/feat-add-docs")
}

func TestGitCloneToolAppendsRepoContextFromSeidr(t *testing.T) {
	t.Parallel()

	type queryRequest struct {
		Query       string   `json:"query"`
		Agent       string   `json:"agent"`
		Limit       int      `json:"limit"`
		Collections []string `json:"collections"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/query" {
			http.NotFound(w, r)
			return
		}
		var req queryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode query request: %v", err)
		}
		if req.Query != "kit/hirdforge" {
			t.Fatalf("query = %q", req.Query)
		}
		if req.Agent != "ragnar" {
			t.Fatalf("agent = %q", req.Agent)
		}
		if req.Limit != 5 {
			t.Fatalf("limit = %d", req.Limit)
		}
		gotCollections := strings.Join(req.Collections, ",")
		if gotCollections != "ragnar,warband_shared" {
			t.Fatalf("collections = %q", gotCollections)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{"content": "shared clone lesson", "similarity": 0.91},
				{"content": "ignored", "similarity": 0.41},
				{"content": "agent-specific note", "similarity": 0.73},
			},
		})
	}))
	defer server.Close()

	tool := &GitCloneTool{
		WorkDir:   t.TempDir(),
		GiteaURL:  "http://gitea.example.com",
		Token:     "",
		AgentName: "ragnar",
		MemoryURL: server.URL,
	}

	out := tool.appendRepoContext("kit/hirdforge", "cloned kit/hirdforge to /workspace/hirdforge")
	if !strings.Contains(out, "[REPO CONTEXT] Relevant knowledge about kit/hirdforge:") {
		t.Fatalf("missing repo context section: %q", out)
	}
	if !strings.Contains(out, "- shared clone lesson") {
		t.Fatalf("missing shared lesson: %q", out)
	}
	if !strings.Contains(out, "- agent-specific note") {
		t.Fatalf("missing agent-specific note: %q", out)
	}
	if strings.Contains(out, "ignored") {
		t.Fatalf("low-similarity result should have been filtered: %q", out)
	}
}

func setupWorkspaceRepo(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	originDir := filepath.Join(root, "origin.git")
	seedDir := filepath.Join(root, "seed")
	workspace := filepath.Join(root, "workspace")
	repoDir := filepath.Join(workspace, "project")

	mkdir(t, workspace)
	runGitCmd(t, root, "git", "init", "--bare", originDir)
	runGitCmd(t, root, "git", "init", seedDir)
	runGitCmd(t, seedDir, "git", "config", "user.email", "tester@example.com")
	runGitCmd(t, seedDir, "git", "config", "user.name", "Tester")
	runGitCmd(t, seedDir, "git", "checkout", "-b", "main")
	writeFile(t, filepath.Join(seedDir, "README.md"), "seed\n")
	runGitCmd(t, seedDir, "git", "add", "README.md")
	runGitCmd(t, seedDir, "git", "commit", "-m", "seed")
	runGitCmd(t, seedDir, "git", "remote", "add", "origin", originDir)
	runGitCmd(t, seedDir, "git", "push", "-u", "origin", "main")
	runGitCmd(t, originDir, "git", "symbolic-ref", "HEAD", "refs/heads/main")
	runGitCmd(t, workspace, "git", "clone", originDir, repoDir)
	runGitCmd(t, repoDir, "git", "config", "user.email", "tester@example.com")
	runGitCmd(t, repoDir, "git", "config", "user.name", "Tester")
	runGitCmd(t, repoDir, "git", "checkout", "main")
	return workspace, repoDir
}

func createRemoteBranch(t *testing.T, repoDir, branch string) {
	t.Helper()

	runGitCmd(t, repoDir, "git", "checkout", "-b", branch)
	writeFile(t, filepath.Join(repoDir, "remote.txt"), branch+"\n")
	runGitCmd(t, repoDir, "git", "add", "remote.txt")
	runGitCmd(t, repoDir, "git", "commit", "-m", "create remote branch")
	runGitCmd(t, repoDir, "git", "push", "-u", "origin", branch)
	runGitCmd(t, repoDir, "git", "checkout", "main")
	runGitCmd(t, repoDir, "git", "branch", "-D", branch)
}

func assertCurrentBranch(t *testing.T, repoDir, want string) {
	t.Helper()
	got := strings.TrimSpace(runGitCmd(t, repoDir, "git", "rev-parse", "--abbrev-ref", "HEAD"))
	if got != want {
		t.Fatalf("current branch = %q, want %q", got, want)
	}
}

func assertRemoteBranch(t *testing.T, repoDir, branch string) {
	t.Helper()
	out := strings.TrimSpace(runGitCmd(t, repoDir, "git", "ls-remote", "--heads", "origin", branch))
	if out == "" {
		t.Fatalf("remote branch %q not found", branch)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runGitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed in %s: %v\n%s", strings.Join(args, " "), dir, err, string(out))
	}
	return string(out)
}

// TestGitCommitToolNoBranchNamesTheWorkBranch pins the P3.0b fix: with no branch
// arg, git-commit pushes the checked-out work branch and NAMES it in the output
// ("Pushed to <branch>", not "current branch") so create-pr's head is
// unambiguous. The vague "current branch" led the model to invent a wrong branch
// the collect step could not find (no_pr).
func TestGitCommitToolNoBranchNamesTheWorkBranch(t *testing.T) {
	workspace, repoDir := setupWorkspaceRepo(t)
	// Put the checkout on a work branch, as the sandbox guard does.
	runGitCmd(t, repoDir, "git", "checkout", "-b", "agent/hf-work-123")
	writeFile(t, filepath.Join(repoDir, "notes.txt"), "work branch commit\n")

	tool := NewGitCommitTool(workspace, "", "", "Builder")
	res := tool.Execute(map[string]interface{}{
		"repo":    "project",
		"message": "add notes",
		// no "branch" arg — commit to the current work branch
	})
	if res.Error != "" {
		t.Fatalf("git-commit failed: %s", res.Error)
	}
	if !strings.Contains(res.Output, "Pushed to agent/hf-work-123") {
		t.Fatalf("output must name the actual work branch, got: %q", res.Output)
	}
	if strings.Contains(res.Output, "current branch") {
		t.Fatalf("output must not say the vague 'current branch': %q", res.Output)
	}
	assertRemoteBranch(t, repoDir, "agent/hf-work-123")
}

// TestGitCommitToolIgnoresDivergentBranchOnWorkBranch pins the robust P3.0b fix:
// when the agent is on a work branch (agent/<task>), a divergent branch arg the
// model invented (e.g. a truncated agent/hf-<short>) is IGNORED — git-commit
// pushes the work branch it is on, so the collect step (which keys on the work
// branch) finds the PR. Prompt guidance wasn't enough for a local model.
func TestGitCommitToolIgnoresDivergentBranchOnWorkBranch(t *testing.T) {
	workspace, repoDir := setupWorkspaceRepo(t)
	runGitCmd(t, repoDir, "git", "checkout", "-b", "agent/hf-019f96da655e-197131ebe6")
	writeFile(t, filepath.Join(repoDir, "notes.txt"), "work branch\n")

	tool := NewGitCommitTool(workspace, "", "", "Builder")
	res := tool.Execute(map[string]interface{}{
		"repo":    "project",
		"message": "add notes",
		"branch":  "agent/hf-197131ebe6", // divergent (truncated) — must be ignored
	})
	if res.Error != "" {
		t.Fatalf("git-commit failed: %s", res.Error)
	}
	if !strings.Contains(res.Output, "Pushed to agent/hf-019f96da655e-197131ebe6") {
		t.Fatalf("must push the work branch, not the divergent arg: %q", res.Output)
	}
	assertCurrentBranch(t, repoDir, "agent/hf-019f96da655e-197131ebe6")
	assertRemoteBranch(t, repoDir, "agent/hf-019f96da655e-197131ebe6")
}
