package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathNotFoundSuggestsRealPath(t *testing.T) {
	dir := t.TempDir()
	// The workspace holds benchfixture/metrics.go; the model will ask for the
	// hallucinated bench/builder/fixture/metrics.go.
	if err := os.MkdirAll(filepath.Join(dir, "benchfixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "benchfixture", "metrics.go"), []byte("package x\n"), 0o644)

	res := NewReadTool(dir).Execute(map[string]interface{}{"path": "bench/builder/fixture/metrics.go"})
	if res.Error == "" {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(res.Error, "benchfixture/metrics.go") || !strings.Contains(res.Error, "did you mean") {
		t.Errorf("not-found must suggest the real path, got: %s", res.Error)
	}
}

func TestPathNotFoundListsTopLevelWhenNoMatch(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "benchfixture"), 0o755)
	res := NewReadTool(dir).Execute(map[string]interface{}{"path": "nope.txt"})
	if !strings.Contains(res.Error, "benchfixture/") || !strings.Contains(res.Error, "top level") {
		t.Errorf("no-match should list top-level entries, got: %s", res.Error)
	}
}

func TestEditWrongPathAlsoCoaches(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "benchfixture"), 0o755)
	os.WriteFile(filepath.Join(dir, "benchfixture", "metrics.go"), []byte("package x\n"), 0o644)
	res := NewEditTool(dir).Execute(map[string]interface{}{
		"path": "bench/builder/fixture/metrics.go", "old_str": "x", "new_str": "y",
	})
	if !strings.Contains(res.Error, "benchfixture/metrics.go") {
		t.Errorf("edit on wrong path should coach to the real path, got: %s", res.Error)
	}
}
