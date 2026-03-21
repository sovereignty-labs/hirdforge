package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const parallelBuildStatePath = "/tmp/parallel-build-state.json"

type ParallelBuildTool struct {
	WorkDir string
}

type parallelBuildState struct {
	IntegrationBranch string              `json:"integration_branch"`
	Ownership         map[string][]string `json:"ownership"`
	UpdatedAt         string              `json:"updated_at"`
}

type parallelMergeResult struct {
	Branch        string   `json:"branch"`
	Status        string   `json:"status"`
	ConflictFiles []string `json:"conflict_files,omitempty"`
	Detail        string   `json:"detail,omitempty"`
}

type parallelBranchStatus struct {
	Branch string `json:"branch"`
	Status string `json:"status"`
	Ahead  int    `json:"ahead"`
}

func NewParallelBuildTool(workDir string) *ParallelBuildTool {
	return &ParallelBuildTool{WorkDir: workDir}
}

func (t *ParallelBuildTool) Name() string { return "parallel-build" }

func (t *ParallelBuildTool) Description() string {
	return "Coordinate parallel agent builds on one feature branch. Actions: plan, open, read-peer, merge, status"
}

func (t *ParallelBuildTool) Parameters() map[string]string {
	return map[string]string{
		"action":             "One of: plan, open, read-peer, merge, status",
		"integration_branch": "Shared integration branch name for the parallel build",
		"ownership":          "JSON object mapping agent_name to a list of owned file paths (plan)",
		"agent_name":         "Agent name creating a working branch (open)",
		"branch":             "Peer branch name to read from (read-peer)",
		"file_path":          "File path to read from a peer branch (read-peer)",
		"agent_branches":     "List of agent branch names (merge, status)",
	}
}

func (t *ParallelBuildTool) Execute(args map[string]interface{}) ToolResult {
	action := strings.TrimSpace(stringValue(args["action"]))
	if action == "" {
		return ToolResult{Error: t.actionUsage("")}
	}

	repoDir, err := t.repoDir()
	if err != nil {
		return ToolResult{Error: err.Error()}
	}

	switch action {
	case "plan":
		return t.plan(repoDir, args)
	case "open":
		return t.open(repoDir, args)
	case "read-peer":
		return t.readPeer(repoDir, args)
	case "merge":
		return t.merge(repoDir, args)
	case "status":
		return t.status(repoDir, args)
	default:
		return ToolResult{Error: t.actionUsage(action)}
	}
}

func (t *ParallelBuildTool) repoDir() (string, error) {
	root, err := runShellCapture(t.WorkDir, "git rev-parse --show-toplevel", 10*time.Second)
	if err != nil || strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("parallel-build requires a git repository in the workspace")
	}
	return strings.TrimSpace(root), nil
}

func (t *ParallelBuildTool) plan(repoDir string, args map[string]interface{}) ToolResult {
	integrationBranch := strings.TrimSpace(stringValue(args["integration_branch"]))
	if integrationBranch == "" {
		return ToolResult{Error: t.actionUsage("plan")}
	}
	ownership, err := parseOwnership(args["ownership"])
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid ownership: %v", err)}
	}

	if res := runGit(repoDir, []string{"git", "checkout", "-b", integrationBranch}, 30*time.Second); res.Error != "" {
		return res
	}
	if res := runGit(repoDir, []string{"git", "push", "origin", integrationBranch}, 60*time.Second); res.Error != "" {
		return res
	}

	state := parallelBuildState{
		IntegrationBranch: integrationBranch,
		Ownership:         ownership,
		UpdatedAt:         time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeParallelBuildState(state); err != nil {
		return ToolResult{Error: err.Error()}
	}
	return marshalOutput(map[string]interface{}{
		"action":             "plan",
		"integration_branch": integrationBranch,
		"ownership":          ownership,
		"state_file":         parallelBuildStatePath,
	})
}

func (t *ParallelBuildTool) open(repoDir string, args map[string]interface{}) ToolResult {
	integrationBranch := strings.TrimSpace(stringValue(args["integration_branch"]))
	agentName := strings.TrimSpace(stringValue(args["agent_name"]))
	if integrationBranch == "" || agentName == "" {
		return ToolResult{Error: t.actionUsage("open")}
	}
	branchName := fmt.Sprintf("%s/%s", agentName, integrationBranch)
	if res := runGit(repoDir, []string{"git", "fetch", "origin", integrationBranch}, 60*time.Second); res.Error != "" {
		return res
	}
	if res := runGit(repoDir, []string{"git", "checkout", "-b", branchName, "origin/" + integrationBranch}, 30*time.Second); res.Error != "" {
		return res
	}
	return marshalOutput(map[string]interface{}{
		"action":             "open",
		"integration_branch": integrationBranch,
		"branch":             branchName,
		"status":             "ready",
	})
}

func (t *ParallelBuildTool) readPeer(repoDir string, args map[string]interface{}) ToolResult {
	branch := strings.TrimSpace(stringValue(args["branch"]))
	filePath := strings.TrimSpace(stringValue(args["file_path"]))
	if branch == "" || filePath == "" {
		return ToolResult{Error: t.actionUsage("read-peer")}
	}
	if res := runGit(repoDir, []string{"git", "fetch", "origin", branch}, 60*time.Second); res.Error != "" {
		return res
	}
	content, err := runShellCapture(repoDir, fmt.Sprintf("git show origin/%s:%s", shellEscape(branch), shellEscape(filePath)), 30*time.Second)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	return ToolResult{Output: content}
}

func (t *ParallelBuildTool) merge(repoDir string, args map[string]interface{}) ToolResult {
	integrationBranch := strings.TrimSpace(stringValue(args["integration_branch"]))
	agentBranches, err := stringList(args["agent_branches"])
	if integrationBranch == "" || err != nil || len(agentBranches) == 0 {
		return ToolResult{Error: t.actionUsage("merge")}
	}

	results := make([]parallelMergeResult, 0, len(agentBranches))
	if res := runGit(repoDir, []string{"git", "checkout", integrationBranch}, 30*time.Second); res.Error != "" {
		return res
	}
	if res := runGit(repoDir, []string{"git", "pull", "origin", integrationBranch}, 60*time.Second); res.Error != "" {
		return res
	}

	for _, branch := range agentBranches {
		branch = strings.TrimSpace(branch)
		if branch == "" {
			continue
		}
		if res := runGit(repoDir, []string{"git", "fetch", "origin", branch}, 60*time.Second); res.Error != "" {
			results = append(results, parallelMergeResult{Branch: branch, Status: "error", Detail: res.Error})
			continue
		}
		res := runGit(repoDir, []string{"git", "merge", "origin/" + branch, "--no-edit"}, 60*time.Second)
		if res.Error == "" {
			results = append(results, parallelMergeResult{Branch: branch, Status: "merged"})
			continue
		}
		conflicts := unmergedFiles(repoDir)
		abortRes := runGit(repoDir, []string{"git", "merge", "--abort"}, 30*time.Second)
		detail := res.Error
		if abortRes.Error != "" {
			detail = strings.TrimSpace(detail + "; merge abort failed: " + abortRes.Error)
		}
		results = append(results, parallelMergeResult{
			Branch:        branch,
			Status:        "conflict",
			ConflictFiles: conflicts,
			Detail:        detail,
		})
	}

	pushRes := runGit(repoDir, []string{"git", "push", "origin", integrationBranch}, 60*time.Second)
	output := map[string]interface{}{
		"action":             "merge",
		"integration_branch": integrationBranch,
		"results":            results,
	}
	if pushRes.Error != "" {
		output["push_error"] = pushRes.Error
	}
	return marshalOutput(output)
}

func (t *ParallelBuildTool) status(repoDir string, args map[string]interface{}) ToolResult {
	integrationBranch := strings.TrimSpace(stringValue(args["integration_branch"]))
	agentBranches, err := stringList(args["agent_branches"])
	if integrationBranch == "" || err != nil || len(agentBranches) == 0 {
		return ToolResult{Error: t.actionUsage("status")}
	}

	if res := runGit(repoDir, []string{"git", "fetch", "origin", integrationBranch}, 60*time.Second); res.Error != "" {
		return res
	}

	statuses := make([]parallelBranchStatus, 0, len(agentBranches))
	for _, branch := range agentBranches {
		branch = strings.TrimSpace(branch)
		if branch == "" {
			continue
		}
		refCheck := runGit(repoDir, []string{"git", "ls-remote", "--heads", "origin", branch}, 30*time.Second)
		if refCheck.Error != "" || strings.TrimSpace(refCheck.Output) == "" {
			statuses = append(statuses, parallelBranchStatus{Branch: branch, Status: "not_started", Ahead: 0})
			continue
		}
		if res := runGit(repoDir, []string{"git", "fetch", "origin", branch}, 60*time.Second); res.Error != "" {
			statuses = append(statuses, parallelBranchStatus{Branch: branch, Status: "in_progress", Ahead: 0})
			continue
		}
		aheadRaw, err := runShellCapture(repoDir, fmt.Sprintf("git rev-list --count origin/%s..origin/%s", shellEscape(integrationBranch), shellEscape(branch)), 30*time.Second)
		if err != nil {
			statuses = append(statuses, parallelBranchStatus{Branch: branch, Status: "in_progress", Ahead: 0})
			continue
		}
		ahead := 0
		fmt.Sscanf(strings.TrimSpace(aheadRaw), "%d", &ahead)
		state := "in_progress"
		if ahead > 0 {
			state = "ready"
		}
		statuses = append(statuses, parallelBranchStatus{Branch: branch, Status: state, Ahead: ahead})
	}

	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Branch < statuses[j].Branch })
	return marshalOutput(map[string]interface{}{
		"action":             "status",
		"integration_branch": integrationBranch,
		"branches":           statuses,
	})
}

func parseOwnership(v interface{}) (map[string][]string, error) {
	raw := map[string]interface{}{}
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) == "" {
			return nil, fmt.Errorf("ownership is required")
		}
		if err := json.Unmarshal([]byte(x), &raw); err != nil {
			return nil, err
		}
	case map[string]interface{}:
		raw = x
	default:
		return nil, fmt.Errorf("ownership must be a JSON object")
	}

	out := make(map[string][]string, len(raw))
	for agent, filesRaw := range raw {
		files, err := stringList(filesRaw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", agent, err)
		}
		out[strings.TrimSpace(agent)] = files
	}
	return out, nil
}

func stringList(v interface{}) ([]string, error) {
	switch x := v.(type) {
	case []string:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out, nil
	case []interface{}:
		out := make([]string, 0, len(x))
		for _, item := range x {
			s := strings.TrimSpace(fmt.Sprint(item))
			if s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return nil, nil
		}
		var out []string
		if strings.HasPrefix(s, "[") {
			if err := json.Unmarshal([]byte(s), &out); err != nil {
				return nil, err
			}
			return out, nil
		}
		return []string{s}, nil
	default:
		return nil, fmt.Errorf("expected list of strings")
	}
}

func stringValue(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func writeParallelBuildState(state parallelBuildState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(parallelBuildStatePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(parallelBuildStatePath, data, 0o644)
}

func unmergedFiles(repoDir string) []string {
	out, err := runShellCapture(repoDir, "git diff --name-only --diff-filter=U", 10*time.Second)
	if err != nil || strings.TrimSpace(out) == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	files := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	sort.Strings(files)
	return files
}

func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func marshalOutput(v interface{}) ToolResult {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	return ToolResult{Output: string(data)}
}

func (t *ParallelBuildTool) actionUsage(action string) string {
	switch action {
	case "plan":
		return `parallel-build plan — required params:
integration_branch   Shared branch name to create from current HEAD
ownership            JSON object mapping agent_name to file-path arrays
Example: parallel-build action=plan integration_branch=feature/search ownership='{"chuck":["cmd/gateway/main.go"],"leif":["pkg/tools/parallel_build.go"]}'`
	case "open":
		return `parallel-build open — required params:
integration_branch   Shared integration branch name
agent_name           Agent creating its working branch
Example: parallel-build action=open integration_branch=feature/search agent_name=chuck`
	case "read-peer":
		return `parallel-build read-peer — required params:
branch      Remote branch name to read from
file_path   Path to file on that branch
Example: parallel-build action=read-peer branch=leif/feature/search file_path=pkg/tools/parallel_build.go`
	case "merge":
		return `parallel-build merge — required params:
integration_branch   Shared integration branch name
agent_branches       JSON array of branch names to merge
Example: parallel-build action=merge integration_branch=feature/search agent_branches='["chuck/feature/search","leif/feature/search"]'`
	case "status":
		return `parallel-build status — required params:
integration_branch   Shared integration branch name
agent_branches       JSON array of branch names to inspect
Example: parallel-build action=status integration_branch=feature/search agent_branches='["chuck/feature/search","leif/feature/search"]'`
	default:
		return `parallel-build — available actions:
plan        Create the shared integration branch and persist file ownership
open        Create an agent-specific working branch from the integration branch
read-peer   Read one file from another agent branch without switching branches
merge       Merge agent branches back into the integration branch
status      Report whether agent branches are not_started, in_progress, or ready
Usage: parallel-build action={plan|open|read-peer|merge|status} ...`
	}
}
