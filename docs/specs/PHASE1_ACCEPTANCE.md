# Phase 1 Acceptance — The Walking Skeleton

**Status:** Phase-boundary review (CLAUDE.md ritual). Produced at the end of the
Phase 1 build, grounded in the code on `main` and in a live dogfood run on the
Asgard deployment (2026-07-23). Companion to `PHASE1_EXECUTION_SPEC.md`.

**One-line verdict:** the determinism spine is **built, deployed, and proven
end-to-end on production** — a labeled issue drove through routing → isolated
sandbox → autonomous agent → real PR → **mechanical gate pass** → reviewer
route, with every transition carrying a mechanical reason. The **reviewer→
approve→Lockbox→merge→validate tail is built and unit-tested but not yet
exercised in a single live run**, blocked only by local-model agent
reliability (~50% completion of the full git flow) — a Phase 2 concern, not a
plumbing defect. Honest status is marked per criterion below.

Legend: **LIVE** = demonstrated on the Asgard deployment · **TESTED** = built +
unit/stub-tested, not yet exercised live end-to-end · **PARTIAL** = see note.

---

## Phase acceptance criteria (from the execution spec §Phase acceptance)

### 1. A labeled issue drives toward a merged PR with zero LLM-dependent coordination steps — **LIVE (through the gate) / TESTED (merge tail)**
- **Live:** dispatch of issue `kit/hirdforge#359` produced task
  `hf-019f900e3da0-820b9412a7`, which advanced
  `queued → dispatched → building → review`, opening **PR #361** and passing the
  mechanical gate (`gate_passed:test-command exit 0`). Screenshot-equivalent:
  the task's lifecycle history in `GET /api/v1/cortex/tasks/{id}`.
- **Zero LLM in coordination — provable by inspection:** `internal/cortex`
  contains the entire coordination path (routing `match.go`, lifecycle
  `lifecycle.go`, dispatch `dispatcher.go`, gate evaluation). None of it calls an
  inference endpoint or reads agent prose to decide a transition; the reviewer
  verdict enters only as a Gitea `pull_request_review` webhook (a mechanical
  observation of Gitea state). The determinism-audit tests
  (`TestMatchRouteDeterministic`, `TestCheckTransitionRejectsInvalidCause`) pin
  this.
- **Merge tail (TESTED):** `approved → merged` (Lockbox-authorized), `merged →
  validated`, and issue-close are implemented and unit-tested
  (`cortex_lockbox_test.go`: wrong-secret rejected, non-approved refused,
  Gitea-failure keeps approved, happy-path merges with the Lockbox reason). Not
  yet reached in a live run because no single run has cleared the reviewer leg
  (agent reliability, below).

### 2. Every state transition carries a mechanical reason, observable via the contract — **LIVE**
- Every lifecycle write goes through `Store.Transition(taskID, to, reason,
  cause)` — there is no bare status setter (PERSISTENCE.md invariant #1,
  enforced by `TestMemStoreInvariants`/`TestPGStoreInvariants`). `cause.kind` is
  a closed mechanical set (`webhook|gate|timeout|operator|sandbox`) with no kind
  for model output.
- **Live evidence:** the observed histories carry reasons like
  `gate_passed:test-command exit 0; PR #361 observed on kit/hirdforge`,
  `no_pr: agent exited without an observable PR`, and `gate_failed:test-command
  exit 1: --- FAIL: TestGitCommitTool...` — each a mechanical cause, readable via
  `/api/v1/cortex/tasks/{id}` and streamed as `task.transition`.

### 3. The agent works only in a sandbox; the sole protected-branch write is the Lockbox-authorized merge — **LIVE (isolation) / TESTED (merge boundary)**
- **Live:** every task ran as a per-task k8s Job in the `sandbox` namespace,
  `serviceAccountName: sandbox-runner` with `automountServiceAccountToken:
  false` (zero k8s API reach), non-root, caps dropped, fresh `emptyDir`
  workspace asserted clean by the guard container. The agent pushed only its
  `agent/<task-id>` branch and opened a PR — it never touched `main`.
- **Merge boundary (TESTED):** the only code path that merges is
  `cortexExecuteMerge`, reached only via the Lockbox callback with a valid
  shared secret and re-checked preconditions (`status==approved`, observed PR).
  No agent bundle includes the `merge-pr` tool. Unit-tested; not yet live
  because no run reached `approved`.

### 4. The three control verbs work; retry carries failure context — **LIVE (dispatch, cancel) / TESTED (retry)**
- **dispatch (LIVE):** `POST /api/v1/cortex/dispatch` created labeled issues and
  drove tasks throughout the session.
- **cancel (LIVE):** `POST /tasks/{id}/cancel` cleanly cancelled the orphaned
  task `hf-019f902420bc-bd661e01e4` (`202`, transitioned to
  `failed(cancelled by operator)`, sandbox torn down).
- **retry (TESTED):** `POST /tasks/{id}/retry` re-dispatches a failed task with
  its failure context populated (mandatory — blind retry is a defect); covered
  by the dispatcher/store tests. Not yet exercised live.

### 5. Existing agent tests stay green; new behavior ships with stub-bed tests — **LIVE**
- `go build ./... && go vet ./... && go test ./...` is green on `main` at every
  merged PR (CI-enforced) and **passes offline inside the airgapped sandbox**
  (verified: `GATE_EXIT=0` on `f910516`). New coordination behavior ships with
  table-/stub-tests throughout `internal/cortex` and `internal/sandbox`; agent
  loop behavior is verified against the `inference_stub` bed.

---

## Task-by-task (P1.1–P1.8)

| Task | What it is | Status | Evidence |
|---|---|---|---|
| **P1.1** Cortex core | routing + lifecycle in Postgres | **LIVE** | 5 routes loaded on the deployed gateway; labeled issue → `queued` task + logged decision; unmatched → no-match, nothing created |
| **P1.2** Native sandbox | per-task k8s Job, reset-to-clean | **LIVE** | `Init:0/2` guard+agent containers ran; guard `checkout ok`; fresh emptyDir asserted clean |
| **P1.3** Dispatch | envelope → one agent in a sandbox | **LIVE** | `dispatched → building` with mechanical reasons; exactly one agent per task |
| **P1.4** Agent build → PR | one-shot loop opens a PR | **LIVE** | agent wrote `stringset.go`+test, committed, pushed, opened **PR #361** |
| **P1.5** Mechanical done-gate | `test-command` exit code decides | **LIVE** | `gate_passed:test-command exit 0` (PR #361); `gate_failed exit 1` caught two real non-hermetic test bugs — a model claiming success never advanced |
| **P1.6** Reviewer route | task-scoped, verdict via Gitea review | **LIVE (route) / PARTIAL (verdict)** | `matched review-on-gate → reviewer dispatched` with the PR diff; reviewer agent ran but did not submit a Gitea review (`no_review`) — same reliability ceiling as the builder |
| **P1.7** Lockbox + merge | approved → Lockbox → merge → validated | **TESTED** | `cortex_lockbox_test.go` (secret gate, precondition re-check, Gitea-failure safety); not live (no run reached `approved`) |
| **P1.8** Observability + control | read endpoints, verbs, streams | **LIVE** | `/cortex/{routes,log,tasks,tasks/{id}}` served live; dispatch+cancel live; `task.transition`/`cortex.decision` broadcast wired |

---

## What Phase 1 also had to build (not in the original spec, forced by reality)

The dogfood surfaced real integration gaps the spec did not anticipate. Each was
diagnosed mechanically and fixed:

- **Airgapped CI/cluster** — the CI runners and cluster have no public internet.
  A lean `Dockerfile.agent-sandbox` (no infra-tool downloads) with a **baked Go
  module + build cache** (`GOPROXY=off`) makes both the agent image and the
  gate's `go test` build offline.
- **Webhook delivery** — Gitea fires `issue_label` (not `issues`) for API
  label-adds, and existing hooks weren't reconciled; the gateway now consumes
  both and PATCHes existing hooks. Operator dispatch also fires the event
  directly (Gitea sends no webhook for API label-adds).
- **git-over-HTTP auth** — git ignores `.netrc`; the guard now primes the git
  credential store from the mounted `sandbox-git-cred` token (covers clone +
  push).
- **gitea ingress allowlist** — the *real* clone blocker: gitea's own ingress
  NetworkPolicy allowlisted `asgard`/`argocd`/`ingress-nginx` but not `sandbox`.
  Added (asgard-infra).
- **git-commit tool bug** — surfaced *by the gate*: the tool clobbered a working
  origin with a hostless URL when a token was resolved from the environment but
  `GiteaURL` was empty. Fixed with a regression test.
- **Restart robustness** — a rolling update orphaned a task at `building`
  (in-memory waiter died with the old gateway). Fixed permanently: a startup
  **reconciler** (re-attach surviving Jobs, fail orphans loudly) + a **timeout
  watchdog** — the "no silent zombies" guarantee the persistence contract
  promised.
- **Turn budget** — the hardcoded 40-round agent cap is now
  `--cortex-max-tool-rounds` (default 80).

---

## The one honest gap: builder-agent reliability on the full git flow

`qwen27b-worker` completes the clone→edit→commit→push→PR sequence **~50% of the
time**; the reviewer leg has the same ceiling. Raising the turn budget to 80 did
**not** fix it (a run still ended `no_pr`) — the model frequently *stops early*
believing it is done, not because it runs out of turns. This is squarely the
**builder-harness problem** the PRD scopes to Phase 2:

- The BUILDER_HARNESS benchmark scored 20/20 on both qwen lanes — but that task
  was **edit-only** (score = local file changes), and never exercised the
  git/commit/PR flow. So this reliability was genuinely unvalidated until the
  live loop exposed it.
- The fix is the M-mechanisms (esp. **M6** externalized todo + re-anchoring,
  **M2** coached errors, **M5** procedural prompt) plus an **extended benchmark**
  that scores the whole issue→PR flow, not just the edit.

This is the headline input to the Phase 2 spec.

---

## Acceptance verdict

Phase 1's goal — *prove the entire determinism spine in the smallest end-to-end
slice* — is **met**: the coordination path is deterministic and mechanical
throughout, deployed on Asgard, and demonstrated live from issue to a
gate-passed PR under review. The pieces past `review` are built and tested and
gated only by agent reliability, which is the defined first work of Phase 2.
Recommend proceeding to Phase 2 with builder reliability as the lead objective.
