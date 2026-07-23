# Phase 1 Execution Spec — The Walking Skeleton

**Status:** Draft, produced at the start of the v2 rebuild. Grounded in the code as
it exists on `main` (`b8c19bd`) and in HIRDFORGE_V2_PRD.md. Checkpoints are called
out inline; each returns for review before its task is implemented.

## Goal

The thinnest end-to-end vertical slice that proves the entire determinism spine
(D-SKELETON): **a labeled issue on this repo drives — with zero LLM in the
coordination path — to a merged PR, fully observable and controllable.**

```
git.hirdforge.com Gitea issue (labeled)
  → webhook → Asgard gateway → Cortex matches a route
  → dispatch ONE agent into a reset-to-clean sandbox
  → agent builds, opens a PR on git.hirdforge.com Gitea
  → MECHANICAL done-gate (test-command exit code in the sandbox)
  → reviewer route: ONE reviewer agent judges the PR DIFF
  → Lockbox approval authorizes → merge → validated → issue closed
```

This crosses what the source spec splits across Phases 1 and 3 — deliberately. We
prove the whole loop thin first, then widen (skill bundles, harness mechanisms,
retry/revise routing, fleet scaling) in later phases.

**Dogfood target (D-SKELETON):** this repo. Safety comes from the mechanical
gates, not from a toy target — the agent works only in a sandbox, never touches a
protected branch, the done-gate is a real test-command exit code, and the merge is
human-authorized via Lockbox. Those gates *are* what make an autonomous agent on
its own codebase acceptable.

## Phase 0 prerequisite (do first, no behavior change)

Rebrand per D-BRAND (module path → `git.hirdforge.com/kit/hirdforge`;
warrior→agent, warband→fleet, chieftain removed; confirm O-BRAND-NAMES), import
source specs (done), confirm `go build ./...` + existing tests green. Ships as its
own PR.

## Task 0 — the load-bearing contracts (STOP-AND-ASK, all before any P1 code)

Per CLAUDE.md stop-and-ask #2, draft each as a standalone doc, present, get
approval, *then* build. These are the spine's contracts:

- **O-ROUTING-SCHEMA** — `cortex.yaml`: event-match rules, bundles
  (`{skills, memory_scopes, profile}`), and the per-route `done_gate`
  (`ci-status | test-command | custom-validator`). The skeleton uses one route:
  label `agent:build` → build bundle → `done_gate: test-command`.
- **Dispatch envelope** — the deterministic message Cortex constructs (issue
  content + bundle + `DONE WHEN`), plus the reviewer variant and the retry variant
  (which carries failure context, D-LESSONS #2).
- **O-SANDBOX-CONTRACT** — allocate → checkout → run → collect-PR → reset/destroy;
  the native ephemeral pattern (D-SANDBOX).
- **O-PERSISTENCE** — the Postgres task-lifecycle schema (extends the A2A task
  store per the v2 Task shape).
- **Done-gate interface** — how a route's `done_gate` is evaluated mechanically
  and its result recorded as the transition reason.

**Acceptance:** all five contracts approved before Task 1.

## Tasks (ordered)

**P1.1 — Cortex core (routing + lifecycle).** A Cortex module in the gateway:
ingest a git.hirdforge.com Gitea webhook (`issue.labeled`), match the one route, record a
`queued` task in Postgres. Reuse `webhook.go` HMAC patterns (O-WEBHOOK-SECRET).
No LLM. *Acceptance: a labeled issue creates a `queued` task with a logged routing
decision; an unmatched issue logs a no-match and creates nothing.*

**P1.2 — Native sandbox.** Implement O-SANDBOX-CONTRACT: allocate a reset-to-clean
env on Asgard, check out the repo at the target branch, expose a run interface,
collect the resulting branch/PR, tear down. *Acceptance: a task can be given a
fresh sandbox and it is provably clean (no prior-task residue) and destroyed
after.*

**P1.3 — Dispatch.** Cortex dispatches one idle agent into a P1.2 sandbox with the
P1.0 dispatch envelope. Reuse the existing `/api/v1/dispatch` mechanism. Status
`queued → dispatched → building`. *Acceptance: a `queued` task dispatches exactly
one agent into a clean sandbox; the transition and its reason are observable.*

**P1.4 — Agent build → PR.** The agent runs its existing (M1-instrumented) tool
loop in the sandbox, produces a branch, opens a PR on git.hirdforge.com Gitea, reports the PR
ref back. Status → `review` only *after* the gate (P1.5). *Acceptance: the agent
opens a real PR on git.hirdforge.com Gitea from sandbox work; the PR ref lands on the task.*

**P1.5 — Mechanical done-gate.** Evaluate the route's `done_gate: test-command` —
run the repo's test command in the sandbox, trust the exit code. Green → advance;
non-zero → `failed(reason=<gate>, excerpt=<output>)`. **No model judges
completion.** (Chosen over `ci-status` to sidestep O-CI runner uncertainty; the
interface supports `ci-status` for routes that want it.) *Acceptance: a task whose
tests pass advances; a task whose tests fail goes `failed` with the excerpt as the
reason — and never advances on a model claiming success.*

**P1.6 — Reviewer route.** On the PR (post-gate), Cortex fires a reviewer route:
dispatch one reviewer agent given the **PR diff** (D-LESSONS #4 — it sees the
artifact). Verdict `approve | changes-requested`. `approve` → `approved`;
`changes-requested` → the sanctioned revise route back to a builder (D-LESSONS #3),
carrying the reviewer's feedback. The reviewer is a gate *on top of* P1.5's
mechanical gate, never instead of it. *Acceptance: a reviewer verdict is recorded
against the actual diff; `changes-requested` routes back to a build with feedback,
not a dead end.*

**P1.7 — Lockbox + merge.** `approved` requires Lockbox authorization; on approve,
merge the PR (the apply); status → `merged` → `validated` (post-merge gate) →
issue closed. *Acceptance: nothing merges without Lockbox approval; merge is the
only write to the protected branch; a rejected approval does not merge.*

**P1.8 — Observability + control (per the contract).** Wire the read endpoints
(`/tasks`, `/tasks/{id}`, `/log`, `/fleet`) and the three control verbs
(`/dispatch`, `/retry`, `/cancel`) for the slice, plus the `task.transition` /
`cortex.decision` streams. *Acceptance: the full lifecycle is observable with a
reason at every transition; retry re-dispatches with failure context; cancel tears
down the sandbox and marks failed.*

## Phase acceptance (the whole slice)

1. A labeled issue on this repo drives to a merged PR with **zero LLM-dependent
   coordination steps** — provable by code inspection of Cortex and the gates.
2. Every state transition carries a **mechanical reason**, observable via the
   contract.
3. The agent works **only in a sandbox**; the sole write to a protected branch is
   the Lockbox-authorized merge.
4. The three control verbs work; retry carries failure context.
5. Existing agent tests stay green; new behavior ships with stub-bed tests.

## Explicitly NOT in Phase 1 (widen later)

Skill/memory bundles beyond the one route (Phase 2), builder-harness M3/M6/M7
(Phase 2/3), retry-on-failure *routing* policy beyond manual retry (Phase 3),
multiple parallel agents (later), steward (Phase 4), the pipeline UI (Phase 5 —
backend + contract first, D-UISEQ), the researcher role (Phase 6). The skeleton
proves the plumbing; it does not do valuable work yet.

## Next-phase draft

At Phase 1 acceptance, draft `PHASE2_EXECUTION_SPEC.md` (skill-based dispatch +
the first builder-harness mechanisms), grounded in the code as it then exists.
