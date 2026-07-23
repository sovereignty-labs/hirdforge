# Phase 2 Execution Spec — Reliable Builders + Skill-Based Dispatch

**Status:** Draft, produced at Phase 1 acceptance, grounded in the code on
`main` as it now exists and in the live dogfood findings (`PHASE1_ACCEPTANCE.md`).
Checkpoints (stop-and-ask) are called out inline. Companion to
`HIRDFORGE_V2_PRD.md` (Phase 2), `BUILDER_HARNESS.md`, and
`docs/source/builder-harness-spec.md`.

## Goal

Two objectives, reliability first because Phase 1 proved the plumbing but
exposed the model-in-the-loop as the bottleneck:

1. **Make the builder (and reviewer) reliably complete the full issue→PR→review
   flow on local models** — from the measured ~50% to the benchmark bar (≥8/10
   clean), by folding in the BUILDER_HARNESS mechanisms and *extending the
   benchmark to score the whole flow*, not just an edit.
2. **Dispatch skill + profile bundles per task** — the `cortex.yaml` bundle
   (`{skills, memory_scopes, profile}`) becomes real: the agent loads a
   per-task skill set and harness profile, with skill-scoped Seidr memory.

The PRD orders Phase 2 as "skill-based dispatch + harness mechanisms." The lived
order is reversed: **reliability is the lead**, because a skill-dispatched agent
that only finishes half its tasks is not yet worth widening. Skill dispatch
(P2.6–P2.7) lands once the builder is reliable.

## Why (the Phase 1 evidence)

- Live: `qwen27b-worker` opened a correct PR (#361) and passed the gate on some
  runs, but ended `no_pr` on others; the reviewer leg ended `no_review`. ~50%.
- Raising the turn cap 40→80 did **not** fix it — the model *stops early*
  believing it is done, so the lever is procedure/re-anchoring, not more turns.
- The 20/20 benchmark was **edit-only** and never exercised git/commit/PR — so
  the flow that actually breaks was unmeasured. Fixing the benchmark to measure
  it is prerequisite to fixing the reliability.

## Task 0 — the load-bearing contracts (STOP-AND-ASK, before code)

Per CLAUDE.md stop-and-ask #2, draft standalone, present, approve, then build:

- **O-PROFILE — the harness profile schema.** A *profile* is the mechanical
  configuration of the loop, distinct from *skills* (knowledge): tool set,
  context/exec budgets, step cap, exit protocol, procedural prompt, loop
  policies (todo on/off, compaction on/off). Bundles already carry a `profile`
  name (`internal/cortex/config.go`); this contract defines what a profile *is*
  and how the agent resolves one. Anticipated baseline profiles: **builder**
  (full tool set, M1–M7, "PR created" completion) and **reviewer** (read/grep/
  review-submit only — *no edit/write/exec*, a prompt-injection safety property —
  "review submitted" completion).
- **O-SKILL-BUNDLE — the skill + memory-scope loading contract.** How a bundle's
  `skills` and `memory_scopes` resolve to loaded skill files and Seidr collection
  scoping at dispatch time. Extends the existing envelope `bundle` field.

**Acceptance:** both contracts approved before P2.6/P2.7 code. (P2.1–P2.5 ride
the *existing* agent loop + stub bed and need no new contract.)

---

## Tasks (ordered) — reliability track

**P2.0 — Extend the benchmark to the full issue→PR flow (the acceptance
instrument).** Today `bench/builder/` scores an edit-only refactor. Add a
scenario that scores the whole flow against a throwaway sandbox repo: agent must
edit → `git-commit` (commit+push) → `create-pr`, and the score is *a real PR
observed on the throwaway remote with the expected diff*, evaluated
mechanically (same D-GATE principle). Wire it to the `inference_stub` bed too so
the mechanisms below get deterministic scenarios. *Acceptance: the benchmark
reports a clean-completion rate over the full flow (baseline expected ~50% on
qwen), and a stub scenario reproduces an "agent stops before create-pr" failure.*

**P2.1 — M6 Externalized plan (todo) + re-anchoring.** The biggest lever for the
early-stop failure. Add a `todo` tool (write/update `{content, status}`,
persisted per session), re-surface the current list as a synthetic
`<system-reminder>` at a cheap cadence *and* on wobble signals (repetition
near-miss, M3 rescue, two consecutive tool failures). The procedural prompt
(P2.3) instructs: tasks with >3 steps write the todo first; mark items done as
you go; the task is not complete until "PR created" is checked. M1's
abnormal-exit summary carries the todo verbatim. *Acceptance: on the P2.0
benchmark, the dispatch shows the model writing → following → completing a todo
that ends in "open PR"; abnormal exits enumerate remaining items; completion
rate improves measurably vs baseline.*

**P2.2 — M2 Coached error results.** Malformed tool calls / tool errors continue
the loop with a coached result (static coaching now; the HEAT-lesson source is a
seam) instead of a silent dead end. One coaching seam, two sources. *Acceptance:
a stub scenario with a malformed call is absorbed (loop continues with coaching)
rather than terminating; absorbed-fumble telemetry flows.*

**P2.3 — M5 Procedural prompt + model routing.** Replace identity-prose with a
builder *operating procedure* (Orient → Survey → Plan → Implement → Verify →
Deliver: branch, commit referencing the issue, **create-pr**, report PR number).
Persona identity ≤ ~15 lines; the rest is procedure. Per-model-family prompt
routing selected from the model string. *Acceptance: the builder runs on
procedure + lean persona only; completion rate does not regress (expected:
improves); the "Deliver" step reliably reaches create-pr.*

**P2.4 — M4 Context budgets & read discipline.** Read/exec output budgets (no
single tool result floods context), line numbers, read-before-edit enforcement
with staleness invalidation. *Acceptance: the four M4 stub scenarios (oversized
read, edit-without-read, huge exec output, two edits no interleaved read) behave
per spec; no truncation is model-invisible.*

**P2.5 — M3 Edit replacer cascade + rescue telemetry.** Multi-strategy edit match
(start at 3), ambiguity fails with coaching (never guesses), rescue telemetry.
*Acceptance: the multi-edit sequence the refactor is made of no longer cascades
into failure; rescue events are counted.*

**Benchmark gate (the "reliable now" proof).** The extended P2.0 benchmark:
**≥8/10 clean full-flow completions** on the `qwen`/`qwen-reserved` lanes (from
~50% baseline). This gate is the precondition for the skill-dispatch track.

---

## Tasks (ordered) — skill-dispatch track (after the benchmark gate)

**P2.6 — Skill + profile bundle dispatch.** Make the envelope's `bundle`
(`{skills, memory_scopes, profile}`) real: at dispatch, the agent loads the named
skill files (from the personas/skills repo) and resolves the harness profile
(O-PROFILE); Seidr recall/remember is scoped to the bundle's `memory_scopes`
(collection-naming convention only, no Seidr API change). *Acceptance: two routes
with different bundles dispatch agents with different loaded skills + memory
scopes, provable from the task record; a skill file added to the repo changes
agent behavior with no code change.*

**P2.7 — The reviewer profile (and profile abstraction).** Give the reviewer its
own profile: read/grep/review-submit tools only — no edit/write/exec (a reviewer
that cannot modify code cannot be prompt-injected into modifying it), smaller
step cap, review procedure that ends in `create-review`. This also fixes the
live `no_review` reliability gap via the same M-mechanisms applied to the review
flow. *Acceptance: the reviewer runs the read-only profile and reliably submits a
Gitea `APPROVE`/`REQUEST_CHANGES` on the benchmark PR; the loop reaches
`approved` → Lockbox live.*

---

## Explicitly NOT in Phase 2 (widen later)

M7 auto-compaction (Phase 3 — partly obviated by M4/M6), retry-on-failure
*routing* policy beyond manual retry (Phase 3), the researcher role (Phase 6),
the pipeline UI (Phase 5). Phase 2 does not change the write path or the
coordination contracts approved in Phase 1.

## Deferred Phase-1 hardening (fold in during Phase 2)

Tracked from the Phase 1 build; none block Phase 2 but should land opportunely:
- **Sandbox egress re-lock** — currently permissive (allow-all) to unblock the
  loop; re-tighten to `{DNS, gitea toServices, inference}` now that the gitea
  *ingress* fix is the real allowance. Re-verify with a live gate-repro.
- **Dedicated Lockbox merge secret** — stop reusing `warband-gitea-token` as the
  callback secret.
- **CI image-bump for `CORTEX_AGENT_IMAGE`** — the env value isn't rewritten by
  the CI bump regex; the sandbox agent image is pinned manually. Either teach
  the regex, or move the agent image to a value the bump covers.
- **In-cluster GOPROXY** — the baked module cache breaks if a task adds a
  dependency (offline `go test`); a cluster module proxy removes that limit.

## Phase acceptance (the whole phase)

1. The extended benchmark shows **≥8/10 clean full-flow completions** on both
   qwen lanes (the reliability proof).
2. A single live dogfood drives **issue → PR → gate → review → approve → Lockbox
   → merge → validated**, end to end, with the reviewer reliably submitting its
   verdict.
3. Skill + profile bundles dispatch measurably different agent behavior with no
   code change; Seidr memory is skill-scoped.
4. Existing tests stay green; every mechanism ships with stub-bed scenarios.

## Next-phase draft

At Phase 2 acceptance, draft `PHASE3_EXECUTION_SPEC.md` (review & validation
routing: PR-event routes, retry-on-failure routing with failure context, the
full revise loop; M7 compaction), grounded in the code as it then exists.
