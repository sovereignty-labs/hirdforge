package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecRunsInWorkspace(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "benchfixture"), 0o755)
	os.WriteFile(filepath.Join(dir, "benchfixture", "metrics.go"), []byte("package x\n"), 0o644)

	tool := NewExecTool()
	tool.WorkDir = dir
	// A relative ls that only succeeds if exec is rooted in the workspace.
	res := tool.Execute(map[string]interface{}{"command": "ls ./benchfixture/"})
	if res.Error != "" {
		t.Fatalf("relative ls in workspace should succeed, got error: %s (out=%q)", res.Error, res.Output)
	}
	if !strings.Contains(res.Output, "metrics.go") {
		t.Errorf("exec should see workspace contents: %q", res.Output)
	}
}

func TestExecNoWorkDirUsesDefaultCwd(t *testing.T) {
	tool := NewExecTool() // WorkDir empty
	res := tool.Execute(map[string]interface{}{"command": "echo ok"})
	if !strings.Contains(res.Output, "ok") {
		t.Errorf("empty WorkDir should still run: %q", res.Output)
	}
}
