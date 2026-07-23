package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	toolpkg "git.hirdforge.com/kit/hirdforge/pkg/tools"
)

// fakePRURL is the PR URL the fake tool "creates"; it flows tool output ->
// completion detection -> final reported content. It is a fixture host, never a
// real repo.
const fakePRURL = "http://gitea.local/kit/hirdforge/pulls/7"

// fakeGiteaPRTool is a test-only stand-in for the production gitea tool's
// create-pr action. In this runtime PR creation is the tool named "gitea" with
// args action="create-pr"; the session loop extracts the PR URL from the tool's
// output (extractFirstURL) and tracks the PR. The fake performs no git or
// network work — it just returns a fixture PR URL and counts invocations.
type fakeGiteaPRTool struct {
	calls int64
}

func (t *fakeGiteaPRTool) Name() string        { return "gitea" }
func (t *fakeGiteaPRTool) Description() string { return "test-only fake gitea create-pr tool" }
func (t *fakeGiteaPRTool) Parameters() map[string]string {
	return map[string]string{"action": "create-pr"}
}
func (t *fakeGiteaPRTool) Execute(map[string]interface{}) toolpkg.ToolResult {
	atomic.AddInt64(&t.calls, 1)
	return toolpkg.ToolResult{Output: "Pull request created: " + fakePRURL}
}

// TestE2EFakePRToolCreatesPR proves the e2e contract can represent a real
// tool-driven PR-created outcome: the agent calls the fake gitea create-pr tool,
// the tool returns a PR URL, and the agent reports that URL — satisfying the
// completion gate and terminating as completed. Fully deterministic and offline.
func TestE2EFakePRToolCreatesPR(t *testing.T) {
	logs := captureLogs(t)

	// Turn 1: call the fake gitea create-pr tool. Turn 2: report the PR URL.
	srv := newStubInferenceServer(t,
		stubToolCall("gitea", `{"action":"create-pr","repo":"kit/hirdforge","head":"fix/widget","title":"Fix widget"}`),
		stubContent("Done. Opened the PR: "+fakePRURL),
	)

	tool := &fakeGiteaPRTool{}
	deps := harnessDeps(srv)
	deps.reg.Register(tool)
	deps.workspace = t.TempDir() // fixture workspace; never a real repo

	const userRequest = "Please create a PR for the widget fix."
	if !requestRequiresCompletionSignal(userRequest) {
		t.Fatal("precondition: the request should require a completion signal")
	}

	proc := newConversationProcessor(deps)
	out, err := proc(context.Background(), "sess-fake-pr", "", userRequest, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 1) The fake PR tool was actually invoked.
	if got := atomic.LoadInt64(&tool.calls); got == 0 {
		t.Fatal("expected the fake gitea create-pr tool to be invoked")
	}
	// 2) The final content is an orchestrator-recognizable completion signal...
	if !contentHasCompletionSignal(out) {
		t.Errorf("final content lacks a completion signal: %q", out)
	}
	// 3) ...and carries the PR URL the tool produced.
	if !strings.Contains(out, fakePRURL) {
		t.Errorf("final content missing the PR URL %q: %q", fakePRURL, out)
	}
	// 4) The PR-requiring completion gate was satisfied.
	if !logs.hasLogMsg("completion_gate_passed") {
		t.Errorf("expected completion_gate_passed to be logged for a satisfied PR gate")
	}
	// 5) The run terminated reportably as completed.
	if !logs.hasTerminationReason(terminationCompleted) {
		t.Errorf("expected session_termination reason %q; reasons seen: %v",
			terminationCompleted, logs.terminationReasons())
	}
}
