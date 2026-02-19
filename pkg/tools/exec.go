package tools

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// ExecTool executes shell commands.
type ExecTool struct {
	Timeout   time.Duration
	MaxOutput int
}

// NewExecTool creates an ExecTool with safe defaults.
func NewExecTool() *ExecTool {
	return &ExecTool{
		Timeout:   30 * time.Second,
		MaxOutput: 1048576,
	}
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

	ctx, cancel := context.WithTimeout(context.Background(), t.Timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "sh", "-c", command).CombinedOutput()
	output := string(out)
	if len(out) > t.MaxOutput {
		output = string(out[:t.MaxOutput]) + fmt.Sprintf("\n... output truncated at %d bytes", t.MaxOutput)
	}

	if ctx.Err() == context.DeadlineExceeded {
		return ToolResult{
			Output: output,
			Error:  fmt.Sprintf("command timed out after %s", t.Timeout),
		}
	}
	if err != nil {
		return ToolResult{
			Output: output,
			Error:  err.Error(),
		}
	}
	return ToolResult{Output: output}
}
