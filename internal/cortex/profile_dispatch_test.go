package cortex

import (
	"strings"
	"testing"

	"git.hirdforge.com/kit/hirdforge/internal/profile"
)

func testDispatcherWithProfiles() *Dispatcher {
	return &Dispatcher{
		AgentCommandBase: []string{"valhalla-agent", "-one-shot", "-workspace", "/work/repo"},
		Profiles: map[string]profile.Profile{
			"builder": {
				Version: 1, Name: "builder", Procedure: "builder",
				Tools:   []string{"todo", "read", "edit", "gitea"},
				Budgets: profile.Budgets{MaxToolRounds: 80, MaxContext: 20},
			},
			"reviewer": {
				Version: 1, Name: "reviewer", Procedure: "reviewer",
				Tools:   []string{"read", "git-diff", "gitea-review"},
				Budgets: profile.Budgets{MaxToolRounds: 30, MaxContext: 20},
			},
		},
	}
}

func TestAgentCommandForResolvesProfile(t *testing.T) {
	d := testDispatcherWithProfiles()

	cmd, p, err := d.agentCommandFor(Bundle{Profile: "builder"})
	if err != nil {
		t.Fatalf("builder: %v", err)
	}
	if p.Name != "builder" {
		t.Fatalf("resolved profile = %q", p.Name)
	}
	joined := strings.Join(cmd, " ")
	if !strings.HasPrefix(joined, "valhalla-agent -one-shot -workspace /work/repo") {
		t.Fatalf("base not preserved: %q", joined)
	}
	for _, want := range []string{"-tools todo,read,edit,gitea", "-max-tool-rounds 80", "-procedure builder"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}

	// reviewer resolves to its own read-only tool set + procedure.
	rcmd, rp, err := d.agentCommandFor(Bundle{Profile: "reviewer"})
	if err != nil {
		t.Fatalf("reviewer: %v", err)
	}
	if rp.Name != "reviewer" {
		t.Fatalf("reviewer name = %q", rp.Name)
	}
	rjoined := strings.Join(rcmd, " ")
	if !strings.Contains(rjoined, "-procedure reviewer") || strings.Contains(rjoined, "edit") {
		t.Fatalf("reviewer command wrong: %q", rjoined)
	}
}

func TestAgentCommandForDefaultAliasesToBuilder(t *testing.T) {
	d := testDispatcherWithProfiles()
	for _, name := range []string{"", "default"} {
		_, p, err := d.agentCommandFor(Bundle{Profile: name})
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		if p.Name != "builder" {
			t.Fatalf("%q resolved to %q, want builder", name, p.Name)
		}
	}
}

func TestAgentCommandForAppendsSkillsAndScopes(t *testing.T) {
	d := testDispatcherWithProfiles()
	d.SkillsRepoURL = "http://gitea/kit/hirdforge-personas.git"

	cmd, _, err := d.agentCommandFor(Bundle{
		Profile:      "builder",
		Skills:       []string{"go/testing-conventions", "hirdforge/commit-style"},
		MemoryScopes: []string{"skill:go", "repo:kit/hirdforge"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	joined := strings.Join(cmd, " ")
	for _, want := range []string{
		"-skills go/testing-conventions,hirdforge/commit-style",
		"-skills-repo http://gitea/kit/hirdforge-personas.git",
		"-memory-scopes skill:go,repo:kit/hirdforge",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
}

func TestAgentCommandForSkillsWithoutRepoIsLoud(t *testing.T) {
	d := testDispatcherWithProfiles() // no SkillsRepoURL
	if _, _, err := d.agentCommandFor(Bundle{Profile: "builder", Skills: []string{"go/x"}}); err == nil {
		t.Fatal("a bundle naming skills with no skills repo must fail loudly")
	}
	// A bundle with NO skills is unaffected by a missing skills repo.
	if _, _, err := d.agentCommandFor(Bundle{Profile: "builder"}); err != nil {
		t.Fatalf("sk-less bundle should dispatch fine: %v", err)
	}
}

func TestAgentCommandForUnknownProfileIsLoud(t *testing.T) {
	d := testDispatcherWithProfiles()
	if _, _, err := d.agentCommandFor(Bundle{Profile: "nonesuch"}); err == nil {
		t.Fatal("unknown profile must fail loudly, not silently fall back")
	}
	// An explicit "default" alias still works even if no default.yaml exists —
	// but a genuinely unknown name never aliases.
	if _, _, err := d.agentCommandFor(Bundle{Profile: "revieww"}); err == nil {
		t.Fatal("typo'd profile name must fail loudly")
	}
}
