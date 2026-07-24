package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEditToolRescuesWhitespaceFumble is M3's DONE-WHEN: a whitespace-imprecise
// old_str demonstrably completes (instead of failing) through the real edit
// tool, the file's formatting is preserved, and rescue telemetry fires.
func TestEditToolRescuesWhitespaceFumble(t *testing.T) {
	dir := t.TempDir()
	seedFile(t, dir, "f.go", "func f() {\n    x  :=  1\n}\n")
	ClearReadState("s")

	var gotStrategy, gotPath string
	tool := NewEditTool(dir)
	tool.OnRescue = func(strategy, path, sample string) { gotStrategy, gotPath = strategy, path }

	NewReadTool(dir).Execute(map[string]interface{}{"path": "f.go", "_session_id": "s"})
	// old_str collapses the internal double-spaces the model didn't reproduce.
	res := tool.Execute(map[string]interface{}{
		"path": "f.go", "old_str": "    x := 1", "new_str": "    x := 2", "_session_id": "s",
	})
	if res.Error != "" {
		t.Fatalf("whitespace-fumbled edit should be rescued, got error: %s", res.Error)
	}
	if !strings.Contains(res.Output, EditRescueMarker) || !strings.Contains(res.Output, "whitespace") {
		t.Errorf("output should report the rescue: %q", res.Output)
	}
	if gotStrategy != "whitespace" || gotPath != "f.go" {
		t.Errorf("rescue telemetry = (%q,%q), want (whitespace,f.go)", gotStrategy, gotPath)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "f.go"))
	if string(b) != "func f() {\n    x := 2\n}\n" {
		t.Errorf("file content after rescue = %q", string(b))
	}
	ClearReadState("s")
}

// TestEditToolIndentRescueMultiline covers the strategy-3 path through the tool:
// a dedented multiline old_str lands at the file's indentation.
func TestEditToolIndentRescueMultiline(t *testing.T) {
	dir := t.TempDir()
	seedFile(t, dir, "f.go", "func f() {\n        a()\n        b()\n}\n")
	ClearReadState("s")
	tool := NewEditTool(dir)
	NewReadTool(dir).Execute(map[string]interface{}{"path": "f.go", "_session_id": "s"})
	res := tool.Execute(map[string]interface{}{
		"path": "f.go", "old_str": "a()\nb()", "new_str": "a()\nc()", "_session_id": "s",
	})
	if res.Error != "" {
		t.Fatalf("dedented multiline edit should be rescued: %s", res.Error)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "f.go"))
	if string(b) != "func f() {\n        a()\n        c()\n}\n" {
		t.Errorf("indent rescue lost the file's indentation: %q", string(b))
	}
	ClearReadState("s")
}

// TestEditToolAmbiguousCoaches: the no-guess rule at the tool boundary.
func TestEditToolAmbiguousCoaches(t *testing.T) {
	dir := t.TempDir()
	seedFile(t, dir, "f.txt", "dup\ndup\n")
	ClearReadState("s")
	tool := NewEditTool(dir)
	NewReadTool(dir).Execute(map[string]interface{}{"path": "f.txt", "_session_id": "s"})
	res := tool.Execute(map[string]interface{}{
		"path": "f.txt", "old_str": "dup", "new_str": "x", "_session_id": "s",
	})
	if res.Error == "" {
		t.Fatal("ambiguous edit must fail, not guess")
	}
	if !strings.Contains(res.Error, "matches 2 places") || !strings.Contains(res.Error, "surrounding context") {
		t.Errorf("ambiguity should be coached: %s", res.Error)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(b) != "dup\ndup\n" {
		t.Error("file must be untouched on an ambiguous edit")
	}
	ClearReadState("s")
}
