package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoProfilesDir points at the real config/profiles so the tests pin the
// shipped baseline profiles, not fixtures.
func repoProfilesDir(t *testing.T) string {
	t.Helper()
	// internal/profile → repo root is two levels up.
	return filepath.Clean(filepath.Join("..", "..", "config", "profiles"))
}

func TestLoadDirBaselineProfiles(t *testing.T) {
	profs, err := LoadDir(repoProfilesDir(t))
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	b, ok := profs["builder"]
	if !ok {
		t.Fatal("builder profile missing")
	}
	// The builder profile must reproduce the pre-profile hardcoded dispatch.
	wantTools := "todo,exec,read,write,edit,git-clone,git-commit,git-diff,gitea"
	if got := strings.Join(b.Tools, ","); got != wantTools {
		t.Fatalf("builder tools = %q, want %q", got, wantTools)
	}
	if b.Procedure != "builder" || b.Completion.Requires != "pr" {
		t.Fatalf("builder procedure/completion = %q/%q", b.Procedure, b.Completion.Requires)
	}

	r, ok := profs["reviewer"]
	if !ok {
		t.Fatal("reviewer profile missing")
	}
	if r.Procedure != "reviewer" || r.Completion.Requires != "review" {
		t.Fatalf("reviewer procedure/completion = %q/%q", r.Procedure, r.Completion.Requires)
	}
	for _, tool := range r.Tools {
		if mutatingTools[tool] {
			t.Fatalf("reviewer profile ships a mutating tool %q", tool)
		}
	}
}

func TestBuilderAgentArgsReproduceToday(t *testing.T) {
	profs, err := LoadDir(repoProfilesDir(t))
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	args := profs["builder"].AgentArgs()
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-tools todo,exec,read,write,edit,git-clone,git-commit,git-diff,gitea") {
		t.Fatalf("builder AgentArgs missing today's tool set: %q", joined)
	}
	if !strings.Contains(joined, "-max-tool-rounds 80") {
		t.Fatalf("builder AgentArgs missing rounds: %q", joined)
	}
	if !strings.Contains(joined, "-procedure builder") {
		t.Fatalf("builder AgentArgs missing procedure: %q", joined)
	}
}

func writeProfile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestValidateRejectsReviewerWithMutatingTool(t *testing.T) {
	dir := t.TempDir()
	// A reviewer-intent profile carrying `edit` must be refused loudly.
	p := writeProfile(t, dir, "reviewer.yaml", `profile_version: 1
name: reviewer
tools: [read, edit, gitea-review]
procedure: reviewer
budgets: {max_tool_rounds: 30}
completion: {requires: review}
`)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "mutating tool") {
		t.Fatalf("expected loud refusal of reviewer+edit, got err=%v", err)
	}
}

func TestValidateRejectsBadVersionAndName(t *testing.T) {
	dir := t.TempDir()
	bad := writeProfile(t, dir, "builder.yaml", `profile_version: 2
name: builder
tools: [read]
procedure: builder
budgets: {max_tool_rounds: 80}
`)
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), "profile_version") {
		t.Fatalf("expected version rejection, got %v", err)
	}

	mismatch := writeProfile(t, dir, "wrongname.yaml", `profile_version: 1
name: builder
tools: [read]
procedure: builder
budgets: {max_tool_rounds: 80}
`)
	if _, err := Load(mismatch); err == nil || !strings.Contains(err.Error(), "match filename") {
		t.Fatalf("expected name/filename mismatch rejection, got %v", err)
	}
}

func TestLoadDirEmptyIsError(t *testing.T) {
	if _, err := LoadDir(t.TempDir()); err == nil {
		t.Fatal("empty profiles dir must be a loud error, not a silent empty set")
	}
}
