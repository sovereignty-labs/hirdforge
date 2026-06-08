package tools

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestReadToolNotFoundHintsAtPersonaRepo verifies that read failures on
// persona-shaped paths (warrior/soul.md, sage/shared-skills.txt, shared/...)
// surface a hint pointing at the personas repo. The base "use ls" hint
// should still be present; the persona hint is appended.
func TestReadToolNotFoundHintsAtPersonaRepo(t *testing.T) {
	tmp := t.TempDir()
	tool := NewReadTool(tmp)

	for _, path := range []string{
		"warrior/soul.md",
		"sage/shared-skills.txt",
		"chieftain/playbook.md",
		"elder/tools.md",
		"shared/skills/delegation.md",
	} {
		res := tool.Execute(map[string]interface{}{"path": path})
		if res.Error == "" {
			t.Fatalf("path %q expected not-found error, got output=%q", path, res.Output)
		}
		if !strings.Contains(res.Error, "persona file") {
			t.Errorf("path %q error missing persona hint: %s", path, res.Error)
		}
		if !strings.Contains(res.Error, "git-clone kit/hirdforge-personas") {
			t.Errorf("path %q error missing clone command: %s", path, res.Error)
		}
		if !strings.Contains(res.Error, "hirdforge-personas/"+path) {
			t.Errorf("path %q error doesn't suggest the re-rooted read path: %s", path, res.Error)
		}
	}
}

// TestReadToolNotFoundDoesNotHintForOrdinaryPaths verifies the hint stays
// off for paths that look like ordinary workspace files — we don't want
// every read failure dragging in unrelated persona advice.
func TestReadToolNotFoundDoesNotHintForOrdinaryPaths(t *testing.T) {
	tmp := t.TempDir()
	tool := NewReadTool(tmp)

	for _, path := range []string{
		"main.go",
		"cmd/agent/main.go",
		"README.md",
		"warrior/main.go", // 2 segments but not a known persona filename
		"shared/main.go",  // shared/ but not the persona convention root — still hints (intentional, that's the heuristic)
	} {
		if path == "shared/main.go" {
			// Documented exception: anything under shared/ matches the heuristic.
			continue
		}
		res := tool.Execute(map[string]interface{}{"path": path})
		if res.Error == "" {
			t.Fatalf("path %q expected not-found error, got output=%q", path, res.Output)
		}
		if strings.Contains(res.Error, "persona file") {
			t.Errorf("path %q got unwanted persona hint: %s", path, res.Error)
		}
	}
}

// TestLooksLikePersonaPath unit-tests the predicate directly so the
// behavior is pinned even if the ReadTool error string ever changes.
func TestLooksLikePersonaPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"warrior/soul.md", true},
		{"chieftain/soul.md", true},
		{"sage/shared-skills.txt", true},
		{"elder/playbook.md", true},
		{"steward/tools.md", true},
		{"shared/skills/delegation.md", true},
		{"shared/platform-constants.md", true},
		{"main.go", false},
		{"cmd/agent/main.go", false},
		{"warrior/main.go", false},
		{"warrior/foo/bar.md", false}, // 3-segment, not the convention
		{"", false},
	}
	for _, c := range cases {
		if got := looksLikePersonaPath(c.path); got != c.want {
			t.Errorf("looksLikePersonaPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
	_ = filepath.Separator // keep filepath imported for future test growth
}
