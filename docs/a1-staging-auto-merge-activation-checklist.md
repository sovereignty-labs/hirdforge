# A1 staging auto-merge — activation checklist & wiring plan

Status: **not active.** The auto-merge gate (`maybeAutoMergeStaging` in
`cmd/agent/staging_auto_merge.go`, shipped in #318) is feature-flagged off by
default and is **not wired into any controller**. This document is the gate that
must be satisfied — every box checked and reviewed — before that wiring lands and
before the `STAGING_AUTO_MERGE` flag is ever enabled in a real environment.

It is the activation counterpart to `docs/e2e-staging-harness-contract.md`
(steps 1–5, all merged). Nothing here changes code or enables anything.

## What exists today

- `evaluateStagingGate(runOutcome)` — base eligibility: `kind=pr` + termination
  `completed` + PR reference present.
- `mergeCandidate{Outcome, ChecksGreen, ChecksSummary}` and
  `maybeAutoMergeStaging(mergeCandidate, stagingMerger)` — merges only when the
  `STAGING_AUTO_MERGE` flag is on **and** the gate is eligible **and**
  `ChecksGreen == true` **and** a concrete PR ref is derivable. Fails closed
  otherwise; never panics; logs a `staging_auto_merge` decision.
- `stagingMerger` interface + isolated `giteaStagingMerger` adapter (the only
  code that calls the Gitea merge API; base URL + token injected, no secrets).
- Flags, both **default off**: `STAGING_DRY_RUN_GATE` (log-only decision),
  `STAGING_AUTO_MERGE` (real merge).

`maybeAutoMergeStaging` is **not called by any production path**, and no code
constructs a real `giteaStagingMerger`. Enabling the flag alone does nothing.

## Required preconditions (all mandatory)

- [ ] **Rotate the Gitea credential first.** The `warband` account token used for
  registry/API access has been handled in plaintext during development and must
  be treated as **compromised**. Rotate it (and re-seal any dependent secrets)
  **before** any auto-merge wiring is activated. The auto-merge adapter must
  receive a **fresh, dedicated, least-privilege** token via injected config —
  never the shared/legacy token, never a hardcoded value.
- [ ] **Define the source of truth for `checksGreen`.** Pick one authoritative
  signal — the Gitea commit-status / combined-status API for the PR head SHA —
  and require **all required contexts green** (not merely "no failures").
  `pending`, `error`, missing, or partial status ⇒ `ChecksGreen = false` (fail
  closed). Document which contexts are *required* and how a flaky/absent context
  is treated (treated as not-green).
- [ ] **Restrict allowed repo/branch scope.** Maintain an explicit allow-list of
  `(owner, repo, base-branch)` the controller may auto-merge. Anything outside
  the list is refused and logged. No wildcards. Start with a single repo/branch.
- [ ] **Define staging branch policy.** Auto-merge targets only the designated
  staging base branch (never `main`/default directly). Specify the base branch,
  the merge method (`merge` vs `squash`), and that protected/default branches are
  out of scope for A1.
- [ ] **Dry-run observation window.** Run with `STAGING_DRY_RUN_GATE=on` and
  `STAGING_AUTO_MERGE=off` for a defined window (e.g. ≥1 week or ≥N candidates).
  Review every `staging_dry_run_gate` decision and confirm eligibility matches
  human judgement before flipping to real merge.
- [ ] **Audit log fields.** Each `staging_auto_merge` event must carry at least:
  `eligible`, `merged`, `reason`, `outcome_kind`, `termination_reason`,
  `checks_green` (+ `checks_summary`), `pr_url`/`pr_number`, `error` (on failure),
  and identifying `session_id`/`agent`/actor + target `owner/repo/index`.
  Decisions and merges must be reconstructable from logs alone.
- [ ] **Emergency disable / rollback.** A single, fast kill switch: unsetting
  `STAGING_AUTO_MERGE` halts all merges immediately (no redeploy required if the
  controller reads it live). Document who can flip it, how to revert a
  bad auto-merge (revert PR), and the on-call contact.
- [ ] **No production flag enabled by default.** Confirm `STAGING_AUTO_MERGE` is
  **not** set in any manifest, Helm values, CI config, env file, or deployment —
  it must be opt-in per environment and off everywhere until activation is
  approved.
- [ ] **No RBAC/permission expansion** unless separately and explicitly reviewed.
  The dedicated merge token must have the **minimum** scope to merge PRs in the
  allow-listed repo and nothing more. Any broadening is its own reviewed change.

## Proposed next PR sequence

Each PR is independently reviewable; the flag stays off until the final step.

1. **Checks-source helper** *(near-test-only)* — a small, isolated helper that
   reads the authoritative check status for a PR head SHA and returns
   `(checksGreen bool, summary string)`, failing closed on pending/error/missing.
   Unit-tested against an `httptest` fake; not wired in. Feeds `mergeCandidate`.
2. **Controller dry-run wiring** *(feature-flagged, dry-run only)* — wire a
   controller that builds a `mergeCandidate` (outcome from the result-extraction
   seam + checks from step 1) and calls the gate in **log-only** mode under
   `STAGING_DRY_RUN_GATE`. Still no real merge. This drives the observation
   window above.
3. **Restricted-scope activation** *(flagged, real merge, tiny blast radius)* —
   enable real merge for a **single** allow-listed repo + staging branch with a
   fresh least-privilege token, after the dry-run window is reviewed. Validate the
   audit trail and the kill switch on a real (throwaway/fixture) PR.
4. **Live A1 enablement** — widen the allow-list per policy once step 3 is proven.
   Gated on all preconditions above being checked and signed off.

## Out of scope for this document

No code changes, no flag enablement, no manifest/RBAC/CI/Seidr/prompt/routing/
deployment changes. This is the checklist; the work is the PR sequence above.
