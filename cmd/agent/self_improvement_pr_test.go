package main

import (
	"testing"

	toolpkg "github.com/kitporath/project_valhalla/pkg/tools"
)

// fakePRTool records the args it was Execute'd with so the test can assert
// which tool the self-improvement helper actually called.
type fakePRTool struct {
	name    string
	gotArgs map[string]interface{}
	gotName string
}

func (f *fakePRTool) Name() string                  { return f.name }
func (f *fakePRTool) Description() string           { return "fake" }
func (f *fakePRTool) Parameters() map[string]string { return map[string]string{} }
func (f *fakePRTool) Execute(args map[string]interface{}) toolpkg.ToolResult {
	f.gotArgs = args
	f.gotName = f.name
	return toolpkg.ToolResult{Output: "created PR #1"}
}

// TestOpenSelfImprovementPRPrefersStandaloneCreatePR verifies that builder-
// tier agents (post-hirdforge#249) with only `create-pr` registered get a
// flat create-pr call, not a gitea-with-action=create-pr call.
func TestOpenSelfImprovementPRPrefersStandaloneCreatePR(t *testing.T) {
	reg := toolpkg.NewRegistry()
	fake := &fakePRTool{name: "create-pr"}
	reg.Register(fake)

	res := openSelfImprovementPR(reg, "kit/hirdforge-personas", "warrior", "stop reading personas without cloning", "body text", "warrior/self-improvement-1")
	if res.Error != "" {
		t.Fatalf("openSelfImprovementPR error: %s", res.Error)
	}
	if fake.gotName != "create-pr" {
		t.Fatalf("called tool = %q, want create-pr", fake.gotName)
	}
	if _, hasAction := fake.gotArgs["action"]; hasAction {
		t.Errorf("create-pr should not receive an `action` key; got args = %#v", fake.gotArgs)
	}
	if got := fake.gotArgs["title"]; got != "soul: warrior learned — stop reading personas without cloning" {
		t.Errorf("title = %v", got)
	}
	if fake.gotArgs["head"] != "warrior/self-improvement-1" {
		t.Errorf("head = %v", fake.gotArgs["head"])
	}
}

// TestOpenSelfImprovementPRFallsBackToGiteaUmbrella verifies that architect/
// coordinator agents with the full gitea umbrella still work — the helper
// falls through to `gitea` with action=create-pr when create-pr isn't a
// standalone tool in the registry.
func TestOpenSelfImprovementPRFallsBackToGiteaUmbrella(t *testing.T) {
	reg := toolpkg.NewRegistry()
	fake := &fakePRTool{name: "gitea"}
	reg.Register(fake)

	res := openSelfImprovementPR(reg, "kit/hirdforge-personas", "chieftain", "tighten dispatch wording", "body", "chieftain/self-improvement-1")
	if res.Error != "" {
		t.Fatalf("openSelfImprovementPR error: %s", res.Error)
	}
	if fake.gotName != "gitea" {
		t.Fatalf("called tool = %q, want gitea", fake.gotName)
	}
	if got := fake.gotArgs["action"]; got != "create-pr" {
		t.Errorf("gitea action = %v, want create-pr", got)
	}
}

// TestOpenSelfImprovementPRMissingBothToolsReturnsError exercises the
// edge case where neither create-pr nor gitea is registered — should not
// panic, should surface an error from executeRegistryTool.
func TestOpenSelfImprovementPRMissingBothToolsReturnsError(t *testing.T) {
	reg := toolpkg.NewRegistry()
	res := openSelfImprovementPR(reg, "kit/hirdforge-personas", "warrior", "x", "y", "z")
	if res.Error == "" {
		t.Fatalf("expected error when neither tool is registered, got output=%q", res.Output)
	}
}
