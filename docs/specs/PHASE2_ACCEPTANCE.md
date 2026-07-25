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

### 2. A single live dogfood drives issue → PR → gate → review → approve → merge → validated — **LIVE, COMPLETE**
**Proven end-to-end in one unbroken live run** (issue #433 → PR #434, sha-4a36c95).
The task's own lifecycle history is the evidence — every transition mechanical:

```
queued      ← matched build-on-label (deduped: one label → ONE task)
dispatched  ← sandbox allocated (profile builder)
building    ← agent container running
review      ← gate_passed: test-command exit 0; PR #434 observed
approved    ← pr.review_submitted by reviewers   (reviewer's own create-review verdict)
merged      ← pr.merged
validated   ← pr.merged  →  issue #433 auto-closed by Cortex
```

- **Dedup (#391):** one `agent:build` label → one task, every run.
- **Builder → observable PR (#423, #431):** the builder commits to the sandbox
  work branch `agent/<task>` — mechanically enforced, so create-pr's head is the
  ref the collect step observes.
- **Mechanical gate:** `gate_passed:test-command exit 0`.
- **Reviewer read-only profile (P2.7):** tools = read, git-diff, create-review,
  list-pr-files — no edit/write/exec/create-pr (prompt-injection safety).
- **Reviewer verdict (the Phase-1 `no_review` gap, closed):** `create-review` →
  APPROVED, with the review-mode completion gate engaged.
- **verdict → approved (#415):** driven from the dispatcher's reliable API
  observation, not the fragile webhook payload.
- **approved waits for the human merge (#428):** the build watchdog no longer
  reaps tasks parked at review/approved/merged — verified live (the task held at
  `approved` for hours, un-reaped, until the merge).
- **merge → validated (#427):** `pr.merged` walks approved→merged→validated.

*In production the merge is the operator's Lockbox tap (the write boundary); the
demo self-merged the throwaway helper PR to exercise the mechanism.*

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
6. **Builder pushed off the work branch (#423, #431):** it invented branches
   ("Builder/feat-*", then a TRUNCATED "agent/hf-<short>"), so a real PR existed
   where the collect step never looked → no_pr. Prompt guidance was insufficient
   for a local model, so git-commit now mechanically ignores a divergent branch
   arg when on a work branch.
7. **Lifecycle skipped `merged` (#427)** and **the build watchdog reaped
   `approved` tasks (#428)** — both surfaced by driving the merge tail, both fixed.
8. **CI wedge root-caused (#435):** the cluster has NO IPv6 egress, but mirrors
   resolve AAAA-first, so `apk`/`go`/`pip` connected over IPv6 and hung until the
   step timeout killed the job before any check ran (exitcode 143) — blocking
   merges repeatedly. Pinning A records / IPv4 precedence fixed it: `apk add`
   went from a 180s hang to ~5s. Closes O-CI-RUNNER-WEDGE.
6. **Builder "no_pr flake" was a real branch bug (#423):** the builder opened
   PRs on an invented branch (`Builder/feat-*`) instead of the sandbox work
   branch `agent/<task>`, so the collect step (which keys on the work branch)
   failed `no_pr` despite a real PR. Fixed: the builder commits to the work
   branch it is already on, and git-commit names the pushed branch. After this,
   the full loop ran clean on the first try (#424 → #425 → approved).

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
