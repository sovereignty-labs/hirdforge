package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ExecGitRedirectPrefix marks an exec result that was refused because the model
// reached for shell git to MUTATE a repository. The agent loop keys off this
// prefix to treat the redirect as a wobble signal (M6 re-anchor).
const ExecGitRedirectPrefix = "Not run: use the git-commit tool"

// gitWriteSubcommands are the git subcommands that mutate repository or remote
// state. These must go through the structured git-commit tool, which verifies
// the branch actually reached origin (ground truth over self-assessment) and
// applies the sandbox credential setup. Read-only git — status, diff, log,
// show, rev-parse, branch, add, checkout — stays available through exec, because
// inspecting repository state is exactly how a builder deduces its way out of
// trouble (fail open).
var gitWriteSubcommands = map[string]bool{
	"commit": true,
	"push":   true,
	"remote": true,
}

// gitGlobalFlagsWithValue are `git` global flags that consume the next token, so
// the subcommand scanner can skip past them (e.g. `git -C repo push`).
var gitGlobalFlagsWithValue = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
}

// detectGitWriteCommand returns the mutating git subcommand in cmd, or "".
// It walks each `git` occurrence (handling absolute paths and global flags) and
// inspects the first non-flag token after it.
func detectGitWriteCommand(cmd string) string {
	fields := strings.Fields(cmd)
	for i := 0; i < len(fields); i++ {
		base := fields[i]
		if idx := strings.LastIndex(base, "/"); idx >= 0 {
			base = base[idx+1:]
		}
		if base != "git" {
			continue
		}
		for j := i + 1; j < len(fields); j++ {
			tok := fields[j]
			if strings.HasPrefix(tok, "-") {
				if gitGlobalFlagsWithValue[tok] {
					j++
				}
				continue
			}
			if gitWriteSubcommands[tok] {
				return tok
			}
			break // first non-flag token is the subcommand; not a write op
		}
	}
	return ""
}

// ExecTool executes shell commands.
type ExecTool struct {
	Timeout   time.Duration
	MaxOutput int

	// RedirectGitWrites refuses mutating shell git and coaches toward the
	// git-commit tool. Set only when that tool is actually registered for this
	// agent, so agents without it (e.g. a read-only reviewer) are unaffected.
	RedirectGitWrites bool
	// WorkDir is the agent workspace, used to name the repositories that are
	// actually present when coaching.
	WorkDir string
}

// defaultExecMaxOutput bounds exec output so a single command cannot flood the
// context (BUILDER_HARNESS §M4). Retained head+tail, so the model still sees the
// command echo / early errors AND the final lines / exit summary. Config, not a
// constant — tunable from telemetry.
const defaultExecMaxOutput = 30 * 1024

// NewExecTool creates an ExecTool with safe defaults.
func NewExecTool() *ExecTool {
	return &ExecTool{
		Timeout:   30 * time.Second,
		MaxOutput: defaultExecMaxOutput,
	}
}

// headTailElide keeps the first ~60% and last ~40% of the budget, with an
// explicit marker naming how many bytes were dropped from the middle — so the
// start (what ran, early errors) and the end (final lines, failures) both
// survive instead of the tail being cut off entirely.
func headTailElide(data []byte, budget int) string {
	if len(data) <= budget || budget <= 0 {
		return string(data)
	}
	head := budget * 6 / 10
	tail := budget - head
	elided := len(data) - head - tail
	return string(data[:head]) +
		fmt.Sprintf("\n\n...[%d bytes elided — head+tail kept; re-run a narrower command for the middle]...\n\n", elided) +
		string(data[len(data)-tail:])
}

// gitRedirectMessage coaches the model onto the verified path, naming the repos
// that actually exist so a wrong repo guess is corrected in the same breath.
func (t *ExecTool) gitRedirectMessage(sub string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s for `git %s`.\n\n", ExecGitRedirectPrefix, sub)
	b.WriteString("Shell git bypasses the harness's push verification (which confirms the branch actually reached origin) and the sandbox credential setup, so it will not reliably work here.\n\n")
	if repos := workspaceRepoDirs(t.WorkDir); len(repos) == 1 {
		fmt.Fprintf(&b, "Do this instead: call the `git-commit` tool with repo: %q, a message, and a branch. ", repos[0])
	} else if len(repos) > 1 {
		fmt.Fprintf(&b, "Do this instead: call the `git-commit` tool with one of these repos: %s, a message, and a branch. ", strings.Join(quoteAll(repos), ", "))
	} else {
		b.WriteString("Do this instead: call the `git-commit` tool with the repo directory, a message, and a branch. ")
	}
	b.WriteString("Then read its output for the pushed branch name and pass that exact name as `create-pr`'s `head`.\n\n")
	b.WriteString("Read-only git (status, diff, log, show, rev-parse) still works through exec — use it to inspect state.")
	return b.String()
}

func (t *ExecTool) Name() string {
	return "exec"
}

func (t *ExecTool) Description() string {
	return "Execute a shell command and return stdout/stderr"
}

func (t *ExecTool) Parameters() map[string]string {
	return map[string]string{
		"command": "The shell command to execute",
	}
}

func (t *ExecTool) Execute(args map[string]interface{}) ToolResult {
	command, ok := args["command"].(string)
	if !ok || command == "" {
		return ToolResult{Error: "command is required"}
	}

	// Coached redirect, not a silent block: the command is reported as not run,
	// with the correct tool call spelled out. Returned as Output (not Error) so
	// it is not retried three times; the loop treats the prefix as a wobble.
	if t.RedirectGitWrites {
		if sub := detectGitWriteCommand(command); sub != "" {
			return ToolResult{Output: t.gitRedirectMessage(sub)}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), t.Timeout)
	defer cancel()

	start := time.Now()
	out, err := exec.CommandContext(ctx, "sh", "-c", command).CombinedOutput()
	duration := time.Since(start)
	byteCount := len(out)
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}
	if ctx.Err() == context.DeadlineExceeded && exitCode < 0 {
		exitCode = 124
	}

	output := headTailElide(out, t.MaxOutput)
	footer := fmt.Sprintf("[exit:%d | %.1fs | %d bytes]", exitCode, duration.Seconds(), byteCount)
	output = output + "\n" + footer

	if ctx.Err() == context.DeadlineExceeded {
		return ToolResult{
			Output: output,
			Error:  fmt.Sprintf("command timed out after %s", t.Timeout),
		}
	}
	if err != nil {
		return ToolResult{
			Output: output,
			Error:  strings.TrimSpace(err.Error()),
		}
	}
	return ToolResult{Output: output}
}
