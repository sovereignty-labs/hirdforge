package main

import (
	"sort"
	"strings"
	"testing"

	toolpkg "git.hirdforge.com/kit/hirdforge/pkg/tools"
)

// TestConfigureToolRegistryBuilderProfile pins the exact tool set a builder
// receives. Regressions in registration gates (auto-register from --peers,
// silent additions like plan, or the gitea suite leaking through create-pr)
// are caught here.
func TestConfigureToolRegistryBuilderProfile(t *testing.T) {
	reg := toolpkg.NewRegistry()
	enabled := map[string]bool{
		"read":        true,
		"write":       true,
		"edit":        true,
		"exec":        true,
		"git-clone":   true,
		"git-commit":  true,
		"git-diff":    true,
		"create-pr":   true,
		"task_result": true,
	}
	configureToolRegistry(reg, toolSetupDeps{
		workspace:     t.TempDir(),
		giteaURL:      "http://gitea.test",
		agentName:     "warrior",
		peers:         map[string]string{"chieftain": "http://chieftain.test"},
		reviewTracker: newReviewContextTracker("warrior", "", 0),
		enabled:       enabled,
	})

	got := reg.List()
	sort.Strings(got)
	want := []string{
		"create-pr",
		"edit",
		"exec",
		"git-clone",
		"git-commit",
		"git-diff",
		"read",
		"task_result",
		"write",
	}
	if len(got) != len(want) {
		t.Fatalf("registry size = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("registry[%d] = %q, want %q", i, got[i], name)
		}
	}
	for _, leak := range []string{"delegate", "task_status", "broadcast", "plan", "plan-step-complete", "gitea", "create-issue", "merge-pr", "close-issue"} {
		if _, ok := reg.Get(leak); ok {
			t.Errorf("unwanted tool registered: %q", leak)
		}
	}
}

// TestConfigureToolRegistryCreatePROnly verifies that --tools=create-pr does
// NOT pull in the rest of the gitea suite.
func TestConfigureToolRegistryCreatePROnly(t *testing.T) {
	reg := toolpkg.NewRegistry()
	configureToolRegistry(reg, toolSetupDeps{
		workspace:     t.TempDir(),
		giteaURL:      "http://gitea.test",
		agentName:     "test",
		reviewTracker: newReviewContextTracker("test", "", 0),
		enabled:       map[string]bool{"create-pr": true},
	})
	if _, ok := reg.Get("create-pr"); !ok {
		t.Fatal("create-pr not registered")
	}
	for _, leak := range []string{"create-issue", "list-issues", "close-issue", "comment", "create-review", "merge-pr", "list-pr-files", "update-labels", "get-issue", "list-branches"} {
		if _, ok := reg.Get(leak); ok {
			t.Errorf("gitea suite leaked through create-pr: %q registered", leak)
		}
	}
}

// TestConfigureToolRegistryReviewerProfile pins P2.7's safety property: the
// reviewer tool set gives the verdict tool (create-review) + read-only helpers
// and NOTHING mutating — no create-pr, no merge, no edit/write/exec. A reviewer
// that physically lacks the mutating tools cannot be prompt-injected into
// changing code.
func TestConfigureToolRegistryReviewerProfile(t *testing.T) {
	reg := toolpkg.NewRegistry()
	configureToolRegistry(reg, toolSetupDeps{
		workspace:     t.TempDir(),
		giteaURL:      "http://gitea.test",
		agentName:     "test",
		reviewTracker: newReviewContextTracker("test", "", 0),
		enabled:       map[string]bool{"read": true, "git-diff": true, "create-review": true, "list-pr-files": true},
	})
	for _, want := range []string{"read", "git-diff", "create-review", "list-pr-files"} {
		if _, ok := reg.Get(want); !ok {
			t.Errorf("reviewer profile missing tool: %q", want)
		}
	}
	// The mutating / builder tools must be absent — this is the security property.
	for _, forbidden := range []string{"create-pr", "merge-pr", "edit", "write", "exec", "git-commit", "git-clone", "create-issue", "close-issue"} {
		if _, ok := reg.Get(forbidden); ok {
			t.Errorf("reviewer profile leaked mutating tool: %q", forbidden)
		}
	}
	// The reviewer procedure renders for a reg holding the verdict tool.
	if p := reviewerProcedure(reg); !strings.Contains(p, "Review procedure") || !strings.Contains(p, "create-review") {
		t.Fatalf("reviewer procedure did not render: %q", p)
	}
}

// TestConfigureToolRegistryStewardProfile pins the interlocutor's read-only
// capability set: it can read the repo and the issue record, and holds NOTHING
// that mutates code or the lifecycle. The absence is the security property
// (O-PROFILE §4, D-INTERLOCUTOR) — the interlocutor proposes; it never does.
func TestConfigureToolRegistryStewardProfile(t *testing.T) {
	reg := toolpkg.NewRegistry()
	configureToolRegistry(reg, toolSetupDeps{
		workspace:     t.TempDir(),
		giteaURL:      "http://gitea.test",
		agentName:     "steward",
		reviewTracker: newReviewContextTracker("steward", "", 0),
		enabled:       map[string]bool{"read": true, "git-diff": true, "list-issues": true, "get-issue": true},
	})
	for _, want := range []string{"read", "git-diff", "list-issues", "get-issue"} {
		if _, ok := reg.Get(want); !ok {
			t.Errorf("steward profile missing read-only tool: %q", want)
		}
	}
	// Nothing that writes code, a PR, an issue, or the lifecycle may be present —
	// including read-only-adjacent gitea verbs the interlocutor must not hold.
	for _, forbidden := range []string{
		"create-pr", "merge-pr", "edit", "write", "exec", "git-commit", "git-clone",
		"create-issue", "close-issue", "comment", "update-labels", "create-review",
	} {
		if _, ok := reg.Get(forbidden); ok {
			t.Errorf("steward profile leaked mutating tool: %q", forbidden)
		}
	}
	// The steward procedure renders plan mode: read-only, grounds first, emits the
	// {reply, plan?} contract, and files nothing.
	p := selectProcedure("steward", reg)
	for _, want := range []string{"interlocutor", "READ-ONLY", "GROUND", "PROSE", "custom-validator", "needs_operator"} {
		if !strings.Contains(p, want) {
			t.Fatalf("steward procedure missing %q; got:\n%s", want, p)
		}
	}
	// It must not instruct mutation — no builder delivery language.
	for _, forbidden := range []string{"create-pr", "git-commit", "IMPLEMENT — make file changes"} {
		if strings.Contains(p, forbidden) {
			t.Fatalf("steward procedure leaked builder instruction %q", forbidden)
		}
	}
}

// TestConfigureToolRegistryGiteaStillRegistersFullSuite ensures gitea opt-in
// still pulls every gitea tool — only the auto-register-from-peers path
// changed, not the explicit gitea path.
func TestConfigureToolRegistryGiteaStillRegistersFullSuite(t *testing.T) {
	reg := toolpkg.NewRegistry()
	configureToolRegistry(reg, toolSetupDeps{
		workspace:     t.TempDir(),
		giteaURL:      "http://gitea.test",
		agentName:     "test",
		reviewTracker: newReviewContextTracker("test", "", 0),
		enabled:       map[string]bool{"gitea": true},
	})
	for _, name := range []string{"create-issue", "create-pr", "list-issues", "close-issue", "comment", "create-review", "merge-pr", "list-pr-files", "update-labels", "get-issue", "list-branches"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("gitea suite missing tool: %q", name)
		}
	}
}

// TestConfigureToolRegistryNoDelegateAutoFromPeers ensures the peers-only
// path no longer leaks delegate / task_status / broadcast.
func TestConfigureToolRegistryNoDelegateAutoFromPeers(t *testing.T) {
	reg := toolpkg.NewRegistry()
	configureToolRegistry(reg, toolSetupDeps{
		workspace:     t.TempDir(),
		agentName:     "test",
		peers:         map[string]string{"peer": "http://peer.test"},
		reviewTracker: newReviewContextTracker("test", "", 0),
		enabled:       map[string]bool{},
	})
	for _, leak := range []string{"delegate", "task_status", "broadcast"} {
		if _, ok := reg.Get(leak); ok {
			t.Errorf("unwanted tool registered from peers-only: %q", leak)
		}
	}
	// task_result IS still auto-registered for builders to read back their
	// own delegated-task artifacts.
	if _, ok := reg.Get("task_result"); !ok {
		t.Error("task_result should still auto-register when peers are present")
	}
}
