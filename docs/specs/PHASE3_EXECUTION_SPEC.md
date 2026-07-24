# Phase 3 Execution Spec — Review & Validation Routing (DRAFT)

**Status:** Draft, produced at Phase 2 acceptance, grounded in the code on `main`
(`sha-3ecc538`) as it now exists. Checkpoints (stop-and-ask) called out inline.
Companion to `HIRDFORGE_V2_PRD.md` (Phase 3) and `BUILDER_HARNESS.md` (M7).

## Goal

Phase 2 made the builder *and* the reviewer reliable in isolation. Phase 3 makes
the **multi-leg loop between them** reliable and complete: the sanctioned revise
cycle, retry-on-failure with real failure context, and the last harness
mechanism (M7). The write boundary and coordination contracts are unchanged.

## Why (the Phase 2 evidence)

- The reviewer now submits verdicts reliably (read-only profile + procedure), and
  `approve-on-review`/`validate-on-merge` close the happy path. But the **unhappy
  paths** — REQUEST_CHANGES → revise → re-review, and gate_failed → retry — are
  wired (`revise-on-changes-requested`) yet unproven across a full multi-attempt
  live run.
- Long builder runs and revise cycles grow context; M4/M6 bound it, but M7
  (compaction) is the declared tail for tasks that outrun the window.

## Carried from Phase 2 (close these first)

- **P3.0a — Demonstrate `verdict → approved → merged → validated` in one unbroken
  live run.** Phase 2 proved every link live *except* the final advance firing in
  a single run (blocked by the builder's no_pr flake on the post-deploy dogfoods);
  the advance is unit-tested + deployed. One clean live dogfood closes it.
- **P3.0b — Builder no_pr reliability.** The builder flaked `no_pr` on ~3 of ~5
  recent live runs (model variance; agent binary unchanged from the runs that
  produced green PRs). Capture the builder agent log on a flake to confirm it is
  the model not reaching create-pr (vs a tool error), and fold any coaching into
  the harness. Benchmark breadth beyond the qwen lanes (M2–M7).

## Tasks (ordered)

**P3.1 — The full revise loop, proven live.** Drive a REQUEST_CHANGES verdict
through `revise-on-changes-requested` → builder re-dispatch on the SAME task
carrying the reviewer feedback as failure context (D-LESSONS #3) → re-review →
APPROVE. *Acceptance: a single live run shows attempt 1 rejected with specific
feedback, attempt 2 addressing it and approved, every transition mechanical; the
attempt counter and failure context are on the task record.*

**P3.2 — Retry-on-failure routing with failure context.** When the mechanical
gate fails, route back to a builder on the same task with the **gate output
excerpt** as failure context (not a blind retry), bounded by a max-attempt cap
that escalates loudly (Discord/event-log) rather than looping. *Acceptance: a
task whose gate fails re-dispatches with the failing gate excerpt in the
envelope; after N attempts it escalates to a human, event-logged.*

**P3.3 — M7 auto-compaction.** Sliding-window + summarization for the agent loop
when a task's context approaches the budget, preserving the M6 externalized plan
and the terminal-outcome contract across a compaction boundary. *Acceptance: the
four M7 stub scenarios (compact preserves plan, preserves last tool result,
preserves the task, survives a mid-tool-call boundary) behave per spec; a long
revise cycle that would have overflowed completes.*

**P3.4 — First skill-file opt-in (skill dispatch, first use).** Add a real skill
file to `hirdforge-personas` (`skills/<name>.md`) and a route/bundle that names
it, demonstrating the P2.6 mechanism live: the dispatched agent's behavior
changes with **no code change**, provable from the task record's loaded-skills.
Scope a `skill:<name>` Seidr collection as the HEAT-promotion seam. *Acceptance:
two routes with different bundles dispatch agents with different loaded skills +
memory scopes, observed live.*

## Explicitly NOT in Phase 3 (widen later)

The pipeline UI (Phase 5), the researcher role (Phase 6), automatic skill
*selection* by Cortex (would risk inference in coordination), HEAT lesson→skill
*promotion* automation (Phase 6). Phase 3 does not change the write path or the
coordination contracts.

## Phase acceptance (the whole phase)

1. A live run drives a REQUEST_CHANGES → revise → APPROVE multi-attempt loop end
   to end, mechanical at every transition.
2. A gate failure re-dispatches with failure context and escalates after the cap.
3. M7 compaction lets a context-heavy task complete without losing plan/task.
4. A skill file added to the repo changes agent behavior live, no code change.
5. Existing tests stay green; every mechanism ships stub-bed scenarios.

## Next-phase draft

At Phase 3 acceptance, draft `PHASE4_EXECUTION_SPEC.md` grounded in the code as it
then exists (per the PRD phase order).
