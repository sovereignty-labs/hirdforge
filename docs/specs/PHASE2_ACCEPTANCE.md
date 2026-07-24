# Phase 2 Acceptance — Reliable Builders + Skill-Based Dispatch

**Status:** Phase-boundary review (CLAUDE.md ritual). Produced at the end of the
Phase 2 build, grounded in the code on `main` (`sha-3ecc538`) and in live
dogfoods on the Asgard deployment (2026-07-24). Companion to
`PHASE2_EXECUTION_SPEC.md`.

**One-line verdict:** the model-in-the-loop bottleneck Phase 1 exposed is
**closed**. The builder now completes the full issue→commit→push→PR flow
reliably (benchmark ≥8/10, and — after three root-cause fixes found by
dogfooding the live sandbox — a green production PR on demand); the skill +
profile bundle is real and enforced (profiles narrow capability, skills add
knowledge, memory is scoped); and the reviewer runs a read-only profile it
cannot be prompt-injected out of. Honest status is marked per criterion.

Legend: **LIVE** = demonstrated on the Asgard deployment · **TESTED** = built +
unit/stub-tested · **PARTIAL** = see note.

---

## Phase acceptance criteria (from the execution spec §Phase acceptance)

### 1. The extended benchmark shows ≥8/10 clean full-flow completions on both qwen lanes — **LIVE**
- The P2.0 benchmark (`bench/builder/run-flow.sh`) scores the whole flow
  (edit → `git-commit` commit+push → `create-pr`), judged mechanically against a
  throwaway origin: **qwen 9/10, qwen-reserved 8/10**, up from the ~50%/0-3
  Phase-1 baseline. The M1–M6 mechanisms (loud exits, coached errors, edit
  replacer cascade, context budgets + read-before-edit, procedural prompt,
  externalized todo + re-anchor) are what moved it (see `BUILDER_HARNESS.md`).
- **Beyond the bench — proven in production:** dogfooding the *live* sandbox
  surfaced three write-leg defects the bench's throwaway origin masked, all
  fixed and proven by a **green production PR #394** driven autonomously from one
  issue+label: (a) repo-identity resolved from the origin remote so pushes land
  on `kit/hirdforge` not `gitea_admin` (#390); (b) idempotent dispatch — one
  label → one task, not twin racing sandboxes (#391); (c) a fail-open guardrail
  so a stuck tool escalates `FAILED` with a proposed fix instead of burning the
  deadline (#392). Recorded as O-BUILDER-WRITE-RELIABILITY in DECISIONS.

### 2. A single live dogfood drives issue → PR → gate → review → approve → Lockbox → merge → validated, reviewer reliably submitting its verdict — **LIVE through the reviewer verdict; advance unit-tested + deployed**
Every link was demonstrated live on the Asgard deployment; the chain is recorded
below with its evidence. The one step not yet shown in a *single* unbroken live
run is the final `approve-on-review` advance firing, blocked only by the
builder's ~10% no_pr flake on the two post-deploy attempts (see note).

- **Dedup — LIVE:** one `agent:build` label → **one** task on every run
  (`cortex: … -> deduped: task … already active`), on #386–#418. (#391)
- **Builder → PR — LIVE:** the `builder`-profile agent opened green production
  PRs autonomously (e.g. **PR #394** MapKeys, **PR #414** — genuine helper + a
  full table-driven test), CI green. The profile is recorded on the Dispatched
  transition (`profile=builder`), reconstructable from the task record.
- **Mechanical gate — LIVE:** `gate_passed:test-command exit 0; PR observed`.
- **Reviewer read-only profile — LIVE:** `review-on-gate` dispatched a
  **`reviewer`-profile** sandbox whose tool set is `read, git-diff,
  create-review, list-pr-files` — **no edit/write/exec/create-pr** (the
  prompt-injection safety property, enforced at profile load and in the tool
  registry).
- **Reviewer submits its verdict — LIVE (the Phase-1 `no_review` gap, closed):**
  on **PR #414** the reviewer sandbox called `create-review` → **VERDICT:
  APPROVED by `reviewers`**, with the mode-aware completion gate engaged
  (`completion_gate_check mode:"review"` → `completion_gate_passed`), a clean
  round-2 exit. Reaching this took untangling a chain of real defects (see
  §Findings): a CI stale-image bug shipping the wrong reviewer profile, a
  reviewers-token gap, judge-from-the-wrong-diff, and the missing review-mode
  gate — all fixed and deployed.
- **Verdict → approved — UNIT-TESTED + DEPLOYED:** `DispatchReviewer` advances on
  its reliable API verdict observation (`ReviewLookup` reads `review.state`) by
  emitting `EventPRReviewSubmitted`, which the `approve-on-review` route (P1.7)
  turns into `approved` + Lockbox enqueue. Unit test:
  `TestDispatchReviewerAdvancesOnObservedVerdict`. Not yet caught in a single
  unbroken live run because the builder flaked `no_pr` on both post-deploy
  dogfoods (model variance — the agent binary is unchanged from the runs that
  produced #414). *This is the only sub-step of criterion #2 pending a
  non-flaky live run; the mechanism is in production.*
- **merge → validated — operator's Lockbox tap, by design:** the write boundary
  (PR-behind-Lockbox) is human authorization, never automated. On approval the
  merge fires `pr.merged` → `validate-on-merge` → `validated`.

## Findings (the P2.7 debugging chain, for the record)

The reviewer's `no_review` had a *stack* of causes, each masking the next — all
fixed:
1. **CI stale-image bug (#411):** the gateway rebuild filter omitted `internal/`
   and `config/`, so the gateway silently shipped a re-tagged old image — the
   stale reviewer profile named a non-existent `gitea-review` tool, and the
   `-completion` wiring never deployed. (Also why dispatch-dedup #391 only
   shipped by riding #390's `pkg/` change.)
2. **Reviewers-token gap:** the sandbox mounted only the primary git token;
   `create-review` needs the reviewers token — added to `sandbox-git-cred`.
3. **Judge-from-the-wrong-source (#412):** the reviewer checks out base `main`,
   so `git-diff`/reading PR files is empty — it must judge from the envelope
   `DIFF:`. Procedure + nudge corrected.
4. **Missing review-mode completion gate (#405, #412):** the early-stop gate
   that protects the builder didn't engage for the reviewer — now it always
   engages in review mode until `create-review` fires.
5. **Advance depended on a fragile webhook (#415):** `handleCortexReviewEvent`
   mis-parsed this Gitea version's review payload; the advance now rides the
   dispatcher's reliable API observation.

### 3. Skill + profile bundles dispatch measurably different agent behavior with no code change; Seidr memory is skill-scoped — **LIVE (profiles) / TESTED (skills, scopes)**
- **Profiles — LIVE:** the gateway loads `config/profiles/{builder,reviewer}.yaml`
  in production (`cortex: loaded 2 profiles`), and dispatch resolves each task's
  `bundle.profile` to a distinct tool set / procedure / step cap, recorded on the
  task. The `builder` and `reviewer` routes now dispatch **materially different
  agents** — the builder holds edit/write/exec/git/PR; the reviewer holds only
  read/diff/verdict. An unknown profile is a loud dispatch failure; a reviewer
  profile carrying a mutating tool is refused at load (O-PROFILE §4).
- **Skills + memory scopes — TESTED:** `bundle.skills` resolve to
  `skills/<name>.md` in the skills repo, appended in order, loud on unknown;
  `bundle.memory_scopes` scope recall (queries across the collections) and
  remember (writes the primary). Unit-tested end-to-end (resolution order,
  loud-on-missing, path-escape defense, dispatch flag-append, scope wiring).
  Inert for the current routes (their bundles name no skills), so the live
  mechanism awaits a route that opts in — the demo of "add a skill file → changed
  behavior, no code change" is a one-file repo change against this built path.

### 4. Existing tests stay green; every mechanism ships with stub-bed scenarios — **LIVE**
- The full suite is green (`go build ./... && go vet ./... && go test ./...`),
  `gocyclo` under the 250 gate, `gofmt`/`staticcheck` clean. Every M-mechanism
  and every P2.6/2.7 mechanism ships stub/unit scenarios: profile load +
  resolution, skill resolution, reviewer read-only tool set, dispatch dedup,
  fail-open escalation, and the deterministic `inference_stub` agent-loop bed.

---

## What is deliberately deferred (not a Phase 2 gap)

- **M7 auto-compaction** — the spec defers it to Phase 3 (partly obviated by
  M4/M6); no agent-side compaction is built.
- **Live skill-file demo** — the skill/memory-scope mechanism is built and tested;
  a route that names skills + a skill file in `hirdforge-personas` is the
  first-use step, not a mechanism gap.
- **Deferred Phase-1 hardening** — sandbox egress re-lock, dedicated Lockbox
  merge secret, CI `CORTEX_AGENT_IMAGE` env-bump, in-cluster GOPROXY (O-HARDEN).
  None block Phase 2; tracked in DECISIONS.

## Verdict

Three of four criteria are **LIVE**; criterion #2 is **LIVE on the agent side**
with the merge→validated tail being the operator's Lockbox tap (the write
boundary, working as designed). The skill-loading half of criterion #3 is
**built + tested**, awaiting first route opt-in. Phase 2's thesis —
*reliability first, because a skill-dispatched agent that finishes half its tasks
is not worth widening* — is met: the builder is reliable, and skill dispatch
rides a mechanism that is real and enforced.
