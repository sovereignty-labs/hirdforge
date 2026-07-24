package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectGitWriteCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		// Mutating — must be caught.
		{"git commit -m 'x'", "commit"},
		{"git push origin main", "push"},
		{"git remote set-url origin http://x", "remote"},
		{"git -C benchfixture push origin feat", "push"},
		{"cd benchfixture && git commit -am wip", "commit"},
		{"/usr/bin/git push", "push"},
		{"git --git-dir=/w/.git commit -m y", "commit"},

		// Read-only / harmless — must pass through (fail-open depends on these).
		{"git status", ""},
		{"git -C benchfixture status --porcelain", ""},
		{"git diff HEAD", ""},
		{"git log --oneline -5", ""},
		{"git show HEAD", ""},
		{"git rev-parse --abbrev-ref HEAD", ""},
		{"git add -A", ""},
		{"git checkout -b feat/x", ""},
		{"go test ./...", ""},
		{"ls -la", ""},
	}
	for _, c := range cases {
		if got := detectGitWriteCommand(c.cmd); got != c.want {
			t.Errorf("detectGitWriteCommand(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
}

func TestExecToolRedirectsGitWritesAndNamesRepo(t *testing.T) {
	ws := t.TempDir()
	// One repository present in the workspace.
	if err := os.MkdirAll(filepath.Join(ws, "benchfixture", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := NewExecTool()
	tool.RedirectGitWrites = true
	tool.WorkDir = ws

	res := tool.Execute(map[string]interface{}{"command": "git push origin feat/x"})
	if res.Error != "" {
		t.Fatalf("redirect should not be an error (avoids retry storms): %s", res.Error)
	}
	if !strings.HasPrefix(res.Output, ExecGitRedirectPrefix) {
		t.Fatalf("expected redirect prefix, got: %q", res.Output)
	}
	if !strings.Contains(res.Output, `"benchfixture"`) {
		t.Errorf("redirect should name the repo actually present: %q", res.Output)
	}
	if !strings.Contains(res.Output, "create-pr") {
		t.Errorf("redirect should point at the full delivery path: %q", res.Output)
	}
}

func TestExecToolAllowsReadOnlyGitAndRunsNormally(t *testing.T) {
	tool := NewExecTool()
	tool.RedirectGitWrites = true
	tool.WorkDir = t.TempDir()

	// Read-only git must actually execute (fail-open: the model inspects state).
	res := tool.Execute(map[string]interface{}{"command": "echo status-ok"})
	if strings.HasPrefix(res.Output, ExecGitRedirectPrefix) {
		t.Fatalf("non-mutating command must not be redirected: %q", res.Output)
	}
	if !strings.Contains(res.Output, "status-ok") {
		t.Errorf("expected the command to run: %q", res.Output)
	}
}

func TestExecToolRedirectDisabledByDefault(t *testing.T) {
	// Agents without the git-commit tool (e.g. a read-only reviewer) are
	// unaffected: the policy is opt-in at registration.
	tool := NewExecTool()
	if tool.RedirectGitWrites {
		t.Error("redirect must be off by default")
	}
	res := tool.Execute(map[string]interface{}{"command": "echo hi"})
	if strings.HasPrefix(res.Output, ExecGitRedirectPrefix) {
		t.Error("redirect must not fire when disabled")
	}
}

func TestRepoNotFoundMessageCoachesWithActualRepos(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "benchfixture", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	msg := RepoNotFoundMessage(ws, "bench/builder/fixture")
	if !strings.Contains(msg, "benchfixture") {
		t.Errorf("single-repo coaching must name it: %q", msg)
	}
	if !strings.Contains(msg, "Retry with repo:") {
		t.Errorf("coaching must tell the model exactly what to retry with: %q", msg)
	}

	// Multiple repos → list them.
	if err := os.MkdirAll(filepath.Join(ws, "other", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	multi := RepoNotFoundMessage(ws, "nope")
	if !strings.Contains(multi, "benchfixture") || !strings.Contains(multi, "other") {
		t.Errorf("multi-repo coaching must list all: %q", multi)
	}

	// No repos → say so plainly.
	empty := RepoNotFoundMessage(t.TempDir(), "nope")
	if !strings.Contains(empty, "no git repository") {
		t.Errorf("empty-workspace coaching should say none present: %q", empty)
	}
}

func TestExecHeadTailElide(t *testing.T) {
	data := []byte(strings.Repeat("A", 100) + strings.Repeat("B", 100) + strings.Repeat("Z", 100))
	got := headTailElide(data, 100)
	if !strings.HasPrefix(got, "A") {
		t.Error("head must be retained")
	}
	if !strings.HasSuffix(got, "Z") {
		t.Error("tail must be retained (this is the point — the old head-only cut dropped the exit summary)")
	}
	if !strings.Contains(got, "bytes elided") {
		t.Errorf("elision must be marked: %q", got)
	}
	if len(got) >= len(data) {
		t.Error("elided output should be smaller than the source")
	}
	// Under budget -> unchanged.
	small := []byte("hello")
	if headTailElide(small, 100) != "hello" {
		t.Error("under-budget data must pass through unchanged")
	}
}
