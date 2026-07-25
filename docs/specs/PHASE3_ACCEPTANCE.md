# Phase 3 Acceptance — Review & Validation Routing

**Status:** Phase-boundary review (CLAUDE.md ritual). Produced at the end of the
Phase 3 build, grounded in the code on `main` and in live runs on the Asgard
deployment (2026-07-25). Companion to `PHASE3_EXECUTION_SPEC.md`.

**One-line verdict:** the multi-leg loop between builder and reviewer is now
complete and bounded. The happy path was already proven at the Phase 2 boundary
(`issue → … → validated`); Phase 3 closes the **unhappy** paths — a failed gate
now retries with evidence instead of dead-ending, compaction can no longer
silently discard the task, and the skill/profile bundle is exercised by a real
skill. M7 summarization is deliberately deferred with a documented trigger.

Legend: **LIVE** = demonstrated on the Asgard deployment · **TESTED** = built +
unit/stub-tested · **DEFERRED** = explicit decision, recorded in DECISIONS.

---

## Phase acceptance criteria (from the execution spec)

### 1. A live run drives REQUEST_CHANGES → revise, mechanical at every transition — **LIVE**
The revise loop is wired, tested, and demonstrated on the deployment. Getting
there took fixing **seven** stacked defects on the review path — every one of
which would have fired the first time an operator clicked "Request changes" on an
agent PR, and each hidden by the one in front of it:

| # | Defect | Effect |
|---|--------|--------|
| #442 | all verdicts on a PR shared one dedup key (Gitea sends `action="reviewed"` for every review) | only the FIRST review on a PR was ever processed |
| #443 | unmappable verdicts hit `default: return` with no logging | a dropped verdict was indistinguishable from "no webhook arrived" |
| #444 | the event filter matched only `pull_request_review` | verdicts got a silent `200 "ignored"`; Gitea saw success, the gateway logged nothing |
| #446 | live Gitea actually sends `pull_request_rejected` / `pull_request_approved` | the header name was different from every guess; `review.type` is empty on the wire |
| #447 | `PrepareRetry` never refreshed `timeout_at` | the retry inherited attempt 1's expired deadline and the watchdog reaped it seconds after dispatch — dead on arrival |
| #448 | revise transitioned `failed→failed` | requesting changes on an already-failed task (the common case) aborted with an illegal transition |
| — | my own harness re-approved only once | a PR whose approval was dismissed by a push sat green-but-unmerged, retrying an unclearable 405 |

**Proven live on a real Gitea review** (issue #449 → PR #450, sha-6a98942,
assertions `received=1 revised=1 reaped=0`):

```
building → review      gate_passed:test-command exit 0; PR #450 observed
         → failed      changes_requested by reviewers on PR #450 — revise dispatch follows
         → dispatched → building        [attempt 2, SAME task, NOT reaped]

cortex: review webhook: event="pull_request_rejected" -> REQUEST_CHANGES on kit/hirdforge#450
cortex: matched revise-on-changes-requested: … -> revise dispatch for task … (attempt 2)

failure_context: {reason: changes_requested, prior_attempt: 1,
                  reviewer_feedback: ["P3.1: please document the empty-slice case in the doc comment."]}
```

The operator's actual review text rides the envelope into attempt 2, and
`reaped=0` confirms the refreshed deadline (#447) holds in situ.

The reviewer's feedback rides the envelope as failure context — the sanctioned
revise path, never a blind retry (D-LESSONS #3). Unit coverage:
`TestHandleEventRevisePath`, `TestReviseFromAlreadyFailedTask`,
`TestRetryRefreshesDeadline`, `TestWebhookDistinctReviewVerdictsAreNotDeduped`,
`TestWebhookSpecificReviewEventHeaderRoutes`.

*Method note:* the first attempt failed on **my** error, not the product's — I
submitted the review 30s after the PR appeared, before the gate had run, so the
task had no PR recorded yet and `FindTaskByPR` correctly found nothing. The test
now waits for `status=review` before requesting changes.

### 2. A gate failure re-dispatches with failure context and escalates after the cap — **TESTED (live route wired)**
- Before Phase 3 a failed gate **dead-ended**: the live logs showed
  `cortex: no-match: no route for event task.gate_failed` on every failure, so a
  task that failed its build simply died.
- `retry-on-gate-failed` (P3.2) now routes the failure back to a builder on the
  **same task**, carrying the gate's own output as `FailureContext.GateExcerpt`
  — evidence, never a blind retry (D-LESSONS #2).
- Bounded by `maxBuildAttempts` (3 total attempts; `Attempt` starts at 1). Past
  the cap the task **stays failed** and the decision reads
  `retry EXHAUSTED … needs a human` — escalate, never loop (D-ESCALATE).
- No routing-schema change was needed: `task.gate_failed` already admitted the
  `route` match key, and the cap is a code constant rather than a new schema
  field (the schema is a load-bearing contract).
- Tests load the **shipped** `configs/cortex.yaml`, so they also prove the live
  routing config validates: `TestGateFailedRetriesWithEvidence`,
  `TestGateFailedRetryCapEscalates`.

### 3. Context compaction cannot silently lose the task — **TESTED** / M7 summarization **DEFERRED**
- The loop always compacted (`progressiveTrim` at ~80% of the context budget),
  but it pinned only `messages[0]` (persona + procedure). On a long TASK-mode run
  it could drop `messages[1]` — **the task statement itself** — leaving the agent
  with "how to work" but not "what to do". It was also entirely silent.
- P3.3 pins the task statement alongside the system prompt, emits
  `context_compacted` telemetry, and **tells the model in-context** that N
  messages were dropped so it re-reads rather than recalls.
- Routing four call sites through one helper also reduced
  `newConversationProcessor` complexity 249 → 243.
- **Deferred:** summarization of the dropped span (true M7) — recorded as
  **O-M7-SCOPE** in DECISIONS with an explicit revisit trigger
  (`context_compacted` / `context_exhaustion` telemetry from a genuinely large
  task). Rationale: every task exercised so far is a one-function helper
  finishing in 2–25 rounds against an 80-round cap, which is no evidence about
  real multi-file work; and the pin + announcement remove the correctness risk.
- Tests: `TestCompactionPinsTaskStatement`, `TestCompactionAnnouncesTheLoss`,
  `TestCompactionNoOpUnderBudget`, `TestCompactionConversationModePinsSystemOnly`.

### 4. A skill file changes agent behaviour with no code change — **TESTED (mechanism) / opt-in live**
- The first real skill exists: `skills/hirdforge/repo-conventions.md` in the
  personas repo (quality gates, table-driven testing style, commit/PR
  conventions, the two doctrine rules) — personas PR #106.
- `build-conventions` bundle loads it and scopes Seidr memory to
  `repo:kit/hirdforge`; the `build-with-conventions` route dispatches it on the
  `agent:build-skilled` label.
- `TestSkilledBundleDispatchesDifferentKnowledge` proves the acceptance
  criterion: two routes dispatch the **same profile** (same capability) with
  **different skills and memory scopes**, provable from the task record — no code
  change, just a skill file and a bundle.
- **Deliberately additive:** `build-on-label` / `build-default` are untouched, so
  the production build path gains no new failure mode (a bundle naming skills
  fails dispatch loudly if the skills repo is unreachable — that risk stays
  opt-in until the skilled label is exercised live).

### 5. Existing tests stay green; every mechanism ships stub-bed scenarios — **LIVE**
- Full suite green (`go build ./... && go vet ./... && go test ./...`), `gocyclo`
  under the 250 gate, `gofmt`/`staticcheck` clean.

---

## Also landed in this phase (found by running the thing)

Not spec'd, but real defects the live loop surfaced and Phase 3 fixed:

- **CI was silently shipping stale gateways** — the rebuild filter omitted
  `internal/` and `config/`, so correct fixes never deployed and debugging chased
  ghosts (#411). *Lesson: verify the deployed artifact contains the change.*
- **O-CI-RUNNER-WEDGE root-caused and closed (#435).** Never flakiness: the
  cluster has no IPv6 egress while the mirrors resolve AAAA-first, so
  `apk`/`go`/`pip` hung until the step timeout killed the job before any check
  ran. Pinning A records / IPv4 precedence took `apk add` from a 180s hang to ~5s.
- **The builder pushed off the work branch** (#423, #431) — it invented branch
  names, so real PRs landed where the collect step never looked. Prompt guidance
  was insufficient for a local model; `git-commit` now enforces the work branch
  mechanically.
- **The build watchdog reaped `approved` tasks** waiting on the human Lockbox
  merge (#428), and the lifecycle skipped `merged` on the way to `validated`
  (#427).

## Verdict

Phase 3's unhappy-path work is complete: retry-with-evidence is bounded and
wired, compaction is safe and observable, and skill dispatch has a real skill
behind it. The one deliberate deferral (M7 summarization) is recorded with a
trigger rather than silently dropped. **Next: Phase 4 — Steward** (conversation →
issue creation, status queries), then Phase 5 — Pipeline UI.
