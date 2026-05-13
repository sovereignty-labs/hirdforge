package tools

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxFileSize = 1024 * 1024

// atomicWriteFile writes data to a temp file in the same directory as path and
// then renames it over path, so a crash or kill mid-write leaves either the
// previous contents or the new contents — never a truncated file. The temp
// file is removed if the rename fails.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		// Best-effort cleanup if anything below this point bails out.
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func resolvePath(workDir, relPath string) (string, error) {
	if relPath == "" {
		return "", fmt.Errorf("path is required")
	}
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	var absPath string
	if filepath.IsAbs(relPath) {
		absPath = filepath.Clean(relPath)
	} else {
		base := filepath.Base(absWorkDir)
		if strings.HasPrefix(relPath, base+"/") {
			relPath = strings.TrimPrefix(relPath, base+"/")
		}
		absPath, err = filepath.Abs(filepath.Join(workDir, relPath))
		if err != nil {
			return "", err
		}
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
		if os.IsNotExist(err) {
			parentDir := filepath.Dir(path)
			if parentDir == "." || parentDir == "" {
				parentDir = "/workspace"
			}
			filename := filepath.Base(path)
			return ToolResult{Error: fmt.Sprintf("Error: %s not found. Use `exec: ls %s` to see available files, or `exec: find /workspace -name '%s'` to search.", path, parentDir, filename)}
		}
		return ToolResult{Error: err.Error()}
	}
	if info.Size() > maxFileSize {
		return ToolResult{Error: "file exceeds 1MB limit"}
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if bytesLookBinary(data) {
		return ToolResult{Error: fmt.Sprintf("Error: binary file detected at %s. Use `exec: file %s` to check file type, or `exec: xxd %s | head -20` for hex inspection.", path, path, path)}
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
	if err := atomicWriteFile(absPath, []byte(content), 0644); err != nil {
		return ToolResult{Error: err.Error()}
	}
	autoStageWrittenFile(absPath)
	output := fmt.Sprintf("wrote %d bytes to %s", len(content), path)
	lineCount := 0
	if content != "" {
		lineCount = strings.Count(content, "\n") + 1
	}
	if lineCount > 200 {
		output = fmt.Sprintf("Warning: wrote %d lines. For files >200 lines, consider `exec: sed` for targeted edits or `exec: python3 -c '...'` for insertions to avoid corruption risk.\n%s", lineCount, output)
	}
	return ToolResult{Output: output}
}

func (t *WriteTool) Verify(args map[string]interface{}, result ToolResult) error {
	if result.Error != "" {
		return nil
	}

	path, ok := args["path"].(string)
	if !ok || path == "" {
		return fmt.Errorf("write verification failed: path is required")
	}

	absPath, err := resolvePath(t.WorkDir, path)
	if err != nil {
		return fmt.Errorf("write verification failed: %w", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("write verification failed: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("write verification failed: file %s is empty", path)
	}

	return nil
}

// EditTool replaces an exact string match in a workspace file.
type EditTool struct {
	WorkDir string
}

func NewEditTool(workDir string) *EditTool {
	return &EditTool{WorkDir: workDir}
}

func (t *EditTool) Name() string {
	return "edit"
}

func (t *EditTool) Description() string {
	return "Edit a file by replacing an exact string match. old_str must appear exactly once in the file."
}

func (t *EditTool) Parameters() map[string]string {
	return map[string]string{
		"path":    "File path relative to workspace",
		"old_str": "Exact string to replace; must appear exactly once",
		"new_str": "Replacement string; if empty, old_str is deleted",
	}
}

func (t *EditTool) Execute(args map[string]interface{}) ToolResult {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return ToolResult{Error: "path is required"}
	}
	oldStr, ok := args["old_str"].(string)
	if !ok {
		return ToolResult{Error: "old_str is required"}
	}
	newStr, ok := args["new_str"].(string)
	if !ok {
		return ToolResult{Error: "new_str is required"}
	}

	absPath, err := resolvePath(t.WorkDir, path)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return ToolResult{Error: err.Error()}
	}
	if bytesLookBinary(data) {
		return ToolResult{Error: fmt.Sprintf("Error: binary file detected at %s. Use `read` only with text files.", path)}
	}

	content := string(data)
	occurrences := strings.Count(content, oldStr)
	if occurrences == 0 {
		return ToolResult{Error: "old_str not found in file. Use the read tool to check the current content."}
	}
	if occurrences > 1 {
		return ToolResult{Error: fmt.Sprintf("old_str appears %d times. Make it more specific to match exactly once.", occurrences)}
	}

	updated := strings.Replace(content, oldStr, newStr, 1)
	if err := atomicWriteFile(absPath, []byte(updated), 0644); err != nil {
		return ToolResult{Error: err.Error()}
	}
	autoStageWrittenFile(absPath)
	return ToolResult{Output: fmt.Sprintf("Edited %s: replaced %d bytes with %d bytes", path, len(oldStr), len(newStr))}
}

func autoStageWrittenFile(absPath string) {
	repoRoot := findGitRepoRoot(filepath.Dir(absPath))
	if repoRoot == "" {
		return
	}
	relPath, err := filepath.Rel(repoRoot, absPath)
	if err != nil {
		log.Printf("write tool auto-stage: failed to compute relative path for %q: %v", absPath, err)
		return
	}
	cmd := exec.Command("git", "add", relPath)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("write tool auto-stage: git add failed for %q: %v: %s", absPath, err, strings.TrimSpace(string(out)))
	}
}

func findGitRepoRoot(startDir string) string {
	dir := startDir
	for {
		if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func bytesLookBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if !utf8.Valid(data) {
		return true
	}
	checkLen := len(data)
	if checkLen > 4096 {
		checkLen = 4096
	}
	for _, b := range data[:checkLen] {
		if b == 0 {
			return true
		}
	}
	return false
}
