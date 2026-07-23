package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.hirdforge.com/kit/hirdforge/internal/cortex"
	taskspkg "git.hirdforge.com/kit/hirdforge/pkg/tasks"
)

func writeEnvelope(t *testing.T, env string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "envelope.json")
	if err := os.WriteFile(path, []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const oneShotEnvelope = `{
  "envelope_version": 1,
  "task_id": "hf-01-one",
  "route_id": "build-on-label",
  "attempt": 2,
  "role": "builder",
  "issue": {"repo": "kit/hirdforge", "number": 41, "title": "add a thing", "body": "the details"},
  "git": {"clone_url": "u", "base_branch": "main", "work_branch": "agent/hf-01-one", "push_credential_ref": "c"},
  "bundle": {"profile": "default"},
  "done_when": "Open a PR. The gate decides; your own assessment does not decide completion.",
  "failure_context": {"prior_attempt": 1, "prior_agent": "agent-a", "reason": "gate_failed:test-command exit 1", "gate_excerpt": "FAIL: TestX"}
}`

func TestRunOneShotHappyPath(t *testing.T) {
	path := writeEnvelope(t, oneShotEnvelope)
	var gotPrompt, gotTaskID string
	process := func(_ context.Context, sessionID, taskID, content string, _ func(interface{}) bool, _ func(taskspkg.ToolLog)) (string, error) {
		gotTaskID, gotPrompt = taskID, content
		return "PR: https://git.hirdforge.com/kit/hirdforge/pulls/55", nil
	}
	if code := runOneShot(process, path); code != oneShotExitOK {
		t.Fatalf("exit = %d, want %d", code, oneShotExitOK)
	}
	if gotTaskID != "hf-01-one" {
		t.Fatalf("taskID = %q", gotTaskID)
	}
	for _, want := range []string{
		"TASK hf-01-one (route build-on-label, attempt 2, role builder)",
		"ISSUE kit/hirdforge#41: add a thing",
		"the details",
		"PRIOR ATTEMPT FAILED", // D-LESSONS #2 rides the prompt
		"gate_failed:test-command exit 1",
		"FAIL: TestX",
		"branch agent/hf-01-one",
		"DONE WHEN:",
		"does not decide completion",
	} {
		if !strings.Contains(gotPrompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

// TestRunOneShotAgentClaimsFailureStillExitsZero pins the doctrine boundary:
// the agent saying FAILED does not change the process outcome — the gate and
// PR observation decide, downstream.
func TestRunOneShotAgentClaimsFailureStillExitsZero(t *testing.T) {
	path := writeEnvelope(t, oneShotEnvelope)
	process := func(_ context.Context, _, _, _ string, _ func(interface{}) bool, _ func(taskspkg.ToolLog)) (string, error) {
		return "FAILED: I could not do it", nil
	}
	if code := runOneShot(process, path); code != oneShotExitOK {
		t.Fatalf("exit = %d — the loop ended; the agent's claim is not this process's business", code)
	}
}

func TestRunOneShotLoopErrorExitsThree(t *testing.T) {
	path := writeEnvelope(t, oneShotEnvelope)
	process := func(_ context.Context, _, _, _ string, _ func(interface{}) bool, _ func(taskspkg.ToolLog)) (string, error) {
		return "", fmt.Errorf("inference backend unreachable")
	}
	if code := runOneShot(process, path); code != oneShotExitInferential {
		t.Fatalf("exit = %d, want %d", code, oneShotExitInferential)
	}
}

func TestRunOneShotBadEnvelope(t *testing.T) {
	for name, env := range map[string]string{
		"not json":      `{`,
		"wrong version": `{"envelope_version": 2, "task_id": "x"}`,
		"no task id":    `{"envelope_version": 1}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := writeEnvelope(t, env)
			process := func(_ context.Context, _, _, _ string, _ func(interface{}) bool, _ func(taskspkg.ToolLog)) (string, error) {
				t.Fatal("loop must not run on a bad envelope")
				return "", nil
			}
			if code := runOneShot(process, path); code != oneShotExitEnvelope {
				t.Fatalf("exit = %d, want %d", code, oneShotExitEnvelope)
			}
		})
	}
	if code := runOneShot(nil, "/nonexistent/envelope.json"); code != oneShotExitEnvelope {
		t.Fatalf("missing file: exit = %d, want %d", code, oneShotExitEnvelope)
	}
}

func TestRenderOneShotPromptReviewerVariant(t *testing.T) {
	env := &cortex.Envelope{
		EnvelopeVersion: 1, TaskID: "hf-02", RouteID: "review-on-gate", Attempt: 1, Role: "reviewer",
		Git: cortex.EnvelopeGit{WorkBranch: "agent/hf-01"},
		Review: &cortex.ReviewContext{
			PR:   cortex.ReviewPR{Repo: "kit/hirdforge", Number: 55, Head: "agent/hf-01", Base: "main"},
			Diff: "diff --git a/x b/x\n+added",
		},
		DoneWhen: "Submit a real Gitea review: APPROVE or REQUEST_CHANGES.",
	}
	p := renderOneShotPrompt(env)
	for _, want := range []string{"PR UNDER REVIEW: kit/hirdforge#55", "diff --git a/x", "DONE WHEN: Submit a real Gitea review"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

// TestRenderOneShotPromptDeterministic: fixed template — same envelope, same
// prompt, byte for byte.
func TestRenderOneShotPromptDeterministic(t *testing.T) {
	env := &cortex.Envelope{EnvelopeVersion: 1, TaskID: "hf-03", RouteID: "r", Attempt: 1, Role: "builder",
		Git: cortex.EnvelopeGit{WorkBranch: "agent/hf-03"}, DoneWhen: "x"}
	first := renderOneShotPrompt(env)
	for i := 0; i < 50; i++ {
		if renderOneShotPrompt(env) != first {
			t.Fatal("prompt rendering is not deterministic")
		}
	}
}
