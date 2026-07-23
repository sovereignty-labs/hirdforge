package main

// One-shot mode (P1.4): the agent as a sandbox Job init-container. Read the
// mounted envelope, render the prompt with a fixed template, run the tool
// loop once, exit. The exit code means only "the loop ran to an end" — task
// completion is decided afterwards by the gate container and Cortex's PR
// observation, never by anything this process prints (D-GATE).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"git.hirdforge.com/kit/hirdforge/internal/cortex"
	taskspkg "git.hirdforge.com/kit/hirdforge/pkg/tasks"
)

const (
	oneShotExitOK          = 0 // loop ended (normally or via M1-classified abnormal end)
	oneShotExitEnvelope    = 2 // envelope unreadable/invalid — a sandbox wiring fault
	oneShotExitInferential = 3 // the loop could not run (inference/backend failure)
)

// runOneShot executes one envelope through the conversation processor.
func runOneShot(process conversationProcessor, envelopePath string) int {
	raw, err := os.ReadFile(envelopePath)
	if err != nil {
		logJSON("error", "oneshot: envelope read failed", map[string]interface{}{"path": envelopePath, "error": err.Error()})
		return oneShotExitEnvelope
	}
	var env cortex.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		logJSON("error", "oneshot: envelope parse failed", map[string]interface{}{"error": err.Error()})
		return oneShotExitEnvelope
	}
	if env.EnvelopeVersion != 1 || env.TaskID == "" {
		logJSON("error", "oneshot: envelope invalid", map[string]interface{}{"version": env.EnvelopeVersion, "task_id": env.TaskID})
		return oneShotExitEnvelope
	}

	prompt := renderOneShotPrompt(&env)
	logJSON("info", "oneshot: dispatch", map[string]interface{}{
		"task_id": env.TaskID, "route_id": env.RouteID, "role": env.Role, "attempt": env.Attempt,
	})

	final, err := process(context.Background(), "oneshot-"+env.TaskID, env.TaskID, prompt,
		func(interface{}) bool { return true }, func(taskspkg.ToolLog) {})
	if err != nil {
		logJSON("error", "oneshot: loop error", map[string]interface{}{"task_id": env.TaskID, "error": err.Error()})
		return oneShotExitInferential
	}
	// Diagnostics only: the M1 outcome classification is logged for the task
	// record's diagnostics field; nothing downstream reads it for control.
	outcome := classifyRunOutcome(final, terminationCompleted)
	logJSON("info", "oneshot: loop ended", map[string]interface{}{
		"task_id": env.TaskID, "outcome_kind": string(outcome.Kind), "final_len": len(final),
	})
	return oneShotExitOK
}

// renderOneShotPrompt is the fixed template of DISPATCH_ENVELOPE.md: same
// envelope ⇒ same prompt, byte for byte.
func renderOneShotPrompt(env *cortex.Envelope) string {
	var b strings.Builder
	fmt.Fprintf(&b, "TASK %s (route %s, attempt %d, role %s)\n\n", env.TaskID, env.RouteID, env.Attempt, env.Role)
	if env.Issue != nil {
		fmt.Fprintf(&b, "ISSUE %s#%d: %s\n\n%s\n\n", env.Issue.Repo, env.Issue.Number, env.Issue.Title, env.Issue.Body)
	}
	if env.FailureContext != nil {
		fc := env.FailureContext
		fmt.Fprintf(&b, "PRIOR ATTEMPT FAILED — do not repeat it blindly.\nAttempt %d by %s failed: %s\n",
			fc.PriorAttempt, orUnknown(fc.PriorAgent), fc.Reason)
		if fc.GateExcerpt != "" {
			fmt.Fprintf(&b, "Gate output excerpt:\n%s\n", fc.GateExcerpt)
		}
		for _, fb := range fc.ReviewerFeedback {
			fmt.Fprintf(&b, "Reviewer feedback: %s\n", fb)
		}
		b.WriteString("\n")
	}
	if env.Review != nil {
		fmt.Fprintf(&b, "PR UNDER REVIEW: %s#%d (%s -> %s)\n",
			env.Review.PR.Repo, env.Review.PR.Number, env.Review.PR.Head, env.Review.PR.Base)
		if len(env.Review.GateResult) > 0 {
			fmt.Fprintf(&b, "Mechanical gate result: %s\n", string(env.Review.GateResult))
		}
		fmt.Fprintf(&b, "\nDIFF:\n%s\n", env.Review.Diff)
		if env.Review.DiffTruncated {
			b.WriteString("\n[diff truncated at 256KiB — fetch the rest via list-pr-files if needed]\n")
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "The repository is already cloned at your workspace root on branch %s.\n\n", env.Git.WorkBranch)
	fmt.Fprintf(&b, "DONE WHEN: %s\n", env.DoneWhen)
	return b.String()
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
