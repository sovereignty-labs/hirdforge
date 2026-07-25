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
	if !strings.Contains(joined, "-completion pr") {
		t.Fatalf("builder AgentArgs missing completion: %q", joined)
	}
	// The reviewer profile must carry review-mode completion.
	rjoined := strings.Join(profs["reviewer"].AgentArgs(), " ")
	if !strings.Contains(rjoined, "-completion review") || !strings.Contains(rjoined, "-procedure reviewer") {
		t.Fatalf("reviewer AgentArgs wrong: %q", rjoined)
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

// The steward (interlocutor) profile is read-only by construction: the
// mutating-tool ban (O-PROFILE §4) must apply to it, and it must ship none.
func TestStewardProfileIsReadOnly(t *testing.T) {
	profs, err := LoadDir(repoProfilesDir(t))
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	s, ok := profs["steward"]
	if !ok {
		t.Fatal("steward profile missing")
	}
	if s.Procedure != "steward" {
		t.Fatalf("steward procedure = %q, want steward", s.Procedure)
	}
	if s.Completion.Requires != "none" {
		t.Fatalf("steward completion = %q, want none", s.Completion.Requires)
	}
	if !s.Policies.ReadOnly {
		t.Fatal("steward profile must set read_only: true")
	}
	if !s.isReadOnlyIntent() {
		t.Fatal("steward profile must be read-only intent (mutating-tool ban applies)")
	}
	for _, tool := range s.Tools {
		if mutatingTools[tool] {
			t.Fatalf("steward profile ships a mutating tool %q", tool)
		}
	}
	// It must hold no operator/mutating verbs at all — not just the banned set.
	for _, tool := range s.Tools {
		switch tool {
		case "create-pr", "merge", "close-issue", "comment", "update-labels", "create-review":
			t.Fatalf("steward profile ships operator/mutating tool %q", tool)
		}
	}
}

// The display name is a user-replaceable stand-in read from config, never a brand
// string baked in Go: steward.yaml carries its own, and a profile omitting it
// falls back to the profile Name (still config-sourced).
func TestDisplayNameIsConfigSourced(t *testing.T) {
	profs, err := LoadDir(repoProfilesDir(t))
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if got := profs["steward"].DisplayName; got != "Steward" {
		t.Fatalf("steward display_name = %q, want %q (from config)", got, "Steward")
	}
	// A profile with no display_name defaults to Name — no hardcoded string.
	dir := t.TempDir()
	p := writeProfile(t, dir, "jeeves.yaml", `profile_version: 1
name: jeeves
tools: [read]
procedure: steward
budgets: {max_tool_rounds: 60}
policies: {read_only: true}
completion: {requires: none}
`)
	pr, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if pr.DisplayName != "jeeves" {
		t.Fatalf("display_name default = %q, want %q (the Name)", pr.DisplayName, "jeeves")
	}
}

// The read-only ban must fire for the steward procedure too, not just reviewers:
// a steward-intent profile carrying a mutating tool is refused loudly at load.
func TestValidateRejectsStewardWithMutatingTool(t *testing.T) {
	dir := t.TempDir()
	p := writeProfile(t, dir, "steward.yaml", `profile_version: 1
name: steward
tools: [read, write, list-issues]
procedure: steward
budgets: {max_tool_rounds: 60}
completion: {requires: none}
`)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "mutating tool") {
		t.Fatalf("expected loud refusal of steward+write, got err=%v", err)
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
