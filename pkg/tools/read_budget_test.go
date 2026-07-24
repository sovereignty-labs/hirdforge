package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLinesFile(t *testing.T, dir, name string, n int) string {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadIsLineNumbered(t *testing.T) {
	dir := t.TempDir()
	writeLinesFile(t, dir, "f.txt", 3)
	res := NewReadTool(dir).Execute(map[string]interface{}{"path": "f.txt"})
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	want := "1: line 1\n2: line 2\n3: line 3"
	if res.Output != want {
		t.Errorf("read output = %q, want %q", res.Output, want)
	}
}

func TestReadCapsAtLineBudgetWithMarker(t *testing.T) {
	dir := t.TempDir()
	writeLinesFile(t, dir, "big.txt", 5000)
	tool := NewReadTool(dir)
	res := tool.Execute(map[string]interface{}{"path": "big.txt"})
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	// Capped at the default 2000-line budget, with an announced truncation.
	if strings.Contains(res.Output, "2001: line 2001") {
		t.Error("output should stop at the line budget")
	}
	if !strings.Contains(res.Output, "of 5000") || !strings.Contains(res.Output, "offset=2001") {
		t.Errorf("truncation must be announced with a continue offset: %q", tail(res.Output))
	}
}

func TestReadOffsetLimitPaging(t *testing.T) {
	dir := t.TempDir()
	writeLinesFile(t, dir, "f.txt", 100)
	tool := NewReadTool(dir)
	res := tool.Execute(map[string]interface{}{"path": "f.txt", "offset": 50, "limit": 3})
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if !strings.HasPrefix(res.Output, "50: line 50\n51: line 51\n52: line 52") {
		t.Errorf("paging should start at offset with absolute numbers: %q", head(res.Output))
	}
	if strings.Contains(res.Output, "53: line 53") {
		t.Error("limit should cap the window")
	}
	if !strings.Contains(res.Output, "offset=53") {
		t.Errorf("should announce the next offset: %q", tail(res.Output))
	}
}

func TestReadOffsetPastEnd(t *testing.T) {
	dir := t.TempDir()
	writeLinesFile(t, dir, "f.txt", 10)
	res := NewReadTool(dir).Execute(map[string]interface{}{"path": "f.txt", "offset": 99})
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if !strings.Contains(res.Output, "past end of file") || !strings.Contains(res.Output, "10 line") {
		t.Errorf("offset past end should say so and name the length: %q", res.Output)
	}
}

func TestReadTruncatesLongLines(t *testing.T) {
	dir := t.TempDir()
	tool := NewReadTool(dir)
	tool.MaxLineLen = 20
	long := strings.Repeat("x", 100)
	if err := os.WriteFile(filepath.Join(dir, "l.txt"), []byte(long+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := tool.Execute(map[string]interface{}{"path": "l.txt"})
	if !strings.Contains(res.Output, "line truncated") || !strings.Contains(res.Output, "80 more chars") {
		t.Errorf("over-long line should be cut with a marker: %q", res.Output)
	}
	if strings.Contains(res.Output, strings.Repeat("x", 100)) {
		t.Error("the full over-long line must not be emitted")
	}
}

func TestReadByteBudgetTruncates(t *testing.T) {
	dir := t.TempDir()
	tool := NewReadTool(dir)
	tool.MaxBytes = 40 // tiny, forces a size-limit stop before the line budget
	writeLinesFile(t, dir, "f.txt", 100)
	res := tool.Execute(map[string]interface{}{"path": "f.txt"})
	if !strings.Contains(res.Output, "size limit") || !strings.Contains(res.Output, "offset=") {
		t.Errorf("byte-budget stop must be announced with an offset: %q", res.Output)
	}
}

func head(s string) string {
	if len(s) > 120 {
		return s[:120]
	}
	return s
}
func tail(s string) string {
	if len(s) > 120 {
		return s[len(s)-120:]
	}
	return s
}
