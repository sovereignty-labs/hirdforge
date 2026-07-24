package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Scenario 2: edit without a prior read is refused with coaching.
func TestEditWithoutReadIsCoached(t *testing.T) {
	dir := t.TempDir()
	seedFile(t, dir, "f.txt", "hello world")
	ClearReadState("s")
	res := NewEditTool(dir).Execute(map[string]interface{}{
		"path": "f.txt", "old_str": "hello", "new_str": "hi", "_session_id": "s",
	})
	if res.Error == "" {
		t.Fatal("edit without read should be refused")
	}
	if !strings.Contains(res.Error, "read-before-edit") || !strings.Contains(res.Error, "have not read") {
		t.Errorf("expected read-before-edit coaching, got: %s", res.Error)
	}
	// The file must be untouched.
	b, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(b) != "hello world" {
		t.Errorf("file must not change on a refused edit, got %q", string(b))
	}
	ClearReadState("s")
}

// Read then edit succeeds.
func TestReadThenEditSucceeds(t *testing.T) {
	dir := t.TempDir()
	seedFile(t, dir, "f.txt", "hello world")
	ClearReadState("s")
	NewReadTool(dir).Execute(map[string]interface{}{"path": "f.txt", "_session_id": "s"})
	res := NewEditTool(dir).Execute(map[string]interface{}{
		"path": "f.txt", "old_str": "hello", "new_str": "hi", "_session_id": "s",
	})
	if res.Error != "" {
		t.Fatalf("read-then-edit should succeed, got: %s", res.Error)
	}
	ClearReadState("s")
}

// Scenario 4: two edits with no interleaved read — the second is refused because
// the first invalidated the read (the multi-edit cascade guard).
func TestSecondEditWithoutRereadIsCoached(t *testing.T) {
	dir := t.TempDir()
	seedFile(t, dir, "f.txt", "hello world")
	ClearReadState("s")
	read, edit := NewReadTool(dir), NewEditTool(dir)
	read.Execute(map[string]interface{}{"path": "f.txt", "_session_id": "s"})
	if res := edit.Execute(map[string]interface{}{"path": "f.txt", "old_str": "hello", "new_str": "hi", "_session_id": "s"}); res.Error != "" {
		t.Fatalf("first edit should succeed: %s", res.Error)
	}
	res := edit.Execute(map[string]interface{}{"path": "f.txt", "old_str": "world", "new_str": "earth", "_session_id": "s"})
	if res.Error == "" {
		t.Fatal("second edit without a re-read must be refused (cascade guard)")
	}
	if !strings.Contains(res.Error, "changed since") {
		t.Errorf("expected a 'read it again' coaching, got: %s", res.Error)
	}
	// Re-reading clears it, and the second edit then works.
	read.Execute(map[string]interface{}{"path": "f.txt", "_session_id": "s"})
	if res := edit.Execute(map[string]interface{}{"path": "f.txt", "old_str": "world", "new_str": "earth", "_session_id": "s"}); res.Error != "" {
		t.Fatalf("edit after re-read should succeed: %s", res.Error)
	}
	ClearReadState("s")
}

// A file changed on disk since it was read is refused until re-read.
func TestEditStaleAfterExternalChange(t *testing.T) {
	dir := t.TempDir()
	seedFile(t, dir, "f.txt", "hello world")
	ClearReadState("s")
	NewReadTool(dir).Execute(map[string]interface{}{"path": "f.txt", "_session_id": "s"})
	seedFile(t, dir, "f.txt", "hello changed world") // external change after read
	res := NewEditTool(dir).Execute(map[string]interface{}{
		"path": "f.txt", "old_str": "hello", "new_str": "hi", "_session_id": "s",
	})
	if res.Error == "" || !strings.Contains(res.Error, "changed since") {
		t.Errorf("stale read should be refused with 'changed since', got: %s", res.Error)
	}
	ClearReadState("s")
}

// Write establishes ground truth, so an immediate edit does not demand a read.
func TestWriteThenEditSucceeds(t *testing.T) {
	dir := t.TempDir()
	ClearReadState("s")
	NewWriteTool(dir).Execute(map[string]interface{}{"path": "f.txt", "content": "hello world", "_session_id": "s"})
	res := NewEditTool(dir).Execute(map[string]interface{}{
		"path": "f.txt", "old_str": "hello", "new_str": "hi", "_session_id": "s",
	})
	if res.Error != "" {
		t.Fatalf("write-then-edit should succeed, got: %s", res.Error)
	}
	ClearReadState("s")
}

// Without a session id, enforcement is disabled entirely (non-session callers).
func TestEditWithoutSessionSkipsEnforcement(t *testing.T) {
	dir := t.TempDir()
	seedFile(t, dir, "f.txt", "hello world")
	res := NewEditTool(dir).Execute(map[string]interface{}{
		"path": "f.txt", "old_str": "hello", "new_str": "hi", // no _session_id
	})
	if res.Error != "" {
		t.Fatalf("no-session edit must behave as before (no read-before-edit): %s", res.Error)
	}
}
