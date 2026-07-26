package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListDirTool(t *testing.T) {
	ws := t.TempDir()
	// a small tree: a subdir, two files, a .git that must be hidden
	if err := os.MkdirAll(filepath.Join(ws, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"go.mod", "README.md"} {
		if err := os.WriteFile(filepath.Join(ws, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tool := NewListDirTool(ws)
	if tool.Name() != "list-dir" {
		t.Fatalf("name = %q", tool.Name())
	}

	// root listing: dirs first (with trailing /), then files; .git hidden.
	res := tool.Execute(map[string]interface{}{"path": "."})
	if res.Error != "" {
		t.Fatalf("list root: %v", res.Error)
	}
	lines := strings.Split(strings.TrimSpace(res.Output), "\n")
	if lines[0] != "cmd/" {
		t.Fatalf("dirs should sort first with a slash; got %q", lines)
	}
	if strings.Contains(res.Output, ".git\n") || strings.Contains(res.Output, ".git/") {
		t.Fatalf(".git must be hidden: %q", res.Output)
	}
	if !strings.Contains(res.Output, "go.mod") || !strings.Contains(res.Output, "README.md") {
		t.Fatalf("files missing: %q", res.Output)
	}

	// empty path defaults to root, not an error.
	if r := tool.Execute(map[string]interface{}{}); r.Error != "" {
		t.Fatalf("empty path should default to root: %v", r.Error)
	}

	// a file is not a directory — clear error steering to read.
	if r := tool.Execute(map[string]interface{}{"path": "go.mod"}); r.Error == "" || !strings.Contains(r.Error, "not a directory") {
		t.Fatalf("file-as-dir should error: %v", r.Error)
	}

	// cannot escape the workspace.
	if r := tool.Execute(map[string]interface{}{"path": "../../etc"}); r.Error == "" {
		t.Fatal("path escape must be refused")
	}
}
