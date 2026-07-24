package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, "skills", rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveBundleSkillsOrderAndContent(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "go/testing-conventions.md", "Table-driven tests, please.")
	writeSkill(t, root, "hirdforge/commit-style.md", "Imperative subject line.")

	content, loaded, err := resolveBundleSkills(root, []string{"go/testing-conventions", "hirdforge/commit-style"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded = %v", loaded)
	}
	// Ordered append, each with a labeled header.
	iA := strings.Index(content, "## Skill: go/testing-conventions")
	iB := strings.Index(content, "## Skill: hirdforge/commit-style")
	if iA < 0 || iB < 0 || iA > iB {
		t.Fatalf("skills not appended in order: %q", content)
	}
	if !strings.Contains(content, "Table-driven tests") || !strings.Contains(content, "Imperative subject line") {
		t.Fatalf("skill bodies missing: %q", content)
	}
}

func TestResolveBundleSkillsUnknownIsLoud(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "present.md", "here")
	// One present, one missing → the whole resolution fails loudly (no silent skip).
	if _, _, err := resolveBundleSkills(root, []string{"present", "absent"}); err == nil {
		t.Fatal("an unresolved skill must fail loudly")
	}
}

func TestResolveBundleSkillsEmpty(t *testing.T) {
	content, loaded, err := resolveBundleSkills(t.TempDir(), nil)
	if err != nil || content != "" || loaded != nil {
		t.Fatalf("empty skills should be a clean no-op: %q %v %v", content, loaded, err)
	}
}

func TestSafeSkillPathRejectsEscape(t *testing.T) {
	for _, bad := range []string{"../secret", "a/../../b", "/etc/passwd", "..", ""} {
		if _, err := safeSkillPath(bad); err == nil {
			t.Errorf("safeSkillPath(%q) should be rejected", bad)
		}
	}
	got, err := safeSkillPath("go/testing")
	if err != nil || got != "go/testing.md" {
		t.Fatalf("safeSkillPath(go/testing) = %q, %v", got, err)
	}
}
