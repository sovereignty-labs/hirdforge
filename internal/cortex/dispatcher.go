package cortex

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"git.hirdforge.com/kit/hirdforge/internal/sandbox"
)

// Dispatcher executes a queued task: envelope → sandbox Job → mechanical gate
// → PR observation → lifecycle transitions, every one with its reason
// (P1.3 + the test-command half of P1.5). No model output is consulted
// anywhere in this file — exit codes, k8s facts, and Gitea API observations
// only.
type Dispatcher struct {
	Store   Store
	Sandbox sandbox.Sandbox
	// PRLookup observes whether a PR exists for a head branch via the Gitea
	// API (never from agent output — PERSISTENCE.md invariant #3). Returns
	// (prNumber, found).
	PRLookup func(ctx context.Context, repo, headBranch string) (int64, bool, error)
	// Events receives the internal task.gate_* events (fed back to
	// Cortex.HandleEvent so gate outcomes can fire follow-on routes).
	Events func(Event)

	AgentImage    string
	AgentCommand  []string
	CloneURLBase  string // e.g. https://git.hirdforge.com — clone URL = base + "/" + repo + ".git"
	BaseBranch    string
	CredentialRef string
}

// DispatchTask drives one task from queued to review/failed, synchronously.
// Callers run it in a goroutine per task; every step is persisted so a crash
// resumes legibly (the task is simply failed by timeout later — no silent
// zombie states).
func (d *Dispatcher) DispatchTask(ctx context.Context, cfg *Config, route *Route, taskID string) error {
	task, _, err := d.Store.GetTask(taskID)
	if err != nil {
		return fmt.Errorf("dispatcher: %w", err)
	}
	env, err := BuildEnvelope(cfg, route, EnvelopeParams{
		Task:           task,
		CloneURL:       d.CloneURLBase + "/" + task.IssueRepo + ".git",
		BaseBranch:     d.BaseBranch,
		CredentialRef:  d.CredentialRef,
		FailureContext: decodeFailureContext(task.FailureContext),
	})
	if err != nil {
		return d.failTask(taskID, "envelope assembly failed: "+err.Error(), Cause{Kind: CauseSandbox})
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		return d.failTask(taskID, "envelope marshal failed: "+err.Error(), Cause{Kind: CauseSandbox})
	}

	gate := route.DoneGate
	spec := sandbox.RunSpec{
		TaskID:       task.ID,
		Envelope:     envJSON,
		AgentImage:   d.AgentImage,
		AgentCommand: d.AgentCommand,
		CloneURL:     env.Git.CloneURL,
		BaseBranch:   env.Git.BaseBranch,
		WorkBranch:   env.Git.WorkBranch,
		GateCommand:  gate.Command,
		CredSecret:   d.CredentialRef,
	}
	if route.TimeoutMinutes > 0 {
		spec.Deadline = time.Duration(route.TimeoutMinutes) * time.Minute
	}

	ref, err := d.Sandbox.Allocate(ctx, spec)
	if err != nil {
		return d.failTask(taskID, "sandbox allocation failed: "+err.Error(), Cause{Kind: CauseSandbox})
	}
	if err := d.Store.Transition(taskID, StatusDispatched,
		fmt.Sprintf("sandbox allocated: %s", ref.String()),
		Cause{Kind: CauseSandbox, Detail: map[string]any{"job": ref.JobName}}); err != nil {
		return err
	}
	if err := d.Store.Transition(taskID, StatusBuilding,
		"agent container running in "+ref.String(),
		Cause{Kind: CauseSandbox, Detail: map[string]any{"job": ref.JobName}}); err != nil {
		return err
	}

	res, waitErr := d.Sandbox.Wait(ctx, &ref)
	defer func() {
		if derr := d.Sandbox.Destroy(context.WithoutCancel(ctx), ref); derr != nil {
			// A leaked sandbox is an incident, not a footnote.
			log.Printf("cortex: SANDBOX LEAK %s: %v", ref.String(), derr)
		}
	}()
	if waitErr != nil {
		return d.failTask(taskID, "sandbox wait failed: "+waitErr.Error(), Cause{Kind: CauseSandbox})
	}
	if res.Phase == "deadline" {
		return d.failTaskWithEvent(ctx, route, taskID, "timeout: sandbox exceeded route deadline",
			Cause{Kind: CauseTimeout, Detail: map[string]any{"result": res}})
	}
	if !res.CleanCheckOK || !res.CheckoutOK {
		return d.failTask(taskID,
			fmt.Sprintf("sandbox guard failed: clean=%v checkout=%v", res.CleanCheckOK, res.CheckoutOK),
			Cause{Kind: CauseSandbox, Detail: map[string]any{"result": res}})
	}

	// Collect: the PR is observed from Gitea, never trusted from agent output.
	prNumber, prFound := int64(0), false
	if d.PRLookup != nil {
		var perr error
		prNumber, prFound, perr = d.PRLookup(ctx, task.IssueRepo, env.Git.WorkBranch)
		if perr != nil {
			return d.failTask(taskID, "pr lookup failed: "+perr.Error(), Cause{Kind: CauseSandbox})
		}
	}
	if !prFound {
		return d.failTaskWithEvent(ctx, route, taskID, "no_pr: agent exited without an observable PR",
			Cause{Kind: CauseGate, Detail: map[string]any{"agent_exit": res.AgentExitCode}})
	}

	// The mechanical done-gate (D-GATE): the gate container ran the route's
	// command in the sandbox; its exit code decides. Evidence travels with
	// the transition either way — an evidence-free green is invalid.
	evidence, _ := d.Sandbox.GateOutput(ctx, ref)
	gateResult := map[string]any{
		"type": gate.Type, "passed": res.GateExitCode == 0, "exit_code": res.GateExitCode,
		"command": gate.Command, "evidence": string(evidence),
	}
	cause := Cause{Kind: CauseGate, Detail: map[string]any{"gate": gateResult, "pr_number": prNumber}}

	if res.GateExitCode != 0 {
		return d.failTaskWithEvent(ctx, route, taskID,
			fmt.Sprintf("gate_failed:%s exit %d: %s", gate.Type, res.GateExitCode, firstLine(string(evidence))),
			cause)
	}
	if err := d.Store.Transition(taskID, StatusReview,
		fmt.Sprintf("gate_passed:%s exit 0; PR #%d observed on %s", gate.Type, prNumber, task.IssueRepo),
		cause); err != nil {
		return err
	}
	d.emit(Event{Type: EventTaskGatePassed, Repo: task.IssueRepo, RouteID: route.ID, TaskID: taskID, PRNumber: prNumber})
	return nil
}

func (d *Dispatcher) failTask(taskID, reason string, cause Cause) error {
	if err := d.Store.Transition(taskID, StatusFailed, reason, cause); err != nil {
		return fmt.Errorf("dispatcher: recording failure %q: %w", reason, err)
	}
	return nil
}

// failTaskWithEvent fails the task AND emits task.gate_failed so failure
// routes (Phase 3 retry policy) can consume it.
func (d *Dispatcher) failTaskWithEvent(ctx context.Context, route *Route, taskID, reason string, cause Cause) error {
	if err := d.failTask(taskID, reason, cause); err != nil {
		return err
	}
	d.emit(Event{Type: EventTaskGateFailed, RouteID: route.ID, TaskID: taskID})
	return nil
}

func (d *Dispatcher) emit(ev Event) {
	if d.Events != nil {
		d.Events(ev)
	}
}

func decodeFailureContext(raw json.RawMessage) *FailureContext {
	if len(raw) == 0 {
		return nil
	}
	var fc FailureContext
	if err := json.Unmarshal(raw, &fc); err != nil {
		return nil
	}
	return &fc
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
