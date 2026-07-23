# Contract 3 — Sandbox Lifecycle (O-SANDBOX-CONTRACT)

**Status: APPROVED 2026-07-23 (Kit, PR #330). AMENDED (pending Kit's approval,
P1.2 PR): the gate-execution mechanism changed from exec-into-a-pause-sidecar
to k8s container sequencing — see §Interface. Rationale: pod `exec` requires
SPDY/WebSocket machinery (hand-rolled protocol or a client-go dependency);
ordered containers achieve the same guarantee — the gate runs in the same
clean pod, after the agent, on the same workspace — with plain REST and
exit codes as OS facts.**

Every agent runs in a reset-to-clean
isolated environment (D-SANDBOX: rebuild the fast ephemeral pattern natively —
hours-to-days, not months). On Asgard k8s the native translation of the
ephemeral-VM pattern is a **per-task Job**: fresh pod + fresh `emptyDir` ⇒
clean by construction, destroyed after collection.

## The lifecycle (states are mechanical facts, not claims)

```
allocate → checkout → run → collect → destroy
```

| Step | What happens | Mechanical evidence |
|---|---|---|
| **allocate** | Cortex creates a k8s Job `hf-task-<task_id>` in namespace `hirdforge-sandbox`, agent image, envelope mounted read-only, fresh `emptyDir` at `/work` | Job + pod exist; pod UID recorded on the task |
| **checkout** | Init step clones `git.clone_url` at `base_branch` into `/work/repo`, creates `work_branch` | clone exit code 0; HEAD SHA recorded |
| **run** | The agent binary runs **one-shot**: reads `/task/envelope.json`, works in `/work/repo`, pushes `work_branch`, opens the PR, exits | pod phase + container exit code |
| **collect** | After exit: Cortex queries Gitea for a PR from `work_branch`; the done-gate evaluates **inside the sandbox pod** (see DONE_GATE.md) before teardown | PR ref from Gitea API; `GateResult` |
| **destroy** | Job deleted (`ttlSecondsAfterFinished` as backstop; explicit delete on collect) | Job absent; recorded as a transition detail |

**Reset-to-clean proof** (P1.2 acceptance): the pod is new and `/work` is a
fresh `emptyDir` every time — no prior-task residue is *possible*, not merely
absent. The run step still asserts it mechanically: the entrypoint fails loudly
if `/work` is non-empty at start, and that check's result lands in the task
record.

## Isolation properties

- **Namespace:** `hirdforge-sandbox`, dedicated. (The v1 scaffold's empty
  `agent-testing` namespace idea, now actually built.)
- **ServiceAccount:** `sandbox-runner` with **zero** k8s API permissions —
  `automountServiceAccountToken: false`.
- **NetworkPolicy:** egress allowed ONLY to: `git.hirdforge.com` (clone/push/PR),
  the model fabric (LiteLLM `agent-host:4000` / lane endpoints), Seidr (when Phase 2
  turns memory on). No cluster-internal reach, no gateway access — the agent
  never calls home; mechanics observe it.
- **Pod security:** non-root (runAsUser 1000), no privilege escalation, drop
  ALL caps, seccomp RuntimeDefault — same posture as `deploy/gateway.yaml`.
- **Resources:** requests/limits set per profile (Phase 1: one default size).
- **Credentials:** a `sandbox-git-cred` secret (push-scoped Gitea token for the
  work branch) mounted by the Job — never in the envelope, never in env dumps.
- **Runtime class:** standard runc in Phase 1. gVisor/Kata is a hardening seam
  (rides Phases 1→3 per the PRD), declared via `runtimeClassName` when adopted —
  the contract doesn't change.

## Interface (what Phase-1 code implements — as amended)

```go
type Sandbox interface {
    Allocate(ctx, spec RunSpec) (Ref, error)      // ConfigMap(envelope) + Job created
    Wait(ctx, ref *Ref) (RunResult, error)        // blocks until the Job is terminal
    GateOutput(ctx, ref Ref) ([]byte, error)      // gate container log tail (≤4KiB) — the evidence
    Destroy(ctx, ref Ref) error                   // idempotent; cascades Job→pod, deletes ConfigMap
}
type RunResult struct {
    CleanCheckOK, CheckoutOK bool                 // guard container facts
    AgentExitCode, GateExitCode int               // OS facts, per container
    Phase string                                  // succeeded | failed | deadline
    StartedAt, FinishedAt time.Time
}
```

**The container sequence IS the lifecycle.** The Job's pod runs, in order:
1. init `guard-checkout` — asserts `/work` is empty (exit 90 = dirty, the
   mechanical reset-to-clean proof), clones at `base_branch`, creates the
   work branch (exit 91 = clone failure).
2. init `agent` — the one-shot agent invocation against the mounted envelope.
3. main `gate` — the route's gate command in `/work/repo`, **after** the agent
   exited, in the same clean environment the work happened in. Its exit code
   and log tail are the `GateResult` inputs (DONE_GATE.md).

`backoffLimit: 0` — a failed pod is never blindly restarted by k8s; recovery
is a Cortex retry dispatch carrying failure context (D-LESSONS #2).
Implemented in `internal/sandbox` over the repo's existing dependency-free
raw-REST k8s pattern (`cmd/gateway/cluster.go`); no client-go.

## Failure semantics (loud, never silent)

- Job pod unschedulable / image pull failure / OOM → task
  `failed(reason=sandbox:<cause>)` with the k8s condition text as excerpt.
- `Wait` timeout (route's `timeout_minutes`) → pod killed, task
  `failed(reason=timeout)`.
- `Destroy` failure → task still advances, but a `sandbox.leak` event fires
  loudly and a sweeper retries deletion — a leaked sandbox is an incident,
  not a footnote.
- `POST /tasks/{id}/cancel` (D-CONTROL) → Destroy + `failed(reason=cancelled)`.

## What this deliberately is not (Phase-1 scope)

One default sandbox size; no per-bundle images; no warm pool (allocate latency
is fine at skeleton scale); no gVisor yet. All are seams, none change the
interface.
