package main

import (
	"sort"
	"testing"

	toolpkg "git.hirdforge.com/kit/hirdforge/pkg/tools"
)

// TestConfigureToolRegistryWarriorProfile pins the exact tool set a builder
// receives. Regressions in registration gates (auto-register from --peers,
// silent additions like plan, or the gitea suite leaking through create-pr)
// are caught here.
func TestConfigureToolRegistryWarriorProfile(t *testing.T) {
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
