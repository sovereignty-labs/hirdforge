package tools

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type FileReadEvent struct {
	Type   string `json:"type"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
}

type FileWriteEvent struct {
	Type      string `json:"type"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	Path      string `json:"path"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	Status    string `json:"status"`
}

type GitCloneEvent struct {
	Type       string `json:"type"`
	Repo       string `json:"repo"`
	Branch     string `json:"branch"`
	TargetPath string `json:"target_path"`
}

type GitBranchCreateEvent struct {
	Type       string `json:"type"`
	Repo       string `json:"repo"`
	BranchFrom string `json:"branch_from"`
	BranchNew  string `json:"branch_new"`
}

type GitCommitEvent struct {
	Type         string `json:"type"`
	Repo         string `json:"repo"`
	Branch       string `json:"branch"`
	Message      string `json:"message"`
	FilesChanged int    `json:"files_changed"`
}

type GitPushEvent struct {
	Type   string `json:"type"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Remote string `json:"remote"`
}

type PRCreateEvent struct {
	Type       string `json:"type"`
	Repo       string `json:"repo"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	BranchFrom string `json:"branch_from"`
	BranchTo   string `json:"branch_to"`
}

type ExecEvent struct {
	Type       string `json:"type"`
	Command    string `json:"command"`
	WorkingDir string `json:"working_dir"`
	ExitCode   int    `json:"exit_code"`
}

type HTTPRequestEvent struct {
	Type       string `json:"type"`
	Method     string `json:"method"`
	URL        string `json:"url"`
	StatusCode int    `json:"status_code"`
}

type PlanEvent struct {
	Type  string   `json:"type"`
	Steps []string `json:"steps"`
}

type PlanStepCompleteEvent struct {
	Type string `json:"type"`
	Step int    `json:"step"`
}

type DelegateEvent struct {
	Type             string `json:"type"`
	FromAgent        string `json:"from_agent"`
	ToAgent          string `json:"to_agent"`
	SessionID        string `json:"session_id"`
	ObjectiveSummary string `json:"objective_summary"`
	TargetRepo       string `json:"target_repo"`
}

type ToolEventContext struct {
	GitBranchFrom string
	GitBranchNew  string
	AgentName     string
}

func sessionIDArg(args map[string]interface{}) string {
	if sessionID := stringArg(args, "_session_id"); sessionID != "" {
		return sessionID
	}
	return stringArg(args, "_task_id")
}

func CaptureTypedToolEventContext(toolName string, args map[string]interface{}, workDir, agentName string) ToolEventContext {
	ctx := ToolEventContext{AgentName: strings.TrimSpace(agentName)}
	if toolName != "git-commit" {
		return ctx
	}
	repo := stringArg(args, "repo")
	branch := stringArg(args, "branch")
	if repo == "" {
		return ctx
	}
	repoDir := filepath.Join(workDir, repo)
	ctx.GitBranchFrom = currentGitBranch(repoDir)
	ctx.GitBranchNew = formatNewBranchName(agentName, branch)
	return ctx
}

func TypedToolStartEvents(toolName string, args map[string]interface{}, workDir string, ctx ToolEventContext) []interface{} {
	switch toolName {
	case "write", "edit":
		path := stringArg(args, "path")
		if path == "" {
			return nil
		}
		repo, branch := fileGitContext(workDir, path)
		lineStart, lineEnd := fileWriteLines(toolName, args)
		return []interface{}{FileWriteEvent{
			Type:      "file_write",
			Repo:      repo,
			Branch:    branch,
			Path:      path,
			LineStart: lineStart,
			LineEnd:   lineEnd,
			Status:    "writing",
		}}
	case "plan":
		steps, err := parsePlanSteps(args["steps"])
		if err != nil {
			return nil
		}
		return []interface{}{PlanEvent{
			Type:  "plan",
			Steps: steps,
		}}
	case "plan-step-complete":
		step := intArg(args, "step")
		if step < 0 {
			return nil
		}
		return []interface{}{PlanStepCompleteEvent{
			Type: "plan_step_complete",
			Step: step,
		}}
	case "delegate":
		toAgent := stringArg(args, "agent")
		task := stringArg(args, "task")
		if toAgent == "" || task == "" {
			return nil
		}
		return []interface{}{DelegateEvent{
			Type:             "delegate",
			FromAgent:        ctx.AgentName,
			ToAgent:          toAgent,
			SessionID:        sessionIDArg(args),
			ObjectiveSummary: summarizeDelegateObjective(task),
			TargetRepo:       extractRepoRef(task),
		}}
	default:
		return nil
	}
}

func TypedToolResultEvents(toolName string, args map[string]interface{}, result ToolResult, workDir string, ctx ToolEventContext) []interface{} {
	if result.Error != "" && toolName != "exec" && toolName != "http" {
		return nil
	}

	switch toolName {
	case "read":
		path := stringArg(args, "path")
		if path == "" {
			return nil
		}
		repo, branch := fileGitContext(workDir, path)
		return []interface{}{FileReadEvent{
			Type:   "file_read",
			Repo:   repo,
			Branch: branch,
			Path:   path,
			Bytes:  len([]byte(result.Output)),
		}}
	case "write", "edit":
		path := stringArg(args, "path")
		if path == "" {
			return nil
		}
		repo, branch := fileGitContext(workDir, path)
		lineStart, lineEnd := fileWriteLines(toolName, args)
		return []interface{}{FileWriteEvent{
			Type:      "file_write",
			Repo:      repo,
			Branch:    branch,
			Path:      path,
			LineStart: lineStart,
			LineEnd:   lineEnd,
			Status:    "complete",
		}}
	case "git-clone":
		repo := stringArg(args, "repo")
		if repo == "" {
			return nil
		}
		return []interface{}{GitCloneEvent{
			Type:       "git_clone",
			Repo:       repo,
			Branch:     stringArg(args, "branch"),
			TargetPath: filepath.Join(workDir, repoDirName(repo)),
		}}
	case "git-commit":
		if strings.HasPrefix(strings.TrimSpace(result.Output), "Nothing to commit.") {
			return nil
		}
		repo := stringArg(args, "repo")
		message := stringArg(args, "message")
		if repo == "" || message == "" {
			return nil
		}
		branch := ctx.GitBranchNew
		if branch == "" {
			branch = ctx.GitBranchFrom
		}
		events := []interface{}{}
		if ctx.GitBranchNew != "" {
			events = append(events, GitBranchCreateEvent{
				Type:       "git_branch_create",
				Repo:       repo,
				BranchFrom: ctx.GitBranchFrom,
				BranchNew:  ctx.GitBranchNew,
			})
		}
		events = append(events,
			GitCommitEvent{
				Type:         "git_commit",
				Repo:         repo,
				Branch:       branch,
				Message:      message,
				FilesChanged: parseFilesChanged(result.Output),
			},
			GitPushEvent{
				Type:   "git_push",
				Repo:   repo,
				Branch: branch,
				Remote: "origin",
			},
		)
		return events
	case "create-pr":
		repo := stringArg(args, "repo")
		title := stringArg(args, "title")
		head := stringArg(args, "head")
		base := stringArg(args, "base")
		if base == "" {
			base = "main"
		}
		if repo == "" || title == "" || head == "" {
			return nil
		}
		return []interface{}{PRCreateEvent{
			Type:       "pr_create",
			Repo:       repo,
			Number:     parsePRNumber(result.Output),
			Title:      title,
			BranchFrom: head,
			BranchTo:   base,
		}}
	case "exec":
		command := stringArg(args, "command")
		if command == "" {
			return nil
		}
		return []interface{}{ExecEvent{
			Type:       "exec",
			Command:    command,
			WorkingDir: workDir,
			ExitCode:   parseExitCode(result.Output, result.Error),
		}}
	case "http":
		url := stringArg(args, "url")
		if url == "" {
			return nil
		}
		method := strings.ToUpper(stringArg(args, "method"))
		if method == "" {
			method = "GET"
		}
		return []interface{}{HTTPRequestEvent{
			Type:       "http_request",
			Method:     method,
			URL:        url,
			StatusCode: parseHTTPStatusCode(result.Output),
		}}
	default:
		return nil
	}
}

func stringArg(args map[string]interface{}, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	return stringFromAny(v)
}

// intArg returns -1 when the value is missing or cannot be parsed.
// That sentinel is intentional here because step 0 is valid for plan-step-complete.
// This differs from workspace.intPayload, which uses 0 as its generic missing-value fallback.
func intArg(args map[string]interface{}, key string) int {
	v, ok := args[key]
	if !ok || v == nil {
		return -1
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	}
	var out int
	if _, err := fmt.Sscanf(strings.TrimSpace(fmt.Sprint(v)), "%d", &out); err == nil {
		return out
	}
	return -1
}

func stringFromAny(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	value := strings.TrimSpace(fmt.Sprint(v))
	if value == "<nil>" {
		return ""
	}
	return value
}

func repoDirName(repo string) string {
	repo = strings.TrimSpace(repo)
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		return repo[i+1:]
	}
	return repo
}

func fileGitContext(workDir, path string) (string, string) {
	absPath := path
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(workDir, path)
	}
	repoRoot := findGitRepoRoot(filepath.Dir(absPath))
	if repoRoot == "" {
		return "", ""
	}
	return filepath.Base(repoRoot), currentGitBranch(repoRoot)
}

func currentGitBranch(repoDir string) string {
	if repoDir == "" {
		return ""
	}
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func fileWriteLines(toolName string, args map[string]interface{}) (int, int) {
	content := stringArg(args, "content")
	if toolName == "edit" {
		content = stringArg(args, "new_str")
	}
	lineEnd := countLines(content)
	lineStart := 0
	if lineEnd > 0 {
		lineStart = 1
	}
	return lineStart, lineEnd
}

func countLines(content string) int {
	if content == "" {
		return 0
	}
	return strings.Count(content, "\n") + 1
}

var filesChangedRE = regexp.MustCompile(`(?m)(\d+) files? changed`)

func parseFilesChanged(output string) int {
	match := filesChangedRE.FindStringSubmatch(output)
	if len(match) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(match[1])
	return n
}

var prNumberRE = regexp.MustCompile(`#(\d+)`)

func parsePRNumber(output string) int {
	match := prNumberRE.FindStringSubmatch(output)
	if len(match) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(match[1])
	return n
}

var exitCodeRE = regexp.MustCompile(`\[exit:(-?\d+)`)

func parseExitCode(output, errText string) int {
	match := exitCodeRE.FindStringSubmatch(output)
	if len(match) >= 2 {
		n, convErr := strconv.Atoi(match[1])
		if convErr == nil {
			return n
		}
	}
	if strings.TrimSpace(errText) != "" {
		return 1
	}
	return 0
}

var httpStatusRE = regexp.MustCompile(`(?m)^HTTP\s+(\d+)`)

func parseHTTPStatusCode(output string) int {
	match := httpStatusRE.FindStringSubmatch(output)
	if len(match) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(match[1])
	return n
}

func summarizeDelegateObjective(task string) string {
	text := strings.TrimSpace(task)
	if text == "" {
		return ""
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "TASK:"))
		line = strings.TrimSpace(strings.TrimPrefix(line, "Task:"))
		if line == "" {
			continue
		}
		if len(line) > 140 {
			return line[:140] + "..."
		}
		return line
	}
	return ""
}

var repoRefRE = regexp.MustCompile(`\b([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)\b`)

func extractRepoRef(task string) string {
	match := repoRefRE.FindStringSubmatch(task)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}
