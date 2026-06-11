# Deterministic e2e / staging harness contract

Status: **first contract landed** (skeleton implemented).
Scope guard: this defines the e2e contract that must hold **before** A1 staging
auto-merge is built. It does **not** add a staging branch, auto-merge, an
autonomy controller, or any deployment change.

## Why

We now have deterministic, offline session-termination tests that drive the real
`newConversationProcessor` against a stub inference server (see
`cmd/agent/inference_stub_test.go` and the `*_harness_test.go` files). Those
prove *how a run ends* (the canonical `session_termination` reasons). Before we
let a controller auto-merge agent work, we need one more guarantee: that a run
ends with a **machine-readable result** the orchestrator can act on.

## The contract

A passing agent run must satisfy all three:

1. **Runs a task** — dispatches at least one tool (real work happened).
2. **Terminates reportably** — emits a canonical `session_termination` event
   (`msg: "session_termination"`, with a `reason` from the `terminationReason`
   vocabulary in `cmd/agent/termination.go`).
3. **Produces an explicit result** — the final content carries exactly one
   orchestrator-recognizable signal, per the production
   `contentHasCompletionSignal`:
   - **PR created** — a PR URL (`…/pulls/<n>`) or `PR #<n>`
   - **FAILED** — `FAILED: <reason>`
   - **NOOP** — `NOOP: <evidence>`

For PR-requiring tasks, the existing **completion gate**
(`requestRequiresCompletionSignal` → nudge → `completion_gate_passed` /
`completion_gate_failed`) enforces signal (3); a missing signal is nudged and,
if still missing, terminates as `no_actionable_output` with
`"FAILED: completion gate exhausted"`.

### What the skeleton asserts today

`cmd/agent/e2e_harness_contract_test.go` (deterministic, offline, temp
workspace, no real repo or network):

- `TestE2EReportableResults` — for each of PR-URL, `PR #n`, `FAILED:`, `NOOP:`:
  the run dispatches a no-op probe tool, returns the result, and we assert the
  result satisfies `contentHasCompletionSignal` **and** a `session_termination`
  (`completed`) event was emitted.
- `TestE2EPRCompletionGatePasses` — a PR-requiring request plus a final message
  carrying a PR URL satisfies the completion gate (`completion_gate_passed`
  logged) and terminates `completed`.

This is the **minimum** "run a task → terminate reportably → emit PR/FAILED/NOOP"
proof, with no production change.

## Next PR sequence toward A1 staging auto-merge

Each step is independently reviewable and stays test-only/near-test-only until
the final A1 PR, which is feature-flagged.

1. **Fake gitea PR tool e2e** *(test-only)* — register a fake `create-pr`-style
   tool that "creates" a PR against a **fixture/temp git repo** and returns a PR
   URL. Assert the agent's PR-tracking path records the PR and the run reports
   it. Proves a real tool-driven PR-created outcome, still offline.

2. **Completion-gate failure e2e** *(test-only)* — PR-requiring task where the
   model never produces a signal; assert the nudge cycle runs and the run
   terminates `no_actionable_output` with the `FAILED: completion gate
   exhausted` result. Proves the unhappy path is reportable too.

3. **Result-extraction seam** *(near-test-only)* — a small exported helper that
   maps a finished run to a typed outcome `{kind: pr|failed|noop, ref}` from the
   final content + `session_termination` event. This is the API a staging
   controller consumes. Unit-tested against the e2e fixtures.

4. **Staging dry-run gate** *(feature-flagged, off by default)* — given a
   `pr` outcome on a staging branch, evaluate merge-eligibility (checks green,
   result==pr, no degraded `session_termination`) and **log** the decision
   without merging. No auto-merge yet.

5. **A1 staging auto-merge** — flip the gate from log-only to merge, behind the
   flag, with the autonomy controller wiring. Only after 1–4 are green.

### Explicitly out of scope here

Staging branch creation, auto-merge, the autonomy controller, and production
deployment changes are **not** in this PR. This document plus the skeleton test
are the contract those later PRs must satisfy.
