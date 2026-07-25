package cortex

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"git.hirdforge.com/kit/hirdforge/internal/profile"
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
	// DiffFetch retrieves a PR's unified diff + head/base refs from Gitea
	// (D-LESSONS #4: the reviewer sees the artifact).
	DiffFetch func(ctx context.Context, repo string, prNumber int64) (diff, head, base string, err error)
	// ReviewLookup observes whether a review verdict exists on a PR via the
	// Gitea API. Returns (state APPROVED|REQUEST_CHANGES, reviewer, found).
	ReviewLookup func(ctx context.Context, repo string, prNumber int64) (string, string, bool, error)

	AgentImage string
	// AgentCommandBase is the constant part of the agent invocation (binary,
	// envelope, workspace, inference/gitea endpoints). The per-task tool set,
	// step cap, procedure, and context budget come from the resolved profile
	// (O-PROFILE) and are appended at dispatch — never hardcoded here.
	AgentCommandBase []string
	// Profiles are the loaded, validated harness profiles keyed by name. A
	// bundle's `profile` field selects one; an unknown name is a loud dispatch
	// failure, never a silent fallback (O-PROFILE resolution obligation).
	Profiles map[string]profile.Profile
	// SkillsRepoURL is the tokenless git URL of the skills/personas repo the
	// sandbox agent resolves bundle.skills against (auth via the guard-primed
	// credential helper). Empty ⇒ bundles that name skills fail loudly at dispatch.
	SkillsRepoURL string
	CloneURLBase  string // e.g. https://git.hirdforge.com — clone URL = base + "/" + repo + ".git"
	BaseBranch    string
	CredentialRef string
}

// legacyDefaultProfile is the pre-O-PROFILE bundle profile name; it aliases to
// "builder" (with a log line) so live configs that still say `profile: default`
// keep dispatching during the migration to named profiles. A truly unknown name
// is NOT aliased — it fails loudly.
const legacyDefaultProfile = "default"

// agentCommandFor resolves a bundle to the full agent command: the constant base
// + the resolved profile's flags (O-PROFILE) + the bundle's skills/memory-scope
// flags (O-SKILL-BUNDLE). An unknown profile is a loud error; "default" is an
// explicit legacy alias for "builder". A bundle that names skills with no skills
// repo configured also fails loudly (the agent would otherwise abort at startup).
func (d *Dispatcher) agentCommandFor(bundle Bundle) ([]string, profile.Profile, error) {
	name := strings.TrimSpace(bundle.Profile)
	if name == "" {
		name = "builder"
	}
	if _, ok := d.Profiles[name]; !ok && name == legacyDefaultProfile {
		log.Printf("cortex: profile %q is a legacy alias → \"builder\" (migrate the bundle to a named profile)", legacyDefaultProfile)
		name = "builder"
	}
	p, ok := d.Profiles[name]
	if !ok {
		return nil, profile.Profile{}, fmt.Errorf("unknown profile %q (loaded: %d)", bundle.Profile, len(d.Profiles))
	}
	cmd := append(append([]string{}, d.AgentCommandBase...), p.AgentArgs()...)

	// O-SKILL-BUNDLE: append the bundle's skills + memory scopes. Skills need a
	// repo to resolve against; a bundle that names skills with none configured is
	// a loud dispatch failure, not a silent drop.
	if len(bundle.Skills) > 0 {
		if strings.TrimSpace(d.SkillsRepoURL) == "" {
			return nil, profile.Profile{}, fmt.Errorf("bundle names skills %v but no skills repo is configured", bundle.Skills)
		}
		cmd = append(cmd, "-skills", strings.Join(bundle.Skills, ","), "-skills-repo", d.SkillsRepoURL)
	}
	if len(bundle.MemoryScopes) > 0 {
		cmd = append(cmd, "-memory-scopes", strings.Join(bundle.MemoryScopes, ","))
	}
	return cmd, p, nil
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

	agentCmd, prof, err := d.agentCommandFor(task.Bundle)
	if err != nil {
		// O-PROFILE: an unresolvable profile is a loud dispatch failure, never a
		// silent fallback — the task fails legibly rather than running an
		// unconfigured loop.
		return d.failTask(taskID, "profile resolution failed: "+err.Error(), Cause{Kind: CauseSandbox})
	}

	gate := route.DoneGate
	spec := sandbox.RunSpec{
		TaskID:       task.ID,
		Envelope:     envJSON,
		AgentImage:   d.AgentImage,
		AgentCommand: agentCmd,
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
	// Record the resolved capability on the transition row (O-PROFILE audit hook:
	// a run's tool set is reconstructable after the fact) — informational, never
	// read for control flow.
	if err := d.Store.Transition(taskID, StatusDispatched,
		fmt.Sprintf("sandbox allocated: %s (profile %s)", ref.String(), prof.Name),
		Cause{Kind: CauseSandbox, Detail: map[string]any{
			"job":       ref.JobName,
			"profile":   prof.Name,
			"procedure": prof.Procedure,
			"tools":     strings.Join(prof.Tools, ","),
		}}); err != nil {
		return err
	}
	if err := d.Store.Transition(taskID, StatusBuilding,
		"agent container running in "+ref.String(),
		Cause{Kind: CauseSandbox, Detail: map[string]any{"job": ref.JobName}}); err != nil {
		return err
	}

	return d.awaitAndFinalize(ctx, route, task, env, ref)
}

// awaitAndFinalize waits for a task's sandbox to terminate, then collects the
// PR, evaluates the mechanical gate, and records the outcome. It is called by
// DispatchTask and by the startup reconciler re-attaching to an in-flight task
// whose waiter goroutine died with a gateway restart — the sandbox Job outlives
// the gateway, so the work is not lost, only its watcher.
func (d *Dispatcher) awaitAndFinalize(ctx context.Context, route *Route, task *TaskRecord, env *Envelope, ref sandbox.Ref) error {
	taskID := task.ID
	gate := route.DoneGate

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
	if err := d.Store.SetPR(taskID, task.IssueRepo, prNumber); err != nil {
		log.Printf("cortex: ERROR persisting PR ref on %s: %v", taskID, err)
	}
	if grJSON, err := json.Marshal(gateResult); err == nil {
		if err := d.Store.SetGateResult(taskID, grJSON); err != nil {
			log.Printf("cortex: ERROR persisting gate result on %s: %v", taskID, err)
		}
	}

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
	// The reason carries the gate's own output (e.g. "gate_failed:test-command
	// exit 1: <first line>"); pass it as the excerpt so a retry route can give
	// the next builder the evidence rather than a bare "it failed" (P3.2).
	d.emit(Event{Type: EventTaskGateFailed, RouteID: route.ID, TaskID: taskID, GateExcerpt: reason})
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

// DispatchReviewer runs the reviewer leg (P1.6) against an existing task in
// status review — no new task row; the reviewer is an attribute of THE task.
// The reviewer's verdict reaches Cortex only as a pr.review_submitted webhook
// (a Gitea fact); this method merely runs the reviewer and observes whether a
// verdict landed. The builder task's mechanical gate has already passed —
// the reviewer is a gate on top, never instead (D-GATE).
func (d *Dispatcher) DispatchReviewer(ctx context.Context, cfg *Config, route *Route, ev Event) error {
	task, _, err := d.Store.GetTask(ev.TaskID)
	if err != nil {
		return fmt.Errorf("dispatcher: reviewer: %w", err)
	}
	if task.Status != StatusReview {
		return fmt.Errorf("dispatcher: reviewer: task %s is %s, not review", task.ID, task.Status)
	}
	if d.DiffFetch == nil {
		return d.failTask(task.ID, "reviewer dispatch impossible: no diff fetcher wired", Cause{Kind: CauseSandbox})
	}
	diff, head, base, err := d.DiffFetch(ctx, task.PRRepo, task.PRNumber)
	if err != nil {
		return d.failTask(task.ID, "reviewer diff fetch failed: "+err.Error(), Cause{Kind: CauseSandbox})
	}
	env, err := BuildEnvelope(cfg, route, EnvelopeParams{
		Task:          task,
		CloneURL:      d.CloneURLBase + "/" + task.IssueRepo + ".git",
		BaseBranch:    d.BaseBranch,
		CredentialRef: d.CredentialRef,
		Review: &ReviewContext{
			PR:         ReviewPR{Repo: task.PRRepo, Number: task.PRNumber, Head: head, Base: base},
			Diff:       diff,
			GateResult: task.DoneGate, // config snapshot + last_result: the gate's evidence
		},
	})
	if err != nil {
		return d.failTask(task.ID, "reviewer envelope assembly failed: "+err.Error(), Cause{Kind: CauseSandbox})
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		return d.failTask(task.ID, "reviewer envelope marshal failed: "+err.Error(), Cause{Kind: CauseSandbox})
	}
	var reviewerBundle Bundle
	if route.Dispatch != nil {
		if b, ok := cfg.Bundles[route.Dispatch.Bundle]; ok {
			reviewerBundle = b
		}
	}
	agentCmd, _, err := d.agentCommandFor(reviewerBundle)
	if err != nil {
		return d.failTask(task.ID, "reviewer profile resolution failed: "+err.Error(), Cause{Kind: CauseSandbox})
	}
	spec := sandbox.RunSpec{
		TaskID:       task.ID + "-review",
		Envelope:     envJSON,
		AgentImage:   d.AgentImage,
		AgentCommand: agentCmd,
		CloneURL:     env.Git.CloneURL,
		BaseBranch:   d.BaseBranch,
		WorkBranch:   "review/" + task.ID,
		GateCommand:  "", // the reviewer's gate is the Gitea review webhook, not a job gate
		CredSecret:   d.CredentialRef,
	}
	if route.TimeoutMinutes > 0 {
		spec.Deadline = time.Duration(route.TimeoutMinutes) * time.Minute
	}
	ref, err := d.Sandbox.Allocate(ctx, spec)
	if err != nil {
		return d.failTask(task.ID, "reviewer sandbox allocation failed: "+err.Error(), Cause{Kind: CauseSandbox})
	}
	_, waitErr := d.Sandbox.Wait(ctx, &ref)
	defer func() {
		if derr := d.Sandbox.Destroy(context.WithoutCancel(ctx), ref); derr != nil {
			log.Printf("cortex: SANDBOX LEAK %s: %v", ref.String(), derr)
		}
	}()
	if waitErr != nil {
		return d.failTask(task.ID, "reviewer sandbox wait failed: "+waitErr.Error(), Cause{Kind: CauseSandbox})
	}
	// Observe the verdict as a Gitea fact via the reliable API read (review.state),
	// then DRIVE the consequence (approve/revise) from that observation rather than
	// depending on the pr.review_submitted webhook — whose payload parsing is
	// fragile across Gitea versions. The webhook route still fires for HUMAN UI
	// reviews (no dispatcher in that path); when both fire for an agent review the
	// second advance is a harmless rejected lifecycle transition.
	if d.ReviewLookup != nil {
		state, reviewer, found, lerr := d.ReviewLookup(ctx, task.PRRepo, task.PRNumber)
		if lerr != nil {
			return d.failTask(task.ID, "review lookup failed: "+lerr.Error(), Cause{Kind: CauseSandbox})
		}
		if !found {
			return d.failTask(task.ID, "no_review: reviewer exited without an observable Gitea review",
				Cause{Kind: CauseGate, Detail: map[string]any{"pr_number": task.PRNumber}})
		}
		if err := d.Store.SetReviewer(task.ID, reviewer); err != nil {
			log.Printf("cortex: ERROR persisting reviewer on %s: %v", task.ID, err)
		}
		log.Printf("cortex: review observed on %s#%d: %s by %s — advancing", task.PRRepo, task.PRNumber, state, reviewer)
		d.emit(Event{
			Type:        EventPRReviewSubmitted,
			Repo:        task.PRRepo,
			PRNumber:    task.PRNumber,
			ReviewState: state,
			Actor:       reviewer,
		})
	}
	return nil
}

// ReconcileOnStartup recovers tasks orphaned by a gateway restart: their
// sandbox Job (and thus their work) outlives the gateway, but the in-memory
// waiter goroutine that would collect the result died. For each in-flight task
// (dispatched/building) it re-attaches a waiter if the Job still exists, or
// fails it loudly if the Job is gone (no silent zombies — PERSISTENCE.md).
// Review/approved tasks are left to their external events + the timeout
// watchdog. Runs once at startup; safe to call with no active tasks.
func (d *Dispatcher) ReconcileOnStartup(ctx context.Context, cfg *Config, namespace string) {
	active, err := d.Store.ListActive()
	if err != nil {
		log.Printf("cortex: reconcile: ListActive failed: %v", err)
		return
	}
	for i := range active {
		task := active[i]
		if task.Status != StatusDispatched && task.Status != StatusBuilding {
			continue // review/approved/merged wait on webhooks + the watchdog
		}
		route := cfg.RouteByID(task.RouteID)
		if route == nil || route.Dispatch == nil {
			_ = d.failTask(task.ID, "reconcile: task's route no longer exists", Cause{Kind: CauseSandbox})
			continue
		}
		ref := sandbox.RefForTask(namespace, task.ID)
		exists, err := d.Sandbox.Exists(ctx, ref)
		if err != nil {
			log.Printf("cortex: reconcile: Exists(%s) failed: %v — leaving for the watchdog", task.ID, err)
			continue
		}
		if !exists {
			log.Printf("cortex: reconcile: task %s orphaned (sandbox gone after restart) — failing", task.ID)
			_ = d.failTaskWithEvent(ctx, route, task.ID,
				"orphaned: sandbox gone after gateway restart — retry to resume",
				Cause{Kind: CauseSandbox, Detail: map[string]any{"reconcile": true}})
			continue
		}
		// Re-attach: the Job is still around, so rebuild the envelope and wait.
		env, err := BuildEnvelope(cfg, route, EnvelopeParams{
			Task:          &task,
			CloneURL:      d.CloneURLBase + "/" + task.IssueRepo + ".git",
			BaseBranch:    d.BaseBranch,
			CredentialRef: d.CredentialRef,
		})
		if err != nil {
			_ = d.failTask(task.ID, "reconcile: envelope rebuild failed: "+err.Error(), Cause{Kind: CauseSandbox})
			continue
		}
		log.Printf("cortex: reconcile: re-attaching to in-flight task %s (%s)", task.ID, ref.String())
		routeCopy, taskCopy, envCopy := route, task, env
		go func() {
			rctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
			defer cancel()
			if err := d.awaitAndFinalize(rctx, routeCopy, &taskCopy, envCopy, ref); err != nil {
				log.Printf("cortex: reconcile: re-attach finalize %s: %v", taskCopy.ID, err)
			}
		}()
	}
}

// RunTimeoutWatchdog periodically fails any active task past its timeout_at —
// the safety net PERSISTENCE.md promises, so a task can never sit non-terminal
// forever (e.g. a stalled reviewer, or an orphan the reconciler could not
// re-attach). Blocks until ctx is done; run it in a goroutine.
func (d *Dispatcher) RunTimeoutWatchdog(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			active, err := d.Store.ListActive()
			if err != nil {
				log.Printf("cortex: watchdog: ListActive failed: %v", err)
				continue
			}
			for i := range active {
				t := active[i]
				if t.TimeoutAt == nil || now.Before(*t.TimeoutAt) {
					continue
				}
				// The build deadline bounds the SANDBOX phases only. Once a task
				// reaches review/approved/merged it is waiting on external actors —
				// the reviewer verdict and the human Lockbox merge — not a running
				// sandbox, so the build deadline must not reap it. (A build deadline
				// killed an approved task that was correctly waiting for the
				// operator's Lockbox tap.) A stuck reviewer leg is failed by
				// DispatchReviewer's own deadline, not here.
				switch t.Status {
				case StatusReview, StatusApproved, StatusMerged:
					continue
				}
				log.Printf("cortex: watchdog: task %s past timeout (%s, status %s) — failing", t.ID, t.TimeoutAt.Format(time.RFC3339), t.Status)
				if err := d.Store.Transition(t.ID, StatusFailed,
					"timeout: task exceeded its deadline (watchdog)",
					Cause{Kind: CauseTimeout, Detail: map[string]any{"watchdog": true, "was": t.Status}}); err != nil {
					// A losing race with normal finalization is fine — it's terminal now.
					log.Printf("cortex: watchdog: transition %s: %v", t.ID, err)
				}
			}
		}
	}
}
