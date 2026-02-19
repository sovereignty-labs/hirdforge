package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxFileSize = 1024 * 1024

func resolvePath(workDir, relPath string) (string, error) {
	if relPath == "" {
		return "", fmt.Errorf("path is required")
	}
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	absPath, err := filepath.Abs(filepath.Join(workDir, relPath))
	if err != nil {
		return "", err
	}
	if absPath != absWorkDir && !strings.HasPrefix(absPath, absWorkDir+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes workspace")
	}
	return absPath, nil
}

// ReadTool reads workspace files.
type ReadTool struct {
	WorkDir string
}

func NewReadTool(workDir string) *ReadTool {
	return &ReadTool{WorkDir: workDir}
}

func (t *ReadTool) Name() string {
	return "read"
}

func (t *ReadTool) Description() string {
	return "Read a file from the agent workspace"
}

func (t *ReadTool) Parameters() map[string]string {
	return map[string]string{
		"path": "File path relative to workspace",
	}
}

func (t *ReadTool) Execute(args map[string]interface{}) ToolResult {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return ToolResult{Error: "path is required"}
	}

	absPath, err := resolvePath(t.WorkDir, path)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if info.Size() > maxFileSize {
		return ToolResult{Error: "file exceeds 1MB limit"}
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	return ToolResult{Output: string(data)}
}

// WriteTool writes workspace files.
type WriteTool struct {
	WorkDir string
}

func NewWriteTool(workDir string) *WriteTool {
	return &WriteTool{WorkDir: workDir}
}

func (t *WriteTool) Name() string {
	return "write"
}

func (t *WriteTool) Description() string {
	return "Write content to a file in the agent workspace"
}

func (t *WriteTool) Parameters() map[string]string {
	return map[string]string{
		"path":    "File path relative to workspace",
		"content": "Content to write",
	}
}

func (t *WriteTool) Execute(args map[string]interface{}) ToolResult {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return ToolResult{Error: "path is required"}
	}
	content, ok := args["content"].(string)
	if !ok {
		return ToolResult{Error: "content is required"}
	}

	absPath, err := resolvePath(t.WorkDir, path)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		return ToolResult{Error: err.Error()}
	}
	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		return ToolResult{Error: err.Error()}
	}
	return ToolResult{Output: fmt.Sprintf("wrote %d bytes to %s", len(content), path)}
}
