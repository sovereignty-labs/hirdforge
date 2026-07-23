# Contract 5 — The Done-Gate Interface (D-GATE made concrete)

**Status: DRAFT, awaiting approval.** How a route's `done_gate` is evaluated
mechanically and its result recorded as the transition reason. This is the
replacement for the v1 failure (`pkg/tasklife.CheckCompletionGates` regex-matches
agent prose and nudges, `pkg/tasklife/gates.go:9` — still live in the v1 agent
loop; never called on the v2 path).

## Principle

**The gate runs the check itself; the agent's opinion is not an input.** After
the agent process exits, the gate executes against ground truth (the sandbox,
the repo, CI) regardless of what the agent claimed. A task whose agent printed
"all tests pass" but whose tests exit 1 goes `failed` — that exact scenario is
a pinned stub-bed test (P1.5 acceptance).

## Interface

```go
// One implementation per gate type; selected by route config. Pure mechanics.
type DoneGate interface {
    // Evaluate runs the mechanical check for a task. err means the gate itself
    // could not run (infra failure) — that is failed(reason=gate_error), which
    // is distinct from a clean "check ran and says no".
    Evaluate(ctx context.Context, t TaskRef, sb SandboxRef) (GateResult, error)
}

type GateResult struct {
    Type      string    // "test-command" | "ci-status" | "custom-validator"
    Passed    bool
    ExitCode  int       // command/validator gates; 0 iff Passed for those types
    Evidence  string    // ≤ 4 KiB tail of output, or the CI contexts + states
    Command   string    // what exactly ran (or which CI contexts were required)
    StartedAt time.Time
    Duration  time.Duration
}
```

`GateResult` is written verbatim into `cortex_tasks.done_gate` (last result)
and into the transition's `cause` — the *same bytes* the UI later renders as
the evidence chip (STEERING: every "done" renders its evidence).

## Gate types

**`test-command`** (the Phase-1 skeleton gate — sidesteps O-CI):
- Runs `route.done_gate.command` via `Sandbox.Exec` **inside the task's own
  sandbox pod** (same env the work happened in), with the route's
  `timeout_minutes`.
- `Passed = (exit code == 0)`. Timeout → `failed(reason=gate_timeout)` with
  partial output as evidence.

**`ci-status`** (interface supports it now; first used when O-CI is confirmed):
- Reads Gitea commit statuses for the PR head SHA
  (`GET /repos/{owner}/{repo}/commits/{sha}/status`).
- `Passed` = combined status `success` for all required contexts listed in
  `route.done_gate.contexts`. Pending → not yet evaluable (Cortex waits on the
  status webhook / re-poll); failure/error → `Passed=false`.

**`custom-validator`**:
- `route.done_gate.path` names an executable **in the repo at the work-branch
  HEAD**; runs in the sandbox via `Sandbox.Exec`; exit code decides. Evidence =
  output tail. (A validator is repo-reviewed code — it enters through the same
  PR-gated write path as everything else.)

## When the gate fires (mechanical triggers only)

```
agent exits ─▶ collect finds PR ─▶ gate.Evaluate ─▶ passed ─▶ status: review  (reason: gate_passed:<type>)
                    │                        └─▶ failed  ─▶ status: failed  (reason: gate_failed:<type>, evidence)
                    └─ no PR found ──────────────────────▶ status: failed  (reason: no_pr)
```

- The trigger is the sandbox `RunResult` (pod terminal state) — an OS fact.
- `task.gate_passed` / `task.gate_failed` are then internal Cortex events that
  the routing table consumes (`review-on-gate` fires on the former; retry
  eligibility attaches to the latter).
- The reviewer gate (P1.6) sits **on top**: `review → approved` additionally
  requires the `pr.review_submitted APPROVED` webhook, and `approved → merged`
  requires Lockbox authorization — judgment and human approval layer on the
  mechanical gate, never substitute it (D-GATE).

## Prohibitions (the determinism audit checks these)

1. No gate implementation may read the agent's transcript, final message, or
   `runOutcome` — those live in `diagnostics`, which nothing on the control
   path consumes.
2. No nudging: the gate never re-prompts an agent. A red gate is a red gate;
   recovery is retry-with-context (D-LESSONS #2), a *new* dispatch.
3. Evidence is stored even when green — an evidence-free `passed` is invalid
   and the store rejects it.
