package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRepoDir(t *testing.T) {
	// Benchmark layout: repo is a subdir workDir/benchfixture.
	sub := t.TempDir()
	os.MkdirAll(filepath.Join(sub, "benchfixture", ".git"), 0o755)
	if got, ok := resolveRepoDir(sub, "benchfixture"); !ok || got != filepath.Join(sub, "benchfixture") {
		t.Errorf("subdir layout: got %q ok=%v", got, ok)
	}

	// Production layout: the workspace ROOT itself is the repo (/work/repo).
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	// Model guessed a wrong subdir name ("repo") — must fall back to the root.
	if got, ok := resolveRepoDir(root, "repo"); !ok || got != root {
		t.Errorf("root layout with wrong subdir arg: got %q ok=%v, want %q", got, ok, root)
	}
	if got, ok := resolveRepoDir(root, "."); !ok || got != root {
		t.Errorf("root layout with '.': got %q ok=%v", got, ok)
	}
	if got, ok := resolveRepoDir(root, ""); !ok || got != root {
		t.Errorf("root layout with empty: got %q ok=%v", got, ok)
	}

	// No repo anywhere → not found.
	if _, ok := resolveRepoDir(t.TempDir(), "nope"); ok {
		t.Error("no repo present should not resolve")
	}
}
