# CLAUDE.md — Hirdforge v2 Session Charter

You are executing the Hirdforge v2 rebuild. This file defines *how* you work; the
documents below define *what* you build. It follows the same charter model that
drove Hirdforge Workbench to a working state — because it worked.

## What Hirdforge v2 is (one paragraph)

Hirdforge is a **human-supervised autonomous multi-agent build platform**: a
deterministic router (**Cortex**) receives events (Gitea webhooks, operator
dispatch), matches declarative routing rules, and dispatches generic **agents**
into isolated reset-to-clean sandboxes to do model-driven work (build, review,
research). Each task's completion is decided by a **mechanical ground-truth gate**
(CI status, a test-command exit code, or a declared validator) — never by a model
reading a model's prose. The write boundary is a **PR merge behind Lockbox
approval**. v1 died because coordination was LLM-driven (a "chieftain" role, plus
regex-gated completion with nudges); v2's thesis is that *coordination is
infrastructure, not agency.*

## Document hierarchy (precedence order)

1. **docs/HIRDFORGE_V2_PRD.md** — plan of record. What and why. Promotes the v2
   architecture spec into the binding PRD.
2. **docs/DECISIONS.md** — closed decisions (binding; do not relitigate) and open
   items (owner + checkpoint). The four carry-forward lessons and the locked
   build calls live here.
3. **docs/specs/PHASE{N}_EXECUTION_SPEC.md** — the current phase's task breakdown
   and acceptance criteria. You draft the next one at phase end.
4. **docs/OBSERVABILITY_CONTRACT.md** — the control + observability API surface.
   Load-bearing: the UI is built against it, so it is frozen before UI work.
5. **docs/BUILDER_HARNESS.md** — the agent tool-loop reliability mechanisms
   (M1–M7), mapped to what is already built vs. remaining.
6. **docs/source/** — the two originating specs (v2 architecture, builder
   harness). Read-only provenance. Where the source and the code disagree, the
   code wins — flag the contradiction.

## Doctrine constraints (binding on every implementation choice)

- **Determinism in the coordination path.** Routing, dispatch, lifecycle
  tracking, and completion decisions live in harness code and mechanical checks,
  never in model behavior. Cortex never calls an inference endpoint. The
  compound-reliability math is the whole reason (0.8⁵ ≈ 33%); every LLM step you
  remove from coordination multiplies reliability.
- **Ground truth over self-assessment.** A task is "done" only when a mechanical
  check says so — CI status, a test-command exit code, a declared validator's
  exit code. **Never** a model judging whether the work is complete. This is the
  exact failure that killed v1 (`pkg/tasklife` regex-matched agent prose and
  nudged). Reviewer *judgment* is allowed as a gate on top of the mechanical
  one — it is never a substitute for it.
- **The write boundary is the PR merge behind Lockbox.** No agent writes to a
  protected branch. Work lands as a PR; the PR is staging, CI is the gate, merge
  is the apply, Lockbox approval authorizes the merge. Agents run in isolated
  reset-to-clean sandboxes and never touch the operator's or the platform's live
  state directly.
- **Escalate loudly, degrade never silently.** No retry, reroute, override, or
  fallback happens without an event-log entry. A human override of a gate is
  fine; a *silent* one is a doctrine violation.

## Stop-and-ask triggers — halt and ask Kit before proceeding when:

1. You would deviate from a closed decision in DECISIONS.md, or from the current
   phase spec's approach (bring the evidence that motivates the deviation).
2. You are about to design a **load-bearing contract**: the `cortex.yaml` routing
   schema, the dispatch message envelope, the completion-gate interface, the
   task-lifecycle state machine, the observability/control API, the Postgres
   persistence schema, the sandbox lifecycle contract. Draft it standalone,
   present it, wait for approval.
3. Any change to the **write path** beyond what the phase spec describes —
   anything touching what gets merged where, when, or how it is authorized.
4. You want a **new dependency** (Go module, container base, system binary) not
   already in the repo or named in a spec.
5. A task's acceptance criteria appear unachievable as specified, or two
   documents genuinely conflict.
6. You discover a source spec was wrong about the current code (update the audit
   delta and flag it).
7. Work would exceed the current phase's scope. Note it in DECISIONS open items
   or a BACKLOG note instead of doing it.

Everything else: proceed. Prefer acting within spec over asking.

## Working rules

- **Tests:** every behavior change ships with a test. The agent runtime has a
  **deterministic stub-inference test bed** (`inference_stub`) — use it; agent-loop
  behavior is verified without live models. Never delete a test to make a change
  pass; if a closed decision removes the behavior, the commit that removes it
  removes the test, citing the decision ID.
- **Determinism audit:** when you touch the coordination path (routing, dispatch,
  completion, review routing), prove the decision is mechanical. If a code path
  lets a model's free-text output decide a state transition, that is a bug of the
  v1 class — flag it, don't ship it.
- **Small PRs, one concern each**, message references the task ID (e.g. `[P1.2]`)
  and any decision ID it executes. The event of removing something (a role, a
  route, a fallback, a test) is always its own commit.
- **Branch → PR → Kit merges. Never push to `main`.** This is the repo's standing
  rule and the platform's own model; agents (including you) follow it.
- **Push discipline:** after a task's commits land and tests pass, push the branch
  to `origin` (KWS: `git.example.internal/hirdforge/hirdforge`) and open/update the PR.
  Masters live on KWS; forge.example.internal is a failover mirror.
- **Audit delta:** maintain `docs/audit/AUDIT_DELTA_V2.md` — a running list of
  source-spec statements your changes have made stale, so ground truth stays
  current without re-auditing.

## Phase-boundary ritual (how Kit's input stays minimal)

At each phase end, produce and present together:
1. **Acceptance demonstration** — evidence each phase-spec criterion is met (test
   names, command output, a short walkthrough).
2. **Updated AUDIT_DELTA_V2.md and DECISIONS.md** (open items resolved/added).
3. **Draft execution spec for the next phase** — same format as the current one,
   grounded in the code as it now exists.

Kit reviews at phase boundaries and stop-and-ask triggers. Nothing else should
require his input.

## Current assignment

Execute **docs/specs/PHASE1_EXECUTION_SPEC.md** — the walking skeleton: one agent
→ Cortex router → mechanically-gated reviewer → one PR, end to end, dogfooded on
this repo. Phases 0–6 are defined in the PRD; Phase 1 is the first build.

## Infra summary (the two-substrate split — see DECISIONS D-INFRA)

- **Git side (KWS master → forge.example.internal failover mirror):** repo, PRs, webhooks,
  CI. The platform wires into KWS Gitea (`git.example.internal`).
- **Compute side (Asgard k8s):** gateway, agents, sandboxes, Postgres, Seidr. The
  gateway *runs* on Asgard but receives webhooks *from* and opens PRs *on* KWS.
